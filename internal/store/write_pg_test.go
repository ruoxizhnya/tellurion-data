package store_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/schema"
	"tellurion-data/internal/store"
)

// PG 腿：只在 DATABASE_URL 存在时跑（CI 不设 ⇒ skip，不算失败）。
//
// 隔离手段是**临时 schema**：`schema.MarketStatements()` 的 DDL 把
// `market.` 前缀替换成随机 schema 名，跑完 `DROP SCHEMA ... CASCADE`。
// 这样既不碰真实 `market`/`public`，也不依赖 D4 物理搬迁是否完成。

func pgPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PG leg")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(context.Background()))
	t.Cleanup(pool.Close)
	return pool
}

func scratchSchema(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("write_test_%d", time.Now().UnixNano())
	_, err := pool.Exec(ctx, "CREATE SCHEMA "+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+name+" CASCADE")
	})

	for _, stmt := range schema.MarketStatements() {
		if strings.HasPrefix(stmt, "CREATE SCHEMA") {
			continue
		}
		_, err := pool.Exec(ctx, strings.ReplaceAll(stmt, "market.", name+"."))
		require.NoError(t, err)
	}
	for _, stmt := range schema.VisibleAtStatements() {
		if !strings.Contains(stmt, "market.") {
			continue // public.* 那一半不属于临时 schema
		}
		_, err := pool.Exec(ctx, strings.ReplaceAll(stmt, "market.", name+"."))
		require.NoError(t, err)
	}
	return name
}

func newPGWriter(t *testing.T, pool *pgxpool.Pool, scratch string) *store.Writer {
	t.Helper()
	w, err := store.NewWriter(store.PoolWriteDB(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)
	return w
}

func pgPoint(symbol string, close float64) store.Point {
	return store.Point{
		Symbol:     symbol,
		TradeTime:  time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Source:     "tushare",
		DataType:   "ohlcv_daily",
		Data:       map[string]interface{}{"open": 1.0, "high": 2.0, "low": 0.5, "close": close, "volume": 100.0},
		IngestTime: time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
		VisibleAt:  time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC),
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// 同 hash 重写 ⇒ no-op；ingest_time 是易变列，不允许触发假冲突。
func TestPG_BulkInsert_IsIdempotentOnSameContent(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	w := newPGWriter(t, pool, scratch)
	ctx := context.Background()

	p := pgPoint("600519.SH", 1.5)
	persisted, skipped, err := w.BulkInsert(ctx, "ohlcv_daily", []store.Point{p})
	require.NoError(t, err)
	assert.Equal(t, 1, persisted)
	assert.Zero(t, skipped)

	p.IngestTime = p.IngestTime.Add(24 * time.Hour) // 重跑作业换 ingest_time
	persisted, skipped, err = w.BulkInsert(ctx, "ohlcv_daily", []store.Point{p})
	require.NoError(t, err)
	assert.Equal(t, 1, persisted, "同内容重写必须是 no-op")
	assert.Zero(t, skipped)

	assert.Equal(t, 1, countRows(t, pool, scratch+".ohlcv_daily_qfq"))
}

// 同主键异内容 ⇒ fail-loud + 整批回滚（批内先插入的另一行也不许留下）。
func TestPG_BulkInsert_ConflictingContentRollsBackWholeBatch(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	w := newPGWriter(t, pool, scratch)
	ctx := context.Background()

	base := pgPoint("600519.SH", 1.5)
	_, _, err := w.BulkInsert(ctx, "ohlcv_daily", []store.Point{base})
	require.NoError(t, err)

	newRow := pgPoint("000001.SZ", 2.0)
	conflict := pgPoint("600519.SH", 9.9) // 同主键、异内容

	_, _, err = w.BulkInsert(ctx, "ohlcv_daily", []store.Point{newRow, conflict})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrContentConflict)

	assert.Equal(t, 1, countRows(t, pool, scratch+".ohlcv_daily_qfq"),
		"整批回滚：新行不得落库")
}

// 漏 visible_at ⇒ 整批拒收，库里零行。
func TestPG_BulkInsert_RejectsBatchWithoutVisibleAt(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	w := newPGWriter(t, pool, scratch)

	p := pgPoint("600519.SH", 1.5)
	p.VisibleAt = time.Time{}
	_, _, err := w.BulkInsert(context.Background(), "ohlcv_daily", []store.Point{p})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrVisibleAtMissing)
	assert.Zero(t, countRows(t, pool, scratch+".ohlcv_daily_qfq"))
}

// B 集（daily_basic → market_activity）不在写面：必须显式拒绝，且不落任何行。
func TestPG_BulkInsert_RejectsBSetType(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	w := newPGWriter(t, pool, scratch)

	_, _, err := w.BulkInsert(context.Background(), "daily_basic", []store.Point{pgPoint("600519.SH", 1.5)})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrWriteTableNotInASet)
	assert.Zero(t, countRows(t, pool, scratch+".market_activity"))
}
