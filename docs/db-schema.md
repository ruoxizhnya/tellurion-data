---
status: active
last-verified: 2026-10-24
verified-by: QT-40 X1d G4 —— DDL 归属落到本仓 `internal/schema`（statements 数组 + 三集合登记 + 回填腿），写面 `store.BulkInsert` 落地并接上 visible_at 门；§4 执行机制、§6.1/§6.5/§6.6 同步订正。P6 —— D2 回填仅测试库经 tagged 套件真跑（生产未执行），§6.5/§6.6 同步
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
| `stock_fundamentals` | 基本面**宽表**（PE/PB/PS/ROE…），带 `available_date` —— 它是**生成列** `COALESCE(ann_date, trade_date)`，即"披露日缺失时退化成报告期截止日"。这条兜底正是 §3 红线 3 点名的形状，且让任何"`available_date` 非空"式判据恒真 ⇒ 见 §6.3 的单独一条 |
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
| `sync_jobs` / `sync_schedules` | 同步任务与调度 | 无前缀（`public` 默认）→ 目标 `ingest`（本仓 DDL 已建 `ingest.sync_jobs` / `ingest.sync_schedules`，物理仍在 `public`） |
| `data_migrations` | 迁移记录 | 无前缀 → 目标 `ingest`（本仓 DDL 已建 `ingest.data_migrations`） |

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
2. **`visible_at` 必填**：缺失即拒收（`ErrVisibleAtMissing`）。它是防前视的**第二道防线**。⚠️ 今天**门与该列都不存在**（对表 E 组实测）⇒ 执法点、加列范围（哪些表在门外）与上线次序全部在 **§6**；不在 §6 的范围里"默认已被这条红线管住"。
3. **`visible_at` 的锚点必须是"披露日"，不是"报告期截止日"**：财报拿报告期末当锚点 = 把前视洞放回来。
4. **不许逐 bar / 逐笔远程**：延迟预算不允许；高频路径在 core 进程内。
5. 归一化在 connector 侧完成，**不许把原始字段泄漏进协议**。

## 4. 建表与迁移

**现状（必须先知道）**：core 的迁移是「**启动时按序执行一组幂等 DDL**」——唯一真相是 `pkg/storage/postgres.go` 的 `migrationStatements()` 数组（`migrate()` 在服务启动时逐条跑）；**没有版本表、没有 down、没有执行记录**（有意取舍，理由见 core 的 `migrations/README.md`）。`migrations/`（14 个）与 `docs/migrations/`（25 个）里的 `.sql` 是**历史镜像，不执行**——改它们不会改库。

**X1d 起（2026-10-24）**：`market` / `ingest` 两个 schema 的 DDL 归属已落到本仓 `internal/schema`（`Statements()` / `VisibleAtStatements()` / `BackfillStatements()`）。执行机制沿用 core 式 ①：有序幂等语句数组 + 启动时执行，不引入迁移库。

- **目标态**：本仓的数组是唯一真相；core 的内联数组在物理搬迁（D4）完成前仍是运行期事实，**这段双份是已登记的过渡状态**，不是新写者。
- **不许做的**：在本仓数组里静默 `ALTER TABLE ... SET SCHEMA`（§1 注：迁移不是改名）；也不许把 `visible_at` 直接写进 `CREATE TABLE`（列必须走独立的加列腿，否则目标侧与今天侧的列会各自长出来）。

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

## 6. `visible_at` 加列与历史行回填（对表 T5，2026-10-22 定）

> **要解决的矛盾**（不是"要不要做"，是"顺序错了会静默坏数据"）：`ErrVisibleAtMissing` 这道门**今天不存在**（§3 红线 2 是目标），而**已落库的行没有任何一列承载可见时刻**。门与列必须同批设计，否则门一上线就把历史数据全判违规（core 对表 §6 风险节的原话）。
> 本节只定**范围、规则、次序与判据**；执行属 X1b/X1d，DDL 文本今天仍只在 core 内联数组里（§4 已述）。

### 6.1 门的定义（执法点先钉到函数）

