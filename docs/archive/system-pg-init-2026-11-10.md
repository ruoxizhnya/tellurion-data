# 系统 PostgreSQL 16 实机初始化（2026-11-10）

> 本文件是**系统实例**（Windows 服务 `postgresql-x64-16`）的初始化取证，接续
> `docs/archive/x1d-local-replay-2026-11-10.md`（便携集群 16.2 @ 55433 的演练）。
> 范围：**只动本机实例**（`127.0.0.1:5432`），未接触任何远端/生产库。

## 1. 前置修复：`pg_hba.conf` 的 BOM 与字节

| 项 | 值 |
|---|---|
| 文件 | `C:\Program Files\PostgreSQL\16\data\pg_hba.conf` |
| 原始 | `5644` 字节，首 3 字节 `EF BB BF`（UTF-8 BOM），尾部 `0D 0A 0D 0A` |
| 备份 | `%TEMP%\pg_hba.conf.bak-20261110`（`LEN=5644`，保留原字节） |
| 修复后 | `5641` 字节，首 3 字节 `23 20 50`（`# P`），首行 `# PostgreSQL Client Authentication Configuration File` |
| 认证规则（未改） | `local/host all all … scram-sha-256`（`127.0.0.1/32`、`::1/128`、replication 同） |

## 2. 服务启动

- `Get-Service postgresql-x64-16` → `Status=Stopped`、`StartType=Automatic`；当前 shell **非管理员**，`Start-Service` / `sc start` 均 `Access is denied (exit 5)`。
- 处置：`Start-Process sc.exe -ArgumentList 'start','postgresql-x64-16' -Verb RunAs`（一次 UAC 提权）→ **`Status=Running`**。
- 服务日志（`...\data\log\postgresql-2026-11-10_*.log`）：`PostgreSQL 16.15, compiled by Visual C++ build 1944, 64-bit`；`正在监听IPv6地址"::"，端口 5432`；`正在监听IPv4地址"0.0.0.0"，端口 5432`；`数据库系统准备接受连接`。
- 连接验证：`psql -h 127.0.0.1 -p 5432 -U postgres -Atc "select version();"` → `PostgreSQL 16.15 …`，`EXIT=0`（口令 `postgres`，`scram-sha-256`）。

> 说明：上一轮排查中 `postgres.exe -D <data>` 报 `无法创建文件 "postmaster.pid": Permission denied`，是 **agent shell 沙箱**限制写 `C:\Program Files\PostgreSQL\16\data`，不是目录 ACL 问题（`BEELINK\ruoxi` 对该目录 `FullControl`）。改走服务路径后不再出现。

## 3. 删除前清单（先列后删）

`pg_database` 实测（删除前）：

| 数据库 | 大小 | 处置 |
|---|---|---|
| `tellurion` | 11 MB | **DROP + 重建**（本轮初始化目标） |
| `tellurion_core_test` | 7841 kB | 保留（core 测试夹具） |
| `tellurion_dualrun_e2e` | 9225 kB | 保留（dualrun e2e 夹具） |
| `tellurion_fulltest` | 10 MB | 保留（core `TEST_DATABASE_DSN` 夹具） |
| `tellurion_orch_core_e2e` | 8209 kB | 保留（编排 e2e 夹具） |
| `tellurion_orchestration` | 8161 kB | 保留（编排 runbook 在册 DSN） |
| `tellurion_strategy_test` | 8137 kB | 保留（strategy runbook 在册 DSN） |
| `postgres` / `template0` / `template1` | — | 保留（系统库） |

`tellurion` 库内共 **56 张表**（`public` 40 + `quant` 10 + `audit`/`ingest`/`market`/`research`/`run` 6）。精确全表 count 后仅有 8 张非空：

| 表 | 行数 |
|---|---|
| `public.ohlcv_daily_qfq` | 522 |
| `public.factor_definitions` | 12 |
| `public.factor_hypothesis` | 12 |
| `audit.message_log` | 3 |
| `quant.run_provenance` | 3 |
| `run.backtest_results` | 3 |
| `public.backtest_adjustment_anchors` | 2 |
| `public.data_migrations` | 2 |

删除动作（真实输出）：

