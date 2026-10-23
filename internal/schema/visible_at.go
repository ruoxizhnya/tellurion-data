package schema

// 本文件是 `docs/db-schema.md` §6.2 那条「三个集合」的代码化版本。
//
// 三个集合不是「所有表都加一列」的糊法，判据是**在册声明**
// （15 个 wire `data_type`，18 条声明）而不是 `NewTableMapper()` 的 13 张表：
// 两者不相等，差 `macro_series` 与 `daily_basic`，而那正是覆盖面最容易漏的
// 两个位置（§6.2 原文）。

// A 集：加 `visible_at` 列（12 张）。它们经 connector → Normalizer →
// BulkInsert 落库，且表里没有语义等价的现成列。
var aSetTables = []string{
	"ohlcv_daily_qfq",
	"ohlcv_minute",
	"realtime_quote",
	"capital_flow",
	"sectors",
	"stock_sector_map",
	"top_list",
	"limit_up_pool",
	"announcements",
	"news",
	"hot_search",
	"global_ohlcv",
}

// B 集：不加列，**明确承认现成列**（2 张）。`as_of` 就是这两条声明给的
// 可见锚点，再建一列 = 同表两个锚点、读方各挑一个。
//
// ⚠️ 这两张表**不走 BulkInsert**（写者在 core 的 daily_basic / macro 同步腿），
// 挂在写入闸门上的判据拦不到它们 —— 覆盖面限制是 §6.2 点名的第一条，
// 由 `docs/db-schema.md` §6.6 ③ 要求「每条都要有测试或显式豁免」。
var bSetTables = []struct {
	Table string
	// Anchor 是承载可见时刻的现成列。
	Anchor string
	// DataType 是 wire 字面值（不是 Go 标识符）。
	DataType string
}{
	{Table: "market_activity", Anchor: "as_of", DataType: "daily_basic"},
	{Table: "quant.macro_series", Anchor: "as_of", DataType: "macro_series"},
}

// cSetExemptions 是「明确在门外」的登记表（§6.2 C 集）。
//
// 每条都必须写明**为什么**——只列名字等于把「忘了」伪装成「豁免」。
var cSetExemptions = []struct {
	Table  string
	Reason string
}{
	{"stock_fundamentals", "已有生成列 available_date = COALESCE(ann_date, trade_date)，判据走 ann_date（见 §6.3 单列条）；再建 visible_at 会让同表两个锚点漂移"},
	{"market.index_daily", "实测漏项：不在 NewTableMapper() 的 13 个映射值里，由另一条路径写；它与 B 集同一个「门拦不到」问题，本次不冒认已覆盖"},
	{"trading_calendar", "是 calendar_hash 的输入，不是观测数据流"},
	{"stocks", "参考数据，无「何时可见」语义"},
	{"index_constituents", "前视风险在成分变更时点，归 resolve 的 as_of 语义"},
	{"dividends", "公司行为事件，可见性由公告侧承担"},
	{"splits", "公司行为事件，同上"},
	{"ingest.raw", "原始归档，按 content_hash + fetched_at 寻址"},
	{"ingest.sync_jobs", "作业状态，不是数据"},
	{"ingest.sync_schedules", "调度配置，不是数据"},
	{"ingest.data_migrations", "迁移留痕，不是数据"},
}

// ASetTables 返回 A 集表名（拷贝，防调用方改动）。
func ASetTables() []string {
	out := make([]string, len(aSetTables))
	copy(out, aSetTables)
	return out
}

// BSetAnchors 返回 B 集的 (表, 现成锚点列, wire data_type)。
func BSetAnchors() []struct {
	Table    string
	Anchor   string
	DataType string
} {
	out := make([]struct {
		Table    string
		Anchor   string
		DataType string
	}, len(bSetTables))
	for i, b := range bSetTables {
		out[i].Table, out[i].Anchor, out[i].DataType = b.Table, b.Anchor, b.DataType
	}
	return out
}

// CSetExemptTables 返回明确在门外的表名（拷贝）。
func CSetExemptTables() []string {
	out := make([]string, 0, len(cSetExemptions))
	for _, e := range cSetExemptions {
		out = append(out, e.Table)
	}
	return out
}

