---
status: active
last-verified: 2026-10-22
verified-by: M5 对账结案 + M8 接口对表落定后的自审订正（引用锚点逐条回核；CT-02 现状按 2026-10-22 工作区实测改写）
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
> **✅ 两条前置的执行状态（2026-10-21）**：① **M5 已结案** → `tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2（本仓在 ADR-027 §2.6.4 拓扑里**有位子**，是两个「不需要另立 ADR 就能升进程」的仓之一；代价是背上 §2.6.2 的**六个限界上下文包级隔离**义务，见对表 T4）。② **M8 已落** → `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1（QT-40 的逐个 port/endpoint：签名 + 错误语义 + 版本策略 + 五条待办 T1~T5）。
> ⚠️ 原条目把模板指向 `target-module-topology.md` 的「UnifiedDataPoint 节」——**该文件 grep `UnifiedDataPoint` 零命中**（幻影引用），已订正为 §4.1。

**仍未解除的硬前置（决定 QT-40 什么时候真能动代码）**：

| 前置 | 状态 | 卡住的是 |
|---|---|---|
| `QT-05`（S1 契约层落码：`pkg/domain`（含 `domain/market`）+ `pkg/backtest/contracts` 进 contracts） | ⬜ 未启动（`tellurion-contracts:docs/TASKS.md` QT-05；contracts 仓今天只有 `doc.go` + `tools/check_api_surface`，core `go.mod` 对 contracts 零 `require`） | **X1b 全部**。本仓 module path 是单段短名（`tellurion-data`），按编译器规则无法 `require` core，又禁止 `replace ../` —— 协议类型拿不到，aggregator 就无法落地。跳序的代价工单施工序已点过一次：「X1a 的协议没冻结就写 aggregator，等于把协议写死在一个服务里」 |
| `QT-02` / `QT-04` / `QT-63`（W0 收尾） | 🔶 在落（main 上已有 `f615e8e` + `9ffff72` 两笔 fill_timing 提交，台账仍标 ⬜）/ ⬜ / ⬜ | QT-05 排在「W0 全绿之后」（core 执行顺序总览 S1 行），因此间接卡住本仓 |
| `CT-02` / `QT-19`（contracts 版本规则表 + 兼容矩阵 + CHANGELOG） | ⬜（2026-10-22 实测：contracts 工作区已有 README 版本表 / 兼容矩阵 / `CHANGELOG.md` 与 `check_release_matrix` 的 AGENTS 条目，**未提交**，台账仍 ⬜；core 总览把 QT-19 排在 QT-07 之后，**不阻塞 X1a**） | 不卡施工，卡**口径**：版本规则只认 contracts README 那张表 + `tellurion:docs/TASKS.md` QT-19 ③ 那五条（0.x 期允许 breaking，但要 bump MINOR + 写 CHANGELOG），**本仓不许自造版本口径** |

**对 QT-40 正文前提的实测订正（不改写六字段正文，按台账惯例在此登记）**：

- **②「现状已实现约 80%」只对 connector 层成立**。aggregator 半边今天是空的：`DatasetSnapshotInput`（`tellurion:pkg/backtest/contracts/provenance.go:97-123`）**零生产者**——全仓 grep 无任何构造点，只有定义与向量测试。X1b 交付的是**第一个生产者**，不是搬迁。
- **③-② 的四个错误码在代码里一个都不存在**（`ErrNoTimeSemantics` / `ErrUnknownDataType` / `ErrVisibleAtMissing` / `ErrSnapshotMismatch` 全仓 grep = 0 命中）。真实在执法的是 `tellurion:pkg/data/source` 的三个注册期哨兵 `ErrDeclarationMissing` / `ErrDeclarationInvalid` / `ErrDeclarationMismatch`（`contract.go:66-68`）。**`ErrNoTimeSemantics` 是 `ErrDeclarationMissing` 的旧名，作废**（见对表 G 组 T1）。
- **③-② 的 `ErrVisibleAtMissing` 有一条工单没写的代价**：现有已落库数据**没有承载 `visible_at` 的列**，这道门一上线历史行不可回填 ⇒ 必须与 X1d 的 schema 迁移同批设计（加列 + 按 `TimeSemantics` 规则重算，重算不了的处理要成文）。登记为对表 T5。
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
