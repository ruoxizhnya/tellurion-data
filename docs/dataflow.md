---
status: active
last-verified: 2026-10-23
verified-by: QT-40 X1b/X1c 核心落地 —— 补「代码落点」一条（链路本身未改）
---

# Dataflow — tellurion-data

> 目的：把「数据从哪来、在谁手里变形、到哪去」写死。**任何跨仓数据交换都必须能在本图上找到一条边**；找不到 = 这段代码在偷偷建第二条路。

## 1. 主链路

```
[Tushare] [东财] [雪球/Yahoo] [巨潮/HKEX] …        ← connector（12 个，可增）
        │ 字段映射 + 声明 TimeSemantics / FrequencySpec
        ↓
[归一化]  Normalizer（共享函数） + ValidatePoints + Deduplicate
        │ 统一成 UnifiedDataPoint（含 visible_at、幂等坐标）
        ↓
[存储]  market / ingest（目标态；今天多数表落 public，X1d 分批迁，现状见 db-schema.md §1）  ← 内容真相源，DDL 唯一 owner = 本仓
        ↓
[aggregator]  PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存 · 因子值   ← 对外唯一面
        ↓  /datasets/resolve · /datasets/{hash} · /factors/{name}@{hash}
core（回测/实盘）   analytics（基准/归因）   strategy（资产）   agent（实验）

（过渡第二路径，X1d 收敛）core ──直读──▶ factor_definitions / factor_cache（因子定义无对外面）
```

> **频率分表与聚合口径**（`tellurion:docs/SPEC.md` 频率词表）：`1m` = `ohlcv_minute` 原样；`1d` = `ohlcv_daily_qfq`（唯一进回测的口径）；`15m` **由 1m 聚合产出、不物化**，每次取数实时聚合。图中与 §2 的「频率聚合」只指这一种。
> **代码落点（2026-10-24）**：图中「归一化」的**聚合与 PIT 两段**在 `tellurion-data:internal/normalize`，「aggregator」在 `tellurion-data:internal/serving`；四个取数 port 的 PG 实现在 `tellurion-data:internal/store`（只读 `market`，`visible_at` 只从列里取、缺列交 PIT 整批拒收）；connector 取数腿与存储写路径仍在 core（X1c/X1d 搬迁中）。
> **因子边**：因子**值**走对外面 `GET /factors/{name}@{hash}`（见 `api-schema.md` §1）；因子**定义**（`factor_definitions`）无对外面，今天 core 直读 `factor_definitions` / `factor_cache` —— **过渡第二路径**（如实登记上图），X1d 后收敛为只经 API（见 `db-schema.md` §5）。

## 2. 关键时序

### 取数（读路径，热）
1. 调用方 `GET /datasets/resolve?universe=csi300&from=…&to=…&freq=1d`
2. aggregator：票池解析 → 按 `visible_at <= as_of` 过滤 → 频率聚合（口径见 §1 注）→ 算 `dataset_hash`
3. 返回 `{dataset_hash, metadata}`；调用方拿 hash 请求 `/datasets/{hash}`
4. 命中缓存直接返回；未命中从 `market` 读并回填

> **红线**：热读路径**仅供 run 前一次快照预加载**（回测启动前取完全区间进引擎快照），**禁止在回测循环内调用**——策略运行时不得直查数据面（`tellurion:docs/SPEC.md`「快照预加载，不许运行时查询」）。

### 入库（写路径，冷）
1. 触发：`POST /api/sync/jobs`（或调度器按 `sync_schedules` 投递）→ `sync_jobs` → worker 领取 → executor → ETL（下面 2–5 步）
2. connector 拉原始数据 → 声明 `TimeSemantics` / `FrequencySpec`
3. Normalizer 映射成 `UnifiedDataPoint`，填 `visible_at`
4. `ValidatePoints` 拒收缺失/异常；`Deduplicate` 按幂等坐标去重
5. 写 `market` 域各表（真名见 `db-schema.md` §1），更新 `sync_jobs` / `sync_schedules` 水位

> **红线**：同步 HTTP 端点**不得承载无界批量**——无界批量一律走作业层（`POST /api/sync/jobs` → `sync_jobs` → worker → executor → ETL，`tellurion:docs/SPEC.md` DATA-10）；HTTP 灌数入口只许单次有界任务（DATA-12：以「没有无界批量问题」为前提）。

## 3. 失败模式（每一处 fail-closed 都要能指名）

| 失败点 | 症状 | 兜底 |
|---|---|---|
| connector 漏声明时间语义 | 数据可能前视 | `ErrDeclarationMissing` 在**注册期**拒收（`Registry.Register`，已有测试；旧名 `ErrNoTimeSemantics` 作废） |
| `visible_at` 用错锚点 | 回测虚高、实盘崩 | 归一化层强制：锚点=**披露日（ann_date）**，不是报告期截止日（`tellurion:docs/SPEC.md` 时间语义）+ 用例钉住财报场景 |
| 上游改了历史数据 | 同参回测结果漂移 | `dataset_hash` 变化 → 结果可追溯到"哪版数据" |
| 缓存串号 | 返回别人的数据 | `ErrSnapshotMismatch` + 500 |

## 4. 不变量（可被测试钉住的）

1. 同一 `dataset_hash` 两次拉取字节一致。
2. 任何返回点满足 `visible_at <= as_of`。
3. `market` / `ingest` 无第二写者（CI 静态检查 + 评审）。
4. connector 全部走 `ConnectorDeclaration`（漏声明被拒收）。

## 5. 与契约文档的关系

- 表结构 → `docs/db-schema.md`
- 接口形状 → `docs/api-schema.md`
- 本文件只讲**流向与顺序**，不重复字段定义。
