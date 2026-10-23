package serving

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/normalize"
)

// ─── 测试替身 ──────────────────────────────────────────────────

type fakeUniverse struct {
	symbols []string
	err     error
	calls   int
	got     UniverseRequest
}

func (f *fakeUniverse) ResolveUniverse(_ context.Context, req UniverseRequest) ([]string, error) {
	f.calls++
	f.got = req
	return append([]string(nil), f.symbols...), f.err
}

type fakeCalendar struct {
	days  []time.Time
	err   error
	calls int
}

func (f *fakeCalendar) TradingDays(_ context.Context, _, _ time.Time) ([]time.Time, error) {
	f.calls++
	return append([]time.Time(nil), f.days...), f.err
}

type fakeVersion struct {
	version string
	err     error
	calls   int
	gotFreq market.Frequency
}

func (f *fakeVersion) DataVersion(_ context.Context, frequency market.Frequency) (string, error) {
	f.calls++
	f.gotFreq = frequency
	return f.version, f.err
}

type fakeBars struct {
	bars    []market.OHLCV
	err     error
	calls   int
	gotFreq market.Frequency
	gotFrom time.Time
	gotTo   time.Time
	gotSyms []string
}

func (f *fakeBars) ReadBars(_ context.Context, symbols []string, from, to time.Time, frequency market.Frequency) ([]market.OHLCV, error) {
	f.calls++
	f.gotSyms = append([]string(nil), symbols...)
	f.gotFrom, f.gotTo, f.gotFreq = from, to, frequency
	return append([]market.OHLCV(nil), f.bars...), f.err
}

// ─── 装配 ─────────────────────────────────────────────────────

func utcDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// shTimestamp 返回 Asia/Shanghai 下的时刻（用于日内用例）。
func shTimestamp(hour, minute int) time.Time {
	return time.Date(2026, 10, 22, hour, minute, 0, 0, market.AsiaShanghai)
}

func dailyBar(symbol string, tradeDate time.Time, close float64) market.OHLCV {
	return market.OHLCV{
		Symbol:    symbol,
		Date:      tradeDate,
		Open:      close - 1,
		High:      close + 1,
		Low:       close - 2,
		Close:     close,
		Volume:    1000,
		Turnover:  10000,
		TradeDays: 1,
		Frequency: market.Freq1d,
		VisibleAt: market.DailyVisibleAt(tradeDate),
	}
}

func minuteBar(symbol string, start time.Time, close float64) market.OHLCV {
	return market.OHLCV{
		Symbol:    symbol,
		Date:      start,
		Open:      close - 1,
		High:      close + 1,
		Low:       close - 2,
		Close:     close,
		Volume:    100,
		Turnover:  1000,
		Frequency: market.Freq1m,
		VisibleAt: market.IntradayVisibleAt(start, 1),
	}
}

type harness struct {
	uni   *fakeUniverse
	cal   *fakeCalendar
	ver   *fakeVersion
	bars  *fakeBars
	cache *Cache
	agg   *Aggregator
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		uni:   &fakeUniverse{symbols: []string{"600000.SH"}},
		cal:   &fakeCalendar{days: []time.Time{utcDay(2026, 10, 22), utcDay(2026, 10, 23)}},
		ver:   &fakeVersion{version: "batch-2026-10-22"},
		bars:  &fakeBars{bars: []market.OHLCV{dailyBar("600000.SH", utcDay(2026, 10, 22), 10)}},
		cache: NewCache(),
	}
	var err error
	h.agg, err = New(Options{
		Bars:     h.bars,
		Universe: h.uni,
		Calendar: h.cal,
		Versions: h.ver,
		Cache:    h.cache,
	})
	require.NoError(t, err)
	return h
}

func baseRequest() ResolveRequest {
	return ResolveRequest{
		Universe:   "csi300",
		From:       utcDay(2026, 10, 22),
		To:         utcDay(2026, 10, 23),
		Frequency:  market.Freq1d,
		Adjustment: "qfq",
		AsOf:       utcDay(2026, 10, 24),
	}
}

