# AGENTS — tellurion-data

> 本文件是**本仓**的唯一入口。判据：一个 agent 只 checkout 本仓，就能知道「怎么构建、边界在哪、依赖哪版契约、活在哪、禁止什么」，**不需要读别的仓的源码**。

## 1. 这是什么仓

数据服务：统一数据协议 + connector / aggregator + `dataset_snapshot_hash`。**只走高频/低频路径，逐 bar / 逐笔永不远程**。

**状态**：X1c 落地（2026-11-10，裁决 B）——`internal/ingest`（注册门 + 12 connector 声明 + ETL 编排与 `GatePoints` 闸门）、`internal/normalize`（频率聚合 + PIT 闸门）、`internal/serving`（aggregator + 快照缓存）、`internal/store`（四个取数 port + 写面 `BulkInsert`）、`internal/schema`（`market` / `ingest` 的 DDL 归属 + `visible_at` 三集合）、`internal/assembly`（投影与写侧链的唯一装配层，ADR-0006）、**`internal/transport/tushare`（传输，ADR-0007）**、**`internal/sync`（作业队列 + 排程 + Worker + Scheduler）** 已落地；HTTP 面 **28 条路由**（`/healthz` + `/readyz` + `/datasets/*` + `POST /ingest/points` + 6 条 G3 只读兼容 + **14 条作业/排程/证据端点**，见 `docs/api-schema.md` §1），探针公开、其余过鉴权闸；**同步取数由本进程自跑**（`TUSHARE_TOKEN` 有值时装真 Tushare 传输；东财客户端恒装配；`ALPHA_VANTAGE_KEY` 有值才启用 Alpha Vantage；Mootdx 传输与 `ingest.raw` 归档按 ADR-0008 挂起）。`stocks` / `trading_calendar` 由**参考数据作业**（保留 job_type）走 upsert，旁路点位闸门。

**服务就绪门 G1（2026-11-07）**：`/readyz` = PG 池可连 + 本仓 DDL 已执行（`to_regclass('market.ohlcv_daily_qfq')`），三态见 `docs/api-schema.md` §1.1；业务端点按身份令牌桶限流（50 rps / 突发 200），429 与 401 可区分、探针豁免。真实 PG 断开/恢复腿已于 2026-11-07 晚在便携 PG 16.2 上真跑（200→503→200 同一进程自愈），SVC-DATA-G1 ✅；**Docker 容器腿仍未执行**（不属本门验收）。

**服务就绪门 G2（2026-11-07）**：`market` / `ingest` 的自证补两条腿——① 迁移重放 `internal/schema/replay_pg_test.go`（同一测试库连跑两遍全部 DDL，断言行数 / 列集 sha256 / `pg_indexes` sha256 不变；无 `DATA_TEST_DSN` 自动 skip）；② 跨 schema 写护栏 `internal/repoguard`（AST 扫生产 Go 的 SQL 常量，写语句只许 `market.` / `ingest.` / `pg_catalog` / `information_schema` / 临时表，读上游不拦）。K3 十张表的处置与 `DATA_MARKET_SCHEMA` 口径见 `docs/db-schema.md` §7/§8。真 PG 重放腿已于 2026-11-07 晚补跑（两次指纹一致），2026-11-10 系统实例整库重建后复跑通过，SVC-DATA-G2 ✅。

**服务就绪门 G4（2026-11-07）**：失败语义四类（PG 不可达 → **503 `dependency_unavailable`**、配额耗尽/connector 超时 → 无 HTTP 面、内容冲突 → 409 `content_conflict`）、超时与「本仓不重试、作业侧重试」的边界成文，见 `docs/api-schema.md` §2.1/§3.1；降级启动（`store.Open` 不拨号 + DDL 失败后台 10s 重试 + `WithAvailability` 门）让拔 PG 时端点 503、不 panic、同进程自愈；观测面是单行访问日志 `route/status/dur_ms`，**不接 Prometheus**；起停、聚合、迁移/回填、503 与投递失败排查、演练记录见 `docs/runbook.md`。真 PG 恢复自愈与真库 409 已于 2026-11-07 晚真跑；回填腿 2026-11-10 在本机系统实例执行（`verify.sql` 22/22、幂等复跑通过），SVC-DATA-G4 ✅。**Docker 容器腿与优雅停机仍未执行**。

