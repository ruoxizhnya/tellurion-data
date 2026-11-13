---
status: active
last-verified: 2026-11-13
verified-by: 2026-11-13 五角审查档 4 立单（只立单不施工）——新立 `DATA-CI-GATES`；此前：2026-11-12 `DATA-REFDATA-JOB-WRITE` 收口（主任授权）——D 段作业真跑两次（job `42545d54b42a0523` / `488b9d63617295cb`，各 `fetched=persisted=343`），部署库回读 total 5,923 / `delisted` 343 / `delist_date` 非 NULL 343 / `active` 5,580（跑前跑后一致）/ delisted 无日期 0，重跑总行数不变（幂等）⇒ ✅；约束核对表落 `db-schema.md` §9.8；核对发现的 `trading_calendar` 漂移由启动期受保护回填补齐（ADR-0009 追加「既有库回填路径」，`internal/schema/guard.go` + PG 用例），部署库实测 `ck_trading_calendar_exchange` 在位、`exchange.attnotnull=true`。此前： 2026-11-12 `DATA-REFDATA-JOB-WRITE` 收口批次（主任指令）——退市腿真源 E2E 打通（D 段 343 行、delist_date 写入、在市票无误标、重跑幂等，写面隔离在临时 schema）⇒ 代码/路径层 ✅；约束核对表落 `db-schema.md` §9.8（22 张表，21/21 一致 + 1 处非缺陷漂移）；**部署库灌 D 段未执行**（写操作需授权）⇒ 本单转 🔶、剩余一条运维腿。此前： 2026-11-12 挂起任务处置批次（主任指令）——`DATA-REFDATA-JOB-WRITE` 重开 ⬜ 并收窄（作业级写入已证明：stocks 7 次 / calendar 3 次成功；剩约束核对表 + 退市覆盖口径，delist_date 实测全 NULL）、`DATA-LISTING-WINDOWS` 前提订正（「具备实现条件」作废，前置=退市腿）、`DATA-TRANSPORT-MULTI` 前提订正（东财恒装配已接线，剩 Juchao/Xueqiu/Yahoo 未验收 + 路由未定）；其余四条 ⏸ 维持。此前： 2026-11-12 跨仓伞五腿——`X-LOW-CLEANUP`（登记分页口径不一致：`/api/evidence` 有 `offset`、`/api/sync/jobs` 只有 `limit`，当时只登记不改行为）、`X-DEP-BUMP-CONSUMERS`（kit `v0.2.0 → v0.5.0`、`go` 1.26.11、pgx 随 MVS 到 `v5.11.0`，CI 增 `govulncheck` 门，降版破坏验证复红、还原复绿；同日补 Dockerfile 工具链到 `golang:1.26.11-alpine`）、`X-AGENTS-DEPENDENCY-TABLE`（`AGENTS.md` §4 按实际 `go.mod` 订正 contracts v0.10.0 / kit v0.5.0 / pgx v5.11.0）、`X-CI-RACE-STATICCHECK`（CI 补 `-race` 与 `staticcheck`；存量 2 条按「先判死代码」清理：`yahooToFloat64` 与 `fakeExecutor.seenJob`；`-race` 用 Docker golang:1.26 全量跑通 EXIT=0）、`X-PROBE-NAMING`（`GET /healthz` 为规范名、`/health` 改过渡弃用别名：报文逐字节相同 + `Deprecation` / `Link` / `Warning` 三信号；路由总数按真实注册订正 28→29；真起服务双路径验证 `/healthz` 200 / `/health` 200 同报文 / `/readyz` 503；摘除前置条件与 core 两处消费者〔`tellurion:docker-compose.yml:160`、`tellurion:tools/local-stack.sh:82`，只记录不动〕见 `docs/api-schema.md` §1.1）；**同日收尾**：core 消费者清零 + e2e 转绿后摘除别名（路由 29→28，`/health` 现为 404，守卫 `TestHealthAliasIsRemoved`；e2e 复验在 core 侧）；同日新立并当日收口 `DATA-PAGINATION-ALIGN`（`/api/sync/jobs` 补 `offset` + `limit`/`offset` 回显，坏值一律 400 分表，`ORDER BY created_at DESC, id DESC` 稳定翻页；真 PG 分页不重不漏实证，见归档）；此前 2026-11-12 收口 `DATA-GRACEFUL-SHUTDOWN`（优雅停机 + 作业租约/心跳/stale 回收，见归档）并把 `DATA-INGEST-RAW` / `DATA-INDEX-CONSTITUENTS` 两条已完成项（外加作废、不计完成的 `DATA-FUNDAMENTALS-READ-PUBLIC`）移入归档，本表只留未做/挂起 —— `DATA-REFDATA-JOB-WRITE`（等真实 `TUSHARE_TOKEN` 部署）、`DATA-LISTING-WINDOWS`、`DATA-TRANSPORT-MULTI`、`DATA-MOOTDX`、`DATA-DIVIDENDS`、`DATA-SPLITS`、`DATA-MARKET-ACTIVITY` 均为 ⏸。此前 2026-11-11 W7 台账立单（A5 授权）——新立 2 条六字段单，证据坐标引 ODR-069/072（`tellurion-docs:docs/archive/odr/`）；`DATA-FUNDAMENTALS-READ-PUBLIC` 立单复核时实测前提已被主任 2026-11-12 归属裁决〔data `a454726` 删 `/fundamental` 读面 + core `c85abff` 建 `GET /api/fundamental/:symbol`〕取代，已标 ✅ 作废、不施工。此前 2026-11-11 文档对齐审计与迁移 —— ① 镜像名统一 `tellurion/data:${DATA_IMAGE_TAG:-latest}`；② contracts pin `v0.6.0 → v0.9.0`，6 个 `domain` 根别名本仓**零引用**（`internal/ingest/tushare_adapter.go` 的根别名 `Fundamental` 已改 `market.Fundamental`），`CT-06-ALIAS-EXIT` 本仓侧完成（`build/vet/test` 全绿）；③ ARCH 的 X0 复核为 ✅（core 已落 `dataset_snapshot_hash`）；④ 8 条剩余项均为 ⏸ 且重开条件仍准确；⑤ 2026-11-11 新登记 `DATA-LISTING-WINDOWS` ⏸（agent 已删除无服务的 listing-windows 客户端腿，见 `tellurion-agent:0eaed19`）。此前：2026-11-10 core 部署面同日修复（compose/e2e/k8s/checker 让 data-service 拿到 `DATABASE_URL` + `DATA_MARKET_SCHEMA`；四条破坏验证真做真还原；Docker daemon 不可达未跑容器腿）等
---

