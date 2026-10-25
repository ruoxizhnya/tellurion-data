---
status: active
last-verified: 2026-10-25
verified-by: QT-40 H4 ②（写端点 `POST /ingest/points` + `internal/assembly` 装配层）；W4 鉴权接线；裁-14 撤 `/factors/{name}@{hash}`
---

# API Schema — tellurion-data

> **实现状态（2026-10-25 更新，QT-40 H4 ②）**：读面 `GET /datasets/resolve` 与 `GET /datasets/{hash}` 已落地；**写面第一条 `POST /ingest/points` 同日落地**（`internal/httpapi/server.go` handler + `internal/assembly` 装配层 + `cmd/dataservice` 接线）。装配开关：`DATABASE_URL`（DSN）、`DATA_MARKET_SCHEMA`（默认 `market`；物理搬迁完成前设空串走 `public`）；DSN 为空 ⇒ 读面与写面都 fail-closed 503（`/health` 常绿）。`/factors/{name}@{hash}` **已于裁-14 撤掉**（路由不存在，不是恒 404 占位）：因子读面只在 core。同 hash 两次拉取逐位一致已由 wire 级测试钉住（`server_test.go` `TestSnapshotSameHashReturnsByteIdenticalBody`）。
>
> ⚠️ **写端点今天没有被真实 PG 跑过**：它的测试腿是「真 `assembly.Writer` + 真 `store.Writer` + 无库 Exec 替身」（`points_test.go:TestIngestPointsThroughRealWriter` 钉住 A 集门与 `ON CONFLICT DO NOTHING` 语句形状）与「替身 sink 记录收到的批次」。SQL 真的作用在库上那一腿仍只有 `internal/store` 的 `DATABASE_URL`-gated PG 套件覆盖。不许把它读成「端到端已验收」。

> **契约地位**：HTTP，对外唯一数据面。**aggregator 是唯一读入口**，connector 与存储不直接对外；写面只有投点这一条，且**只接受有界批次**。
> 本文件是**本仓对外接口的唯一真相源**。代码与它不一致时，先判断谁错：契约错 → 改本文 + 立 ADR；实现错 → 改代码。
> 跨仓消费方（core / agent / ui / 其他服务）按本文件编码，**不读本仓实现**。
> **鉴权（2026-10-25 已接线，ADR-0005）**：机制在 kit `v0.1.4` 的 `auth/httpadapter`（2026-10-25 引入 `v0.1.1`，同日随 kit `KT-11` 升 `v0.1.4`——`v0.1.1` 会把 `sub=service:<repo>` 的非数字 subject 当场 401，服务间调用走不到策略表），策略表在本仓
> `internal/httpapi/policy.go`。`/health` 公开；数据面读端点接受 `service`（服务间调用，裁-17）或
> `viewer` 及以上（any-of 写全，不是层级）；**写端点只接受 `service` / `admin`**（见 §1 策略表注）。**全部端点**
> fail-closed：未装配 Authorizer → 503，无凭证 / 坏 token → 401，越权 → 403。无 secret + 回环 = 开放；
> 无 secret + 非回环 = **拒绝启动**（本服务默认 `:8081` 全网卡，必须显式配 `JWT_SECRET` 或声明容器只在回环发布）。

## 1. 接口面

> 逐个端点的**参数语义、错误映射、幂等与版本策略**在 `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1 C 组（M8 接口对表，2026-10-21）。正式 schema 落 core `docs/openapi.yaml`（真相源在 core，QT-50 ③-③），**本文件不复制 openapi**。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/datasets/resolve?universe=&index_code=&from=&to=&freq=&adjustment=&as_of=` | 解析票池 + 窗口 + 频率 + 复权口径 + 可见时点 ⇒ `{dataset_hash, metadata}`（`metadata` 回显六分量） |
| GET | `/datasets/{hash}` | 按 hash 取数据（同一 hash 两次结果必须**字节一致**） |
| POST | `/ingest/points` | **写面**：投送一批**已归一**的点位（`{data_type, points[]}`）⇒ 闸门 + 去重 + A 集门 + 内容比对落库；返回 `{data_type, received, persisted, skipped}` |

