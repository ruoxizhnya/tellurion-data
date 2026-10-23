package ingest

import (
	"encoding/json"
	"time"
)

// UnifiedDataPoint 是 connector 产出的 wire 点位（写入路径的入参形状）。
//
// 它与 `tellurion:pkg/data/source/unified.go` 的同名类型逐字段对齐：X1a 把
// 协议冻结进 contracts（`dataprotocol`）之前，本仓以本地镜像承载写入面，
// 只消费、不另立语义（同 `tellurion-analytics:internal/analysis/model.go`
// 对冻结形状的处理方式）。冻结后本类型随包迁走，调用点改前缀。
//
// VisibleAt 是本仓相对 core 形状的**唯一扩展**：X1d D1 给各表加
// `visible_at` 列之后，写入路径要落的就是这个值；在列落地前它只随
// `GatePoints` 在内存里流转，不持久化。
type UnifiedDataPoint struct {
	// Symbol 是项目约定的规范代码（"600519.SH" / "AAPL"）。
	Symbol string
	// TradeTime 是数据点归属时刻。日频 OHLCV 取交易日零点；实时快照取
	// 快照时刻；财报取报告期截止日（可见锚点另走 VisibleField，见 gate.go）。
	TradeTime time.Time
	// Source 是 connector 名（"tushare" / "mootdx"），落 `source` 列。
	Source string
	// DataType 决定写哪张表（"ohlcv_daily" / "capital_flow" / ...）。
	DataType string
	// Data 是该类型的类型化字段（JSONB 形状）。必含键见各 connector 的
	// 声明与 normalizer；可见时间锚点列必须出现在这里（或由 TradeTime 承载）。
	Data map[string]interface{}
	// IngestTime 由写入管线在构造点时赋值，落 `ingest_time` 列。
	IngestTime time.Time
	// VisibleAt 是按声明的可见锚点导出的可见时刻（`GatePoints` 填写）。
	VisibleAt time.Time
}

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

// ToJSON 序列化 Data（JSONB 列 / 审计留痕）。
func (p UnifiedDataPoint) ToJSON() ([]byte, error) {
	return json.Marshal(p.Data)
}
