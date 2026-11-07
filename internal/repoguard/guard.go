// Package repoguard 是本仓的**静态边界护栏**（SVC-DATA-G2 ③②）。
//
// 唯一职责：扫本仓所有生产 `.go` 文件里的 SQL 字符串常量，出现写语句指向
// 非本仓 schema（`quant.` / `public.` / `run.` / `strategy.` / `risk.` /
// `analysis.` / `ai.` / `broker.`，或任何不在白名单里的 schema）即报错。
//
// 边界口径：
//   - 本仓**可以只读**上游 schema（`SELECT ... FROM quant.*` 不拦）；
//   - 本仓**只允许写** `market.` / `ingest.` / `pg_catalog` /
//     `information_schema` / 临时表（未限定 schema 的写语句）；
//   - `_test.go` 不扫描：那里的字符串多是断言或夹具，不是本仓的生产写 SQL；
//     生产 SQL 常量都在非测试文件里。
package repoguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Finding 是一条跨 schema 写命中。
type Finding struct {
	File    string
	Line    int
	Keyword string
	Target  string
	Reason  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s %s（%s）", f.File, f.Line, f.Keyword, f.Target, f.Reason)
}

// allowedSchemas 是本仓允许写的 schema（未限定 schema 的临时表另行放行）。
var allowedSchemas = map[string]struct{}{
	"market":             {},
	"ingest":             {},
	"pg_catalog":         {},
	"information_schema": {},
	"pg_temp":            {},
	"pg_toast_temp":      {},
}

// forbiddenSchemas 是点名禁止写入的仓外 schema。它们是显式黑名单；
// 其余未登记 schema 由白名单兜住。
var forbiddenSchemas = map[string]struct{}{
	"quant":    {},
	"public":   {},
	"run":      {},
	"strategy": {},
	"risk":     {},
	"analysis": {},
	"ai":       {},
	"broker":   {},
}

// writeStatement 匹配写语句的起始关键字与目标位置。
// 只匹配关键字，不跨子句；目标标识符在匹配结束后读取。
var writeStatement = regexp.MustCompile(`(?is)\b(` +
	`INSERT\s+INTO|MERGE\s+INTO|DELETE\s+FROM|TRUNCATE(?:\s+TABLE)?|UPDATE|` +
	`ALTER\s+(?:TABLE|SCHEMA|INDEX|VIEW|MATERIALIZED\s+VIEW|SEQUENCE|TYPE|FUNCTION|TRIGGER)|` +
	`DROP\s+(?:TABLE|SCHEMA|INDEX|VIEW|MATERIALIZED\s+VIEW|SEQUENCE|TYPE|FUNCTION|TRIGGER)|` +
	`CREATE\s+(?:UNLOGGED\s+)?(?:TABLE|INDEX|SCHEMA|VIEW|MATERIALIZED\s+VIEW)` +
	`)\s+(?:IF\s+(?:NOT\s+)?EXISTS\s+)?(?:ONLY\s+)?`)

// onClause 用于 CREATE/ALTER/DROP INDEX：真正的表在 `ON <schema>.<table>`。
var onClause = regexp.MustCompile(`(?is)\bON\s+(?:ONLY\s+)?`)

// keywordTargets 是 UPDATE 子句里会被误读成目标的关键字。
var keywordTargets = map[string]struct{}{
	"SET":    {},
	"SKIP":   {},
	"LOCKED": {},
}

// Scan 递归扫描 root 下的生产 `.go` 文件，返回所有跨 schema 写命中。
func Scan(root string) ([]Finding, error) {
	var findings []Finding
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			sql, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			pos := fset.Position(lit.Pos())
			for _, hit := range writesInSQL(sql) {
				findings = append(findings, Finding{
					File:    pos.Filename,
					Line:    pos.Line,
					Keyword: hit.keyword,
					Target:  hit.target,
					Reason:  hit.reason,
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findings, nil
}

type writeHit struct {
	keyword string
	target  string
	reason  string
}

// writesInSQL 返回一条 SQL 字符串里的越界写命中。
func writesInSQL(sql string) []writeHit {
	var hits []writeHit
	for _, match := range writeStatement.FindAllStringSubmatchIndex(sql, -1) {
		keyword := strings.ToUpper(strings.Join(strings.Fields(sql[match[2]:match[3]]), " "))
		rest := sql[match[1]:]

		target := readTarget(rest)
		if strings.Contains(keyword, "INDEX") {
			if loc := onClause.FindStringIndex(rest); loc != nil {
				target = readTarget(rest[loc[1]:])
			}
		}
		if target == "" {
			continue
		}
		if _, skip := keywordTargets[strings.ToUpper(target)]; skip {
			continue
		}
		// `visible_at` D1 的今天侧加列腿在代码里是 `ALTER TABLE IF EXISTS public.`
		// 与表名动态拼接，字面量只剩以点结尾的前缀。它是 `docs/db-schema.md`
		// §6.5 D1 的**登记过渡**（物理搬迁前 public 仍是今天的位置），不是业务写；
		// 只对这一条放行，其余 public 写仍红。
		if strings.EqualFold(target, "public.") && strings.HasPrefix(keyword, "ALTER TABLE") {
			continue
		}
		if reason := classify(target, strings.Contains(keyword, "SCHEMA")); reason != "" {
			hits = append(hits, writeHit{keyword: keyword, target: target, reason: reason})
		}
	}
	return hits
}

// readTarget 从语句剩余部分读第一个标识符（可带 schema 前缀 / 引号）。
func readTarget(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '_' || c == '$' || c == '.' || c == '"' {
			b.WriteByte(c)
			continue
		}
		break
	}
	return b.String()
}

// classify 判断写目标是否越界；空字符串表示放行。
func classify(target string, schemaStatement bool) string {
	lower := strings.ToLower(strings.ReplaceAll(target, `"`, ""))

	if schemaStatement {
		if _, bad := forbiddenSchemas[lower]; bad {
			return "禁止创建/修改/删除仓外 schema"
		}
		if _, ok := allowedSchemas[lower]; !ok {
			return "schema 不在本仓白名单"
		}
		return ""
	}

	i := strings.LastIndex(lower, ".")
	if i < 0 {
		// 未限定 schema：临时表或由 search_path 决定，放行。
		return ""
	}
	schemaName := lower[:i]
	if _, bad := forbiddenSchemas[schemaName]; bad {
		return "写语句指向仓外 schema " + schemaName
	}
	if _, ok := allowedSchemas[schemaName]; !ok {
		return "写语句指向非白名单 schema " + schemaName
	}
	return ""
}
