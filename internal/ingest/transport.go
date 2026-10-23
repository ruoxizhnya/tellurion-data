package ingest

import (
	"context"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// 本文件是**传输 port**：四个 Tushare 系 connector 今天依赖 core 的
// `pkg/data.TushareClient`（HTTP 客户端本体 + 配额闸 + raw 归档），而本仓
// 禁止 import core 的实现包（`AGENTS.md` §3）。
//
// 处置：把 connector **用到的那几个方法**抽成窄接口，实现仍留在 core，
// 由 core 在装配处适配（一个薄 wrapper，不改取数语义）。这样：
//   - X1c 搬的是 connector 的**声明与归一化**，不是把 HTTP 传输重写一遍
//     （工单禁区：「不重写现有 connector 语义」）；
//   - 传输后续整体迁入时，只需替换实现，接口与调用点不动。
//
// ⚠️ 本接口**只增不删**：删方法 = core 侧的适配器编译不过，而那正是
// 「悄悄改了取数路径」的形状。

// TushareTransport 是 Tushare 系 connector 依赖的取数面。
type TushareTransport interface {
	// FetchStocks 是健康探针用的最便宜调用（交易所上市清单）。
	FetchStocks(ctx context.Context, exchange, listStatus string) ([]market.Stock, error)
	// FetchDailyOHLCV 取某票区间日线（前复权口径由实现负责）。
	FetchDailyOHLCV(ctx context.Context, symbol, startDate, endDate string) ([]market.OHLCV, error)
	// FetchFundamentals 取某票某日的财报字段。
	FetchFundamentals(ctx context.Context, symbol, date string) ([]market.Fundamental, error)

	// MacroIndicators 返回宏观指标目录（空 Symbols 时取全目录）。
	// 目录归属传输侧：它随源 API 的可用指标变化，connector 不该内置一份。
	MacroIndicators() []string
	// FetchMacroIndicators 按指标取观测序列。
	FetchMacroIndicators(ctx context.Context, indicators ...string) ([]domain.MacroSeries, error)
	// ProbeMacro 是只取不落的连通性探针。
	ProbeMacro(ctx context.Context, indicator string) error

	// FetchDailyBasic 取某日的全市场活跃度截面。
	FetchDailyBasic(ctx context.Context, date string) ([]domain.MarketActivity, error)
	// ProbeDailyBasic 是只取不落的连通性探针。
	ProbeDailyBasic(ctx context.Context, date string) error

	// FetchMoneyflowTiers 取某票区间的分档成交额净额。
	FetchMoneyflowTiers(ctx context.Context, symbol, startDate, endDate string) ([]MoneyflowTiers, error)
	// ProbeMoneyflow 是只取不落的连通性探针。
	ProbeMoneyflow(ctx context.Context, symbol, startDate, endDate string) error
}

// MoneyflowTiers 是资金流的分档净额行（`capital_flow` 的四档列）。
//
// 四个净额都可空：nil = 该档本行不可得，落库是 NULL 而不是 0
// （「缺失≠0」）。字段与 core `pkg/data.MoneyflowTierNet` 逐字对齐。
type MoneyflowTiers struct {
	Symbol    string
	TradeDate time.Time
	SuperNet  *float64
	LargeNet  *float64
	MediumNet *float64
	SmallNet  *float64
}

// NetByColumn 按 capital_flow 列名取该档净额；未知列名返回 nil
// （绝不把一个不存在的列写成 0）。
func (m MoneyflowTiers) NetByColumn(column string) *float64 {
	switch column {
	case "super_net":
		return m.SuperNet
	case "large_net":
		return m.LargeNet
	case "medium_net":
		return m.MediumNet
	case "small_net":
		return m.SmallNet
	default:
		return nil
	}
}