内部链路（不对外）：`connector` → `Normalizer` → `ValidatePoints` → **`GatePoints`（visible_at 整批门）** → `Deduplicate` → `BulkInsert`（A 集表；同内容 no-op / 异内容整批回滚）。`POST /ingest/points` 走的是**同一条链的后半段**（从 `ValidatePoints` 起），投影与次序在 `internal/assembly`——两条入口共享一条链，不是两份实现（ADR-0006）。

> **`POST /ingest/points` 的三条硬规矩**（都在 `internal/assembly`，handler 只做编码与分诊）：
> 1. **`visible_at` 与 `ingest_time` 不接受投送**。请求体里出现这两个字段 ⇒ 解码失败 400 `invalid_parameter`（`DisallowUnknownFields`），不是「忽略」。被忽略的字段会让调用方以为自己指定的可见时刻生效了——那比 400 坏得多。可见时刻只由该 data_type 的**声明锚点**推导，入库时刻只由本进程时钟填。
> 2. **有界**：`assembly.MaxPointsPerRequest = 10000` 点 / 请求体 8 MiB。这是 `docs/dataflow.md` §2「同步 HTTP 端点不得承载无界批量」那条红线的落地。超限**整批拒收** 413，不写前 N 条，也不静默截断。
> 3. **`source` 必填**：来源是行内容的一部分（写面对「同主键换源」是响亮拒收），空白来源等于放弃那条判定。
>
> 数字载荷以 `json.Number` 解码后交给写面（`UseNumber`），不在 HTTP 层折成 `float64`。

> **实现状态（2026-10-25，裁-14 落账）**：
> - `/datasets/resolve`、`/datasets/{hash}`：已实现（`internal/httpapi` + `internal/serving`）。
> - `/factors/{name}@{hash}`：**已撤掉**（裁-14，2026-10-25）。路由表里不存在它 ⇒ ServeMux 默认 404；不再有 `factor_not_found` 文案。原因：生产方与读端点都在 core（`pkg/data/factor_formula.go` / `factor_equitydeep.go` → `SaveFactorCacheBatch`；读面 `cmd/data/handlers_factor.go`），SPEC 把 C 类派生计算列为本仓非目标；常驻 404 比没有路由更坏——调用方会把 404 读成「该数据集没有该因子」。**因子读面明确由 core 提供**。上移触发条件（任一成立再启动整体迁移）：① C 类生产需要横向扩容；② 出现第二个不接受 core API 的消费方。
> - `POST /ingest/points`：已实现（H4 ②，2026-10-25）。**调用方今天只有 core 的两条作业**（`capital_flow` / `top_list`，见 ADR-0006）；同步作业层本身**尚未迁入本仓**（仍在 core `cmd/data`，传输也在 core），所以这条端点是「别人取数、本仓落库」的接缝，不是本仓自己跑同步作业的能力。

> **`as_of` 与 `adjustment` 为什么必须在参数里**：前者是第二道防前视的输入（aggregator 按 `visible_at <= as_of` 过滤，定义权在本仓，见对表 L2）；后者由**请求方（core）给**、本仓只按参数取数并回算 hash——本仓**不读** `backtest_adjustment_anchors`（owner = core 回测域），否则 hash 口径两处各自算一遍（对表 L3）。

