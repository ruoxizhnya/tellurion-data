---
status: active
last-verified: 2026-11-11
verified-by: 代码-文档对齐审计 —— 传输/调度归属按 `a96d836` 后代码订正；作业层链路与 `/api/sync/*` 实际接线核对
---

# Dataflow — tellurion-data

> 目的：把「数据从哪来、在谁手里变形、到哪去」写死。**任何跨仓数据交换都必须能在本图上找到一条边**；找不到 = 这段代码在偷偷建第二条路。

## 1. 主链路

```
[Tushare] [东财] [雪球/Yahoo] [巨潮/HKEX] …        ← connector（12 个，可增）
        │ 字段映射 + 声明 TimeSemantics / FrequencySpec
        ↓
[归一化]  Normalizer（共享函数） + ValidatePoints + GatePoints + Deduplicate
        │ 统一成 UnifiedDataPoint（含 visible_at、幂等坐标）
        ↓
[装配]  internal/assembly —— `ingest.UnifiedDataPoint → store.Point` 的唯一投影处
        │ 两条入口在这里汇成**一条**链：HTTP 投点 / 作业层（2026-11-10 起在本仓，ADR-0006）
        ↓
[存储]  market / ingest（本机实例已于 2026-11-10 完成 `SET SCHEMA`，现状见 db-schema.md §1/§9）  ← 内容真相源，DDL 唯一 owner = 本仓；写面 = `BulkInsert`
        ↓
[aggregator]  PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存   ← 对外唯一面
        ↓  /datasets/resolve · /datasets/{hash}
core（回测/实盘）   analytics（基准/归因）   strategy（资产）   agent（实验）

写路径的跨仓边（2026-11-10 起）：
  本仓作业层（`/api/sync/jobs` → worker：取数 + 归一，传输在本仓）
      ──POST /ingest/points（≤10000 点、≤8 MiB；`visible_at` / `ingest_time` 不在投送面上）──▶
  [装配] → [存储]
  超限 / 缺锚点 / 换源冲突 ⇒ **整批拒收**（413 / 400 / 409），一行都不落

（过渡路径，未收敛）因子读面：core `cmd/analysis` 的 `/api/factors/:name` 仍转发到本仓 `/factors/:name`，而本仓已按裁-14 撤掉该路由 ⇒ 这条边**目前未接线**（见 §5 待裁）。
```

> **频率分表与聚合口径**（`tellurion:docs/SPEC.md` 频率词表）：`1m` = `ohlcv_minute` 原样；`1d` = `ohlcv_daily_qfq`（唯一进回测的口径）；`15m` **由 1m 聚合产出、不物化**，每次取数实时聚合。图中与 §2 的「频率聚合」只指这一种。
> **代码落点（2026-10-25，H4 ②）**：图中「归一化」的聚合与 PIT 两段在 `tellurion-data:internal/normalize`，**ETL 编排（`Registry.Fetch` / `ETLPipeline` / `ValidatePoints` / `GatePoints` / `Deduplicate`）在 `tellurion-data:internal/ingest`**；**「装配」在 `tellurion-data:internal/assembly`**——ADR-0004 第 2 条那句「投影在装配层完成」的那个装配层，两条入口（`POST /ingest/points` 与 `Pipeline`）共用它，免得「跳过 / 去重 / 闸门」的计数口径分叉；「aggregator」在 `tellurion-data:internal/serving`；存储的**读面（四个取数 port）与写面（`BulkInsert`）都在** `tellurion-data:internal/store`（`visible_at` 只从列里取、缺列交 PIT 整批拒收；写面只落 A 集表）。**传输与调度在本仓（2026-11-10 起）**：`internal/transport/*` 持有 connector 的 HTTP/SDK 客户端，`internal/sync` 持有 `sync_jobs` → worker → executor 的调度；core 的 `pkg/data` / `cmd/data` 已于 `a96d836`（2026-11-08）整体删除，**core 对 `market.*` / `ingest.*` 的同步写点归零**（裁决 ⑰ 的 `market.index_daily` 除外）——点数以 grep 为准，见 `db-schema.md` §9.4。
> **因子边（裁-14，2026-10-25）**：本仓**不提供因子读面**——`/factors/{name}@{hash}` 已撤掉。因子**值**的写在 core（`pkg/storage/cache.go` 的 `SaveFactorCacheBatch` 等，写 `factor_cache` / `factor_returns` / `ic_analysis`）；`pkg/data` 与 `cmd/data/handlers_factor.go` 已于 `a96d836` 删除。⚠️ 但 core 的 HTTP 门面 `cmd/analysis/handlers_proxy.go` 仍把 `/api/factors/:name` 转发到本仓 `/factors/:name` ⇒ **该跨仓边未接线**，属待裁项（见 §5）。**上移触发条件（任一成立再启动整体迁移）**：① C 类生产需要横向扩容；② 出现第二个不接受 core API 的消费方。常驻 404 比没有路由更坏——调用方会把 404 读成「该数据集没有该因子」。

