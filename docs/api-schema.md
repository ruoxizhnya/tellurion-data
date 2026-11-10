---
status: active
last-verified: 2026-11-07
verified-by: SVC-DATA-G4 —— 失败语义四类、超时/重试边界、单行访问日志（不接 Prometheus）、降级启动与 503 自愈；真实 PG 恢复腿当晚已补跑（200→503→200 同一进程自愈）
---

# API Schema — tellurion-data

> **实现状态（2026-10-25 更新，QT-40 H4 ②）**：读面 `GET /datasets/resolve` 与 `GET /datasets/{hash}` 已落地；**写面第一条 `POST /ingest/points` 同日落地**（`internal/httpapi/server.go` handler + `internal/assembly` 装配层 + `cmd/dataservice` 接线）。装配开关：`DATABASE_URL`（DSN）、`DATA_MARKET_SCHEMA`（默认 `market`；物理搬迁完成前设空串走 `public`）；DSN 为空 ⇒ 读面与写面都 fail-closed 503（`/health` 常绿）。`/factors/{name}@{hash}` **已于裁-14 撤掉**（路由不存在，不是恒 404 占位）：因子读面只在 core。同 hash 两次拉取逐位一致已由 wire 级测试钉住（`server_test.go` `TestSnapshotSameHashReturnsByteIdenticalBody`）。
>
> ⚠️ **写端点已于 2026-11-07 晚在真 PG 上跑过**（便携 PostgreSQL 16.2 @ 55432，`DATA_MARKET_SCHEMA=market`）：同内容两次 `200 {"received":1,"persisted":1,"skipped":0}`；同主键改内容 → `409 content_conflict`；空库/停库时 `503 dependency_unavailable`。此前只有替身 Exec 的测试腿；真库门禁现在由 `internal/store` 的 `DATABASE_URL`-gated PG 套件 + 这次真二进制演练共同覆盖。
>
> ✅ **G3 只读兼容面已于 2026-11-10 在真 PG 上端到端跑通**（PostgreSQL 16.15 @ 5432，测试库 `tellurion_data_test`，空 `JWT_SECRET` 回环开放）：7 条端点全部返回预期形状（含 `/stocks/{symbol}` 404、bulk 未知票 `ohlcv: []` 不影响整体 200）；`/fundamental` 的 PIT 正确排除未来期；`stocks` 的 HTTP 回执与同库直读 SQL **md5 完全一致 `40f25781e835b1ff1e2d0e8d6a729fe5`**（2 行），`ohlcv` 行数/`sum(close)=21.2`/`sum(volume)=3469134` 双侧一致。证据与命令见 `docs/TASKS.md` `SVC-DATA-G3` 施工记录。

