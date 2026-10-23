package normalize

import (
	"fmt"
	"sort"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// 频率聚合口径（文档真相 = `tellurion:docs/SPEC.md`「规范层 Schema」节）。
//
// 主口径：开 = 首笔开，高 = 期间最高，低 = 期间最低，收 = 末笔收，
// 量 / 额 = 期间合计。
//
// 边界三细则：
//  1. 午休断点：15m bucket 按会话内时钟对齐（09:30/09:45/…/11:15 |
//     13:00/…/14:45），**不得跨午休**；
//  2. 集合竞价：09:15–09:25 并入首个上午 bucket；14:57–15:00 并入末 bucket；
//  3. 停牌 / 缺数据：当日无 bar ⇒ 不产出聚合 bar（缺失，不零填充）。
//
// 时段边界按**秒**判定：11:30:00 / 15:00:00 两个整点收盘打点合法（分别并入
// 11:15 / 14:45 bucket），带秒 / 亚秒的越界打点拒收。分钟粒度判定会把
// 15:00:30 与 15:00:00 混同，静默放宽口径。

// A 股连续竞价时段边界（Asia/Shanghai，分钟计数 0..1440）。
const (
	auctionOpenMin    = 9*60 + 15  // 09:15 开盘集合竞价开始
	morningOpenMin    = 9*60 + 30  // 09:30
	morningCloseMin   = 11*60 + 30 // 11:30
	afternoonOpenMin  = 13 * 60    // 13:00
	afternoonCloseMin = 15 * 60    // 15:00
	bucketMinutes     = 15
)

// Aggregate 把 1m bar 聚合到 target（15m 或 1d）。输入 bar 的 Frequency
// 必须声明为 1m —— 声明与形态不符即拒（fail-loud）。
//
// 15m 是唯一「由 1m 实时聚合、不物化」的频率（`tellurion-data:docs/dataflow.md`
// §1）；1d 的分支留给「无日线表、只能从分钟线合成」的源，路由上默认仍读日线表。
func Aggregate(bars []market.OHLCV, target market.Frequency) ([]market.OHLCV, error) {
	if target != market.Freq15m && target != market.Freq1d {
		return nil, fmt.Errorf("normalize: aggregation target must be 15m/1d (input is always 1m), got %q", target.String())
	}
	for i, b := range bars {
		if b.Frequency != market.Freq1m {
			return nil, fmt.Errorf(
				"normalize: Aggregate input bar[%d] (%s %s) declares %q, want %q",
				i, b.Symbol, b.Date.Format(time.RFC3339), b.Frequency.String(), market.Freq1m.String())
		}
	}
	if len(bars) == 0 {
		return nil, nil
	}

	sorted := make([]market.OHLCV, len(bars))
	copy(sorted, bars)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Symbol != sorted[j].Symbol {
			return sorted[i].Symbol < sorted[j].Symbol
		}
		return sorted[i].Date.Before(sorted[j].Date)
	})

	type acc struct {
		open, close      float64
		high, low        float64
		volume, turnover float64
	}
	type bucketKey struct {
		symbol string
		start  time.Time
	}

	buckets := make(map[bucketKey]*acc, len(sorted))
	var keys []bucketKey
	for _, b := range sorted {
		var (
			start time.Time
			err   error
		)
		if target == market.Freq15m {
			start, err = bucketStart15m(b.Date)
		} else {
			start, err = bucketStartDaily(b.Date)
		}
		if err != nil {
			return nil, err
		}
		k := bucketKey{symbol: b.Symbol, start: start}
		a, ok := buckets[k]
		if !ok {
			buckets[k] = &acc{open: b.Open, close: b.Close, high: b.High, low: b.Low, volume: b.Volume, turnover: b.Turnover}
			keys = append(keys, k)
			continue
		}
		// 已按 (symbol, date) 排序 ⇒ 后来的一定更晚：收 = 末笔。
		a.close = b.Close
		if b.High > a.high {
			a.high = b.High
		}
		if b.Low < a.low {
			a.low = b.Low
		}
		a.volume += b.Volume
		a.turnover += b.Turnover
	}

	out := make([]market.OHLCV, 0, len(keys))
	for _, k := range keys {
		a := buckets[k]
		bar := market.OHLCV{
			Symbol:   k.symbol,
			Date:     k.start,
			Open:     a.open,
			High:     a.high,
			Low:      a.low,
			Close:    a.close,
			Volume:   a.volume,
			Turnover: a.turnover,
			// TradeDays 不填（0）：聚合 bar 不发明日线语义；
			// LimitUp/LimitDown 不推导（需日线参考价，属消费侧）。
			Frequency: target,
		}
		if target == market.Freq15m {
			bar.VisibleAt = market.IntradayVisibleAt(k.start, bucketMinutes)
		} else {
			bar.VisibleAt = market.DailyVisibleAt(k.start)
		}
		out = append(out, bar)
	}
	sortBars(out)
	return out, nil
}