## 2. 关键时序

### 取数（读路径，热）
0. **鉴权闸**（2026-10-25，2026-11-11 按 `servedPolicy` 校准）：每个请求先过 `requireAuth`——`/health` / `/readyz` 公开；`/datasets/*` 与 7 条 G3 只读面接受 `service` / `viewer` / `trader` / `admin`；`POST /api/v1/ohlcv/bulk` 任意已认证；`POST /ingest/points` **只有** `service` / `admin`；`/api/sync/*` 作业创建/取消/重试与 `schedules/{id}/run` = `trader` / `admin`，排程增删改/切换 = `admin`，读面 = `service` / `viewer` / `trader` / `admin`（逐条见 `api-schema.md` §1）。
   未装配 Authorizer → 503 `auth_unavailable`，无凭证 / 坏 token → 401，越权 → 403；通过后才进入 aggregator。无 secret 时是第 2 态：**回环绑定**下读面公开、写面 503 `auth_unavailable`，非回环绑定且未声明则拒绝启动；只有逐字声明 `AUTH_INSECURE_EXPOSURE=loopback-published` 才进入全放行的第 3 态。
1. 调用方 `GET /datasets/resolve?universe=csi300&from=…&to=…&freq=1d`
2. aggregator：票池解析 → 按 `visible_at <= as_of` 过滤 → 频率聚合（口径见 §1 注）→ 算 `dataset_hash`
3. 返回 `{dataset_hash, metadata}`；调用方拿 hash 请求 `/datasets/{hash}`
4. 命中缓存直接返回；未命中从 `market` 读并回填

> **红线**：热读路径**仅供 run 前一次快照预加载**（回测启动前取完全区间进引擎快照），**禁止在回测循环内调用**——策略运行时不得直查数据面（`tellurion:docs/SPEC.md`「快照预加载，不许运行时查询」）。

### 入库（写路径，冷）
1. 触发：`POST /api/sync/jobs`（本仓作业层；调度器按 `sync_schedules` 投递）→ `sync_jobs` → worker 领取 → executor → ETL（下面 2–6 步）
   - **历史形态（H4，capital_flow / top_list）**：core 的 executor 只跑 2–3 步（取数 + 归一），再 `POST /ingest/points` 投进本仓——该形态已随 core `pkg/data` 退役（`a96d836`，2026-11-08）消失。今天 2–6 步都在本仓进程内；`POST /ingest/points` 保留为「别人取数、本仓落库」的接缝，走同一条链的后半段（ADR-0006）。判据不变：闸门与幂等在落库那一侧。
