---
status: active
last-verified: 2026-10-25
verified-by: W4 鉴权接线（kit net/http 适配 + 3 路由策略表 + REV-17 逐条移植）；D4-1 裁-14 撤因子读面
---

# TASKS — tellurion-data 未完成项

> 本仓**唯一**任务台账。跨仓任务只在 §二 留指针，**不镜像正文**（双写必然漂移）。
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `✅ 完成（附日期）`
> 每张单六字段（① 目标 ② 上下文 ③ 要求 ④ 约束与非目标 ⑤ 验收 ⑥ 边界），判据见 `tellurion:AGENTS.md` §8.5。
> 完成即标 ✅（附日期）并移入 `docs/archive/TASKS-YYYY-MM-DD-archived.md`；本文件只留未完成项。

---

## 一、本仓任务

> **约定变更（2026-10-21）**：原「占位仓不写正式六字段任务单」作废——X 线任务单已展开为本仓正式任务（见下）。M5/M8 开工前置不变；实现仍未开工，开工前仍须过前置两条。（2026-10-23：实现已开工，见本单下的进度块。）

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
| X1b aggregator | 🔶 取数面 + 写面代码已落（2026-10-24） | `internal/serving`：`Resolve`（参数校验 → 票池解析 → 取数 → 聚合 → PIT → 六分量 hash → 强幂等缓存）、`Snapshot`（重算 hash + 内容指纹自证）、`Metadata` 回显同源校验；`internal/normalize`：`Aggregate`（1m→15m/1d，会话内对齐、不跨午休、秒级边界）与 `VisibleAsOf`（`visible_at <= as_of`，缺可见时间点整批拒收）；`internal/httpapi`（resolve/snapshot/health，哨兵→状态码映射）+ `cmd/dataservice`；**四个 port 已有 PG 实现** `internal/store`（`DATABASE_URL` 有值即装配；空则 503）；**G4 新增**：`internal/ingest` 的 `ETLPipeline` / `Registry.Fetch` / `ValidatePoints` / `GatePoints` / `Deduplicate` 与 `internal/store.BulkInsert` 写面 | ~~`/factors/{name}@{hash}` 只有路由、恒 404~~ **已撤（裁-14，2026-10-25）**：因子读面只在 core，本仓路由表不含 `/factors/*`；**写面装配未接**：同步作业（`sync_jobs` → worker → `ETLPipeline`）仍在 core；`visible_at` 只从列取、未回填的行会被 PIT 整批拒收（生产 D2 未执行前的预期行为） |
| X1c connector 声明 | 🔶 connector 已搬（2026-10-24） | **12 个 connector 的取数/归一化已搬入** `internal/ingest`（`declarations_*.go` 各自持声明；运行时 9 方法面在 `adapter.go`）；**过渡期 `Catalog()` 双真相已删除**，装配改 `Connectors(Transports)`；`TushareTransport` / `MootdxTransport` 窄接口把传输留在 core（本仓不 import 实现包）。测试跑在真实 connector 上（18 条声明 / 15 个 data_type；SupportedTypes ↔ Declarations 一一对应） | **传输实现仍在 core**（Tushare/Eastmoney/Mootdx 的 HTTP/SDK 客户端与 raw 归档），本仓只按 `Transports` 注入；core 侧 `pkg/data/source` 的旧实现未删（跨仓切换不在本仓权限内）。**写面已归位**（2026-10-24，G4）：`internal/ingest.ETLPipeline` + `internal/store.BulkInsert`；但**同步作业的装配仍在 core**（`sync_jobs` → worker → ETL），core `pkg/data/source/etl.go` 的旧写路径尚未删 |
| X1d schema 归属 | 🔶 DDL 归属 + 写面已落（2026-10-24） | `internal/schema`：`market` / `ingest` 建表（19 张，`TableNames()` 可对账）、`visible_at` 三集合（A 集 12 张 × 目标/今天两侧、B 集承认 `as_of`、C 集逐条豁免、`GateClassFor` 分类）、哨兵防重放的回填腿（`BackfillStatements()`，与建表分开返回）；DDL 在 `DATABASE_URL` 有值时由 `cmd/dataservice` 启动执行。**G4 写面**：`internal/store.BulkInsert` 只开 A 集（B/C 集拒写）、事务前 `visible_at` 判零整批拒收、`ON CONFLICT DO NOTHING` + 内容比对（同内容 no-op / 异内容 `ErrContentConflict` 整批回滚） | **物理搬迁**（`SET SCHEMA` 到 `market` / `ingest`）未做——core 内联 DDL 仍是运行期事实；**回填只在测试库执行过**（2026-10-24，tagged 套件 `-tags backfill` + `DATA_TEST_DSN`），**生产未回填**；**第二写者未真正消灭**：core 侧同步腿仍在写 `market` / `ingest`，且仍用 `DO UPDATE`（与本仓内容比对语义并存，见 ADR-0004 代价）；**写面装配未接**：`ETLPipeline` 尚未挂到本仓同步作业（作业层仍在 core） |

