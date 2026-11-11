---
status: active
last-verified: 2026-11-10
verified-by: 裁决 ⑰ 施工 —— `market.index_daily` 撤认领：本仓 `internal/schema` 的 DDL / `TableNames()` 条目 / C 集豁免三处删除，`TableNames()` 23 → 22 张，新增守卫 `TestIndexDaily_IsNotOwnedByThisRepo`；§1.1 / §6.2 / §7 / §8 同步订正。真 PG 重放腿复跑（`TestSchemaReplay_TwoRunsLeaveShapeUnchanged` PASS）
---

# DB Schema — tellurion-data

> **所有权声明**：本仓**拥有 `market` 与 `ingest` 两个 schema 的 DDL**，是这两组表的唯一写者。其他仓（core / agent / analytics / strategy）只经 API 读。
> **唯一例外（裁决 ⑰，2026-11-10 已施工）**：`market.index_daily` 的 DDL 与写权都在 core（`tellurion:pkg/storage/index_daily.go`），本仓**不建表、不登记、不认领**（两套 DDL 形状不同，先建表者定形）。
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
| `market.index_daily` | 指数日线（**已是 `market.` 前缀**；**owner = core，本仓不认领**，见 §7） |
| `dividends` / `splits` | 分红 / 拆股（公司行为） |
| `trading_calendar` | 交易日历 |
| `stocks` | 标的（内外部代码映射；含 `delist_date`，供历史票池 / survivorship 过滤） |
| `index_constituents` | 指数成分 |

> **摘除认领**：`backtest_adjustment_anchors`（回测复权锚点）**已划归 core 回测域（2026-10-21 裁决）；本仓不认领** —— 写者是 `tellurion:pkg/backtest/adjustment_snapshot.go`（起跑冻结/回填锚点），不是本仓行情同步，不随 `market` 迁移（详见 `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` 归属表）。

### 1.2 基本面

| 真实表 | 内容 |
|---|---|
| `stock_fundamentals` | 基本面**宽表**（PE/PB/PS/ROE…），带 `available_date` —— 它是**生成列** `COALESCE(ann_date, trade_date)`，即"披露日缺失时退化成报告期截止日"。这条兜底正是 §3 红线 3 点名的形状，且让任何"`available_date` 非空"式判据恒真 ⇒ 见 §6.3 的单独一条 |
| `fundamentals_detail` | 基本面**逐字段行存 + PIT**（契约 C1，与宽表并存不复用） |

### 1.3 因子（不属本仓）

> **归属现状（2026-11-11 订正）**：本仓 `internal/schema/schema.go` 的 `TableNames()` 实测 **22 张表**，全部是 `ingest.*` / `market.*`，**不含任何 `factor_*`**。下表列的是因子域的历史/目标说明，**不是本仓物理表清单**；这些表的物理 DDL 与写者今天都在 `tellurion`（core）的 `public` schema。

| 表 | 内容与今天的 owner |
|---|---|
| `factor_definitions` | 因子定义（库中公式 + `version_hash`）；DDL/写者在 core（`tellurion:pkg/storage/postgres.go`、`factor_definition.go`） |
| `factor_cache` | 因子值缓存；owner = **core**，按 ADR-037 在消费点按需求值，**不新增 DB 写者**（当前零写者） |
| `factor_returns` | 因子分组收益；owner = **core**（ADR-038），物理在 core `public`，写方法在 `tellurion:pkg/storage/cache.go`，但**非测试零调用**；本仓不认领 |
| `ic_analysis` | 因子 IC 分析结果；owner = **core**（ADR-038），物理在 core `public`，读写方法均**零调用**；本仓不认领 |
| `factor_hypothesis` | 因子假设登记；**owner = `tellurion-agent` 的 `ai.factor_hypothesis`**（U8 裁决 + 代码实测），core 的 `public.factor_hypothesis` 已退役 |
| `factor_registrations` / `factor_registration_audit` / `factor_mining_records` | 因子注册审计 / 挖掘留痕；DDL 与写者在 core（`tellurion:pkg/storage/factor_registration.go`、`mining.go`） |