2. connector 取数（`ingest.Registry.Fetch`：按 data_type 的链依次尝试，可重试错误换源、不可重试立即返回、disabled 跳过）→ 声明 `TimeSemantics` / `FrequencySpec`
3. `Normalizer` 映射成 `UnifiedDataPoint`（`ingest_time` 由 pipeline 时钟统一填；载荷为 nil 的点只计入 `skipped`）
4. `ValidatePoints` 拒收形态不合法（缺 symbol / trade_time / data）
5. **`GatePoints`**（`internal/ingest`）按该 data_type 的**声明锚点**逐点校验：任一点缺锚点 ⇒ `ErrVisibleAtMissing`、坏值 ⇒ `ErrAnchorInvalid`，**整批拒收且不触达 store**
6. `Deduplicate` 按幂等坐标（symbol + data_type + trade_time）去重 → `store.BulkInsertWithOptions` 写 A 集表：`ON CONFLICT DO NOTHING` + 内容比对（排除易变列 `ingest_time`）；同内容 no-op、同主键异内容 `ErrContentConflict` 整批回滚；`extra.overwrite=true` 时改为 `ON CONFLICT (pk) DO UPDATE`（覆盖重算，ADR-0011）；B/C 集 data_type 拒写（`ErrWriteTableNotInASet` / `ErrUnmappedDataType`）。`persisted` 口径 = **请求点已就位**（新插入 + 同内容 no-op），不是新增行数

> **红线**：同步 HTTP 端点**不得承载无界批量**——无界批量一律走作业层（`POST /api/sync/jobs` → `sync_jobs` → worker → executor → ETL，`tellurion:docs/SPEC.md` DATA-10）；HTTP 灌数入口只许单次有界任务（DATA-12：以「没有无界批量问题」为前提）。
> **这条红线现在执法在本仓**（2026-10-25，H4 ②）：`POST /ingest/points` 有 `assembly.MaxPointsPerRequest = 10000` 与 8 MiB 报文上限两道门，超限**整批拒收 413**，既不截断也不「写前 N 条」。代价如实登记：调用方必须按上界分片，一片一个请求，于是一次作业跨请求失败时**已落的那片不回滚**——作业层的「整池一次」原子性从来就不在跨仓这一侧，读 `sync_jobs.error_message` 时要按分片理解。

### 参考数据（旁路点位链，ADR-0008 / ADR-0012）
`stocks` / `trading_calendar` / `index_constituents` / `fundamentals` 是 C 集参照表（无 `visible_at`、无 data_type 声明），不走上图，走一条短链：

1. `POST /api/sync/jobs` 或排程以**保留 job_type**（`stocks` / `trading_calendar` / `index_constituents` / `fundamentals`）入队；
2. `Runner.Execute` 在 `FetchRequestFromParams` **之前**分流给 `ReferenceRunner`（参考数据作业不会掉进点位管线）。`fundamentals` 与 data_type 同名，但参考腿优先；
3. `ReferenceTransport` 取数（`stock_basic` / `trade_cal` / `index_weight` / `fina_indicator`）→ `store.ReferenceWriter` 用 `ON CONFLICT` upsert：**后到者覆盖可变列**，不做内容比对整批回滚。
4. **字段契约 fail-closed**：`stock_basic` 必填 `ts_code` / `symbol` / `name` / `list_date` / `delist_date`，`trade_cal` 必填 `cal_date` / `is_open`；缺列或改名 ⇒ `ErrTransportDataQuality`，作业在开事务前失败（不会把退市票静默写成 `active`，也不会把整天静默写成非交易日）。多余列显式忽略。fixture 与线上核对见 `internal/sync/testdata/` + `internal/sync/reference_contract_test.go`。
5. **财报口径（2026-11-12，ADR-0012）**：`fina_indicator` 按**报告期**区间（`start_date` / `end_date` = 报告期截止日）一次拉全；`trade_date` = 报告期 `end_date`，`ann_date` 原样保留。PIT 只认 `available_date`（生成列 `COALESCE(ann_date, trade_date)`）——三季报 9/30 截止、10/25 披露，回测在 10/25 前看不到它。源端没给的字段落 `NULL`，**不折成 0**；响应 `fields` 会剔除全空列，解析必须按响应自报的列名建映射（按固定下标会把 `roe` 写进 `pe`）。另：`fina_indicator` **不提供 `pe/pb/ps`**（实测 `fields` 无这三列），估值字段需另接 `daily_basic`。