> **契约地位**：HTTP，对外唯一数据面。**aggregator 是唯一读入口**，connector 与存储不直接对外；写面只有投点这一条，且**只接受有界批次**。
> 本文件是**本仓对外接口的唯一真相源**。代码与它不一致时，先判断谁错：契约错 → 改本文 + 立 ADR；实现错 → 改代码。
> 跨仓消费方（core / agent / ui / 其他服务）按本文件编码，**不读本仓实现**。
> **鉴权（2026-10-25 已接线，ADR-0005）**：机制在 kit `v0.1.4` 的 `auth/httpadapter`（2026-10-25 引入 `v0.1.1`，同日随 kit `KT-11` 升 `v0.1.4`——`v0.1.1` 会把 `sub=service:<repo>` 的非数字 subject 当场 401，服务间调用走不到策略表），策略表在本仓
> `internal/httpapi/policy.go`。`/health` 与 `/readyz` 公开；数据面读端点接受 `service`（服务间调用，裁-17）或
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
| GET | `/ohlcv/{symbol}?start_date=YYYYMMDD&end_date=YYYYMMDD` | **G3 只读兼容**（core 旧读链）：日线 ⇒ `{ohlcv:[OHLCV]}` |
| GET | `/fundamental/{symbol}?date=YYYYMMDD` | **G3 只读兼容**：`date` 当日已可用的最新一期财报 ⇒ `{fundamental: Fundamental}`；无记录 404 `not_found` |
| GET | `/stocks[?exchange=]` | **G3 只读兼容**：股票列表 ⇒ `{stocks:[Stock]}` |
| GET | `/index/{code}/constituents[?date=YYYYMMDD]` | **G3 只读兼容**：当日生效成分（缺省 = 今天）⇒ `{constituents:[IndexConstituent]}` |
| GET | `/api/v1/trading/calendar?start=YYYY-MM-DD&end=YYYY-MM-DD` | **G3 只读兼容**：交易日轴 ⇒ `{trading_days:["YYYY-MM-DD"]}` |
| GET | `/api/v1/stocks/{symbol}` | **G3 只读兼容**：单只股票 ⇒ `{stock: Stock}`；无该票 404 `not_found` |
| POST | `/api/v1/ohlcv/bulk` | **G3 只读兼容**（只读 POST，请求体只承载查询参数）：批量日线 ⇒ `{results:[{symbol,ohlcv,error?}]}`；逐票错误不整体失败 |
| POST | `/api/sync/jobs` | **作业层**：`{job_type, params, max_retries?}` ⇒ 202 `{job}`；`job_type` 即 data_type（单一真相），`params` 用 `FetchRequestFromParams` 的同一套解析（symbols / 日期 / inverted window 校验） |
| GET | `/api/sync/jobs[?status=&job_type=&limit=]` | **作业层**：`{jobs:[Job]}`（缺省最近 50 条，`status` ∈ pending/running/succeeded/failed/cancelled） |
| GET | `/api/sync/jobs/{id}` | **作业层**：单条作业；不存在 404 `job_not_found` |
| POST | `/api/sync/jobs/{id}/cancel` | **作业层**：只对 pending/running 生效；终态 409 `job_not_cancellable` |
| POST | `/api/sync/jobs/{id}/retry` | **作业层**：只对 failed/cancelled 生效（回到 pending、`retry_count+1`）；其余 409 `job_not_retryable` |
| POST | `/api/sync/schedules` | **排程**：`{name, job_type, cron_expression, params, is_active}` ⇒ 201；**`is_active` 未给即 false**（不主动跑生产取数），`next_run_at` 仅在激活时计算 |
| GET | `/api/sync/schedules` | **排程**：`{schedules:[Schedule]}` |
| PUT | `/api/sync/schedules/{id}` | **排程**：整体覆盖（含 `is_active`），重算 `next_run_at` |
| DELETE | `/api/sync/schedules/{id}` | **排程**：204；不存在 404 `schedule_not_found` |
| POST | `/api/sync/schedules/{id}/toggle` | **排程**：翻转 `is_active`；激活时从当前时刻推进 `next_run_at` |
| POST | `/api/sync/schedules/{id}/run` | **排程**：立即入队一次（不改 `next_run_at`）⇒ 202 `{job_id, next_run_at}` |

> **`job_type` 的两个保留字面值（2026-11-10，ADR-0008）**：除 data_type 外，`stocks` 与 `trading_calendar` 是**参考数据作业**，走 `ReferenceRunner` 的 upsert 腿（`market.stocks` / `market.trading_calendar`），**不经过**点位闸门与 `FetchRequestFromParams` 的 data_type 校验。
> - `stocks`：`params` 可给 `exchange`（空 = 全市场）、`list_status`（空 = `L` 上市中；退市要单独跑 `D`）。
> - `trading_calendar`：`params` 可给 `exchange`（空 = `SSE`）、`start_date` / `end_date`（空 = 当年；需要历史必须显式给区间）。
> - 结果里的 `source` / `fetched` / `persisted` 与点位作业同形；无 `TUSHARE_TOKEN` 时这两类作业以 `misconfigured` 失败（不会静默写 0 行）。


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
> - `POST /ingest/points`：已实现（H4 ②，2026-10-25）。**调用方今天只有 core 的两条作业**（`capital_flow` / `top_list`，见 ADR-0006）。**同步作业层已于 X1c（2026-11-10，裁决 B）迁入本仓**：`/api/sync/jobs` + `/api/sync/schedules` 共 11 条端点，由本进程内 Worker（2s 轮询）与 Scheduler（30s 扫 due）执行取数链；`POST /ingest/points` 仍是「别人取数、本仓落库」的接缝，两者共用同一条 `register → fetch → gate → dedup → bulk insert`。
> - **作业层语义（X1c，2026-11-10）**：状态只有 `pending / running / succeeded / failed / cancelled`五档；`job_type` 就是 data_type，不设第二套命名；失败原因写 `error_message`（截断 4000 字符）并在 `GET /api/sync/jobs/{id}` 可见；`result` 是 `{data_type, source, fetched, persisted, skipped, duration_ms}`。**worker 不自动重试**——失败即终态，重排是显式 `POST .../retry`。`TUSHARE_TOKEN` 为空时传输未装，作业会在行上响亮失败（`all adapters for <type> are disabled`），不会静默写 0 行。
> - **G3 只读兼容面（2026-11-10）**：上述 7 条已实现（`internal/httpapi/legacy_read.go` + `serving.LegacyReader` + `internal/store/reference.go`）。它们只按表读、按 core 旧字段名编码，**不带 `as_of`/`dataset_hash` 语义**，也不参与快照哈希——任何新调用方都该用 `/datasets/*`。**这组端点是主任裁决对 K1（2026-11-08 补裁 #11「本仓不补齐 core 的 5 条旧读面」）字面口径的推翻**，追认状态见 `docs/TASKS.md` SVC-DATA-G3 施工记录。**已知限制**：`stock_fundamentals` 的 DDL 仍是 core 的迁移（本仓只读），在 `DATA_MARKET_SCHEMA=market` 且该表未建时 `/fundamental/{symbol}` 会 500（K3 收编前不静默降级）。

