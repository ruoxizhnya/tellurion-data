---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
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
[存储]  market / ingest  （真相源，DDL 唯一 owner = 本仓）
        ↓
[aggregator]  PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存   ← 对外唯一面
        ↓
core（回测/实盘）   analytics（基准/归因）   strategy（资产）   agent（实验）
```

## 2. 关键时序

### 取数（读路径，热）
1. 调用方 `GET /datasets/resolve?universe=csi300&from=…&to=…&freq=1d`
2. aggregator：票池解析 → 按 `visible_at <= current_bar_time` 过滤 → 频率聚合 → 算 `dataset_hash`
3. 返回 `{dataset_hash, metadata}`；调用方拿 hash 请求 `/datasets/{hash}`
4. 命中缓存直接返回；未命中从 `market` 读并回填

### 入库（写路径，冷）
1. connector 拉原始数据 → 声明 `TimeSemantics` / `FrequencySpec`
2. Normalizer 映射成 `UnifiedDataPoint`，填 `visible_at`
3. `ValidatePoints` 拒收缺失/异常；`Deduplicate` 按幂等坐标去重
4. 写 `market` 域各表（真名见 `db-schema.md` §1），更新 `sync_jobs` / `sync_schedules` 水位

## 3. 失败模式（每一处 fail-closed 都要能指名）

| 失败点 | 症状 | 兜底 |
|---|---|---|
| connector 漏声明时间语义 | 数据可能前视 | `ErrNoTimeSemantics` 拒收（已有测试） |
| `visible_at` 用错锚点 | 回测虚高、实盘崩 | 归一化层强制 + 用例钉住财报场景 |
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