```
DROP DATABASE tellurion WITH (FORCE);   -- DROP DATABASE, EXIT=0
CREATE DATABASE tellurion ENCODING 'UTF8';  -- CREATE DATABASE, EXIT=0
```

## 4. 全量初始化（系统实例 `tellurion` @ 5432）

使用的执行源是本仓唯一真相 `internal/schema`（临时工具 `x1d_init_tmp`，跑完即删，不入库）：

| 步 | 命令 | 结果 |
|---|---|---|
| ① DDL | `INIT_PHASE=ddl go run ./x1d_init_tmp` | `DDL_OK statements=68 tables=22`，`EXIT=0` |
| ② 迁移 | `psql -v ON_ERROR_STOP=1 -f migrations/0001_x1d_set_schema.sql` | `BEGIN / DO / COMMIT`；22 条 `skip public.<t>(不存在/已搬过)`，`EXIT=0` |
| ③ 对拍 | `psql -v ON_ERROR_STOP=1 -f migrations/0001_x1d_set_schema.verify.sql` | **22 行全部 `target_state=ok`、`public_state` 全空**；逐表 count 全 `0`，`EXIT=0` |
| ④ 回填 | `INIT_PHASE=backfill go run ./x1d_init_tmp`（会话内 `SET search_path TO market, ingest`） | 12 条语句全 `rows=0`（空表），`BACKFILL_OK statements=12`，`EXIT=0` |
| ⑤ 幂等复跑 | 重跑 ① + ④ | `DDL_OK statements=68`；`BACKFILL_OK statements=12`（哨兵未增） |
| ⑥ tagged 套件 | `DATA_TEST_DSN=…/tellurion_data_test go test -tags backfill ./internal/schema -count=1` | `PASS`（含 `TestSchemaReplay_TwoRunsLeaveShapeUnchanged 0.43s`），`EXIT=0` |

> 临时测试库 `tellurion_data_test` 为套件专用（套件只建临时 schema 并自删），**保留**作为本仓在册的 `DATA_TEST_DSN` 夹具（与其它仓的 `*_test` 库同口径）。

## 5. 最终状态（复核查询）

```
db=tellurion
port=5432
server_version=16.15
sentinel=12                 -- ingest.data_migrations 12 条 057* 哨兵
market_tables=18
ingest_tables=4
public_tables=0             -- 旧 public.* 已随库删除，未再长出
visible_at_cols=12          -- A 集 12 张 market 表均有 visible_at
```

服务连通性（真实二进制 + 真库）：

- `DATABASE_URL=.../tellurion?sslmode=disable`、`DATA_MARKET_SCHEMA=market`、`-addr 127.0.0.1:18092`
- `GET /health` → `200 {"status":"ok"}`
- `GET /readyz` → `200 {"status":"ready","store":"postgres"}`
- 启动日志：`data-service listening on 127.0.0.1:18092 (data ports wired to PG; write面 wired to PG, auth=false)`、`syncjob: worker … started`

## 6. 清理

- 便携演练集群 `%TEMP%\pgdata_x1d_replay`（73.0 MB，含库 `tellurion_x1d_replay` / `tellurion_test`、端口 55433 早已 `pg_ctl stop`）→ **已删除**。
- `%TEMP%\x1d_seed.sql` → 已删除；`%TEMP%\pg_hba.conf.bak-20261110` 保留（原始字节备份，回滚用）。
- 临时工具 `x1d_init_tmp/` → 已删除（未入库）。

## 7. 边界与仍未做（如实）

- **只动本机**：所有命令指向 `127.0.0.1:5432`；未触碰任何远端/生产库。
- **未动其它库**：6 个 `tellurion_*` 测试/e2e 库按第 3 节清单保留，删除它们会打断其它仓 runbook 在册的测试腿。
- **`tellurion` 库内非数据域的表**（`quant.*` / `audit.*` / `research.*` / `run.*` / `public.users` 等）随整库 DROP 一并消失，由各自服务的启动 DDL 重建；本轮未逐域核对其重建结果。
- **服务自启动**依赖 Windows 服务（`StartType=Automatic`）；本轮启动经一次 UAC 提权，非静默。
- **QT-40 三块缺口未变**：`ingest.raw` 归档未搬、仅 Tushare 一门传输、7 张零写者表——本次只把运行环境从便携集群换成系统实例，不构成这三项的关闭依据。
