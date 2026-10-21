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

> **表名来自实测**：对 core 的内联 DDL（`pkg/storage/*.go` 的 `CREATE TABLE IF NOT EXISTS`）与 `migrations/` / `docs/migrations/` 做了全量 `grep`（2026-10-20）。左列是**今天的真实名字**，右列是迁移后的归属。
> **不按类型名臆造表名**：本文档早期版本写过 `market.ohlcv` / `market.symbols` / `ingest.data_sync_state` 这类"听起来对"的名字，实际一个都不存在，已全部换成真名。

### 1.1 行情与参考数据（目标：`market`）

| 真实表 | 内容 |
|---|---|
| `global_ohlcv` | 日线行情主表（含复权口径） |
| `ohlcv_minute` | 分钟线 |
| `ohlcv_daily_qfq` | 前复权日线（派生表） |
| `market.index_daily` | 指数日线（**已是 `market.` 前缀**） |
| `dividends` / `splits` | 分红 / 拆股（公司行为） |
| `trading_calendar` | 交易日历 |
| `stocks` | 标的（内外部代码映射） |
| `index_constituents` | 指数成分 |

> **摘除认领**：`backtest_adjustment_anchors`（回测复权锚点）**已划归 core 回测域（2026-10-21 裁决）；本仓不认领** —— 写者是 `tellurion:pkg/backtest/adjustment_snapshot.go`（起跑冻结/回填锚点），不是本仓行情同步，不随 `market` 迁移（详见 `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` 归属表）。

### 1.2 基本面

| 真实表 | 内容 |
|---|---|
| `stock_fundamentals` | 基本面**宽表**（PE/PB/PS/ROE…），带 `available_date` |
| `fundamentals_detail` | 基本面**逐字段行存 + PIT**（契约 C1，与宽表并存不复用） |

### 1.3 因子

| 真实表 | 内容 |
|---|---|
| `factor_definitions` | 因子定义（库中公式 + `version_hash`） |
| `factor_cache` | 因子值结果（按 symbol/date 缓存） |
| `factor_returns` | 因子分组收益（`factor_name` × `trade_date` × `quintile`，写者 `tellurion:pkg/storage/cache.go`） |
| `ic_analysis` | 因子 IC 分析结果（`factor_name` × `trade_date`，写者 `tellurion:pkg/storage/cache.go`） |
| `factor_hypothesis` | 因子假设登记与内置种子（写者 `tellurion:pkg/storage/factor_hypothesis.go`） |
| `factor_registrations` / `factor_registration_audit` / `factor_mining_records` | 因子注册与审计 / 挖掘留痕（D8，同族治理账本：登记记「谁把哪一版接进来」，挖掘留痕记「这一版凭什么进库」；写者同为数据服务挖掘入口） |

> **SPEC-R-02 认领**：`factor_returns` / `ic_analysis` / `factor_hypothesis` 属本仓**因子域**（与 `factor_cache` / `factor_definitions` 同域），唯一 owner = `tellurion-data`。当前 DDL 文本实体在 `tellurion:pkg/storage/postgres.go`（core 内联 DDL 为唯一真相），X1d 按 schema 迁移时随因子域一并搬走；在此之前 core 的写入属过渡期实现，不回写归属。

### 1.4 专题数据

| 真实表 | 内容 |
|---|---|
| `sectors` / `stock_sector_map` | 行业与成分映射 |
| `limit_up_pool` / `top_list` / `capital_flow` / `hot_search` / `realtime_quote` | 涨停池 / 龙虎榜 / 资金流 / 热搜 / 实时快照 |
| `news` / `announcements` | 新闻 / 公告 |
| `market_activity` | 市场活跃度 |
| `quant.sw_industry_tree` / `quant.industry_vitality` | 申万行业树 / 行业活力（**已在 `quant` 前缀下**） |
| `quant.supply_chain_edge` / `quant.etf_constituent` / `quant.macro_series` | 产业链边 / ETF 成分 / 宏观序列 |

### 1.5 原始与同步

| 真实表 | 内容 | 今天在哪 |
|---|---|---|
| `ingest.raw` | 原始落库（未归一化，**已是 `ingest.` 前缀**） | ✅ 已归位 |
| `sync_jobs` / `sync_schedules` | 同步任务与调度 | 无前缀（`public` 默认）→ 目标 `ingest` |
| `data_migrations` | 迁移记录 | 无前缀 → 目标 `ingest` |

**红线**：`market` / `ingest` 只有本仓写；出现第二个写者 = 边界已腐化。

> **迁移不是改名**：X1d 的头号目标是"单一写者"，不是"统一前缀"。今天多数表无前缀（落默认 schema），搬进 `market` 要动所有读方 —— 分批进行，每批都要过对拍。

## 2. 只读输入（不属于本仓，禁止写入）

| 数据 | 真实 owner |
|---|---|
| 回测结果（`BacktestResult` / 指标） | core（2026-10-21 裁决：单一写者；orchestration 只经 API 读） |
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

## 4. 建表与迁移

**现状（必须先知道）**：core 的迁移是「**启动时按序执行一组幂等 DDL**」——唯一真相是 `pkg/storage/postgres.go` 的 `migrationStatements()` 数组（`migrate()` 在服务启动时逐条跑）；**没有版本表、没有 down、没有执行记录**（有意取舍，理由见 core 的 `migrations/README.md`）。`migrations/`（14 个）与 `docs/migrations/`（25 个）里的 `.sql` 是**历史镜像，不执行**——改它们不会改库。

**本仓约定**（从建仓第一天执行）：

- 迁移文件只放 `migrations/`，命名 `<序号>_<动作>.sql`，**本仓内**递增（不与别的仓共享序列，也不复用号）。
- **执行机制随第一个建表单定，二选一（不能"都不选"）**：
  1. **core 式**：Go 里的 `migrationStatements()` 数组 + 启动执行，幂等、无版本表——与 core 一致、运维零新增；
  2. **版本化**：引入迁移库 + `schema_migrations` 表（可 down、有执行记录）——服务变多后更稳，但要一次性 baseline。
  **取向**：X1~X3 期间优先 ①（少一层依赖），服务数 ≥3 或出现"必须先降级"的需求时重估 ②。
- **只前滚**：已合并的迁移不改；修错靠新增一条。
- 每条必须可重入（`IF NOT EXISTS` / `IF EXISTS`），且能在空库上一次跑完。
- 跨 schema 取数只走**只读视图或 API**；出现 `join` 别人的表就是边界腐化的第一信号。
- 结构变更的同步义务：本文件 + `docs/ARCHITECTURE.md` 的「数据归属」节必须同一次提交里更新。

## 5. 待定

- `factor_cache`（因子值结果）与 `factor_definitions`（定义）的**读方收敛**：今天 core 内部有直读，X1d 后必须只剩 API。
- `global_ohlcv` / `ohlcv_minute` 的**分区与索引**是否随迁移重做（现为内联 DDL 建表，迁移后要有可重入脚本）。
- `data_source_registry` 归 `market` 还是 `ingest`：取决于"配置算不算数据"，X1d 定。