// ─── Resolve ──────────────────────────────────────────────────

func TestResolve_IsDeterministicAndEchoesMetadata(t *testing.T) {
	h := newHarness(t)
	req := baseRequest()

	first, err := h.agg.Resolve(context.Background(), req)
	require.NoError(t, err)
	second, err := h.agg.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first, second, "同输入必须同 hash 同 metadata")
	require.NotEmpty(t, first.Hash)

	meta := first.Metadata
	require.Equal(t, []string{"600000.SH"}, meta.Universe)
	require.Equal(t, contracts.UniverseHash([]string{"600000.SH"}), meta.UniverseHash)
	require.True(t, meta.WindowStart.Equal(req.From))
	require.True(t, meta.WindowEnd.Equal(req.To))
	require.Equal(t, "1d", meta.Frequency)
	require.Equal(t, "batch-2026-10-22", meta.DataVersion)
	require.Equal(t, contracts.CalendarHash(h.cal.days), meta.CalendarHash)
	require.Equal(t, "qfq", meta.AdjustmentBasis)
	require.True(t, meta.AsOf.Equal(req.AsOf))
	require.Equal(t, "csi300", h.uni.got.Name)
	require.True(t, h.uni.got.AsOf.Equal(req.AsOf), "as_of 必须透传给票池解析，否则会有幸存者偏差")
	require.Equal(t, market.Freq1d, h.bars.gotFreq, "请求 1d 时直接读日线表")
	require.Equal(t, 2, h.bars.calls, "每次 Resolve 都会取数（缓存按 hash 寻址，不短路 Resolve）")
}

func TestResolve_HashCoversEveryFrozenComponent(t *testing.T) {
	baseline := newHarness(t)
	base, err := baseline.agg.Resolve(context.Background(), baseRequest())
	require.NoError(t, err)

	cases := []struct {
		name   string
		mutate func(h *harness, req *ResolveRequest)
	}{
		{"universe", func(h *harness, _ *ResolveRequest) {
			h.uni.symbols = []string{"600000.SH", "600001.SH"}
		}},
		{"window", func(_ *harness, req *ResolveRequest) { req.To = utcDay(2026, 10, 24) }},
		{"frequency", func(h *harness, req *ResolveRequest) {
			req.Frequency = market.Freq1m
			req.AsOf = shTimestamp(10, 0)
			h.bars.bars = []market.OHLCV{minuteBar("600000.SH", shTimestamp(9, 30), 10)}
		}},
		{"data_version", func(h *harness, _ *ResolveRequest) { h.ver.version = "batch-2026-10-23" }},
		{"calendar", func(h *harness, _ *ResolveRequest) {
			h.cal.days = []time.Time{utcDay(2026, 10, 22)}
		}},
		{"adjustment", func(_ *harness, req *ResolveRequest) { req.Adjustment = "hfq" }},
	}

	seen := map[string]string{base.Hash: "baseline"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			req := baseRequest()
			tc.mutate(h, &req)
			got, err := h.agg.Resolve(context.Background(), req)
			require.NoError(t, err)
			require.NotEqual(t, base.Hash, got.Hash, "改动冻结六分量中的 %s 必须改变 dataset_snapshot_hash", tc.name)
			require.NotContains(t, seen, got.Hash, "不同分量不得撞同一个 hash")
			seen[got.Hash] = tc.name
		})
	}
}

func TestResolve_AsOfIsNotInTheSixComponentsAndCollidesFailClosed(t *testing.T) {
	h := newHarness(t)
	req := baseRequest()
	first, err := h.agg.Resolve(context.Background(), req)
	require.NoError(t, err)

	// as_of 只决定可见上界、不在冻结六分量里 ⇒ 同 hash。
	req.AsOf = req.AsOf.Add(time.Hour)
	_, err = h.agg.Resolve(context.Background(), req)
	require.ErrorIs(t, err, ErrSnapshotMismatch, "同 hash 不同 as_of 必须 fail-closed，不静默返回别人的数据集")

	fresh := newHarness(t)
	second, err := fresh.agg.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first.Hash, second.Hash, "确认这是 hash 定义本身的行为，不是缓存 bug")
}

