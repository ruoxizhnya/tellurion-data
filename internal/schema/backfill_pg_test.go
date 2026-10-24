//go:build backfill

// 本文件是 D2 `visible_at` 历史行回填的**唯一执行入口**（`docs/db-schema.md`
// §6.5）：默认构建不编译，只有 `go test -tags backfill` 才会进来；且只认
// `DATA_TEST_DSN`（库名必须以 `_test` 结尾）——生产 DSN 自动 skip。
//
// 隔离手段是**临时 schema**：建表语句的 `market.` / `ingest.` 前缀改成随机
// schema 名，回填语句在事务里 `SET LOCAL search_path` 指过去；跑完
// `DROP SCHEMA ... CASCADE`。因此它既不碰本仓目标 `market` / `ingest`，
// 也不碰库里已有数据。
//
// 验收三集合（§6.2）：
//   - A 集 12 张：回填后无 NULL，且取值符合 §6.3/§6.4 的档位（不是 trade_date 一把梭）；
//   - B 集 2 张：只承认现成 `as_of`，回填不加 `visible_at`；
//   - C 集：明确在门外的表不得出现 `visible_at` 列。
//
// 另钉两条：哨兵防重放（第二次执行不改值；哨兵置位后新来的 NULL 行不会被
// 重放回填，新行由写面 D3 拒收）与哨兵名逐条对账。

package schema_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
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

const (
	backfillTradeDate = "2026-01-15"
	backfillIngestAt  = "2026-02-03T04:05:06Z"
	backfillAnnAt     = "2026-01-15T09:30:00+08:00"
	backfillPublishAt = "2026-01-14T23:05:00+08:00"
)

var (
	backfillIngestTime  = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	backfillCloseTime   = time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC) // 15:00 Asia/Shanghai
	backfillAnnTime     = time.Date(2026, 1, 15, 1, 30, 0, 0, time.UTC)
	backfillPublishTime = time.Date(2026, 1, 14, 15, 5, 0, 0, time.UTC)
)

// backfillSeed 是 A 集每张表的一条历史行：锚点列有值、visible_at 为 NULL。
type backfillSeed struct {
	table string
	// insert 里的 %s 是临时 schema 的引用。
	insert string
	where  string
	want   time.Time
}

