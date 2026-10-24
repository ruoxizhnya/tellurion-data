---
status: active
last-verified: 2026-10-24
verified-by: QT-40 G4 —— 写面（`Registry.Fetch` / `ETLPipeline` / `GatePoints` / `Deduplicate` / `store.BulkInsert`）落到本仓，链路、失败模式与不变量同步
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
[存储]  market / ingest（目标态；今天多数表落 public，X1d 分批迁，现状见 db-schema.md §1）  ← 内容真相源，DDL 唯一 owner = 本仓；写面 = `BulkInsert`
        ↓
[aggregator]  PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存   ← 对外唯一面
        ↓  /datasets/resolve · /datasets/{hash}
core（回测/实盘）   analytics（基准/归因）   strategy（资产）   agent（实验）

（过渡第二路径，X1d 收敛）core ──直读──▶ factor_definitions / factor_cache（因子读面只在 core，本仓不提供）
```

> **频率分表与聚合口径**（`tellurion:docs/SPEC.md` 频率词表）：`1m` = `ohlcv_minute` 原样；`1d` = `ohlcv_daily_qfq`（唯一进回测的口径）；`15m` **由 1m 聚合产出、不物化**，每次取数实时聚合。图中与 §2 的「频率聚合」只指这一种。
> **代码落点（2026-10-24，G4）**：图中「归一化」的聚合与 PIT 两段在 `tellurion-data:internal/normalize`，**ETL 编排（`Registry.Fetch` / `ETLPipeline` / `ValidatePoints` / `GatePoints` / `Deduplicate`）在 `tellurion-data:internal/ingest`**；「aggregator」在 `tellurion-data:internal/serving`；存储的**读面（四个取数 port）与写面（`BulkInsert`）都在** `tellurion-data:internal/store`（`visible_at` 只从列里取、缺列交 PIT 整批拒收；写面只落 A 集表）。**仍在 core**：connector 的传输实现（Tushare/东财/Mootdx 的 HTTP/SDK 客户端与 `ingest.raw` 归档，经 `ingest.Transports` 注入）与同步作业的装配（`sync_jobs` → worker → `ETLPipeline`）；core 侧 `pkg/data/source` 的旧写路径尚未删。
> **因子边（裁-14，2026-10-25）**：本仓**不提供因子读面**——`/factors/{name}@{hash}` 已撤掉。因子**值**与**定义**的生产方和读端点都在 core（`pkg/data` → `SaveFactorCacheBatch`；`cmd/data/handlers_factor.go`），今天 core 直读 `factor_definitions` / `factor_cache`（过渡路径，如实登记上图）。**上移触发条件（任一成立再启动整体迁移）**：① C 类生产需要横向扩容；② 出现第二个不接受 core API 的消费方。常驻 404 比没有路由更坏——调用方会把 404 读成「该数据集没有该因子」。

## 2. 关键时序

### 取数（读路径，热）
1. 调用方 `GET /datasets/resolve?universe=csi300&from=…&to=…&freq=1d`
2. aggregator：票池解析 → 按 `visible_at <= as_of` 过滤 → 频率聚合（口径见 §1 注）→ 算 `dataset_hash`
3. 返回 `{dataset_hash, metadata}`；调用方拿 hash 请求 `/datasets/{hash}`
4. 命中缓存直接返回；未命中从 `market` 读并回填

> **红线**：热读路径**仅供 run 前一次快照预加载**（回测启动前取完全区间进引擎快照），**禁止在回测循环内调用**——策略运行时不得直查数据面（`tellurion:docs/SPEC.md`「快照预加载，不许运行时查询」）。

### 入库（写路径，冷）
1. 触发：`POST /api/sync/jobs`（或调度器按 `sync_schedules` 投递）→ `sync_jobs` → worker 领取 → executor → ETL（下面 2–6 步）
2. connector 取数（`ingest.Registry.Fetch`：按 data_type 的链依次尝试，可重试错误换源、不可重试立即返回、disabled 跳过）→ 声明 `TimeSemantics` / `FrequencySpec`
3. `Normalizer` 映射成 `UnifiedDataPoint`（`ingest_time` 由 pipeline 时钟统一填；载荷为 nil 的点只计入 `skipped`）
4. `ValidatePoints` 拒收形态不合法（缺 symbol / trade_time / data）
5. **`GatePoints`**（`internal/ingest`）按该 data_type 的**声明锚点**逐点校验：任一点缺锚点 ⇒ `ErrVisibleAtMissing`、坏值 ⇒ `ErrAnchorInvalid`，**整批拒收且不触达 store**
6. `Deduplicate` 按幂等坐标（symbol + data_type + trade_time）去重 → `store.BulkInsert` 写 A 集表：`ON CONFLICT DO NOTHING` + 内容比对（排除易变列 `ingest_time`）；同内容 no-op、同主键异内容 `ErrContentConflict` 整批回滚；B/C 集 data_type 拒写（`ErrWriteTableNotInASet` / `ErrUnmappedDataType`）。`persisted` 口径 = **请求点已就位**（新插入 + 同内容 no-op），不是新增行数

> **红线**：同步 HTTP 端点**不得承载无界批量**——无界批量一律走作业层（`POST /api/sync/jobs` → `sync_jobs` → worker → executor → ETL，`tellurion:docs/SPEC.md` DATA-10）；HTTP 灌数入口只许单次有界任务（DATA-12：以「没有无界批量问题」为前提）。

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

## 4. 不变量（可被测试钉住的）

1. 同一 `dataset_hash` 两次拉取字节一致。
2. 任何返回点满足 `visible_at <= as_of`。
3. `market` / `ingest` 无第二写者（CI 静态检查 + 评审）。
4. connector 全部走 `ConnectorDeclaration`（漏声明被拒收）。
5. 写面幂等：同主键同内容重写 = no-op；同主键异内容 = fail-loud 且整批不落（`ON CONFLICT DO NOTHING` + 内容比对，排除 `ingest_time`）。
6. 写面只落 A 集（`visible_at` 三集合门）；B/C 集只能走各自同步腿或被明确豁免。
7. 本仓**不提供因子读面**（裁-14）：路由表里没有 `/factors/*`，因子值 / 定义一律由 core 提供。

## 5. 与契约文档的关系

- 表结构 → `docs/db-schema.md`
- 接口形状 → `docs/api-schema.md`
- 本文件只讲**流向与顺序**，不重复字段定义。