> **SPEC-R-02（2026-11-11 订正）**：因子域**不再认领为本仓所有**。`ADR-037` 判 `factor_cache` owner=core、消费点按需求值；裁-14 与 `dataflow` 不变量 7 判 data 不提供因子读面。`factor_hypothesis` 已归 `tellurion-agent`；`factor_returns` / `ic_analysis` 由 **ADR-038** 裁决归 core（与因子值同源），在任何情况下都**不迁入本仓、不新增写者**。
>
> **历史**：2026-10-25 U8 已把 `factor_hypothesis` 改判给 agent；原 SPEC-R-02 的"唯一 owner = data"是过期快照，已被 ADR-037 与裁-14 取代。

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
| `ingest.raw` | 原始落库（未归一化，**已是 `ingest.` 前缀**） | ✅ 已归位 + **证据归档面已落地（2026-11-12，ADR-0010）**：写者 `store.EvidenceStore`（`POST /api/ingest/raw`），读方 `GET /api/evidence[/{content_hash}]`；落库前脱敏、保留期见 §9.5 |
| `sync_jobs` / `sync_schedules` | 同步任务与调度 | ✅ **已归位（2026-11-10，X1c）**：本仓 DDL 建 `ingest.sync_jobs` / `ingest.sync_schedules`，读写者只有 `internal/sync`；core 的 `pkg/storage/sync_jobs.go` 已随 `a96d836` 删除，`public.*` 物理残留已由 X1d 迁移脚本搬入（2026-11-10 便携演练见 `docs/archive/x1d-local-replay-2026-11-10.md`；系统实例 `127.0.0.1:5432` 同日按新结构整库重建，见 `docs/archive/system-pg-init-2026-11-10.md`） |
| `data_migrations` | 迁移记录 | 无前缀 → 目标 `ingest`（本仓 DDL 已建 `ingest.data_migrations`） |

**红线**：`market` / `ingest` 只有本仓写；出现第二个写者 = 边界已腐化。

> **✅ 2026-11-10 终局（取代 2026-10-25 的过渡口径）**：core 的数据面写点在 `a96d836`（2026-11-08）随 `pkg/data` / `cmd/data` 整体删除而**归零**（`pkg/storage/ohlcv.go` / `sector.go` / `ingest_raw.go` / `stocks.go` / `calendar.go` 均已删除）；core 非测试代码对 `market.*` / `ingest.*` 的写点只剩裁决 ⑰ 显式留给 core 的 `market.index_daily`（`pkg/storage/index_daily.go`），它不属本仓 22 张表。物理 `SET SCHEMA` 已于 2026-11-10 在本机实例执行（22 表、`public` 归零、`verify.sql` 22/22），详见 §9 与 `docs/archive/x1d-local-replay-2026-11-10.md`。判点数仍以 grep 为准（§9.4 给出可复跑的 grep 口径）。

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
2. **`visible_at` 必填**：缺失即拒收（`ErrVisibleAtMissing`）。它是防前视的**第二道防线**。⚠️ 2026-10-24（X1d）起**门与列都已落地**（`GatePoints` + `VisibleAtStatements()`；PG 回填 2026-11-10 在本机实例执行）；执法点、加列范围（哪些表在门外）与上线次序见 **§6**，不在 §6 的范围里"默认已被这条红线管住"。
3. **`visible_at` 的锚点必须是"披露日"，不是"报告期截止日"**：财报拿报告期末当锚点 = 把前视洞放回来。
4. **不许逐 bar / 逐笔远程**：延迟预算不允许；高频路径在 core 进程内。
5. 归一化在 connector 侧完成，**不许把原始字段泄漏进协议**。

## 4. 建表与迁移

