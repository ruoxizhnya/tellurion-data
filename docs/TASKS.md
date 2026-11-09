---
status: active
last-verified: 2026-11-07
verified-by: SVC-DATA-G1/G2/G4 真库腿补齐（便携 PostgreSQL 16.2 @ 55432：/readyz 200→503→200 同一进程自愈、迁移重放、回填、真库 409）
---

# TASKS — tellurion-data 未完成项

> 本仓**唯一**任务台账。跨仓任务只在 §二 留指针，**不镜像正文**（双写必然漂移）。
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `✅ 完成（附日期）`
> 每张单六字段（① 目标 ② 上下文 ③ 要求 ④ 约束与非目标 ⑤ 验收 ⑥ 边界），判据见 `tellurion:AGENTS.md` §8.5。
> 完成即标 ✅（附日期）并移入 `docs/archive/TASKS-YYYY-MM-DD-archived.md`；本文件只留未完成项。

> **第三轮裁决指针（2026-10-25）**：⑤ 生产 `visible_at` 回填**只授权 9 张 core 无写者的 A 集表**（`ohlcv_daily_qfq` / `sectors` / `stock_sector_map` 等 core 停写后再批；**执行者是运维，不是 agent**）。⑰ `market.index_daily` **owner = core**：本仓须撤认领并删 `internal/schema/market.go:301` 的建表/索引、`TableNames()` 条目、`visible_at.go:54` 的 C 集条目、`docs/db-schema.md:25/151` 的认领，并加守卫（core 与 data 两套 DDL 形状不同，**先建表者定形**）。正文见 `tellurion:docs/design/qt-decoupling/cross-repo-decision-record-2026-10-25.md` §五。
>
> **⑰ 施工前置复核（2026-10-26 实测，仍未落）**：上面四个撤销点今天都还在——`internal/schema/market.go:301`（建表）+ `:317`（索引）、
> `internal/schema/schema.go:47`（`TableNames()` 条目）、`internal/schema/visible_at.go:54`（C 集条目）、`docs/db-schema.md:25`（认领行）。
> ⚠️ **并且有一条守卫与它反向耦合，执行 ⑰ 时必须同批改，不能只改一边**：`internal/schema/schema_test.go:69` 现在是
> `assert.Contains(t, exempt, "market.index_daily")`——它逐字钉住的正是 ⑰ **要撤掉**的那个状态。删掉 C 集条目而不改这条断言 ⇒ 测试当场红；
> 反过来先把断言改成 `NotContains` 而不删条目 ⇒ 等于用测试给未完成的搬迁发通行证。**判据：同一笔提交里同时删 `visible_at.go:54` 的条目、
> 把 `:69` 翻成 `assert.NotContains(...)`，并保留 `:73-78` 的「三集合互不重叠」腿不变**（那条腿在撤认领后才会真的开始起作用）。

---

## 一、本仓任务

> **约定变更（2026-10-21）**：原「占位仓不写正式六字段任务单」作废——X 线任务单已展开为本仓正式任务（见下）。M5/M8 开工前置不变；实现仍未开工，开工前仍须过前置两条。（2026-10-23：实现已开工，见本单下的进度块。）

### SVC-DATA-G0 · 服务就绪门 G0：自有部署与本地启动（ADR-036）[P0] ✅（2026-11-07）

- **① 目标**：本仓作为数据面服务，能在没有 core 源码的情况下独立构建、启动、被健康检查。
- **② 上下文**：ADR-036 定六仓为服务；今天本仓有 `cmd/dataservice` + 23 张 `market.*`/`ingest.*` schema，但无 `Dockerfile` / `deploy/`；生产 `:8081` 仍由 core `cmd/data` 提供。
- **③ 要求**：新增 `Dockerfile`（多阶段构建、非 root、`curl`、`HEALTHCHECK /health`）；新增 `deploy/compose.yaml`（本仓 build context；`DATABASE_URL` / `DATA_MARKET_SCHEMA` / `JWT_SECRET`；端口 `:8081`）；补 `config/` 或 env 说明；README 写单仓启动命令。
- **④ 约束与非目标**：不改业务逻辑与 schema；不接 core 写切读（归 G3）；不引入 K8s；不把 core `cmd/data` 复制进本仓。
- **⑤ 验收**：`docker compose -f deploy/compose.yaml config` 通过；单仓启动后 `GET /health` 200；有 Go 环境时 `go build ./...` 单仓绿；无 `replace` / `go.work`。
- **⑥ 边界**：只改本仓 deployment；core 侧 compose 由 `SVC-DEPLOY-01` 处理。

> **施工记录（2026-11-07，补齐验收并翻 ✅）**：
> - **落点核查**：`Dockerfile`（多阶段 `golang:1.25-alpine` → `alpine:3.19`、`USER nobody`、装 `curl`、`HEALTHCHECK … curl -fsS http://127.0.0.1:8081/health`）、`deploy/compose.yaml`（`name: tellurion-data`；服务 `postgres` + `data`；`DATABASE_URL` / `DATA_MARKET_SCHEMA: market` / `JWT_SECRET` 三键齐；端口 `127.0.0.1:8081:8081`；`depends_on: service_healthy`）、`README.md` 含单仓启动命令。
> - **验收（真实输出，2026-11-07）**：`docker compose -f deploy/compose.yaml config -q` → **EXIT=0**；`go build ./...` → **EXIT=0**；`go.mod` 无 `replace`（0 行）、无 `go.work`；真实二进制 + 真库（便携 PG 16.2 `127.0.0.1:55432`、库 `tellurion_data_test`、`DATA_MARKET_SCHEMA=market`，监听 `127.0.0.1:18092`）→ `GET /health` `200 {"status":"ok"}`、`GET /readyz` `200 {"status":"ready","store":"postgres"}`。
> - **未执行（如实）**：`docker compose up --build`（本机 Docker 引擎未起，`docker version` 客户端 27.4.1-rd / daemon 不可达）；容器内跑通属容器腿，不影响本单 ⑤ 的三条书面判据。

### SVC-DATA-G3 · 切读 / 停写与 core 旧实现退役（ADR-036）[P0] 🔶

- **① 目标**：`market.*` / `ingest.*` 的唯一 writer 变成本仓；core 不再写行情/同步表，旧 `pkg/data` / `cmd/data` 删除。
- **② 上下文**：`X1d`（QT-40）已规划「消灭第二写者」，但今天 core 仍持约 20 张裸表与生产写入；本仓 `POST /ingest/points` 只覆盖 `capital_flow` / `top_list` 两条作业。
- **③ 要求**：先出逐表 writer/reader 清单；按四步（第二写者 → 对拍 → 切读 → 停写）推进；core 读路径改 `data_service.url`；停写后删 core `pkg/data` 写入与 `cmd/data` 同步面；`market.index_daily` 保持 core owner（裁决 ⑰）。
- **④ 约束与非目标**：不在 core 写者未停前做物理 `SET SCHEMA`；不搬 `quant.*` 运行态表；不接逐 bar / 逐笔。
- **⑤ 验收**：core 侧 `market.*` / `ingest.*` 写点 = 0；`X1d` 关闭；core `cmd/data` 删除或只剩只读代理；新旧行数/校验和对拍报告归档。
- **⑥ 边界**：本仓只负责自己的 schema 与 API；core 代码删除由 `SVC-CORE-DATA-RETIRE` 跟踪。

> **施工记录（2026-11-10，G3 读面补齐；`gofmt -l .` 空、`go build/vet/test ./...` 全 0）**
>
> - **裁决（推翻 K1 字面；待主任追认）**：第三轮 K1（2026-11-08 补裁 #11）的口径是「本仓以已登记路由为唯一数据服务面，core 的旧读面由 core 侧逐条反代或删除、本仓不补齐」。实测 core `pkg/marketdata/http_provider.go` 仍按旧 cmd/data 的 **7 条路由**取数（`/ohlcv/{symbol}`、`/fundamental/{symbol}`、`/stocks`、`/index/{code}/constituents`、`/api/v1/trading/calendar`、`/api/v1/stocks/{symbol}`、`POST /api/v1/ohlcv/bulk`），而旧服务已随数据面退役删除 ⇒ 产品读链实际 404。用户裁决（2026-11-10）：**由本仓补齐这 7 条只读兼容端点，core 零改**。这推翻 K1 的字面口径（不是推翻其目标：本仓仍不提供逐 bar 远程取数、不新开供给面），理由 = 产品读链优先 + 这组形状是逐表兼容。**待主任追认**；不追认则应回到「core 侧删/反代」并撤掉本笔 7 条路由。
> - **落地**：`internal/serving/legacy.go`（`LegacyReader` 端口 = `BarSource` + `TradingCalendar` + `Stocks` / `Stock` / `Fundamental` / `IndexConstituents`）；`internal/store/reference.go`（四条只读 SQL，点名列、无 `SELECT *`）；`internal/httpapi/legacy_read.go`（7 个 handler + `WithLegacyReader`；参数分诊 400、无记录 404 `not_found`、未接线 503 `dependency_unavailable`、bulk 逐票错误不进整体失败）；`server.go` 路由表、`policy.go` `servedPolicy`（`POST /api/v1/ohlcv/bulk` 从 `legacyREV17` **移出**）、`cmd/dataservice/main.go` 接线；`legacy_read_test.go` 覆盖 7 条成功形状 + 2 条 404 + 5 类 400 + 未接线 503。
> - **形状对拍（逐条实测 core 消费面）**：`{ohlcv:[...]}` / `{fundamental:{...}}` / `{stocks:[...]}` / `{constituents:[{symbol,...}]}` / `{trading_days:["YYYY-MM-DD"]}` / `{stock:{...}}`（404 ⇒ core 合成 `Stock{Symbol}`）/ `{results:[{symbol,ohlcv,error?}]}`；日期格式 `20060102`（读）/ `2006-01-02`（日历）。
> - **PIT 口径**：`/fundamental` 取 `COALESCE(available_date, trade_date) <= asOf` 的最新一期（与 core `pkg/storage` 一致）；`/index/{code}/constituents` 用半开区间 `[in_date, out_date)`；`stocks` 的 `delist_date` 用 `to_jsonb` 探针（本仓 `market.stocks` 无该列、旧 `public.stocks` 有）。
> - **已知限制（不静默降级）**：`stock_fundamentals` 的 DDL 仍是 core 的迁移（本仓只读）。`DATA_MARKET_SCHEMA=market` 且该表未建时 `/fundamental/{symbol}` 会 500；收编属 K3，不在本笔。
> - **未执行（如实，2026-11-10 复查已部分补齐：真库端到端、行数/校验对拍、core 写点归零均已完成，见下方施工记录；X1d 两项运维动作仍未做）**：真库端到端起服务实测未跑（本机无 PG、Docker 引擎不可用）；X1d 物理 `SET SCHEMA`、`visible_at` 生产回填、新旧行数/校验和对拍报告、core 写点归零取证均未做 ⇒ 本单保持 🔶，⑤ 的「core 写点 = 0 / `cmd/data` 删除」仍未满足。

**施工记录（2026-11-10，G3 真库端到端 + 对拍取证；本仓代码零改动）**

