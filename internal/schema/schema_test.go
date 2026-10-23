package schema

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A 集必须**恰好**是这 12 张：多一张 = 把 C 集偷放进门里；少一张 = 门有洞。
func TestASet_IsExactlyTheTwelveDeclaredTables(t *testing.T) {
	require.Equal(t, []string{
		"ohlcv_daily_qfq", "ohlcv_minute", "realtime_quote", "capital_flow",
		"sectors", "stock_sector_map", "top_list", "limit_up_pool",
		"announcements", "news", "hot_search", "global_ohlcv",
	}, ASetTables())
}

// 每个 A 集成员都要有两条加列语句（market.\* 目标 + public.\* 今天），
// 且列**可空**：出现 NOT NULL 或 DEFAULT 就是把「不知道」写成一个值。
func TestVisibleAt_AddsNullableColumnForEveryASetTable(t *testing.T) {
	stmts := VisibleAtStatements()
	require.Len(t, stmts, len(ASetTables())*2)

	for _, table := range ASetTables() {
		target := "ALTER TABLE IF EXISTS market." + table + " ADD COLUMN IF NOT EXISTS visible_at TIMESTAMPTZ"
		today := "ALTER TABLE IF EXISTS public." + table + " ADD COLUMN IF NOT EXISTS visible_at TIMESTAMPTZ"
		assert.Contains(t, stmts, target, "market.* 目标侧缺列：%s", table)
		assert.Contains(t, stmts, today, "public.* 今天侧缺列：%s", table)
	}
	for _, s := range stmts {
		assert.NotContains(t, strings.ToUpper(s), "NOT NULL",
			"visible_at 必须可空（§6.5 D1）：%s", s)
		assert.NotContains(t, strings.ToUpper(s), "DEFAULT",
			"visible_at 不许设默认值（默认值 = 把不知道写成一个时刻）：%s", s)
	}
}

// B 集必须被**明确承认为** as_of，而不是「看起来像」；且不许给它加新列。
// 两条反证腿：① 表名必须出现在豁免/承认表里；② 不得出现在加列语句中。
func TestBSet_RecognisesExistingAsOfAndNeverAddsAColumn(t *testing.T) {
	anchors := BSetAnchors()
	require.Len(t, anchors, 2)
	assert.Equal(t, "market_activity", anchors[0].Table)
	assert.Equal(t, "as_of", anchors[0].Anchor)
	assert.Equal(t, "daily_basic", anchors[0].DataType)
	assert.Equal(t, "quant.macro_series", anchors[1].Table)
	assert.Equal(t, "as_of", anchors[1].Anchor)
	assert.Equal(t, "macro_series", anchors[1].DataType)

	for _, s := range VisibleAtStatements() {
		for _, b := range anchors {
			assert.NotContains(t, s, b.Table, "B 集不许加列（同表两个锚点必漂）：%s", b.Table)
		}
	}
}

// C 集的每条豁免都必须带理由——只列名字等于把「忘了」伪装成「豁免」。
func TestCSet_EveryExemptionCarriesAReason(t *testing.T) {
	require.NotEmpty(t, cSetExemptions)
	for _, e := range cSetExemptions {
		assert.NotEmpty(t, strings.TrimSpace(e.Reason), "%s 的豁免理由不能为空", e.Table)
	}

	exempt := CSetExemptTables()
	assert.Contains(t, exempt, "stock_fundamentals")
	assert.Contains(t, exempt, "market.index_daily")
	assert.Contains(t, exempt, "ingest.raw")

	// 三个集合互不重叠：同一张表既在门外又要加列，说明口径自相矛盾。
	for _, c := range exempt {
		for _, a := range ASetTables() {
			assert.NotEqual(t, c, "market."+a)
			assert.NotEqual(t, c, a)
		}
	}
}

// 回填的**第一条破坏验证**（§6.4 禁止的第四档）：任何一条回填都不许
// `SET visible_at = trade_date` 一把梭让门变绿。
func TestBackfill_NeverAssignsVisibleAtFromTradeDateAlone(t *testing.T) {
	bare := regexp.MustCompile(`(?i)SET\s+visible_at\s*=\s*trade_date\b`)
	for _, s := range BackfillStatements() {
		assert.False(t, bare.MatchString(s),
			"禁止 `SET visible_at = trade_date` 一把梭（§6.4 第四档）；原文：%s", s)
	}
}