**现状（必须先知道）**：core 自有的 `quant.*` / `audit.*` / `public.stock_fundamentals` 等迁移仍是「**启动时按序执行一组幂等 DDL**」——唯一真相是 `pkg/storage/postgres.go` 的 `migrationStatements()` 数组（`migrate()` 在服务启动时逐条跑）；**没有版本表、没有 down、没有执行记录**（有意取舍，理由见 core 的 `migrations/README.md`）。`market` / `ingest` 两个 schema **不在此列**（见下一段）。`migrations/`（14 个）与 `docs/migrations/`（25 个）里的 `.sql` 是**历史镜像，不执行**——改它们不会改库。

**X1d 起（2026-10-24）**：`market` / `ingest` 两个 schema 的 DDL 归属已落到本仓 `internal/schema`（`Statements()` / `VisibleAtStatements()` / `BackfillStatements()`）。执行机制沿用 core 式 ①：有序幂等语句数组 + 启动时执行，不引入迁移库。

- **目标态（2026-11-10 已达成）**：本仓的数组是唯一真相；core 的内联数组已随 `a96d836`（2026-11-08）删除（core 只剩自己拥有的 `stock_fundamentals` / `market.index_daily` 等），双份过渡状态结束。
- **不许做的**：在本仓数组里静默 `ALTER TABLE ... SET SCHEMA`（§1 注：迁移不是改名）；也不许把 `visible_at` 直接写进 `CREATE TABLE`（列必须走独立的加列腿，否则目标侧与今天侧的列会各自长出来）。
- **X1d 物理搬迁脚本（2026-11-10）**：`migrations/0001_x1d_set_schema.sql`（另有 `.down.sql` / `.verify.sql`），单事务、幂等；目标空壳可删，**目标副本有行即报错中止**（绝不静默合并）。22 张表的清单与 `schema.TableNames()` 由 `TestMigrationTableListMatchesSchema` 钉住（三份脚本都要对得上）。它**不进** `Statements()`——迁移是运维动作，不是建表。演练（Docker PostgreSQL 16.15）：22 表搬迁、`public` 归零、探针行保留、非空冲突整体回滚（`public` 仍 22 表）、重跑 22 条 skip、`down` 还原 22 表且行数不变。**本地系统实例已执行（2026-11-10）**：系统 PostgreSQL 16.15（服务 `postgresql-x64-16`，`127.0.0.1:5432`）修复 `pg_hba.conf` 的 UTF-8 BOM 后启动；`tellurion` 库按删除前清单 DROP + 重建 → 68 条 DDL / 22 张表 → `0001` 22 条 skip → `verify.sql` 22/22 `ok`、`public_state` 全空 → 回填 12 条（空表 0 行、哨兵 12）→ 幂等复跑 + `-tags backfill` 套件全 PASS。取证见 `docs/archive/system-pg-init-2026-11-10.md`。**仍未做**：任何远端/生产实例零动作（按约定执行者是运维）。

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

