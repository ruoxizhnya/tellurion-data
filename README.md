# tellurion-data

> 5 分钟了解本服务：[SERVICE.md](SERVICE.md)

> module: `tellurion-data` ｜ 状态：**X1c / X1d 已落地（2026-11-10）** —— `internal/ingest` / `internal/normalize` / `internal/serving` / `internal/store` / `internal/schema` / `internal/transport/tushare` / `internal/sync` 已落地；HTTP 面（**23 路由** + 鉴权闸：`GET /health`、`GET /readyz`、`GET /datasets/resolve`、`GET /datasets/{hash}`、`POST /ingest/points`，**7 条 G3 只读兼容端点** `GET /ohlcv/{symbol}`、`GET /fundamental/{symbol}`、`GET /stocks`、`GET /index/{code}/constituents`、`GET /api/v1/trading/calendar`、`GET /api/v1/stocks/{symbol}`、`POST /api/v1/ohlcv/bulk`，以及 **11 条作业/排程端点** `/api/sync/jobs*`、`/api/sync/schedules*`）与取数存储 port 已接线，REV-17 策略已移植（路由真相源见 `docs/api-schema.md` §1，收口记录见 `docs/archive/tasks-archive-2026-11.md`）

数据服务：统一数据协议 + connector / aggregator + `dataset_snapshot_hash`。**只走高频/低频路径，逐 bar / 逐笔永不远程**。

## 目录

| 路径 | 内容 |
|---|---|
| `docs/` | 本仓文档：台账 / 边界 / 决策 / 归档 —— 从这里进：`docs/README.md` |
| `AGENTS.md` | 本仓入口（构建、边界、禁止、依赖版本） |
| `internal/ingest` | connector 注册门 + 12 connector 取数/归一化（声明真源 = 各自 `Declarations()`） |
| `internal/normalize` | 频率聚合（1m → 15m/1d）+ PIT 闸门 |
| `internal/serving` | aggregator（票池解析 · 取数 · 聚合 · PIT · hash · 缓存） |
| `internal/store` | 四个取数 port + 写面 `BulkInsert` / `ReferenceWriter` |
| `internal/schema` | `market` / `ingest` 的 DDL 归属（22 表）+ `visible_at` 三集合 |
| `internal/transport/tushare` | Tushare 传输（ADR-0007） |
| `internal/sync` | 作业队列 + 排程 + Worker + Scheduler |
| `internal/httpapi` | HTTP 面（23 路由；全端点鉴权闸） |
| `cmd/dataservice` | 进程入口（默认 `:8081`；无 secret 且非回环 → 拒绝启动） |
| `doc.go` | 根包文档占位（无实现） |

## 快速开始

```bash
go build ./...
go vet ./...
go test ./...
```

本仓**单独 checkout 就能跑通**上面三条。需要别的仓源码才过 → 边界错了，不是加 `replace` 能解决的问题。

### 容器方式（单仓可起）

```bash
docker compose -f deploy/compose.yaml config -q
docker compose -f deploy/compose.yaml up --build -d
curl -fsS http://127.0.0.1:8081/health
docker compose -f deploy/compose.yaml down
```

- `deploy/compose.yaml` 自带 `postgres`（`pg_isready` 健康检查）；`data` 用 `depends_on.condition=service_healthy` 等它就绪后再起。
- 宿主端口 `8081`；`DATABASE_URL` / `DATA_MARKET_SCHEMA=market` / `JWT_SECRET` 已在 compose 内配好。连外部库时用 `DATA_DATABASE_URL=...` 覆盖。
- 镜像多阶段构建（`golang:1.25-alpine` → `alpine:3.19`），以 `USER nobody` 运行，`HEALTHCHECK` 打 `/health`。
- 取数传输（ADR-0008）：`TUSHARE_TOKEN` 有值才装 Tushare（点位作业 + `stocks` / `trading_calendar` 参考数据作业）；东财客户端无凭证恒装配；`ALPHA_VANTAGE_KEY` 有值才启用 Alpha Vantage。无 token 时取数作业在作业行上响亮失败（不会静默写 0 行）。

> 起服务：默认 `:8081`（全网卡）且无 `JWT_SECRET` 会被启动闸**拒绝**（不允许无鉴权数据面暴露）。
> 本地开发用 `-addr 127.0.0.1:8081`；容器形态必须显式 `AUTH_INSECURE_EXPOSURE=loopback-published` 或配 `JWT_SECRET`。

## 边界

**可以**：统一数据协议与 connector / aggregator；`dataset_snapshot_hash` 的计算与登记；数据质量与缺口报告

**不可以**：逐 bar / 逐笔的远程取数（硬否决：延迟预算不允许）；import core 的实现包；直连 core 的业务表

## 文档

- 文档导航：`docs/README.md`
- 本仓台账：`docs/TASKS.md`（唯一；跨仓任务只留指针）
- 边界与依赖方向：`docs/ARCHITECTURE.md`
- 决策记录：`docs/adr/`

## 许可

MIT（见 `LICENSE`）。