func backfillSeeds() []backfillSeed {
	return []backfillSeed{
		{
			table:  "ohlcv_daily_qfq",
			insert: `INSERT INTO %s.ohlcv_daily_qfq (symbol, trade_date, open, high, low, close, volume, created_at) VALUES ('BF-DAILY', '` + backfillTradeDate + `', 1, 2, 0.5, 1.5, 100, '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-DAILY'",
			want:   backfillIngestTime, // 无 ingest_time ⇒ created_at 顶第一档
		},
		{
			table:  "ohlcv_minute",
			insert: `INSERT INTO %s.ohlcv_minute (symbol, ts, open, high, low, close, ingest_time) VALUES ('BF-MIN', '2026-01-15T09:31:00+08:00', 1, 2, 0.5, 1.5, '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-MIN'",
			want:   backfillIngestTime, // bar_period_end 算不了 ⇒ ingest_time 兜底
		},
		{
			table:  "realtime_quote",
			insert: `INSERT INTO %s.realtime_quote (symbol, ts, ingest_time) VALUES ('BF-RT', '2026-01-15T09:31:00+08:00', '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-RT'",
			want:   backfillIngestTime, // fetch_time 一族
		},
		{
			table:  "capital_flow",
			insert: `INSERT INTO %s.capital_flow (symbol, trade_date, period, ingest_time) VALUES ('BF-CF', '` + backfillTradeDate + `', 'day', '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-CF'",
			want:   backfillCloseTime, // after_close
		},
		{
			table:  "sectors",
			insert: `INSERT INTO %s.sectors (sector_code, sector_name, trade_date, ingest_time) VALUES ('BF-SEC', 'BF 板块', '` + backfillTradeDate + `', '` + backfillIngestAt + `')`,
			where:  "sector_code = 'BF-SEC'",
			want:   backfillIngestTime,
		},
		{
			table:  "stock_sector_map",
			insert: `INSERT INTO %s.stock_sector_map (symbol, sector_code, sector_name, ingest_time) VALUES ('BF-SSM', 'BF-SEC', 'BF 板块', '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-SSM'",
			want:   backfillIngestTime,
		},
		{
			table:  "top_list",
			insert: `INSERT INTO %s.top_list (trade_date, symbol, name, ingest_time) VALUES ('` + backfillTradeDate + `', 'BF-TOP', 'BF 龙虎榜', '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-TOP'",
			want:   backfillCloseTime,
		},
		{
			table:  "limit_up_pool",
			insert: `INSERT INTO %s.limit_up_pool (trade_date, symbol, name, ingest_time) VALUES ('` + backfillTradeDate + `', 'BF-LIMIT', 'BF 涨停', '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-LIMIT'",
			want:   backfillCloseTime,
		},
		{
			table:  "announcements",
			insert: `INSERT INTO %s.announcements (ann_id, symbol, ann_title, ann_time, ingest_time) VALUES ('BF-ANN', 'BF-ANN-SYM', 'BF 公告', '` + backfillAnnAt + `', '` + backfillIngestAt + `')`,
			where:  "ann_id = 'BF-ANN'",
			want:   backfillAnnTime, // announcement_day：锚点列本身
		},
		{
			table:  "news",
			insert: `INSERT INTO %s.news (news_id, symbol, title, publish_time, ingest_time) VALUES ('BF-NEWS', 'BF-NEWS-SYM', 'BF 新闻', '` + backfillPublishAt + `', '` + backfillIngestAt + `')`,
			where:  "news_id = 'BF-NEWS'",
			want:   backfillPublishTime, // publish_time
		},
		{
			table:  "hot_search",
			insert: `INSERT INTO %s.hot_search (rank, keyword, snapshot_time, ingest_time) VALUES (1, 'BF 热词', '2026-01-15T09:31:00+08:00', '` + backfillIngestAt + `')`,
			where:  "keyword = 'BF 热词'",
			want:   backfillIngestTime,
		},
		{
			table:  "global_ohlcv",
			insert: `INSERT INTO %s.global_ohlcv (symbol, trade_date, open, high, low, close, ingest_time) VALUES ('BF-GLOBAL', '` + backfillTradeDate + `', 1, 2, 0.5, 1.5, '` + backfillIngestAt + `')`,
			where:  "symbol = 'BF-GLOBAL'",
			want:   backfillIngestTime, // 境外市场：不套 A 股收盘
		},
	}
}

// applyScratchSchema 在随机 schema 里建好 market / ingest 两套表 + A 集加列。
func applyScratchSchema(t *testing.T, pool *pgxpool.Pool) (schemaName, quoted string) {
	t.Helper()
	ctx := context.Background()
	schemaName = fmt.Sprintf("backfill_test_%d", time.Now().UnixNano())
	quoted = pgx.Identifier{schemaName}.Sanitize()

	_, err := pool.Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})

	for _, stmt := range schema.MarketStatements() {
		if strings.HasPrefix(strings.TrimSpace(stmt), "CREATE SCHEMA") {
			continue
		}
		_, err := pool.Exec(ctx, strings.ReplaceAll(stmt, "market.", quoted+"."))
		require.NoError(t, err, "market DDL: %s", stmt)
	}
	for _, stmt := range schema.IngestStatements() {
		if strings.HasPrefix(strings.TrimSpace(stmt), "CREATE SCHEMA") {
			continue
		}
		_, err := pool.Exec(ctx, strings.ReplaceAll(stmt, "ingest.", quoted+"."))
		require.NoError(t, err, "ingest DDL: %s", stmt)
	}
	for _, stmt := range schema.VisibleAtStatements() {
		// public.* 那一半是今天侧的位置，临时 schema 里不需要。
		if !strings.Contains(stmt, "market.") {
			continue
		}
		_, err := pool.Exec(ctx, strings.ReplaceAll(stmt, "market.", quoted+"."))
		require.NoError(t, err, "visible_at DDL: %s", stmt)
	}
	return schemaName, quoted
}

func hasColumn(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schemaName, table, column string) bool {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		schemaName, table, column).Scan(&n))
	return n > 0
}

func tableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schemaName, table string) bool {
	t.Helper()
	var reg *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass($1)::text`,
		schemaName+"."+table).Scan(&reg))
	return reg != nil
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, quoted, table string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+quoted+"."+table).Scan(&n))
	return n
}

func visibleAtOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, quoted, table, where string) time.Time {
	t.Helper()
	var got time.Time
	require.NoError(t, pool.QueryRow(ctx, "SELECT visible_at FROM "+quoted+"."+table+" WHERE "+where).Scan(&got),
		"%s: visible_at 仍为 NULL 或不可读", table)
	return got
}

// runBackfill 逐条执行 BackfillStatements()；每条一个事务并 SET LOCAL
// search_path，模拟生产 search_path 下「表在 data 自己的 schema」的形状。
func runBackfill(t *testing.T, ctx context.Context, pool *pgxpool.Pool, quoted string) {
	t.Helper()
	for i, stmt := range schema.BackfillStatements() {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, "SET LOCAL search_path TO "+quoted)
		require.NoError(t, err)
		if _, err := tx.Exec(ctx, stmt); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("backfill statement %d failed: %v\n%s", i+1, err, stmt)
		}
		require.NoError(t, tx.Commit(ctx), "backfill statement %d commit", i+1)
	}
}