**G3 只读兼容面（2026-11-10，SVC-DATA-G3；2026-11-12 归属裁决后为 6 条）**：core 的 `pkg/marketdata/http_provider.go` 仍按旧 cmd/data 的旧路由取数，旧服务已退役 ⇒ 本仓补上**只读兼容端点**（`/ohlcv/{symbol}`、`/stocks`、`/index/{code}/constituents`、`/api/v1/trading/calendar`、`/api/v1/stocks/{symbol}`、`POST /api/v1/ohlcv/bulk`），core 零改。它们是**逐表兼容形状**，不带 `as_of` / `dataset_hash` 语义，不参与快照哈希；端口 `serving.LegacyReader`、实现 `internal/store/reference.go`、handler `internal/httpapi/legacy_read.go`。**这推翻了 K1 的字面口径（本仓不补齐旧读面），待主任追认**。**归属裁决（2026-11-12）**：`GET /fundamental/{symbol}` 已从本仓删除——`stock_fundamentals` 的 DDL/写者/读者都在 core，本仓不补空表、不冒充读面；基本面读面是 core 的 `GET /api/fundamental/{symbol}`（空态 404 `fundamental_not_found`，缺表由 core `/readyz` 暴露）。`docs/TASKS.md` 的 SVC-DATA-G3 记 ✅（2026-11-10 本地实例执行 X1d 全流程：22 表搬迁、`visible_at` 回填、verify 22/22、幂等与 tagged 套件全过；同日系统实例 `postgresql-x64-16` @ `127.0.0.1:5432` 修复 `pg_hba.conf` BOM 后整库重建初始化，证据 `docs/archive/system-pg-init-2026-11-10.md`）；QT-40 记 ✅（2026-11-10 终扫，ADR-0008：东财 / Alpha Vantage 接线，`stocks` / `trading_calendar` 补参考数据写者；`ingest.raw` 归档由 ADR-0010 接管，2026-11-12 `index_constituents` 补写者后，仅 `dividends` / `splits` / `market_activity` 三张零写者表 **⏸ 挂起**，各带重开条件）。

**今天的实现**：本仓 `cmd/dataservice`（`DATABASE_URL` 有值 ⇒ 建池 → 执行本仓 DDL → 装出读面 aggregator、写面 `assembly.Writer`、作业队列与 worker/scheduler；为空 ⇒ 数据端点一律 503）+ `internal/httpapi`（上述 26 路由；`auth.go` = kit 适配，`policy.go` = 策略表，`sync.go` = 作业层，`sync_status.go` = 作业层自省与 SSE 进度，`legacy_read.go` = G3 只读兼容面，`limit.go` = 按身份令牌桶，写端点档位只 `service` + `admin`）。`core:cmd/data` 与 `core:pkg/data` **已于 2026-11-08（`a96d836`）整体删除**；取数执行者只有本仓（`TUSHARE_TOKEN` 缺省时作业响亮失败，不静默写 0 行）。`capital_flow` / `top_list` 两条作业的落库仍可经 `POST /ingest/points` 投点。

**鉴权三态（2026-11-12 安全修复）**：① 有 `JWT_SECRET` ⇒ 全闸；② 无 secret + **回环绑定** + 未显式声明 ⇒ 读面公开、**写面一律 503 `auth_unavailable`**（不再「鉴权没启用 = 已通过」）；③ 无 secret + 逐字 `AUTH_INSECURE_EXPOSURE=loopback-published` ⇒ 任意绑定全放行，启动必须 WARN 同时点名写面无鉴权与**实际**绑定地址（非回环时加「已对外暴露」），声明值单独标注；未知声明值 ⇒ 拒绝启动。非回环绑定 + 无声明同样是**拒绝启动**（错误原文点名「缺 JWT_SECRET 且未声明，绑定地址不是回环」），与其余五仓同口径。判定在 `cmd/dataservice.decideAuthPosture`，闸门在 `internal/httpapi/auth.go`（`NewAuthorizer` = 第 1/2 态，`NewOpenAuthorizer` = 第 3 态），`deploy/compose.yaml` 不再内置 `JWT_SECRET` 默认值（其容器绑 `0.0.0.0`，缺 secret 且无声明即拒启，属预期）。