> 环境：本机 PostgreSQL 16.15（Windows 服务运行中，`127.0.0.1:5432`），与 2026-11-07 记录的「本机无 PG」口径不同——本轮实测可用。
>
> - **① 端到端起服务**：新建测试库 `tellurion_data_test`（后缀 `_test`）；`go build -o %TEMP%\dataservice_e2e.exe ./cmd/dataservice` 退出码 0；以 `DATABASE_URL=...tellurion_data_test?sslmode=disable`、`DATA_MARKET_SCHEMA=market`、空 `JWT_SECRET`（回环开放访问）启动，`/readyz` 首轮 200，启动日志「data ports wired to PG; write面 wired to PG, auth=false」，DDL 自动建出 `market`/`ingest` 全部表。
> - **② 播种**：`market.stocks` 2 行、`market.ohlcv_daily_qfq` 3 行、`market.trading_calendar` 7 行（含 11-07/11-08 两个非交易日）、`market.index_constituents` 2 行、`market.stock_fundamentals` 2 行（一期 `available_date=2026-10-30`、一期未来 `2026-12-25` 用于验 PIT）。`stock_fundamentals` 按已知限制**手工建表**（本仓 DDL 不建该表）。
> - **③ 7 条端点真库回执（全部 200 / 404 符合预期）**：
>   - `GET /ohlcv/000001.SZ?start_date=20261103&end_date=20261104` → 2 根，close 10.5 / 10.7；
>   - `GET /fundamental/000001.SZ?date=20261110` → `pe=10.5`、`date=2026-10-30`（未来期 `2026-12-25` 被 PIT 正确排除）；
>   - `GET /stocks` → 2 只；`GET /stocks?exchange=SZSE` → 1 只；
>   - `GET /index/000300.SH/constituents?date=20261110` → 2 条（半开区间内）；
>   - `GET /api/v1/trading/calendar?start=2026-11-02&end=2026-11-08` → 5 个交易日（周末两条被过滤）；
>   - `GET /api/v1/stocks/000001.SZ` → 200 单只；`GET /api/v1/stocks/999999.SZ` → 404；
>   - `POST /api/v1/ohlcv/bulk`（3 票，含 999999.SZ）→ 200，前两票带数据、未知票 `ohlcv: []`，整体不失败。
> - **④ 新旧行数/校验对拍**（旧路径已删，等价取证 = HTTP 回执 vs 同库直读 SQL）：`stocks` 行数 2=2，**md5 完全一致 `40f25781e835b1ff1e2d0e8d6a729fe5`**（`symbol|name|exchange|industry|status` 排序后拼接）；`ohlcv` 行数 2=2、`sum(close)=21.2`、`sum(volume)=3469134` 双侧一致；`constituents` 2=2；`calendar` 5=5；`fundamental` PIT 取值 `pe=10.5 / available=2026-10-30` 与 SQL 一致。
> - **⑤ core 写点归零取证（core 仓 `git grep`，非测试）**：`(INSERT INTO|UPDATE|DELETE FROM|TRUNCATE)\s+(market|ingest)\.` 唯一命中 `pkg/storage/index_daily.go:31`（`market.index_daily`，**裁决 ⑰ 保留 core owner**）⇒ 其余 `market.*` / `ingest.*` 写点 = 0；`FROM (market|ingest)\.` 唯一命中同文件 `index_daily.go:134`；`cmd/data` 与 `pkg/data` 目录均不存在；裸表写助手 `TableFor` 零命中。core 侧退役单 `SVC-CORE-DATA-RETIRE` 已 ✅，与本取证一致。
> - **⑥ 仍未做（不翻 ✅）**：X1d 的物理 `SET SCHEMA`（生产迁移）与 `visible_at` 生产回填（运维动作）——两者都不是 agent 可自行执行的生产写；X1d 行仍 🔶，故本单保持 🔶。
> - **⑦ 收尾**：测试库 `tellurion_data_test` 用完即删，进程已停；本仓代码与门禁零改动。

### QT-40 · X1 数据服务（统一协议 + aggregator + `dataset_snapshot_hash`）[P1] 🔶

> **迁入说明（2026-10-21）**：本单自 core 仓 `tellurion:docs/TASKS.md`「五、X 线」迁入（任务台账批量重分布：X 线单展开到各服务仓为正式六字段单，「占位仓不写正式任务单」旧约定同时作废）。六字段正文逐字保留、不改写；core 侧同号条目已改为指针。

- **① 目标**：数据协议冻结进 contracts；aggregator 能给出可复现的 `dataset_snapshot_hash`；connector 全部走声明式契约。
- **② 上下文**：`data-service-contract-and-ownership.md`（D19）X1a~X1d；现状已实现约 80%（12 connector + `ConnectorDeclaration` + `ETLPipeline`/`Normalizer`/`ValidatePoints`/`Deduplicate`；PG 已有 ingest/market/quant/research）。
- **③ 要求**：① 协议类型（`UnifiedDataPoint`/`OHLCV`/`FrequencySpec`/`TimeSemantics`）进 QT-05 的 contracts；② `ErrVisibleAtMissing` 等错误码；③ aggregator 面（PIT 过滤 + 频率聚合 + 票池解析 + 快照 hash + 缓存）；④ `dataset_snapshot_hash` 按 QT-03 的唯一定义实现（含 `calendar_hash`/`adjustment_basis`）；⑤ 12 connector 全走 Declaration + 共享 Normalizer。
- **④ 约束与非目标**：不重写现有 connector；不在内核内保留第二写者；不做跨库拆分。
- **⑤ 验收**：同一 hash 两次拉取返回一致；漏声明 `visible_at` 的源被拒收；改日历/复权口径 → hash 变。
- **⑥ 边界**：只做数据面；策略/风控/分析各自的 schema 见后续单。

**施工进度与未落项（2026-10-24，`go build/vet/test ./...` 全绿）**

| 序列 | 状态 | 已落 | 未落（不假装完成） |
|---|---|---|---|
| X1a 协议冻结 | ✅（2026-10-25） | contracts **`v0.6.0`**（2026-10-24 发版、**远端 tag 已推**）新增 `dataprotocol/`：`UnifiedDataPoint` 七字段（含 `VisibleAt`）+ `ToJSON` / `DeduplicateKey` + 15 个 `DataType*` 常量，导出面 447→465。本仓 `require v0.6.0`；`internal/ingest/point.go` 的**逐字段镜像已删**，改为 `type UnifiedDataPoint = dataprotocol.UnifiedDataPoint`（别名 + 注入时钟的构造器留本侧：契约刻意不读时钟，ADR-0004）；`DeduplicateKey` 公式改为委托契约方法。守卫：`protocol_test.go` 钉住「类型必须是契约那个类型」与去重坐标同值 | 无。**未顺手做（如实）**：`internal/store/write.go` 的写面 `Point` 仍是本地投影（store 刻意不认识采集语义，与 ETL 点位不是同一个形状），投影收编未随本单。**常量腿已收编（2026-10-25）**：`internal/ingest/connectors.go` 的 15 个 `DataType*` 改为 `dataprotocol.DataType*` 别名（导出名不变、12 个 connector 调用点零改；契约改值时本包取值随编译期变化），包内字面量零残留；守卫 `protocol_test.go:TestDataTypeConstantsMatchContract`（破坏验证：把 `DataTypeDailyBasic` 改成本地错误字面量 ⇒ 红，逐字节还原（`sha256 4a565295…`）⇒ 绿）；18 条声明 / 15 个 data_type 一一对应仍绿 |
| X1b aggregator | 🔶 取数面 + 写面代码已落（2026-10-24） | `internal/serving`：`Resolve`（参数校验 → 票池解析 → 取数 → 聚合 → PIT → 六分量 hash → 强幂等缓存）、`Snapshot`（重算 hash + 内容指纹自证）、`Metadata` 回显同源校验；`internal/normalize`：`Aggregate`（1m→15m/1d，会话内对齐、不跨午休、秒级边界）与 `VisibleAsOf`（`visible_at <= as_of`，缺可见时间点整批拒收）；`internal/httpapi`（resolve/snapshot/health，哨兵→状态码映射）+ `cmd/dataservice`；**四个 port 已有 PG 实现** `internal/store`（`DATABASE_URL` 有值即装配；空则 503）；**G4 新增**：`internal/ingest` 的 `ETLPipeline` / `Registry.Fetch` / `ValidatePoints` / `GatePoints` / `Deduplicate` 与 `internal/store.BulkInsert` 写面 | ~~`/factors/{name}@{hash}` 只有路由、恒 404~~ **已撤（裁-14，2026-10-25）**：因子读面只在 core，本仓路由表不含 `/factors/*`；~~写面装配未接~~ **H4 ② 已接（2026-10-25）**：`POST /ingest/points` + `internal/assembly` + `cmd/dataservice` 装配（core 经 HTTP 投点，取数与传输仍在 core）；`visible_at` 只从列取、未回填的行会被 PIT 整批拒收（生产 D2 未执行前的预期行为） |
| X1c connector 声明 | 🔶 connector 已搬（2026-10-24） | **12 个 connector 的取数/归一化已搬入** `internal/ingest`（`declarations_*.go` 各自持声明；运行时 9 方法面在 `adapter.go`）；**过渡期 `Catalog()` 双真相已删除**，装配改 `Connectors(Transports)`；`TushareTransport` / `MootdxTransport` 窄接口把传输留在 core（本仓不 import 实现包）。测试跑在真实 connector 上（18 条声明 / 15 个 data_type；SupportedTypes ↔ Declarations 一一对应） | **传输实现仍在 core**（Tushare/Eastmoney/Mootdx 的 HTTP/SDK 客户端与 raw 归档），本仓只按 `Transports` 注入；core 侧 `pkg/data/source` 的旧实现未删（跨仓切换不在本仓权限内）。**写面已归位**（2026-10-24，G4）：`internal/ingest.ETLPipeline` + `internal/store.BulkInsert`；但**同步作业的装配仍在 core**（`sync_jobs` → worker → ETL），core `pkg/data/source/etl.go` 的旧写路径尚未删 |
| X1d schema 归属 | 🔶 DDL 归属 + 写面已落（2026-10-24） | `internal/schema`：`market` / `ingest` 建表（**23 张**：`market` 19 + `ingest` 4；`TableNames()` 可对账——原写「19 张」只数了 `market`，2026-10-25 实测订正）、`visible_at` 三集合（A 集 12 张 × 目标/今天两侧、B 集承认 `as_of`、C 集逐条豁免、`GateClassFor` 分类）、哨兵防重放的回填腿（`BackfillStatements()`，与建表分开返回）；DDL 在 `DATABASE_URL` 有值时由 `cmd/dataservice` 启动执行。**G4 写面**：`internal/store.BulkInsert` 只开 A 集（B/C 集拒写）、事务前 `visible_at` 判零整批拒收、`ON CONFLICT DO NOTHING` + 内容比对（同内容 no-op / 异内容 `ErrContentConflict` 整批回滚） | **物理搬迁**（`SET SCHEMA` 到 `market` / `ingest`）未做——core 内联 DDL 仍是运行期事实；**回填只在测试库执行过**（2026-10-24，tagged 套件 `-tags backfill` + `DATA_TEST_DSN`），**生产未回填**；**第二写者：`capital_flow` / `top_list` 两条已停（H4 ①，2026-10-25 实测 core 生产 `NewETLPipeline` 引用归零）**，其余同步腿仍在 core 写 `market` / `ingest` 且仍用 `DO UPDATE`（与本仓内容比对语义并存，见 ADR-0004 代价）。**⚠️ 2026-10-25 二次实测：物理搬迁不能在本条停掉前做** —— core 仍直写 `market.index_daily`（`tellurion:pkg/storage/index_daily.go:31`，调用链 `pkg/data/tushare.go:929` → `cmd/data/handlers_sync.go:88` → `cmd/data/main.go:155`），且其余 ETL 用**裸表名**写 `public.*`（`tellurion:pkg/storage/bulk_insert.go` 的 `TableFor` 返回 `ohlcv_daily_qfq` 等裸名，core 仓内无 `search_path` 设置）——这些表在本仓 `TableNames()` 里已被登记为 `market.*` 所有。⇒ 搬迁前置是「core 停所有写点」，**现未满足**（跨仓单未立，见 `tellurion:docs/design/qt-decoupling/cross-repo-dispatch-plan-wave2-2026-10-25.md` §5 ⑰）；~~写面装配未接~~ **HTTP 投点入口已接（H4 ②）**，作业那条入口（`sync_jobs` → worker → `ETLPipeline`）仍在 core —— 本仓 `assembly.Pipeline` 今天零生产调用方 |

