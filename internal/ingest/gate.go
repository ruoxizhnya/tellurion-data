package ingest

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"tellurion-data/internal/normalize"
)

// 写侧可见性闸门（`docs/db-schema.md` §6.1 的 D3 那一步）：归一化之后、
// 入库之前，按**该 data_type 自己声明**的可见锚点逐点校验。
//
// 三条纪律（§3 红线 + §6.3/§6.6）：
//  1. 判据是「**声明的那一列**非空且可解析」，不是「有一个长得像可见时间的
//     列非空」——`stock_fundamentals.available_date` 这类 COALESCE 生成列
//     永不为空，拿它当判据是假护栏（§6.3 专门点名）；
//  2. 缺锚点 ⇒ **整批拒收**（`ErrVisibleAtMissing`），不许用 mtime / 交易日
//     零点兜底；这与取数侧 `normalize.VisibleAsOf` 的批次语义同源；
//  3. 本包不读时钟、不推导「收盘时刻 / 披露日零点」一类语义值（那是 X1d
//     D2 回填与 §6.3 的重算规则，且 after_close 的 15:00 北京时刻对
//     `global_ohlcv` 这类境外源不成立）——锚点列的值就是可见时刻，
//     值不存在就拒，值存在就直接落 `VisibleAt`。
var (
	// ErrAnchorInvalid 是锚点列有值但解析不出时间。它与「缺锚点」
	// （ErrVisibleAtMissing）必须是两个错误：前者是数据形态坏了要修数据，
	// 后者是声明链断了要修采集。
	ErrAnchorInvalid = errors.New("ingest: visible anchor value is present but not a parsable date/datetime")
)

// anchorTimeLayouts 是锚点列字符串值的解析词表，从宽到严：带时区 RFC3339 →
// 不带时区的「日期 时刻」→ 纯日期。解析成功的时间值直接接受（connector 侧
// 已解析的形状不该被字符串化）。
var anchorTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// DeclarationFor 返回某 data_type 在注册表里的声明。
//
// 没有任何 connector 声明该类型 ⇒ ErrDeclarationMissing（fail-closed：
// 写入路径不许给「没登记过可见语义」的类型放行）。
func (r *Registry) DeclarationFor(dataType string) (ConnectorDeclaration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.connectors {
		for _, d := range c.Declarations() {
			if d.DataType == dataType {
				return d, nil
			}
		}
	}
	return ConnectorDeclaration{}, fmt.Errorf("%w: no registered connector declares data type %q", ErrDeclarationMissing, dataType)
}

// GatePoints 是写侧闸门：逐点校验声明的可见锚点，给每个点落 `VisibleAt`。
//
// 返回值是新切片（含填好 VisibleAt 的点拷贝），输入不被修改；**任一点
// 违规即整批拒收**，错误信息点名第一条违规记录，不给「少一条」的静默丢弃
// 留空间（§6.1：门一开，「少一条进统计」变成「少可见时点整批拒收」）。
func GatePoints(decl ConnectorDeclaration, points []UnifiedDataPoint) ([]UnifiedDataPoint, error) {
	if !decl.TimeSemantics.Valid() {
		return nil, fmt.Errorf("%w: gate needs a declared time semantics, got %q", ErrDeclarationInvalid, decl.TimeSemantics)
	}
	field := strings.TrimSpace(decl.VisibleField)
	if field == "" {
		return nil, fmt.Errorf("%w: gate needs a declared visible anchor field", ErrDeclarationMismatch)
	}

	out := make([]UnifiedDataPoint, 0, len(points))
	for i, p := range points {
		visible, err := anchorValue(decl, p, field)
		if err != nil {
			return nil, fmt.Errorf("point[%d] %s %s: %w", i, p.Symbol, p.TradeTime.Format(time.RFC3339), err)
		}
		gated := p
		gated.VisibleAt = visible
		out = append(out, gated)
	}
	return out, nil
}

// anchorValue 取单点的可见锚点值：保留锚点走 TradeTime，其余走 Data 列。
func anchorValue(decl ConnectorDeclaration, p UnifiedDataPoint, field string) (time.Time, error) {
	if field == VisibleFieldTradeTime {
		if p.TradeTime.IsZero() {
			return time.Time{}, fmt.Errorf("%w: anchor %q resolves to TradeTime which is zero", ErrVisibleAtMissing, field)
		}
		return p.TradeTime.UTC(), nil
	}
	raw, ok := p.Data[field]
	if !ok || raw == nil {
		return time.Time{}, fmt.Errorf("%w: anchor column %q is absent or NULL", ErrVisibleAtMissing, field)
	}
	switch v := raw.(type) {
	case time.Time:
		if v.IsZero() {
			return time.Time{}, fmt.Errorf("%w: anchor column %q is zero", ErrVisibleAtMissing, field)
		}
		return v.UTC(), nil
	case string:
		if strings.TrimSpace(v) == "" {
			return time.Time{}, fmt.Errorf("%w: anchor column %q is empty", ErrVisibleAtMissing, field)
		}
		for _, layout := range anchorTimeLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("%w: anchor column %q value %q matches none of %v",
			ErrAnchorInvalid, field, v, anchorTimeLayouts)
	default:
		return time.Time{}, fmt.Errorf("%w: anchor column %q has Go type %T (want string or time.Time)",
			ErrAnchorInvalid, field, raw)
	}
}

// ErrVisibleAtMissing 是取数侧 PIT 闸门的哨兵（normalize 包定义），写入侧
// 复用同一个：缺可见时间在两条路径上都是「整批拒收」，不该有两个名字。
var ErrVisibleAtMissing = normalize.ErrVisibleAtMissing
