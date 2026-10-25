---
status: active
last-verified: 2026-10-25
verified-by: QT-40 H4 ②（写端点 + `internal/assembly` 装配层，链路多一条跨仓边）；W4 鉴权接线（kit net/http 适配 + 路由策略表 + REV-17 移植）
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
        │ 两条入口在这里汇成**一条**链：HTTP 投点 / 未来的作业层（ADR-0006）
        ↓
[存储]  market / ingest（目标态；今天多数表落 public，X1d 分批迁，现状见 db-schema.md §1）  ← 内容真相源，DDL 唯一 owner = 本仓；写面 = `BulkInsert`
        ↓
[aggregator]  PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存   ← 对外唯一面
        ↓  /datasets/resolve · /datasets/{hash}
core（回测/实盘）   analytics（基准/归因）   strategy（资产）   agent（实验）

写路径的跨仓边（QT-40 H4，2026-10-25 起）：
  core 的作业层（取数 + 归一，传输仍在 core）
      ──POST /ingest/points（≤10000 点、≤8 MiB；`visible_at` / `ingest_time` 不在投送面上）──▶
  [装配] → [存储]
  超限 / 缺锚点 / 换源冲突 ⇒ **整批拒收**（413 / 400 / 409），一行都不落

（过渡第二路径，X1d 收敛）core ──直读──▶ factor_definitions / factor_cache（因子读面只在 core，本仓不提供）
```

> **频率分表与聚合口径**（`tellurion:docs/SPEC.md` 频率词表）：`1m` = `ohlcv_minute` 原样；`1d` = `ohlcv_daily_qfq`（唯一进回测的口径）；`15m` **由 1m 聚合产出、不物化**，每次取数实时聚合。图中与 §2 的「频率聚合」只指这一种。
> **代码落点（2026-10-25，H4 ②）**：图中「归一化」的聚合与 PIT 两段在 `tellurion-data:internal/normalize`，**ETL 编排（`Registry.Fetch` / `ETLPipeline` / `ValidatePoints` / `GatePoints` / `Deduplicate`）在 `tellurion-data:internal/ingest`**；**「装配」在 `tellurion-data:internal/assembly`**——ADR-0004 第 2 条那句「投影在装配层完成」的那个装配层，两条入口（`POST /ingest/points` 与 `Pipeline`）共用它，免得「跳过 / 去重 / 闸门」的计数口径分叉；「aggregator」在 `tellurion-data:internal/serving`；存储的**读面（四个取数 port）与写面（`BulkInsert`）都在** `tellurion-data:internal/store`（`visible_at` 只从列里取、缺列交 PIT 整批拒收；写面只落 A 集表）。**仍在 core**：connector 的传输实现（Tushare/东财/Mootdx 的 HTTP/SDK 客户端与 `ingest.raw` 归档，经 `ingest.Transports` 注入）与同步作业的**调度**（`sync_jobs` → worker → executor）。**capital_flow / top_list 两条作业的落库那一跳已改投本仓**（core 不再对这两张表 `ON CONFLICT DO UPDATE`）；其余同步腿（日线 / 板块 / 财报…）仍由 core 直写 `public` —— 点数以 grep 为准，见 §4 不变量 3 的实测口径。
> **因子边（裁-14，2026-10-25）**：本仓**不提供因子读面**——`/factors/{name}@{hash}` 已撤掉。因子**值**与**定义**的生产方和读端点都在 core（`pkg/data` → `SaveFactorCacheBatch`；`cmd/data/handlers_factor.go`），今天 core 直读 `factor_definitions` / `factor_cache`（过渡路径，如实登记上图）。**上移触发条件（任一成立再启动整体迁移）**：① C 类生产需要横向扩容；② 出现第二个不接受 core API 的消费方。常驻 404 比没有路由更坏——调用方会把 404 读成「该数据集没有该因子」。

## 2. 关键时序

### 取数（读路径，热）
0. **鉴权闸**（2026-10-25）：每个请求先过 `requireAuth`——`/health` 公开；`/datasets/*` 需 `viewer` 及以上。
   未装配 Authorizer → 503 `auth_unavailable`，无凭证 / 坏 token → 401，越权 → 403；通过后才进入 aggregator。
1. 调用方 `GET /datasets/resolve?universe=csi300&from=…&to=…&freq=1d`
2. aggregator：票池解析 → 按 `visible_at <= as_of` 过滤 → 频率聚合（口径见 §1 注）→ 算 `dataset_hash`
3. 返回 `{dataset_hash, metadata}`；调用方拿 hash 请求 `/datasets/{hash}`
4. 命中缓存直接返回；未命中从 `market` 读并回填

> **红线**：热读路径**仅供 run 前一次快照预加载**（回测启动前取完全区间进引擎快照），**禁止在回测循环内调用**——策略运行时不得直查数据面（`tellurion:docs/SPEC.md`「快照预加载，不许运行时查询」）。

### 入库（写路径，冷）
1. 触发：`POST /api/sync/jobs`（在 **core**，或调度器按 `sync_schedules` 投递）→ `sync_jobs` → worker 领取 → executor → ETL（下面 2–6 步）
   - **跨仓形态（H4 起，capital_flow / top_list 两条）**：core 的 executor 只跑 2–3 步（取数 + 归一），然后把点位 `POST /ingest/points` 投进本仓，**4–6 步在本仓进程内执行**。判据是「闸门与幂等在落库那一侧」——投送方不该能声明「我这批是可见的」。
2. connector 取数（`ingest.Registry.Fetch`：按 data_type 的链依次尝试，可重试错误换源、不可重试立即返回、disabled 跳过）→ 声明 `TimeSemantics` / `FrequencySpec`
3. `Normalizer` 映射成 `UnifiedDataPoint`（`ingest_time` 由 pipeline 时钟统一填；载荷为 nil 的点只计入 `skipped`）
4. `ValidatePoints` 拒收形态不合法（缺 symbol / trade_time / data）
5. **`GatePoints`**（`internal/ingest`）按该 data_type 的**声明锚点**逐点校验：任一点缺锚点 ⇒ `ErrVisibleAtMissing`、坏值 ⇒ `ErrAnchorInvalid`，**整批拒收且不触达 store**
6. `Deduplicate` 按幂等坐标（symbol + data_type + trade_time）去重 → `store.BulkInsert` 写 A 集表：`ON CONFLICT DO NOTHING` + 内容比对（排除易变列 `ingest_time`）；同内容 no-op、同主键异内容 `ErrContentConflict` 整批回滚；B/C 集 data_type 拒写（`ErrWriteTableNotInASet` / `ErrUnmappedDataType`）。`persisted` 口径 = **请求点已就位**（新插入 + 同内容 no-op），不是新增行数

> **红线**：同步 HTTP 端点**不得承载无界批量**——无界批量一律走作业层（`POST /api/sync/jobs` → `sync_jobs` → worker → executor → ETL，`tellurion:docs/SPEC.md` DATA-10）；HTTP 灌数入口只许单次有界任务（DATA-12：以「没有无界批量问题」为前提）。
> **这条红线现在执法在本仓**（2026-10-25，H4 ②）：`POST /ingest/points` 有 `assembly.MaxPointsPerRequest = 10000` 与 8 MiB 报文上限两道门，超限**整批拒收 413**，既不截断也不「写前 N 条」。代价如实登记：调用方（core 的作业层）必须按上界分片，一片一个请求，于是一次作业跨请求失败时**已落的那片不回滚**——作业层的「整池一次」原子性从来就不在跨仓这一侧，读 `sync_jobs.error_message` 时要按分片理解。

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
| 换源写同一主键 | core 老语义是 `DO UPDATE` 静默覆盖；本仓是响亮失败 | 409 `content_conflict` + 整批回滚。⚠️ 这是**行为变更**，不是等价搬迁（ADR-0006「代价」）：上游修正历史数值会一直 409，直到走一次受控修正流程 |
| 写端点未接线（无 `DATABASE_URL`） | 请求打到一个不会落库的进程 | 503 `dependency_unavailable`（用例 `TestIngestPointsUnwiredFailsClosed`），与读面同一档 fail-closed |

## 4. 不变量（可被测试钉住的）

1. 同一 `dataset_hash` 两次拉取字节一致。
2. 任何返回点满足 `visible_at <= as_of`。
3. `market` / `ingest` 无第二写者（CI 静态检查 + 评审）。
   ⚠️ **今天只对两条链成立**（2026-10-25 实测，H4 ② 落账）：`capital_flow` / `top_list` 的写入方只有本仓；core 仍在写 `ohlcv_daily_qfq`（`tellurion:pkg/storage/ohlcv.go:15,53`）、`sectors` 与 `stock_sector_map`（`tellurion:pkg/storage/sector.go:56,96`），另有两处带 schema 前缀的写点 `tellurion:pkg/storage/index_daily.go:31`（`market.index_daily`）与 `tellurion:pkg/storage/ingest_raw.go:62`（`ingest.raw`）。这条不变量**不作为已完成项**；判点数以 grep 为准，不引用任何文档里写死的数（含本行）。
4. connector 全部走 `ConnectorDeclaration`（漏声明被拒收）。
5. 写面幂等：同主键同内容重写 = no-op；同主键异内容 = fail-loud 且整批不落（`ON CONFLICT DO NOTHING` + 内容比对，排除 `ingest_time`）。
6. 写面只落 A 集（`visible_at` 三集合门）；B/C 集只能走各自同步腿或被明确豁免。
7. 本仓**不提供因子读面**（裁-14）：路由表里没有 `/factors/*`，因子值 / 定义一律由 core 提供。
8. 写面**有界**：单次 `POST /ingest/points` ≤ `assembly.MaxPointsPerRequest`（10000 点）且 ≤ 8 MiB 报文；超限整批拒收，不截断（`docs/dataflow.md` §2 红线的执法点）。
9. **可见时刻与入库时刻只由本仓推导**：`visible_at` 来自声明锚点（`GatePoints`），`ingest_time` 来自本进程时钟；投送面上没有这两个字段。
10. **两条入口共用一条写侧链**：HTTP 投点（`assembly.Write`）与作业管线（`assembly.Pipeline`）对同一批数据必须产出逐字段相同的 `store.Point`（用例 `TestWriteAndPipelineAgreeOnTheSameBatch`）——分叉就是本仓内部复活两个真相。

## 5. 与契约文档的关系

- 表结构 → `docs/db-schema.md`
- 接口形状 → `docs/api-schema.md`
- 本文件只讲**流向与顺序**，不重复字段定义。
