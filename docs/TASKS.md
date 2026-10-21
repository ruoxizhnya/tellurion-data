---
status: active
last-verified: 2026-10-21
verified-by: 任务台账批量重分布（X 线单展开：QT-40 迁入）
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

> **⚠️ 开工前置（refactor-plan-review M5/M8，2026-10-20 登记）**：本表任何单开工前，必须先过 core `tellurion:docs/TASKS.md`「五、X 线」的**公共前置**两条：① 建仓/升格前先与 ADR-027 §2 对账（12 仓是上限/当前规划，不是承诺终态）；② 本仓设计文档先写「**接口对表**」小节（签名 + 错误语义 + 版本策略，模板见 `tellurion:docs/design/qt-decoupling/target-module-topology.md` UnifiedDataPoint 节），**未对表不开工**。正文以彼处为准，此处只留指针、不镜像。

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
