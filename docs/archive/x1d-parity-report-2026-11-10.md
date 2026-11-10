# X1d 切读 / 停写对拍报告（2026-11-10）

> **性质**：历史证据归档，不是当前口径文档。当前口径见 `docs/TASKS.md`（SVC-DATA-G3 / QT-40）与 `docs/db-schema.md` §8/§9。
> **来源**：`docs/TASKS.md` 的「G3 真库端到端 + 对拍取证（2026-11-10）」记录；其中 core 侧写点归零部分于 **2026-11-10 本轮再次用真实命令复核**（见 §1）。
> **本轮限制（如实）**：`postgresql-x64-16` 服务 Stopped（`pg_hba.conf` 为 UTF-8 BOM，恢复属运维动作且禁止 agent 改写），Docker daemon 不可达 ⇒ 真库端到端**未重跑**，§2 数字沿用同日已测记录。

## 1. core 侧写点归零（2026-11-10 本轮实测）

命令（core 仓，非测试文件）：

```
git grep -nE '(INSERT INTO|UPDATE|DELETE FROM|TRUNCATE)[[:space:]]+(market|ingest)\.' -- '*.go' ':!*_test.go'
```

结果：唯一命中 `pkg/storage/index_daily.go:31`，即裁决 ⑰ 显式保留给 core 的 `market.index_daily`（不属本仓 22 张表）。

目录退役：

```
Test-Path cmd/data  -> False
Test-Path pkg/data  -> False
```

补充实测：`git grep -n "ingest.raw|ingest_raw" -- '*.go' ':!*_test.go'` 只剩注释命中，`pkg/storage/ingest_raw.go` 不存在 ⇒ core 对 `ingest.raw` 零写点。

## 2. 新旧读面行数 / 校验对拍（2026-11-10 真库端到端记录）

环境：本机 PostgreSQL 16.15，测试库 `tellurion_data_test`，`DATA_MARKET_SCHEMA=market`，`cmd/dataservice` 监听回环端口。旧路径（`cmd/data`）已删除，「新读面 vs 同库直读 SQL」为等价对拍。

| 数据集 | 行数（HTTP / SQL） | 校验 |
|---|---|---|
| `stocks` | 2 / 2 | 排序后 `symbol|name|exchange|industry|status` 拼接 md5 = `40f25781e835b1ff1e2d0e8d6a729fe5`，两侧一致 |
| `ohlcv_daily_qfq` | 2 / 2 | `sum(close)=21.2`、`sum(volume)=3469134`，两侧一致 |
| `index_constituents` | 2 / 2 | 半开区间 `[in_date, out_date)` 内条数一致 |
| `trading_calendar` | 5 / 5 | `2026-11-02~2026-11-08` 两侧均过滤掉周末两条 |
| `stock_fundamentals` | PIT 取值 | `pe=10.5`、`available_date=2026-10-30`；未来期 `2026-12-25` 被 PIT 正确排除，与 SQL 一致 |

7 条只读兼容端点回执：`/ohlcv/{symbol}`、`/fundamental/{symbol}`、`/stocks`（含 `?exchange=SZSE`）、`/index/{code}/constituents`、`/api/v1/trading/calendar`、`/api/v1/stocks/{symbol}`（未知票 404）、`POST /api/v1/ohlcv/bulk`（逐票错误不进整体失败）全部符合预期。

## 3. 未执行（如实）

- X1d 生产物理 `SET SCHEMA`、`visible_at` 生产回填：属运维动作，且本机 `pg_hba.conf` 不得由 agent 改写 ⇒ 未执行。
- 本轮真库重跑：PG 服务 Stopped、Docker 不可达 ⇒ 未执行；不以上述历史数字冒充新证据。