> **状态更新（2026-10-24 / 2026-11-10）**：本节是 2026-10-22 的设计起点；门与列均已于 X1d 落地并在本机实例回填。判据与次序仍有效，"今天不存在"只描述立节时的状态。
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
| **C · 明确在门外** | `stock_fundamentals`（见 6.3 单列）、`trading_calendar`、`stocks`、`index_constituents`、`dividends`、`splits`、因子族全部、`ingest.raw`、`sync_jobs`/`sync_schedules`/`data_migrations`、其余 `quant.*` / `research.*` | 本次不加列 | 逐个理由不同：`trading_calendar` 是 `calendar_hash` 的输入不是观测数据流；`index_constituents` 的前视风险在**成分变更时点**，归 resolve 的 `as_of` 语义（对表 D 组）；因子族的值来自计算而非 connector 声明，可见性由 `dataset_snapshot_hash` 承担。**`market.index_daily` 已不在本集合（2026-11-10）**：裁决 ⑰ 把 owner 判给 core，本仓连 DDL 与写权都不认领，登记「在门外」等于仍宣称对它有一份可见性口径 ⇒ C 集条目、DDL、`TableNames()` 条目三处同批删除 |

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
6. **写面经 HTTP 的覆盖面（2026-10-25，H4 ②）**：`POST /ingest/points` 的 A 集门与 `ON CONFLICT DO NOTHING` 语句形状由 `internal/httpapi/points_test.go:TestIngestPointsThroughRealWriter` 在**真 `store.Writer`** 上钉住（B 集 `daily_basic` / C 集 `fundamentals` 过闸门后仍被写面拒收，且一条 INSERT 都没发出；A 集 `ohlcv_daily` 发出的 SQL 含 `visible_at` 列）。「缺 `visible_at` 整批不落」在装配层另有一条独立腿（`internal/assembly`：`TestWriteRejectsWholeBatchWithoutAnchorAndNeverCallsStore`）。⚠️ **诚实边界（2026-11-10 更新）**：上述单测不连库；真 SQL 腿由 `internal/store` 的 `DATABASE_URL`-gated PG 套件覆盖，写端点已于 2026-11-07 晚在真 PG（便携 16.2）上端到端跑过（同内容 200 / 改内容 409 / 停库 503），见 `api-schema.md` §1 注。

## 7. K3 十张「有表无写者」的处置（SVC-DATA-G2 ③③，2026-11-07）

> **问题来源**：材料 `tellurion:docs/design/qt-decoupling/core-governance-decision-materials-2026-10-26.md` §5.2 K3 列出本仓 DDL 里 **10 张表在今天没有任何本仓写者**（market 侧 7 + ingest 侧 3）。
> **取证方法**：在 core 仓 `pkg/storage/*.go` 逐文件 `git grep` 字面写点（`INSERT INTO` / `UPDATE` / `DELETE FROM`），并单独核对 `BulkInsert` 的动态拼表名路径——`tellurion:pkg/storage/bulk_insert.go:44-58` 的 `NewTableMapper()` 只映射 13 个 `data_type`，`:331` 用映射结果拼 `INSERT INTO %s`；**这 10 张均不在映射里**，所以不是「BulkInsert 漏传」，而是「本仓今天确实没有写者」。
> **结论分布**：9 张「补写者」+ 1 张显式声明「只由 core 写，本仓不拥有」+ 0 张弃表。**今天本仓对这 10 张的写者数 = 0**（本仓 `.go` 里只有 DDL，没有指向它们的 `INSERT` / `UPDATE` / `DELETE`）；补写者落地前，core 的写点仍是已登记的过渡写者，归零在 `tellurion:SVC-CORE-DATA-RETIRE`。