它不经过 `ValidatePoints` / `GatePoints` / `Deduplicate` / `BulkInsert`。参照表的语义是「后到者覆盖」而不是「历史不可改」，硬套点位闸门只会把合法的名称/状态变更判成冲突；财报更正/追溯调整同理。

### 复权基准与重算（ADR-0011）

**基准定义**：`前复权价 = 当日价格 × 当日复权因子 / 该 symbol 的全局基准因子`。基准因子由 `market.adjust_baseline_changes` 的最新一条记录固定，**不随 `start_date/end_date` 变化**——同一 `(symbol, trade_date)` 在任意窗口下 `close` 相同。

**建基准（只发生一次）**：取数时按序解析（`tushare.resolveBaseline`）：
1. 账本有该 symbol 的最新记录 ⇒ 用它；
2. 无账本但 `market.ohlcv_daily_qfq` 已有该 symbol 行 ⇒ 用 `MAX(trade_date)` 及其当日因子，append 一条 `reason=bootstrap`（老实现的基准就是当时窗口的最新交易日，那一行必然已落库，是对旧口径最忠实的重建）；
3. 首次入库 ⇒ 用本次窗口的最新因子，append 一条 `reason=bootstrap`。

基准一旦建立**不自动推进**：否则每个增量作业都要重写全部历史。`bootstrap` 每 symbol 只有一条（局部唯一索引），并发首拉时输的一方读回赢家的基准再算价格。

**改基准（显式、可审计）**：`POST /api/sync/jobs` 带 `extra.baseline_date` + `extra.overwrite=true`（缺一即 400）。取数按新基准重算，账本 append 一条 `reason=manual`、`previous_date/previous_factor` 齐全的记录；同一基准重放不追加。写入用 UPSERT 覆盖受影响区间，重跑幂等（行数不变、内容哈希不变）。变更后的价格与原口径的差异可由新旧基准因子算出（`close_new / close_old = base_old / base_new`）。

**为什么不能按窗口算**：qfq 基准随窗口漂移会让「同参回测」的结果取决于数据是分几次、按什么窗口拉进来的；且 `ON CONFLICT DO NOTHING` 下想对齐旧行必须先手工删行。两者都违背「数据面唯一写者、可审计」。

## 3. 失败模式（每一处 fail-closed 都要能指名）

