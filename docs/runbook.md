# Runbook — tellurion-data

> 对象：单进程、单人自托管的 `cmd/dataservice`（Windows PowerShell 5 已验证）。目标不是「全套
> SRE」，而是**故障时能在三分钟内判断该重启、该修库、还是该改调用方**。
> 可观测面只有一行访问日志（`docs/api-schema.md` §1.2），**没有 Prometheus / 面板**。
>
> 本文中**能跑的命令都实际跑过并贴了真实输出**；必须真 PG / Docker 才能验证的腿，2026-11-07 晚已用便携
> PostgreSQL 16.2 补跑（§3/§4/§5/§7.3）；优雅停机 2026-11-12 已用探针容器 `docker stop`（`SIGTERM`）
> 补跑（§7.4），剩余 Docker 容器腿在对应小节与 §8 明写「未执行」。没有把未跑的输出写成已跑。

> 本文命令里的端口以你实际 `-addr` 为准；G4 演练用了 `18081`（拔 PG）/ `18082`（基线）/
> `18083`（§6 的 curl 复核），避免和日常 `8081` 冲突。

## 0. 命令前缀（每段都先执行这一行）

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
```

## 1. 起停

### 1.1 有真 PG（正常形态；2026-11-07 晚已用便携 PG 16.2 @ 55432 真跑，见 §7.3）

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$env:DATABASE_URL = 'postgres://postgres@127.0.0.1:55432/tellurion_data_test?sslmode=disable'
$env:DATA_MARKET_SCHEMA = 'market'
go run ./cmd/dataservice -addr 127.0.0.1:8081
```

- 鉴权三态（2026-11-12）：有 `JWT_SECRET` ⇒ 全闸；无 secret + **回环绑定** + 未声明 ⇒ 进程可起，**读面公开、写面一律 503 `auth_unavailable`**；无 secret + **非回环绑定** + 未声明 ⇒ **拒绝启动**（错误点名「缺 JWT_SECRET 且未声明，绑定地址不是回环」）；要本地开放写面，显式 `AUTH_INSECURE_EXPOSURE=loopback-published`（任意绑定放开，启动 WARN 同时说出写面无鉴权与**实际**绑定地址）。
- `AUTH_INSECURE_EXPOSURE` 只接受逐字 `loopback-published`；拼错 / 未知值 ⇒ **拒绝启动**（不静默落到默认态）。
- 启动时会重放本仓 DDL（幂等）；PG 连不上**不再退出**，见 §1.2 与 §5。

第 3 态真输出（2026-11-12，`AUTH_INSECURE_EXPOSURE=loopback-published`，`-addr 0.0.0.0:18092`）：

```
data-service: WARNING 写面无鉴权：write endpoints accept unauthenticated requests; 实际绑定 bind address="0.0.0.0:18092"; 声明 AUTH_INSECURE_EXPOSURE=loopback-published (声明值，不等于实际绑定)；绑定非回环，已对外暴露 (non-loopback bind)
```

WARN 里的 `bind address` 是**实际**监听地址，与声明值分开写；绑 `127.0.0.1` 时不带「已对外暴露」，绑 `0.0.0.0` / 空主机（`:8081`）时必带。

