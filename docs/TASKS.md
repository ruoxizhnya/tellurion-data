---
status: active
last-verified: 2026-11-12
verified-by: 2026-11-12 跨仓伞五腿——`X-LOW-CLEANUP`（登记分页口径不一致：`/api/evidence` 有 `offset`、`/api/sync/jobs` 只有 `limit`，只登记不改行为）、`X-DEP-BUMP-CONSUMERS`（kit `v0.2.0 → v0.5.0`、`go` 1.26.11、pgx 随 MVS 到 `v5.11.0`，CI 增 `govulncheck` 门，降版破坏验证复红、还原复绿；同日补 Dockerfile 工具链到 `golang:1.26.11-alpine`）、`X-AGENTS-DEPENDENCY-TABLE`（`AGENTS.md` §4 按实际 `go.mod` 订正 contracts v0.10.0 / kit v0.5.0 / pgx v5.11.0）、`X-CI-RACE-STATICCHECK`（CI 补 `-race` 与 `staticcheck`；存量 2 条按「先判死代码」清理：`yahooToFloat64` 与 `fakeExecutor.seenJob`；`-race` 用 Docker golang:1.26 全量跑通 EXIT=0）、`X-PROBE-NAMING`（`GET /healthz` 为规范名、`/health` 改过渡弃用别名：报文逐字节相同 + `Deprecation` / `Link` / `Warning` 三信号；路由总数按真实注册订正 28→29；真起服务双路径验证 `/healthz` 200 / `/health` 200 同报文 / `/readyz` 503；摘除前置条件与 core 两处消费者〔`tellurion:docker-compose.yml:160`、`tellurion:tools/local-stack.sh:82`，只记录不动〕见 `docs/api-schema.md` §1.1）；同日新立 `DATA-PAGINATION-ALIGN`（⬜ P2，offset 对齐方案 + 消费者清单 + cursor 重开条件，只写单未改代码）；此前 2026-11-12 收口 `DATA-GRACEFUL-SHUTDOWN`（优雅停机 + 作业租约/心跳/stale 回收，见归档）并把 `DATA-INGEST-RAW` / `DATA-INDEX-CONSTITUENTS` / `DATA-FUNDAMENTALS-READ-PUBLIC` 三条已完成项移入归档，本表只留未做/挂起 —— `DATA-REFDATA-JOB-WRITE`（等真实 `TUSHARE_TOKEN` 部署）、`DATA-LISTING-WINDOWS`、`DATA-TRANSPORT-MULTI`、`DATA-MOOTDX`、`DATA-DIVIDENDS`、`DATA-SPLITS`、`DATA-MARKET-ACTIVITY` 均为 ⏸。此前 2026-11-11 W7 台账立单（A5 授权）——新立 2 条六字段单，证据坐标引 ODR-069/072（`tellurion-docs:docs/archive/odr/`）；`DATA-FUNDAMENTALS-READ-PUBLIC` 立单复核时实测前提已被主任 2026-11-12 归属裁决〔data `a454726` 删 `/fundamental` 读面 + core `c85abff` 建 `GET /api/fundamental/:symbol`〕取代，已标 ✅ 作废、不施工。此前 2026-11-11 文档对齐审计与迁移 —— ① 镜像名统一 `tellurion/data:${DATA_IMAGE_TAG:-latest}`；② contracts pin `v0.6.0 → v0.9.0`，6 个 `domain` 根别名本仓**零引用**（`internal/ingest/tushare_adapter.go` 的根别名 `Fundamental` 已改 `market.Fundamental`），`CT-06-ALIAS-EXIT` 本仓侧完成（`build/vet/test` 全绿）；③ ARCH 的 X0 复核为 ✅（core 已落 `dataset_snapshot_hash`）；④ 8 条剩余项均为 ⏸ 且重开条件仍准确；⑤ 2026-11-11 新登记 `DATA-LISTING-WINDOWS` ⏸（agent 已删除无服务的 listing-windows 客户端腿，见 `tellurion-agent:0eaed19`）。此前：2026-11-10 core 部署面同日修复（compose/e2e/k8s/checker 让 data-service 拿到 `DATABASE_URL` + `DATA_MARKET_SCHEMA`；四条破坏验证真做真还原；Docker daemon 不可达未跑容器腿）等
---

