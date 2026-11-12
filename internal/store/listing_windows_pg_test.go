package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
)

// ListingWindows 的真库腿：全量 / 指定交易日两种模式，边界取 core
// `ListingWindow.IsListed` 的闭区间口径（退市日当天仍算在市）。
func TestStore_PG_ListingWindows(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	writer, err := store.NewReferenceWriter(store.PoolWriteDB(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)
	st, err := store.New(store.PoolQuerier(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)
	ctx := context.Background()

	day := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	delist := day(2021, 6, 30)

	_, err = writer.UpsertStocks(ctx, []market.Stock{
		{Symbol: "600001.SH", Name: "退市样本", Exchange: "SH", Status: "delisted",
			ListDate: day(1998, 1, 1), DelistDate: &delist},
		{Symbol: "600036.SH", Name: "招商银行", Exchange: "SH", Status: "active",
			ListDate: day(2002, 4, 9)},
		{Symbol: "301999.SZ", Name: "未来上市", Exchange: "SZ", Status: "active",
			ListDate: day(2025, 1, 1)},
		// 上市日 NULL：不做左端过滤，也不折成 1970。
		{Symbol: "000000.XX", Name: "上市日未知", Exchange: "SZ", Status: "active"},
	})
	require.NoError(t, err)

	all, err := st.ListingWindows(ctx, time.Time{})
	require.NoError(t, err)
	require.Len(t, all, 4, "全市场必须含已退市票")
	bySymbol := make(map[string]serving.ListingWindow, len(all))
	for _, w := range all {
		bySymbol[w.Symbol] = w
	}
	gone := bySymbol["600001.SH"]
	assert.True(t, gone.Delisted)
	require.NotNil(t, gone.DelistDate)
	assert.Equal(t, "2021-06-30", gone.DelistDate.Format("2006-01-02"))
	assert.Equal(t, "delisted", gone.Status)
	assert.True(t, bySymbol["000000.XX"].ListDate.IsZero(), "NULL list_date 必须是零值，不能折成 1970")
	assert.False(t, bySymbol["600036.SH"].Delisted)
	assert.Nil(t, bySymbol["600036.SH"].DelistDate)

	// 退市日当天仍算在市。
	onDelist, err := st.ListingWindows(ctx, delist)
	require.NoError(t, err)
	assert.Len(t, onDelist, 3, "退市日当天：600001.SH + 600036.SH + 000000.XX")

	// 退市次日剔除退市票。
	after, err := st.ListingWindows(ctx, day(2021, 7, 1))
	require.NoError(t, err)
	assert.Len(t, after, 2)

	// 上市日之前不出现（未来上市票在 2019 年不在池）。
	before, err := st.ListingWindows(ctx, day(2019, 12, 31))
	require.NoError(t, err)
	assert.Len(t, before, 3, "2019 年：600001.SH + 600036.SH + 000000.XX")
	for _, w := range before {
		assert.NotEqual(t, "301999.SZ", w.Symbol)
	}
}
