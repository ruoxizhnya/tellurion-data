package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
//
// 宏观指标为发布驱动的事件流（发布后方可见）。可见锚点取 **as_of（导入
// 时刻）**，不取 obs_date（观测日早于真实发布日）——2026-10-16 用户裁决 A
// 的保守代理，代价是失去发布日精度。
func (a *TushareMacroAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			DataType:      DataTypeMacro,
			TimeSemantics: market.VisibleOnAnnouncementDay,
			VisibleField:  "as_of",
			Frequency:     market.FreqSpecEvent,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*TushareMacroAdapter)(nil)