# TASKS — tellurion-data 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整时登记 / 承接的挂起项。此前已收口单的正文与施工记录全部在归档文件里；2026-11-12 收口的单（`DATA-GRACEFUL-SHUTDOWN`、`DATA-INGEST-RAW`、`DATA-INDEX-CONSTITUENTS`、`DATA-FUNDAMENTALS-READ-PUBLIC`）已移入归档的「2026-11-12 收口」节。

### DATA-PAGINATION-ALIGN · 两个列表端点分页口径统一（`/api/evidence` vs `/api/sync/jobs`）[P2] ⬜

- **① 目标**：把本仓两个列表端点的分页参数与回显统一成同一套，消费方不必按端点写两套翻页；本条只做**加法**（补 `offset` + 回显），不删既有参数。
- **② 上下文（2026-11-12 实测）**：
  - `GET /api/evidence`：`limit + offset`（`internal/httpapi/evidence.go:133-176`）；`limit` 缺省 20 / 上限 100，`offset` 缺省 0，越界 400；响应 `{items, limit, offset}`；SQL 按 `fetched_at DESC`。
  - `GET /api/sync/jobs`：只有 `limit`（`internal/httpapi/sync.go:90-115`）；只校验「正整数」，无上限、缺省 50；响应 `{jobs}`；SQL `ORDER BY created_at DESC LIMIT $3`（`internal/sync/pg_store.go:77-89`）。
  - 登记出处：`X-LOW-CLEANUP`（`e700080`，`docs/api-schema.md` §1「分页口径不一致」段）。
- **③ 要求（建议形状：offset 对齐，不引入 cursor）**：
  - `GET /api/sync/jobs?status=&job_type=&limit=&offset=`：`offset` 缺省 0、非负、非法 400 `invalid_parameter`；`limit` 保持缺省 50，补上限 200；响应加两个回显字段 ⇒ `{jobs, limit, offset}`（老客户端忽略新字段即可）。
  - 存储面：`syncjob.JobFilter` 加 `Offset int`，`internal/sync/pg_store.go` 的 `ListJobs` SQL 加 `OFFSET $4`；`fakeJobStore` 同步签名。
  - **为何不是 cursor（keyset）**：① 两个端点都是「按时间倒序看最近 N 条」，今天没有深翻页/全量遍历消费者（UI 作业控制台首屏 20–50 条；证据查询只按 hash 取单条，`tellurion-ui:web/src/api/evidence.ts:12-14`）；② cursor 要改响应形状（`next_cursor`）并同时改两个端点，属**破坏性**，offset 是加法；③ `sync_jobs.created_at` 无唯一性，keyset 还得引入 `(created_at, id)` 复合游标，成本高于当前需求。证据列表虽 append-only、offset 在并发写入下可能重复/漏项，但「最近 N 条查看」可接受；**全量导出/审计遍历**出现时再立 cursor 单（见重开条件）。
- **④ 约束与非目标**：不改 `jobs` 数组元素形状；不改 `status` / `job_type` 过滤语义；不给 `/api/evidence` 改排序或加 cursor；不动 core（core 只反代单条 `GET /api/evidence/{content_hash}`，`tellurion:docs/openapi.yaml:2757`）。
- **⑤ 验收**：
  - handler 腿：`offset` 缺省 0、负数/非整数 400、超过末页返回空数组；`limit` 上限 200 越界 400；响应回显 `limit` / `offset`。
  - PG 腿（`DATA_TEST_DSN=…tellurion_data_test`）：同一稳定快照下 `limit=1&offset=1` 返回第 2 条，不重不漏。
  - 消费方回归：`tellurion-ui` 的 `listSyncJobs`（`web/src/api/sync.ts:36-41`）与 `tellurion:tools/sync_all.sh:128` 不传 `offset` 时行为不变；UI e2e `e2e/tests/data-sync.spec.ts:92` 的 `GET /api/sync/jobs` 仍绿。
  - 文档：`docs/api-schema.md` §1 删「分页口径不一致（登记）」段，改为两端点统一说明。
