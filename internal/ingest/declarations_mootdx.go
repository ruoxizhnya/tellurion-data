package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
func (a *MootdxAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			// 实时行情是观察即可见的快照，不参与回测，但口径必须如实声明。
			DataType:      DataTypeRealtime,
			TimeSemantics: market.VisibleCustom,
			VisibleExpr:   "visible_at = fetch_time（实时快照，观察时刻即可见）",
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecIntraday,
		},
		{
			// 分钟 bar 的可见时间点 = bar 周期结束（蓝图 §4.3）。
			DataType:      DataTypeOHLCMinute,
			TimeSemantics: market.VisibleCustom,
			VisibleExpr:   "visible_at = bar_period_end（分钟 bar 周期结束即可见）",
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecIntraday,
		},
		{
			DataType:      DataTypeOHLCDaily,
			TimeSemantics: market.VisibleAfterClose,
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecDaily,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*MootdxAdapter)(nil)
