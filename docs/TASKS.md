---
status: active
last-verified: 2026-10-22
verified-by: QT-40 对表 T2~T5 收尾（四条待办的落点与裁出项逐个登记；CT-02 状态按 contracts `191ba0b` 实测改 ✅）
---

# TASKS — tellurion-data 未完成项

> 本仓**唯一**任务台账。跨仓任务只在 §二 留指针，**不镜像正文**（双写必然漂移）。
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `✅ 完成（附日期）`
> 每张单六字段（① 目标 ② 上下文 ③ 要求 ④ 约束与非目标 ⑤ 验收 ⑥ 边界），判据见 `tellurion:AGENTS.md` §8.5。
> 完成即标 ✅（附日期）并移入 `docs/archive/TASKS-YYYY-MM-DD-archived.md`；本文件只留未完成项。

---

## 一、本仓任务

> **约定变更（2026-10-21）**：原「占位仓不写正式六字段任务单」作废——X 线任务单已展开为本仓正式任务（见下）。M5/M8 开工前置不变；实现仍未开工，开工前仍须过前置两条。

### QT-40 · X1 数据服务（统一协议 + aggregator + `dataset_snapshot_hash`）[P1] ⬜

> **迁入说明（2026-10-21）**：本单自 core 仓 `tellurion:docs/TASKS.md`「五、X 线」迁入（任务台账批量重分布：X 线单展开到各服务仓为正式六字段单，「占位仓不写正式任务单」旧约定同时作废）。六字段正文逐字保留、不改写；core 侧同号条目已改为指针。

- **① 目标**：数据协议冻结进 contracts；aggregator 能给出可复现的 `dataset_snapshot_hash`；connector 全部走声明式契约。
- **② 上下文**：`data-service-contract-and-ownership.md`（D19）X1a~X1d；现状已实现约 80%（12 connector + `ConnectorDeclaration` + `ETLPipeline`/`Normalizer`/`ValidatePoints`/`Deduplicate`；PG 已有 ingest/market/quant/research）。
- **③ 要求**：① 协议类型（`UnifiedDataPoint`/`OHLCV`/`FrequencySpec`/`TimeSemantics`）进 QT-05 的 contracts；② `ErrVisibleAtMissing` 等错误码；③ aggregator 面（PIT 过滤 + 频率聚合 + 票池解析 + 快照 hash + 缓存）；④ `dataset_snapshot_hash` 按 QT-03 的唯一定义实现（含 `calendar_hash`/`adjustment_basis`）；⑤ 12 connector 全走 Declaration + 共享 Normalizer。
- **④ 约束与非目标**：不重写现有 connector；不在内核内保留第二写者；不做跨库拆分。
- **⑤ 验收**：同一 hash 两次拉取返回一致；漏声明 `visible_at` 的源被拒收；改日历/复权口径 → hash 变。
- **⑥ 边界**：只做数据面；策略/风控/分析各自的 schema 见后续单。


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
> 收尾后本单的**直接**硬前置只剩 `QT-05` 一条（下表第一行）；W0 那两条是**间接**（卡的是 QT-05），`CT-02` 已落、不再挂账。③-① 的「`TimeSemantics` 进 contracts」也要按 T2 的口径读：它随 `domain/market` 整包迁，**不在 `dataprotocol` 里重复定义**。

**仍未解除的硬前置（决定 QT-40 什么时候真能动代码）**：