**施工记录（2026-10-24，G9 四步串行；`go build/vet/test ./...` 全绿）**

| 步 | 结果 | 提交（本仓 main） |
|---|---|---|
| 1 · X1d schema 归属 | 🔶 DDL 归属已落：`internal/schema`（19 张表 / A 集 12 张 `visible_at` 两侧加列 / B 集承认 `as_of` / C 集逐条豁免 / 哨兵回填腿）。**禁 `SET visible_at = trade_date` 有测试钉住** | `9e68de6` |
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

**未落项（如实登记，不写成已完成）**

- **第二写者未消灭**：core 侧同步腿仍在写 `market` / `ingest`；本仓只能先立 owner（DDL）与写闸门。物理 `SET SCHEMA` 搬迁（D4）未做。
- **`visible_at` 生产未回填**：D2 已**只在测试库**经 tagged 套件真跑（`internal/schema/backfill_pg_test.go`，`DATA_TEST_DSN` + 库名 `_test`）；生产库回填仍须显式启动开关或运维执行，**未做**。未回填的行会被 PIT 整批拒收（生产 D2 前序红线：D2 未完成期间读路径不许按 `visible_at` 过滤）。
- **传输未搬**：Tushare / 东财 / Mootdx 的 HTTP/SDK 客户端与 `ingest.raw` 归档仍在 core；本仓按 `Transports` 注入。
- **写面代码已落、装配未接**：`ETLPipeline` 与 `store.BulkInsert` 在本仓（2026-10-24，G4），但**同步作业层仍在 core**（`sync_jobs` → worker → ETL），本仓没有作业装配/调度；core `pkg/data/source/etl.go` 的旧写路径尚未删，且仍用 `ON CONFLICT DO UPDATE`（与 ADR-0004 的内容比对语义并存）。
- ~~**`/factors/{name}@{hash}` → 裁-14（2026-10-25）**~~ **✅ 已落（D4-1，2026-10-25，本笔）**：路由、`handleFactor`、`ErrFactorNotFound` 与恒 404 占位全部撤掉，不做表迁移。原因：生产方与读端点都在 core（`pkg/data/factor_formula.go` / `factor_equitydeep.go` → `SaveFactorCacheBatch`；读面 `cmd/data/handlers_factor.go`），SPEC 把 C 类派生计算（因子 / 回测 / IC）列为本仓非目标；常驻 404 比没有路由更坏——调用方会把 404 读成「该数据集没有该因子」。**因子读面明确由 core 提供**，本仓 openapi / api-schema 同步删除该路径。**上移触发条件（任一成立再启动整体迁移）**：① C 类生产需要横向扩容；② 出现第二个不接受 core API 的消费方。
- ~~**鉴权未移植（裁-15，2026-10-25 已定，可开工）**~~ **✅ 已落（2026-10-25，W4）**：新 `dataservice` 已接 kit `v0.1.1` 的 net/http 适配；`servedPolicy` = `/health` Public + `/datasets/*` viewer；旧 `cmd/data` 的 REV-17 表 28 条逐条移植为 `legacyREV17`（端点迁入时直接挂闸）；路由表 ≡ 策略表守卫 + 三态测试已落。**未落**：REV-17 对应的端点和真实保护仍在 core `cmd/data`（本仓只持迁移表）；core `proxyRequest` 已转发 `Authorization`（2026-10-25）。
  - **服务间凭证（裁-17 定稿，2026-10-25）**：`servedPolicy` 的 `GET /datasets/resolve`、`GET /datasets/{hash}` 两条 Roles 需加 **`"service"`**（调用方是 core / analytics，用 `service` 角色长期 token，不是 `trader`）；`/health` 保持 Public。部署清单给 `data-service` 配 `JWT_SECRET` 与调用方 token 属 core QT-71 腿。
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