| 失败点 | 症状 | 兜底 |
|---|---|---|
| connector 漏声明时间语义 | 数据可能前视 | `ErrDeclarationMissing` 在**注册期**拒收（`Registry.Register`，已有测试；旧名 `ErrNoTimeSemantics` 作废） |
| `visible_at` 用错锚点 | 回测虚高、实盘崩 | 归一化层强制：锚点=**披露日（ann_date）**，不是报告期截止日（`tellurion:docs/SPEC.md` 时间语义）+ 用例钉住财报场景 |
| 上游改了历史数据 | 同参回测结果漂移 | `dataset_hash` 变化 → 结果可追溯到"哪版数据" |
| 缓存串号 | 返回别人的数据 | `ErrSnapshotMismatch` + 500 |
| 批次漏 `visible_at` | 不可见的数据混进快照（前视） | `ingest.GatePoints` 按声明锚点逐点校验、`store.BulkInsert` 事务前再判一次；任一 offender ⇒ `ErrVisibleAtMissing` **整批拒收**（用例 `TestWriter_RejectsWholeBatchWithoutVisibleAt` / `TestETLProcess_MissingVisibleAnchorRejectsWholeBatchWithoutWriting` / PG 腿 `TestPG_BulkInsert_RejectsBatchWithoutVisibleAt`） |
| 同主键重写但内容不同 | 「同 hash 不同内容」静默出现 | `ErrContentConflict` + **整批回滚**（用例 `TestWriter_DifferentContentFailsLoudAndRollsBack` / `TestPG_BulkInsert_ConflictingContentRollsBackWholeBatch`）；同内容重写是 no-op |
| 往 B/C 集表写 | 同一张表两个可见性锚点 | `store.specFor` 复核 `schema.GateClassFor`：B/C ⇒ `ErrWriteTableNotInASet`，未知 ⇒ `ErrUnmappedDataType`；写面只开 A 集 |
| 因子读面误挂本仓 | 常驻 404 被读成「该数据集没有该因子」 | 裁-14 已撤路由；重新引入 `/factors/*` ⇒ `TestFactorRouteIsRemoved` 红（路由表与契约双向对拍） |
| 投点批次超上界 | 「无界批量」披成一次 HTTP 请求 | `assembly.MaxPointsPerRequest`（10000）+ 8 MiB 报文上限 ⇒ **整批拒收 413 `batch_too_large`**，不截断、不「写前 N 条」（用例 `TestWriteRejectsBatchAboveThePerRequestLimit` / `TestIngestPointsRejectsBatchOverTheLimitOnTheWire` / `TestIngestPointsRejectsOversizedBody`） |
| 调用方自己投 `visible_at` / `ingest_time` | 闸门拦不住自称「我可见」的批次 | 这两个字段不在投送面上：`DisallowUnknownFields` ⇒ 400 `invalid_parameter`（用例 `TestWirePointCarriesNoServerOwnedFields` / `TestIngestPointsRejectsServerOwnedFields`） |
| 换源写同一主键 | core 老语义是 `DO UPDATE` 静默覆盖；本仓是响亮失败 | 409 `content_conflict` + 整批回滚。⚠️ 这是**行为变更**，不是等价搬迁（ADR-0006「代价」）：上游修正历史数值会一直 409，直到走一次受控修正流程（`extra.overwrite=true`，ADR-0011） |
| 复权基准随取数窗口漂移 | 同一 `(symbol, trade_date)` 出现两个 `close`；扩窗补历史会改动旧价格，且 `DO NOTHING` 下补不回去 | 基准固定在 `market.adjust_baseline_changes`（ADR-0011）；用例 `TestFetchDailyOHLCV_BaselineIsPinnedAcrossWindows`；账本未装配 ⇒ 日线取数**直接失败**（不退回旧口径） |
| 基准变更后旧行写不进去 | 只能手工删行才能对齐口径 | `extra.overwrite=true` ⇒ `ON CONFLICT (pk) DO UPDATE`；PG 用例 `TestPG_BulkInsertWithOptions_OverwriteRewritesAndIsIdempotent`（重跑行数/哈希不变） |
| 写端点未接线（无 `DATABASE_URL`） | 请求打到一个不会落库的进程 | 503 `dependency_unavailable`（用例 `TestIngestPointsUnwiredFailsClosed`），与读面同一档 fail-closed |

## 4. 不变量（可被测试钉住的）

1. 同一 `dataset_hash` 两次拉取字节一致。
2. 任何返回点满足 `visible_at <= as_of`。
3. `market` / `ingest` 无第二写者（CI 静态检查 + 评审）。
   ✅ **2026-11-10 起成立**：core 数据面写点已随 `a96d836`（2026-11-08）全部删除；core 非测试代码只剩裁决 ⑰ 的 `market.index_daily`（`tellurion:pkg/storage/index_daily.go`，不在本仓 23 表内）。点数以 grep 为准，见 `db-schema.md` §9.4。