- **⑥ 边界**：本仓读面参数与 SQL；UI 若要加「下一页」交互归 UI 单独立项；core 无列表消费，不在本次。
- **重开条件（改 cursor）**：出现全量遍历 / 审计导出 / 深翻页消费者，或 `sync_jobs` 数据量让 offset 扫描成为实际问题时，另立单改 keyset（`(created_at, id)` 复合游标），并把本条的回显字段改成 `next_cursor`。

### DATA-REFDATA-JOB-WRITE · 参考数据作业级写入未验 + 既有库 DDL 不自动补齐 [P1] ⏸

- **① 目标**：证明「作业」这条路径（而不只是 writer 单测）真能把参考数据写进部署库，并把既有库的约束缺口交代清楚。
- **② 上下文**：`c344ec2` 的契约测试走的是真实 `Client` → 真实 `ReferenceWriter`，`c58e783` / `4afe14c` 补齐了 `delist_date` 与日历写侧守卫；**作业级**（`internal/sync` 队列 → 调度 → 落库）在部署库上尚未跑过一次完整 `stocks` + `trading_calendar`。另外 `exchange` 的 `NOT NULL DEFAULT` / `CHECK` 只对**新建表**生效，既有库不会自动补约束。
- **③ 要求**：① 部署后跑一次 `stocks` 与 `trading_calendar` 作业，记录行数、`delist_date` 回读值、失败行数；② 对既有库逐条核对 `db-schema.md` 里的约束是否在位（不在位就补 `ALTER`，或明确登记为「靠写侧守卫」）；③ 结果写进本节（或另立收口记录）。
- **④ 约束与非目标**：不改 writer 契约与 DDL 形状；不在本单引入通用迁移框架。
- **⑤ 验收**：两条作业的真实行数与 `delist_date` 回读；既有库约束核对表逐行有结论。
- **⑥ 边界**：本仓作业面与 schema；生产库执行归运维。
- **重开条件**：一次带真实 `TUSHARE_TOKEN` 的部署。

### DATA-LISTING-WINDOWS · point-in-time 上市/退市窗口 [P3] ⏸

- **① 目标**：给下游「取池子时按日在市」提供可判定的上市/退市窗口。
- **② 上下文**：core QT-67 的 `POST /api/v1/listing-windows` 已随 `SVC-CORE-DATA-RETIRE` 退役，今天没有任何服务提供；agent 的偏差维曾调它，但只会 404 后静默退回 `PoolSourceCurrent`，已于 2026-11-11 删除该客户端腿与生产接线（`tellurion-agent:0eaed19`）。本仓 `market.stocks` 已有 `list_date` / `delist_date`，具备实现条件。
- **③ 要求**：若重开——① 提供粗粒度只读端点（一次覆盖整批 symbol / 整个区间，不做逐 symbol 远程）；② 明确 `NULL` 语义（未知 ≠ 一个看起来合理的日期）与 PIT 口径；③ 一条契约测试 + 与 agent 的对拍。
- **④ 约束与非目标**：不为「补端点」而造无消费者的数据；不改 `market.stocks` 的既有列语义。
- **⑤ 验收**：agent 的偏差维能据真实窗口报 `PoolSourcePointInTime`，且 `DelistedInPool` 有回读证据。
- **⑥ 边界**：数据形状与读端口归本仓；消费口径归 agent。
- **重开条件**：下游出现 point-in-time 上市/退市窗口的真实需求（回测票池按日在市、退市偏差维），届时由本仓提供；`market.stocks.delist_date` 已存在。

