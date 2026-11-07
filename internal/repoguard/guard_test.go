package repoguard

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoCrossSchemaWritesInProductionSQL 是本仓的**静态边界护栏**（G2 ③②）：
// 生产代码里出现写语句指向仓外 schema 即红。README/文档里的示例不在扫描面内
// （只扫 Go 字符串常量，不扫注释与 Markdown）。
func TestNoCrossSchemaWritesInProductionSQL(t *testing.T) {
	root := repoRoot(t)
	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	for _, f := range findings {
		t.Errorf("跨 schema 写：%s", f)
	}
	if len(findings) > 0 {
		t.Fatalf("本仓只允许写 market. / ingest. / pg_catalog / information_schema / 临时表；发现 %d 处越界写", len(findings))
	}
}

// TestWritesInSQL_ClassifiesTargets 钉住检测器本身：写语句按 schema 分类，
// 只读 SELECT 不拦。
func TestWritesInSQL_ClassifiesTargets(t *testing.T) {
	public := "pub" + "lic"
	quant := "qu" + "ant"
	ai := "a" + "i"
	run := "r" + "un"

	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"insert quant", "INSERT INTO " + quant + ".foo (a) VALUES (1)", true},
		{"insert public", "INSERT INTO " + public + ".ohlcv_daily_qfq (symbol) VALUES ('x')", true},
		{"update ai", "UPDATE " + ai + ".factor_hypothesis SET x = 1", true},
		{"delete public", "DELETE FROM " + public + ".stocks WHERE symbol = $1", true},
		{"alter public", "ALTER TABLE IF EXISTS " + public + ".stocks ADD COLUMN x INT", true},
		{"drop run", "DROP TABLE " + run + ".batch_runs", true},
		{"truncate strategy", "TRUNCATE TABLE strategy.definitions", true},
		{"create schema risk", "CREATE SCHEMA IF NOT EXISTS risk", true},
		{"unknown schema", "INSERT INTO research.note (a) VALUES (1)", true},
		{"create index on public", "CREATE INDEX IF NOT EXISTS idx ON " + public + ".stocks (symbol)", true},
		{"market insert", "INSERT INTO market.stocks (symbol) VALUES ($1)", false},
		{"ingest create", "CREATE TABLE IF NOT EXISTS ingest.raw (content_hash TEXT)", false},
		{"ingest delete", "DELETE FROM ingest.raw WHERE content_hash = $1", false},
		{"create market schema", "CREATE SCHEMA IF NOT EXISTS market", false},
		{"update unqualified", "UPDATE sync_jobs SET status = $2 WHERE id = $1", false},
		{"insert unqualified", "INSERT INTO data_migrations (name) VALUES ($1)", false},
		{"upsert clause", "INSERT INTO market.sectors (a) VALUES (1) ON CONFLICT (a) DO UPDATE SET a = EXCLUDED.a", false},
		{"for update", "SELECT id FROM market.stocks FOR UPDATE SKIP LOCKED", false},
		{"select public", "SELECT * FROM " + public + ".stocks", false},
		{"select quant", "SELECT * FROM " + quant + ".macro_series", false},
		{"dynamic target", "INSERT INTO " + "%s" + ".ohlcv_daily_qfq (symbol) VALUES ($1)", false},
		{"visible_at transition DDL", "ALTER TABLE IF EXISTS " + public + ".", false},
		{"other public alter", "ALTER TABLE IF EXISTS " + public + ".stocks ADD COLUMN x INT", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := writesInSQL(tc.sql)
			if tc.want && len(got) == 0 {
				t.Fatalf("应判为越界写：%s", tc.sql)
			}
			if !tc.want && len(got) > 0 {
				t.Fatalf("不应判为越界写：%s ⇒ %+v", tc.sql, got)
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败，无法定位仓库根")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs %s: %v", root, err)
	}
	if !strings.HasSuffix(abs, "tellurion-data") {
		t.Fatalf("仓库根不像 tellurion-data：%s", abs)
	}
	return abs
}
