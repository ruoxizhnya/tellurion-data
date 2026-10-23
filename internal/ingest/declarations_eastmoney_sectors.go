package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
func (a *EastmoneySectorsAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			// 板块行情是盘中实时快照（push2），观察时刻即可见。
			DataType:      DataTypeSectors,
			TimeSemantics: market.VisibleCustom,
			VisibleExpr:   "visible_at = fetch_time（盘中板块快照，观察时刻即可见）",
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecIntraday,
		},
		{
			// 个股板块映射为参考数据，按观察时刻可见。
			DataType:      DataTypeStockSector,
			TimeSemantics: market.VisibleCustom,
			VisibleExpr:   "visible_at = fetch_time（个股板块映射，观察时刻即可见）",
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecEvent,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*EastmoneySectorsAdapter)(nil)