> **`as_of` 与 `adjustment` 为什么必须在参数里**：前者是第二道防前视的输入（aggregator 按 `visible_at <= as_of` 过滤，定义权在本仓，见对表 L2）；后者由**请求方（core）给**、本仓只按参数取数并回算 hash——本仓**不读** `backtest_adjustment_anchors`（owner = core 回测域），否则 hash 口径两处各自算一遍（对表 L3）。

> **鉴权策略表（2026-10-25，裁-15 + H4 ②）**：
> - **本仓实际路由**（`servedPolicy`）：`GET /health`、`GET /readyz` = Public；`GET /datasets/resolve`、`GET /datasets/{hash}` = `service`（裁-17）+ viewer / trader / admin；**`POST /ingest/points` = 只 `service` + `admin`**。写面不放 `viewer`/`trader`，也不 Public：读档位与写档位混在同一档里，将来给写面收紧就说不清是谁在什么时候为什么放开的。`admin` 之所以必须在：core 的 `pkg/auth` 角色集今天仍是 {viewer, trader, admin}，`service` 要能签发是 QT-71 的活——不开 `admin` 这条腿今天就不可用（代价与订正条件见 ADR-0006「代价」节）。**G3（2026-11-10）**：6 条只读兼容 GET = `service` + viewer / trader / admin；`POST /api/v1/ohlcv/bulk` = any-authenticated（只读 POST，沿用 REV-17 口径，匿名仍拒）。**X1c 作业层（2026-11-10）**：`GET /api/sync/jobs`、`GET /api/sync/jobs/{id}`、`GET /api/sync/schedules` = `service` + viewer / trader / admin；`POST /api/sync/jobs`、`{id}/cancel`、`{id}/retry`、`schedules/{id}/run` = trader | admin；`POST /api/sync/schedules`、`PUT`、`DELETE`、`{id}/toggle` = admin（沿用 REV-17「排程管理 admin、其余变更 trader」的档位，迁移不重拍）。
> - **REV-17 逐条移植**（`legacyREV17`）：旧 `core:cmd/data` 的 28 条策略（排程管理 admin / 其余变更 trader / `POST /screen` 与 `POST /api/v1/ohlcv/bulk` any-authenticated）已逐条搬入本仓；**`POST /api/v1/ohlcv/bulk` 已于 G3（2026-11-10）落地并移入 `servedPolicy`；8 条作业/排程策略已于 X1c（2026-11-10）落地并移入 `servedPolicy`（`{id}` 形状、角色结论不变）⇒ `legacyREV17` 余 19 条**，其余端点仍由 core 提供（`cmd/data`）。保留这张表是为了端点迁移时「同一路由、同一角色」不再重新拍板；守卫 `TestLegacyREV17PolicyIsVerbatim` + `TestLegacyREV17DecisionsMatchOldService` 钉住档位与结论，且禁止同一路由同时出现在两张表。
> - 守卫：`TestRouteTableEqualsServedPolicy`（路由表 ≡ 实际策略表，**逐条点名 23 条**）；`TestServedPolicyWriteRouteRolesAreNarrow`（写面档位只有 service+admin，不 Public、不 any-authenticated）；`TestServedPolicyLegacyReadRoutes`（G3 七条档位 + bulk 不再属 legacy）；`TestIngestPointsAuthTiers`（service/admin 过闸到业务 503，viewer/trader/未知角色 403，无凭证 401）；`TestAuthWireThreeStates`（health 公开 / 无凭证 401 / viewer 可读 / 闸未装配 503）；`TestServedPolicyServiceRole`（service 过闸到 handler、未列角色仍 403、health 仍 Public）；作业层 HTTP 行为由 `internal/httpapi/sync_test.go` 覆盖（202/400/404/409/503、空数组、坏 cron、`run` 立即入队）。
> - **跨仓前置**：core `proxyRequest` 已转发入站 `Authorization`（2026-10-25）；`POST /api/v1/ohlcv/bulk` 经 core 代理不再 401。**写端点的调用方凭证**由 core 侧 env 注入（`INGEST_WRITE_*`），不在本仓签发。

