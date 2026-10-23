# 0002 · market / ingest 的 DDL 归属本仓 + visible_at 三集合

- **状态**：已裁决（来源：ADR-D19 / `data-service-contract-and-ownership.md` §1「DDL 只有一个 owner」；`docs/db-schema.md` §6）
- **场景**：`market` / `ingest` 的表**今天建在 core 的内联 `migrationStatements()` 里**，而写者横跨 core 与数据服务两条腿——同一个 schema 两处建、两处写，正是 X1d「消灭第二写者」要收的形状。同时 §3 红线 2 的 `visible_at` 门要上线，已落库的行却**没有任何一列承载可见时刻**：门与列必须同批设计，否则门一开就把历史数据全判违规。
- **决策**：
  1. `market` / `ingest` 的 DDL **归属本仓** `internal/schema`（`Statements()` 有序幂等数组，core 式机制 ①，不引迁移库）；
  2. `visible_at` 按 §6.2 的 **A/B/C 三集合**执行：A 集 12 张加可空 `TIMESTAMPTZ` 列、B 集 2 张**承认现成 `as_of`**、C 集逐个写明门外理由；
  3. 回填（D2）与建表**分开返回**（`BackfillStatements()`，哨兵防重放 + `WHERE visible_at IS NULL` 可重入），默认测试不触碰；
  4. **禁止** `UPDATE ... SET visible_at = trade_date` 一把梭（§6.4 第四档）。
- **理由**：归属只有一个 owner，才谈得上「单一写者」；三集合而不是「所有表加一列」，是因为判据取**在册声明**（15 个 wire `data_type`）而非 `TableMapper` 的 13 张表——两者差 `macro_series` 与 `daily_basic`，那正是门最容易漏掉的覆盖面。
- **代价 / 后果**：① D4 物理搬迁完成前，本仓数组与 core 内联数组**双份并存**（已登记的过渡状态，不是新写者）；② `visible_at` 今天在 `market.*` 与 `public.*` 各加一次（目标 + 今天），搬迁时列随表走；③ B 集与 `market.index_daily` 不走 BulkInsert，写入闸门**拦不到**它们，必须显式豁免或另写执法点，不许默认「门已全覆盖」；④ 历史行不可回填的部分留 NULL，读路径必须显式排除。
- **替代关系**：补充 0001（协议必填 `visible_at`），不取代它