func sentinelNames(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile(`WHERE name = '([^']+)'`)
	var names []string
	for _, stmt := range schema.BackfillStatements() {
		m := re.FindStringSubmatch(stmt)
		require.Len(t, m, 2, "回填语句缺哨兵名：%s", stmt)
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}

func TestBackfillSuite_TestDatabaseOnly(t *testing.T) {
	dsn, skip, err := backfillTestDSN(os.Getenv)
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

	schemaName, quoted := applyScratchSchema(t, pool)

	// ---- 造历史行：A 集每张表一行（visible_at 为空） ----
	seeds := backfillSeeds()
	require.Len(t, seeds, len(schema.ASetTables()), "A 集每张表都要有回填用例")
	seen := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		seen[s.table] = true
		_, err := pool.Exec(ctx, fmt.Sprintf(s.insert, quoted))
		require.NoError(t, err, "seed %s", s.table)
	}
	for _, table := range schema.ASetTables() {
		require.True(t, seen[table], "A 集表 %s 没有回填用例", table)
	}

	// ---- B 集：market_activity 由本仓 DDL 建；macro_series 是 quant.macro_series
	// 在临时 schema 里的替身（本仓不拥有 quant DDL）----
	_, err = pool.Exec(ctx, "CREATE TABLE "+quoted+".macro_series (series_code TEXT PRIMARY KEY, as_of TIMESTAMPTZ NOT NULL)")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO `+quoted+`.market_activity (symbol, trade_date, source, as_of)
		VALUES ('BF-ACT', '`+backfillTradeDate+`', 'tushare', '2026-01-15T08:00:00Z')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO `+quoted+`.macro_series (series_code, as_of)
		VALUES ('BF-MACRO', '2026-01-15T08:00:00Z')`)
	require.NoError(t, err)

	// ---- 执行回填 ----
	runBackfill(t, ctx, pool, quoted)

	// ---- 验收 A 集：无 NULL，且取值逐档对得上 ----
	for _, table := range schema.ASetTables() {
		var nulls int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT count(*) FROM "+quoted+"."+table+" WHERE visible_at IS NULL").Scan(&nulls))
		assert.Zero(t, nulls, "%s: 回填后仍有 %d 行 visible_at 为 NULL", table, nulls)
		assert.Equal(t, 1, countRows(t, ctx, pool, quoted, table), "%s: 回填不许改行数", table)
	}
	for _, s := range seeds {
		got := visibleAtOf(t, ctx, pool, quoted, s.table, s.where)
		assert.True(t, s.want.Equal(got), "%s: want %s, got %s", s.table, s.want, got)
	}

	// ---- 第四档反证：after_close 三张不得等于 trade_date 的 UTC 零点 ----
	for _, table := range []string{"capital_flow", "top_list", "limit_up_pool"} {
		var asTradeDate bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT visible_at = (trade_date::timestamp AT TIME ZONE 'UTC')
			FROM `+quoted+`.`+table+` LIMIT 1`).Scan(&asTradeDate))
		assert.False(t, asTradeDate, "%s: 回填成了 trade_date 零点（§6.4 禁止的第四档）", table)
	}

	// ---- 境外行情不许套 A 股收盘 ----
	var globalAsClose bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT visible_at = ((trade_date::timestamp + interval '15 hours') AT TIME ZONE 'Asia/Shanghai')
		FROM `+quoted+`.global_ohlcv LIMIT 1`).Scan(&globalAsClose))
	assert.False(t, globalAsClose, "global_ohlcv 是境外市场，不许套 15:00 北京收盘")

	// ---- 三集合：visible_at 列**恰好**出现在 A 集 12 张上 ----
	rows, err := pool.Query(ctx, `SELECT table_name FROM information_schema.columns
		WHERE table_schema = $1 AND column_name = 'visible_at'`, schemaName)
	require.NoError(t, err)
	var withColumn []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		withColumn = append(withColumn, name)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	sort.Strings(withColumn)
	wantColumns := schema.ASetTables()
	sort.Strings(wantColumns)
	assert.Equal(t, wantColumns, withColumn, "visible_at 列只许长在 A 集上（B/C 集加了列 = 同表两个锚点）")

	// ---- B 集：承认现成 as_of，不加 visible_at ----
	bSetScratchName := map[string]string{
		"market_activity":    "market_activity",
		"quant.macro_series": "macro_series", // 临时 schema 里的替身
	}
	for _, b := range schema.BSetAnchors() {
		table, ok := bSetScratchName[b.Table]
		require.True(t, ok, "B 集成员 %s 未映射到临时 schema 表名", b.Table)
		assert.True(t, tableExists(t, ctx, pool, schemaName, table), "B 集表 %s 不存在", b.Table)
		assert.True(t, hasColumn(t, ctx, pool, schemaName, table, b.Anchor),
			"B 集表 %s 必须有现成锚点列 %s", b.Table, b.Anchor)
		assert.False(t, hasColumn(t, ctx, pool, schemaName, table, "visible_at"),
			"B 集表 %s 不许再加 visible_at", b.Table)
	}

	// B 集的 as_of 就是可见锚点：回填不许改写它。
	bAnchorWant := time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)
	for _, q := range []struct{ table, where string }{
		{"market_activity", "symbol = 'BF-ACT'"},
		{"macro_series", "series_code = 'BF-MACRO'"},
	} {
		var got time.Time
		require.NoError(t, pool.QueryRow(ctx, "SELECT as_of FROM "+quoted+"."+q.table+" WHERE "+q.where).Scan(&got))
		assert.True(t, bAnchorWant.Equal(got), "%s: 回填改动了 as_of（%s）", q.table, got)
	}

	// ---- C 集：本仓 DDL 覆盖的那些表不得出现 visible_at ----
	owned := make(map[string]bool)
	for _, name := range schema.TableNames() {
		base := strings.TrimPrefix(strings.TrimPrefix(name, "market."), "ingest.")
		owned[base] = true
	}
	checked := 0
	for _, c := range schema.CSetExemptTables() {
		base := strings.TrimPrefix(strings.TrimPrefix(c, "market."), "ingest.")
		if !owned[base] {
			// 不归本仓建（例如 stock_fundamentals），运行时表不存在；覆盖面由
			// schema_test.go 的分类测试与 docs/db-schema.md §6.3 单列条承担。
			t.Logf("C 集 %s 无本仓 DDL，运行时跳过（静态分类测试覆盖）", c)
			continue
		}
		require.True(t, tableExists(t, ctx, pool, schemaName, base), "C 集表 %s 不存在", c)
		assert.False(t, hasColumn(t, ctx, pool, schemaName, base, "visible_at"),
			"C 集表 %s 明确在门外，不许有 visible_at", c)
		checked++
	}
	require.Positive(t, checked, "C 集至少要核到一张本仓建的表")

	// ---- 哨兵：名字逐条对账，条数等于回填语句数 ----
	wantSentinels := sentinelNames(t)
	sentinelRows, err := pool.Query(ctx, "SELECT name FROM "+quoted+".data_migrations ORDER BY name")
	require.NoError(t, err)
	var gotSentinels []string
	for sentinelRows.Next() {
		var name string
		require.NoError(t, sentinelRows.Scan(&name))
		gotSentinels = append(gotSentinels, name)
	}
	require.NoError(t, sentinelRows.Err())
	sentinelRows.Close()
	assert.Equal(t, wantSentinels, gotSentinels, "哨兵必须一条回填一条，且名字对得上")

	// ---- 可重入 / 防重放：第二次执行不改值、哨兵不重复 ----
	before := make(map[string]time.Time, len(seeds))
	for _, s := range seeds {
		before[s.table] = visibleAtOf(t, ctx, pool, quoted, s.table, s.where)
	}
	runBackfill(t, ctx, pool, quoted)
	for _, s := range seeds {
		after := visibleAtOf(t, ctx, pool, quoted, s.table, s.where)
		assert.True(t, before[s.table].Equal(after), "%s: 第二次执行改动了 visible_at", s.table)
	}
	assert.Equal(t, len(wantSentinels), countRows(t, ctx, pool, quoted, "data_migrations"),
		"第二次执行不许重复写哨兵")

	// 哨兵置位后新来的 NULL 行不会被重放回填：新行由写面 D3 负责拒收。
	_, err = pool.Exec(ctx, `INSERT INTO `+quoted+`.capital_flow (symbol, trade_date, period, ingest_time)
		VALUES ('BF-CF-LATE', '2026-01-16', 'day', '`+backfillIngestAt+`')`)
	require.NoError(t, err)
	runBackfill(t, ctx, pool, quoted)
	var lateNull bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT visible_at IS NULL FROM `+quoted+`.capital_flow
		WHERE symbol = 'BF-CF-LATE'`).Scan(&lateNull))
	assert.True(t, lateNull, "哨兵已置位：回填不许重放（新行由写面 D3 拒收）")
}