### 1.1 服务探针与请求治理（G1，2026-11-07）—— ✅ 代码已落，真实 PG 腿当晚已跑

| 方法 | 路径 | 用途 | 鉴权 |
|---|---|---|---|
| GET | `/health` | 进程存活（**不读库**，库挂了也 200） | 无（公开） |
| GET | `/readyz` | 就绪：PG 池可连 + 本仓 DDL 已执行（`SELECT to_regclass('market.ohlcv_daily_qfq')`） | 无（公开） |

`/readyz` 三态（`readyBody = {status, store, error?}`）：

| 情形 | HTTP | `status` | `store` |
|---|---|---|---|
| `DATABASE_URL` 为空 / readiness 未注入 | 503 | `not_ready` | `unwired` |
| PG 不可达或 DDL 未执行 | 503 | `not_ready` | `postgres`（`error` 带原因） |
| 正常 | 200 | `ready` | `postgres` |

> **探针必须公开且豁免限流**：否则「鉴权配置坏了」会被读成「进程死了」，或把探针限流读成未登录
> （core 侧 AUD-57 的同型坑：探针 401 会把整站弹到登录页）。实现上公开路由直接注册，不经过
> `requireAuth`，因此也不经过限流。

**限流**：按调用方身份（`username` → `sub` → `open-access` 共用一桶）的进程内令牌桶，默认 **50 rps / 突发 200**
（`internal/httpapi/limit.go`，不引新依赖）。超限 = `429` + `code:"rate_limited"` + `Retry-After: 1`；
**鉴权先于限流**，因此 `401` / `403` 永远不会被 429 顶替。守卫 `TestRateLimitDistinctFromAuthAndProbesExempt`。

**错误码闭合表**：§2 那张表就是**完整清单**——表里没有的码视为缺陷。代码侧唯一分诊点是
`internal/httpapi/server.go` 的 `sentinelCodes`（读面）与 `internal/assembly.WireError`（写面），
未登记错误一律 500 `internal`。读面 9 个哨兵逐条在表里（含 `ErrEmptyDataVersion → 409
empty_data_version`），用例 `TestEmptyDataVersionMapsToConflict` 钉住其中一条的码字与状态码，
防止有人把冲突类错误顺手改成 500。

**分页（统一策略：整段返回 + 声明上界，本轮不引 cursor）**：本仓**没有集合类读端点**——
`/datasets/resolve` 与 `/datasets/{hash}` 各自只返回**一个**数据集（`{dataset_hash, metadata}` / 一个
`Dataset`），不存在「翻页」。声明上界：`POST /ingest/points` ≤ 10000 点 / ≤ 8 MiB（超限 413）。
将来新增任何列表端点，必须**要么**给 `limit` / `cursor`，**要么**在本文写明整段返回的条数上界与超限行为；
不允许「返回全部且不声明」。（`GET /rules` 式整段返回的判据可参照 `tellurion-risk:docs/api-schema.md` §1。）

**幂等（写面重投）**：`POST /ingest/points` 以行主键 + 全列内容比对判定，**重复投的返回码与首次一致**
（200，`persisted` 计入已就位的点，`skipped` 计入重复/非法点）；`source` 参与比对，换源 = 409
`content_conflict`。详见 §3。

### 1.2 可观测最小面（G4 ③）—— 不接 Prometheus，只有一行访问日志

**显式声明**：本服务是单人自托管、单进程，**不引 APM / OpenTelemetry / Prometheus 客户端，
不暴露 `/metrics`**。引入指标栈的运维成本高于它今天能回答的问题；要回答的只有两件：
**每端点错误率**与**延迟**。