## 2. 构建 / 测试（必须单仓可跑）

```bash
go build ./...
go vet ./...
go test ./...
```

- 这三条是 R1/R2 的判据：**单独 checkout 就能绿**。
- CI 覆盖面按现状（`.github/workflows/ci.yml`，单 job `go`「Build & Test」）依次为：gofmt（有未格式化文件即红）→ `go build ./...` → `go vet ./...` → **Govulncheck 已是门**（`go run golang.org/x/vuln/cmd/govulncheck@latest ./...`，符号级可达性）→ `go test ./... -count=1 -coverprofile=coverage.out` + **覆盖率门**（DATA-CI-GATES）→ **`go test -race ./... -count=1`**（同 job，ubuntu-latest 自带 gcc）→ **Staticcheck**（`go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...`）。跑 `-race` 与 `staticcheck` 是 CI 步骤，**本地上面三条命令不含它们**，本地要自证需另跑同样的两条。
- **CI 的 DB 腿（DATA-CI-GATES，2026-11-13）**：Test job 起 `postgres:16` service，并同时注入 `DATA_TEST_DSN` 与 `DATABASE_URL`（都指向 CI 内临时库 `tellurion_data_test`，**绝不指向部署库**）。迁移入口就是测试自身：`internal/schema.Statements()` 是唯一 DDL owner，各 PG 套件连接时建临时 schema 并应用幂等 DDL；CI 不额外跑外部迁移脚本。效果：库侧 24 条用例由 SKIP 转 PASS（实测带 DSN 364 PASS / 4 SKIP，无 DSN 340 PASS / 28 SKIP）。**保留无 DSN 时按设计 skip**：本地不设 DSN 仍是 28 条 SKIP 且原因可读。
- **覆盖率门（DATA-CI-GATES）**：阈值 **54%**（语句加权，`go tool cover -func` 的 `total`），来源 = 2026-11-13 在本机隔离 PG 16（库 `tellurion_data_test`）上真跑全套件实测 **58.9%** 减 5 点取整（跨仓口径见 `tellurion:docs/TASKS.md` → `X-COVERAGE-FLOOR`）。低于阈值 CI `exit 1`。**这 5 点余量是防漂移的缓冲，不是单文件绊线**：本仓 5,142 条语句，删单个测试文件最多掉 3.4 点（实测 `internal/httpapi/sync_test.go` ⇒ 55.5%），所以真正拦住「DB 腿没跑」的是数值本身——**漏配 DSN 时实测 53.5% < 54%，门直接红**。
- 可选集成测试（无外部条件时自动 skip）：
  - `visible_at` 回填（D2，仅测试库）：`DATA_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_data_test?sslmode=disable' go test -tags backfill ./internal/schema -count=1`
    —— 默认构建不编译；只读 `DATA_TEST_DSN` 且库名必须以 `_test` 结尾（生产 `DATABASE_URL` / 生产库名自动 skip），已设置但连不上则 fail-loud。临时 schema 跑完即删，不碰目标 `market` / `ingest`。
- 若某一步需要别的仓的源码目录才过，那是**边界画错了**——停下来修边界，**不要**加 `replace ../xxx` 绕过。
- 本机同时改多个仓时可用 `go.work` 联调，但**它不入库**（`.gitignore` 已挡）。

## 3. 边界（硬约束）

**允许**

- 统一数据协议与 connector / aggregator
- `dataset_snapshot_hash` 的计算与登记
- 同步任务与调度、数据质量与缺口报告（同步表的真名见 `docs/db-schema.md` §1.5）

**禁止**

- 逐 bar / 逐笔的远程取数（硬否决：延迟预算不允许）
- import core 的实现包
- 直连 core 的业务表

