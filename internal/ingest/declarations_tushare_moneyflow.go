package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
//
// Tushare `moneyflow` 与东财资金流是**同一张表的另一族口径**，时间语义一致：
// 分档成交额由全日逐笔归类聚合而成，收盘前不存在 ⇒ after_close + trade_date。
func (a *TushareMoneyflowAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			DataType:      DataTypeCapitalFlow,
			TimeSemantics: market.VisibleAfterClose,
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecDaily,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*TushareMoneyflowAdapter)(nil)
