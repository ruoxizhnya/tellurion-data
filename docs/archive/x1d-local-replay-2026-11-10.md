# X1d 本地实例全流程重放（2026-11-10）

> 范围：**只动本机 scratch 实例**。按主任授权清空/重建本地库、不保留历史数据；未连接任何远端或生产库。
> 结论：`SVC-DATA-G3` ⑤ 最后一条（`X1d` 关闭）在本机实例上取得实证；`QT-40` 的 X1d 前置同时解除（其余三项缺口见文末）。

## 1 · 环境

| 项 | 值 |
|---|---|
| PostgreSQL | **16.2**（便携发行包 `%TEMP%\pg16\pg\bin`，非系统服务） |
| 数据目录 | `%TEMP%\pgdata_x1d_replay`（`initdb -A trust` 新建，本机 scratch） |
| 端口 / 认证 | `127.0.0.1:55433` / trust（仅回环） |
| 实例时区 | `Europe/Berlin`（`initdb --locale=C` 的默认；下文 `+01` 与上海时间等价，语句里显式 `Asia/Shanghai`） |
| 业务库 | `tellurion_x1d_replay`；另有 `tellurion_test` 供 tagged 套件 |
| 系统 PG 服务 | `postgresql-x64-16` 仍为 Stopped、`pg_hba.conf` BOM 损坏 —— **本轮未改该文件**，改用便携集群绕开 |

## 2 · 执行步骤（真实命令）

1. **建库 + 跑本仓 DDL**：临时工具执行 `schema.Statements()` → **68 条语句 / 22 张表**（`market` 18 + `ingest` 4）。
2. **构造 core 时代几何**：把 22 张表全部 `ALTER TABLE … SET SCHEMA public`（22/22 成功），再往里灌历史行——A 集 12 张各 1 行、`stocks` 2 行、`trading_calendar` 4 行，其余代表表各 1 行；**全部 `visible_at` 为 NULL**。
   - 说明：core 的旧内联 DDL 已随 `a96d836` 删除，无法逐字重放；这里用**本仓 DDL 反向搬到 `public`**构造旧位置，列形状与目标表逐列一致，`SET SCHEMA` 只关心「同名表 + 数据」。
3. **重建目标空壳**：再跑一遍 `schema.Statements()`（68 条）→ `market` 18 张、`ingest` 4 张空壳；`public` 侧 22 张表与数据原样，`public.*` 上 `visible_at` 列 12 个（A 集）。
4. **执行既有迁移脚本**：`psql -v ON_ERROR_STOP=1 -f migrations/0001_x1d_set_schema.sql` → `exit 0`；每条均为「drop 空壳 → move public.x → market/ingest.x」。
5. **执行对拍脚本**：`psql -v ON_ERROR_STOP=1 -f migrations/0001_x1d_set_schema.verify.sql` → 22 行全 `ok`、`public_state` 列为空；`exit 0`。
6. **执行回填**：`schema.BackfillStatements()`（12 条，`search_path = market, ingest`）→ `exit 0`。
7. **复核 + 幂等**：复跑 verify（22 行计数 NOTICE 全保留）；复跑 up 迁移 → **22 条 skip**、`exit 0`；复跑回填 → 哨兵命中、值不变。
8. **仓库自带 tagged 验收套件**：`DATA_TEST_DSN=…tellurion_test go test -tags backfill ./internal/schema -run TestBackfill -v` → **7 个用例全 PASS**（含 `TestBackfillSuite_TestDatabaseOnly` 0.56s），`ok tellurion-data/internal/schema 0.615s`。

## 3 · 取证

### 3.1 搬迁前后行数（逐表）

| 表 | 搬迁前（public） | 搬迁后 |
|---|---|---|
| `market.announcements` / `capital_flow` / `dividends` / `global_ohlcv` / `hot_search` / `index_constituents` / `limit_up_pool` / `market_activity` / `news` / `ohlcv_daily_qfq` / `ohlcv_minute` / `realtime_quote` / `sectors` / `splits` / `stock_sector_map` / `top_list` | 各 1 | 各 1 |
| `market.stocks` | 2 | 2 |
| `market.trading_calendar` | 4 | 4 |
| `ingest.raw` / `sync_jobs` / `sync_schedules` | 各 1 | 各 1 |
| `ingest.data_migrations` | 0 | 0（回填后 → 12 哨兵） |

- `public` 剩余表数：**0**（22 → 0）。
- `verify.sql`：22/22 `ok`，无 `pending` / 无 `ERROR`，`exit 0`。

### 3.2 `visible_at` 回填

| 检查 | 结果 |
|---|---|
| A 集 12 张合计 `visible_at IS NULL` | **0** |
| `ingest.data_migrations` 哨兵 | **12**（12 条语句各一条） |
| `capital_flow`（trade_date 2026-01-15） | `2026-01-15 08:00:00+01` = 上海 15:00 收盘 |
| `announcements`（ann_time 09:30+08） | `2026-01-15 02:30:00+01`（锚点列） |
| `news`（publish_time 2026-01-14 23:05+08） | `2026-01-14 16:05:00+01`（锚点列） |
| `ohlcv_daily_qfq` / `ohlcv_minute` / `hot_search` | 回落到 `created_at` / `ingest_time`（与 `backfill_pg_test.go` 的档位一致） |
| 二次执行 | 哨兵防重放：`capital_flow.visible_at` 不变，哨兵仍 12 |

### 3.3 tagged 套件（`-tags backfill`）

```
PASS TestBackfill_NeverAssignsVisibleAtFromTradeDateAlone
PASS TestBackfill_IsSentinelGuardedAndReentrant
PASS TestBackfill_AfterCloseUsesExplicitShanghaiClose
PASS TestBackfill_GlobalOHLCVDoesNotUseAShareClose
PASS TestBackfillTestDSN_ProductionDatabaseIsSkippedNotRun
PASS TestBackfillTestDSN_BadDSNFailsLoud
PASS TestBackfillSuite_TestDatabaseOnly   (C 集 stock_fundamentals 无本仓 DDL，运行时跳过)
ok  	tellurion-data/internal/schema	0.615s
```

## 4 · 未覆盖 / 边界

- **未跑容器 / HTTP 服务腿**：本轮走的是 DDL + psql + `BackfillStatements()` 的物理路径，未启动 `cmd/dataservice`、未跑 Docker（daemon 不可达）。
- **数据是合成探针行**（`BF-*` / `000001.SZ` 等），不是历史生产数据的拷贝 —— 与「允许清空、不保留历史」的授权一致。
- **系统 PG 服务未修**：`pg_hba.conf` 的 BOM 损坏仍在（禁改令未变）；本重放用便携集群完成，**不需要**修该文件。
- **`market.index_daily` / `stock_fundamentals`** 不在这 22 张内（前者 core owner ⑰，后者 DDL 在 core），本轮未搬。
- `QT-40` 剩余缺口不在本轮范围：`ingest.raw` 归档面、仅 Tushare 一门传输、7 张零写者表（见 `docs/db-schema.md` §9）。
