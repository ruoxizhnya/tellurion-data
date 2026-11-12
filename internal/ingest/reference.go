package ingest

// 参考数据（reference data）的取数窄口。
//
// 为什么单开一个口，而不是往 `TushareTransport` 里加方法：`stocks` /
// `trading_calendar` / `index_constituents` 不是点位流——没有 `visible_at`、
// 不进 `ingest.data_points` 的 data_type 声明表。它们的落点是 C 集参照表，
// 写面是 upsert/整期替换（见 `store.ReferenceWriter`），而不是 `BulkInsert`
// 的闸门 + 幂等内容比对。
// 把两类语义塞进同一条接口，调用方就分不清「哪条路要过 visible_at 门」。
//
// 依赖方向仍是 `transport → ingest`：本文件只放类型与接口，不 import 具体传输。

import (
	"context"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// TradingDay 是 `market.trading_calendar` 的一行。
//
// `Exchange` 是交易所代码（SSE / SZSE）；`Date` 为 UTC 零点的自然日，
// 与库里 `DATE` 列经 pgx 读出时的时区口径一致。
type TradingDay struct {
	Date         time.Time
	Exchange     string
	IsTradingDay bool
}

// DailyBasic 是 `market.daily_basic` 的一行（Tushare `daily_basic` 的估值截面）。
//
// 与 `market.Fundamental` 的区别是**粒度**：这里每个交易日一个截面值，
// PIT 判据是 `trade_date`（收盘后可见），不是报告期/公告日。因此两者分表，
// 不共用唯一键（见 ADR-0012 追加节）。
type DailyBasic struct {
	Symbol  string
	Date    time.Time
	PE      *float64
	PETTM   *float64
	PB      *float64
	PS      *float64
	PSTTM   *float64
	DVRatio *float64
	TotalMV *float64
	CircMV  *float64
}

// ReferenceTransport 是参考数据同步依赖的取数面。
//
// `FetchStocks` 与 `TushareTransport` 上的同名方法逐字一致；重复声明而不是
// 嵌入，是为了让「参考数据同步」能独立于点位管线的接口演化——将来换源时
// 两条腿可以各自替换。
type ReferenceTransport interface {
	// FetchStocks 取交易所上市清单（listStatus 空 = 源端默认）。
	FetchStocks(ctx context.Context, exchange, listStatus string) ([]market.Stock, error)
	// FetchTradeCalendar 取 exchange 在 [start, end] 的日历；零值日期 = 不限。
	FetchTradeCalendar(ctx context.Context, exchange string, start, end time.Time) ([]TradingDay, error)
	// FetchIndexConstituents 取指数在 [start, end] 内最新一期成分（含权重）。
	FetchIndexConstituents(ctx context.Context, indexCode string, start, end time.Time) ([]market.IndexConstituent, error)
}
