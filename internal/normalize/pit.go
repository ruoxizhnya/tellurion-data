// Package normalize 是「规范化 Normalization」限界上下文
// （`docs/ARCHITECTURE.md` §2.1）：把 connector 交来的数据统一成规范层形状。
//
// 今天承载两件事，都是 aggregator 取数路径上的纯函数：
//   - `Aggregate`：1m → 15m / 1d 的频率聚合（聚合口径见 `tellurion:docs/SPEC.md`
//     「规范层 Schema」节，本文件是数据服务侧的代码真相）；
//   - `VisibleAsOf`：PIT 闸门——可见时间点晚于 `as_of` 的记录一律不返回，
//     缺 `visible_at` 的批次整批拒收（`ErrVisibleAtMissing`）。
//
// 两者都不做 I/O、不读时钟、不读随机：同输入必然同输出，才配得上
// `dataset_snapshot_hash` 的可复现语义。
package normalize

import (
	"errors"
	"fmt"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// ErrVisibleAtMissing 是 PIT 闸门的拒收信号：数据点缺可见时间点。
//
// 语义（`tellurion-data:docs/api-schema.md` §2）：**拒收该批次**，
// 不许用 mtime / 交易日零点等「看起来合理」的值兜底——未声明的可见性
// 就是泄漏，不是「永远可见」。
var ErrVisibleAtMissing = errors.New("normalize: data point has no visible_at")

// ErrFrequencyMismatch 是「记录自带频率与请求频率不符」。频率是记录自带
// 字段（ADR-031 §4.1），声明与形态不符即拒，不静默改写。
var ErrFrequencyMismatch = errors.New("normalize: bar frequency contradicts the requested frequency")

// VisibleAsOf 是 PIT 闸门：返回 `VisibleAt <= asOf` 的记录子集。
//
// 三条判据：
//  1. `asOf` 为零值 ⇒ 报错（调用方必须先给出物化上界，本函数不猜「现在」）；
//  2. 任一条记录的 `VisibleAt` 为零值 ⇒ **整批拒收**（`ErrVisibleAtMissing`），
//     错误信息点名第一条违规记录；
//  3. 过滤用 `VisibleAt.After(asOf)` 的否定式 —— 恰好等于 `asOf` 的记录**可见**
//     （PIT 语义：可见时刻不晚于观察时刻）。
//
// 返回值是新切片，输入不被修改；顺序保持输入顺序（排序是调用方的事）。
func VisibleAsOf(bars []market.OHLCV, asOf time.Time) ([]market.OHLCV, error) {
	if asOf.IsZero() {
		return nil, fmt.Errorf("normalize: visible-as-of requires a non-zero as_of")
	}
	for _, b := range bars {
		if b.VisibleAt.IsZero() {
			return nil, fmt.Errorf("%w: %s %s", ErrVisibleAtMissing, b.Symbol, b.Date.Format(time.RFC3339))
		}
	}
	out := make([]market.OHLCV, 0, len(bars))
	for _, b := range bars {
		if !b.VisibleAt.After(asOf) {
			out = append(out, b)
		}
	}
	return out, nil
}

// ValidateFrequency 校验每条记录自带的频率等于 want。
//
// 非聚合路径（请求 1m / 1d、源表本就是该频率）必须过这道校验：频率字段
// 是记录自己声明的，源侧填错时这里 fail-loud，而不是把 1m 当 15m 发出去。
func ValidateFrequency(bars []market.OHLCV, want market.Frequency) error {
	if !want.Valid() {
		return fmt.Errorf("%w: target %q is not a known frequency", ErrFrequencyMismatch, want.String())
	}
	for i, b := range bars {
		if b.Frequency != want {
			return fmt.Errorf("%w: bar[%d] %s %s declares %q, want %q",
				ErrFrequencyMismatch, i, b.Symbol, b.Date.Format(time.RFC3339), b.Frequency.String(), want.String())
		}
	}
	return nil
}