| 项 | 定案 |
|---|---|
| 执法位置 | `ValidatePoints`（`tellurion:pkg/data/source/etl.go:168`）——归一化之后、入库之前。它今天只判 `Symbol`/`TradeTime`/`Data` 非空，且缺项是**静默丢弃并计数**（返回 `([]UnifiedDataPoint, int)`）⇒ 加第四判据是**行为变更**：现在"少一条"进统计，改后"少可见时点"**整批拒收**。**2026-10-23 进展**：写侧门已在**数据服务侧**落地 = `tellurion-data:internal/ingest/gate.go` 的 `GatePoints`（判据 = **声明锚点列本身**非空且可解析，缺列报 `ErrVisibleAtMissing`、坏值报新哨兵 `ErrAnchorInvalid`，两者分诊；`available_date` 生成列当判据的假护栏有反证测试钉住）——core `ValidatePoints` 的第四判据随传输迁移（X1c）收编到同一处 |
| 「该批次」指什么 | 本次调用的入参切片，不是整张表。**只作用于写入路径的新批次，不追溯历史行**（历史行走 6.4） |
| 判据写法 | **「该 data_type 声明的锚点列本身非空」**，不是「有一个长得像可见时间的列非空」。差别是实打实的，见 6.3 的 `available_date` 条 |
| 读侧的另一半 | `api-schema.md` §1 注：resolve 物化时按 `visible_at <= as_of` 筛。**口径订正（本次对表逮到两处措辞）**：本仓 `docs/adr/0001` 决策 ① 写的是 `visible_at <= current_bar_time`，core 对表 L2 写的是 `visible_at <= as_of` —— 两条都不算错，但指的是**两次不同的比较**（前者在消费点逐 bar，后者在物化时给上界）。定案：**data 侧只声明并落列；比较可以发生在消费点，但任何一侧都不许重算 `visible_at`**。已 Accepted 的 ADR-0001 不原地改写，措辞合并放它的后继条目里 |

### 6.2 范围：三个集合，别一句"所有表都要加"糊过去

判据取**在册声明**（`tellurion:pkg/data/source/declarations.go`：18 条声明、去重 **15 个 `data_type`**——下面一律用 wire 上的**字面值**，不用 Go 标识符：`DataTypeAnnounce` 的字面值是 `announcements`、`DataTypeFundamental` 是 `fundamentals`、`DataTypeRealtime` 是 `realtime_quote`、`DataTypeMacro` 是 `macro_series`），不取 `NewTableMapper()` 的 `tables` 映射（13 张）——两者**不相等**（差 `macro_series` 与 `daily_basic`），这正是门覆盖面最容易漏的地方：

| 集 | 成员 | 处理 | 为什么 |
|---|---|---|---|
| **A · 加列**（12 张） | `ohlcv_daily_qfq`、`ohlcv_minute`、`realtime_quote`、`capital_flow`、`sectors`、`stock_sector_map`、`top_list`、`limit_up_pool`、`announcements`、`news`、`hot_search`、`global_ohlcv` | `ADD COLUMN IF NOT EXISTS visible_at TIMESTAMPTZ`（可空，先不 NOT NULL） | 经 connector→`Normalizer`→`BulkInsert` 落库，且表里**没有**语义等价的现成列 |
| **B · 不加列，承认现成列**（2 张） | `market_activity`（`daily_basic`）、`quant.macro_series`（`macro_series`） | 两表都已有 `as_of` 列，而 `as_of` **就是这两条声明给的可见锚点**（`declarations.go:83` 的 `VisibleField: "as_of"`，2026-10-16 用户裁决 A 的保守代理）⇒ 目标契约里它们的 `visible_at` **等于 `as_of`**，再建一列 = 同表两个锚点，读方各挑一个 | ⚠️ 但这两张表**不走 `BulkInsert`**（写者各是 `tellurion:pkg/data/tushare_daily_basic.go`（Migration 056）与 `pkg/data/macro.go`（Migration 050），两条都不在 `NewTableMapper()` 的 13 个映射值里）⇒ 挂在 `ValidatePoints` 上的门**拦不到它们**。要么把它们纳入 mapper 面，要么给这两条路径各写一次执法点——**不许默认"门已覆盖全部声明源"**，这是本节最大的覆盖面限制 |
| **C · 明确在门外** | `stock_fundamentals`（见 6.3 单列）、`market.index_daily`、`trading_calendar`、`stocks`、`index_constituents`、`dividends`、`splits`、因子族全部、`ingest.raw`、`sync_jobs`/`sync_schedules`/`data_migrations`、其余 `quant.*` / `research.*` | 本次不加列 | 逐个理由不同：`trading_calendar` 是 `calendar_hash` 的输入不是观测数据流；`index_constituents` 的前视风险在**成分变更时点**，归 resolve 的 `as_of` 语义（对表 D 组）；`market.index_daily` 是**实测漏项**——`NewTableMapper()` 的 13 个映射值里没有它，它由另一条路径写 ⇒ 与 B 集同一个"门拦不到"问题；因子族的值来自计算而非 connector 声明，可见性由 `dataset_snapshot_hash` 承担 |