| 表 | 今天的写者（core 取证） | 处置 | 说明 |
|---|---|---|---|
| `stocks` | `tellurion:pkg/storage/stocks.go:29`（`SaveStock`）、`:61`（`SaveStockBatch`） | **补写者** | 标的基础数据，随 stock 同步腿收编进本仓；core 写点是过渡，不新开第二套语义 |
| `dividends` | `tellurion:pkg/storage/cache.go:280`（`SaveDividendBatch`） | **补写者** | 公司行为，属行情参考数据；同 `stock` 同步腿 |
| `splits` | `tellurion:pkg/storage/cache.go:499`（`SaveSplitBatch`） | **补写者** | 同上 |
| `trading_calendar` | `tellurion:pkg/storage/calendar.go:21`（`SaveTradingCalendarEntry`）、`:43`（`SaveTradingCalendarBatch`） | **补写者** | 按 §6.2 属 C 集：它是 `calendar_hash` 的输入、不是观测流，**不补 `visible_at`**；补写者不等于加列 |
| `index_constituents` | `tellurion:pkg/storage/cache.go:383`（`SaveIndexConstituentBatch`） | **补写者** | 前视风险在成分变更时点，归 resolve 的 `as_of`（§6.2 对表 D 组）；本仓补写者按同一语义 |
| `market.index_daily` | `tellurion:pkg/storage/index_daily.go:31`（`saveIndexDailySQL`） | **显式声明「只由 core 写，本仓不拥有」** | 裁决 ⑰ 明定归 core（`tellurion:docs/design/qt-decoupling/cross-repo-decision-record-2026-10-25.md` ⑰）。**2026-11-10 已施工**：本仓 DDL、`TableNames()` 条目、C 集豁免三处删除，并加守卫 `TestIndexDaily_IsNotOwnedByThisRepo`（本仓 statements 出现该表即红）——从「建表但不认领写权」收紧为「连表都不建」，因为两套 DDL 形状不同、先建表者定形 |
| `market_activity` | `tellurion:pkg/storage/market_activity.go:35`（`saveMarketActivitySQL`） | **补写者** | B 集：写者是 core 的 `daily_basic` 同步腿（不经 `BulkInsert`）；收编那条腿时由本仓补写者接管 |
| `ingest.raw` | ~~`tellurion:pkg/storage/ingest_raw.go:62`（`SaveRawIngest`）~~ **文件已删（`a96d836`，2026-11-08）** | ✅ **已接管（2026-11-12，ADR-0010）** | 本仓 `store.EvidenceStore` 独占写读（`POST /api/ingest/raw` / `GET /api/evidence[/{content_hash}]`）；落库前脱敏、保留期清理见 §9.5；core 只做读反代 |
| `sync_jobs` | ~~`tellurion:pkg/storage/sync_jobs.go:15`（`CreateSyncJob`）、`:107`（`UpdateSyncJobIfStatus`）、`:173`（`DeleteSyncJob`）~~ **文件已删（`a96d836`，2026-11-08）** | ✅ **已接管（2026-11-10，X1c）** | 本仓 `internal/sync.NewPGStore` 独占读写（`FOR UPDATE SKIP LOCKED` 领取、`FinishJob` 写终态）；`/api/sync/jobs*` 是唯一入口 |
| `sync_schedules` | ~~`tellurion:pkg/storage/sync_jobs.go:230`（`CreateSyncSchedule`）、`:326`（`UpdateSyncSchedule`）、`:346`（`DeleteSyncSchedule`）~~ **文件已删（`a96d836`，2026-11-08）** | ✅ **已接管（2026-11-10，X1c）** | 本仓 `internal/sync` 的 Scheduler（30s 扫 due + `EnqueueFromSchedule` 单事务入队并推进 `next_run_at`） |

> **未取证项**：本表只钉「core 有字面写点」与「本仓无写者」两件事；**各写点在运行期是否仍被调用**（例如某个 executor 已被 feature flag 关掉）不在本单取证范围，写「未取证」，不把「有字面 SQL」冒充「线上仍在写」。
> **护栏关系**：`internal/repoguard` 只约束**写语句的 schema**（本仓只许 `market.` / `ingest.` / `pg_catalog` / `information_schema` / 临时表），**不约束表覆盖**——K3 的 10 张属于「写者缺口」，不是「越界写」，两者不能互相冒名。

## 8. `DATA_MARKET_SCHEMA` 口径与物理搬迁前置（SVC-DATA-G2 ③④，2026-11-07）

**三句话钉死口径**：

1. **出厂值是 `market`**：`deploy/compose.yaml` 的 `DATA_MARKET_SCHEMA: market`；`cmd/dataservice/main.go` 只把它交给 `store.Options.MarketSchema`。
2. **它只决定本仓 SQL 的表名前缀，不决定 DDL 落点**：`applySchema` 固定执行 `schema.Statements()`，`market` / `ingest` 是 DDL 里的字面量；空值在 `internal/store` 的标识符拼装处回落为 `public`（注释写明是兼容 D4 之前的过渡值）。
3. ~~core 用裸表名写、core 仓内没有 `search_path` ⇒ 落 `public.*`~~ **（2026-11-08 起不再成立）**：core 的数据面写点已随 `a96d836` 全部删除；本机实例 2026-11-10 已执行 `SET SCHEMA`，`public` 侧 22 张同名表归零。今天 `DATA_MARKET_SCHEMA=market` 与 core 之间只剩 `market.index_daily`（裁决 ⑰：core 自带 schema 前缀，DDL 也在 core，本仓不建不读）。`ingest.raw` 在 core 侧零写点，本仓的归档面于 2026-11-12 落地（ADR-0010，见 §9.5）。

