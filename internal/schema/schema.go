// Package schema 是 `market` / `ingest` 两个 schema 的 **DDL 唯一 owner**
// （`docs/db-schema.md` §1、§4，X1d）。
//
// 三条纪律（照 core 的内联 DDL 口径平移，不另造结构）：
//  1. 唯一执行路径是 `Statements()` 返回的**有序幂等语句**——数组顺序就是
//     依赖顺序（先 schema、再表、再列、最后索引）。本仓不使用 `.sql` 文件做
//     执行源（`docs/db-schema.md` §4 的取向 ①：core 式数组 + 启动执行）。
//  2. 只前滚：已发布的语句不改，修错靠**追加**一条；每条必须可重入
//     （`IF NOT EXISTS` / `IF EXISTS`），能在空库上一次跑完。
//  3. **迁移不是改名**：今天物理表大多在 `public`。本包的 market.\* 目标是
//     归属声明，物理搬迁是独立一步（`docs/db-schema.md` §1 注、§6.5 D4），
//     不在 `Statements()` 里静默 `ALTER TABLE ... SET SCHEMA`。
//
// 与 core 的关系：X1d 之后 `market` / `ingest` 只有本仓写。core 的内联 DDL
// 在物理搬迁完成前仍是运行期事实，这段差异在 `docs/db-schema.md` §1 与
// `docs/TASKS.md` 的未落项里如实登记——**不在这里假装已经搬完**。
package schema

// Statements 返回本仓拥有的全部建表/加列/索引语句（有序、幂等）。
//
// 顺序不可换：`CREATE SCHEMA` 必须在任何 `market.` / `ingest.` 表之前；
// `visible_at` 加列必须在表存在之后（`ALTER TABLE IF EXISTS` 兜住"表还没建"
// 的空库首跑）。
func Statements() []string {
	out := make([]string, 0, 64)
	out = append(out, MarketStatements()...)
	out = append(out, IngestStatements()...)
	out = append(out, VisibleAtStatements()...)
	return out
}

// TableNames 返回本仓 DDL 覆盖的**物理表名**（含 schema 前缀，排序后）。
//
// 用途是让文档与测试有一个可对账的清单：新增表却忘了登记 → 测试红。
func TableNames() []string {
	return []string{
		"ingest.data_migrations",
		"ingest.raw",
		"ingest.sync_jobs",
		"ingest.sync_schedules",
		"market.announcements",
		"market.capital_flow",
		"market.dividends",
		"market.global_ohlcv",
		"market.hot_search",
		"market.index_constituents",
		"market.index_daily",
		"market.limit_up_pool",
		"market.market_activity",
		"market.news",
		"market.ohlcv_daily_qfq",
		"market.ohlcv_minute",
		"market.realtime_quote",
		"market.sectors",
		"market.splits",
		"market.stock_sector_map",
		"market.stocks",
		"market.top_list",
		"market.trading_calendar",
	}
}
