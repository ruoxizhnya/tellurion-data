---
status: active
last-verified: 2026-10-25
verified-by: 裁-14 落地（D4-1）——撤掉 `/factors/{name}@{hash}` 路由与恒 404 占位；因子读面只在 core。路由表由 `internal/httpapi` 的 `TestRouteTableIsThePublishedSurface` / `TestFactorRouteIsRemoved` 钉住
---

# API Schema — tellurion-data

> **实现状态（2026-10-25 更新，裁-14）**：`GET /datasets/resolve` 与 `GET /datasets/{hash}` 的 **HTTP handler 已落地**（`internal/httpapi/server.go` + `cmd/dataservice`，标准库 `net/http`；哨兵 → 状态码映射见 `sentinelCodes`，未登记错误一律 500）。**四个取数 port 已有 PG 实现**（`internal/store`：`BarSource` / `TradingCalendar` / `DataVersioner` / `UniverseResolver`），进程在 `DATABASE_URL` 有值时启动即执行本仓 schema 的 DDL 并接线；**为空时仍 fail-closed 503**（`/health` 常绿）。装配开关：`DATABASE_URL`（DSN）、`DATA_MARKET_SCHEMA`（默认 `market`；物理搬迁完成前设空串走 `public`）。`/factors/{name}@{hash}` **已于裁-14 撤掉**（路由不存在，不是恒 404 占位）：因子读面只在 core，本仓 openapi / 本文同步删除该路径。同 hash 两次拉取逐位一致已由 wire 级测试钉住（`server_test.go` `TestSnapshotSameHashReturnsByteIdenticalBody`）。

> **契约地位**：HTTP，对外唯一数据面。**aggregator 是唯一入口**，connector 与存储不直接对外。
> 本文件是**本仓对外接口的唯一真相源**。代码与它不一致时，先判断谁错：契约错 → 改本文 + 立 ADR；实现错 → 改代码。
> 跨仓消费方（core / agent / ui / 其他服务）按本文件编码，**不读本仓实现**。

## 1. 接口面

> 逐个端点的**参数语义、错误映射、幂等与版本策略**在 `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1 C 组（M8 接口对表，2026-10-21）。正式 schema 落 core `docs/openapi.yaml`（真相源在 core，QT-50 ③-③），**本文件不复制 openapi**。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/datasets/resolve?universe=&index_code=&from=&to=&freq=&adjustment=&as_of=` | 解析票池 + 窗口 + 频率 + 复权口径 + 可见时点 ⇒ `{dataset_hash, metadata}`（`metadata` 回显六分量） |
| GET | `/datasets/{hash}` | 按 hash 取数据（同一 hash 两次结果必须**字节一致**） |

内部链路（不对外）：`connector` → `Normalizer` → `ValidatePoints` → **`GatePoints`（visible_at 整批门）** → `Deduplicate` → `BulkInsert`（A 集表；同内容 no-op / 异内容整批回滚）。

> **实现状态（2026-10-25，裁-14 落账）**：
> - `/datasets/resolve`、`/datasets/{hash}`：已实现（`internal/httpapi` + `internal/serving`）。
> - `/factors/{name}@{hash}`：**已撤掉**（裁-14，2026-10-25）。路由表里不存在它 ⇒ ServeMux 默认 404；不再有 `factor_not_found` 文案。原因：生产方与读端点都在 core（`pkg/data/factor_formula.go` / `factor_equitydeep.go` → `SaveFactorCacheBatch`；读面 `cmd/data/handlers_factor.go`），SPEC 把 C 类派生计算列为本仓非目标；常驻 404 比没有路由更坏——调用方会把 404 读成「该数据集没有该因子」。**因子读面明确由 core 提供**。上移触发条件（任一成立再启动整体迁移）：① C 类生产需要横向扩容；② 出现第二个不接受 core API 的消费方。

> **`as_of` 与 `adjustment` 为什么必须在参数里**：前者是第二道防前视的输入（aggregator 按 `visible_at <= as_of` 过滤，定义权在本仓，见对表 L2）；后者由**请求方（core）给**、本仓只按参数取数并回算 hash——本仓**不读** `backtest_adjustment_anchors`（owner = core 回测域），否则 hash 口径两处各自算一遍（对表 L3）。

## 2. 错误码与 fail-closed 语义