**物理 `SET SCHEMA` 搬迁的前置 = core 停写**（不是「本仓 DDL 先建好」）：

> **2026-11-10 状态**：前置已满足（core 写点归零），搬迁已在本机实例执行；下面三条保留为可复用的搬迁纪律，不再代表待办。

- 只要 core 还在用裸表名写，把 `public.x` 搬进 `market.x` 之后，core 的下一次启动迁移/写入会再次解析到 `public.x`（或再次 `CREATE TABLE IF NOT EXISTS x`），结果不是报错就是**悄悄长出第二份同名表**——两种都与「单一 writer」相反。
- 因此搬迁顺序只能是：**core 写点归零/删除 → 确认无裸名写 → 再 `SET SCHEMA`**。core 侧那一半是 `tellurion:SVC-CORE-DATA-RETIRE`（本仓不改 core 写点，见 G2 ④）。
- `DATA_MARKET_SCHEMA` 在代码里保留「空 = public」只是兼容保留；**它不是搬迁开关**：切这个变量不搬任何一行数据，也不改变 DDL 落点。出厂 compose 始终写 `market`，运维不要用改环境变量来代替 `SET SCHEMA`。

## 9. 逐表 writer / reader 清单（SVC-DATA-G3 ③，2026-11-10）

> **表名口径** = `internal/schema.TableNames()` 的 **22 张**（`market` 18 + `ingest` 4）。
> **取证方式**：本仓写者/读方逐条点名 `internal/store`、`internal/sync`、`internal/schema` 的 SQL 常量与端口实现；core 侧实测
> `git grep -nE '(INSERT INTO|UPDATE|DELETE FROM|TRUNCATE)[[:space:]]+(market|ingest)\.' -- '*.go' ':!*_test.go'` ⇒ 唯一命中 `pkg/storage/index_daily.go:31`（`market.index_daily`，**不属本仓 22 张**）。
> core `cmd/data` / `pkg/data` 已于 `a96d836`（2026-11-08）整体删除；`Test-Path cmd/data` / `Test-Path pkg/data` 均为 False。

### 9.1 有本仓写者的表（17 张）

