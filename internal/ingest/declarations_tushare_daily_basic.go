package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
func (a *TushareDailyBasicAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			// 换手率族由**全日**成交量统计得出，收盘前不存在——蓝图 §4.2
			// 点名的高危 PIT 场景。可见锚点是 trade_date（wire 的 date 列）。
			DataType:      DataTypeDailyBasic,
			TimeSemantics: market.VisibleAfterClose,
			VisibleField:  "trade_date",
			Frequency:     market.FreqSpecDaily,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*TushareDailyBasicAdapter)(nil)
