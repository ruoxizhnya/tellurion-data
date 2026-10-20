---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
---

# DB Schema — tellurion-data

> **所有权声明**：本仓**拥有 `market` 与 `ingest` 两个 schema 的 DDL**，是这两组表的唯一写者。其他仓（core / agent / analytics / strategy）只经 API 读。
> 上级依据：`tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §1 —— **DDL 只有一个 owner**，同一个 PG 实例、每服务一个 schema，将来拆库是"按 schema 搬走"而不是重写。
> 本文件是本仓表结构的**唯一真相源**：代码里的建表语句与它不一致时，**改代码**；要改表结构，先改本文件（结构级变更还要立 ADR）。

## 1. 归属表

| 表 | 内容 | 现状 |
|---|---|---|
| `market.ohlcv` / `market.bars` | 行情主表（日线 / 分钟） | 现住 core 的 `quant` schema，X1d 迁入 |
| `market.symbols` | 标的与内外部代码映射 | 同上 |
| `market.adjust_factors` | 复权因子 | 同上 |
| `market.corporate_actions` | 分红 / 送股 / 配股 | 同上（BE-11 待深查） |
| `market.fundamentals` | 财报字段（带 `visible_at`） | 同上 |
| `market.macro` | 宏观序列 | 同上 |
| `market.sector` / `market.supply_chain` | 行业与产业链映射 | 同上 |
| `market.factor_definitions` / `market.factor_values` | 因子定义与因子值 | 同上 |
| `ingest.data_sync_state` | 同步水位 / 幂等坐标 | 同上 |

> 表名以 core 现有定义为准，X1d 迁移时逐表核对，**不在本文预造表名**。

## 2. 只读输入（不属于本仓，禁止写入）

| 数据 | 真实 owner |
|---|---|
| 回测结果（`BacktestResult` / 指标） | core（今天）/ `tellurion-orchestration`（X5 后） |
| 风控规则与快照 | `tellurion-risk` |
| 策略定义与 artifact | `tellurion-strategy` |
| 实验与研究数据 | `tellurion-agent`（`ai.*`） |

本仓**不读**这些表，也不 join 它们。

## 3. 红线

1. **单一写者**：`market` / `ingest` 只有本仓写；出现第二个写者 = 边界已腐化。
2. **`visible_at` 必填**：缺失即拒收（`ErrVisibleAtMissing`）。它是防前视的**第二道防线**。
3. **`visible_at` 的锚点必须是"披露日"，不是"报告期截止日"**：财报拿报告期末当锚点 = 把前视洞放回来。
4. **不许逐 bar / 逐笔远程**：延迟预算不允许；高频路径在 core 进程内。
5. 归一化在 connector 侧完成，**不许把原始字段泄漏进协议**。

## 4. 迁移策略

- 迁移脚本落在 `migrations/`，命名 `<序号>_<动作>.sql`（全仓一个递增序列，不复用号）。
- **只前滚**：已合并的迁移文件不改；修错靠新增一条。
- 每条迁移必须可重入（`IF NOT EXISTS` / `IF EXISTS`），且能在空库上一次跑完。
- 跨 schema 取数只走**只读视图或 API**；出现 `join` 别人的表就是边界腐化的第一信号。
- 表结构变更的同步义务：本文件 + `docs/ARCHITECTURE.md` 的「数据归属」节必须同一次提交里更新。

## 5. 待定

- `market.factor_values` 与 core 现有因子表的分工（谁迁、迁哪些列）待 X1d 定稿。