func TestResolve_PITDropsBarsNotYetVisible(t *testing.T) {
	h := newHarness(t)
	h.bars.bars = []market.OHLCV{
		dailyBar("600000.SH", utcDay(2026, 10, 22), 10),
		dailyBar("600000.SH", utcDay(2026, 10, 23), 11),
	}
	req := baseRequest()
	req.AsOf = market.DailyVisibleAt(utcDay(2026, 10, 22)) // 10-22 15:00 SH，恰好等于可见时刻

	res, err := h.agg.Resolve(context.Background(), req)
	require.NoError(t, err)

	ds, err := h.agg.Snapshot(res.Hash)
	require.NoError(t, err)
	series := ds.Bars["600000.SH"]
	require.Len(t, series, 1, "10-23 的 bar 在 10-22 收盘时不可见")
	require.True(t, series[0].Date.Equal(utcDay(2026, 10, 22)))
}

func TestResolve_RejectsBatchWithMissingVisibleAt(t *testing.T) {
	h := newHarness(t)
	bar := dailyBar("600000.SH", utcDay(2026, 10, 22), 10)
	bar.VisibleAt = time.Time{}
	h.bars.bars = []market.OHLCV{bar}

	_, err := h.agg.Resolve(context.Background(), baseRequest())
	require.ErrorIs(t, err, normalize.ErrVisibleAtMissing)
}

func TestResolve_Aggregates15mFromOneMinute(t *testing.T) {
	h := newHarness(t)
	h.bars.bars = []market.OHLCV{
		minuteBar("600000.SH", shTimestamp(9, 30), 10),
		minuteBar("600000.SH", shTimestamp(9, 35), 10.5),
		minuteBar("600000.SH", shTimestamp(9, 45), 11),
		minuteBar("600000.SH", shTimestamp(9, 50), 12),
	}
	req := baseRequest()
	req.Frequency = market.Freq15m
	req.AsOf = shTimestamp(10, 15)

	res, err := h.agg.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, market.Freq1m, h.bars.gotFreq, "15m 不物化：读 1m 再聚合")
	require.Equal(t, "15m", res.Metadata.Frequency)

	ds, err := h.agg.Snapshot(res.Hash)
	require.NoError(t, err)
	series := ds.Bars["600000.SH"]
	require.Len(t, series, 2)
	require.True(t, series[0].Date.Equal(shTimestamp(9, 30)))
	require.Equal(t, market.Freq15m, series[0].Frequency)
	require.Equal(t, 9.0, series[0].Open)
	require.Equal(t, 11.5, series[0].High)
	require.Equal(t, 8.0, series[0].Low)
	require.Equal(t, 10.5, series[0].Close)
	require.True(t, series[0].VisibleAt.Equal(shTimestamp(9, 45)))
}

func TestResolve_AggregatesBeforePITFiltering(t *testing.T) {
	h := newHarness(t)
	h.bars.bars = []market.OHLCV{
		minuteBar("600000.SH", shTimestamp(9, 30), 10),
		minuteBar("600000.SH", shTimestamp(9, 45), 11),
	}
	req := baseRequest()
	req.Frequency = market.Freq15m
	// 09:50：09:30 bucket（09:45 可见）已可见，09:45 bucket（10:00 可见）尚不可见。
	req.AsOf = shTimestamp(9, 50)

	res, err := h.agg.Resolve(context.Background(), req)
	require.NoError(t, err)

	ds, err := h.agg.Snapshot(res.Hash)
	require.NoError(t, err)
	series := ds.Bars["600000.SH"]
	require.Len(t, series, 1, "未到期的 bucket 不得返回半桶；先聚合后过滤才会得到完整首桶")
	require.True(t, series[0].Date.Equal(shTimestamp(9, 30)))
	require.Equal(t, 10.0, series[0].Close)
}

