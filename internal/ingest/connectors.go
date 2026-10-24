package ingest

import "github.com/ruoxizhnya/tellurion-contracts/dataprotocol"

// 本文件是 12 个 connector 的**唯一装配面**，取代过渡期的 `Catalog()`
// 声明清单（`docs/TASKS.md` QT-40 已登记的「catalog 是过渡期双真相」）。
//
// 为什么删得掉：每条声明现在由 connector 自己的 `Declarations()` 提供
// （`declarations_*.go`），清单不再手抄一份——手抄的那份迟早与实现漂移，
// 而漂移的方向恰恰是「注册门放行了一个实际没声明的源」。
//
// 传输由调用方注入：本仓不持有 HTTP/SDK 客户端（那是 core 的搬迁尾巴），
// 因此装配参数用 `Transports` 显式列出，nil 表示该门以 disabled 注册
// （取数时跳过，启动不失败）。

// Wire data type 取值（契约单源：`tellurion-contracts:dataprotocol`，QT-40 X1a）。
//
// 它们是**跨语言契约**：落库的 data_type、声明里的 DataType、读方的过滤值
// 必须逐字一致。取值只在契约里定义一次，这里保留本包导出名做别名，12 个
// connector 的调用点零改动；契约改值时本包取值在编译期随之改变，不再有
// 第二份副本漂移。
const (
	DataTypeOHLCDaily   = dataprotocol.DataTypeOHLCDaily
	DataTypeOHLCMinute  = dataprotocol.DataTypeOHLCMinute
	DataTypeRealtime    = dataprotocol.DataTypeRealtime
	DataTypeCapitalFlow = dataprotocol.DataTypeCapitalFlow
	DataTypeFundamental = dataprotocol.DataTypeFundamental
	DataTypeSectors     = dataprotocol.DataTypeSectors
	DataTypeStockSector = dataprotocol.DataTypeStockSector
	DataTypeTopList     = dataprotocol.DataTypeTopList
	DataTypeLimitUpPool = dataprotocol.DataTypeLimitUpPool
	DataTypeAnnounce    = dataprotocol.DataTypeAnnounce
	DataTypeNews        = dataprotocol.DataTypeNews
	DataTypeHotSearch   = dataprotocol.DataTypeHotSearch
	DataTypeGlobalOHLCV = dataprotocol.DataTypeGlobalOHLCV
	DataTypeMacro       = dataprotocol.DataTypeMacro
	DataTypeDailyBasic  = dataprotocol.DataTypeDailyBasic
)

// Transports 是 connector 依赖的传输实现集合。nil 字段 = 该门 disabled。
type Transports struct {
	Tushare TushareTransport
	Mootdx  MootdxTransport
	// Eastmoney 是东财 HTTP 客户端（三个东财门共用）。
	Eastmoney *EastmoneyClient
	// AlphaVantageKey 是 Alpha Vantage 的 API key（空 = disabled）。
	AlphaVantageKey string
}

// Connectors 返回 12 个 connector（每次调用返回新实例，防调用方改动内部状态）。
//
// 顺序即登记顺序；`Registry.Names()` 会按名字排序，因此这里的顺序只影响日志。
func Connectors(t Transports) []DataSourceAdapter {
	return []DataSourceAdapter{
		NewTushareAdapter(t.Tushare),
		NewTushareMacroAdapter(t.Tushare),
		NewTushareMoneyflowAdapter(t.Tushare),
		NewTushareDailyBasicAdapter(t.Tushare),
		NewMootdxAdapter(t.Mootdx),
		NewEastmoneyAdapter(t.Eastmoney),
		NewEastmoneySectorsAdapter(t.Eastmoney),
		NewEastmoneyTopListAdapter(t.Eastmoney),
		NewJuchaoAdapter(),
		NewXueqiuAdapter(),
		NewAlphaVantageAdapter(t.AlphaVantageKey),
		NewYahooFinanceAdapter(),
	}
}

// ConnectorNames 返回 12 个 connector 的稳定名字（跨仓契约，改名会静默改变注册面）。
func ConnectorNames() []string {
	return []string{
		"tushare",
		"tushare_macro",
		"tushare_moneyflow",
		"tushare_daily_basic",
		"mootdx",
		"eastmoney",
		"eastmoney_sectors",
		"eastmoney_toplist",
		"juchao",
		"xueqiu",
		"alpha_vantage",
		"yahoo_finance",
	}
}

// RegisterConnectors 把 12 个 connector 装进注册表（任一被拒即返回错误，
// 不跳过——漏声明的源必须让进程起不来，而不是静默少一个门）。
func RegisterConnectors(reg *Registry, t Transports) error {
	for _, c := range Connectors(t) {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}
