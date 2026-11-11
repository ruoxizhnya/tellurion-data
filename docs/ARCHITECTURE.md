---
status: active
last-verified: 2026-10-24
verified-by: QT-40 H4 ② —— 写端点 `POST /ingest/points` + `internal/assembly` 装配层（ADR-0006），§2.1 包数与 §8 X1b 状态同步。上一版：QT-40 G4（写面落 `internal/store` + ETL 落 `internal/ingest`，ADR-0004）；P6（`visible_at` 回填仅在测试库经 tagged 套件执行）
---

# 架构 — tellurion-data

## 1. 定位

数据服务：**统一数据协议 + connector 归一化 + aggregator**。它存在的理由不是"把取数拆出去"，而是让**数据本身带上时间语义**，从根上堵住前视。

## 2. 分层

```
connector（12 个，可增）
      ↓  字段映射 + 声明 TimeSemantics/FrequencySpec
Normalizer + ValidatePoints + Deduplicate
      ↓  UnifiedDataPoint（含 visible_at）
market / ingest  （DDL 唯一 owner）
      ↓
aggregator  PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存
      ↓  对外唯一面
core / analytics / strategy / agent
```

### 2.1 六个限界上下文的包级隔离（对表 T4，2026-10-22 立）

> **这是目标分层，不是现状**——2026-10-25 实测：已建起 `internal/ingest` / `internal/normalize` / `internal/serving` 三个上下文包（`internal/store` 是存储读写实现、`internal/schema` 是 DDL 归属，二者不是限界上下文；**`internal/assembly` 也不是限界上下文**——它是「采集」与「存储」两个上下文之间那条被 ADR-0004 禁止直接互相 import 的**接缝**，唯一持有 `UnifiedDataPoint → store.Point` 的投影，见 ADR-0006；`internal/transport` / `internal/sync` 是基础设施包）；`internal/derived` / `internal/evidence` 仍是目标（派生读面与证据面今天在 core），`internal/sync` 已于 2026-11-10（X1c）在本仓落地。
> 义务来源是 `tellurion-docs:docs/adr/adr-027-modular-decomposition-to-independent-services.md` §2.6.2（data-service 的 **7 个**限界上下文里，只有 Reduction 物理独立，**其余 6 个在本仓内按包强制隔离** = 编译期边界 + 该 ADR §6 门禁断言），
> 由 `tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2 的 data 行点名：「`tellurion-data:docs/ARCHITECTURE.md` 今天没有这段分层（实测 grep `限界上下文` 零命中）」——本节就是补上它。
> ADR-027 §7 同时禁止把这 7 个上下文都拆成服务（单人自托管，拆 7 个服务是运维灾难；DDD 的价值在**语言边界**，不在进程数）。所以本表的产物是**包**，不是仓。
> **「目标包」一列是本节给的建议名**——ADR-027 只定「按包强制隔离」这条义务，一个包名都没定。X1b 建仓时要改名就改本表（连同下面断言 1 的方向图），**别造第二套名字**：断言是按目录名写的，两套名字等于没有边界。

| 限界上下文 | 领域语言（该上下文自己的词，不许借别的上下文的词） | 变化原因 | 目标包 | 今天的代码在哪（core） |
|---|---|---|---|---|
| 采集 Ingestion | `DataSource` / `Quota` / `RawPayload` / `content_hash` | 外部 API 变（tushare 改字段、东财反爬） | `internal/ingest` | **本仓**：`internal/ingest`（`declarations_*.go` + `adapter.go` 9 方法面）+ `internal/transport/tushare`；core `pkg/data` 已于 `a96d836`（2026-11-08）删除 |
| 规范化 Normalization | `TradingCalendar` / `CorporateAction` / `PITAlignment` / `Board` | 数据口径变 | `internal/normalize` | **本仓**：`internal/normalize`（`Aggregate` / `VisibleAsOf`）+ `internal/ingest`（`ETLPipeline` / `ValidatePoints` / `GatePoints` / `Deduplicate`）；core `pkg/data/source/etl.go` 已删 |
| 供给 Serving | `Provider` / `OHLCV` / `Quote` / **`SeriesSpec`** | 消费者需求变 | `internal/serving` | **本仓**：`internal/serving`（`Resolve` / `Snapshot` / `legacy.go` 兼容端口）+ `internal/httpapi` 的读 handlers；core `pkg/marketdata` 只剩 HTTP 客户端 |
| 派生 Derived | `Factor` / `ICEntry` / `ZScore` | 因子定义变 | **不迁入本仓**（ADR-037：owner=core，消费点按需求值） | core：`pkg/storage/cache.go`（`factor_cache` / `factor_returns` / `ic_analysis` 的写）+ `pkg/backtest/cache/factor_cache.go` + `pkg/mining`（**仍在 core**，因子读面不在本仓；`pkg/data/factor*.go` 已随 `a96d836` 删除） |
| 证据 Evidence | `Citation` / `Provenance` | 可追溯性要求变 | `internal/evidence` | **本仓（2026-11-12 起，ADR-0010）**：`internal/evidence`（脱敏 / 规范化哈希 / 保留期）+ `internal/store/evidence.go`（`ingest.raw` 读写+清理）+ `internal/httpapi/evidence.go`（`POST /api/ingest/raw`、`GET /api/evidence[/{content_hash}]`）；core 只保留 `GET /api/evidence/{content_hash}` 读反代（按 `/api/sync/*` 同族登记为本仓入口，不标 `x-served-by`），不持有第二份存储 |
| 同步编排 Sync | `SyncJob` / `Schedule` / `WorkerPool` / `Backpressure` | 调度策略变 | `internal/sync` | **2026-11-10 起已在本仓**：`internal/sync`（package `syncjob`，队列 + cron + Worker + Scheduler）、`internal/transport/tushare`（传输）、`cmd/dataservice` 的装配；core 侧 `pkg/sync` 与 `cmd/data` 已随 `a96d836` 删除 |
| 文本降维 Reduction | `NewsItem` / `Hotness` / `DecayHalfLife` / `WeightSeries` | NLP 模型 / 规则变 | **不给包**（下表说明） | 无实现 |

**Reduction 这一行故意留空**。ADR-027 §2.6.2 判它是**第七个上下文且唯一物理独立**（第五个服务），但 `tellurion:docs/design/qt-decoupling/repo-inventory.md` §1 的十二仓表里没有它的仓位——该篇 §1.2 R1 把这条列为**待裁 U6**，倾向「先作 `tellurion-data` 内的独立包，做文本降维时再升格」，代价是「§2.6.2 的『资源特征不同 / 失败可隔离』两条拿不到，且 ADR-027 的五服务拓扑在文档上缺一角」。**在本表里替 U6 预先分配一个包**（如 `internal/reduction`）等于用一份服务仓文档裁决另一个仓的拓扑，所以这里只登记义务：真要做文本降维时，它要么进本仓一个**与上面六个平级、且不被 `serving` 直接调用**的包（失败隔离靠调用方向保证），要么立第 13 仓并同步改 §0/§1 的「12 仓」口径。

**门禁断言**（ADR-027 §6 那三类在本仓的可执行版本；判据现在就能写，**AST 断言本体属 X1b 剩余交付物**——包已建起，守卫测试尚未落）

1. **包方向**：`ingest → normalize → serving` 单向；`derived` 不得 import `serving`（派生值是被供给的资产，不是供给的一部分）；`evidence` 只依赖 contracts。断言形态照 core `cmd/analysis/deps_test.go` 的 `mustNotHave`（AST / import 扫描，命中即红）。
2. **数据归属**：`market` / `ingest` 的建表与写 SQL **在且只在**本仓（ADR-027 §6 第 2 条要求从「表在 DDL 里」升级到「在且只在 owner 的 DDL 里」）。X1d 之前的现状是 core 内联 DDL 承载，所以这条在迁移完成前**只能是告警级**，登记见 `db-schema.md` §1 的 SPEC-R-02 认领行。
3. **可达性**：`cmd/*/` 下的 `New*Handler` / `register*Routes` 必须有调用点（§6 第 3 条）。对本仓它防的是反方向的漂移——`api-schema.md` §1 那三条 `/datasets/*` 今天是**目标契约**，别让它先在文档里"存在"、再被当成已实现的读面。（2026-10-23：aggregator 的 **port 面**已落，HTTP 面仍是 **0 个 handler**，本仓也还没有 `cmd/`。）

**两条没闭合的口子（本次对表实测，不在本节里假装解决）**

- ① `core:pkg/data/source` 一个包同时装着**采集与规范化**两个上下文（`adapter.go`/`registry.go`/`declarations.go` 是采集契约，`etl.go`/`unified.go` 是规范化）。整包平移过来的结果是"目录看起来分好了、边界一个没立"——X1b 建包时**必须把这两块拆开**，断言 1 才有东西可断。同类混装还在 `core:pkg/data` 顶层（`factor*.go` 派生 与 `tushare*.go` 采集同目录）。
- ② §2.6.2 给规范化的领域语言点名列了 `Board`，但 `Board` 的代码今天物理在**供给**包（`pkg/marketdata/board.go`）；`PITAlignment` 今天**没有独立承载物**——可见性全靠列 + 读路径比较（`pkg/storage/fundamentals.go:121` 的 `available_date <= $2`），它的形状要等 `db-schema.md` §6 那条加列之后才存在。建仓时逐个定"跟语言走还是跟调用方走"，**别只搬有现成归属的那几个**。证据上下文（表中第 5 行）已于 **2026-11-12 随 ADR-0010 收口**：归档写读面与 `ingest.raw` owner 都在本仓，core 只留 `GET /api/evidence/{content_hash}` 读反代；ADR-027 的构件图（evidence API 算在 data-service）由此成立，无需回改。**别留一张看起来已有归属、实际代码在别的进程里的表**。

## 3. 组件职责

| 层 | 职责 | 不该做什么 |
|---|---|---|
| connector | 对接源、字段映射、声明语义 | 不做归一化（那是共享函数的事） |
| Normalizer | 统一成协议类型 | 不做业务聚合 |
| 参考数据同步 | 参照表 upsert（`stocks` / `trading_calendar`），旁路点位闸门（ADR-0008） | 不写 A 集点位、不做 PIT 判定 |
| 存储 | 真相源 | 不对外 |
| aggregator | PIT / 聚合 / 票池 / hash / 缓存 | 不做业务计算 |

## 4. 三份契约（功能的边界就在这三份文件里）

| 维度 | 文件 | 回答什么问题 |
|---|---|---|
| 数据形状 | [`db-schema.md`](db-schema.md) | 本仓拥有哪些表？谁能写？ |
| 接口形状 | [`api-schema.md`](api-schema.md) | 对外暴露什么？错误码与幂等怎么做？ |
| 流向与顺序 | [`dataflow.md`](dataflow.md) | 数据怎么流？哪里 fail-closed？ |

## 5. 依赖方向

```
contracts（协议类型） + kit（logging/http/db）
        ↑
  tellurion-data  ◄──(HTTP)──  core / analytics / strategy / agent
```

> **箭头图例**（与 `tellurion-docs:docs/REPO-MAP.md` 同向）：`A ──► B` = A 调用 B，箭头指向**被调方**。四方经 HTTP 调用本仓「对外面」（run 前取数 / 异步），本仓只供数、不反向调用（2026-10-21 审查 M24 订正：原图箭头反向，读作「data 调四方」）。

## 5.1 鉴权（2026-10-25）

- 机制：kit `v0.1.4` 的 `auth/httpadapter`（Bearer → `Parse` → `kind=access` → 角色 any-of + 启动闸）；本仓只写策略表（`internal/httpapi/policy.go`）。`v0.1.4`（kit `KT-11`）起，`role=service` 的 token 不再要求 subject 可解析为 `int64`——这是 `sub=service:core` 能真走到本仓策略表的前置。
- 策略：`/health` 公开；`/datasets/resolve`、`/datasets/{hash}` = `service`（裁-17，服务间长期 token）或 `viewer` 及以上。旧 `cmd/data` 的 REV-17 表（28 条）已逐条移植为 `legacyREV17`，等端点迁入时直接挂闸；**G3（2026-11-10）已把 `POST /api/v1/ohlcv/bulk` 落地并移入 `servedPolicy`；X1c（2026-11-10）把 8 条作业/排程策略落地并移入 `servedPolicy` ⇒ `legacyREV17` 余 19 条、`servedPolicy` 23 条路由**（作业读 = `service`+viewer/trader/admin，作业变更 = trader|admin，排程管理 = admin）。
- 失败面：未装配 Authorizer → 503；无凭证 / 坏 token → 401；越权 → 403。无 secret + 回环 = 开放；无 secret + 非回环 = **拒绝启动**。
- 裁决落 [`adr/0005-鉴权接入-kit适配与REV-17移植.md`](adr/0005-鉴权接入-kit适配与REV-17移植.md)。

## 6. 关键决策

- [`adr/0001-统一协议与-visible-at-第二道防线.md`](adr/0001-统一协议与-visible-at-第二道防线.md)
- [`adr/0002-market-ingest-ddl归属与-visible-at-三集合.md`](adr/0002-market-ingest-ddl归属与-visible-at-三集合.md)
- [`adr/0003-connector搬入本仓传输以窄-port-留在-core.md`](adr/0003-connector搬入本仓传输以窄-port-留在-core.md)
- [`adr/0004-写面归属-store-与幂等内容比对.md`](adr/0004-写面归属-store-与幂等内容比对.md)
- [`adr/0005-鉴权接入-kit适配与REV-17移植.md`](adr/0005-鉴权接入-kit适配与REV-17移植.md)
- [`adr/0006-跨仓写入经HTTP投点与装配层归属.md`](adr/0006-跨仓写入经HTTP投点与装配层归属.md)
- [`adr/0007-本仓自持传输与调度.md`](adr/0007-本仓自持传输与调度.md)
- [`adr/0008-参考数据同步与传输多门.md`](adr/0008-参考数据同步与传输多门.md)

## 7. 非目标（明确不做）

- 不做逐 bar / 逐笔远程取数
- 不做策略或因子计算（只提供已落库的因子值）
- 不做回测结果存储
- 不做跨 schema join

## 8. 分期与状态

| 阶段 | 内容 | 验收 |
|---|---|---|
| X1a | 协议冻结进 contracts（类型 + TimeSemantics + 错误码） | core/agent 只依赖 contracts |
| X1b | aggregator（PIT + 聚合 + 票池 + hash） | 同 hash 两次一致 |
| X1c | connector 契约收口 | 漏声明被拒收 |
| X1d | schema 归属迁移 | 无第二写者 |
| X0 | `dataset_snapshot_hash` 记入结果 | 与 `strategy_ref` 同款留痕 |

**施工状态（2026-10-24 实测，`go build/vet/test ./...` 全绿）**

- **X1a ✅（2026-10-25）**：contracts `v0.6.0` 新增 `dataprotocol` 包（`UnifiedDataPoint` 七字段 + `ToJSON` / `DeduplicateKey` + 15 个 `DataType*` 常量），本仓 `require v0.6.0` 后**删掉 `internal/ingest/point.go` 的逐字段镜像**，改 `type UnifiedDataPoint = dataprotocol.UnifiedDataPoint`；构造器（注入 `IngestTime`，契约不读时钟）与包内 `DeduplicateKey` 委托留本侧。守卫见 `internal/ingest/protocol_test.go`。**2026-11-11 起 pin 升至 `v0.9.0`**（`CT-06-ALIAS-EXIT`：本仓对 6 个 `domain` 根别名零引用——根别名 `Fundamental` 已改 `market.Fundamental`，其余 5 个 0 命中；根 `domain` 仅剩非别名 `MacroSeries` / `MarketActivity`；`build/vet/test` 全绿）。
- **X1b 🔶 取数面 + 写面代码已落，写面装配已接到 HTTP 投点（2026-10-25 H4 ②）**：`internal/serving`（`Resolve` / `Snapshot`、六分量 hash、强幂等缓存、回显同源校验）、`internal/normalize`（`Aggregate` 1m→15m/1d、`VisibleAsOf` PIT）；**2026-10-24 G4**：`internal/ingest` 落 `ETLPipeline` / `Registry.Fetch` / `ValidatePoints` / `GatePoints` / `Deduplicate`，`internal/store` 落写面 `BulkInsert`（A 集表、`ON CONFLICT DO NOTHING` + 内容比对）；**2026-10-25 H4 ②**：新增 `internal/assembly`（投影唯一处 + `DeclarationsRegistry()` 零传输声明面 + `Pipeline()`），`POST /ingest/points` 经它落库，`cmd/dataservice` 在 `DATABASE_URL` 有值时装出写面（为空 503）。**已落（2026-11-10 订正）**：同步作业的调度与取数已归本仓（X1c），`Pipeline()` 已有本仓生产调用方；`/factors/{name}@{hash}` 仅路由恒 404（**已撤**，裁-14，2026-10-25）——因子读面只在 core，本仓路由表不含 `/factors/*`。**2026-11-10 订正**：「同步作业的调度与取数仍在 core」已不成立（core `cmd/data` / `pkg/data` 已删，见 X1c），调度与 Tushare 传输已归本仓。
- **X1c ✅（2026-11-10，裁决 B）传输与调度自持**：`internal/transport/tushare` 实现 `ingest.TushareTransport` 10 个方法；`internal/sync`（package `syncjob`）持有 `ingest.sync_jobs` 队列、`ingest.sync_schedules` 排程（自研 5 段 cron）、进程内 Worker（2s 轮询 + `FOR UPDATE SKIP LOCKED`）与 Scheduler（30s 扫 due）；11 条 HTTP 端点（`/api/sync/jobs*`、`/api/sync/schedules*`）按 REV-17 档位挂闸；`cmd/dataservice` 在 `TUSHARE_TOKEN` 非空时装真传输，worker / scheduler 在 schema 就绪后启动。执行链与 `POST /ingest/points` 共用 `assembly.Pipeline`（单源）。**2026-11-10 终扫（ADR-0008）**：东财客户端恒装配、Alpha Vantage 由 `ALPHA_VANTAGE_KEY` 控制（Juchao / Xueqiu / Yahoo 本就恒启用）；`stocks` / `trading_calendar` 补参考数据写者；`ingest.raw` 归档与 Mootdx 传输 **⏸ 挂起**（带重开条件）。worker 失败仍不自动重试。决策见 ADR-0007 / ADR-0008。
- **X1d ✅ DDL 归属 + 写面已落**：`internal/schema` 持有 `market` / `ingest` 的建表、`visible_at` 加列（A 集 12 张 × 目标/今天两侧）与哨兵防重放的回填腿；`store.BulkInsert` 走 `schema.GateClassFor` 三集合门（B/C 集拒写，缺 `visible_at` 整批拒收），见 `db-schema.md` §6。**2026-11-10 本机实例已执行**：22 张目标表 `SET SCHEMA`、`public` 归 0、A 集 NULL 归 0、回填哨兵 12、幂等复跑 22 skip（证据 `docs/archive/x1d-local-replay-2026-11-10.md`）；远端/生产库未动。第二写者已归零（core 侧 `market.*` / `ingest.*` 写点仅剩裁决 ⑰ 的 `market.index_daily`）。
- **X0 ✅（2026-11-11 复核）**：`dataset_snapshot_hash` 已由 core 的 run provenance / artifact publisher 落库（`cmd/analysis/handlers_backtest_provenance.go`、`internal/analysisartifact/publisher.go`），不再「无消费方」。
- **G3 只读兼容面 🔶（2026-11-10）**：core `pkg/marketdata/http_provider.go` 的 7 条旧读路由由本仓补齐（`internal/serving/legacy.go` 端口 → `internal/store/reference.go` 实现 → `internal/httpapi/legacy_read.go` handler），core 零改；只读、不参与快照哈希。**推翻 K1 字面口径（本仓不补齐旧读面），待主任追认**。切读/停写与 core 旧实现退役仍未完成（历史记录见 `docs/archive/tasks-archive-2026-11.md` 的 SVC-DATA-G3）。