| 表 | 本仓写者 | 本仓读方 |
|---|---|---|
| `market.ohlcv_daily_qfq` | `store.Writer.BulkInsert`（`POST /ingest/points`；同步作业 worker 同一写面） | `GET /ohlcv/{symbol}`、`POST /api/v1/ohlcv/bulk`、`GET /datasets/resolve`（`store.ReadBars`）、`GET /datasets/{hash}`（`store.DataVersion`） |
| `market.ohlcv_minute` | 同上 | `GET /datasets/resolve`（`1m`，`ReadBars`）、`GET /datasets/{hash}`（`DataVersion`）；**无**逐分钟兼容端点 |
| `market.realtime_quote` | 同上 | 无 |
| `market.capital_flow` | 同上（东财 connector / `POST /ingest/points` 投点） | 无 |
| `market.sectors` | 同上 | 无 |
| `market.stock_sector_map` | 同上 | 无 |
| `market.top_list` | 同上 | 无 |
| `market.limit_up_pool` | 同上 | 无 |
| `market.announcements` | 同上 | 无 |
| `market.news` | 同上 | 无 |
| `market.hot_search` | 同上 | 无 |
| `market.global_ohlcv` | 同上 | 无 |
| `market.stocks` | `store.ReferenceWriter.UpsertStocks`（作业类型 `stocks`，`ON CONFLICT (symbol)` upsert；**旁路点位闸门**；解析契约由 fixture 钉住，缺 `delist_date` 即拒写；含 `delist_date`，见 ADR-0009 决策二） | `GET /stocks`、`GET /api/v1/stocks/{symbol}` |
| `market.trading_calendar` | `store.ReferenceWriter.UpsertTradingDays`（作业类型 `trading_calendar`，`ON CONFLICT (trade_date)` upsert；缺 `is_open` 即拒写；**单交易日历**，混交易所批次拒写，`exchange` 仅 provenance，见 ADR-0009） | `GET /api/v1/trading/calendar` |
| `ingest.sync_jobs` | `internal/sync`（`PGStore`，11 条作业/排程端点） | 同左（`GET /api/sync/jobs*`） |
| `ingest.sync_schedules` | 同上 | 同上 |
| `ingest.raw` | `store.EvidenceStore.Put`（`POST /api/ingest/raw`；单写者 = 本仓；`ON CONFLICT (content_hash) DO NOTHING`，冲突内容比对） | `GET /api/evidence/{content_hash}`、`GET /api/evidence`；保留期清理 `PurgeExpired`（§9.5） |

> 参考数据的解析契约（`stock_basic` / `trade_cal` 字段形状、缺列 fail-closed）由 `internal/sync/reference_contract_test.go` + `testdata/*.json` 钉住；2026-11-11 已用 `TUSHARE_TOKEN` 线上核对（L 5580 行 / D 343 行 / `trade_cal` 4 列），**未**走过生产部署后的作业级写入。

### 9.2 零写者的表（4 张 + 1 张哨兵；均为「挂起」，见 ADR-0008）

| 表 | 本仓写者 | 本仓读方 | 处置（2026-11-10） |
|---|---|---|---|
| `market.dividends` | 无 | 无 | ⏸ 挂起：无消费者、无 connector。重开 = 公司行为/复权消费方出现 |
| `market.splits` | 无 | 无 | ⏸ 挂起：同上 |
| `market.index_constituents` | 无 | `GET /index/{code}/constituents`、`GET /datasets/resolve` 的 universe 判定 | ⏸ 挂起：Tushare `index_weight` 只给周期快照权重，`in_date/out_date` 区间需多期快照推导；当前无消费者驱动。重开 = 确定指数集与来源后另立单 |
| `market.market_activity` | 无 | 无 | ⏸ 挂起：B 集（`as_of` 语义）；`daily_basic` 传输可取数但写面被 `writeExcludedTypes` 排除，补 B 集写者属另一设计。重开 = B 集读消费者出现 |
| `ingest.data_migrations` | 仅 `schema.BackfillStatements()` 哨兵（`-tags backfill`，运维执行） | 回填 guard | 不是日常写者，不计入上表 |

> `ingest.raw` 已移出本表：2026-11-12 起由本仓证据归档面接管（ADR-0010，见 §9.1 与 §9.5）。

### 9.3 与本仓相关但不属 22 张的两张表

| 表 | 归属 | 写者 | 本仓读方 |
|---|---|---|---|
| `market.index_daily` | **core**（DDL 与读写都在 core，裁决 ⑰） | core `pkg/storage/index_daily.go:31` | 无（本仓不建、不读） |
| `stock_fundamentals` | **core**（DDL 与写点都在 core，裸表名 ⇒ `public.stock_fundamentals`） | core | **本仓不读**（2026-11-12 归属裁决）：基本面读面是 core 的 `GET /api/fundamental/{symbol}`，空态 404 `fundamental_not_found`；本仓不补空表、不冒充读面 |

### 9.4 结论

