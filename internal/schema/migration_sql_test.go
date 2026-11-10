package schema_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"tellurion-data/internal/schema"
)

// TestMigrationTableListMatchesSchema 钉住 `migrations/0001_x1d_set_schema*.sql`
// 里的表清单与 `schema.TableNames()` 逐条一致。
//
// 为什么要有这条守卫：搬迁脚本是手写的 SQL，加表时只改 Go 侧（或只改 SQL）不会
// 有任何编译错误；等到生产搬完才发现某张表没搬，读面就少一张。三份脚本
// （up / down / verify）都要对得上——少一份，回滚或对拍就是瞎的。
func TestMigrationTableListMatchesSchema(t *testing.T) {
	want := append([]string(nil), schema.TableNames()...)
	sort.Strings(want)

	for _, name := range []string{
		"0001_x1d_set_schema.sql",
		"0001_x1d_set_schema.down.sql",
		"0001_x1d_set_schema.verify.sql",
	} {
		path := filepath.Join("..", "..", "migrations", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		got := extractTableList(string(raw))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s 表清单与 schema.TableNames() 不一致\n got = %v\nwant = %v", name, got, want)
		}
	}
}

// extractTableList 抓 `('market', 'stocks')` 形状的字面量并排序去重。
var tablePairRE = regexp.MustCompile(`\('(market|ingest)',\s*'([a-z_]+)'\)`)

func extractTableList(sql string) []string {
	seen := map[string]bool{}
	for _, m := range tablePairRE.FindAllStringSubmatch(sql, -1) {
		seen[m[1]+"."+m[2]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