> **本表是目标契约，不是现状**（2026-10-21 实测订正）。逐个哨兵的**真实定义处、执法函数与 HTTP 映射**在
> `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1 E 组——本文件不再重述签名，只留分诊口径。
> 三条纪律：① **执法点必须点名到函数**（只写触发条件，实现就会把门放到错误的层）；② 哨兵**只增不删不改语义**；
> ③ 「缺失 ≠ 一个看起来合理的值」——`ErrVisibleAtMissing` 明写不许用 mtime 兜底，与 §5 的「不返回空集」同源。

| 错误 | 今天是否已存在 | 触发 | 调用方行为 |
|---|---|---|---|
| `ErrDeclarationMissing` | ✅（`tellurion:pkg/data/source/contract.go:66`） | connector 支持某类型却没声明时间语义 | 注册期拒收（fail-closed，不许猜）。⚠️ **本表旧名 `ErrNoTimeSemantics` 作废**，不留两个名字 |
| `ErrDeclarationInvalid` / `ErrDeclarationMismatch` | ✅（`:67` / `:68`） | 声明非法（枚举 / 缺 `VisibleExpr` / 重复）／声明与数据形态矛盾 | 注册期拒收 |
| `ErrUnsupported` | ✅（`tellurion:pkg/data/source/adapter.go:73`） | 请求了未登记的数据类型 | 400，不返回空集。**不另造 `ErrUnknownDataType`**（同义哨兵会让 `errors.Is` 分诊两义，见对表 E 组） |
| `ErrVisibleAtMissing` | ✅（`tellurion-data:internal/normalize/pit.go`） | 数据点缺 `visible_at` | 拒收该批次；**不许用 mtime 兜底**。⚠️ 代价：历史行无该列、不可回填 ⇒ 与 X1d 加列同批设计（对表 T5） |
| `ErrSnapshotMismatch` | ✅（`tellurion-data:internal/serving/errors.go`） | hash 与返回内容不一致 | 500 + 告警（数据已被篡改或缓存串了）；与「没这个 hash」（404）**必须是两个码** |
| `ErrMissingParameter` / `ErrInvalidParameter` / `ErrUniverseAmbiguous` / `ErrEmptyUniverse` / `ErrEmptyCalendar` / `ErrEmptyDataVersion` / `ErrUnexpectedSymbol` / `ErrSnapshotNotFound` | ✅（`tellurion-data:internal/serving/errors.go`） | resolve 参数缺失 / 非法 / 票池二义 / 票池为空 / 日历为空 / 数据版本为空 / 越界 symbol；按 hash 取不到 | 400 / 409 / 404 的分诊口径见该文件头注释；哨兵只增不删（**唯一例外**：`ErrFactorNotFound` 随裁-14 撤路由一并删除，不再有第二个「因子读面在本仓」的真相） |

## 3. 幂等性与重试

- `dataset_hash` 是内容寻址。**唯一定义 = `tellurion-contracts:engine/provenance.go` 的 `DatasetSnapshotInput`（六分量：`universe_hash` / `window` / `frequency` / `data_version` / `calendar_hash` / `adjustment_basis`）**，本文件不重述分量（QT-03 于 2026-10-21 冻结；旧四项写法缺 `calendar_hash` 与 `adjustment_basis`，会导致「供应商补一个节假日 → 同 hash 不同结果」，已订正为引用。core 的 `pkg/backtest/contracts` 已迁入 contracts 仓，旧路径作废）。
- 本仓**不许自行实现 hash**：调 `contracts` 的 `DatasetSnapshotInput{...}.Hash()`。
- 同一 hash 的读请求可任意重试（无副作用）；返回内容重算 hash 不符 ⇒ `ErrSnapshotMismatch`。
- 写入幂等靠 `sync_jobs` / `sync_schedules` 的任务状态 + 数据点主键（目标迁 `ingest`，见 `db-schema.md` §1.5）。

## 4. 版本与兼容

- 协议类型（`UnifiedDataPoint` 等）住在 contracts，版本随契约走。**包名与迁入时机**见对表 A 组注 ①/②：定名 `contracts/dataprotocol`，那一笔挂在 X1b 第一 commit。判据的**条款号是订正过的**（2026-10-22，QT-40 对表 T2 核对上游时逮到）：不许提前迁的那条是 contracts `docs/adr/0001` **①**「本仓只放被 ≥2 个仓使用的类型」（`tellurion-contracts:AGENTS.md` §3 写作「只有单个仓使用的类型属泄漏」），**不是 ③**——③ 管的是 `Config`/`Options`/`Impl` 命名。⚠️ 同一份 ADR 的「代价」节又接受 S1 整包迁时带进 core-only 类型、事后按清单回收，于是「新建包要不要走同一条容忍」成一个真岔路：**两个选项都自洽，登记在 `tellurion-contracts:docs/API.md` §8 待裁 #4，X1a 前挑定并写回本节**，不要 core / data 两侧各按一条施工。
- 端点只增不删；废弃走 `deprecated` 头 + 一个 MINOR 周期。
- 字段语义变更（尤其 `visible_at`）算 breaking，必须走 ADR。

## 5. 不提供什么（边界）

- 不提供逐 bar / 逐笔取数接口
- **不把本仓做成「HTTP 包装的通用 DB 网关」**（`tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2 对 ADR-027 §7 的对账结论）：core `cmd/data` 今天那批 `GET /ohlcv/:symbol`、`GET /fundamental/:symbol` 是**逐 symbol 逐日期的 DB 代理形状**，X1b 的归位是**并入 resolve + dataset**，不是原样搬进本仓——逐个端点的去处见对表 D 组
- 不提供回测结果存储（那是 run）
- 不提供策略 / 因子**计算**，也**不提供因子读面**（裁-14 撤掉 `/factors/{name}@{hash}`）——因子定义与值一律由 core 提供；**因子定义与 SeriesSpec 归 strategy**（ADR-027 §2.6.1 / ADR-028 §6），求值在消费点（§7「不做因子服务」）；三条界线的定案见对表 G 组 L1
- 不提供跨 schema join（宁可多一次调用）