> **鉴权策略表（2026-10-25，裁-15 + H4 ②）**：
> - **本仓实际路由**（`servedPolicy`）：`GET /health` = Public；`GET /datasets/resolve`、`GET /datasets/{hash}` = `service`（裁-17）+ viewer / trader / admin；**`POST /ingest/points` = 只 `service` + `admin`**。写面不放 `viewer`/`trader`，也不 Public：读档位与写档位混在同一档里，将来给写面收紧就说不清是谁在什么时候为什么放开的。`admin` 之所以必须在：core 的 `pkg/auth` 角色集今天仍是 {viewer, trader, admin}，`service` 要能签发是 QT-71 的活——不开 `admin` 这条腿今天就不可用（代价与订正条件见 ADR-0006「代价」节）。
> - **REV-17 逐条移植**（`legacyREV17`）：旧 `core:cmd/data` 的 28 条策略（排程管理 admin / 其余变更 trader / `POST /screen` 与 `POST /api/v1/ohlcv/bulk` any-authenticated）已逐条搬入本仓，**但对应端点尚未迁入**（仍在 core）。保留这张表是为了端点迁移时「同一路由、同一角色」不再重新拍板；守卫 `TestLegacyREV17PolicyIsVerbatim` + `TestLegacyREV17DecisionsMatchOldService` 钉住档位与结论。
> - 守卫：`TestRouteTableEqualsServedPolicy`（路由表 ≡ 实际策略表，**逐条点名 4 条**）；`TestServedPolicyWriteRouteRolesAreNarrow`（写面档位只有 service+admin，不 Public、不 any-authenticated）；`TestIngestPointsAuthTiers`（service/admin 过闸到业务 503，viewer/trader/未知角色 403，无凭证 401）；`TestAuthWireThreeStates`（health 公开 / 无凭证 401 / viewer 可读 / 闸未装配 503）；`TestServedPolicyServiceRole`（service 过闸到 handler、未列角色仍 403、health 仍 Public）。
> - **跨仓前置**：core `proxyRequest` 已转发入站 `Authorization`（2026-10-25）；`POST /api/v1/ohlcv/bulk` 经 core 代理不再 401。**写端点的调用方凭证**由 core 侧 env 注入（`INGEST_WRITE_*`），不在本仓签发。

## 2. 错误码与 fail-closed 语义