实现是一条固定格式的访问日志（`internal/httpapi/server.go` 的 `withAccessLog`，无新依赖）：

```
httpapi: route="GET /datasets/resolve" status=200 dur_ms=3.412318
```

| 字段 | 含义 | 用途 |
|---|---|---|
| `route` | **注册的模式**（如 `/datasets/{hash}`），不是原始路径 | 避免每个 hash 变成一条独立序列 |
| `status` | 最终状态码（含 401/403/429/503） | 错误率 = `status>=500` / 总数；4xx 单看 |
| `dur_ms` | 处理耗时（毫秒，六位小数；微秒级请求也能看出差别） | 平均 / 最大值 |

- 只有进入路由的请求会打日志；**404 / 405 由 ServeMux 直接返回，不在这行里**——排查「调用方
  打错路径」要看调用方日志，不要在本服务日志里找。
- `dur_ms` 受宿主时钟分辨率限制：本机（Windows）实测最小非零间隔约 0.5ms，所以很快的请求会记成
  `0.000000`（含义是「<0.5ms」，不是「零耗时」）。判断量级用慢请求（`/readyz`）与
  `TestAccessLogDurationMeasuresSlowHandler` 的守卫，不要拿 0 当异常。
- 日志里**不写报文内容、不写 token、不写 DSN**。
- 聚合命令（PowerShell 5）与真实输出见 `docs/runbook.md` §2。
- 故障时先看 `/readyz` 的 `error`，再按 §5 的排查顺序定位；**没有指标面板可看，这是有意的**。

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
| 请求超限 | ✅ 2026-11-07（G1） | 同一身份令牌桶耗尽（默认 50 rps / 突发 200；探针豁免） | 429 `rate_limited` + `Retry-After`；不重试到成功为止，按退避重试 |
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
| `store.ErrDependencyUnavailable` | ✅ 2026-11-07（G4，`internal/store/availability.go`） | PG 拨号失败 / SQLSTATE `08` 连接异常 / 连接或语句超时 / 查询中途断开 | 503 `dependency_unavailable`；**可退避重试**，恢复后同一进程自愈。只包连接层，坏 SQL 仍是 500 |
| `store.ErrInvalidPoint` | ✅（`internal/store/write.go`） | 载荷字段类型不对（如数值列给了字符串） | 400 `invalid_point`；单行折跳计入 `skipped`，与整批拒收分得开 |
| 未登记的失败 | —— | 上面都没命中 | 500 `internal`：**不把未知失败伪装成 4xx**（分诊表在 `assembly.WireError`，新增哨兵必须同批登记，否则红在 `TestWireErrorCoversEveryWriteFailure`） |

### 2.1 失败语义四类（G4 ①）

> 只列**今天真的会发生的四类**。每行回答四个问题：HTTP 码、是否重试、是否落痕、调用方该做什么。
> 「落痕」指本仓**留下的证据**；不在本仓的腿必须写明「不在本仓」，不许写成「有日志」。