下面三条对所有仓都成立：

- **禁止** `replace` 指向仓外路径（单仓就不可构建了）。
- **禁止**提交 `go.work` / `go.work.sum`（本地产物；已在 `.gitignore`）。
- 新增第三方依赖前，先在第 4 节登记，再改 `go.mod`；没登记就加依赖 = 绕过评审。

## 4. 依赖的契约版本

| 依赖 | 版本 | 说明 |
|---|---|---|
| `github.com/ruoxizhnya/tellurion-contracts` | `v0.10.0`（2026-11-12 按实际 `go.mod` 订正；此前台账写 `v0.9.0`，go.mod 早已是 `v0.10.0`） | 数据协议 / DTO 只认契约；`market.*` 一律走 `domain/market`。6 个 `domain` 根别名（`OHLCV` / `Stock` / `IndexDaily` / `Split` / `Dividend` / `Fundamental`）本仓**零引用**（`Fundamental` 已于 2026-11-11 改 `market.Fundamental`），待 contracts 侧 `CT-06-ALIAS-EXIT` 删除；根 `domain` 仅剩 `MacroSeries` / `MarketActivity`（非别名，不阻塞）。`internal/ingest` 的 `UnifiedDataPoint` 是 `dataprotocol.UnifiedDataPoint` 的别名，本地不再持逐字段镜像 |
| `github.com/ruoxizhnya/tellurion-kit` | `v0.5.0`（2026-11-12 随 `X-DEP-BUMP-CONSUMERS` 从 `v0.2.0` 升；此前 2026-11-10 随 `KT-13` 从 `v0.1.4` 升） | 鉴权机制：`auth`（token 解析）+ `auth/httpadapter`（net/http 适配、启动闸、Policy）。本仓只写策略表，不另造 token 校验。**`v0.1.4` 起 `role=service` 的非数字 subject 不再被 401** ⇒ core 用 `sub=service:core` 调 `POST /ingest/points` 才真正走得通 |
| `github.com/stretchr/testify` | `v1.11.1` | 仅测试用（断言）；与 contracts 仓同版本 |
| `github.com/jackc/pgx/v5` | `v5.11.0` | PG 驱动/连接池（X1d 的四个 port 实现）。2026-11-12 随 kit `v0.5.0` 的 MVS 抬升（消除 GO-2026-5004），`go` directive 同步升到 `1.26.11` |

> 跨仓依赖**只认版本号**（伪版本或 tag），不认源码路径。契约变更走 `tellurion-contracts` 的发布，不走"顺手改一下"。

## 5. 活在哪、文档在哪

- **本仓台账**：`docs/TASKS.md`（唯一）。**没有单号别开工。**
- 文档导航：`docs/README.md`；边界与依赖方向：`docs/ARCHITECTURE.md`；决策：`docs/adr/`。
- 跨仓背景（只读，不在本仓改）：`tellurion:docs/design/qt-decoupling/repo-topology-and-naming.md`、`tellurion:docs/TASKS.md`。

> 跨仓引用一律写 `repo:path` 纯文本，**不做可点击相对链接**——独立 checkout 时 `../../` 必断，R2 会永远红。

## 6. 文档真相源（改功能前先看这里）

本仓的「功能边界」由三份文件钉住，**它们是唯一真相源，不是参考读物**：

| 维度 | 文件 |
|---|---|
| 数据形状（我拥有哪些表、谁能写） | `docs/db-schema.md` |
| 接口形状（对外暴露什么、错误码、幂等） | `docs/api-schema.md` |
| 流向与顺序（数据怎么走、哪里 fail-closed） | `docs/dataflow.md` |

**变更触发是硬约束**（完整表见 `docs/README.md` §3）：改表 → 改 `db-schema.md`；改接口 → 改 `api-schema.md`；改链路 → 改 `dataflow.md`；改分层 → 改 `ARCHITECTURE.md` + 一条 ADR；删档/迁移/换工具 → 一条 ODR。PR 里代码动了而对应文档没动，应被打回。
