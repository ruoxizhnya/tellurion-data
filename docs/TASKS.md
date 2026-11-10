---
status: active
last-verified: 2026-11-11
verified-by: 2026-11-11 文档对齐审计与迁移 —— ① 镜像名统一 `tellurion/data:${DATA_IMAGE_TAG:-latest}`；② contracts pin `v0.6.0 → v0.9.0`，源码零 `contracts/market` 引用（全走 `domain/market`），`CT-06-ALIAS-EXIT` 本仓侧完成（`build/vet/test` 全绿）；③ ARCH 的 X0 复核为 ✅（core 已落 `dataset_snapshot_hash`）；④ 8 条剩余项均为 ⏸ 且重开条件仍准确。此前：2026-11-10 core 部署面同日修复（compose/e2e/k8s/checker 让 data-service 拿到 `DATABASE_URL` + `DATA_MARKET_SCHEMA`；四条破坏验证真做真还原；Docker daemon 不可达未跑容器腿）等
---

# TASKS — tellurion-data 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整时登记 / 承接的挂起项。此前已收口单的正文与施工记录全部在归档文件里。

### DATA-REFDATA-JOB-WRITE · 参考数据作业级写入未验 + 既有库 DDL 不自动补齐 [P1] ⏸

- **① 目标**：证明「作业」这条路径（而不只是 writer 单测）真能把参考数据写进部署库，并把既有库的约束缺口交代清楚。
- **② 上下文**：`c344ec2` 的契约测试走的是真实 `Client` → 真实 `ReferenceWriter`，`c58e783` / `4afe14c` 补齐了 `delist_date` 与日历写侧守卫；**作业级**（`internal/sync` 队列 → 调度 → 落库）在部署库上尚未跑过一次完整 `stocks` + `trading_calendar`。另外 `exchange` 的 `NOT NULL DEFAULT` / `CHECK` 只对**新建表**生效，既有库不会自动补约束。
- **③ 要求**：① 部署后跑一次 `stocks` 与 `trading_calendar` 作业，记录行数、`delist_date` 回读值、失败行数；② 对既有库逐条核对 `db-schema.md` 里的约束是否在位（不在位就补 `ALTER`，或明确登记为「靠写侧守卫」）；③ 结果写进本节（或另立收口记录）。
- **④ 约束与非目标**：不改 writer 契约与 DDL 形状；不在本单引入通用迁移框架。
- **⑤ 验收**：两条作业的真实行数与 `delist_date` 回读；既有库约束核对表逐行有结论。
- **⑥ 边界**：本仓作业面与 schema；生产库执行归运维。
- **重开条件**：一次带真实 `TUSHARE_TOKEN` 的部署。

### DATA-INGEST-RAW · `ingest.raw` 归档面 [P2] ⏸

- **① 目标**：决定原始字节是否需要留存与回放。
- **② 上下文**：`ingest.raw` 表零写者、零读者、无保留期；本仓 `Transports` 是**解析后**窄接口，没有 raw 字节通道；core 退役时已随 `pkg/data` 删除写点。
- **③ 要求**：若重开——传输接口加 raw sink + 保留期/脱敏/容量口径 + 至少一个回放消费者。
- **④ 约束与非目标**：零消费者时不建通道。
- **⑤ 验收**：有真实回放消费者且保留期成文。
- **⑥ 边界**：本仓传输与 schema；合规口径归运维。
- **重开条件**：出现原始报文回放需求，或合规要求原始留存。

### DATA-TRANSPORT-MULTI · 传输多门（除 Tushare 外的门均未真接线）[P2] ⏸

- **① 目标**：把「多门传输」从代码在仓里、实际只有一门在跑的状态交代清楚，并给每门一个可判定的重开条件。
- **② 上下文**：真装配且真取数的只有 Tushare（`TUSHARE_TOKEN`）；东财客户端代码在仓内但**未接线**；Juchao / Xueqiu / Yahoo 自带默认端点、connector 恒启用但无验收；Alpha Vantage 需 key；Mootdx 无实现（见 `DATA-MOOTDX`）。多门路由策略尚未定。
- **③ 要求**：重开某门时——① 接入装配与健康检查；② 与 Tushare 同形状的 fail-closed 与数据质量闸；③ 一门一条契约测试 + 一次真取数；④ 写清多门路由/优先级/去重口径。
- **④ 约束与非目标**：不为「门多」而接门；不给未验证的 connector 发「已支持」表述。
- **⑤ 验收**：每门有独立可复现的真取数记录与失败语义。
- **⑥ 边界**：传输实现与装配；数据源授权与凭据归运维。
- **重开条件**：Tushare 之外出现明确的第二数据源需求（容灾 / 字段互补 / 额度）。

### DATA-MOOTDX · Mootdx 传输 [P2] ⏸

- **① 目标**：补齐 TDX 传输门。
- **② 上下文**：`Transports.Mootdx` 是空接口，需 TDX TCP 协议实现与可用数据源；当前仅 Tushare 一门真装配。
- **③ 要求**：实现 TCP 协议适配 + 声明 + 至少一条契约测试；与 Tushare 同形状的 fail-closed。
- **④ 约束与非目标**：不自造协议；不绕过数据源授权。
- **⑤ 验收**：真连一次取数成功 + 断连失败语义。
- **⑥ 边界**：传输实现；多门路由策略另行决定。
- **重开条件**：拿到可用的 TDX 数据源与授权。

### DATA-DIVIDENDS · `dividends` 零写者 [P3] ⏸

- **② 上下文**：表在 schema 内但无 writer（`docs/db-schema.md` §9 零写者清单）。
- **重开条件**：出现分红/送转的消费方（复权因子或回测现金流）。
- **④ 约束**：不为了「填表」而写无消费者的数据。

### DATA-SPLITS · `splits` 零写者 [P3] ⏸

- **② 上下文**：同 `DATA-DIVIDENDS`，零写者。
- **重开条件**：复权因子改为自算、或以拆股事件驱动持仓调整。

### DATA-INDEX-CONSTITUENTS · `index_constituents` 零写者 [P3] ⏸

- **② 上下文**：表在 schema 内但无 writer；指数成分历史是回测票池的另一来源。
- **重开条件**：出现「按指数成分历史截断票池」的回测需求。

### DATA-MARKET-ACTIVITY · `market_activity` 零写者 [P3] ⏸

- **② 上下文**：表在 schema 内但无 writer；原 `pkg/marketdata` 的有库用例依赖它，已由 `tellurion` 侧形状镜像夹具替代。
- **重开条件**：出现市场宽度/情绪类指标需求。

## 二、跨仓指针（不在本仓，勿双写）

| 事项 | 去哪 |
|---|---|
| core 侧旧数据面退役（`pkg/data` / `cmd/data` 删除） | `tellurion:docs/TASKS.md` |
| X1d 生产物理搬迁 + `visible_at` 回填（运维动作） | `tellurion:docs/TASKS.md` → `OPS-X1D-PROD-MIGRATION` |
| 6 个 `market` 别名（零写者表引用的形状） | `tellurion-contracts:docs/TASKS.md` → `CT-06-ALIAS-EXIT`。**本仓已迁完**（2026-11-11：源码零 `contracts/market` 引用，全部走 `domain/market`；pin `v0.6.0 → v0.9.0`，`build/vet/test` 全绿）⇒ contracts 侧可删别名 |

## 维护本文件

- **新增单**：写全六字段；六字段里必须显式引用**冻结契约的路径与节号**（契约仓的包路径、设计文档的节号），不许写"参考相关实现"。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
