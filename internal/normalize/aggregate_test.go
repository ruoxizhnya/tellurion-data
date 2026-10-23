package normalize

import (
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/require"
)

// sh 返回 Asia/Shanghai 时间（2026-10-22）。
func sh(hour, minute, second int) time.Time {
	return time.Date(2026, 10, 22, hour, minute, second, 0, market.AsiaShanghai)
}

func minuteBar(symbol string, start time.Time, o, h, l, c, volume, turnover float64) market.OHLCV {
	return market.OHLCV{
		Symbol:    symbol,
		Date:      start,
		Open:      o,
		High:      h,
		Low:       l,
		Close:     c,
		Volume:    volume,
		Turnover:  turnover,
		Frequency: market.Freq1m,
		VisibleAt: market.IntradayVisibleAt(start, 1),
	}
}

func TestAggregate15m_OpenHighLowCloseAndTotals(t *testing.T) {
	bars := []market.OHLCV{
		minuteBar("600000.SH", sh(9, 30, 0), 10, 11, 9, 10.5, 100, 1000),
		minuteBar("600000.SH", sh(9, 40, 0), 10.5, 12, 10, 11.5, 200, 2000),
		minuteBar("600000.SH", sh(9, 44, 0), 11.5, 13, 11, 12.5, 300, 3000),
	}

	got, err := Aggregate(bars, market.Freq15m)
	require.NoError(t, err)
	require.Len(t, got, 1)

	bar := got[0]
	require.Equal(t, "600000.SH", bar.Symbol)
	require.True(t, bar.Date.Equal(sh(9, 30, 0)), "bucket starts at the session-aligned quarter hour")
	require.Equal(t, 10.0, bar.Open, "open = first bar's open")
	require.Equal(t, 13.0, bar.High)
	require.Equal(t, 9.0, bar.Low)
	require.Equal(t, 12.5, bar.Close, "close = last bar's close in time order")
	require.Equal(t, 600.0, bar.Volume)
	require.Equal(t, 6000.0, bar.Turnover)
	require.Equal(t, market.Freq15m, bar.Frequency)
	require.True(t, bar.VisibleAt.Equal(sh(9, 45, 0)), "15m bar is visible at bucket end")
}

func TestAggregate15m_BucketBoundaries(t *testing.T) {
	bars := []market.OHLCV{
		minuteBar("600000.SH", sh(9, 44, 0), 1, 1, 1, 1, 1, 1),
		minuteBar("600000.SH", sh(9, 45, 0), 2, 2, 2, 2, 1, 1),
		minuteBar("600000.SH", sh(9, 59, 0), 3, 3, 3, 3, 1, 1),
		minuteBar("600000.SH", sh(10, 0, 0), 4, 4, 4, 4, 1, 1),
	}

	got, err := Aggregate(bars, market.Freq15m)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.True(t, got[0].Date.Equal(sh(9, 30, 0)))
	require.True(t, got[1].Date.Equal(sh(9, 45, 0)))
	require.True(t, got[2].Date.Equal(sh(10, 0, 0)))
}

func TestAggregate15m_DoesNotCrossLunchBreak(t *testing.T) {
	bars := []market.OHLCV{
		minuteBar("600000.SH", sh(11, 29, 0), 10, 11, 9, 10.5, 10, 100),
		minuteBar("600000.SH", sh(13, 0, 0), 20, 21, 19, 20.5, 20, 200),
	}

	got, err := Aggregate(bars, market.Freq15m)
	require.NoError(t, err)
	require.Len(t, got, 2, "11:29 归 11:15 bucket，13:00 归 13:00 bucket，绝不跨午休合并")
	require.True(t, got[0].Date.Equal(sh(11, 15, 0)))
	require.True(t, got[1].Date.Equal(sh(13, 0, 0)))
	require.Equal(t, 10.0, got[0].Open)
	require.Equal(t, 20.0, got[1].Open, "afternoon bar must not inherit the morning bucket's OHLC")
}

func TestAggregate15m_ClosingMarksMergeIntoLastBucket(t *testing.T) {
	bars := []market.OHLCV{
		minuteBar("600000.SH", sh(11, 15, 0), 10, 11, 9, 10.5, 10, 100),
		minuteBar("600000.SH", sh(11, 30, 0), 11, 12, 10, 11.5, 20, 200),
	}

	got, err := Aggregate(bars, market.Freq15m)
	require.NoError(t, err)
	require.Len(t, got, 1, "11:30:00 收盘打点并入 11:15 bucket")
	require.True(t, got[0].Date.Equal(sh(11, 15, 0)))
	require.Equal(t, 11.5, got[0].Close)
	require.True(t, got[0].VisibleAt.Equal(sh(11, 30, 0)))

	dailyClose := []market.OHLCV{
		minuteBar("600000.SH", sh(14, 45, 0), 10, 11, 9, 10.5, 10, 100),
		minuteBar("600000.SH", sh(15, 0, 0), 11, 12, 10, 11.5, 20, 200),
	}
	got, err = Aggregate(dailyClose, market.Freq15m)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, got[0].Date.Equal(sh(14, 45, 0)))
	require.True(t, got[0].VisibleAt.Equal(sh(15, 0, 0)))
}

