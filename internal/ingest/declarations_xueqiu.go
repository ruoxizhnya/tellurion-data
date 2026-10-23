package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
func (a *XueqiuAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			DataType:      DataTypeHotSearch,
			TimeSemantics: market.VisibleCustom,
			VisibleExpr:   "visible_at = fetch_time（热搜榜快照，观察时刻即可见）",
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecIntraday,
		},
		{
			// 新闻：发布时间（publish_time）即可见。
			DataType:      DataTypeNews,
			TimeSemantics: market.VisibleCustom,
			VisibleExpr:   "visible_at = publish_time（新闻发布时刻即可见）",
			VisibleField:  "publish_time",
			Frequency:     market.FreqSpecEvent,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*XueqiuAdapter)(nil)