### 6.3 重算规则（输入 = 15 个在册 `data_type` 的 `TimeSemantics` + `VisibleField`）

| 语义 | 在册 data_type | 重算式 | 今天能不能算 |
|---|---|---|---|
| `after_close` | `ohlcv_daily`、`capital_flow`、`top_list`、`limit_up_pool`、`daily_basic`、`global_ohlcv` | `visible_at = trade_date 的 15:00 北京时间` = 该日 **UTC 07:00**（若图省事按"该日 UTC 零点"落，等于北京 08:00，比收盘**早 7 小时** = 前视方向） | 能算，但 `global_ohlcv` 是**境外市场**（yahoo / alpha_vantage），"15:00 北京"对它不成立而源不给市场时区 ⇒ 它走 6.4 第一档，**不许按 A 股收盘时刻套** |
| `announcement_day` | `fundamentals`、`announcements`、`macro_series` | `visible_at = 锚点列当日 00:00 北京时间`（锚点分别是 `ann_date` / `ann_time` / `as_of`；`ann_time` 带时刻时**直接用时刻**，别再折成零点） | 能算，**前提是锚点列非空**。⚠️ 粒度代价要写明：这条语义本身是**日粒度**（词表注释原话「披露日早于等于回测日期才可用」），折成当日 00:00 等于宣布「披露日凌晨即可见」，而 A 股公告**多在盘后**发布 ⇒ 相对真实可见时刻最多早一整天。**这是 `announcement_day` 自带的口径，不是本节新增的风险**；日频研究踩不到，盘中 / 分钟策略会踩到。要压实时刻属于**枚举扩展**（如加 `announcement_after_close`）= MINOR + 「常量 + 校验 + SPEC」三件套同步，**不许在重算式里偷偷加偏移**（那等于改语义却不改词表） |
| `custom` | `realtime_quote`、`ohlcv_minute`、`sectors`、`stock_sector`、`hot_search`、`news` | 按声明的 `VisibleExpr` | ⚠️ **今天落不了地**：6 条 custom 的 `VisibleExpr` 全是中文自然语言（`"visible_at = fetch_time（实时快照，观察时刻即可见）"` `:96`、`"visible_at = bar_period_end（分钟 bar 周期结束即可见）"` `:104`），`validateDeclarations` 只判非空、不解析。两条出路：X1a 把它**结构化**（`{kind: period_end, offset: freq}` 一类），或由实现侧"看懂中文"各写一段 SQL —— 后者就是把协议语义放回注释，禁。本次定案：`fetch_time` 一族（`realtime_quote` / `sectors` / `stock_sector` / `hot_search`）按语义**等于 `ingest_time` 列**，直接取；`ohlcv_minute` 的 `bar_period_end` 算不了（要频率 + 周期长度），走 6.4；`news` 的锚点是实列 `publish_time`，直接算 |
| `next_open` | **零声明** | 词表里有（`tellurion:pkg/domain/market/visibility.go:72`），15 个在册 `data_type` 里没有一个用它 | 无历史行需回填。规则留空**但不许删枚举值**——删取值是 breaking |

> **`stock_fundamentals` 单列一条（本节最容易做成假护栏的地方）**：这张表**已经有**一列形状完全相同的生成列——
> `ALTER TABLE stock_fundamentals ADD COLUMN IF NOT EXISTS available_date DATE GENERATED ALWAYS AS (COALESCE(ann_date, trade_date)) STORED`（`tellurion:pkg/storage/postgres.go:476`，Migration 031，`postgres.go:456` 那行注释自己标的是 **P1-4 / 语义债**）。
> 更值得留意的是它的设计理由原文（`postgres.go:470-471`）：「生成列从物理上保证：**它不可能为空**，也不可能和 COALESCE 的结果不一致，加列时存量自动算好（不需要回填 UPDATE）」——这句在"防丢列"上是优点，**搬到可见性门上就是缺陷本身**。
> 两条后果：① **别再新建 `visible_at`**——同表两个可见性锚点必然漂，且今天所有 PIT 读都走 `available_date`（`pkg/storage/fundamentals.go:69`、`:121`、`:165`）；
> ② **门在这张表上用 `available_date` 判就是假护栏**：`trade_date NOT NULL` + COALESCE ⇒ `available_date` **永远非 NULL**，「`visible_at IS NOT NULL` 才放行」恒真，`ann_date` 缺失（= 真实披露日未知、退化成报告期截止日 = 前视）的行照样大摇大摆进门。
> 所以执法判据必须是 **`ann_date IS NOT NULL`**，不是 `available_date IS NOT NULL`。
> COALESCE 这条兜底本身要不要改（改成 `ann_date` 为空即 NULL），是个有代价的岔路：改了更诚实，但**今天能读到的那批 ann_date 为空的行会全部消失**，`GetFundamentalsPIT` 的结果集会变小 —— 属 X1d 裁决，本节不默默改，只把"不许拿生成列当判据"钉成 6.6 的验收②。

