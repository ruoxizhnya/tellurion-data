package store_test

// 复权基准账本（ADR-0011）的 PG 腿：append-only、bootstrap 每 symbol 唯一、
// previous_* 可审计、LatestTradeDate 读的是 qfq 表的 MAX(trade_date)。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/store"
)

func TestPG_AdjustBaselineStore_AppendOnlyAndBootstrapUniqueness(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	ctx := context.Background()
	s, err := store.NewAdjustBaselineStore(store.PoolBaselineDB(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)

	_, _, ok, err := s.Load(ctx, "600519.SH")
	require.NoError(t, err)
	assert.False(t, ok, "空账本必须返回 ok=false 而不是错误")

	bootstrap := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.Save(ctx, "600519.SH", bootstrap, 2.0, nil, nil, "bootstrap"))

	gotDate, gotFactor, ok, err := s.Load(ctx, "600519.SH")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, bootstrap, gotDate)
	assert.Equal(t, 2.0, gotFactor)

	// 并发首拉语义：第二条 bootstrap 必须被局部唯一索引吞掉。
	require.NoError(t, s.Save(ctx, "600519.SH",
		time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), 5.0, nil, nil, "bootstrap"))
	_, gotFactor, _, err = s.Load(ctx, "600519.SH")
	require.NoError(t, err)
	assert.Equal(t, 2.0, gotFactor, "bootstrap 只许一个赢家")

	// 人工改基准：追加一条带 previous_* 的记录。
	prevDate := bootstrap
	prevFactor := 2.0
	manual := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.Save(ctx, "600519.SH", manual, 4.0, &prevDate, &prevFactor, "manual"))

	gotDate, gotFactor, ok, err = s.Load(ctx, "600519.SH")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, manual, gotDate)
	assert.Equal(t, 4.0, gotFactor)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM "+scratch+".adjust_baseline_changes WHERE symbol = $1", "600519.SH").Scan(&n))
	assert.Equal(t, 2, n, "账本 append-only：bootstrap 1 条 + manual 1 条")

	var prevD time.Time
	var prevF float64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT previous_date, previous_factor FROM `+scratch+`.adjust_baseline_changes
		 WHERE symbol = $1 AND reason = 'manual'`, "600519.SH").Scan(&prevD, &prevF))
	assert.Equal(t, bootstrap, prevD)
	assert.Equal(t, 2.0, prevF)
}

func TestPG_AdjustBaselineStore_LatestTradeDate(t *testing.T) {
	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	ctx := context.Background()
	s, err := store.NewAdjustBaselineStore(store.PoolBaselineDB(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)

	_, ok, err := s.LatestTradeDate(ctx, "600519.SH")
	require.NoError(t, err)
	assert.False(t, ok, "无数据必须 ok=false（不是零值日期）")

	w := newPGWriter(t, pool, scratch)
	p := pgPoint("600519.SH", 1.5)
	_, _, err = w.BulkInsert(ctx, "ohlcv_daily", []store.Point{p})
	require.NoError(t, err)

	latest, ok, err := s.LatestTradeDate(ctx, "600519.SH")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), latest)
}
