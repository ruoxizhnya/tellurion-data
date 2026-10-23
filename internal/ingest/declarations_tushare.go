package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
//
// 口径照抄 core `pkg/data/source/declarations.go`（逐字、不改语义）。
// Tushare 是 A 股日线（前复权，唯一进回测口径）与财报字段的来源。
func (a *TushareAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			DataType:      DataTypeOHLCDaily,
			TimeSemantics: market.VisibleAfterClose,
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecDaily,
		},
		{
			// 财报：披露日对齐防前视。锚点是**披露日**（保留记号 ann_date，
			// 语义承载在规范层 market.Fundamental.AnnDate），不是 TradeTime
			// —— 后者的财报含义是**报告期截止日**，拿它当锚点 = 前视洞。
			DataType:      DataTypeFundamental,
			TimeSemantics: market.VisibleOnAnnouncementDay,
			VisibleField:  VisibleFieldAnnDate,
			Frequency:     market.FreqSpecEvent,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*TushareAdapter)(nil)
