package serving

import (
	"context"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// LegacyReader 是 **G3 只读兼容面** 的端口集合（SVC-DATA-G3 2026-11-10）。
//
// 为什么这组端口不在 aggregator 里：aggregator 是「数据集供给」的唯一面，
// 输入是票池 + 窗口 + 频率，输出是可复现的 dataset 快照。core 的旧读面
// （`tellurion:pkg/marketdata/http_provider.go` 打的那 7 条路由）是**逐表的
// 兼容形状**——它们带着 core 侧的字段名与日期格式，且没有 as_of/hash 语义。
// 把两者混在同一个端口上，会让「兼容读」看起来像「数据集」，下一轮又有人
// 拿它当供给面用。
//
// 边界：本接口只读，禁止任何写方法；实现方（`internal/store.Store`）也不得
// 在这组方法里读墙钟——唯一例外是 `IndexConstituents` 的 as_of 缺省值，
// 由 HTTP 层显式传入（见 `httpapi.legacyReadAsOf`）。
type LegacyReader interface {
	BarSource
	TradingCalendar

	// Stocks 返回股票列表；exchange 为空 = 全市场。
	Stocks(ctx context.Context, exchange string) ([]market.Stock, error)
	// Stock 按代码取单只；found=false 表示库里没有（不是错误）。
	Stock(ctx context.Context, symbol string) (market.Stock, bool, error)
	// Fundamental 返回 asOf 当日**已可用**的最新一期财报（available_date 口径）。
	Fundamental(ctx context.Context, symbol string, asOf time.Time) (market.Fundamental, bool, error)
	// IndexConstituents 返回 indexCode 在 asOf 当日生效的成分（in_date/out_date 半开区间）。
	IndexConstituents(ctx context.Context, indexCode string, asOf time.Time) ([]market.IndexConstituent, error)
}
