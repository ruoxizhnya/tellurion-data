package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
func (a *JuchaoAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			// 公告：公告时间（ann_time）即可见，披露驱动事件流。
			DataType:      DataTypeAnnounce,
			TimeSemantics: market.VisibleOnAnnouncementDay,
			VisibleField:  "ann_time",
			Frequency:     market.FreqSpecEvent,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*JuchaoAdapter)(nil)