**施工记录（2026-10-24，G9 四步串行；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · X1d schema 归属 | 🔶 DDL 归属已落：`internal/schema`（19 张表 / A 集 12 张 `visible_at` 两侧加列 / B 集承认 `as_of` / C 集逐条豁免 / 哨兵回填腿）。**禁 `SET visible_at = trade_date` 有测试钉住**。**⚠️ 数字订正（2026-10-25 实测）**：`TableNames()` 实为 **23 张**（`market` 19 + `ingest` 4），原文的「19」只数了 `market`；本行保留原文以存历史，**引用时以 23 为准** | `9e68de6` |
| 2 · 4 个 port 的 PG 实现 | ✅ `internal/store` 四 port 落地并接线：`DATABASE_URL` 有值 → 建池 → 执行 DDL → aggregator；空 → 503。`visible_at` 只从列里取，NULL 交 PIT 整批拒收 | `ae85295` |
| 3 · 12 connector 搬入 | ✅ 12 个门搬入 `internal/ingest`（11 个文件，东财 sectors/toplist 同文件两门），一 connector 一 commit；**`Catalog()` 双真相已删除** | `a659ff0` + `34e3978`…`92e66fd`（12 笔）+ `00aeb25` |
| 4 · `UnifiedDataPoint` 迁 contracts | ⛔ **阻塞（前置未解除）**：G1 第二批次尚未发版——upstream 已发 `v0.4.0`，包清单仍无 `dataprotocol` / `UnifiedDataPoint`。本地镜像 `internal/ingest/point.go` 保留，不提前删（**2026-10-25 已解除**：contracts `v0.6.0` 已推，见下方 X1a 收编记录） | —— |

**施工记录（2026-10-24，G4 三步；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · X1d 写面 | ✅ `internal/ingest`：`ETLPipeline` / `Registry.Fetch` / `ValidatePoints` / `Deduplicate`；`internal/store.BulkInsert`：A 集表、三集合门、`ON CONFLICT DO NOTHING` + 内容比对、漏 `visible_at` 整批拒收 | `8d07a22` |
| 2 · `/factors/{name}@{hash}` | ✅ 路由 + 参数形状校验；未实现前**恒 404 `factor_not_found`**，不返回伪数据 | `37109be` |
| 3 · 停做三项 | **2026-10-24 已裁决拆分处置**：① `visible_at` 回填 → **仅测试库已执行**（tagged 套件，生产未执行，见下条施工记录）；② 物理 `SET SCHEMA` → **继续等**，需 core 先停第二写者（跨仓单独立项）；③ `X1a dataprotocol` → **走 G0D-C4 的 (a) 先迁**（contracts 新建 `dataprotocol` 包，本仓镜像改 import） | —— |

**施工记录（2026-10-24，P6 · 仅测试库回填；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| P6 · `visible_at` 回填（仅测试库） | ✅ `internal/schema/backfill_pg_test.go`（`-tags backfill`，默认构建不编译）：临时 schema 真跑 `BackfillStatements()`；A 集 12 张逐档取值（after_close / 锚点列 / ingest_time / created_at）、哨兵防重放与二次执行不改值、B 集 `as_of` 不改写、C 集无 `visible_at`；DSN 只认 `DATA_TEST_DSN` 且库名须 `_test`——生产 DSN（`DATABASE_URL`）与生产库名**自动 skip**，已设但连不上 fail-loud。**生产库未回填** | —— |

**施工记录（2026-10-25，X1a 收编 contracts `v0.6.0`；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · 契约 pin | ✅ `go get github.com/ruoxizhnya/tellurion-contracts@v0.6.0`（`v0.5.0 → v0.6.0`）；远端 tag 实测存在（`git ls-remote --tags origin v0.6*`） | 本笔 |
| 2 · 删镜像 | ✅ `internal/ingest/point.go` 的逐字段 struct 删除，改 `type UnifiedDataPoint = dataprotocol.UnifiedDataPoint`；`ToJSON` 用契约方法；注入 `IngestTime` 的构造器留本侧（契约不读时钟） | 本笔 |
| 3 · 去重坐标单源 | ✅ `internal/ingest/dedupe.go` 的 `DeduplicateKey(p)` 改为委托 `p.DeduplicateKey()`，不再另写公式 | 本笔 |
| 4 · 守卫 | ✅ `internal/ingest/protocol_test.go`：`TestUnifiedDataPointIsContractType`（类型必须 == 契约类型）+ `TestDeduplicateKeyDelegatesToContract`（UTC 归一 / 纳秒精度） | 本笔 |

**破坏验证（X1a 收编）**：把别名改回逐字段本地 struct（七字段同形、自带 `DeduplicateKey`）⇒ `TestUnifiedDataPointIsContractType` 红（`ingest.UnifiedDataPoint` ≠ `dataprotocol.UnifiedDataPoint`）；逐字节还原（`sha256 f9b02f6f…`）后复跑绿。

**施工记录（2026-10-25，X1a 常量腿；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · 常量别名 | ✅ `internal/ingest/connectors.go` 的 15 个 `DataType*` 改 `dataprotocol.DataType*` 别名（导出名不变、调用点零改，契约改值编译期随动）；包内字面量零残留 | 本笔 |
| 2 · 守卫 | ✅ `internal/ingest/protocol_test.go` 新增 `TestDataTypeConstantsMatchContract`（15 个常量逐一 == 契约值） | 本笔 |
| 3 · 注册面 | ✅ `TestConnectors_CoverAllWireDataTypes`（18 条声明 / 15 个 data_type）、`TestConnectors_DeclarationsMatchSupportedTypes` 同轮 PASS | 本笔 |

**破坏验证（常量腿）**：把 `DataTypeDailyBasic` 改成 `"daily_basic_broken"` ⇒ 守卫红；逐字节还原（`sha256 4a565295…`）后复跑绿。

**验收三条的实测证据（本仓 `go test`）**

1. **同一 hash 两次拉取逐位一致**：`TestSnapshotSameHashReturnsByteIdenticalBody` PASS（wire 级 body 逐字节）；`TestResolve_IsDeterministicAndEchoesMetadata` PASS。
2. **漏声明 `visible_at` 的源被拒收**：`TestRegistry_RejectsMissingDeclaration` PASS（注册期）；`TestGatePointsRejectsMissingAnchorEvenWhenGeneratedColumnPresent` / `TestGatePointsRejectsWholeBatchOnSingleOffender` PASS（写侧整批拒收）。
3. **改日历 / 复权口径 → hash 变**：`TestResolve_HashCoversEveryFrozenComponent` 六个子用例（universe / window / frequency / data_version / calendar / adjustment）全 PASS。
4. **G4 写面（2026-10-24）**：漏 `visible_at` 整批拒收 —— `TestWriter_RejectsWholeBatchWithoutVisibleAt` / `TestETLProcess_MissingVisibleAnchorRejectsWholeBatchWithoutWriting` / PG 腿 `TestPG_BulkInsert_RejectsBatchWithoutVisibleAt` PASS；同主键同内容 no-op —— `TestWriter_SameContentRewriteIsNoOp` / `TestPG_BulkInsert_IsIdempotentOnSameContent` PASS；同主键异内容 fail-loud 且整批回滚 —— `TestWriter_DifferentContentFailsLoudAndRollsBack` / `TestPG_BulkInsert_ConflictingContentRollsBackWholeBatch` PASS；B 集拒写 —— `TestWriter_RejectsTablesOutsideASet` / `TestPG_BulkInsert_RejectsBSetType` PASS；`SupportedTypes ↔ Declarations` 一一对应保持绿；~~`/factors` 恒 404~~ **裁-14 已撤（2026-10-25）** —— `TestFactorRouteIsRemoved` / `TestRouteTableIsThePublishedSurface` PASS。
5. **P6 回填（2026-10-24，仅测试库）**：`DATA_TEST_DSN='postgres://…/tellurion_data_test' go test -tags backfill ./internal/schema` PASS——A 集 12 张逐档取值、哨兵防重放/二次执行不改值、B 集 `as_of` 不改写、C 集无 `visible_at`；生产 `DATABASE_URL` 与「生产库名塞进 `DATA_TEST_DSN`」两种设置下均 **SKIP**；默认 `go test ./...` 不编译该套件。临时 schema 跑完即删（实测只剩 `public` / `information_schema` / `pg_*`）。

**破坏验证（三条，均已「改坏 → 红 → 逐字节还原 → 绿」）**

- X1d 回填把 `afterClose` 改成 `trade_date` ⇒ 禁止第四档 + 显式收盘口径两条测试同时红；
- port 把 `VisibleAt` 改成按 `trade_date` 现算 ⇒ 两条 store 测试红（NULL 未传播到 PIT）；
- 改一条 connector 声明的 `DataType` ⇒ 注册门 / 一一对应 / `DeclarationFor` 三条红。

**G4 破坏验证（2026-10-24，已「改坏 → 红 → 逐字节还原 → 绿」）**

- 把 `store.BulkInsert` 的 `visible_at` 判零短路（`if false && p.VisibleAt.IsZero()`）⇒ `TestWriter_RejectsWholeBatchWithoutVisibleAt` FAIL（预期错误未出现）；从备份逐字节还原后 SHA256 一致、测试 PASS。

**P6 破坏验证（2026-10-24，已「改坏 → 红 → 逐字节还原 → 绿」）**

- 关掉 DSN 的 `_test` 后缀守卫（`if false && !strings.HasSuffix(...)`）⇒ `TestBackfillTestDSN_ProductionDatabaseIsSkippedNotRun` FAIL（生产 DSN 被放行）；逐字节还原后 SHA256 一致、PASS。
- 把 `057a_visible_at_capital_flow` 的表达式改成 `trade_date` ⇒ `TestBackfill_NeverAssignsVisibleAtFromTradeDateAlone` FAIL，且测试库套件两条断言 FAIL（取值 07:00 不符 / 第四档 `trade_date` 零点）；逐字节还原后两腿均 PASS。

**施工记录（2026-10-25，D4-1 · 裁-14 撤因子读面；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · 撤路由 | ✅ `internal/httpapi/server.go` 删 `GET /factors/{factor}` 与整个 `handleFactor`；`sentinelCodes` 去掉 `serving.ErrFactorNotFound`；原处留注释说明「不要重新引入」 | 本笔 |
| 2 · 撤哨兵 | ✅ `internal/serving/errors.go` 删 `ErrFactorNotFound` 及其头注释条目（它只服务这条恒 404 路由） | 本笔 |
| 3 · 路由守卫 | ✅ `internal/httpapi/server_test.go` 删 `TestFactorEndpointIsAlways404AndNeverFake`，新增 `TestFactorRouteIsRemoved`（`/factors/momentum@deadbeef`、`/factors/momentum`、`/factors/@x` 三条路径均 404，且响应体不含 `factor_not_found` / `dataset_hash`——钉住「不是换了个 404 壳」）与 `TestRouteTableIsThePublishedSurface`（`/health` 200、`/datasets/resolve` 缺参 400、`/datasets/deadbeef` 业务 404 `snapshot_not_found`；发布面 ≡ 路由表） | 本笔 |
| 4 · 文档 | ✅ `docs/api-schema.md` 删路径表行与 `factor_not_found` 错误行、实现状态改「已撤」、§5 边界改「不提供因子读面」；`docs/dataflow.md` 图 / 因子边 / 失败模式 / 不变量 7 同步；`docs/ARCHITECTURE.md` X1b 未落项同步 | 本笔 |
| 5 · 契约对拍 | ✅ 本仓**无 openapi 文件**（未生成 `dto/`）；core `tellurion:docs/openapi.yaml` grep `factors` 只在 `/api/factors/{factor_name}`（citation 点查）与响应字段上命中，**从未收录 `/factors/{name}@{hash}`**（对表 G 组 T3 明确「本次不入 openapi」）。故双向对拍 = 「路由表 ≡ 发布面」由 `TestRouteTableIsThePublishedSurface` 在代码侧钉住，契约侧无条目可删 | 本笔 |

