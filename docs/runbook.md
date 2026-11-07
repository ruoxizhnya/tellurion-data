# Runbook — tellurion-data

> 对象：单进程、单人自托管的 `cmd/dataservice`（Windows PowerShell 5 已验证）。目标不是「全套
> SRE」，而是**故障时能在三分钟内判断该重启、该修库、还是该改调用方**。
> 可观测面只有一行访问日志（`docs/api-schema.md` §1.2），**没有 Prometheus / 面板**。
>
> 本文中**能跑的命令都实际跑过并贴了真实输出**；必须真 PG / Docker 才能验证的腿（§1.1/§1.4/
> §3 末/§4 末），在对应小节与 §8 明写「未执行」。没有把未跑的输出写成已跑。

> 本文命令里的端口以你实际 `-addr` 为准；G4 演练用了 `18081`（拔 PG）/ `18082`（基线）/
> `18083`（§6 的 curl 复核），避免和日常 `8081` 冲突。

## 0. 命令前缀（每段都先执行这一行）

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
```

## 1. 起停

### 1.1 有真 PG（正常形态，**未执行**：本机无 PG）

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$env:DATABASE_URL = 'postgres://postgres:postgres@127.0.0.1:5432/tellurion?sslmode=disable'
$env:JWT_SECRET  = '<与 core 共享的 secret>'
go run ./cmd/dataservice -addr 127.0.0.1:8081
```

- 无 `JWT_SECRET` + **非回环**绑定 ⇒ 拒绝启动（这是有意的；见 `docs/api-schema.md` 头注）。
- 无 `JWT_SECRET` + 回环绑定 ⇒ 开放访问，日志会打
  `data-service: WARNING authentication disabled (open access); ...`。
- 启动时会重放本仓 DDL（幂等）；PG 连不上**不再退出**，见 §1.2 与 §5。

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
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:18082/health
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:18082/readyz
```

真输出：`200`、`503`。

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
> 服务尚未实现优雅停机；要「零丢请求」重启只能先摘流量（换端口 / 停上游作业）再杀。

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
GET /health n=2 5xx=0 avg_ms=0.000000 max_ms=0.000000
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

真输出：

```
=== RUN   TestSchemaReplay_TwoRunsLeaveShapeUnchanged
    replay_pg_test.go:47: DATA_TEST_DSN 未设置；重放腿只在测试库执行
--- SKIP: TestSchemaReplay_TwoRunsLeaveShapeUnchanged (0.00s)
PASS
ok  	tellurion-data/internal/schema	0.052s
```

对测试库**真重放**（未执行：本机无 PG）：

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$env:DATA_TEST_DSN = 'postgres://postgres:postgres@127.0.0.1:5432/tellurion_data_test?sslmode=disable'
go test ./internal/schema -run 'TestSchemaReplay_TwoRunsLeaveShapeUnchanged' -count=1 -v
```

约束：库名**必须**以 `_test` 结尾，否则套件自动 skip（生产 DSN 误设也连不上去）。

## 4. `visible_at` 回填

只允许对**测试库**跑，生产库会被 DSN 规则拒绝；且由 `data_migrations` 哨兵防重放。
**不要**在服务启动路径里跑它（`cmd/dataservice` 头注释）。

```powershell
$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User'); $env:CGO_ENABLED='0'
$env:DATA_TEST_DSN = 'postgres://postgres:postgres@127.0.0.1:5432/tellurion_data_test?sslmode=disable'
go test -tags backfill ./internal/schema -run 'TestBackfillSuite_TestDatabaseOnly' -count=1 -v
```

本机无库时的真输出（SKIP）：

```
=== RUN   TestBackfillSuite_TestDatabaseOnly
    backfill_pg_test.go:244: DATA_TEST_DSN not set; skipping visible_at backfill suite (test DB only)
--- SKIP: TestBackfillSuite_TestDatabaseOnly (0.00s)
PASS
ok  	tellurion-data/internal/schema	0.053s
```

**未执行**：真实 `_test` 库上的回填（本机无 PG / Docker）。回填口径见 `docs/db-schema.md` §6.5。

## 5. 503 排查路径（按顺序，别跳）

1. **`/health` 还是 200 吗？** 不是 ⇒ 进程死了或端口不对，先看 §1.3 的 `err.log` 尾部。
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

   本机真输出：`tcp5432_listening=False`（没有真 PG）。
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

409 的库侧核对（**未执行**，需真库）：

```sql
SELECT symbol, trade_time, source, visible_at, updated_at
FROM market.ohlcv_daily_qfq
WHERE symbol = '600519.SH'
ORDER BY trade_time DESC
LIMIT 5;
```

判据：同 `(symbol, trade_time)` 的 `source` 或任一内容列与请求不同 ⇒ 409 是正确拒收；
**不要**用 `UPDATE` 把它改成请求值来「消除 409」，那等于绕过 ADR-0004/0006 的受控修正。

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
curl.exe -s -o "$env:TEMP\health.body"   -w '%{http_code}' http://127.0.0.1:18081/health
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
  说明重试循环在工作；库真恢复后的翻正**未执行**（本机无 PG）。

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

**结论**：拔 PG 形态满足「503 / 不 panic / 不静默成功 / 进程存活」，基线配置可正常启动。
**未执行**：真 PG 恢复后同一进程自愈的端到端腿（需要真库）；HTTP 层自愈由
`TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal` 覆盖：

```
=== RUN   TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal
... route="GET /datasets/resolve" status=503 ...
... route="GET /datasets/resolve" status=200 ...
--- PASS: TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal (0.04s)
```

## 8. 未执行项（必须真 PG / 容器）

| 腿 | 状态 | 原因 |
|---|---|---|
| DDL 对真库重放两次形状不变 | **未执行** | 本机无 PG，`DATA_TEST_DSN` 未设置（套件 SKIP） |
| `visible_at` 回填（`_test` 库） | **未执行** | 同上（`-tags backfill` SKIP） |
| 409 `content_conflict` 真主键复现 | **未执行** | 需要真库已有行；只有单测覆盖 |
| PG 恢复后同一进程自愈（真库） | **未执行** | 无真 PG；HTTP 层由单测覆盖，进程内重试日志已观察 |
| `docker compose` 起 PG + 服务 | **未执行** | Docker 引擎不可用 |
| 优雅停机 / 进程真实退出码 | **未执行** | 当前只有 `Stop-Process` 强杀 |