> **本表是目标契约，不是现状**（2026-10-21 实测订正）。逐个哨兵的**真实定义处、执法函数与 HTTP 映射**在
> `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1 E 组——本文件不再重述签名，只留分诊口径。
> 三条纪律：① **执法点必须点名到函数**（只写触发条件，实现就会把门放到错误的层）；② 哨兵**只增不删不改语义**；
> ③ 「缺失 ≠ 一个看起来合理的值」——`ErrVisibleAtMissing` 明写不许用 mtime 兜底，与 §5 的「不返回空集」同源。

| 错误 | 今天是否已存在 | 触发 | 调用方行为 |
|---|---|---|---|
| 无凭证 / token 过期 | ✅ 2026-10-25 | 数据面端点未带 / 带了坏 `Bearer` | 401 `unauthenticated`；不重试，补齐凭证 |
| 角色不足 | ✅ 2026-10-25 | 角色不在策略表（数据面接受 `service` / `viewer` / `trader` / `admin`） | 403 `forbidden`；不重试 |
| 鉴权闸未装配 | ✅ 2026-10-25 | 进程未装 `Authorizer` | 503 `auth_unavailable`；**拒绝而非放行** |
| `ErrDeclarationMissing` | ✅（`tellurion:pkg/data/source/contract.go:66`） | connector 支持某类型却没声明时间语义 | 注册期拒收（fail-closed，不许猜）。⚠️ **本表旧名 `ErrNoTimeSemantics` 作废**，不留两个名字 |
| `ErrDeclarationInvalid` / `ErrDeclarationMismatch` | ✅（`:67` / `:68`） | 声明非法（枚举 / 缺 `VisibleExpr` / 重复）／声明与数据形态矛盾 | 注册期拒收 |
| `ErrUnsupported` | ✅（`tellurion:pkg/data/source/adapter.go:73`） | 请求了未登记的数据类型 | 400，不返回空集。**不另造 `ErrUnknownDataType`**（同义哨兵会让 `errors.Is` 分诊两义，见对表 E 组） |
| `ErrVisibleAtMissing` | ✅（`tellurion-data:internal/normalize/pit.go`） | 数据点缺 `visible_at` | 拒收该批次；**不许用 mtime 兜底**。⚠️ 代价：历史行无该列、不可回填 ⇒ 与 X1d 加列同批设计（对表 T5） |
| `ErrSnapshotMismatch` | ✅（`tellurion-data:internal/serving/errors.go`） | hash 与返回内容不一致 | 500 + 告警（数据已被篡改或缓存串了）；与「没这个 hash」（404）**必须是两个码** |
| `ErrMissingParameter` / `ErrInvalidParameter` / `ErrUniverseAmbiguous` / `ErrEmptyUniverse` / `ErrEmptyCalendar` / `ErrEmptyDataVersion` / `ErrUnexpectedSymbol` / `ErrSnapshotNotFound` | ✅（`tellurion-data:internal/serving/errors.go`） | resolve 参数缺失 / 非法 / 票池二义 / 票池为空 / 日历为空 / 数据版本为空 / 越界 symbol；按 hash 取不到 | 400 / 409 / 404 的分诊口径见该文件头注释；哨兵只增不删（**唯一例外**：`ErrFactorNotFound` 随裁-14 撤路由一并删除，不再有第二个「因子读面在本仓」的真相） |
| `assembly.ErrInvalidBatch` | ✅ 2026-10-25（H4 ②） | 投点批次不合法：缺 `data_type` / 空批次 / 点上的 `data_type` 与批次不一致 / `source` 空白 | 400 `invalid_batch`；**一条哨兵四种报文**（同属「调用方修请求」），拆四条就是 §2 纪律②禁的同义两义 |
| `assembly.ErrBatchTooLarge` | ✅ 2026-10-25（H4 ②） | 批次数 > `MaxPointsPerRequest`（10000） | 413 `batch_too_large`；**整批拒收**，调用方按上界分片，不截断（dataflow §2 红线） |
| `assembly.ErrMisconfigured` | ✅ 2026-10-25（H4 ②） | 装配漏了 registry 或写面 sink | 装配期返回错误；不是数据拒收，处置是改装配代码 |
| `ingest.ErrDeclarationMissing` | ✅（`internal/ingest/declaration.go`） | 投了一个**没有任何 connector 声明过可见语义**的 data_type | 400 `data_type_undeclared`；写面**先查声明再动数据**，不触达 store |
| `normalize.ErrVisibleAtMissing`（= `ingest.ErrVisibleAtMissing` = `store.ErrVisibleAtMissing`） | ✅ | 任一点缺声明锚点 | 400 `visible_at_missing`，**整批不落**；三条路径同一个哨兵同一个码，不留三个名字 |
| `ingest.ErrAnchorInvalid` | ✅（`internal/ingest/gate.go`） | 锚点列有值但解析不出日期 | 400 `anchor_invalid`；与「缺锚点」必须是两个码（一个修数据、一个修采集） |
| `store.ErrWriteTableNotInASet` / `store.ErrUnmappedDataType` | ✅（`internal/store/write.go`） | 目标表在 B/C 集 / 该 data_type 没有写面映射 | 400 `write_table_not_in_a_set` / `data_type_unmapped`；处置不同（换一个不该投的 vs 补一份 spec），所以两个码 |
| `store.ErrContentConflict` | ✅（`internal/store/write.go`） | 同主键已存在且内容不同（含**换源**） | 409 `content_conflict` + **整批回滚**；不许重试成覆盖，走受控修正流程（ADR-0004/0006） |
| `store.ErrInvalidPoint` | ✅（`internal/store/write.go`） | 载荷字段类型不对（如数值列给了字符串） | 400 `invalid_point`；单行折跳计入 `skipped`，与整批拒收分得开 |
| 未登记的失败 | —— | 上面都没命中 | 500 `internal`：**不把未知失败伪装成 4xx**（分诊表在 `assembly.WireError`，新增哨兵必须同批登记，否则红在 `TestWireErrorCoversEveryWriteFailure`） |

## 3. 幂等性与重试

- `dataset_hash` 是内容寻址。**唯一定义 = `tellurion-contracts:engine/provenance.go` 的 `DatasetSnapshotInput`（六分量：`universe_hash` / `window` / `frequency` / `data_version` / `calendar_hash` / `adjustment_basis`）**，本文件不重述分量（QT-03 于 2026-10-21 冻结；旧四项写法缺 `calendar_hash` 与 `adjustment_basis`，会导致「供应商补一个节假日 → 同 hash 不同结果」，已订正为引用。core 的 `pkg/backtest/contracts` 已迁入 contracts 仓，旧路径作废）。
- 本仓**不许自行实现 hash**：调 `contracts` 的 `DatasetSnapshotInput{...}.Hash()`。
- 同一 hash 的读请求可任意重试（无副作用）；返回内容重算 hash 不符 ⇒ `ErrSnapshotMismatch`。
- 写入幂等靠 `sync_jobs` / `sync_schedules` 的任务状态 + 数据点主键（目标迁 `ingest`，见 `db-schema.md` §1.5）。
- **`POST /ingest/points` 是内容比对幂等，不是「请求体 hash 幂等」**（H4 ②）：同批次重放 ⇒ 每行走 `ON CONFLICT DO NOTHING` + 全列内容比对 ⇒ no-op 且计入 `persisted`（`persisted` = 「请求点已就位」，不是新增行数）。因此**不需要** `Idempotency-Key`：判定按行做，不按键做，重放与首次的落库结果逐字节一致。三条推论要如实读：
  1. `ingest_time` **不参与比对**（每次重放都不同，比它必然假冲突）——这是有意的排除，代价是「仅入库时刻变化」不算内容变化；
  2. `source` **参与比对**：同一主键换一个源 = 行内容真的变了 ⇒ 409 `content_conflict` 整批回滚，不静默覆盖；
  3. 409 **不可靠重试消解**（同一批重放还是 409），必须人工判定是「上游改了历史」还是「口径混了」，走一次受控修正（本仓还没有那条流程，见 ADR-0004 代价 ④）。

## 4. 版本与兼容

- 协议类型（`UnifiedDataPoint` 等）住在 contracts，版本随契约走。**包名与迁入时机**见对表 A 组注 ①/②：定名 `contracts/dataprotocol`，那一笔挂在 X1b 第一 commit。判据的**条款号是订正过的**（2026-10-22，QT-40 对表 T2 核对上游时逮到）：不许提前迁的那条是 contracts `docs/adr/0001` **①**「本仓只放被 ≥2 个仓使用的类型」（`tellurion-contracts:AGENTS.md` §3 写作「只有单个仓使用的类型属泄漏」），**不是 ③**——③ 管的是 `Config`/`Options`/`Impl` 命名。⚠️ 同一份 ADR 的「代价」节又接受 S1 整包迁时带进 core-only 类型、事后按清单回收，于是「新建包要不要走同一条容忍」成一个真岔路：**两个选项都自洽，登记在 `tellurion-contracts:docs/API.md` §8 待裁 #4，X1a 前挑定并写回本节**，不要 core / data 两侧各按一条施工。
- 端点只增不删；废弃走 `deprecated` 头 + 一个 MINOR 周期。
- 字段语义变更（尤其 `visible_at`）算 breaking，必须走 ADR。

## 5. 不提供什么（边界）

- **不提供「替调用方取数」的写入口**：`POST /ingest/points` 只收**已归一**的点位。给它一个 `FetchRequest` 让它去上游取数 = 把传输实现搬进本仓（AGENTS.md §3 禁区，传输仍在 core）；给它原始报文 = 把归一化口径做成第二套。
- **不提供无界批量**：单次投点 ≤ 10000 点 / ≤ 8 MiB（§1 三条硬规矩之二）。无界批量属于作业层，不是「一个更大的请求」。
- **不接受投送的 `visible_at` / `ingest_time`**（§1 三条硬规矩之一）：本仓不替调用方判定「什么时候可见」。
- 不提供逐 bar / 逐笔取数接口
- **不把本仓做成「HTTP 包装的通用 DB 网关」**（`tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2 对 ADR-027 §7 的对账结论）：core `cmd/data` 今天那批 `GET /ohlcv/:symbol`、`GET /fundamental/:symbol` 是**逐 symbol 逐日期的 DB 代理形状**，X1b 的归位是**并入 resolve + dataset**，不是原样搬进本仓——逐个端点的去处见对表 D 组
- 不提供回测结果存储（那是 run）
- 不提供策略 / 因子**计算**，也**不提供因子读面**（裁-14 撤掉 `/factors/{name}@{hash}`）——因子定义与值一律由 core 提供；**因子定义与 SeriesSpec 归 strategy**（ADR-027 §2.6.1 / ADR-028 §6），求值在消费点（§7「不做因子服务」）；三条界线的定案见对表 G 组 L1
- 不提供跨 schema join（宁可多一次调用）