**破坏验证（D4-1）**：把 `mux.HandleFunc("GET /factors/{factor}", s.handleHealth)` 加回 `Routes()`（等价于恢复旧占位）⇒ `TestFactorRouteIsRemoved` FAIL（`/factors/momentum@deadbeef` 期望 404 实得 200）；逐字节还原后 `sha256 063DACAEF79B5A53FB4DC6139CAC66D38504D09AFC47B50A78EDC571807A20DC` 与改前一致，复跑 PASS。

**施工记录（2026-10-25，W4 · 鉴权接线 + REV-17 逐条移植；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · 机制接 kit | ✅ `internal/httpapi/auth.go`：`Authorizer` / `NewAuthorizer(*auth.TokenService)` 内部走 kit `v0.1.1` 的 `auth/httpadapter`；`requireAuth` 三态（503 / 401 / 403）；`/health` Public 也过闸 | 本笔 |
| 2 · 策略表 | ✅ `internal/httpapi/policy.go`：`servedPolicy`（health Public + 两个 `/datasets/*` viewer）+ `legacyREV17`（旧 `cmd/data` 28 条逐条移植，admin / trader / any-authenticated 档位一一对应） | 本笔 |
| 3 · 路由同源 | ✅ `Server.routes()` 单一列表注册+包闸；`routePatterns()` 供守卫对拍 | 本笔 |
| 4 · 启动闸 | ✅ `cmd/dataservice`：`-jwt-secret` / `JWT_SECRET` + `-insecure-exposure` / `AUTH_INSECURE_EXPOSURE`；`DecideStartup`——默认 `:8081` 无 secret **拒绝启动** | 本笔 |
| 5 · 测试 | ✅ `TestRouteTableEqualsServedPolicy`（3 路由 ≡ 3 策略）、`TestLegacyREV17PolicyIsVerbatim`（28 条条目与档位）、`TestLegacyREV17DecisionsMatchOldService`（同一路由+角色结论一致）、`TestAuthWireThreeStates`（health 公开 / 401 / viewer 可读 / 503） | 本笔 |
| 6 · 依赖 | ✅ `github.com/ruoxizhnya/tellurion-kit v0.1.1`（AGENTS §4） | 本笔 |
| 7 · 文档 | ✅ `api-schema.md`（策略表 / 401/403/503）、`ARCHITECTURE.md` §5.1/§6、ADR-0005 + 索引、README/AGENTS | 本笔 |

**破坏验证（W4）**：删掉 `servedPolicy` 里的 `GET /datasets/{hash}` ⇒ `TestRouteTableEqualsServedPolicy` 红（route 3 vs policy 2）、`TestAuthWireThreeStates` 红（viewer 期望 503 实得 403）；逐字节还原（`sha256 AA06D41F…`）后两条复跑绿。

**跨仓腿（core）**：`cmd/analysis/handlers_proxy.go` 的 `proxyRequest` 已把入站 `Authorization` 转发给数据服务（2026-10-25）；core 侧守卫 `TestProxyForwardsAuthorization` / `TestProtectedRouteThroughProxyDoesNotLoseCredentials` 证明 `POST /api/v1/ohlcv/bulk` 经代理不 401。

**施工记录（2026-10-25，G6 · 裁-17 本仓半边：`servedPolicy` 加 `service`；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · 前提实测 | ✅ 派工写「无独立可开工项」，实测本仓有一条**已裁决未开工**的独立项：裁-17 只落了文档（`90e0efb`），`policy.go` 两条 `/datasets/*` 的 Roles 仍是 `{viewer, trader, admin}` | 本笔 |
| 2 · 依赖判定 | ✅ **不升 kit**：本仓 pin 的 `v0.1.1` `httpadapter.Authorize`（模块缓存 `tellurion-kit@v0.1.1/auth/httpadapter/httpadapter.go:159-163`）按角色 any-of **精确串匹配**、不看角色枚举；kit 本地 `main`（`v0.1.2`，KT-09）新增的 `IssueServiceToken` 铸的是 `Kind: KindAccess` + 调用方给的角色串（`auth/service_token.go:41-44`）⇒ 服务 token 就是普通 access token，**pin 版本即可验**，本仓不为此改 `go.mod` | 本笔 |
| 3 · 策略表 | ✅ `servedPolicy` 两条读端点 Roles 加 `service`；`/health` 保持 Public（裁-17 原口径） | 本笔 |
| 4 · 测试 | ✅ `TestServedPolicyServiceRole`：service token 在 `/datasets/resolve` 与 `/datasets/{hash}` 都走到 handler（断 503 **且** 报文 `code=dependency_unavailable`——只看状态码会把闸拦下的 503 `auth_unavailable` 误读成过闸）；合法签名但角色 `writer` 仍 403 `forbidden`；策略表逐条含 `service` + `/health` 仍 Public | 本笔 |
| 5 · 文档 | ✅ `api-schema.md`（鉴权注 / 策略表 / 守卫清单 / 403 触发行）、`ARCHITECTURE.md` §6 策略行 | 本笔 |

**破坏验证（G6）**：把两条 Rules 的 `service` 抽掉 ⇒ `TestServedPolicyServiceRole` 红在 wire 层（`/datasets/resolve` 实得 403 `forbidden`，期望 503 `dependency_unavailable`）；逐字节还原（`sha256 16956C20…` 与还原后一致）后 `TestServedPolicyServiceRole` / `TestRouteTableEqualsServedPolicy` / `TestAuthWireThreeStates` 三条复跑绿。

> **订正（2026-10-25，随 kit `KT-11` / `v0.1.4`）· 上面第 2 步「不升 kit」的结论已被证伪 ⇒ 本仓 pin 现为 `v0.1.4`**
> 第 2 步当时判的是「**角色档位**能不能用 `service` 串」——那一半仍然成立（`Authorize` 走 any-of 精确串匹配，不依赖 kit 的角色枚举）。
> 它漏判的是**同一条 token 能否过 `Authenticate`**：`v0.1.1` 的 `authenticate()` 无条件要求 `claims.UserIDInt64()`，
> 而裁-17 的口径是 `sub=service:<repo>`（非数字）⇒ 服务 token 在**进入策略表之前**就 401 `malformed token subject`，
> `TestServedPolicyServiceRole` 当时能过是因为它用的是 `IssueTokens` 签的**数字 subject** + `role=service`，绕开了这个形状。
> kit `v0.1.4`（`KT-11`）只对 `claims.Role == auth.ServiceRole` 放宽该校验，其余输入仍 401。
> **本仓动作**：`go.mod` / `go.sum` 升 `v0.1.4`（`go.sum` 两行取自 `sum.golang.org`；`v0.1.4/go.mod` 的 hash 与 `v0.1.1` 逐字节相同 ⇒ 依赖图未变），
> `AGENTS.md` §4 那一行同步。**验收证据 = CI 退出码**（写这条的当时本机无 Go 工具链，`go build/vet/test` 一条都没复跑，不拿本机绿当结论）。
> ⚠️ **2026-10-26 复测订正：「本机无 Go」从今天起不许再当不复跑的理由**。Go **1.25.14 已装**
> （`C:\Users\ruoxi\AppData\Local\Programs\go1.25.14\bin\go.exe`），只是**不在默认 Git Bash 的 PATH 里**
> （`command -v go` 空）——先 `export PATH="/c/Users/ruoxi/AppData/Local/Programs/go1.25.14/bin:$PATH"` 就能实跑。
> 本批对本仓实跑的真实退出码：`gofmt -l .` **空**；`go build ./...` **EXIT=0**；`go vet ./...` **EXIT=0**；
> `go test ./... -count=1` **EXIT=0**（`-v` 下 **4 条 SKIP**，全是 `TestPG_BulkInsert_*` —— 本机 `127.0.0.1:5432` 无监听，
> `netstat` 0 行；skip 不是 pass，PG 腿仍要在有库环境补）。两条本机硬限制照旧：`-race` 跑不了
> （`go env CGO_ENABLED` = 0，`go build -race` 直接 EXIT=2「requires cgo」），故 race 腿仍归 CI。
> **全组统一环境表**：`tellurion:docs/design/qt-decoupling/cross-repo-decision-record-2026-10-26.md` §三 —— 环境口径以后者为唯一出处，别再各仓各写一份（本行此前与 orchestration 那行就是两套相反结论并存）。

> **裁决（2026-10-25，主任口径）· ⑨：`visible_at` 生产库回填 —— 暂不授权**
> 裁决全文见 `tellurion:docs/design/qt-decoupling/cross-repo-decision-record-2026-10-25.md` ⑨。
>
> **依据（实测）**：
> - **core 第二写者仍在** —— core `pkg/data/source/etl.go` 存在、第 68 行注释写着 `ON CONFLICT upsert`；`cmd/data/registry_init.go` 实测 11 处 `RegisterJobType` ⇒ 同步作业层仍完整在 core。
> - **回填后 PIT 门立即生效**：历史上 `visible_at` 为 NULL 的行会**整批拒收**（本台账原文「未回填的行会被 PIT 整批拒收」）⇒ 在第二写者没停的前提下，这等于把数据面切到一个未验证的状态。
> - 本台账原文已写死红线：「**生产 D2 前序红线：D2 未完成期间读路径不许按 `visible_at` 过滤**」。
>
> **落地动作**：把「生产回填」登记为**运维动作**而非 agent 动作；前置写死为「**core 停第二写者 → 回填 → 才允许读路径按 `visible_at` 过滤**」。回填脚本的护栏已就绪且 fail-closed（`internal/schema/backfill_pg_test.go` 用 `-tags backfill`，只认 `DATA_TEST_DSN` 且库名须 `_test`，生产 DSN 与生产库名自动 skip）。**本轮不安排任何 agent 去写生产库。**

**仍未落（core 半边，不在本仓）**：部署清单给 `data-service` 配 `JWT_SECRET` 与调用方 `service` token = core QT-71 腿；本仓只保证「给了 service token 就能读」，不保证生产上已经配发。

**施工记录（2026-10-25，H4 ② · 写面装配：`POST /ingest/points` + `internal/assembly`；`go build/vet/test ./...` 全绿，tagged 回填腿同批复跑绿）**

| 步 | 状态 | 做了什么 | 判据 |
|---|---|---|---|
| 1 · 装配层 | ✅ 新包 `internal/assembly` | `Writer.Write`（上限 → 声明 → 逐点 data_type / 空 source 点名 → `ValidatePoints` → `GatePoints` → `DeduplicateWithCount` → 投影 → sink）、`Project`/`ProjectAll`、`NewInserter`、`Pipeline`（作业那条入口与 HTTP 这条入口**共用同一个写侧次序**，由 `TestWriteAndPipelineAgreeOnTheSameBatch` 逐字节钉住）、`DeclarationsRegistry`（`RegisterConnectors(reg, Transports{})`：装声明**不装传输**，本仓禁区）、`WireError`（哨兵→状态码表放本包，`httpapi` 不 import 三层） | `internal/assembly/assembly_test.go` 14 条（含整批拒收不触 store、上界用绝对数值、每条哨兵各有用例） |
| 2 · HTTP 写端点 | ✅ `POST /ingest/points` | `MaxBytesReader` 8 MiB + `DisallowUnknownFields` + `UseNumber`（`json.Number` 保精度）；策略表 `{service, admin}`；未装配 ⇒ 503 `dependency_unavailable` | `internal/httpapi/points_test.go`（含 `TestIngestPointsThroughRealWriter`：**真 `store.Writer`** 跑 B/C 集拒收且零 INSERT、A 集出 `INSERT INTO public.ohlcv_daily_qfq … visible_at … ON CONFLICT DO NOTHING`；含鉴权三态与 405） |
| 3 · compose root | ✅ `cmd/dataservice/main.go` | DSN 有值才装 `store.NewWriter` + `DeclarationsRegistry` + `assembly.New` + `httpapi.WithPoints`；`statusLine` 把读面 / 写面分开报 | 新 `cmd/dataservice/main_test.go` 3 条（本包此前**零测试**，装配那一跳没证据 = SYNC-01 家族） |
| 4 · 文档 | ✅ | `api-schema.md` §1（路由 + 三条硬规则 + 错误码逐行 + 幂等语义 + §5 边界 + ⚠️ 从未对真库跑过）；`dataflow.md`（[装配] 节点、跨仓写边、§2 有界红线与分片代价、4 条失败模式、不变量 3 重写 + 新增 8/9/10）；`db-schema.md` §1.5/§6.6；`ARCHITECTURE.md` §2.1（六包 + 装配层是缝不是限界上下文）/§8 X1b；`AGENTS.md` 状态与本节；新 ADR-0006 | 文档动了 == 代码动了（同笔提交） |