// VisibleAtStatements 返回 A 集 12 张表的加列语句（§6.5 D1）。
//
// 两处都加，是刻意的：
//   - `market.<t>` 是**归属目标**（本仓建的 schema）；
//   - `public.<t>` 是**今天的物理位置**（`docs/db-schema.md` §1 注：迁移不是
//     改名，物理搬迁是独立一步）。只在目标侧加列，今天的库仍然没有该列，
//     写入闸门 `GatePoints` 落的 `VisibleAt` 就没有落点。
//
// 列一律**可空、不设默认、不 NOT NULL**（D1 原文）：默认值会把「不知道」
// 伪装成「某个看起来合理的时刻」，而那正是 §6.4 禁止的第四档。
func VisibleAtStatements() []string {
	out := make([]string, 0, len(aSetTables)*2)
	for _, t := range aSetTables {
		out = append(out,
			`ALTER TABLE IF EXISTS `+marketSchema+`.`+t+` ADD COLUMN IF NOT EXISTS visible_at TIMESTAMPTZ`,
			`ALTER TABLE IF EXISTS public.`+t+` ADD COLUMN IF NOT EXISTS visible_at TIMESTAMPTZ`,
		)
	}
	return out
}

// BackfillStatements 返回 D2 的分批回填语句（**不在 `Statements()` 里**）。
//
// 为什么单独一个函数：§6.5 的实测教训是「默认构建的测试曾把一条整表换算真的
// 作用在 392 万行上」。回填腿只允许出现在启动路径或 tagged 集成套件，因此
// 它与建表语句分开返回，调用方显式选择。
//
// 每条都满足：
//   - 哨兵防重放（`data_migrations` + `IF NOT EXISTS (SELECT 1 ...)`），
//     形状照 Migration 052/054；
//   - `WHERE visible_at IS NULL`，本身可重入；
//   - **绝不** `SET visible_at = trade_date` 一把梭（§6.4 禁止的第四档）。
//
// 档位按 §6.3/§6.4：
//   - `after_close` → 交易日 15:00 北京时间（= UTC 07:00，加 `AT TIME ZONE`
//     显式换算，不依赖服务器时区）；
//   - `announcement_day` → 锚点列本身（`ann_time` / `publish_time`）；
//   - `custom` 的 `fetch_time` 一族 → `ingest_time`；
//   - `ohlcv_minute` 的 `bar_period_end` 算不了（要频率 + 周期长度）→
//     `ingest_time` 兜底；
//   - `global_ohlcv` 是境外市场，A 股收盘时刻对它不成立 → `ingest_time`；
//   - `ohlcv_daily_qfq` 没有 `ingest_time` → `created_at`。
//
// `data_migrations` 用**不限定 schema** 的名字：今天的哨兵表在 `public`，
// 物理搬迁后随表走，语句不变。
func BackfillStatements() []string {
	// afterClose 是 §6.3 的收盘口径：交易日 15:00 北京时间。
	const afterClose = `(trade_date::timestamp + interval '15 hours') AT TIME ZONE 'Asia/Shanghai'`

	doc := func(name, table, expr string) string {
		return `DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM data_migrations WHERE name = '` + name + `') THEN
				UPDATE ` + table + ` SET visible_at = ` + expr + ` WHERE visible_at IS NULL;
				INSERT INTO data_migrations (name) VALUES ('` + name + `') ON CONFLICT (name) DO NOTHING;
			END IF;
		END $$`
	}

	out := make([]string, 0, len(aSetTables))
	out = append(out,
		doc("057a_visible_at_capital_flow", "capital_flow", afterClose),
		doc("057b_visible_at_top_list", "top_list", afterClose),
		doc("057c_visible_at_limit_up_pool", "limit_up_pool", afterClose),
		doc("057d_visible_at_announcements", "announcements", "ann_time"),
		doc("057e_visible_at_news", "news", "publish_time"),
		// fetch_time 一族：观察时刻即可见 ⇒ ingest_time。
		doc("057f_visible_at_realtime_quote", "realtime_quote", "ingest_time"),
		doc("057g_visible_at_sectors", "sectors", "ingest_time"),
		doc("057h_visible_at_stock_sector_map", "stock_sector_map", "ingest_time"),
		doc("057i_visible_at_hot_search", "hot_search", "ingest_time"),
		// bar_period_end 需要频率 + 周期长度，今天算不了 ⇒ 保守兜底。
		doc("057j_visible_at_ohlcv_minute", "ohlcv_minute", "ingest_time"),
		// 境外市场：A 股 15:00 对它不成立。
		doc("057k_visible_at_global_ohlcv", "global_ohlcv", "ingest_time"),
		// 无 ingest_time，用 created_at 顶第一档（列注释须写明它是代理）。
		doc("057l_visible_at_ohlcv_daily_qfq", "ohlcv_daily_qfq", "created_at"),
	)
	return out
}