// bucketStart15m 返回 1m bar 所属 15m bucket 的起点（Asia/Shanghai 时刻）。
func bucketStart15m(ts time.Time) (time.Time, error) {
	t := ts.In(market.AsiaShanghai)
	if err := checkInSession(t); err != nil {
		return time.Time{}, err
	}
	hm := t.Hour()*60 + t.Minute()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, market.AsiaShanghai)

	var startMin int
	switch {
	case hm < morningOpenMin:
		// 开盘集合竞价（09:15–09:29）→ 首个上午 bucket
		startMin = morningOpenMin
	case hm < morningCloseMin:
		startMin = morningOpenMin + (hm-morningOpenMin)/bucketMinutes*bucketMinutes
	case hm == morningCloseMin:
		// 11:30:00 上午收盘打点 → 上午末 bucket
		startMin = morningCloseMin - bucketMinutes
	case hm < afternoonCloseMin:
		startMin = afternoonOpenMin + (hm-afternoonOpenMin)/bucketMinutes*bucketMinutes
	default:
		// 15:00:00 收盘打点 → 下午末 bucket
		startMin = afternoonCloseMin - bucketMinutes
	}
	return day.Add(time.Duration(startMin) * time.Minute), nil
}

// bucketStartDaily 返回 1m bar 所属交易日的日 bucket 起点。
// 用 UTC 零点（与日线表 DATE 的扫描形态一致），保证两种 1d 口径可比。
func bucketStartDaily(ts time.Time) (time.Time, error) {
	t := ts.In(market.AsiaShanghai)
	if err := checkInSession(t); err != nil {
		return time.Time{}, err
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
}

// checkInSession 拒绝交易时段之外的 bar（盘前 / 午休 / 盘后打点）。
// 静默丢 bar 等于悄悄改口径，所以这里 fail-loud。
func checkInSession(t time.Time) error {
	hm := t.Hour()*60 + t.Minute()
	switch {
	case hm < auctionOpenMin:
		return fmt.Errorf("normalize: bar %s is before the 09:15 opening auction (not an A-share session)", t.Format(time.RFC3339))
	case hm < morningCloseMin:
		return nil // 09:15–11:29：集合竞价 + 上午连续竞价
	case hm == morningCloseMin:
		if t.Second() == 0 && t.Nanosecond() == 0 {
			return nil // 11:30:00 上午收盘打点
		}
		return fmt.Errorf("normalize: bar %s falls in the lunch break (11:30–13:00)", t.Format(time.RFC3339Nano))
	case hm < afternoonOpenMin:
		return fmt.Errorf("normalize: bar %s falls in the lunch break (11:30–13:00)", t.Format(time.RFC3339))
	case hm < afternoonCloseMin:
		return nil // 13:00–14:59：下午连续竞价 + 收盘集合竞价
	case hm == afternoonCloseMin:
		if t.Second() == 0 && t.Nanosecond() == 0 {
			return nil // 15:00:00 收盘打点
		}
		return fmt.Errorf("normalize: bar %s is after the 15:00 close (not an A-share session)", t.Format(time.RFC3339Nano))
	default:
		return fmt.Errorf("normalize: bar %s is after the 15:00 close (not an A-share session)", t.Format(time.RFC3339))
	}
}

// SortBars 按 (symbol, date) 升序排序。供 aggregator 在物化前统一顺序。
func SortBars(bars []market.OHLCV) { sortBars(bars) }

func sortBars(bars []market.OHLCV) {
	sort.SliceStable(bars, func(i, j int) bool {
		if bars[i].Symbol != bars[j].Symbol {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Date.Before(bars[j].Date)
	})
}