**破坏验证（H4 ②，四条腿，改坏 → 红 → 逐字节还原 → 绿）**：
① `internal/store/write.go:294` 的 `if p.VisibleAt.IsZero()` 短路成 `if false && …` ⇒ `TestWriter_RejectsWholeBatchWithoutVisibleAt` 红（`An error is expected but got nil`）；还原 sha256 `ef5dd20c815bd203` 一致后复跑绿。
② `internal/assembly/assembly.go` 的 `GatePoints` 失败分支从 `return Result{}, err` 改成 `_ = err; gated = valid` ⇒ `TestWriteRejectsWholeBatchWithoutAnchorAndNeverCallsStore` 红（缺 `normalize: data point has no visible_at`）；还原 sha256 `af114e3708739ed5` 一致后绿。
③ `cmd/dataservice/main.go` 未配 DSN 那条分支塞进 `httpapi.WithPoints(nil)` ⇒ `TestComposeRootWiresTheWriteFace` 红（503 装配形状被改掉）；还原 sha256 `7d6a4295ef2e88d9` 一致后绿。
④（同批 core 腿）白名单多塞一个文件 / 生产码重新出现 `NewETLPipeline` 引用 ⇒ repoguard 红，见 `tellurion:docs/TASKS.md` QT-40 H4 ① 记录。

**环境注（如实）**：`tellurion_data_test` 这个测试库在本机**曾存在、今天不存在**（首次跑本单的门禁腿时 PG 答 `database "tellurion_data_test" does not exist`，套件的 fail-loud 分支如实红了）。为执行组级验收而 `CREATE DATABASE tellurion_data_test`（空库，不含业务数据），随后 `-tags backfill ./internal/schema` 绿。这是环境动作，不是代码结论。

**未落项（如实登记，不写成已完成）**

- **第二写者：两条停了，其余仍在**（H4 ① 同批，2026-10-25 实测）：`capital_flow` / `top_list` 这两条 core 侧的 `INSERT … ON CONFLICT DO UPDATE` 已经改成投点给本仓 `POST /ingest/points`，core 生产代码里 `NewETLPipeline` 引用与 `ETLPipeline.Process` 调用方**双双归零**、repoguard 白名单同批清空为空名单。其余同步腿（ohlcv / stocks / fundamentals / calendar / index_constituents / sectors / limit_up_pool / `ingest.raw` 归档…）照旧在 core 写。物理 `SET SCHEMA` 搬迁（D4）仍未做（前提见上条与本条：写点没有归一）。
  - **写点实测口径（G6，2026-10-25 更正本条的粗糙表述）**：core 里**带 schema 前缀**写 `market.` / `ingest.` 的生产点只有两处——`tellurion:pkg/storage/index_daily.go:31`（`market.index_daily`）与 `tellurion:pkg/storage/ingest_raw.go:62`（`ingest.raw`）；主力同步腿 `tellurion:pkg/storage/bulk_insert.go:331` 拼的是**无前缀**表名（`ohlcv_daily_qfq` / `ohlcv_minute`），今天落 `public`。所以「停第二写者」**不等于**「core 整个同步层停写」：真正要做的是那两处前缀写点归一 + 无前缀写点在表搬走后的 search_path / 限定名处置（本仓 `DATA_MARKET_SCHEMA` 设空串走 `public` 就是这个过渡开关，见 `api-schema.md` 引言）。判据以 grep 为准，别引用任何文档里写死的点数。
- **`visible_at` 生产未回填**：D2 已**只在测试库**经 tagged 套件真跑（`internal/schema/backfill_pg_test.go`，`DATA_TEST_DSN` + 库名 `_test`）；生产库回填仍须显式启动开关或运维执行，**未做**。未回填的行会被 PIT 整批拒收（生产 D2 前序红线：D2 未完成期间读路径不许按 `visible_at` 过滤）。
- **传输未搬**：Tushare / 东财 / Mootdx 的 HTTP/SDK 客户端与 `ingest.raw` 归档仍在 core；本仓按 `Transports` 注入。
- **写面已接（HTTP 这一半）✅（H4 ②，2026-10-25，本笔）**：`POST /ingest/points` → `internal/assembly.Writer` → `store.Writer`，`cmd/dataservice` 在有 `DATABASE_URL` 时真的装配。
  - **仍没接的是作业层那一条入口**：本仓**没有** `sync_jobs` / worker / 调度（它们在 core），所以 `assembly.Pipeline` 今天**零生产调用方**——落它是为了让两条入口共用同一写侧次序**可测**，不是占位。传输也仍在 core（下条）。
  - **端点从未对真 PG 跑过一次**：`store.Writer` 的 PG 腿是 opt-in（`DATA_TEST_DSN`），HTTP → assembly → PG 的**整条**在本机没有跑通过（取证与复现命令见 `api-schema.md` §1 末的 ⚠️）。登记为缺口，不写成「端到端已验收」。
- ~~**`/factors/{name}@{hash}` → 裁-14（2026-10-25）**~~ **✅ 已落（D4-1，2026-10-25，本笔）**：路由、`handleFactor`、`ErrFactorNotFound` 与恒 404 占位全部撤掉，不做表迁移。原因：生产方与读端点都在 core（`pkg/data/factor_formula.go` / `factor_equitydeep.go` → `SaveFactorCacheBatch`；读面 `cmd/data/handlers_factor.go`），SPEC 把 C 类派生计算（因子 / 回测 / IC）列为本仓非目标；常驻 404 比没有路由更坏——调用方会把 404 读成「该数据集没有该因子」。**因子读面明确由 core 提供**，本仓 openapi / api-schema 同步删除该路径。**上移触发条件（任一成立再启动整体迁移）**：① C 类生产需要横向扩容；② 出现第二个不接受 core API 的消费方。
- ~~**鉴权未移植（裁-15，2026-10-25 已定，可开工）**~~ **✅ 已落（2026-10-25，W4）**：新 `dataservice` 已接 kit `v0.1.1` 的 net/http 适配；`servedPolicy` = `/health` Public + `/datasets/*` viewer；旧 `cmd/data` 的 REV-17 表 28 条逐条移植为 `legacyREV17`（端点迁入时直接挂闸）；路由表 ≡ 策略表守卫 + 三态测试已落。**未落**：REV-17 对应的端点和真实保护仍在 core `cmd/data`（本仓只持迁移表）；core `proxyRequest` 已转发 `Authorization`（2026-10-25）。
  - ~~**服务间凭证（裁-17 定稿，2026-10-25）：`servedPolicy` 两条 Roles 需加 `"service"`**~~ **✅ 本仓半边已落（G6，2026-10-25，本笔）**：`GET /datasets/resolve`、`GET /datasets/{hash}` 的 Roles 现为 `service` + viewer / trader / admin，`/health` 保持 Public；~~**没升 kit**（服务 token 就是 `kind=access` 上的角色串，pin 的 `v0.1.1` 即可验，见上方施工记录第 2 步）~~ **⚠️ 这半句已订正（2026-10-25，随 kit `KT-11` / `v0.1.4`）：`v0.1.1` 的 `Authenticate` 会把 `sub=service:<repo>` 的非数字 subject 当场 401，策略表根本读不到 ⇒ 本仓 pin 已升 `v0.1.4`，理由与取证见上方 G6 施工记录末的订正块**。**仍未落**：部署清单给 `data-service` 配 `JWT_SECRET` 与调用方 `service` token 属 core QT-71 腿。
- **跨仓腿（报协调者，本仓不做）**：① 物理 `SET SCHEMA` 需要 core 先停第二写者；② 传输实现仍在 core（X1c 已登记）；③ ~~`UnifiedDataPoint` 迁 contracts 等 G0D-C4 发版~~ **已解除（2026-10-25）**：contracts `v0.6.0` 已推，本仓镜像已删、改别名，见上方 X1a 收编记录。

**已登记的取舍与风险（必须随 X1b 一起读）**

- **`as_of` 不在冻结六分量里**：同六分量、不同 `as_of` 会撞同一 hash。处置 = 缓存对同 hash 不同 metadata / 内容报 `ErrSnapshotMismatch`（fail-closed，绝不静默返回别人的数据集），并有测试钉住。是否把 `as_of` 升为第七分量属 QT-03 后继裁决。
- **聚合先于 PIT 过滤**：bucket 内任一条 1m bar 的可见时刻都不晚于 bucket 结束；若先过滤再聚合会造出「看起来完整、实际缺尾」的半桶。
- **15m 不物化、1d 直读日线表**：`sourceFrequency` 是唯一映射；1d 不从分钟合成（避免日线口径随分钟完整度漂移）。
- ~~**catalog 是过渡期双真相**~~ **已消除（2026-10-24）**：`Catalog()` 清单已删除，声明改由每个 connector 自己的 `Declarations()` 提供；`Connectors(Transports)` 只做装配。守卫：18 条声明 / 15 个 data_type / `SupportedTypes ↔ Declarations` 一一对应（破坏验证：改一条声明的 DataType → 三条测试同时红）。


---