### DATA-TRANSPORT-MULTI · 传输多门（除 Tushare 外的门均未真接线）[P2] ⏸

- **① 目标**：把「多门传输」从代码在仓里、实际只有一门在跑的状态交代清楚，并给每门一个可判定的重开条件。
- **② 上下文**：真装配且真取数的只有 Tushare（`TUSHARE_TOKEN`）；东财客户端代码在仓内但**未接线**；Juchao / Xueqiu / Yahoo 自带默认端点、connector 恒启用但无验收；Alpha Vantage 需 key；Mootdx 无实现（见 `DATA-MOOTDX`）。多门路由策略尚未定。
- **③ 要求**：重开某门时——① 接入装配与健康检查；② 与 Tushare 同形状的 fail-closed 与数据质量闸；③ 一门一条契约测试 + 一次真取数；④ 写清多门路由/优先级/去重口径。
- **④ 约束与非目标**：不为「门多」而接门；不给未验证的 connector 发「已支持」表述。
- **⑤ 验收**：每门有独立可复现的真取数记录与失败语义。
- **⑥ 边界**：传输实现与装配；数据源授权与凭据归运维。
- **重开条件**：Tushare 之外出现明确的第二数据源需求（容灾 / 字段互补 / 额度）。

### DATA-MOOTDX · Mootdx 传输 [P2] ⏸

- **① 目标**：补齐 TDX 传输门。
- **② 上下文**：`Transports.Mootdx` 是空接口，需 TDX TCP 协议实现与可用数据源；当前仅 Tushare 一门真装配。
- **③ 要求**：实现 TCP 协议适配 + 声明 + 至少一条契约测试；与 Tushare 同形状的 fail-closed。
- **④ 约束与非目标**：不自造协议；不绕过数据源授权。
- **⑤ 验收**：真连一次取数成功 + 断连失败语义。
- **⑥ 边界**：传输实现；多门路由策略另行决定。
- **重开条件**：拿到可用的 TDX 数据源与授权。

### DATA-DIVIDENDS · `dividends` 零写者 [P3] ⏸

- **② 上下文**：表在 schema 内但无 writer（`docs/db-schema.md` §9 零写者清单）。
- **重开条件**：出现分红/送转的消费方（复权因子或回测现金流）。
- **④ 约束**：不为了「填表」而写无消费者的数据。

### DATA-SPLITS · `splits` 零写者 [P3] ⏸

- **② 上下文**：同 `DATA-DIVIDENDS`，零写者。
- **重开条件**：复权因子改为自算、或以拆股事件驱动持仓调整。

### DATA-MARKET-ACTIVITY · `market_activity` 零写者 [P3] ⏸

- **② 上下文**：表在 schema 内但无 writer；原 `pkg/marketdata` 的有库用例依赖它，已由 `tellurion` 侧形状镜像夹具替代。
- **重开条件**：出现市场宽度/情绪类指标需求。

## 二、跨仓指针（不在本仓，勿双写）

| 事项 | 去哪 |
|---|---|
| core 侧旧数据面退役（`pkg/data` / `cmd/data` 删除） | `tellurion:docs/TASKS.md` |
| X1d 生产物理搬迁 + `visible_at` 回填（运维动作） | `tellurion:docs/TASKS.md` → `OPS-X1D-PROD-MIGRATION` |
| 6 个 `market` 别名（零写者表引用的形状） | `tellurion-contracts:docs/TASKS.md` → `CT-06-ALIAS-EXIT`。**本仓已零引用**（2026-11-11：根别名 `Fundamental` → `market.Fundamental`，其余 5 个别名 0 命中；根 `domain` 仅剩非别名类型 `MacroSeries` / `MarketActivity`；pin `v0.9.0`，`build/vet/test` 全绿）⇒ contracts 侧可删别名 |

## 维护本文件

- **新增单**：写全六字段；六字段里必须显式引用**冻结契约的路径与节号**（契约仓的包路径、设计文档的节号），不许写"参考相关实现"。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
