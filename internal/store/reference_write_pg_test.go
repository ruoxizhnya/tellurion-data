package store_test

// PG 腿：只在 DATABASE_URL 存在时跑（CI 不设 ⇒ skip）。复用 write_pg_test.go
// 的临时 schema 隔离，验证 `ReferenceWriter` 的 ON CONFLICT 语义真的能跑：
//   - 同 symbol 二次 upsert 覆盖可变列（name/status）；
//   - 源端没给的字段（industry 空 / market_cap 0）不擦掉已有值；
//   - 日历按 trade_date 覆盖 is_trading_day。
//
// 为什么需要这条：单测只钉了参数形状，SQL 本身（别名、COALESCE、ON CONFLICT
// 列是否存在）只有真库能证明。

import (
	"context"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/store"
)

func TestReferenceWriter_PG_UpsertStocksOverwritesAndPreservesUnknowns(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	writer, err := store.NewReferenceWriter(store.PoolWriteDB(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)
	ctx := context.Background()

	listDate := time.Date(1991, 4, 3, 0, 0, 0, 0, time.UTC)
	delistDate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	written, err := writer.UpsertStocks(ctx, []market.Stock{{
		Symbol: "000001.SZ", Name: "平安银行", Exchange: "SZSE",
		Industry: "银行", ListDate: listDate, DelistDate: &delistDate, Status: "active",
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, written)

	// 第二轮：名称与状态变、源端没给 industry / market_cap / delist_date。
	written, err = writer.UpsertStocks(ctx, []market.Stock{{
		Symbol: "000001.SZ", Name: "平安银行(新)", Exchange: "SZSE", Status: "delisted",
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, written)

	var name, industry, status string
	var marketCap *float64
	var storedDelist *time.Time
	err = pool.QueryRow(ctx,
		"SELECT name, industry, status, market_cap, delist_date FROM "+scratch+".stocks WHERE symbol = $1",
		"000001.SZ").Scan(&name, &industry, &status, &marketCap, &storedDelist)
	require.NoError(t, err)
	assert.Equal(t, "平安银行(新)", name, "name must be overwritten")
	assert.Equal(t, "银行", industry, "missing industry must not erase the stored value")
	assert.Equal(t, "delisted", status, "status must be overwritten")
	assert.Nil(t, marketCap, "market_cap 0 must stay NULL")
	require.NotNil(t, storedDelist, "delist_date must be persisted")
	assert.Equal(t, "2020-01-01", storedDelist.Format("2006-01-02"),
		"missing delist_date must not erase the stored value")
}

func TestReferenceWriter_PG_UpsertTradingDaysOverwritesOpenFlag(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	writer, err := store.NewReferenceWriter(store.PoolWriteDB(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)
	ctx := context.Background()

	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	_, err = writer.UpsertTradingDays(ctx, []store.TradingDay{{Date: day, Exchange: "SSE", IsTradingDay: false}})
	require.NoError(t, err)
	_, err = writer.UpsertTradingDays(ctx, []store.TradingDay{{Date: day, Exchange: "SSE", IsTradingDay: true}})
	require.NoError(t, err)

	var open bool
	var exchange string
	err = pool.QueryRow(ctx,
		"SELECT exchange, is_trading_day FROM "+scratch+".trading_calendar WHERE trade_date = $1",
		day).Scan(&exchange, &open)
	require.NoError(t, err)
	assert.Equal(t, "SSE", exchange)
	assert.True(t, open, "second upsert must overwrite is_trading_day")

	var count int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM "+scratch+".trading_calendar WHERE trade_date = $1", day).Scan(&count))
	assert.Equal(t, 1, count, "upsert must not duplicate the row")
}