> **⚠️ 开工前置（refactor-plan-review M5/M8，2026-10-20 登记）**：本表任何单开工前，必须先过 core `tellurion:docs/TASKS.md`「五、X 线」的**公共前置**两条：① 建仓/升格前先与 ADR-027 §2 对账（12 仓是上限/当前规划，不是承诺终态）；② 本仓设计文档先写「**接口对表**」小节（签名 + 错误语义 + 版本策略，模板见 `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1），**未对表不开工**。正文以彼处为准，此处只留指针、不镜像。
>
> **✅ 两条前置的执行状态（2026-10-21 落，2026-10-22 收尾）**：① **M5 已结案** → `tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2（本仓在 ADR-027 §2.6.4 拓扑里**有位子**，是两个「不需要另立 ADR 就能升进程」的仓之一；代价是背上 §2.6.2 的**六个限界上下文包级隔离**义务，见对表 T4）。② **M8 已落** → `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1（QT-40 的逐个 port/endpoint：签名 + 错误语义 + 版本策略 + 五条待办 T1~T5）。
> ⚠️ 原条目把模板指向 `target-module-topology.md` 的「UnifiedDataPoint 节」——**该文件 grep `UnifiedDataPoint` 零命中**（幻影引用），已订正为 §4.1。
>
> **✅ 对表 T2~T5 已收尾（2026-10-22；T1 于 2026-10-21 结案）**——四条各落到哪儿，正文不镜像、只指节号：
>
> | 待办 | 落在哪 | 本轮裁出/逮到的东西（各一句话，细节在对应节） |
> |---|---|---|
> | T2 | `tellurion-contracts:docs/API.md` §8 待裁 **#4** | `dataprotocol` 落点已定名，但「只有 core 一个使用方 ⇒ 不许提前迁」与 ADR-0001「代价」节（S1 接受 core-only、事后回收）**是两个都自洽的读法**，X1a 前必须挑一条；顺带订正 core 注 ② 与本仓 `api-schema.md` §4 **误引的条款号**（③ 管命名，数量那条是 ①） |
> | T3 | core 对表 §4.1 G 组 **T3 定案** | 真相 = `/factors/{name}@{hash}`；`/api/factors/{factor_name}` 保留但收窄为 citation 点查、不进回测取数路径。**本次不入 openapi**（三条端点零实现，写进表 = 契约里出现不存在的端点，而 `dto/` 是 openapi 的生成物）；另逮到一条**与入表无关的既有欠账**：`docs/openapi.yaml:3103` 少了实现要求的 `symbol`/`date` 两个必填参数 ⇒ 按契约生成的客户端必然 400 |
> | T4 | 本仓 `docs/ARCHITECTURE.md` **§2.1** | 六上下文逐个给目标包 + 今天代码位置；Reduction **故意不分配包**（那是 core `repo-inventory.md` 待裁 U6，服务仓文档无权替另一个仓的拓扑裁决）；两条口子如实登记：`pkg/data/source` 一包装了采集+规范化两个上下文（整包平移 ≠ 边界成立），`Board` 物理在供给侧、`PITAlignment` 今天无承载物 |
> | T5 | 本仓 `docs/db-schema.md` **§6** | 门钉到 `ValidatePoints`（加判据 = 从「计数丢弃」变「整批拒收」，是行为变更）；范围裁成**三个集合**：A 加列 12 张 / B 承认现成 `as_of` 2 张（`market_activity`、`quant.macro_series` —— 它们**不走 BulkInsert，门拦不到**）/ C 明确门外（含实测漏项 `market.index_daily`）；不可回填三档 + 明禁「`SET visible_at = trade_date` 一把梭」；`stock_fundamentals.available_date` 是 `COALESCE(ann_date, trade_date)` 生成列 ⇒ 拿它当判据是**假护栏**，判据必须写 `ann_date IS NOT NULL` |
>
> 收尾后本单的**直接**硬前置只剩 `QT-05` 一条（下表第一行）——**2026-10-24 已实测解除**；W0 那两条是**间接**（卡的是 QT-05），`CT-02` 已落、不再挂账。③-① 的「`TimeSemantics` 进 contracts」也要按 T2 的口径读：它随 `domain/market` 整包迁，**不在 `dataprotocol` 里重复定义**。

**硬前置对账（2026-10-24：原列三条均已解除，保留历史口径）**：

| 前置 | 状态 | 卡住的是 |
|---|---|---|
| `QT-05`（S1 契约层落码：`pkg/domain`（含 `domain/market`）+ `pkg/backtest/contracts` 进 contracts） | ✅ **已完成**（2026-10-24 实测：contracts 四包 429→439 导出面、已发版至 `v0.3.0`；本仓 `go.mod` `require v0.1.0`） | 不再卡本仓；原「X1b 全部」前提作废（见本节 2026-10-23 实测更新注） |
| `QT-04` / `QT-63`（W0 剩下的两条） | ✅ / ✅（2026-10-24 实测 core 台账：`QT-04` 标题行 `✅（2026-10-22）`、`QT-63` 标题行 `✅（2026-10-22）`；原 ⬜ 是过期快照） | 不再卡 QT-05（QT-05 本身已完成，见上一行） |
| `CT-02` / `QT-19`（contracts 版本规则表 + 兼容矩阵 + CHANGELOG） | ✅ **已落**（2026-10-22，contracts `191ba0b`：README 的版本规则表 + 兼容矩阵 + `CHANGELOG.md` + `check_release_matrix` 进 CI；`QT-19` 那条**排在 `QT-07`（S2 打 tag）之后**；2026-10-23 起矩阵已有 `v0.1` / `v0.2` / `v0.3` 三行，不再是缺项） | 不卡施工，卡**口径**：版本规则只认 contracts README 那张表 + `tellurion:docs/TASKS.md` QT-19 ③ 那五条（0.x 期允许 breaking，但要 bump MINOR + 写 CHANGELOG），**本仓不许自造版本口径**。对表 F 组与 §2.1 的引用已对上 |

> **2026-10-23 实测更新（2026-10-24 已按实测把上表状态校正为 ✅）**：`QT-05` 已**部分落地**——contracts 仓 `v0.1.0` 已发（S2 打 tag 完成），`domain/market` 与 `engine` 的六分量 hash 可直接消费，本仓 `require v0.1.0` 后离线 `build/vet/test` 全绿。仍缺 `UnifiedDataPoint` 进 contracts ⇒ **X1c 的协议冻结半边**继续挂此条；**X1b 的取数 / hash 面已不再被它卡住**（用的正是已冻结的 `market` 类型）。

**对 QT-40 正文前提的实测订正（不改写六字段正文，按台账惯例在此登记）**：

- **②「现状已实现约 80%」只对 connector 层成立**。aggregator 半边今天是空的：`DatasetSnapshotInput`（`tellurion:pkg/backtest/contracts/provenance.go:97-123`，该文件已于 S1 迁入 `tellurion-contracts:engine/provenance.go`）**零生产者**——全仓 grep 无任何构造点，只有定义与向量测试。X1b 交付的是**第一个生产者**，不是搬迁。
- **③-② 的四个错误码在代码里一个都不存在**（`ErrNoTimeSemantics` / `ErrUnknownDataType` / `ErrVisibleAtMissing` / `ErrSnapshotMismatch` 全仓 grep = 0 命中）。真实在执法的是 `tellurion:pkg/data/source` 的三个注册期哨兵 `ErrDeclarationMissing` / `ErrDeclarationInvalid` / `ErrDeclarationMismatch`（`contract.go:66-68`）。**`ErrNoTimeSemantics` 是 `ErrDeclarationMissing` 的旧名，作废**（见对表 G 组 T1）。
- **③-② 的 `ErrVisibleAtMissing` 有一条工单没写的代价**：现有已落库数据**没有承载 `visible_at` 的列**，这道门一上线历史行不可回填 ⇒ 必须与 X1d 的 schema 迁移同批设计（加列 + 按 `TimeSemantics` 规则重算，重算不了的处理要成文）。登记为对表 T5 —— **方案已于 2026-10-22 成文** = `docs/db-schema.md` §6（含一条工单没预见的覆盖面限制：`market_activity` / `quant.macro_series` 两个已声明语义的源**不走 BulkInsert**，门挂在 `ValidatePoints` 上拦不到它们）。
- **⑤ 三条验收的覆盖面要按对表收窄一条**：「改日历/复权口径 → hash 变」这条**已由 QT-03 的向量测试钉住**（`provenance_vector_test.go:194-228`，`DatasetSnapshotHash_UniqueDefinition` 逐分量 mutate），本仓 X1b 新增的判据是**生产者侧**的——六个入参（`universe` / `window` / `frequency` / `data_version` / `calendar` / `adjustment`）逐个 mutate 必须变 hash，测试已落。⚠️ **`as_of` 不在冻结六分量里**（2026-10-23 实现时确认，不是笔误），处置见本单「已登记的取舍与风险」第一条：缓存对同 hash 不同 `as_of` fail-closed。

### SVC-DATA-G1 · 服务就绪门 G1：版本化接口 · 错误模型 · 幂等 · 分页/限流（ADR-036）[P0] ✅（2026-11-07 真库腿补齐）

- **① 目标**：本仓对外接口满足 ADR-036 §3 的 G1 五件——版本化 API、错误模型、鉴权/授权、幂等、分页/限流——并各自有可执行判据。
- **② 上下文**：今天 4 条路由（`/health` + `/datasets/resolve` + `/datasets/{hash}` + `POST /ingest/points`），鉴权已落（`internal/httpapi/auth.go` = kit 适配、`policy.go` = 策略表，写端点档位只 `service` + `admin`）；**实测本仓无 openapi / swagger 文件**（版本化只剩 `docs/api-schema.md` 人读）；`/readyz` 全 workspace 代码 0 命中；分页与限流今天没有。跨仓前置：core 的 5 条旧读面（`/ohlcv/{symbol}`、`/api/v1/ohlcv/bulk`、`/screen`、`/market/index`、`/factors/{name}`）与本仓 4 条**不对齐**（材料 §2.6 ① / §5.2 K1），本单不擅自补齐它们。
- **③ 要求**：① `api-schema.md` 给每条端点写明版本与兼容策略（加字段 = MINOR、改语义 = MAJOR，按 C5 保留一个版本周期）；② 错误码闭合表：每个哨兵 → HTTP 码 + 报文形状逐条列出，**未列出的码视为缺陷**（今天只有零散映射）；③ 读端点要么加分页（`limit`/`cursor`），要么显式声明「整段返回」并给条数上界 + 超限行为；④ `POST /ingest/points` 的幂等语义成文（以 `dataprotocol.DeduplicateKey` 为键，重复投的返回码与首次一致）；⑤ 限流只需「不把自己打挂」的量级，但 **429 语义必须与 401 可区分**——core 侧 AUD-57 的教训：探针被限流读成「未登录」会把整站弹到登录页，故 `/health`、`/readyz` 必须豁免且有用例；⑥ 健康/就绪分离：`/health` = 进程活着，`/readyz` = PG 池可连 + 本仓 DDL 已执行。
- **④ 约束与非目标**：不实现 core 那 5 条旧面（**K1 已裁**，2026-11-08 补裁 #11：以本仓已登记路由为唯一数据服务面，core 的 5 条旧读面由 core 在 `SVC-DATA-G3` 逐条处置、本仓不补齐）；不引入 API 网关/新框架；不做多租户配额；不改 contracts pin（`v0.6.0`）；不在本单做物理 schema 搬迁。
- **⑤ 验收**：① `api-schema.md` 每条端点有版本行、错误码表逐条齐全（抽 2 条比对实现）；② 四条用例真跑：分页上界、429 与 401 可区分、探针豁免、幂等重投返回同码；③ `GET /readyz` 在 `DATABASE_URL` 为空 / PG 不可达时 **503**、正常时 **200**（各一条用例）；④ `go build/vet/test ./...` 单仓三条绿（贴真实退出码）。
- **⑥ 边界**：本仓接口面；core 改指本仓的切读归 `SVC-DATA-G3`，K1 的接口面对齐**已裁**（2026-11-08 补裁 #11：本仓路由是唯一数据服务面，5 条旧读面由 core 侧逐条反代或删除；**待主任追认**）。

> **施工记录（2026-11-07 · SVC-DATA-G1 落账；真库腿当晚补齐后状态 ✅）**
>
> 已交付（本仓）：
>
> - **`GET /readyz`**（`internal/httpapi/server.go`）：三态 `{status, store, error?}`——未注入 readiness
>   → 503 `not_ready/unwired`；检查失败 → 503 `not_ready/postgres` + 原因；通过 → 200 `ready/postgres`。
>   超时上界 3s。`cmd/dataservice/main.go` 的 `readinessCheck(pool)` = `pool.Ping` + `SELECT
>   to_regclass('market.ohlcv_daily_qfq') IS NOT NULL`（池连得上但建表没跑也不算就绪）。
> - **探针公开且豁免**：`/health`、`/readyz` 在 `servedPolicy` 标 `Public`，`Routes()` 直接注册，
>   不经过 `requireAuth` ⇒ 不鉴权、不限流。守卫测试覆盖「闸未装配时探针仍 200」与「真 kit 下无 token 探针不被 401」。
> - **限流**（`internal/httpapi/limit.go`）：按 `username` → `sub` → `open-access` 共用桶的令牌桶，
>   默认 50 rps / 突发 200；超限 429 `rate_limited` + `Retry-After: 1`；**鉴权先于限流**（`Authorizer`
>   改为返回 `principal`，限流只用它分桶，不参与鉴权结论）。`WithRateLimit` 可覆盖/关闭（测试用）。
> - **版本 / 错误 / 分页 / 幂等成文**：`api-schema.md` 新增 §1.1（探针表、三态表、限流参数、错误码闭合表口径、
>   分页声明、幂等重投语义）；§2 增 `rate_limited` 行；§4 写版本策略（v1 路径、MINOR/MAJOR 判据、双写窗口）；
>   头部实现状态与 frontmatter 同笔更新。
> - **用例**：`health_test.go` 三条（探针公开 + 就绪翻转 + 无闸可用；429 与 401 可区分 + 探针不占桶；
>   `empty_data_version` 409 钉码）；`points_test.go` 新增 `TestIngestPointsReplayReturnsSameCodeAndBody`；
>   路由守卫清单由 4 条改 5 条（点名 `GET /readyz`）；`cmd/dataservice/main_test.go` 的源码钉由
>   `httpapi.New(agg, authorizer, httpapi.WithPoints(points))` 改为分别钉 `WithPoints` 与 `WithReadiness`。
>
> 验收对照（⑤）：
>
> - ① 每条端点有版本与兼容策略 = ✅（§4）；错误码表逐条齐全 = ✅（§2 + `sentinelCodes` 9 条 + 写面
>   `assembly.WireError`，抽 `ErrEmptyDataVersion` 与实现比对）。
> - ② 四条用例：**分页上界** = ⚠️ 本仓无集合类读端点，按 ③ 的「显式声明整段返回 + 上界」兑现
>   （`/datasets/*` 单词数据集；`POST /ingest/points` ≤ 10000 点 / 8 MiB，超限用例
>   `TestIngestPointsRejectsBatchOverTheLimitOnTheWire` + `TestIngestPointsRejectsOversizedBody`）；
>   **429 与 401 可区分** = ✅；**探针豁免** = ✅；**幂等重投同码** = ✅（wire 级 + 2026-11-07 真库级，见下方真库腿）。
> - ③ `/readyz` 三态各有用例 = ✅（handler 级）；**真 PG 不可达 → 503、恢复 → 200** = ✅（2026-11-07 真库腿，见下）。
> - ④ `gofmt -l .` 空、`go build ./...` 退出 0、`go vet ./...` 退出 0、`go test ./... -count=1` 退出 0
>   （8 个包 ok：cmd/dataservice、assembly、httpapi、ingest、normalize、schema、serving、store）。
>
> 破坏验证（真做真还原，逐条贴红）：
>
> 1. 关掉限流（`requireAuth` 的 429 分支加 `&& false`）⇒ `TestRateLimitDistinctFromAuthAndProbesExempt`
>    红（expected 429 / actual 503），还原复绿。
> 2. 去掉「探针直接注册」（`Routes()` 的 `rule.Public` 分支加 `&& false`）⇒ 同一用例「probe 0」红
>    （expected 200 / actual 429）、`TestProbesArePublicAndReadinessFlips` 的无闸场景红
>    （/health 503、/readyz 503），逐字节还原复绿。
> 3. 把 `ErrEmptyDataVersion` 的映射从 409 改成 500 ⇒ `TestEmptyDataVersionMapsToConflict` 红
>    （expected 409 / actual 500），还原复绿。
>
> 未执行 / 订正口径（如实登记）：
>
> 1. **真 PG 两级腿（2026-11-07 晚已补齐，本单翻 ✅）**：便携 PostgreSQL 16.2 @ `127.0.0.1:55432`、库 `tellurion_data_test`，真二进制 `cmd/dataservice`（`DATA_MARKET_SCHEMA=market`）：
>    - **200**：`GET /readyz` → `200`，body `{"status":"ready","store":"postgres"}`（`pool.Ping` + `to_regclass('market.ohlcv_daily_qfq')` 均通过）；`GET /health` → `200`。
>    - **停库 ⇒ 503**：`pg_ctl -D ... stop -m fast`（`STOP_EXIT=0`）后 `GET /readyz` → `503`，body `{"status":"not_ready","store":"postgres","error":"analysis: ping postgres: write failed: write tcp 127.0.0.1:…->127.0.0.1:55432: wsasend: An existing connection was forcibly closed by the remote host."}`；同刻 `GET /health` 仍 `200`（健康/就绪分离）。
>    - **恢复 ⇒ 200（同一进程自愈）**：`pg_ctl start`（`START_EXIT=0`）后 `GET /readyz` → `200`；服务日志逐秒可查：`08:21:00 status=200` → `08:21:15…08:21:23 status=503` → `08:21:24 status=200`，进程未重启。
>    - **幂等/冲突（真库）**：`POST /ingest/points` 同内容两次均 `200 {"received":1,"persisted":1,"skipped":0}`；改 `close` 后同主键 → `409 {"error":{"code":"content_conflict","message":"store: row exists with different content for the same primary key: point[0] 600519.SH ohlcv_daily"}}`。
>    - **口径订正（实测推翻本单原先的「skipped」期待）**：`persisted` 的语义是「请求点已就位」（新插入 + 同内容 no-op），`skipped` 只数**载荷不合法**被跳过的行（`internal/store/write.go:266-268`）。所以真实重放的返回是 `persisted=1/skipped=0`，**不是** `skipped=1`；⑤② 只承诺「重投返回码与首次一致」，该承诺成立（200 vs 200）。
> 2. **幂等键口径与票面不同**：③④ 写「以 `dataprotocol.DeduplicateKey` 为键」，而本仓写面实际是
>    **行主键 + 全列内容比对**（§3；`DeduplicateKey` 是 core 传输侧对单批点的去重键，不是库级冲突判定）。
>    两者目的相同（重放不产生新行、不报错），但**不是同一个键**；不另造第二套去重，按 §3 既有语义成文。
> 3. 未做多租户配额、未引 API 网关、未改 contracts pin —— 按 ④ 约束本来就是非目标。

### SVC-DATA-G2 · 服务就绪门 G2：自有 schema 的唯一 writer 与可重放迁移（ADR-036）[P0] ✅（2026-11-07 真库重放腿补跑通过）

> **2026-11-07 真库腿补跑（✅）**：起便携 PostgreSQL 16.2（`127.0.0.1:55432`，不占默认 5432），
> `DATA_TEST_DSN='postgres://postgres@127.0.0.1:55432/tellurion_data_test?sslmode=disable' go test ./internal/schema -run TestSchemaReplay_TwoRunsLeaveShapeUnchanged -v -count=1`
> → `--- PASS: TestSchemaReplay_TwoRunsLeaveShapeUnchanged (0.31s)`、退出码 0（两次指纹比对在用例内断言，**不是 skip**）；
> 同一 DSN 下 `go test ./... -count=1` 退出码 0。⑤① 的两次指纹输出已取得；②③④ 沿用本节原施工记录。
> 环境口径：代码零改动，端口由 `DATA_TEST_DSN` 注入，端口约定里的 5432 未变。

- **① 目标**：`market` / `ingest` 的 DDL 归属在本仓**自证**——迁移可重复执行、不跨 schema 写、每张表「谁写」有答案。
- **② 上下文**：`internal/schema` 持 23 张 DDL（`market` 19 + `ingest` 4，`TableNames()` 可对账），`cmd/dataservice` 在 `DATABASE_URL` 有值时启动执行；**今天仍是双 owner**——core 侧写点未归零（本仓台账 X1d 行自证「其余同步腿仍在 core 写」；core 侧删除单 = `tellurion:SVC-CORE-DATA-RETIRE`）；`deploy/compose.yaml:32` 出厂 `DATA_MARKET_SCHEMA: market`，而 core 用**裸表名**写 `public.*`（core 仓内无 `search_path`）⇒ 两种读法指向的不是同一批表，这条口径必须钉死；`visible_at` 回填只在测试库跑过（`-tags backfill` + `DATA_TEST_DSN`，库名须以 `_test` 结尾）；材料 §5.2 K3 列出 **10 张「有表无写者」**（market 侧 7 + ingest 侧 3）。
- **③ 要求**：① 迁移重放自证：同一库连跑两次全部 DDL，断言行数 / `pg_indexes` / 列集指纹**逐位不变**，这条腿做成默认可跑（无库时 skip，不是假绿）；② 禁跨 schema 写护栏：新增 repoguard 静态扫本仓 SQL，出现 `quant.` / `public.` / 其他仓 schema 的写语句即红；③ K3 那 10 张逐张给处置（补写者 / 显式声明「只由 core 写，本仓不拥有」 / 弃表），处置写进 `docs/db-schema.md`；④ 把 `DATA_MARKET_SCHEMA` 取值口径与「物理 `SET SCHEMA` 搬迁的前置 = core 停写」成文。
- **④ 约束与非目标**：不执行物理搬迁；不 `DROP` 任何表或索引；不改 core 的写点（那是 `SVC-CORE-DATA-RETIRE` ③ 的串行序）；不碰生产库（护栏与重放腿只连 `_test` 库）；`market.index_daily` 按裁决 ⑰ 属 core，不许顺手接管。
- **⑤ 验收**：① 重放腿两次指纹逐位相同（贴两次输出）；② 破坏验证：往本仓任一 SQL 常量加一行 `INSERT INTO quant.foo` ⇒ 护栏红，逐字节还原复绿；③ K3 的 10 行每行有出处与处置，`db-schema.md` 与之一致；④ 三条命令单仓绿。
- **⑥ 边界**：本仓 schema 自证；「旧写者归零 + 旧代码删除」= core 侧退役单 + 本仓 `SVC-DATA-G3` 两件事，不在本单。

**施工记录（2026-11-07）**

- **产物**：
  - ③① 重放腿：`internal/schema/replay_pg_test.go`（`TestSchemaReplay_TwoRunsLeaveShapeUnchanged`；临时 schema 全量跑两遍，比 `rows` / `information_schema.columns` 列集 sha256 / `pg_indexes` sha256；无 `DATA_TEST_DSN` 即 `t.Skip`，设置了但连不上 fail-loud）。
  - ③② 护栏：`internal/repoguard/guard.go` + `guard_test.go`（AST 扫生产 `.go` 的 SQL 字符串常量；写语句只许 `market.` / `ingest.` / `pg_catalog` / `information_schema` / 临时表，`SELECT` 读上游不拦；唯一登记过渡 = `visible_at` D1 的 `ALTER TABLE IF EXISTS public.` 动态前缀）。
  - ③③/③④ 文档：`docs/db-schema.md` §7（K3 十张逐张处置）、§8（`DATA_MARKET_SCHEMA` 口径与物理搬迁前置）。
- **门禁（真实退出码）**：`go build ./...` = **0**；`go vet ./...` = **0**；`go test ./... -count=1` = **0**（9 个包 `ok`）；`gofmt -l` 三个改动/新增 Go 文件 = **空**（退出 0）。DDL 表数复核 = **23**（`TableNames()`：market 19 + ingest 4），与票面一致。
- **破坏验证（真做真还原）**：往 `internal/schema/schema.go` 临时追加 `var guardProbeSQL = "INSERT INTO quant.foo (a) VALUES (1)"` ⇒ `go test ./internal/repoguard -run TestNoCrossSchemaWritesInProductionSQL -v` **FAIL（exit 1）**，报 `internal\schema\schema.go:63: INSERT INTO quant.foo（写语句指向仓外 schema quant）`；用备份逐字节还原后 `sha256` 前后一致 —— before/after 均 `a339a588f4457da393bd0237d78e22c3338803e99885159572734a017ff8527d`（破坏态 `587e0105778839ef7a3ebfa23f79938c4cf0d8f36aec6be4a196b15e43b6be65`），复跑 **PASS（exit 0）**。
- **未执行项（当日早先状态；① 已于 2026-11-07 晚补齐，见本节顶部记录）**：① ~~真 PG 重放两次指纹未执行~~ → **已执行**（`--- PASS: TestSchemaReplay_TwoRunsLeaveShapeUnchanged (0.45s)`，两次指纹在用例内逐位比对，退出码 0）；② 真库夹具（完整 DDL 首跑/二跑、`ingest.data_migrations` 哨兵行）已随 ① 一并真跑；③ K3 各写点在运行期是否仍被调用（如 executor 是否已被 feature flag 关掉）= **未取证**，只取证「core 有字面写点」与「本仓无写者」（跨仓取证，不属本单验收）。
- **K3 处置一览**（9 补写者 + 1 core-only + 0 弃表）：`stocks` → 补写者；`dividends` → 补写者；`splits` → 补写者；`trading_calendar` → 补写者；`index_constituents` → 补写者；`market.index_daily` → **显式声明只由 core 写、本仓不拥有**（裁决 ⑰）；`market_activity` → 补写者；`ingest.raw` → 补写者；`sync_jobs` → 补写者；`sync_schedules` → 补写者。逐行 core 写点见 `docs/db-schema.md` §7。

### SVC-DATA-G4 · 服务就绪门 G4：失败语义 · 超时重试 · 健康就绪分离 · runbook（ADR-036）[P1] ✅（2026-11-07 真库腿补齐）

- **① 目标**：本仓作为服务**可运维**——依赖失败时说什么、超时多久、怎么重启与排查，全部成文并被真跑过一次。
- **② 上下文**：ADR-036 §3 的 G4 判据 = 「超时/重试/熔断、失败语义、日志/指标/追踪、回滚」+「故障演练 + runbook + 健康/就绪分离」；**全 workspace 实测 `runbook` 只出现在 ADR-036 自己的正文里**，六个服务仓一份都没有 ⇒ G4 此前无人认领（本组六仓的 `SVC-*-G4` 是同一件事的六个实例，各自只写自己仓的依赖面）；本仓今天只有 `/health`，`DATABASE_URL` 为空时数据端点 503（fail-closed 已写进 `api-schema.md`），出站传输由 core 注入（`Transports` 窄接口），本仓侧没有超时上界的文档。
- **③ 要求**：① 失败语义表：PG 不可达 / 数据源配额耗尽 / connector 超时 / 内容冲突（`ErrContentConflict`）四类，各写明「HTTP 码、是否重试、是否落痕、调用方该做什么」；② 超时与重试边界（**包括「本仓不重试、由作业侧重试」这类不做的理由**也要成文）；③ 可观测最小面：每端点错误率与延迟，或显式声明「单人自托管不接 Prometheus」并写明故障时用什么定位（日志 + 一条命令）；④ 新建 `docs/runbook.md`：起停、看日志、重放迁移、`visible_at` 回填操作、503 与投递失败的排查路径；⑤ 一次故障演练记录：拔掉 PG ⇒ 端点 503 且不 panic、不静默成功，恢复后自愈。
- **④ 约束与非目标**：不引 APM / SaaS / OTel collector；不做高可用与多副本；不在本单接真数据源传输（仍在 core）；不改 `/health` 既有语义（`/readyz` 属 `SVC-DATA-G1` ⑥）。
- **⑤ 验收**：① runbook 每条命令可复制、当场执行过并贴真实输出；② 演练记录有真退出码（拔 PG 前后各一次）；③ 失败语义表四类齐全，每条有对应用例或写明「无用例的原因」；④ 三条命令绿。
- **⑥ 边界**：本仓运维面；core 的聚合 compose 与 `cmd/worker` / `cmd/dualrun` 部署形态归 `tellurion:QT-79`。

> **施工记录（2026-11-07 · SVC-DATA-G4 落账；真库腿当晚补齐后状态 ✅）**
>
> 已交付（本仓）：
>
> - **PG 不可达 → 503**：新增 `internal/store/availability.go` 的哨兵 `ErrDependencyUnavailable` 与
>   `classifyDependency`——只把连接层失败（`08` 类、`57P*`、`57014`、`*pgconn.ConnectError`、超时、
>   `net.Error`、`io.EOF`）折成哨兵；SQL 语义错误仍走 500。读面经 `pgxQuerier.Query`/`rows.Err()`，
>   写面经 `pgxWriteDB.BeginTx`/`Exec`/`Compare Scan`/`Commit` 分诊；HTTP 映射在
>   `httpapi.sentinelCodes`（读）与 `assembly.WireError`（写），码 `503 dependency_unavailable`。
> - **降级启动 + 自愈**：`store.Open` 改为**建池不拨号**（拨号 5s、语句 30s 由
>   `connect_timeout` / `statement_timeout` 兜住）；`cmd/dataservice` 的 DDL 失败不再 `Fatal`，
>   数据端点由 `httpapi.WithAvailability(atomic.Bool)` 立即 503，后台每 10s 重放 `schema.Statements()`，
>   成功后同进程翻正。
> - **超时边界**：`http.Server` 5s/30s/120s/120s（ReadHeader/Read/Write/Idle）；探针 3s；
>   启动 DDL 30s；PG 拨号 5s、语句 30s。出站 connector 不在本仓（core），已在文档写明。
> - **可观测最小面**：`httpapi.withAccessLog` 一行一请求，固定
>   `route=<模式> status=<int> dur_ms=<float>`；显式声明**不引 APM/OTel/Prometheus、无 `/metrics`**，
>   聚合命令与真实输出见 `docs/runbook.md` §2。守卫：`TestAccessLogLineCarriesRouteStatusAndDuration`、
>   `TestAccessLogDurationMeasuresSlowHandler`。
> - **文档**：`docs/api-schema.md` 新增 §1.2（可观测）、§2 哨兵行、§2.1（四类失败语义表）、§3.1（超时与
>   重试边界 + 「本仓不重试、由作业侧重试」的三条理由）；新建 `docs/runbook.md`（起停、看日志/聚合、
>   重放迁移、`visible_at` 回填、503 与投递失败排查、演练记录、未执行清单）。
>
> 验收对照（⑤）：
>
> - ① runbook 每条可复制、当场执行并贴真实输出 = ✅（可执行腿全跑；**2026-11-07 晚补跑 §3 重放、§4 回填、§5 真库 409**，
>   输出已贴回 runbook；Docker 容器腿仍标「未执行」）。
> - ② 演练记录真退出码 = ✅：拔 PG 形态 4 次 `curl` 均 `exit=0`、HTTP 码 200/503/503/503，进程存活且 `panic_lines=0`；
>   基线配置重起同样 4 次 `exit=0`。**2026-11-07 晚真 PG 演练**：`/readyz` 200 →（`pg_ctl stop`，`STOP_EXIT=0`）503
>   →（`pg_ctl start`，`START_EXIT=0`）200，**同一进程自愈**，日志 `08:21:00 200 → 08:21:15…23 503 → 08:21:24 200`。
>   **进程本身由 `Stop-Process` 强杀，Windows 不返回退出码（`[]`），已如实记录**；优雅停机未实现（不在本单）。
> - ③ 四类失败语义齐全 = ✅：PG 不可达/内容冲突有 HTTP 用例，配额耗尽/connector 超时有单测且写明
>   「HTTP 面无用例及原因：没有路由触发取数」。
> - ④ 门禁：`gofmt -l .` 输出空、退出 0；`go build ./...` 退出 0；`go vet ./...` 退出 0；
>   `go test ./... -count=1` 退出 0（9 包 ok：cmd/dataservice、assembly、httpapi、ingest、normalize、
>   repoguard、schema、serving、store）。
>
> 真实演练（2026-11-07 05:37，`DATABASE_URL` → `127.0.0.1:5999`，无监听）：
>
> - `/health` 200；`/readyz` 503 `postgres unreachable ... connection refused`；
>   `GET /datasets/resolve` 与 `POST /ingest/points` 均 503 `dependency_unavailable`；
> - 进程 11s 后仍存活，`panic_lines=0`；05:37:29 打出
>   `schema retry failed (data endpoints stay 503): ... connection refused`；
> - 回指基线（`DATABASE_URL` 为空）重起：`/health` 200、`/readyz` 503 `unwired`、数据端点 503，未 panic。
>
> 破坏验证（真做真还原）：
>
> 1. 改坏 `dependencyReady` 的判据（`if false && s.available != nil && !s.available()`）⇒
>    `TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal` 红：
>    `expected: 503 / actual: 200`（正是「静默成功」事故形态），退出码 1。
> 2. `Copy-Item` 逐字节还原 `internal/httpapi/server.go`：还原前/后 sha256 均为
>    `C9DB9BA4DC8BF6BF82C0D9F6DC79D7CD20C57767A62F6603B06B3E37C4B7FDB5`；复跑同用例绿（退出 0）。
>
> 未执行 / 订正口径（如实登记）：
>
> 1. **真 PG 恢复后同一进程自愈** = ✅（2026-11-07 晚真库演练：200 → 503 → 200，进程未重启；日志三段时间戳见上）。
>    HTTP 层自愈仍由 `TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal` 覆盖。
> 2. **真库 DDL 重放两次、`visible_at` 回填、409 真主键复现** = ✅（2026-11-07 晚）：
>    - 重放：`DATA_TEST_DSN=…/tellurion_data_test go test ./internal/schema -run TestSchemaReplay_TwoRunsLeaveShapeUnchanged -count=1` → `PASS`（两次指纹一致，详见 SVC-DATA-G2 记录）。
>    - 回填：`DATA_TEST_DSN=… go test -tags backfill ./internal/schema -run TestBackfillSuite_TestDatabaseOnly -v -count=1` → `PASS`（A 集 12 张逐档取值、哨兵防重放、B/C 集不动）。
>    - 409：真服务 `DATA_MARKET_SCHEMA=market` 下 `POST /ingest/points` 同内容两次 → 均 `200 {"received":1,"persisted":1,"skipped":0}`；同主键改 `close` → `409 {"error":{"code":"content_conflict",…}}`。
> 3. **进程真实退出码**未取得：`Stop-Process` 强杀在 Windows 不返回 ExitCode；优雅停机不在本单（非目标）。
> 4. **`dur_ms` 分辨率**：本机单调时钟最小非零间隔约 0.5ms，快请求记 `0.000000`（= <0.5ms），
>    已在 `api-schema.md` §1.2 与 runbook §2 写明，未把 0 当异常。

## 施工序（候选单，开工前必须展开成六字段）

> 这张表是**施工顺序**，不是任务台账：每条在执行前要展开成六字段单（目标/上下文/要求/约束/验收/边界），并写明冻结契约的路径与节号。
> 顺序有依赖，**不要跳级**：跳序的代价是返工（例：X1a 的协议没冻结就写 aggregator，等于把协议写死在一个服务里）。
> 2026-10-21：候选单已展开为「一、本仓任务」的正式六字段条目（表中「上游单号」指向的 QT 单正文现在就在本仓）。

| 阶段 | 内容 | 上游单号 | 前置 |
|---|---|---|---|
| X1a | 协议冻结进 contracts（`UnifiedDataPoint` / `TimeSemantics` / 四类错误码） | `QT-40` | ✅ 本仓立项（2026-10-25 已收编 `v0.6.0`） |
| X1b | aggregator：PIT 过滤 + 频率聚合 + 票池解析 + `dataset_snapshot_hash` | `QT-40` | X1a |
| X1c | 12 个 connector 收口到 `ConnectorDeclaration`，漏声明即拒收 | `QT-40` | X1b |
| X1d | `market` / `ingest` schema 归属迁移，消灭第二写者 | `QT-40` | X1c |
| X0 | `dataset_snapshot_hash` 记入回测结果 | `QT-03` | X1b |

## 二、跨仓指针（不在本仓，勿双写）

| 单号 | 主题 | 在哪 |
|---|---|---|
| QT-40 | X1 数据服务：统一协议 + aggregator + `dataset_snapshot_hash` | 已迁入本仓「一、本仓任务」（2026-10-21；core `tellurion:docs/TASKS.md` 留同号指针） |
| QT-08 | 本仓 CI / AGENTS / 台账由该单一并补齐 | `tellurion:docs/TASKS.md` |
| SPEC-R-02 | 五张无主表 owner 认领——`factor_hypothesis` / `factor_returns` / `ic_analysis` 归本仓因子域（已补 `docs/db-schema.md` §1.3） | `tellurion:docs/TASKS.md` §九 |

---

## 维护本文件

- **新增单**：写全六字段；六字段里必须显式引用**冻结契约的路径与节号**（契约仓的包路径、设计文档的节号），不许写"参考相关实现"。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
