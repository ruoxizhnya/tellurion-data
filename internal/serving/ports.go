// Package serving 是「供给 Serving」限界上下文（`docs/ARCHITECTURE.md` §2.1）：
// 数据服务对外的**唯一面**（aggregator）。
//
// 一次 `Resolve` 做五件事，缺一不可：
//  1. 票池解析（按 `as_of` 解析，成分变更不进未来）；
//  2. 取数（按源频率读，15m 由 1m 聚合）；
//  3. 频率聚合（`internal/normalize.Aggregate`）；
//  4. PIT 过滤（`internal/normalize.VisibleAsOf`）；
//  5. 算 `dataset_snapshot_hash`（调 contracts 的唯一定义，本包不自己实现）。
//
// 可复现性红线：同输入必然同 hash、同字节；`as_of` 只决定可见上界，
// 它**不在冻结六分量里**（见 `Metadata.AsOf` 注释与 docs 的风险登记）。
package serving

import (
	"context"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// UniverseRequest 是票池解析的输入。
//
// `AsOf` 必须透传：成分表会变，拿「今天的成分」跑三年前的回测就是幸存者
// 偏差（`tellurion:pkg/marketdata/board.go` 的 as-of 语义）。
type UniverseRequest struct {
	// Name 是票池名（如 "csi300"）；与 IndexCode / Symbols 三选一。
	Name string
	// IndexCode 是指数代码（如 "000300.SH"）；与 Name / Symbols 三选一。
	IndexCode string
	// Symbols 是显式票池（单 symbol 取数也走同一面，不另开逐 bar 端点）。
	Symbols []string
	// AsOf 是解析时点。
	AsOf time.Time
}

// UniverseResolver 解析票池（输入面，保留在本仓；不是取数面）。
type UniverseResolver interface {
	ResolveUniverse(ctx context.Context, req UniverseRequest) ([]string, error)
}

// TradingCalendar 返回窗口内的交易日轴（保序）。
//
// 它是 `calendar_hash` 的输入：日历补一个节假日而行情一个都没变时，
// 结果仍会变 —— 所以轴必须进 hash（QT-03 六分量之一）。
type TradingCalendar interface {
	TradingDays(ctx context.Context, from, to time.Time) ([]time.Time, error)
}

// DataVersioner 返回「这一版数据」的身份（供应商批次 / 表水位），
// 参与 `dataset_snapshot_hash`。
type DataVersioner interface {
	DataVersion(ctx context.Context, frequency market.Frequency) (string, error)
}

// BarSource 按源频率读行情。
//
// 频率口径：请求 15m 时 aggregator 读 1m 再聚合（15m 不物化）；请求 1d 时
// 直接读日线表（不从分钟合成，避免日线口径随分钟数据完整度漂移）。
type BarSource interface {
	ReadBars(ctx context.Context, symbols []string, from, to time.Time, frequency market.Frequency) ([]market.OHLCV, error)
}