func TestResolve_RejectsSourceFrequencyContradiction(t *testing.T) {
	h := newHarness(t)
	h.bars.bars = []market.OHLCV{minuteBar("600000.SH", shTimestamp(9, 30), 10)}

	_, err := h.agg.Resolve(context.Background(), baseRequest())
	require.ErrorIs(t, err, normalize.ErrFrequencyMismatch)
}

func TestResolve_RejectsSymbolOutsideUniverse(t *testing.T) {
	h := newHarness(t)
	h.bars.bars = []market.OHLCV{dailyBar("999999.SH", utcDay(2026, 10, 22), 10)}

	_, err := h.agg.Resolve(context.Background(), baseRequest())
	require.ErrorIs(t, err, ErrUnexpectedSymbol)
	require.Contains(t, err.Error(), "999999.SH")
}

func TestResolve_MissingSymbolIsAbsentNotZeroFilled(t *testing.T) {
	h := newHarness(t)
	h.uni.symbols = []string{"600000.SH", "600001.SH"}
	h.bars.bars = []market.OHLCV{dailyBar("600000.SH", utcDay(2026, 10, 22), 10)}

	res, err := h.agg.Resolve(context.Background(), baseRequest())
	require.NoError(t, err)
	ds, err := h.agg.Snapshot(res.Hash)
	require.NoError(t, err)

	require.Contains(t, ds.Bars, "600000.SH")
	require.NotContains(t, ds.Bars, "600001.SH", "窗口内没数据的 symbol 直接缺席，不补空序列")
	require.Len(t, ds.Bars, 1)
}

func TestResolve_FailsClosedOnEmptyPorts(t *testing.T) {
	t.Run("empty universe", func(t *testing.T) {
		h := newHarness(t)
		h.uni.symbols = nil
		_, err := h.agg.Resolve(context.Background(), baseRequest())
		require.ErrorIs(t, err, ErrEmptyUniverse)
		require.Zero(t, h.bars.calls, "票池为空时不得取数")
	})
	t.Run("empty calendar", func(t *testing.T) {
		h := newHarness(t)
		h.cal.days = nil
		_, err := h.agg.Resolve(context.Background(), baseRequest())
		require.ErrorIs(t, err, ErrEmptyCalendar)
		require.Zero(t, h.bars.calls)
	})
	t.Run("blank data version", func(t *testing.T) {
		h := newHarness(t)
		h.ver.version = "   "
		_, err := h.agg.Resolve(context.Background(), baseRequest())
		require.ErrorIs(t, err, ErrEmptyDataVersion)
		require.Zero(t, h.bars.calls)
	})
}

func TestResolve_ValidatesRequestBeforeAnyIO(t *testing.T) {
	h := newHarness(t)
	req := baseRequest()
	req.From = time.Time{}

	_, err := h.agg.Resolve(context.Background(), req)
	require.ErrorIs(t, err, ErrMissingParameter)
	require.Zero(t, h.uni.calls)
	require.Zero(t, h.cal.calls)
	require.Zero(t, h.ver.calls)
	require.Zero(t, h.bars.calls)
}