### 6.4 算不了的处理（三档，禁止第四档）

按可用性递降；**每档的代价都要写进行为说明**，否则下一位执行者会以为兜底是免费的：

1. **保守兜底 `visible_at = ingest_time`**（A 集 12 张里 11 张有这列）——语义 = 「至少在导入时才可能到手」，与宏观 `as_of` 的既有裁决同形（`declarations.go:60-77`，代价「失去发布日精度」2026-10-16 已由用户接受）。**这条不是无损兜底**：该行在此时刻**之前**的任何 `as_of` 都取不到 ⇒ 用它跑历史回测是**系统性少数据**，不是"精度差一点"。
2. **拿不到 `ingest_time` 的**：A 集里唯一一张是 `ohlcv_daily_qfq`（DDL 只有 `created_at`，`NewTableMapper()` 给它的列集合也不写 `source`/`ingest_time`）⇒ 用 `created_at` 顶第一档；`stock_fundamentals` 同理被禁止写那两列（`bulk_insert_fundamentals_pit_test.go:157` 的 forbidden 名单点名 `source`/`ingest_time`/`available_date`/`created_at`），但它在 6.3 单列那条里已定：**判据走 `ann_date`，不新建列**。用 `created_at` 时要在列注释里写明「本列是 UTC 入库时刻的代理，不是源披露/收盘时刻」。
3. **三档都拿不到 ⇒ `visible_at` 留 NULL**，读路径必须显式排除（`WHERE visible_at IS NOT NULL AND visible_at <= $as_of`）。**不补零、不猜、不回填 `trade_date`**。
4. **禁止的第四档**：`UPDATE ... SET visible_at = trade_date` 一把梭让门变绿。这正是 `available_date` 的 COALESCE 已经犯过的形状（把"我不知道"写成"报告期截止日就能看"），§3 红线 3 就是为它写的。

### 6.5 上线次序（四步；顺序红线在最后一行）

| 步 | 动作 | 机制 |
|---|---|---|
| D1 | 加列（可空 `TIMESTAMPTZ`，不 NOT NULL、不设默认） | ✅ **已落本仓**（2026-10-24，X1d）：`internal/schema.VisibleAtStatements()`，A 集 12 张各两条（`market.*` 目标 + `public.*` 今天） |
| D2 | 分批回填（按 6.3 规则 + 6.4 档位） | 一次性重活**必须靠哨兵防重放**：`data_migrations`（`postgres.go:492`，`name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ DEFAULT NOW()`）+ `IF NOT EXISTS (SELECT 1 FROM data_migrations WHERE name='...')`，形状照 Migration 052 / 054（`:521`、`:555`）。DBT-07 的实测教训就记在 §4：哨兵置位后**不可重放**，且默认构建的测试曾把 054 的整表 ×1000 真的作用在 3,926,510 行上 ⇒ 回填腿只允许出现在启动路径或 tagged 集成套件，不许进默认 `go test`（护栏：core `internal/repoguard/store_migration_isolation_test.go`）。**2026-10-24 ✅ 仅测试库已执行**：`internal/schema/backfill_pg_test.go`（`go test -tags backfill ./internal/schema`，默认构建不编译；DSN 只认 `DATA_TEST_DSN` 且库名须 `_test`，生产 DSN 自动 skip）在临时 schema 里真跑 `BackfillStatements()`；**生产库未回填** |
| D3 | 开门：`ValidatePoints` 加第四判据（只拦新批次） | ✅ **已落本仓**（2026-10-24，X1d 写面），**两道防线**：① `internal/ingest.GatePoints` 按该 data_type 的**声明锚点**逐点校验（缺 ⇒ `ErrVisibleAtMissing`、坏值 ⇒ `ErrAnchorInvalid`），失败**不触达 store**；② `internal/store.BulkInsert` 在开事务前再判一次 `Point.VisibleAt`，任一点为零 ⇒ 整批拒收（防御直达 store 的调用）。core `ValidatePoints` 的第四判据随传输迁移收编；与 `ErrVisibleAtMissing` 的新建同一条 commit（对表 E 组：哨兵只增不删不改语义） |
| D4 | 读路径切到 `visible_at` | 逐表切；每张表切之前先跑对照腿 |

