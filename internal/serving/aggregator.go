package serving

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"

	"tellurion-data/internal/normalize"
)

// Options 是 Aggregator 的装配参数。四个 port 全部必填（缺一个就报错，
// 不做「默认读某个表」的隐式装配）。
type Options struct {
	Bars     BarSource
	Universe UniverseResolver
	Calendar TradingCalendar
	Versions DataVersioner
	// Cache 可选；为 nil 时用进程内空缓存（同 hash 强幂等）。
	Cache *Cache
}

// Aggregator 是供给面的唯一入口：`Resolve` 物化一次数据集，`Snapshot`
// 按 hash 取回。并发安全取决于注入的 port 是否并发安全（本类型只持不可变
// 引用，缓存自身加锁）。
type Aggregator struct {
	bars     BarSource
	universe UniverseResolver
	calendar TradingCalendar
	versions DataVersioner
	cache    *Cache
}

// New 构造 Aggregator。任一必填 port 为 nil ⇒ 报错（fail-closed：缺装配
// 的进程不该在第一次请求时才发现）。
func New(opts Options) (*Aggregator, error) {
	if opts.Bars == nil {
		return nil, fmt.Errorf("%w: bar source is nil", ErrInvalidParameter)
	}
	if opts.Universe == nil {
		return nil, fmt.Errorf("%w: universe resolver is nil", ErrInvalidParameter)
	}
	if opts.Calendar == nil {
		return nil, fmt.Errorf("%w: trading calendar is nil", ErrInvalidParameter)
	}
	if opts.Versions == nil {
		return nil, fmt.Errorf("%w: data versioner is nil", ErrInvalidParameter)
	}
	cache := opts.Cache
	if cache == nil {
		cache = NewCache()
	}
	return &Aggregator{
		bars:     opts.Bars,
		universe: opts.Universe,
		calendar: opts.Calendar,
		versions: opts.Versions,
		cache:    cache,
	}, nil
}

// Resolve 按五步物化一次数据集并返回其 hash 与六分量回显。
//
// 顺序是刻意的：**先聚合、后 PIT 过滤**。bucket 内任一条 1m bar 的可见时刻
// 都不晚于 bucket 结束，若先过滤再聚合，一个「看起来完整」的 bucket 可能
// 实际缺尾 —— 那种半桶会被下游当成真实行情。
func (a *Aggregator) Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error) {
	if err := req.Validate(); err != nil {
		return ResolveResult{}, err
	}

	rawUniverse, err := a.universe.ResolveUniverse(ctx, UniverseRequest{
		Name:      strings.TrimSpace(req.Universe),
		IndexCode: strings.TrimSpace(req.IndexCode),
		Symbols:   req.Symbols,
		// AsOf 必须透传：拿今天的成分跑三年前的回测 = 幸存者偏差。
		AsOf: req.AsOf,
	})
	if err != nil {
		return ResolveResult{}, fmt.Errorf("serving: resolve universe: %w", err)
	}
	symbols, err := normalizeSymbols(rawUniverse)
	if err != nil {
		return ResolveResult{}, err
	}
	if len(symbols) == 0 {
		return ResolveResult{}, ErrEmptyUniverse
	}

	calendar, err := a.calendar.TradingDays(ctx, req.From, req.To)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("serving: trading days: %w", err)
	}
	if err := validateCalendar(calendar); err != nil {
		return ResolveResult{}, err
	}

	version, err := a.versions.DataVersion(ctx, req.Frequency)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("serving: data version: %w", err)
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return ResolveResult{}, ErrEmptyDataVersion
	}

	source := sourceFrequency(req.Frequency)
	bars, err := a.bars.ReadBars(ctx, symbols, req.From, req.To, source)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("serving: read bars: %w", err)
	}
	if source == req.Frequency {
		if err := normalize.ValidateFrequency(bars, req.Frequency); err != nil {
			return ResolveResult{}, err
		}
	} else {
		bars, err = normalize.Aggregate(bars, req.Frequency)
		if err != nil {
			return ResolveResult{}, err
		}
	}

	bars, err = normalize.VisibleAsOf(bars, req.AsOf)
	if err != nil {
		return ResolveResult{}, err
	}
	// PIT 过滤保持输入顺序；物化前统一排序，保证同输入同字节。
	normalize.SortBars(bars)

	grouped, err := groupBySymbol(bars, symbols)
	if err != nil {
		return ResolveResult{}, err
	}

	meta := Metadata{
		Universe:        symbols,
		UniverseHash:    contracts.UniverseHash(symbols),
		WindowStart:     req.From,
		WindowEnd:       req.To,
		Frequency:       req.Frequency.String(),
		DataVersion:     version,
		CalendarHash:    contracts.CalendarHash(calendar),
		AdjustmentBasis: req.adjustment(),
		AsOf:            req.AsOf,
	}
	hash := meta.snapshotInput(calendar).Hash()

	dataset, err := a.cache.Store(hash, meta, calendar, grouped)
	if err != nil {
		return ResolveResult{}, err
	}
	return ResolveResult{Hash: dataset.Hash, Metadata: dataset.Metadata}, nil
}

// Snapshot 按 hash 取回已物化的数据集（404 / 缓存串号 500 的判据在 Cache）。
func (a *Aggregator) Snapshot(hash string) (Dataset, error) {
	trimmed := strings.TrimSpace(hash)
	if trimmed == "" {
		return Dataset{}, fmt.Errorf("%w: dataset hash is empty", ErrMissingParameter)
	}
	return a.cache.Get(trimmed)
}

// sourceFrequency 是「目标频率 → 读哪张表」的唯一映射。
//
//   - 15m：读 1m 再聚合（15m **不物化**，见 docs/dataflow.md §1）；
//   - 1d：直接读日线表（不从分钟合成 —— 日线口径会随分钟数据完整度漂移）；
//   - 1m：原样读。
func sourceFrequency(target market.Frequency) market.Frequency {
	switch target {
	case market.Freq15m:
		return market.Freq1m
	case market.Freq1d:
		return market.Freq1d
	default:
		return market.Freq1m
	}
}

// groupBySymbol 把有序 bars 折成 map，并拒绝票池外的 symbol。
//
// 窗口内没有数据的 symbol **直接缺席**（不补空序列）：补一个空切片会让
// 「停牌」与「没请求」两种语义在下游不可分辨。
func groupBySymbol(bars []market.OHLCV, universe []string) (map[string][]market.OHLCV, error) {
	known := make(map[string]struct{}, len(universe))
	for _, s := range universe {
		known[s] = struct{}{}
	}
	out := make(map[string][]market.OHLCV, len(universe))
	for _, b := range bars {
		if _, ok := known[b.Symbol]; !ok {
			return nil, fmt.Errorf("%w: %s at %s", ErrUnexpectedSymbol, b.Symbol, b.Date.Format(time.RFC3339))
		}
		out[b.Symbol] = append(out[b.Symbol], b)
	}
	return out, nil
}