# TASKS — tellurion-data 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整时登记 / 承接的挂起项。此前已收口单的正文与施工记录全部在归档文件里；2026-11-12 收口的单（`DATA-GRACEFUL-SHUTDOWN`、`DATA-INGEST-RAW`、`DATA-INDEX-CONSTITUENTS`、`DATA-FUNDAMENTALS-READ-PUBLIC`（作废，不计完成）、`DATA-PAGINATION-ALIGN`、`DATA-REFDATA-JOB-WRITE`）已移入归档的「2026-11-12 收口」节。

### DATA-LISTING-WINDOWS · point-in-time 上市/退市窗口 [P3] ⏸

- **① 目标**：给下游「取池子时按日在市」提供可判定的上市/退市窗口。
- **② 上下文**：core QT-67 的 `POST /api/v1/listing-windows` 已随 `SVC-CORE-DATA-RETIRE` 退役，今天没有任何服务提供；agent 的偏差维曾调它，但只会 404 后静默退回 `PoolSourceCurrent`，已于 2026-11-11 删除该客户端腿与生产接线（`tellurion-agent:0eaed19`）。本仓 `market.stocks` 已有 `list_date` / `delist_date` 列；**2026-11-12 退市腿收口后 D 段 343 行已入库**（`DATA-REFDATA-JOB-WRITE`），数据条件已满足——仍 ⏸ 的原因是没有消费者（不是没有数据）。
- **③ 要求**：若重开——① 提供粗粒度只读端点（一次覆盖整批 symbol / 整个区间，不做逐 symbol 远程）；② 明确 `NULL` 语义（未知 ≠ 一个看起来合理的日期）与 PIT 口径；③ 一条契约测试 + 与 agent 的对拍。
- **④ 约束与非目标**：不为「补端点」而造无消费者的数据；不改 `market.stocks` 的既有列语义。
- **⑤ 验收**：agent 的偏差维能据真实窗口报 `PoolSourcePointInTime`，且 `DelistedInPool` 有回读证据。
- **⑥ 边界**：数据形状与读端口归本仓；消费口径归 agent。
- **重开条件**：下游出现 point-in-time 上市/退市窗口的真实需求（回测票池按日在市、退市偏差维），届时由本仓提供。数据前置已解除（`market.stocks.delist_date` 已有真实覆盖，2026-11-12 起 343 行）。

