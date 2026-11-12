package serving

import (
	"context"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// ListingWindow 是一只票的上市窗口（对齐 core `pkg/backtest.ListingWindow`）。
//
// ListDate 为零值 = 上市日未知 —— 调用方不做左端过滤（宁可放宽也不凭空剔除）；
// DelistDate == nil = 尚未退市（右端开放）。Delisted 是 `delist_date` 非空的
// 直接派生，调用方不必再判 nil。Status 原样带出（`active` / `delisted`），
// 让「暂停上市」这类状态不被 bool 压平。
type ListingWindow struct {
	Symbol     string
	ListDate   time.Time
	DelistDate *time.Time
	Delisted   bool
	Status     string
}

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
	// ListingWindows 返回全市场上市窗口；tradeDate 非零 = 只返回当日仍在市的票。
	//
	// 「当日仍在市」与 core 的 `ListingWindow.IsListed` 同口径：两端闭区间
	// （`list_date <= tradeDate <= delist_date`），退市日当天仍算在市；
	// `list_date IS NULL` 不做左端过滤。
	ListingWindows(ctx context.Context, tradeDate time.Time) ([]ListingWindow, error)
	// LatestTradeDate 返回库内日线的最新交易日；found=false 表示还没有任何
	// 可用日线。core 的 `/api/stocks/count` 用它派生 `latest_date`——
	// 不新增端点，由已有读面算出来（2026-11-12 裁）。
	LatestTradeDate(ctx context.Context) (time.Time, bool, error)
	// Stock 按代码取单只；found=false 表示库里没有（不是错误）。
	Stock(ctx context.Context, symbol string) (market.Stock, bool, error)
	// IndexConstituents 返回 indexCode 在 asOf 当日生效的成分（in_date/out_date 半开区间）。
	IndexConstituents(ctx context.Context, indexCode string, asOf time.Time) ([]market.IndexConstituent, error)
}