4. connector 全部走 `ConnectorDeclaration`（漏声明被拒收）。
5. 写面幂等：同主键同内容重写 = no-op；同主键异内容 = fail-loud 且整批不落（`ON CONFLICT DO NOTHING` + 内容比对，排除 `ingest_time`）。
6. 写面只落 A 集（`visible_at` 三集合门）；B/C 集只能走各自同步腿或被明确豁免。
7. 本仓**不提供因子读面**（裁-14）：路由表里没有 `/factors/*`，因子值 / 定义一律由 core 提供。
8. 写面**有界**：单次 `POST /ingest/points` ≤ `assembly.MaxPointsPerRequest`（10000 点）且 ≤ 8 MiB 报文；超限整批拒收，不截断（`docs/dataflow.md` §2 红线的执法点）。
9. **可见时刻与入库时刻只由本仓推导**：`visible_at` 来自声明锚点（`GatePoints`），`ingest_time` 来自本进程时钟；投送面上没有这两个字段。
10. **两条入口共用一条写侧链**：HTTP 投点（`assembly.Write`）与作业管线（`assembly.Pipeline`）对同一批数据必须产出逐字段相同的 `store.Point`（用例 `TestWriteAndPipelineAgreeOnTheSameBatch`）——分叉就是本仓内部复活两个真相。
11. **复权基准全局固定**：同一 `(symbol, trade_date)` 的 qfq 价与请求窗口无关（用例 `TestFetchDailyOHLCV_BaselineIsPinnedAcrossWindows`）；改基准只能走 `baseline_date + overwrite=true`，并在 `market.adjust_baseline_changes` 留下 `previous_*` 齐全的 manual 记录（ADR-0011）。

## 5. 与契约文档的关系

- 表结构 → `docs/db-schema.md`
- 接口形状 → `docs/api-schema.md`
- 本文件只讲**流向与顺序**，不重复字段定义。

### 5.1 跨仓死链的处置结论（2026-11-12 裁，已落地）

core `cmd/analysis/handlers_proxy.go` 曾无条件注册下列反代（`data_service.url` 缺省值 `http://data-service:8081`），但本仓路由表里没有对应端点。按**领域归属**逐条裁定，不是按「补一行最快」：

| core 对外路由 | 转发到 | 领域归属 | 结论 |
| --- | --- | --- | --- |
| `GET /api/sync/workers` | `data /api/sync/workers` | data（队列消费者在本进程） | ✅ **data 补正式读面**：`{num_workers, worker_ids, registered_types, is_running}`，运行态未注入 503 |
| `GET /api/sync/jobs/:id/progress` | `data /api/sync/jobs/{id}/progress` | data（作业状态在本进程） | ✅ **data 补正式读面**：SSE，500ms 一条，终态关闭；不存在在 SSE 头之前 404 |
| `GET /api/sync/scheduler/stats`（审计新发现） | 同上 | data（cron 在本进程） | ✅ **data 补正式读面**：`{active_schedules,total_entries,next_runs}` |
| `POST /api/screen` | `data /screen` | core（分析语义，因子/筛选） | **core 本地供数**；data 不提供该语义，`legacyREV17` 里的档位只是历史记录 |
| `GET /api/stocks/count` | `data /stocks/count` | data 已有读面可派生 | **不新增端点**：`GET /stocks` 增加 `latest_date`，count/by_status 由调用方派生 |
| `POST /api/ingest/raw` | `data /api/ingest/raw` | data 唯一写面 | data 的实际写面是 `POST /ingest/points`；**不得为一个路径名造第二个写入口**，两边对齐到同一真实路径 |
| `GET /api/factors/:factor_name` | `data /factors/:factor_name` | core（因子读面只在 core） | 维持裁-14 退役，路由不存在（不是恒 404 占位） |
| `POST /api/v1/ohlcv/bulk` | `data /api/v1/ohlcv/bulk` | data | ✅ 存在 |

本仓本次新增 3 条读面后共 **26 条路由**（`docs/api-schema.md` §1 为准）。守卫 `TestRouteTableEqualsServedPolicy` 逐条点名，漏登记策略或留幽灵路由都会红。

另：core `docs/openapi.yaml` 的 `/api/sync/{path}` 仍按旧 per-dataset 子路径（`/api/sync/stocks`、`/api/sync/ohlcv/all`、`/api/sync/fundamentals`、`/api/sync/calendar`、`/api/sync/dividends`、`/api/sync/splits`、`/api/sync/ingest/*`）描述，与本仓 X1c 的 `/api/sync/jobs|schedules` 不一致；同属待裁的跨仓契约分歧，未改动 core。