func TestAggregate15m_OpeningAuctionMergesIntoFirstBucket(t *testing.T) {
	bars := []market.OHLCV{
		minuteBar("600000.SH", sh(9, 15, 0), 10, 10, 10, 10, 10, 100),
		minuteBar("600000.SH", sh(9, 20, 0), 10, 10, 10, 10, 20, 200),
		minuteBar("600000.SH", sh(9, 25, 0), 10, 10, 10, 10, 30, 300),
		minuteBar("600000.SH", sh(9, 30, 0), 10, 10, 10, 10, 40, 400),
	}

	got, err := Aggregate(bars, market.Freq15m)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, got[0].Date.Equal(sh(9, 30, 0)))
	require.Equal(t, 100.0, got[0].Volume)
}

func TestAggregate_RejectsBarsOutsideSession(t *testing.T) {
	cases := []struct {
		name string
		ts   time.Time
	}{
		{"before opening auction", sh(9, 14, 59)},
		{"lunch break", sh(12, 0, 0)},
		{"lunch break with seconds", sh(11, 30, 30)},
		{"after close with seconds", sh(15, 0, 1)},
		{"after close", sh(16, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bar := minuteBar("600000.SH", tc.ts, 1, 1, 1, 1, 1, 1)
			_, err := Aggregate([]market.OHLCV{bar}, market.Freq15m)
			require.Error(t, err)
			_, err = Aggregate([]market.OHLCV{bar}, market.Freq1d)
			require.Error(t, err, "日聚合同样只接受交易时段内的分钟 bar")
		})
	}
}

func TestAggregate_RejectsNonOneMinuteInput(t *testing.T) {
	bar := minuteBar("600000.SH", sh(9, 30, 0), 1, 1, 1, 1, 1, 1)
	bar.Frequency = market.Freq15m
	_, err := Aggregate([]market.OHLCV{bar}, market.Freq1d)
	require.Error(t, err)

	bar.Frequency = ""
	_, err = Aggregate([]market.OHLCV{bar}, market.Freq15m)
	require.Error(t, err, "未声明频率的 bar 不得被聚合成任何口径")
}

func TestAggregate_RejectsUnsupportedTarget(t *testing.T) {
	_, err := Aggregate(nil, market.Freq1m)
	require.Error(t, err)
	_, err = Aggregate(nil, "5m")
	require.Error(t, err)
}

func TestAggregate_EmptyInput(t *testing.T) {
	got, err := Aggregate(nil, market.Freq15m)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestAggregate1d_BucketsByTradeDate(t *testing.T) {
	bars := []market.OHLCV{
		minuteBar("600000.SH", sh(9, 30, 0), 10, 11, 9, 10.5, 10, 100),
		minuteBar("600000.SH", sh(14, 59, 0), 10.5, 12, 10, 11.5, 20, 200),
	}

	got, err := Aggregate(bars, market.Freq1d)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), got[0].Date)
	require.Equal(t, market.Freq1d, got[0].Frequency)
	require.Equal(t, 10.0, got[0].Open)
	require.Equal(t, 11.5, got[0].Close)
	require.Equal(t, 30.0, got[0].Volume)
	require.True(t, got[0].VisibleAt.Equal(sh(15, 0, 0)))
}

func TestAggregate_KeepsSymbolsSeparate(t *testing.T) {
	bars := []market.OHLCV{
		minuteBar("600000.SH", sh(9, 30, 0), 10, 11, 9, 10.5, 10, 100),
		minuteBar("600001.SH", sh(9, 30, 0), 20, 21, 19, 20.5, 20, 200),
	}

	got, err := Aggregate(bars, market.Freq15m)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "600000.SH", got[0].Symbol)
	require.Equal(t, "600001.SH", got[1].Symbol)
	require.Equal(t, 10.0, got[0].Open)
	require.Equal(t, 20.0, got[1].Open)
}

func TestAggregate_OrderIndependent(t *testing.T) {
	ordered := []market.OHLCV{
		minuteBar("600000.SH", sh(9, 30, 0), 10, 11, 9, 10.5, 10, 100),
		minuteBar("600000.SH", sh(9, 40, 0), 10.5, 12, 10, 11.5, 20, 200),
		minuteBar("600000.SH", sh(9, 50, 0), 11.5, 13, 11, 12.5, 30, 300),
		minuteBar("600001.SH", sh(9, 30, 0), 20, 21, 19, 20.5, 40, 400),
	}
	shuffled := []market.OHLCV{ordered[3], ordered[1], ordered[0], ordered[2]}

	want, err := Aggregate(ordered, market.Freq15m)
	require.NoError(t, err)
	got, err := Aggregate(shuffled, market.Freq15m)
	require.NoError(t, err)
	require.Equal(t, want, got, "聚合结果只取决于 (symbol, date) 排序后的输入，不取决于到达顺序")
}

func TestSortBars_OrdersBySymbolThenDate(t *testing.T) {
	bars := []market.OHLCV{
		{Symbol: "600001.SH", Date: sh(9, 30, 0)},
		{Symbol: "600000.SH", Date: sh(9, 45, 0)},
		{Symbol: "600000.SH", Date: sh(9, 30, 0)},
	}
	SortBars(bars)
	require.Equal(t, "600000.SH", bars[0].Symbol)
	require.True(t, bars[0].Date.Equal(sh(9, 30, 0)))
	require.True(t, bars[1].Date.Equal(sh(9, 45, 0)))
	require.Equal(t, "600001.SH", bars[2].Symbol)
}