| 类别 | 触发与判据 | HTTP 码 | 是否重试 | 是否落痕 | 调用方该做什么 | 真实用例 / 为什么没有 |
|---|---|---|---|---|---|---|
| **PG 不可达** | 拨号失败、SQLSTATE `08`、连接/语句超时、查询中途断开；`internal/store.classifyDependency` 统一折成 `store.ErrDependencyUnavailable` | **503** `dependency_unavailable`（读面与写面同码） | **调用方重试**：指数退避（1s 起，封顶 30s），依赖恢复后同一进程自愈。本仓**不在请求内重试** | 是。访问日志 `status=503`；`/readyz` 的 `error`；降级启动时 `data-service: schema not applied yet ...`，恢复时 `schema applied after retry` | 不要当成 404「没数据」，也不要换源/兜底数据；退避重试，连续失败超过 3 分钟告警。503 **不带** `Retry-After`（恢复时间不可知），与 429 区分 | ✅ 真跑：DSN 指向 `127.0.0.1:5999`（无监听）→ `/datasets/resolve`、`/ingest/points` 均 503 且进程不退出（`docs/runbook.md` §7）；单测 `TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal`、`TestResolveMapsDependencyUnavailableTo503`、`TestClassifyDependency`、`TestIngestPointsMapsWriteFailuresToCodes/pg_unavailable` |
| **数据源配额耗尽** | `ingest.ErrRateLimited`（`internal/ingest/adapter.go`）；由传输/connector 在源侧配额用尽时返回 | **无 HTTP 码**：本仓没有任何路由触发取数，`POST /ingest/points` 只收**已归一**的点 | 不在同一源上重试；`Registry.Fetch` **换下一个源一次**（`IsRetryable`）。无备源则原样返回作业层 | **不在本仓**：本仓没有生产取数调用方（传输在 core），故没有请求日志可看；落痕在作业层 | 作业层决定：等配额窗口重置或换源；**不要高频重打**（配额错误重试不会成功，只会把窗口耗得更久） | ✅ 单测 `TestRegistryFetch_QuotaExhaustedFallsBackToNextSource`、`TestRegistryFetch_QuotaExhaustedWithNoFallbackReturnsRateLimited`。**HTTP 面无用例及原因**：没有 HTTP 端点调用 `Registry.Fetch`，造不出这个响应 |
| **connector 超时** | 出站 HTTP 客户端超时 / 上游 5xx ⇒ `ingest.ErrUpstreamUnavailable`（`internal/ingest/adapter.go`） | **无 HTTP 码**，同上：取数不在本仓 HTTP 面 | 不在同一源上重试；`Registry.Fetch` 换源一次；无备源则返回作业层 | **不在本仓**：超时发生在 core 的传输/作业进程，日志在那边 | 作业层按自己的超时预算重排任务；区分「源慢」与「本仓慢」——本仓的慢看访问日志 `dur_ms` | ✅ 单测 `TestRegistryFetch_FallsBackOnRetryableError`（同一条可重试分诊路径）。**HTTP 面无用例及原因**：同「配额耗尽」，没有路由触发 connector |
| **内容冲突** | 同主键已存在且全列内容不同（含换源）⇒ `store.ErrContentConflict` | **409** `content_conflict` | **不重试**：同一批重放仍是 409，重试不会消解 | 是。访问日志 `status=409`；错误报文带主键（`600519.SH ohlcv_daily`） | **人工判定**：上游改了历史 vs 口径混了；走受控修正，不许改成自动覆盖（ADR-0004/0006）。修好后重投同一批 | ✅ 单测 `TestIngestPointsMapsWriteFailuresToCodes/content_conflict`；落库层 `TestWriter_DifferentContentFailsLoudAndRollsBack`（整批回滚，不写半批）。真库腿未执行（见 §7） |

**两个跨类的硬规矩**：

1. **503 不重试到成功为止**：本仓不在请求内 sleep/重试；HTTP 层只有一次尝试。重试属于**作业侧**
   的调度语义（有退避、有上限、有告警），塞进请求会把「库挂了」变成「请求排队把连接池拖死」。
2. **409 与 503 永不互换**：409 是「数据真的冲突」，503 是「现在读不到」。把 409 读成 503 会让调用方
   无限重试；把 503 读成 409 会让调用方以为历史被改了。

## 3. 幂等性与重试

- `dataset_hash` 是内容寻址。**唯一定义 = `tellurion-contracts:engine/provenance.go` 的 `DatasetSnapshotInput`（六分量：`universe_hash` / `window` / `frequency` / `data_version` / `calendar_hash` / `adjustment_basis`）**，本文件不重述分量（QT-03 于 2026-10-21 冻结；旧四项写法缺 `calendar_hash` 与 `adjustment_basis`，会导致「供应商补一个节假日 → 同 hash 不同结果」，已订正为引用。core 的 `pkg/backtest/contracts` 已迁入 contracts 仓，旧路径作废）。
- 本仓**不许自行实现 hash**：调 `contracts` 的 `DatasetSnapshotInput{...}.Hash()`。
- 同一 hash 的读请求可任意重试（无副作用）；返回内容重算 hash 不符 ⇒ `ErrSnapshotMismatch`。
- 写入幂等靠 `sync_jobs` / `sync_schedules` 的任务状态 + 数据点主键（目标迁 `ingest`，见 `db-schema.md` §1.5）。
- **`POST /ingest/points` 是内容比对幂等，不是「请求体 hash 幂等」**（H4 ②）：同批次重放 ⇒ 每行走 `ON CONFLICT DO NOTHING` + 全列内容比对 ⇒ no-op 且计入 `persisted`（`persisted` = 「请求点已就位」，不是新增行数）。因此**不需要** `Idempotency-Key`：判定按行做，不按键做，重放与首次的落库结果逐字节一致。三条推论要如实读：
  1. `ingest_time` **不参与比对**（每次重放都不同，比它必然假冲突）——这是有意的排除，代价是「仅入库时刻变化」不算内容变化；
  2. `source` **参与比对**：同一主键换一个源 = 行内容真的变了 ⇒ 409 `content_conflict` 整批回滚，不静默覆盖；
  3. 409 **不可靠重试消解**（同一批重放还是 409），必须人工判定是「上游改了历史」还是「口径混了」，走一次受控修正（本仓还没有那条流程，见 ADR-0004 代价 ④）。