| 前置 | 状态 | 卡住的是 |
|---|---|---|
| `QT-05`（S1 契约层落码：`pkg/domain`（含 `domain/market`）+ `pkg/backtest/contracts` 进 contracts） | ⬜ 未启动（`tellurion-contracts:docs/TASKS.md` QT-05；contracts 仓今天只有 `doc.go` + 两个自查工具 `tools/check_api_surface` / `tools/check_release_matrix`，**零导出符号**，core `go.mod` 对 contracts 零 `require`） | **X1b 全部**。本仓 module path 是单段短名（`tellurion-data`），按编译器规则无法 `require` core，又禁止 `replace ../` —— 协议类型拿不到，aggregator 就无法落地。跳序的代价工单施工序已点过一次：「X1a 的协议没冻结就写 aggregator，等于把协议写死在一个服务里」 |
| `QT-04` / `QT-63`（W0 剩下的两条） | ⬜ / ⬜（core 台账 2026-10-22 实测：`QT-04` 行仍 ⬜、`QT-63` 行仍 ⬜；同表 `QT-02` 已 **✅（2026-10-21）**，main 上五笔 fill_timing 提交 `f615e8e`→`9ffff72`→`676eab1`→`ee93174`→`271273a` 逐个 `git log -1` 核过） | QT-05 排在「W0 全绿之后」（core 执行顺序总览 S1 行），因此**间接**卡住本仓——它卡的是 QT-05，不是本仓的代码 |
| `CT-02` / `QT-19`（contracts 版本规则表 + 兼容矩阵 + CHANGELOG） | ✅ **已落**（2026-10-22，contracts `191ba0b`：README 的版本规则表 + 兼容矩阵 + `CHANGELOG.md` + `check_release_matrix` 进 CI；`QT-19` 那条**排在 `QT-07`（S2 打 tag）之后**，所以矩阵今天只有 `main`（未发版）一行是**预期**，不是缺项） | 不卡施工，卡**口径**：版本规则只认 contracts README 那张表 + `tellurion:docs/TASKS.md` QT-19 ③ 那五条（0.x 期允许 breaking，但要 bump MINOR + 写 CHANGELOG），**本仓不许自造版本口径**。对表 F 组与 §2.1 的引用已对上 |

**对 QT-40 正文前提的实测订正（不改写六字段正文，按台账惯例在此登记）**：

- **②「现状已实现约 80%」只对 connector 层成立**。aggregator 半边今天是空的：`DatasetSnapshotInput`（`tellurion:pkg/backtest/contracts/provenance.go:97-123`）**零生产者**——全仓 grep 无任何构造点，只有定义与向量测试。X1b 交付的是**第一个生产者**，不是搬迁。
- **③-② 的四个错误码在代码里一个都不存在**（`ErrNoTimeSemantics` / `ErrUnknownDataType` / `ErrVisibleAtMissing` / `ErrSnapshotMismatch` 全仓 grep = 0 命中）。真实在执法的是 `tellurion:pkg/data/source` 的三个注册期哨兵 `ErrDeclarationMissing` / `ErrDeclarationInvalid` / `ErrDeclarationMismatch`（`contract.go:66-68`）。**`ErrNoTimeSemantics` 是 `ErrDeclarationMissing` 的旧名，作废**（见对表 G 组 T1）。
- **③-② 的 `ErrVisibleAtMissing` 有一条工单没写的代价**：现有已落库数据**没有承载 `visible_at` 的列**，这道门一上线历史行不可回填 ⇒ 必须与 X1d 的 schema 迁移同批设计（加列 + 按 `TimeSemantics` 规则重算，重算不了的处理要成文）。登记为对表 T5 —— **方案已于 2026-10-22 成文** = `docs/db-schema.md` §6（含一条工单没预见的覆盖面限制：`market_activity` / `quant.macro_series` 两个已声明语义的源**不走 BulkInsert**，门挂在 `ValidatePoints` 上拦不到它们）。
- **⑤ 三条验收的覆盖面要按对表收窄一条**：「改日历/复权口径 → hash 变」这条**已由 QT-03 的向量测试钉住**（`provenance_vector_test.go:194-228`，`DatasetSnapshotHash_UniqueDefinition` 逐分量 mutate），本仓 X1b 新增的判据是**生产者侧**的——即「resolve 传进来的 `as_of` / `adjustment` 参数确实进了那六分量」，否则 hash 函数再对，喂进去的是常量也照样同 hash 不同结果。

## 施工序（候选单，开工前必须展开成六字段）

> 这张表是**施工顺序**，不是任务台账：每条在执行前要展开成六字段单（目标/上下文/要求/约束/验收/边界），并写明冻结契约的路径与节号。
> 顺序有依赖，**不要跳级**：跳序的代价是返工（例：X1a 的协议没冻结就写 aggregator，等于把协议写死在一个服务里）。
> 2026-10-21：候选单已展开为「一、本仓任务」的正式六字段条目（表中「上游单号」指向的 QT 单正文现在就在本仓）。

| 阶段 | 内容 | 上游单号 | 前置 |
|---|---|---|---|
| X1a | 协议冻结进 contracts（`UnifiedDataPoint` / `TimeSemantics` / 四类错误码） | `QT-40` | 本仓立项 |
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