func TestResolve_DeterministicBytesRegardlessOfSourceOrder(t *testing.T) {
	ordered := []market.OHLCV{
		dailyBar("600000.SH", utcDay(2026, 10, 22), 10),
		dailyBar("600000.SH", utcDay(2026, 10, 23), 11),
		dailyBar("600001.SH", utcDay(2026, 10, 22), 20),
	}
	shuffled := []market.OHLCV{ordered[2], ordered[1], ordered[0]}

	h1 := newHarness(t)
	h1.uni.symbols = []string{"600000.SH", "600001.SH"}
	h1.bars.bars = ordered
	h2 := newHarness(t)
	h2.uni.symbols = []string{"600000.SH", "600001.SH"}
	h2.bars.bars = shuffled

	first, err := h1.agg.Resolve(context.Background(), baseRequest())
	require.NoError(t, err)
	second, err := h2.agg.Resolve(context.Background(), baseRequest())
	require.NoError(t, err)
	require.Equal(t, first.Hash, second.Hash)

	ds1, err := h1.agg.Snapshot(first.Hash)
	require.NoError(t, err)
	ds2, err := h2.agg.Snapshot(second.Hash)
	require.NoError(t, err)
	require.Equal(t, ds1.Digest, ds2.Digest, "乱序输入必须产出逐字节相同的内容")
	require.Equal(t, ds1, ds2)

	for i := 1; i < len(ds1.Bars["600000.SH"]); i++ {
		require.True(t, ds1.Bars["600000.SH"][i-1].Date.Before(ds1.Bars["600000.SH"][i].Date),
			"物化后的序列必须按时间升序")
	}
}

func TestResolve_PropagatesPortErrors(t *testing.T) {
	sentinel := errors.New("upstream down")

	h := newHarness(t)
	h.uni.err = sentinel
	_, err := h.agg.Resolve(context.Background(), baseRequest())
	require.ErrorIs(t, err, sentinel)

	h = newHarness(t)
	h.cal.err = sentinel
	_, err = h.agg.Resolve(context.Background(), baseRequest())
	require.ErrorIs(t, err, sentinel)

	h = newHarness(t)
	h.ver.err = sentinel
	_, err = h.agg.Resolve(context.Background(), baseRequest())
	require.ErrorIs(t, err, sentinel)

	h = newHarness(t)
	h.bars.err = sentinel
	_, err = h.agg.Resolve(context.Background(), baseRequest())
	require.ErrorIs(t, err, sentinel)
}

// ─── Snapshot / 装配 ──────────────────────────────────────────

func TestSnapshot_RoundTripAndNotFound(t *testing.T) {
	h := newHarness(t)
	res, err := h.agg.Resolve(context.Background(), baseRequest())
	require.NoError(t, err)

	ds, err := h.agg.Snapshot(res.Hash)
	require.NoError(t, err)
	require.Equal(t, res.Hash, ds.Hash)
	require.Equal(t, res.Metadata, ds.Metadata)
	require.Len(t, ds.Bars["600000.SH"], 1)
	require.NotEmpty(t, ds.Digest)

	_, err = h.agg.Snapshot("deadbeef")
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	for _, blank := range []string{"", "   "} {
		_, err = h.agg.Snapshot(blank)
		require.ErrorIs(t, err, ErrMissingParameter)
	}
}

func TestNew_RequiresEveryPort(t *testing.T) {
	cases := map[string]Options{
		"bars nil":     {Universe: &fakeUniverse{}, Calendar: &fakeCalendar{}, Versions: &fakeVersion{}},
		"universe nil": {Bars: &fakeBars{}, Calendar: &fakeCalendar{}, Versions: &fakeVersion{}},
		"calendar nil": {Bars: &fakeBars{}, Universe: &fakeUniverse{}, Versions: &fakeVersion{}},
		"versions nil": {Bars: &fakeBars{}, Universe: &fakeUniverse{}, Calendar: &fakeCalendar{}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(opts)
			require.ErrorIs(t, err, ErrInvalidParameter)
		})
	}

	agg, err := New(Options{Bars: &fakeBars{}, Universe: &fakeUniverse{}, Calendar: &fakeCalendar{}, Versions: &fakeVersion{}})
	require.NoError(t, err)
	require.NotNil(t, agg.cache, "未注入 Cache 时必须自建空缓存")
}

func TestSourceFrequency(t *testing.T) {
	require.Equal(t, market.Freq1m, sourceFrequency(market.Freq15m), "15m 读 1m 再聚合，不物化")
	require.Equal(t, market.Freq1d, sourceFrequency(market.Freq1d), "日线直接读日线表，不从分钟合成")
	require.Equal(t, market.Freq1m, sourceFrequency(market.Freq1m))
}
