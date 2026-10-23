# 0004 · 写面归属 `internal/store`，幂等用内容比对而非 last-write-wins

- **状态**：已裁决（X1d 写面，2026-10-24）
- **场景**：X1d 要把 core `tellurion:pkg/data/source/etl.go` 的写侧语义搬进本仓。两个必须落定的选择：① `BulkInsert` 归哪一层——`internal/ingest` 还是 `internal/store`；② 幂等语义沿用 core `pkg/storage/bulk_insert.go` 的 `ON CONFLICT DO UPDATE`（后写覆盖），还是改成别的。这两个选择一旦被跨仓消费方依赖，改回来就是 breaking。
- **决策**：
  1. **写面归 `internal/store`**：`BulkInsert(ctx, dataType, []store.Point)` 知道表名、列集合、主键、冲突 SQL 与日期列比较——这些是**存储语义**，不是采集语义。ETL 编排（`Registry.Fetch` / `Normalizer` / `ValidatePoints` / `GatePoints` / `Deduplicate`）归 `internal/ingest`。
  2. **两侧不互相 import**：`ingest` 定义窄接口 `BulkInserter`（入参是 `ingest.UnifiedDataPoint`），`store` 定义本地镜像 `store.Point` 并实现同形方法；投影在未来的装配层完成。禁止新增 `ingest → store` 或 `store → ingest` 的包边（ARCHITECTURE §2.1 断言 1）。
  3. **写面只开 A 集**：`store.specFor` 用 `schema.GateClassFor` 复核目标表分类。B 集（`market_activity` / `quant.macro_series`）承认现成 `as_of`，走各自同步腿；C 集与未知 data_type 一律拒写（`ErrWriteTableNotInASet` / `ErrUnmappedDataType`），不猜列。
  4. **幂等 = `ON CONFLICT DO NOTHING` + 全列内容比对**（排除易变列 `ingest_time`）：同主键同内容 ⇒ no-op 且计入 `persisted`；同主键异内容 ⇒ `ErrContentConflict` + **整批回滚**。`persisted` 口径是「请求点已就位」，不是「新增行数」；`skipped` 只计坏载荷单行。
  5. **`visible_at` 两道门**：`ingest.GatePoints` 按声明锚点逐点校验（失败不触达 store）；`store.BulkInsert` 在开事务前再判一次 `Point.VisibleAt` 为零即整批拒收——防的是绕过 ETL 直接调 store 的装配。
- **理由**：① 写面按表结构工作，放 `ingest` 会让采集包替数据库决定列与冲突策略，方向反了；② 后写覆盖会让上游修正历史数据时**静默改写已有行**，结果是「同一 hash 对应两份不同内容」——这正是 `api-schema.md` §3 内容寻址要防的事。内容比对把「改了历史数据」从静默变成显式失败，由上游决定是否走一次受控修正（未来另立流程），而不是顺手覆盖。
- **代价 / 后果**：① 过渡期 core 仍用 `DO UPDATE` 写同一批表，**同表两种幂等语义并存**——已登记为未落项，随物理搬迁（D4）收敛；② 内容比对依赖列集合，表结构变更必须同步 `store.writeSpecs`（`TestWriteSpecs_MatchASetAndConnectorTypes` 已钉住 12 张 A 集表与 connector data_type 的对应）；③ 排除 `ingest_time` 意味着「仅入库时刻变化」不算内容变化，这是有意的——否则每次重跑都会冲突；④ 同主键异内容会阻塞整批（含同批里本可写入的点），这是 fail-loud 的代价，换的是不静默覆盖。
- **替代关系**：补充 0002（`visible_at` 三集合）与 0003（传输留在 core），不取代它们；core `pkg/storage/bulk_insert.go` 的 `DO UPDATE` 语义在本仓**不继承**。