### 1.2 无库 / PG 不可达（降级形态，数据面 503）

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
Remove-Item Env:DATABASE_URL -ErrorAction SilentlyContinue
go run ./cmd/dataservice -addr 127.0.0.1:18082
```

真输出（2026-11-07 本机，`DATABASE_URL` 为空）：

```
data-service listening on 127.0.0.1:18082 (data ports unwired: /datasets/* -> 503; write面 unwired: POST /ingest/points -> 503, auth=false)
```

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:18082/healthz
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:18082/readyz
```

真输出：`200`、`503`。

> **`/health` 过渡弃用别名已于 2026-11-12 摘除**（X-PROBE-NAMING 收尾；core 消费者清零 + core e2e 复验通过后单独 commit）。现在请求 `GET /health` 返回 **404**，排障一律用 `/healthz`。若某探针/脚本仍在打旧名，那是它自己的缺陷——把调用点补到 `/healthz`，**不要**加回别名。

### 1.3 后台起 / 停（演练与日常排障用）

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
go build -o "$env:TEMP\dataservice.exe" ./cmd/dataservice
$p = Start-Process -FilePath "$env:TEMP\dataservice.exe" -ArgumentList '-addr','127.0.0.1:8081' `
  -RedirectStandardOutput "$env:TEMP\dataservice.out.log" -RedirectStandardError "$env:TEMP\dataservice.err.log" -PassThru
$p.Id | Set-Content "$env:TEMP\dataservice.pid"
```

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
Stop-Process -Id (Get-Content "$env:TEMP\dataservice.pid") -Force
```

> `Stop-Process` 是强制终止，**Windows 不返回进程退出码**（演练里该字段为空，如实记录）。
> 本进程已实现优雅停机（2026-11-12，`DATA-GRACEFUL-SHUTDOWN`）：收到 `SIGTERM`/`SIGINT`
> 后先 `Shutdown` 排空在途 HTTP 请求（30s 预算），再取消作业 ctx、把在途同步作业
> `ReleaseJob` 交还（预算未用尽回 `pending`，用尽判 `failed`），最多等 15s 关池。
> Windows 原生进程没有 `SIGTERM` 语义 —— 等价路径用容器 `docker stop`（默认发
> `SIGTERM`）或 Linux 上的 `kill -TERM`；本机无容器时用日志里的
> `shutdown signal received; draining in-flight HTTP requests` 与 `released ... on shutdown`
> 两行确认走的是优雅路径。

### 1.4 容器（未执行）

```powershell
docker compose -f deploy/compose.yaml up -d --build data
```

**未执行**：本机 Docker 引擎不可用（`docker info` 报
`open //./pipe/dockerDesktopLinuxEngine: The system cannot find the file specified`），也没有 `psql`。

## 2. 看日志、算错误率与延迟

服务日志写 stderr：

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
Get-Content "$env:TEMP\dataservice.err.log" -Tail 50 -Wait
```

访问日志固定形态（每请求一行）：

```
httpapi: route="GET /datasets/resolve" status=200 dur_ms=3.412318
```

**每端点错误率 + 延迟聚合**（一条命令；`route` 是注册模式，不含 hash）：

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$log = "$env:TEMP\dataservice-g4-drill\down.err.log"
$rows = Get-Content $log | Select-String 'httpapi: route="(.+?)" status=(\d+) dur_ms=([\d.]+)' | ForEach-Object { $m=$_.Matches[0]; [pscustomobject]@{route=$m.Groups[1].Value; status=[int]$m.Groups[2].Value; ms=[double]$m.Groups[3].Value} }
$rows | Group-Object route | ForEach-Object { $g=@($_.Group); $err=@($g | Where-Object {$_.status -ge 500}).Count; $avg=($g | Measure-Object ms -Average).Average; $max=($g | Measure-Object ms -Maximum).Maximum; "{0} n={1} 5xx={2} avg_ms={3:N6} max_ms={4:N6}" -f $_.Name,$g.Count,$err,$avg,$max }
```

真输出（对 §7 演练日志）：

```
GET /healthz n=2 5xx=0 avg_ms=0.000000 max_ms=0.000000
GET /readyz n=1 5xx=1 avg_ms=0.639000 max_ms=0.639000
GET /datasets/resolve n=1 5xx=1 avg_ms=0.000000 max_ms=0.000000
POST /ingest/points n=1 5xx=1 avg_ms=0.000000 max_ms=0.000000
```

> **`0.000000` 的读法**：不是「零耗时」，是**低于本机单调时钟分辨率**。本机实测最小非零间隔
> 约 0.5ms（一次采样 0.5217ms / 108585 次空转），所以 <0.5ms 的 handler 会被记成 0。
> 这只影响「很快」的请求；`/readyz` 的 0.639ms 那段证明计时链路是通的，守卫
> `TestAccessLogDurationMeasuresSlowHandler` 用 2ms handler 钉住量级。
> 需要更细的延迟请换 Linux 宿主或加外部探针——本仓不引指标客户端。
>
> 聚合命令里的 `$log` 换成你要分析的那份 `err.log`。

## 3. 重放迁移

**没有独立的 migrate 命令**：DDL 在每次进程启动时重放（`schema.Statements()`，幂等）。
正常启动即重放，见 §1.1；降级启动失败后每 10s 重试，见 §5。

无库时验证重放腿的形状（会 SKIP，不是通过）：

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
go test ./internal/schema -run 'TestSchemaReplay_TwoRunsLeaveShapeUnchanged' -count=1 -v
```

无库时的真输出（历史记录，证明 skip 不是假绿）：

```
=== RUN   TestSchemaReplay_TwoRunsLeaveShapeUnchanged
    replay_pg_test.go:47: DATA_TEST_DSN 未设置；重放腿只在测试库执行
--- SKIP: TestSchemaReplay_TwoRunsLeaveShapeUnchanged (0.00s)
PASS
ok  	tellurion-data/internal/schema	0.052s
```

对测试库**真重放**（2026-11-07 已执行，便携 PostgreSQL 16.2 @ `127.0.0.1:55432`）：

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$env:DATA_TEST_DSN = 'postgres://postgres@127.0.0.1:55432/tellurion_data_test?sslmode=disable'
go test ./internal/schema -run 'TestSchemaReplay_TwoRunsLeaveShapeUnchanged' -count=1 -v
```

真输出（`REPLAY_EXIT=0`）：

```
=== RUN   TestSchemaReplay_TwoRunsLeaveShapeUnchanged
--- PASS: TestSchemaReplay_TwoRunsLeaveShapeUnchanged (0.45s)
PASS
ok  	tellurion-data/internal/schema	0.499s
```

约束：库名**必须**以 `_test` 结尾，否则套件自动 skip（生产 DSN 误设也连不上去）。

## 4. `visible_at` 回填

只允许对**测试库**跑，生产库会被 DSN 规则拒绝；且由 `data_migrations` 哨兵防重放。
**不要**在服务启动路径里跑它（`cmd/dataservice` 头注释）。

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$env:DATA_TEST_DSN = 'postgres://postgres@127.0.0.1:55432/tellurion_data_test?sslmode=disable'
go test -tags backfill ./internal/schema -run 'TestBackfillSuite_TestDatabaseOnly' -count=1 -v
```

无库时的真输出（SKIP，历史记录）：

```
=== RUN   TestBackfillSuite_TestDatabaseOnly
    backfill_pg_test.go:244: DATA_TEST_DSN not set; skipping visible_at backfill suite (test DB only)
--- SKIP: TestBackfillSuite_TestDatabaseOnly (0.00s)
PASS
ok  	tellurion-data/internal/schema	0.053s
```

**真库回填（2026-11-07 已执行，`BACKFILL_EXIT=0`）**：

```
=== RUN   TestBackfillSuite_TestDatabaseOnly
    backfill_pg_test.go:366: C 集 stock_fundamentals 无本仓 DDL，运行时跳过（静态分类测试覆盖）
--- PASS: TestBackfillSuite_TestDatabaseOnly (0.48s)
PASS
ok  	tellurion-data/internal/schema	0.525s
```

回填口径见 `docs/db-schema.md` §6.5。

## 5. 503 排查路径（按顺序，别跳）

1. **`/healthz` 还是 200 吗？** 不是 ⇒ 进程死了或端口不对，先看 §1.3 的 `err.log` 尾部。
2. **看 `/readyz` 的 body**：

   ```powershell
   $env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
   curl.exe -s http://127.0.0.1:8081/readyz
   ```

   真输出（PG 不可达时）：

   ```
   {"status":"not_ready","store":"postgres","error":"postgres unreachable: failed to connect to `user=postgres database=tellurion`: 127.0.0.1:5999 (127.0.0.1): dial error: dial tcp 127.0.0.1:5999: connectex: No connection could be made because the target machine actively refused it."}
   ```

   `store:"unwired"` ⇒ 启动时 `DATABASE_URL` 为空；`store:"postgres"` + `error` ⇒ 库或 DDL。
3. **确认服务的 `DATABASE_URL`**（注意：看**进程启动时**的环境，不是当前 shell）：

   ```powershell
   $env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
   Select-String -Path "$env:TEMP\dataservice.err.log" -Pattern 'data ports wired to PG|data ports unwired'
   ```
4. **TCP 通不通**：

   ```powershell
   $env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
   "tcp5432_listening=$(Test-NetConnection -ComputerName 127.0.0.1 -Port 5432 -InformationLevel Quiet)"
   ```

   本机真输出：`tcp5432_listening=False`（没有系统 PG）。**2026-11-07 真库演练用的是便携 PG 的 55432**
   （`tcp55432_listening=True`），把上面命令的端口换成实际 DSN 端口即可。
5. **DDL 是否执行**：日志里找这两行之一：

   ```powershell
   $env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
   Select-String -Path "$env:TEMP\dataservice.err.log" -Pattern 'schema not applied yet|schema retry failed|schema applied after retry'
   ```

   - `schema not applied yet ...`：降级启动，10s 后自动重试；
   - `schema retry failed ...`：仍在失败，按 2/4 修库；
   - `schema applied after retry`：已自愈，数据端点应恢复 200。
6. **还是 503 但库是通的**：看访问日志该路由的 `status=503` 频率；确认不是 `available` 门卡在
   false（只会在 DDL 未成功时发生）。`/readyz` 200 而数据端点 503 的形态本轮**没有**观察到，
   若出现按缺陷处理。

## 6. 投递失败排查路径（`POST /ingest/points`）

先按 HTTP 码分流（完整语义表见 `docs/api-schema.md` §2.1）：

| 码 | code | 先做什么 |
|---|---|---|
| 503 | `dependency_unavailable` | 走 §5：库不可达 / DDL 未就绪。**不要**改请求体重试 |
| 409 | `content_conflict` | **停**。同主键已存在且内容不同（含换源）。查库比对后人工修正，重试没用 |
| 413 | `batch_too_large` | 按 ≤10000 点 / ≤8 MiB 分片重投 |
| 429 | `rate_limited` | 按 `Retry-After` 退避；不要并发重打 |
| 401 / 403 | `unauthenticated` / `forbidden` | 修凭证 / 角色（写面只收 `service`、`admin`） |
| 400 | `invalid_batch` / `visible_at_missing` / `anchor_invalid` / `data_type_undeclared` / `data_type_unmapped` / `write_table_not_in_a_set` / `invalid_point` | 修请求或修声明；**不是**库故障 |
| 500 | `internal` | 未登记错误，查 `err.log` 尾部；不要当 4xx 重试 |

复现一条 503（本机真输出）：

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
curl.exe -s -i -X POST -H "Content-Type: application/json" --data-binary '{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"close":1685.5}}]}' http://127.0.0.1:8081/ingest/points
```

真 body：`{"error":{"code":"dependency_unavailable","message":"postgres is not ready (schema not applied or pool unreachable); check GET /readyz"}}`

409 的核对（**HTTP 级 2026-11-07 已执行**；库侧 SQL 本机无 `psql`，未执行）：

```powershell
# 已真跑：同内容两次都 200；只改 close 再投 → 409
$body = '{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"open":1680.0,"high":1690.0,"low":1675.0,"close":1685.5,"volume":123456.0}}]}'
```

真结果（`DATA_MARKET_SCHEMA=market`，真库）：

```
FIRST=200    {"data_type":"ohlcv_daily","received":1,"persisted":1,"skipped":0}
REPLAY=200   {"data_type":"ohlcv_daily","received":1,"persisted":1,"skipped":0}
CONFLICT=409 {"error":{"code":"content_conflict","message":"store: row exists with different content for the same primary key: point[0] 600519.SH ohlcv_daily"}}
```

库侧可选核对（列名以本仓 DDL 为准：`trade_date` / `created_at`，没有 `trade_time` / `updated_at`）：

```sql
SELECT symbol, trade_date, source, close, visible_at, created_at
FROM market.ohlcv_daily_qfq
WHERE symbol = '600519.SH'
ORDER BY trade_date DESC
LIMIT 5;
```

判据：同 `(symbol, trade_date)` 的 `source` 或任一内容列与请求不同 ⇒ 409 是正确拒收；
**不要**用 `UPDATE` 把它改成请求值来「消除 409」，那等于绕过 ADR-0004/0006 的受控修正。

## 6.1 复权基准：什么时候必须手工推进（ADR-0011）

**规则**：qfq 的基准日/基准因子由 `market.adjust_baseline_changes` 的最新一条记录固定，
取数时**不自动推进**（`docs/dataflow.md` §「复权基准与重算」）。自动推进会每跑一次增量作业
就把全部历史价格改一遍，且「同参回测结果随取数窗口变化」正是最难查的那类 bug。

**因此以下情况必须由人显式推进基准**（不要等它自己动）：

1. 数据源的除权除息/复权因子发生**回溯修正**（Tushare 的 `adj_factor` 历史值被重算），
   旧基准已经不代表「以今天为起点」的前复权口径；
2. 需要把某票的基准与全库统一（例如老数据是按各自窗口基准算的，见 2026-11-12 的
   `600000.SH` / `600519.SH` 对齐）；
3. 发现某票的账本基准日停在一个**已确认错误的交易日**（例如误用停牌日建账）。

**推进方式**（正规 job，不要手改表、不要 `UPDATE` 价格）：

```powershell
$body = '{"job_type":"ohlcv_daily","params":{"symbols":["600000.SH"],"start_date":"2024-01-02","end_date":"2024-03-29","extra":{"baseline_date":"2025-12-31","overwrite":true}}}'
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/api/sync/jobs' -ContentType 'application/json' -Body $body
```

- `baseline_date` 必须同时给 `overwrite=true`，否则入队期 400 `invalid_parameter`；
  只允许 `job_type=ohlcv_daily`。
- 账本会 append 一条 `reason=manual`、带 `previous_date` / `previous_factor` 的记录
  （账本中该 symbol 首次记账时 `previous_*` 为 `NULL`，属正常）。
- 同一基准重放**不追加**账本行，价格重写幂等（行数不变、内容不变）。

**核对**：

```sql
-- 当前基准（每 symbol 一条最新记录）
SELECT DISTINCT ON (symbol) symbol, base_date, base_factor, previous_date, previous_factor, reason
FROM market.adjust_baseline_changes ORDER BY symbol, changed_at DESC, id DESC;

-- 全库有效基准是否唯一（账本优先，无账本按 MAX(trade_date) 重建口径）
WITH eff AS (
  SELECT d.symbol,
         COALESCE((SELECT b.base_date FROM market.adjust_baseline_changes b
                   WHERE b.symbol = d.symbol ORDER BY b.changed_at DESC, b.id DESC LIMIT 1),
                  MAX(d.trade_date)) AS baseline
  FROM market.ohlcv_daily_qfq d GROUP BY d.symbol
)
SELECT baseline, COUNT(*) FROM eff GROUP BY baseline;
```

判据：`baseline` 只有一行（或所有行都是有意保留的例外），没有某个 symbol 同时落在两个基准上。

**回退**：用旧基准日再发一次同形状 job（`baseline_date=旧日期` + `overwrite=true`）。
旧基准因子可从账本的 `previous_factor` 取；账本没有记录时，用任一日
`close_new / close_old = base_old / base_new` 反推，别凭记忆填。

## 7. 故障演练记录（G4 ⑤）

- 时间：2026-11-07 05:37（Asia/Shanghai）
- 宿主：Windows 10 / PowerShell 5；go1.25.14 windows/amd64；**无 PG、无 Docker 引擎**
- 手法：`DATABASE_URL` 指向 `127.0.0.1:5999`（无监听端口）启动，模拟「拔 PG」

### 7.1 拔 PG：进程不退出、数据端点 503、不 panic

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
go build -o "$env:TEMP\dataservice-g4.exe" ./cmd/dataservice
New-Item -ItemType Directory -Force "$env:TEMP\dataservice-g4-drill" | Out-Null
$env:DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5999/tellurion?sslmode=disable'
$p = Start-Process -FilePath "$env:TEMP\dataservice-g4.exe" -ArgumentList '-addr','127.0.0.1:18081' `
  -RedirectStandardOutput "$env:TEMP\dataservice-g4-drill\down.out.log" `
  -RedirectStandardError  "$env:TEMP\dataservice-g4-drill\down.err.log" -PassThru
Remove-Item Env:DATABASE_URL
```

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$body = '{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"close":1685.5}}]}'
Set-Content -Path "$env:TEMP\ingest.json" -Value $body -Encoding ASCII
curl.exe -s -o "$env:TEMP\health.body"   -w '%{http_code}' http://127.0.0.1:18081/healthz
curl.exe -s -o "$env:TEMP\readyz.body"   -w '%{http_code}' http://127.0.0.1:18081/readyz
curl.exe -s -o "$env:TEMP\resolve.body"  -w '%{http_code}' 'http://127.0.0.1:18081/datasets/resolve?symbols=600519.SH&from=2026-01-05T00:00:00Z&to=2026-01-09T00:00:00Z&freq=1d&adjustment=qfq&as_of=2026-01-10T00:00:00Z'
curl.exe -s -o "$env:TEMP\ingest.body"   -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data-binary "@$env:TEMP\ingest.json" http://127.0.0.1:18081/ingest/points
```

真结果（来自同一次演练；`curl` 退出码为另记的 `$LASTEXITCODE`）：

```
health http=200 curl_exit=0 body={"status":"ok"}
readyz http=503 curl_exit=0 body={"status":"not_ready","store":"postgres","error":"postgres unreachable: failed to connect to `user=postgres database=tellurion`: 127.0.0.1:5999 (127.0.0.1): dial error: dial tcp 127.0.0.1:5999: connectex: No connection could be made because the target machine actively refused it."}
resolve http=503 curl_exit=0 body={"error":{"code":"dependency_unavailable","message":"postgres is not ready (schema not applied or pool unreachable); check GET /readyz"}}
ingest http=503 curl_exit=0 body={"error":{"code":"dependency_unavailable","message":"postgres is not ready (schema not applied or pool unreachable); check GET /readyz"}}
process_alive=True pid=<启动输出里的 PID>
retry_log_count=1
2026/11/07 05:37:29 data-service: schema retry failed (data endpoints stay 503): schema statement 1 failed: failed to connect to `user=postgres database=tellurion`: 127.0.0.1:5999 (127.0.0.1): dial error: dial tcp 127.0.0.1:5999: connectex: No connection could be made because the target machine actively refused it.
phase=DOWN stopped process_exit_code=[]
panic_lines=0
```

- **不静默成功**：两个数据端点都是 503，没有 200。
- **不 panic**：`panic_lines=0`；进程在 11s 后仍存活。
- **退出码**：4 次 `curl` 均为 `exit=0`，HTTP 码如上；进程由 `Stop-Process` 强杀，
  Windows 不返回进程退出码（`[]`，未取得）。演练命令整体退出码 `0`。
- **自愈证据（进程内，真跑）**：`schema not applied yet` → 10s 后 `schema retry failed`，
  说明重试循环在工作；库真恢复后的翻正**未在本形态执行**（当时本机无 PG），
  已由 §7.3 的真库拔插补齐（200 → 503 → 200，同一进程）。

### 7.2 回指基线配置：启动路径可用

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
Remove-Item Env:DATABASE_URL -ErrorAction SilentlyContinue
# 同 1.3 的 Start-Process，-addr 127.0.0.1:18082
```

真输出：

```
health http=200 curl_exit=0 body={"status":"ok"}
readyz http=503 curl_exit=0 body={"status":"not_ready","store":"unwired","error":"DATABASE_URL is empty: data ports are not wired"}
resolve http=503 curl_exit=0 body={"error":{"code":"dependency_unavailable","message":"data ports are not wired (PG implementations land with X1d)"}}
ingest http=503 curl_exit=0 body={"error":{"code":"dependency_unavailable","message":"write面 is not wired (no PG store or declaration registry)"}}
process_alive=True pid=<启动输出里的 PID>
phase=BASELINE stopped process_exit_code=[]
panic_lines=0
```

### 7.3 真库拔插：200 → 503 → 200（2026-11-07 晚补跑）

- 库：便携 PostgreSQL 16.2 @ `127.0.0.1:55432`，库 `tellurion_data_test`
- 服务：`$env:DATA_MARKET_SCHEMA='market'; $env:DATABASE_URL='postgres://postgres@127.0.0.1:55432/tellurion_data_test?sslmode=disable'; .\data-svc.exe -addr 127.0.0.1:18081`
- 手法：先 `GET /readyz`，再 `pg_ctl stop -m fast` 拔库，再 `pg_ctl start` 恢复（同一进程，不重启服务）

真输出：

```
READY_UP=200   {"status":"ready","store":"postgres"}
HEALTH_UP=200
PG_STOP=0
READY_DOWN=503 {"status":"not_ready","store":"postgres","error":"analysis: ping postgres: write failed: write tcp 127.0.0.1:…->127.0.0.1:55432: wsasend: An existing connection was forcibly closed by the remote host."}
HEALTH_DOWN=200
PG_RESTART=0
READY_BACK=200 {"status":"ready","store":"postgres"}
```

服务日志（同一 PID，逐秒）：

```
08:21:00 httpapi: route="GET /readyz" status=200
08:21:15 … 08:21:23 httpapi: route="GET /readyz" status=503   （拔库区间，同时 /healthz 一直 200）
08:21:24 httpapi: route="GET /readyz" status=200              （库回来后自愈）
```

**结论**：真库断开 ⇒ 就绪 503 且 `health≠ready`；恢复后同一进程自愈 200，无需重启。

**结论**：拔 PG 形态满足「503 / 不 panic / 不静默成功 / 进程存活」，基线配置可正常启动。
HTTP 层自愈另由 `TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal` 覆盖：

```
=== RUN   TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal
... route="GET /datasets/resolve" status=503 ...
... route="GET /datasets/resolve" status=200 ...
--- PASS: TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal (0.04s)
```

### 7.4 SIGTERM 优雅停机（2026-11-12，`DATA-GRACEFUL-SHUTDOWN`）

**形态**：不动正在跑的 `tellurion-data-service-1`，用**独立探针容器**跑本次构建的
Linux 二进制（`CGO_ENABLED=0 GOOS=linux` 构建后只读挂载），接
`tellurion_data_test` 库，`docker stop` 默认发 `SIGTERM`：

```powershell
docker run -d --name data-shutdown-probe --network tellurion_default `
  -e DATABASE_URL='postgres://postgres:postgres@host.docker.internal:5432/tellurion_data_test?sslmode=disable' `
  -e AUTH_INSECURE_EXPOSURE=loopback-published -e JWT_SECRET= `
  -p 127.0.0.1:18081:8081 `
  -v "$env:TEMP\dataservice-linux:/probe/dataservice:ro" `
  --entrypoint /probe/dataservice tellurion/data:latest -addr 0.0.0.0:8081
# 健康检查后：
docker stop -t 20 data-shutdown-probe
```

真输出（健康检查 200、worker/scheduler 已起）：

```
09:11:32 data-service: WARNING 写面无鉴权：… bind address="0.0.0.0:8081"; 声明 AUTH_INSECURE_EXPOSURE=loopback-published …
09:11:32 data-service listening on 0.0.0.0:8081 (data ports wired to PG; write面 wired to PG, auth=false)
09:11:32 syncjob: scheduler started (interval=30s)
09:11:32 syncjob: worker 1678460421d8-1 started (poll=2s)
09:11:36 httpapi: route="GET /healthz" status=200 dur_ms=0.36
09:11:53 data-service: shutdown signal received; draining in-flight HTTP requests
09:11:53 data-service: stopped
09:11:53 syncjob: scheduler stopped
ExitCode=0 OOMKilled=false Error=
```

**结论**：`SIGTERM` 走优雅路径（排空 → 停作业循环 → 退出码 0），不是硬切。
「在途 HTTP 请求被排空而不是断开」由 `TestRunHTTPDrainsInFlightRequests` 钉住；
「在途作业停机交还」由 `TestWorker_ReleasesJobOnShutdown` + PG 腿
`TestPGStoreReleaseJobRequeuesThenFails` 钉住。

**限制（如实记录）**：Windows 原生进程没有 `SIGTERM`，本机只能用 `Stop-Process` 强杀；
容器/`kill -TERM` 才是验收路径。本次探针容器未制造真实在途同步作业
（需要外部 Tushare 配额），作业交还分支以单测 + PG 腿验收。

## 8. 真库 / 容器腿清单

| 腿 | 状态 | 原因 |
|---|---|---|
| DDL 对真库重放两次形状不变 | **已执行（2026-11-07）** | `TestSchemaReplay_TwoRunsLeaveShapeUnchanged` PASS |
| `visible_at` 回填（`_test` 库） | **已执行（2026-11-07）** | `TestBackfillSuite_TestDatabaseOnly` PASS |
| 409 `content_conflict` 真主键复现 | **已执行（2026-11-07）** | 真服务 `POST /ingest/points`：同内容 200、改内容 409 `content_conflict` |
| PG 恢复后同一进程自愈（真库） | **已执行（2026-11-07）** | §7.3：200 → 503 → 200，进程未重启 |
| `docker compose` 起 PG + 服务 | **未执行** | Docker 引擎不可用（不属 G4 ⑤ 验收） |
| 优雅停机 / 进程真实退出码 | **已执行（2026-11-12，探针容器）** | `docker stop`（`SIGTERM`）→ 排空日志 + `stopped`，`ExitCode=0`，见 §7.4；Windows 原生进程无 `SIGTERM`，仍只能强杀 |
