---
service-card: 1
repo: tellurion-data
kind: service
lifecycle: production
owner: ruoxizhnya
impact: degrading

requires:
  - tellurion-contracts
  - tellurion-kit
required-by: []
calls: []
called-by:
  - tellurion
  - tellurion-agent

truth:
  readme: README.md
  agents: AGENTS.md
  architecture: docs/ARCHITECTURE.md
  api: docs/api-schema.md
  db: docs/db-schema.md
  dataflow: docs/dataflow.md
  runbook: docs/runbook.md
  tasks: docs/TASKS.md

status: evergreen
last-verified: 2026-11-11
verified-by: 2026-11-11 实读 README / AGENTS / ARCHITECTURE / api-schema / db-schema / dataflow / runbook / go.mod / deploy/compose.yaml；端口、端点数量、表数量均未写入
---

# 服务卡：tellurion-data

> **一句话**：A 股数据面的唯一入口——取数、归一、可见时点闸门、落库与快照哈希；core 与 agent 只经 HTTP 访问它。
>
> `service` · `production` · `degrading` · `ruoxizhnya`

## 1. 这是什么

它是行情、参考数据与基本面数据的**唯一写者**：把外部数据源取回的原始数据归一成契约形状，过 `visible_at` 整批门与去重 / 内容比对，再落到 `market` / `ingest` 两个 schema；对外提供按数据集解析、按 hash 取数、只读兼容读面、作业 / 排程管理，以及写面接缝。

**它拥有**：`market` / `ingest` 两个 schema 的 DDL 与写权（例外：`market.index_daily` 的 DDL 与写权在 core，本仓不认领）；`contracts/dataprotocol` 的跨仓形状由 contracts 定义，本仓只实现。
**它不拥有**：因子定义与因子读面、回测结果、策略资产、风险规则；这些各有 owner。

## 2. 它不做什么

- 不提供「替调用方取数」的写入口：`POST /ingest/points` 只收**已归一**的点位。
- 不提供逐 bar / 逐笔取数接口。
- 不接受调用方投送 `visible_at` / `ingest_time`：可见时刻只由该 data_type 的声明锚点推导。
- 不提供无界批量：单次投点有明确上限，超限整批拒收。
- 不提供因子计算，也不提供因子读面；因子定义与值由 core 提供。

## 3. 与谁相连

### 调用它（入边）

| 来源 | 协议 | 用来做什么 | 本仓挂了会怎样 |
|---|---|---|---|
| `tellurion` | HTTP | 数据集解析 / 按 hash 取数 / 只读兼容面 / 作业与排程管理 | core 的行情读与同步路径降级或 503；core 进程本身不受影响 |
| `tellurion-agent` | HTTP | AI 取数工具与交易日历 | AI 的数据类回答降级；核心交易链路不受影响 |

### 它调用（出边）

| 目标 | 协议 | 用来做什么 | 目标挂了会怎样 |
|---|---|---|---|
| PostgreSQL | PG | `market` / `ingest` 的 DDL、读与写 | `/readyz` 503；数据读端点 503；进程不退出、不 panic |
| Tushare | HTTP | 日线 / 分钟线 / 基本面 / 公司行为等主数据源 | 未配置 token 时作业以 `misconfigured` 响亮失败，不静默写 0 行 |
| Eastmoney | HTTP | 无凭证、始终装配的取数腿 | 该腿失败时由作业层报错；不会伪装成成功 |
| Alpha Vantage | HTTP | 有 key 才装配的补充数据源 | 未装配时该源不可用；作业按可用适配器报错 |

> 不写 URL、端口、超时。外部系统的地址与凭证在 `README.md` / `deploy/compose.yaml` / 进程环境变量；接口形状见 `docs/api-schema.md`。
> 本仓**不调用任何内部服务仓**：入边是 core / agent，出边只有 PG 与外部数据源。

## 4. 运行时身份

- **常驻进程**：`cmd/dataservice`；无额外 worker 进程（作业 worker 与 scheduler 在进程内）。
- **存活/就绪语义**：
  - `GET /healthz`：进程存活，**不读库**；库挂了也返回 200。唯一的存活探针名（过渡别名 `/health` 已于 2026-11-12 摘除，现为 404）。
  - `GET /readyz`：PG 池可连 **且** 本仓 DDL 已执行；未装配 / PG 不可达 / DDL 未执行均为 503。
  - 探针公开且豁免限流；路径是稳定身份，不是接口清单。
- **部署真相**：`deploy/compose.yaml`、`Dockerfile`、`README.md` 快速开始（启动参数与环境变量）。
- **端口**：不在此卡；见上述部署真相。

## 5. 数据与契约归属

| 维度 | 归属 | 真相源 |
|---|---|---|
| DB schema | `market` / `ingest` 的 DDL 与写权；`market.index_daily` 例外归 core | `docs/db-schema.md` |
| 跨仓 DTO / 契约 | 形状由 `tellurion-contracts/dataprotocol` 定义；本仓实现与落库 | `docs/api-schema.md`、contracts `docs/API.md` |
| 对外 HTTP | 数据集读面、只读兼容面、作业 / 排程、写面接缝 | `docs/api-schema.md` |

> 只写「拥有哪块」，不写表名清单、字段、端点数量。表级例外与撤认领记录见 `docs/db-schema.md` §1.1 / §7。
> **未裁事项**：`factor_returns` / `ic_analysis` 的归属仍未裁决，本仓不认领、不迁入、不新增写者，见 `tellurion-docs:docs/design/ddd-boundary-review-2026-11-11.md` C3/D1。

## 6. 坏了怎么办

- **影响面**：core 的行情读路径与同步作业降级 / 503；已落库数据仍可被 core 直读的部分不受影响。数据面 fail-closed，不回落成「空数据成功」。
- **先确认（按顺序）**：
  1. `GET /healthz` 是否 200；不是则看进程日志与启动参数。
  2. `GET /readyz` 是否 200；503 时看 `store` / `error`，按 runbook §5「503 排查路径」走。
  3. 作业失败看 `GET /api/sync/jobs/{id}` 的 `error_message`；按 runbook §6「投递失败排查路径」走。
- **升级路径**：`owner` → 跨仓争议 → core `docs/TASKS.md`
- **runbook**：`docs/runbook.md`（起停 §1、503 §5、投递失败 §6）。

## 7. 5 分钟后去哪

| 我想…… | 读这个 |
|---|---|
| 知道它承诺什么接口、错误码与鉴权档位 | `docs/api-schema.md` |
| 知道它拥有哪些表、哪些表不认领 | `docs/db-schema.md` |
| 知道数据怎么流、哪里 fail-closed | `docs/dataflow.md` |
| 起停 / 排障 / 故障演练 | `docs/runbook.md` |
| 当前在做什么 | `docs/TASKS.md` |
| 架构与决策 | `docs/ARCHITECTURE.md` / `docs/adr/` |
