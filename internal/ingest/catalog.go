package ingest

import (
	"fmt"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// 本文件是 **12 个 connector 的注册声明清单**（QT-40 ③ / X1c 的接收面）。
//
// 口径来源：`tellurion:pkg/data/source/declarations.go` 的 18 条声明
// （2026-10-23 逐条对照）。本仓不自造语义——每一条都与 core 的既有声明
// 同值；X1c 搬 connector 时，catalog 就是注册表要满足的**目标形状**，
// 搬完由 connector 自己的 `Declarations()` 提供同一批值（届时删除本表）。
//
// ⚠️ 本表的 `Schema` **只登记可见时间锚点列**，不冒充完整 wire schema：
// 完整 schema 随 connector 在 X1c 迁入时补齐。锚点列的用途是让注册门
// 能验证「声明了可见时间但记录里没有可推导的锚点」这类形态矛盾。
//
// 12 个源的声明口径（保守：不早于真实可见时刻）：
//   - 日更聚合数据（日线 / 资金流 / 龙虎榜 / 活跃度 / 全球行情）→ after_close；
//   - 披露驱动数据（财报 / 公告 / 宏观 / 新闻）→ announcement_day 或 custom；
//   - 实时快照 / 日内 bar / 板块映射 / 热搜 → custom + 判定表达；
//   - 宏观的可见锚点取 `as_of`（导入时刻），不取 `obs_date`（观测日早于发布日，
//     拿它当锚点即前视——2026-10-16 用户裁决 A 的保守代理）。

// Wire data type 字面值（与 core `pkg/data/source/adapter.go` 常量同值）。
const (
	dataTypeOHLCDaily   = "ohlcv_daily"
	dataTypeOHLCMinute  = "ohlcv_minute"
	dataTypeRealtime    = "realtime_quote"
	dataTypeCapitalFlow = "capital_flow"
	dataTypeFundamental = "fundamentals"
	dataTypeSectors     = "sectors"
	dataTypeStockSector = "stock_sector"
	dataTypeTopList     = "top_list"
	dataTypeLimitUpPool = "limit_up_pool"
	dataTypeAnnounce    = "announcements"
	dataTypeNews        = "news"
	dataTypeHotSearch   = "hot_search"
	dataTypeGlobalOHLCV = "global_ohlcv"
	dataTypeMacro       = "macro_series"
	dataTypeDailyBasic  = "daily_basic"
)

// catalogConnector 是 catalog 阶段的 Connector 实现（无传输）。
type catalogConnector struct {
	name    string
	types   []string
	decls   []ConnectorDeclaration
	anchors map[string]DataSchema // 仅含可见锚点列
}

func (c *catalogConnector) Name() string             { return c.name }
func (c *catalogConnector) SupportedTypes() []string { return append([]string(nil), c.types...) }
func (c *catalogConnector) Declarations() []ConnectorDeclaration {
	out := make([]ConnectorDeclaration, len(c.decls))
	copy(out, c.decls)
	return out
}

func (c *catalogConnector) Schema(dataType string) (DataSchema, error) {
	s, ok := c.anchors[dataType]
	if !ok {
		return DataSchema{}, fmt.Errorf("%w: %s has no anchor schema for %q", ErrDeclarationMismatch, c.name, dataType)
	}
	return s, nil
}

// Catalog 返回 12 个 connector 的注册清单（每次调用返回新对象，防调用方改动）。
func Catalog() []Connector {
	return []Connector{
		tushareConnector(),
		tushareMacroConnector(),
		tushareMoneyflowConnector(),
		tushareDailyBasicConnector(),
		mootdxConnector(),
		eastmoneyConnector(),
		eastmoneySectorsConnector(),
		eastmoneyTopListConnector(),
		juchaoConnector(),
		xueqiuConnector(),
		alphaVantageConnector(),
		yahooFinanceConnector(),
	}
}

// CatalogNames 返回 12 个 connector 的稳定名字（排序与 Catalog 一致）。
func CatalogNames() []string {
	out := make([]string, 0, 12)
	for _, c := range Catalog() {
		out = append(out, c.Name())
	}
	return out
}

func tushareConnector() Connector {
	return &catalogConnector{
		name:  "tushare",
		types: []string{dataTypeOHLCDaily, dataTypeFundamental},
		decls: []ConnectorDeclaration{
			decl(dataTypeOHLCDaily, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
			// 财报：TradeTime 承载报告期截止日，可见锚点必须是披露日
			//（规范层 market.Fundamental.AnnDate），否则前视洞回来。
			decl(dataTypeFundamental, market.VisibleOnAnnouncementDay, "", VisibleFieldAnnDate, market.FreqSpecEvent),
		},
		anchors: map[string]DataSchema{},
	}
}

func tushareMacroConnector() Connector {
	return &catalogConnector{
		name:  "tushare_macro",
		types: []string{dataTypeMacro},
		decls: []ConnectorDeclaration{
			// 锚点取 as_of（导入时刻），不取 obs_date：观测日早于真实发布日。
			decl(dataTypeMacro, market.VisibleOnAnnouncementDay, "", "as_of", market.FreqSpecEvent),
		},
		anchors: map[string]DataSchema{
			dataTypeMacro: anchorSchema(dataTypeMacro, "as_of", "datetime"),
		},
	}
}

func tushareMoneyflowConnector() Connector {
	return &catalogConnector{
		name:  "tushare_moneyflow",
		types: []string{dataTypeCapitalFlow},
		decls: []ConnectorDeclaration{
			decl(dataTypeCapitalFlow, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
		},
		anchors: map[string]DataSchema{},
	}
}

func tushareDailyBasicConnector() Connector {
	return &catalogConnector{
		name:  "tushare_daily_basic",
		types: []string{dataTypeDailyBasic},
		decls: []ConnectorDeclaration{
			// 换手率族由全日成交量统计得出，收盘前不存在（蓝图 §4.2 高危 PIT）。
			decl(dataTypeDailyBasic, market.VisibleAfterClose, "", "trade_date", market.FreqSpecDaily),
		},
		anchors: map[string]DataSchema{
			dataTypeDailyBasic: anchorSchema(dataTypeDailyBasic, "trade_date", "date"),
		},
	}
}

func mootdxConnector() Connector {
	return &catalogConnector{
		name:  "mootdx",
		types: []string{dataTypeRealtime, dataTypeOHLCMinute, dataTypeOHLCDaily},
		decls: []ConnectorDeclaration{
			decl(dataTypeRealtime, market.VisibleCustom,
				"visible_at = fetch_time（实时快照，观察时刻即可见）", VisibleFieldTradeTime, market.FreqSpecIntraday),
			decl(dataTypeOHLCMinute, market.VisibleCustom,
				"visible_at = bar_period_end（分钟 bar 周期结束即可见）", VisibleFieldTradeTime, market.FreqSpecIntraday),
			decl(dataTypeOHLCDaily, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
		},
		anchors: map[string]DataSchema{},
	}
}

func eastmoneyConnector() Connector {
	return &catalogConnector{
		name:  "eastmoney",
		types: []string{dataTypeCapitalFlow},
		decls: []ConnectorDeclaration{
			decl(dataTypeCapitalFlow, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
		},
		anchors: map[string]DataSchema{},
	}
}

func eastmoneySectorsConnector() Connector {
	return &catalogConnector{
		name:  "eastmoney_sectors",
		types: []string{dataTypeSectors, dataTypeStockSector},
		decls: []ConnectorDeclaration{
			decl(dataTypeSectors, market.VisibleCustom,
				"visible_at = fetch_time（盘中板块快照，观察时刻即可见）", VisibleFieldTradeTime, market.FreqSpecIntraday),
			decl(dataTypeStockSector, market.VisibleCustom,
				"visible_at = fetch_time（个股板块映射，观察时刻即可见）", VisibleFieldTradeTime, market.FreqSpecEvent),
		},
		anchors: map[string]DataSchema{},
	}
}

func eastmoneyTopListConnector() Connector {
	return &catalogConnector{
		name:  "eastmoney_toplist",
		types: []string{dataTypeTopList, dataTypeLimitUpPool},
		decls: []ConnectorDeclaration{
			decl(dataTypeTopList, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
			decl(dataTypeLimitUpPool, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
		},
		anchors: map[string]DataSchema{},
	}
}

func juchaoConnector() Connector {
	return &catalogConnector{
		name:  "juchao",
		types: []string{dataTypeAnnounce},
		decls: []ConnectorDeclaration{
			// 公告时间（ann_time）即可见，披露驱动事件流。
			decl(dataTypeAnnounce, market.VisibleOnAnnouncementDay, "", "ann_time", market.FreqSpecEvent),
		},
		anchors: map[string]DataSchema{
			dataTypeAnnounce: anchorSchema(dataTypeAnnounce, "ann_time", "datetime"),
		},
	}
}

func xueqiuConnector() Connector {
	return &catalogConnector{
		name:  "xueqiu",
		types: []string{dataTypeHotSearch, dataTypeNews},
		decls: []ConnectorDeclaration{
			decl(dataTypeHotSearch, market.VisibleCustom,
				"visible_at = fetch_time（热搜榜快照，观察时刻即可见）", VisibleFieldTradeTime, market.FreqSpecIntraday),
			decl(dataTypeNews, market.VisibleCustom,
				"visible_at = publish_time（新闻发布时刻即可见）", "publish_time", market.FreqSpecEvent),
		},
		anchors: map[string]DataSchema{
			dataTypeNews: anchorSchema(dataTypeNews, "publish_time", "datetime"),
		},
	}
}

func alphaVantageConnector() Connector {
	return &catalogConnector{
		name:  "alpha_vantage",
		types: []string{dataTypeGlobalOHLCV},
		decls: []ConnectorDeclaration{
			decl(dataTypeGlobalOHLCV, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
		},
		anchors: map[string]DataSchema{},
	}
}

func yahooFinanceConnector() Connector {
	return &catalogConnector{
		name:  "yahoo_finance",
		types: []string{dataTypeGlobalOHLCV},
		decls: []ConnectorDeclaration{
			decl(dataTypeGlobalOHLCV, market.VisibleAfterClose, "", VisibleFieldTradeTime, market.FreqSpecDaily),
		},
		anchors: map[string]DataSchema{},
	}
}

// decl 是声明字面量的简写，避免 18 条声明各写一遍五个字段名。
func decl(dataType string, ts market.TimeSemantics, expr, field string, freq market.FrequencySpec) ConnectorDeclaration {
	return ConnectorDeclaration{
		DataType:      dataType,
		TimeSemantics: ts,
		VisibleExpr:   expr,
		VisibleField:  field,
		Frequency:     freq,
	}
}

func anchorSchema(dataType, field, typ string) DataSchema {
	return DataSchema{DataType: dataType, Fields: []SchemaField{{Name: field, Type: typ}}}
}
