package schema_test

// 本文件是 SVC-DATA-G2 ③① 的**迁移重放自证腿**：同一库连续跑两次
// `schema.Statements()`，断言第二次前后「表行数 / pg_indexes / 列集指纹」
// 逐位不变。
//
// 默认 `go test ./...` 不会连库：只读 `DATA_TEST_DSN`（复用回填套件的
// DSN 判定，库名必须以 `_test` 结尾），未设置即 `t.Skip`。设置但连不上
// 则 fail-loud，不当成「没配」。
//
// 隔离手段与回填套件相同：临时 schema + 把语句里的 `market.` / `ingest.` /
// `public.` 前缀改写成随机 schema 名，跑完 `DROP SCHEMA ... CASCADE`。
// 因此既不碰目标 `market` / `ingest`，也不碰库里已有数据。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/schema"
)

// replaySnapshot 是重放腿的三个不变式：行数、列集指纹、索引指纹。
type replaySnapshot struct {
	rows         map[string]int
	columnLines  []string
	columnDigest string
	indexLines   []string
	indexDigest  string
}

func TestSchemaReplay_TwoRunsLeaveShapeUnchanged(t *testing.T) {
	dsn, skip, err := replayDSN(os.Getenv)
	require.NoError(t, err, "DATA_TEST_DSN 已设置但无法解析")
	if skip != "" {
		t.Skip(skip)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, pool.Ping(pingCtx), "DATA_TEST_DSN 已设置但连不上（fail-loud）")

	schemaName := fmt.Sprintf("replay_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schemaName}.Sanitize()
	_, err = pool.Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})

	require.NoError(t, applyReplayStatements(ctx, pool, quoted), "第一次全量重放")

	// 哨兵行：第二次重放若把表重建/清空，行数就会变。
	_, err = pool.Exec(ctx, "INSERT INTO "+quoted+".data_migrations (name) VALUES ('replay_probe')")
	require.NoError(t, err)

	before := takeReplaySnapshot(t, ctx, pool, schemaName, quoted)
	require.NoError(t, applyReplayStatements(ctx, pool, quoted), "第二次全量重放必须无错")
	after := takeReplaySnapshot(t, ctx, pool, schemaName, quoted)

	assert.Equal(t, before.rows, after.rows, "第二次重放改动了表行数")
	assert.Equal(t, before.columnLines, after.columnLines, "第二次重放改动了列集")
	assert.Equal(t, before.columnDigest, after.columnDigest, "列集指纹必须逐位不变")
	assert.Equal(t, before.indexLines, after.indexLines, "第二次重放改动了 pg_indexes")
	assert.Equal(t, before.indexDigest, after.indexDigest, "pg_indexes 指纹必须逐位不变")
	assert.Len(t, before.rows, len(schema.TableNames()), "重放后表数必须等于 TableNames()")
	assert.Equal(t, 1, before.rows["ingest.data_migrations"], "哨兵行必须存活")
}

// replayDSN 与回填腿同一套硬约束：只读 DATA_TEST_DSN、库名必须以 `_test`
// 结尾、DSN 解析失败 fail-loud（不当成「没配」跳过）。
func replayDSN(getenv func(string) string) (dsn, skip string, err error) {
	const envName = "DATA_TEST_DSN"
	raw := strings.TrimSpace(getenv(envName))
	if raw == "" {
		return "", envName + " 未设置；重放腿只在测试库执行", nil
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse %s: %w", envName, err)
	}
	db := strings.ToLower(strings.TrimSpace(cfg.Database))
	if !strings.HasSuffix(db, "_test") {
		return "", fmt.Sprintf("拒绝重放到非 _test 库 %q（%s 必须指向 *_test 库）", cfg.Database, envName), nil
	}
	return raw, "", nil
}

// applyReplayStatements 按 `Statements()` 原顺序执行全部 DDL；`CREATE SCHEMA`
// 换成临时 schema（已建），其余前缀改写到临时 schema。
func applyReplayStatements(ctx context.Context, pool *pgxpool.Pool, quoted string) error {
	for i, stmt := range schema.Statements() {
		trimmed := strings.TrimSpace(stmt)
		if strings.HasPrefix(strings.ToUpper(trimmed), "CREATE SCHEMA") {
			continue
		}
		rewritten := strings.ReplaceAll(stmt, "market.", quoted+".")
		rewritten = strings.ReplaceAll(rewritten, "ingest.", quoted+".")
		rewritten = strings.ReplaceAll(rewritten, "public.", quoted+".")
		if _, err := pool.Exec(ctx, rewritten); err != nil {
			return fmt.Errorf("statement %d failed: %w\n%s", i+1, err, stmt)
		}
	}
	return nil
}

func takeReplaySnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schemaName, quoted string) replaySnapshot {
	t.Helper()

	rows := make(map[string]int, len(schema.TableNames()))
	for _, name := range schema.TableNames() {
		parts := strings.SplitN(name, ".", 2)
		require.Len(t, parts, 2, "TableNames() 必须带 schema 前缀：%s", name)
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT count(*) FROM "+quoted+"."+pgx.Identifier{parts[1]}.Sanitize()).Scan(&n),
			"count %s", name)
		rows[name] = n
	}

	columnRows, err := pool.Query(ctx, `SELECT table_name, ordinal_position, column_name,
			data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1
		ORDER BY table_name, ordinal_position`, schemaName)
	require.NoError(t, err)
	var columnLines []string
	for columnRows.Next() {
		var table, column, dataType, nullable, def string
		var pos int
		require.NoError(t, columnRows.Scan(&table, &pos, &column, &dataType, &nullable, &def))
		columnLines = append(columnLines,
			fmt.Sprintf("%s|%d|%s|%s|%s|%s", table, pos, column, dataType, nullable, def))
	}
	require.NoError(t, columnRows.Err())
	columnRows.Close()
	sort.Strings(columnLines)

	indexRows, err := pool.Query(ctx, `SELECT tablename, indexname, indexdef
		FROM pg_indexes WHERE schemaname = $1
		ORDER BY tablename, indexname`, schemaName)
	require.NoError(t, err)
	var indexLines []string
	for indexRows.Next() {
		var table, index, def string
		require.NoError(t, indexRows.Scan(&table, &index, &def))
		// indexdef 里含随机 schema 名，但同一用例两次快照之间不变。
		indexLines = append(indexLines, table+"|"+index+"|"+def)
	}
	require.NoError(t, indexRows.Err())
	indexRows.Close()
	sort.Strings(indexLines)

	return replaySnapshot{
		rows:         rows,
		columnLines:  columnLines,
		columnDigest: digestLines(columnLines),
		indexLines:   indexLines,
		indexDigest:  digestLines(indexLines),
	}
}

func digestLines(lines []string) string {
	h := sha256.New()
	for _, line := range lines {
		_, _ = h.Write([]byte(line))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