- **G3 ①「唯一 writer 变成本仓」成立**：core 非测试代码对 `market.*` / `ingest.*` 的写点 = 0（唯一例外是裁决 ⑰ 显式留给 core 的 `market.index_daily`）。
- **零写者已从 7 张降到 4 张（2026-11-12 证据归档面落地后再降 1）**：`stocks` / `trading_calendar` 补了参考数据写者（ADR-0008），`ingest.raw` 由证据归档面接管（ADR-0010）；剩余 `dividends` / `splits` / `index_constituents` / `market_activity` 全部登记为 **⏸ 挂起**（各带重开条件，不计开放项），不再有「无归属」的零写者。
- **读方覆盖**：9 张业务表 + 2 张作业表有本仓读端点（`ohlcv_daily_qfq` / `ohlcv_minute` / `stocks` / `trading_calendar` / `index_constituents` / `ingest.raw` / `sync_jobs` / `sync_schedules`）；其余 13 张中 10 张 A 集只写不读、3 张挂起零写零读、1 张回填哨兵。
- **X1d 物理 `SET SCHEMA` + `visible_at` 回填已在本机实例执行（2026-11-10）**：22 张目标表搬迁、`public` 归 0、A 集 NULL 归 0、哨兵 12、幂等复跑通过（`docs/archive/x1d-local-replay-2026-11-10.md`）；未接触远端/生产库。

### 9.5 `ingest.raw` 证据归档面口径（2026-11-12，ADR-0010）

**归属与写者**：`ingest.raw` 的唯一写者是 data 服务（`store.EvidenceStore.Put`，入口 `POST /api/ingest/raw`）。core 只保留 `GET /api/evidence/{content_hash}` 读反代，**不持有第二份证据存储**；出现第二个写者 = 边界已腐化（§1.5 红线）。

**表结构与索引**：DDL 在 `internal/schema/ingest.go`（`content_hash` 主键）；读面按 `fetched_at DESC, content_hash DESC` 排序，2026-11-12 补索引 `idx_ingest_raw_fetched_at (fetched_at DESC)`，与保留期清理共用。

**内容哈希（内容寻址）**：`content_hash` = **脱敏后** payload 的规范化 JSON 的 sha256（小写 hex，64 位）。规范化 = `json.Decoder.UseNumber` 解码 → `json.Marshal`（map 键字典序）。先脱敏再哈希，保证引用方拿到的哈希能读回同一条记录。同 hash 重复写为 no-op；同 hash 异内容 → 500，不覆盖。

**落库前脱敏**：递归遍历 payload；键名归一（小写、去掉 `-` 与 `_`）后命中下列集合的值替换为 `"[REDACTED]"`（数组/嵌套对象递归，非字符串值同样替换）：

```
token, accesstoken, refreshtoken, secret, clientsecret, password, passwd,
apikey, authorization, cookie, setcookie, credential, credentials,
session, sessionid, privatekey
```

规则实现与测试在 `internal/evidence`（`RedactedKeyNames()` 导出集合，`TestRedactMasksSensitiveFields` 钉住）；改集合必须同批改本节与测试。

**保留期（必须落地，不允许「以后再说」）**：

- 默认 **365 天**；`EVIDENCE_RETENTION_DAYS` 可覆盖。
- 口径：`fetched_at < now - N 天` 的记录视为过期。
- **0 / 负数 / 非整数在启动期直接拒起**（`evidence.RetentionDays` + `cmd/dataservice` 装配）；不允许用 0 隐式关掉清理。
- 清理：`store.EvidenceStore.PurgeExpired`，在 schema 就绪后立即执行一次，此后每 **24h** 一次；只打删除条数日志，不参与读面判定。
- 容量上限：单条请求体 ≤ 8 MiB；分页读面 `limit` ≤ 100。超出保留期的归档不再可查（这是明确的运维口径，不是缺陷）。

**不做回放**：归档面只提供「按内容哈希查阅一条不可变记录」。设计原文（ADR-022 §5）与 UI A-06 消费契约都只要求可查阅；回放/重放消费者若将来出现，另立 ADR，不在本表上顺手扩。


