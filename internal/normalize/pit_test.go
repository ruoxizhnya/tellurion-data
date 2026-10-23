package normalize

import (
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/require"
)

func pitBar(symbol string, visible time.Time) market.OHLCV {
	return market.OHLCV{
		Symbol:    symbol,
		Date:      visible.Add(-time.Minute),
		Close:     10,
		Frequency: market.Freq1d,
		VisibleAt: visible,
	}
}

func TestVisibleAsOf_FiltersByBoundary(t *testing.T) {
	asOf := time.Date(2026, 10, 22, 15, 0, 0, 0, market.AsiaShanghai)
	before := pitBar("600000.SH", asOf.Add(-time.Minute))
	// 恰好等于 asOf 的记录必须**可见**（可见时刻不晚于观察时刻）。
	at := pitBar("600001.SH", asOf)
	after := pitBar("600002.SH", asOf.Add(time.Nanosecond))

	got, err := VisibleAsOf([]market.OHLCV{before, at, after}, asOf)
	require.NoError(t, err)
	require.Equal(t, []market.OHLCV{before, at}, got)
}

func TestVisibleAsOf_RejectsZeroAsOf(t *testing.T) {
	_, err := VisibleAsOf([]market.OHLCV{pitBar("600000.SH", time.Now())}, time.Time{})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrVisibleAtMissing)
}

func TestVisibleAsOf_RejectsWholeBatchWhenAnyVisibleAtMissing(t *testing.T) {
	asOf := time.Date(2026, 10, 22, 15, 0, 0, 0, market.AsiaShanghai)
	ok := pitBar("600000.SH", asOf.Add(-time.Hour))
	// 缺可见时间点的记录即使「时间上会被过滤掉」也必须整批拒收：
	// 兜底成 mtime / 零点就是把未声明的可见性当成永远可见。
	bad := market.OHLCV{Symbol: "600001.SH", Date: asOf.Add(48 * time.Hour), Frequency: market.Freq1d}

	_, err := VisibleAsOf([]market.OHLCV{ok, bad}, asOf)
	require.ErrorIs(t, err, ErrVisibleAtMissing)
	require.Contains(t, err.Error(), "600001.SH")
}

func TestVisibleAsOf_DoesNotMutateInput(t *testing.T) {
	asOf := time.Date(2026, 10, 22, 15, 0, 0, 0, market.AsiaShanghai)
	in := []market.OHLCV{pitBar("600000.SH", asOf.Add(-time.Hour))}
	snapshot := append([]market.OHLCV(nil), in...)

	got, err := VisibleAsOf(in, asOf)
	require.NoError(t, err)
	require.Equal(t, snapshot, in)
	require.Len(t, got, 1)

	got[0].Close = 999
	require.Equal(t, 10.0, in[0].Close, "filtered slice must not alias the input")
}

func TestVisibleAsOf_EmptyInput(t *testing.T) {
	asOf := time.Date(2026, 10, 22, 15, 0, 0, 0, market.AsiaShanghai)
	got, err := VisibleAsOf(nil, asOf)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestValidateFrequency(t *testing.T) {
	oneMinute := market.OHLCV{Symbol: "600000.SH", Frequency: market.Freq1m}
	daily := market.OHLCV{Symbol: "600000.SH", Frequency: market.Freq1d}

	require.NoError(t, ValidateFrequency([]market.OHLCV{oneMinute}, market.Freq1m))

	err := ValidateFrequency([]market.OHLCV{oneMinute, daily}, market.Freq1m)
	require.ErrorIs(t, err, ErrFrequencyMismatch)
	require.Contains(t, err.Error(), "bar[1]")

	err = ValidateFrequency(nil, "5m")
	require.ErrorIs(t, err, ErrFrequencyMismatch)
}