// 回填必须是防重放 + 可重入的：缺哨兵 = 可被重复执行（Migration 052/054 的教训）。
func TestBackfill_IsSentinelGuardedAndReentrant(t *testing.T) {
	stmts := BackfillStatements()
	require.Len(t, stmts, len(ASetTables()), "A 集每张表都要有一条回填腿")

	names := make(map[string]struct{}, len(stmts))
	for _, s := range stmts {
		assert.Contains(t, s, "INSERT INTO data_migrations", "缺哨兵：%s", s)
		assert.Contains(t, s, "WHERE visible_at IS NULL", "不可重入：%s", s)
		assert.Contains(t, s, "ON CONFLICT (name) DO NOTHING")
		name := sentinelName(s)
		require.NotEmpty(t, name)
		_, dup := names[name]
		assert.False(t, dup, "哨兵名重复：%s", name)
		names[name] = struct{}{}
	}
}

// after_close 的换算必须显式带时区与 15 小时偏移：写成「当日 UTC 零点」
// 等于北京 08:00，比收盘早 7 小时（前视方向，§6.3 原文）。
func TestBackfill_AfterCloseUsesExplicitShanghaiClose(t *testing.T) {
	want := `(trade_date::timestamp + interval '15 hours') AT TIME ZONE 'Asia/Shanghai'`
	for _, s := range BackfillStatements() {
		if !strings.Contains(s, "capital_flow") && !strings.Contains(s, "top_list") &&
			!strings.Contains(s, "limit_up_pool") {
			continue
		}
		assert.Contains(t, s, want, "after_close 必须走显式 15:00 北京换算：%s", s)
	}
}

// 境外行情不许套 A 股收盘时刻：一眼能认出是 global_ohlcv 那条腿。
func TestBackfill_GlobalOHLCVDoesNotUseAShareClose(t *testing.T) {
	for _, s := range BackfillStatements() {
		if !strings.Contains(s, "global_ohlcv") {
			continue
		}
		assert.NotContains(t, s, "15 hours", "global_ohlcv 是境外市场，A 股收盘对它不成立：%s", s)
		assert.Contains(t, s, "ingest_time")
		return
	}
	t.Fatal("global_ohlcv 必须有回填腿")
}

// 建表清单与 DDL 必须互相对账：新增表忘了登记 → 红。
func TestTableNames_MatchCreateStatements(t *testing.T) {
	declared := make(map[string]struct{})
	for _, n := range TableNames() {
		declared[n] = struct{}{}
	}

	created := make(map[string]struct{})
	for _, s := range Statements() {
		name := createTableName(s)
		if name == "" {
			continue
		}
		created[name] = struct{}{}
	}

	for name := range created {
		_, ok := declared[name]
		assert.True(t, ok, "DDL 建了 %s 但 TableNames() 没登记", name)
	}
	for name := range declared {
		_, ok := created[name]
		assert.True(t, ok, "TableNames() 声明了 %s 但 DDL 没建", name)
	}
}

// 建表语句必须幂等：空库一次跑完的前提是每条都能重入。
func TestStatements_AreIdempotent(t *testing.T) {
	for _, s := range Statements() {
		upper := strings.ToUpper(s)
		switch {
		case strings.HasPrefix(upper, "CREATE SCHEMA"):
			assert.Contains(t, upper, "IF NOT EXISTS", s)
		case strings.HasPrefix(upper, "CREATE TABLE"):
			assert.Contains(t, upper, "IF NOT EXISTS", s)
		case strings.HasPrefix(upper, "CREATE INDEX"):
			assert.Contains(t, upper, "IF NOT EXISTS", s)
		case strings.HasPrefix(upper, "ALTER TABLE"):
			assert.Contains(t, upper, "IF EXISTS", s)
			assert.Contains(t, upper, "IF NOT EXISTS", s)
		default:
			t.Fatalf("未分类的语句（新增语句类型必须在本测试里登记判据）：%s", s)
		}
	}
}

// visible_at 不得被塞进建表语句里「顺手加一列」——它必须走独立的加列腿，
// 否则目标侧与今天侧的列会各自长出来、迟早漂。
func TestStatements_DoNotInlineVisibleAtIntoCreateTable(t *testing.T) {
	for _, s := range Statements() {
		if !strings.Contains(strings.ToUpper(s), "CREATE TABLE") {
			continue
		}
		assert.NotContains(t, s, "visible_at", "visible_at 只走 VisibleAtStatements()：%s", s)
	}
}

func sentinelName(stmt string) string {
	const marker = "WHERE name = '"
	i := strings.Index(stmt, marker)
	if i < 0 {
		return ""
	}
	rest := stmt[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func createTableName(stmt string) string {
	const marker = "CREATE TABLE IF NOT EXISTS "
	i := strings.Index(stmt, marker)
	if i < 0 {
		return ""
	}
	rest := stmt[i+len(marker):]
	j := strings.IndexAny(rest, " (\n\t")
	if j < 0 {
		return ""
	}
	return rest[:j]
}