### DATA-TRANSPORT-MULTI · 传输多门（Juchao/Xueqiu/Yahoo 未验收，多门路由未定）[P2] ⏸

- **① 目标**：把「多门传输」从代码在仓里、实际只有一门在跑的状态交代清楚，并给每门一个可判定的重开条件。
- **② 上下文（2026-11-12 实测订正）**：已接线的门有三门——Tushare（`TUSHARE_TOKEN` 有值）、东财（**恒装配**，`cmd/dataservice/main.go:325-332`；原「未接线」表述作废）、Alpha Vantage（`ALPHA_VANTAGE_KEY` 有值才启用）。Juchao / Xueqiu / Yahoo 自带默认端点、connector 恒启用但**无真取数验收**；Mootdx 无实现（见 `DATA-MOOTDX`）。多门路由 / 优先级 / 去重口径未定。
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

#### 2026-11-13 五角审查 · 档 4（测试）

### DATA-CI-GATES · CI 起 PG 并设覆盖率门（14 条 DB 用例不再 skip）[P2] ⬜

> 档 4（测试）· 来源：03-testing P1-3 / P2-2 · 顺序 4-8 · 依赖：core 伞 `X-COVERAGE-FLOOR`

- **① 目标**：CI 真跑 DB 腿，并把 14 条按设计 skip 的用例变成真实执行；同时设覆盖率门。
- **② 上下文**：`.github/workflows/ci.yml` 的 Test job 无 `services: postgres`、未注入测试 DSN，库侧用例按设计 skip；本仓有迁移入口，但 CI 未应用（施工第一步先 grep 测试代码确认 DSN 环境变量名与迁移命令，勿猜）。
- **③ 要求**：① 加 `postgres:16` service + `pg_isready`；② 注入本仓测试使用的 DSN；③ 在 Test 前应用本仓迁移（走唯一合法入口，不绕过）；④ 本地无 DSN 时仍按设计 skip（保留原因）；⑤ 覆盖率门 = 本仓最新实测 - 5 点（先测后设）。
- **④ 约束与非目标**：不把 skip 改成假通过；不改业务代码；不在本条修 DB 用例暴露的真实缺陷（另立单）。
- **⑤ 验收**：CI 日志中 DB 用例以 PASS 出现（非 SKIP）且数量 > 0；删除一个有断言的测试文件 ⇒ 覆盖率门红；本地无 DSN ⇒ skip 且原因可读。
- **⑥ 边界**：本仓 CI + 测试夹具；迁移入口若归本仓则复用，不复制 core 的工具。

## 二、跨仓指针（不在本仓，勿双写）

| 事项 | 去哪 |
|---|---|
| core 侧旧数据面退役（`pkg/data` / `cmd/data` 删除） | `tellurion:docs/TASKS.md` |
| X1d 生产物理搬迁 + `visible_at` 回填（运维动作） | `tellurion:docs/TASKS.md` → `OPS-X1D-PROD-MIGRATION`（**已收口 2026-11-12**：部署库 2026-11-10 已整库重建为目标形状，见 core 关闭记录） |
| 6 个 `market` 别名（零写者表引用的形状） | `tellurion-contracts:docs/TASKS.md` → `CT-06-ALIAS-EXIT`。**本仓已零引用**（2026-11-11：根别名 `Fundamental` → `market.Fundamental`，其余 5 个别名 0 命中；根 `domain` 仅剩非别名类型 `MacroSeries` / `MarketActivity`；pin `v0.9.0`，`build/vet/test` 全绿）⇒ contracts 侧可删别名 |

## 维护本文件

- **新增单**：写全六字段；六字段里必须显式引用**冻结契约的路径与节号**（契约仓的包路径、设计文档的节号），不许写"参考相关实现"。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
