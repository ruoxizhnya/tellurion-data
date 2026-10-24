package ingest

import (
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
)

// UnifiedDataPoint 是跨仓共享的 wire 形状，定义在
// `tellurion-contracts:dataprotocol`（QT-40 X1a，契约 `v0.6.0`）。
//
// 本包不再持有逐字段镜像：七个字段（含 VisibleAt）、`ToJSON` 与
// `DeduplicateKey` 都由契约提供，这里只留别名让包内调用点不换前缀。
// 契约刻意不提供构造器（值对象不读时钟，ADR-0004），因此「注入
// IngestTime」的便捷构造留在本侧。
type UnifiedDataPoint = dataprotocol.UnifiedDataPoint

// NewUnifiedDataPoint 构造一个点，IngestTime 由调用方的时钟给出——本包不读
// 墙钟（可复现性：同输入同字节，时钟只允许从外部注入）。
func NewUnifiedDataPoint(symbol, source, dataType string, tradeTime time.Time, ingestTime time.Time, data map[string]interface{}) UnifiedDataPoint {
	return UnifiedDataPoint{
		Symbol:     symbol,
		TradeTime:  tradeTime,
		Source:     source,
		DataType:   dataType,
		Data:       data,
		IngestTime: ingestTime,
	}
}