**回填前先数行**：逐表 `SELECT count(*) FROM <t> WHERE <锚点列> IS NULL` 登记，再决定分批大小——本文件**不写行数**（没实测就不写）。
**每批要对拍**：回填前后 `count(*)` / `min(visible_at)` / `max(visible_at)` 三项不变式 + 一条现成 PIT 读函数的结果不变（`GetFundamentalsPIT`，`pkg/storage/fundamentals.go:114` 就是对照腿）。
**两条顺序红线**：① D3 必须在 D2 之后（否则门一开就把历史全判违规）；② **D2 未完成期间，读路径不许开始按 `visible_at` 过滤** —— 未回填的行会凭空消失，那是**反方向**的静默数据缺失，和泄漏一样不会报错。

### 6.6 验收（可机械执行）

1. A 集 12 张表都有 `visible_at` 列且可空；B 集 2 张表的 `as_of` 在文档与协议里被**明确承认为** `visible_at`（不是"看起来像"）。
2. 造一条 `ann_date` 为 NULL 的财报行走 `BulkInsert` ⇒ **拒收**，错误报文点名 `ann_date`。破坏验证的**第二条腿**：把执法判据换成 `available_date IS NOT NULL` 之后，这条测试**必须仍然红**——它若变绿，说明判据被偷换成了那个永不为 NULL 的生成列，即 6.3 点名的假护栏，本节作废。⚠️ **现状（2026-10-24）**：`fundamentals` 属 C 集，写面在 `specFor` 处以 `ErrWriteTableNotInASet` 拒写（早于锚点校验）⇒ 这条判据的落点是 fundamentals 自己的同步腿，**不在 `BulkInsert` 上**；不许用「写面拒了」冒充「锚点判据已生效」。写面的覆盖面由 A 集 spec 复核（`TestWriteSpecs_MatchASetAndConnectorTypes`）与 B 集拒写用例（`TestWriter_RejectsTablesOutsideASet`）钉住。
3. 覆盖面要如实登记：B 集与 C 集里「门拦不到」的每一条，都要有一条测试或一条显式豁免，**不许靠"门在 ETL 上"这句话默认全覆盖**。
4. **已落的测试证据（2026-10-24，G4）**：整批拒收 —— `TestWriter_RejectsWholeBatchWithoutVisibleAt`、`TestETLProcess_MissingVisibleAnchorRejectsWholeBatchWithoutWriting`、PG 腿 `TestPG_BulkInsert_RejectsBatchWithoutVisibleAt`；B 集拒写 —— `TestWriter_RejectsTablesOutsideASet`、PG 腿 `TestPG_BulkInsert_RejectsBSetType`；同 hash 重写 —— `TestWriter_SameContentRewriteIsNoOp` / `TestPG_BulkInsert_IsIdempotentOnSameContent`，异内容 —— `TestWriter_DifferentContentFailsLoudAndRollsBack` / `TestPG_BulkInsert_ConflictingContentRollsBackWholeBatch`。**破坏验证**：把 `store.BulkInsert` 的 `VisibleAt` 判空短路（`if false && …`）⇒ 整批拒收用例红；逐字节还原后绿（SHA256 一致）。
5. **回填测试证据（2026-10-24，P6，仅测试库）**：`TestBackfillSuite_TestDatabaseOnly`（`-tags backfill`）PASS —— A 集 12 张回填后无 NULL 且逐档取值符合 §6.3/§6.4（after_close 显式 15:00 北京、锚点列、`ingest_time`、`created_at`），哨兵逐条对账且二次执行不改值，B 集 `as_of` 不改写、C 集无 `visible_at`；临时 schema 跑完即删。**生产 DSN 自动 skip**由 `TestBackfillTestDSN_ProductionDatabaseIsSkippedNotRun` 钉住（只读 `DATA_TEST_DSN`，库名须 `_test`）。**破坏验证**：`057a` 改成 `SET visible_at = trade_date` ⇒ 禁止第四档用例 + 测试库取值/零点两条断言红；逐字节还原后绿（SHA256 一致）。