### 3.1 超时与重试边界（G4 ②）

**超时（全部是硬上限，不是「建议」）**：

| 边界 | 值 | 落点 | 到点后的表现 |
|---|---|---|---|
| HTTP 读请求头 | 5s | `cmd/dataservice` `http.Server.ReadHeaderTimeout` | 连接关闭（不是 5xx） |
| HTTP 读请求体 | 30s | `ReadTimeout` | 连接关闭；8 MiB 上限另由 handler 的 `MaxBytesReader` 拦成 413 |
| HTTP 写响应 | 120s | `WriteTimeout`（**声明值**，未在真实负载下定标） | 连接中断；调用方看到的是网络错误，不是 5xx |
| HTTP 空闲连接 | 120s | `IdleTimeout` | 回收 |
| `/readyz` 探针 | 3s | `internal/httpapi` `readinessTimeout` | 503 `not_ready`，`error` 写原因 |
| 启动/重放 DDL 单次 | 30s | `schemaStartupTimeout` | 记为失败；数据端点保持 503，10s 后重试 |
| PG 拨号 | 5s | `internal/store` `connectTimeout` | 503 `dependency_unavailable`（`*pgconn.ConnectError`） |
| PG 单条语句 | 30s | `internal/store` `statementTimeout`（`statement_timeout=30000`） | SQLSTATE `57014` ⇒ 503 `dependency_unavailable` |
| 出站 connector | **不在本仓** | 传输在 core；本仓没有出站 HTTP 调用 | —— |

**重试（本仓的答案几乎都是「不重试」）**：

| 场景 | 本仓重试次数 | 谁重试 | 理由 |
|---|---|---|---|
| 读端点（resolve / snapshot） | 0 | 调用方可任意重试 | 纯读、无副作用；本仓重试只会放大故障时的连接占用 |
| 写端点（ingest/points） | 0 | **调用方/作业侧** | 写入是内容比对幂等，重放安全；请求内重试会在库挂时把连接池拖死 |
| PG 不可达（503） | 0（请求内） | 调用方，指数退避 | 「库什么时候回来」不应由每个请求各自猜；恢复后同一进程自愈 |
| 启动 DDL | 每 10s 一次，**不设上限**，直到成功或进程退出 | 本仓后台 | 单人自托管只看「最终起来」；10s 间隔不构成对库的压测。真实 PG 恢复腿已于 2026-11-07 晚真跑（200→503→200） |
| connector 配额/超时 | 同一源 0 次，换源 1 次 | 作业侧 | 换源是 ETL 语义，不是 HTTP 语义；同源重打只会更糟 |

> **为什么本仓不重试、由作业侧重试**：重试需要三样本仓没有的东西——**任务状态**（同一作业重跑
> 到哪一步）、**退避与上限**（谁来决定第几次放弃）、**告警归属**（失败该通知谁）。三者都在作业层
> （今天的 core `cmd/data`，迁入后仍是作业进程）。本仓是**无状态数据面**，只负责一次判定的结果；
> 在请求里重试会把「依赖故障」翻译成「请求堆积」，把 503 的可观测性也一起毁掉。
> 反过来，**启动迁移重试是本仓自己的事**：它没有上游作业可依赖，且必须让进程活到库恢复。

## 4. 版本与兼容

- **版本策略（G1，2026-11-07）**：当前路径即 **v1**（`/datasets/*`、`/ingest/*` 不写 `/v1`）。
  **MINOR**（加可选字段 / 加端点 / 加错误码）= 不改路径、旧客户端不感知；**MAJOR**（删字段、改字段语义、
  改已有错误码含义）= 新路径段 `/v2` + 一个版本周期的双写窗口，**禁止同路径改语义**。
  本仓今天没有 openapi 文件（正式 schema 的真相源在 core，QT-50 ③-③），版本纪律以本节 + §2 表为准。
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
