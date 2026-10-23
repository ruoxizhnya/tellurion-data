package ingest

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// Declarations implements Connector.Declarations.
func (a *AlphaVantageAdapter) Declarations() []ConnectorDeclaration {
	return []ConnectorDeclaration{
		{
			DataType:      DataTypeGlobalOHLCV,
			TimeSemantics: market.VisibleAfterClose,
			VisibleField:  VisibleFieldTradeTime,
			Frequency:     market.FreqSpecDaily,
		},
	}
}

// compile-time proof: this connector satisfies the runtime contract.
var _ DataSourceAdapter = (*AlphaVantageAdapter)(nil)
