package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
func (a *EastmoneyTopListAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			DataType:      DataTypeTopList,
			TimeSemantics: market.VisibleAfterClose,
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecDaily,
		},
		{
			DataType:      DataTypeLimitUpPool,
			TimeSemantics: market.VisibleAfterClose,
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecDaily,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*EastmoneyTopListAdapter)(nil)
