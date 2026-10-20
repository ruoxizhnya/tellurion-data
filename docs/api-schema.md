---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
---

# API Schema — tellurion-data

> **契约地位**：HTTP，对外唯一数据面。**aggregator 是唯一入口**，connector 与存储不直接对外。
> 本文件是**本仓对外接口的唯一真相源**。代码与它不一致时，先判断谁错：契约错 → 改本文 + 立 ADR；实现错 → 改代码。
> 跨仓消费方（core / agent / ui / 其他服务）按本文件编码，**不读本仓实现**。

## 1. 接口面

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/datasets/resolve?universe=&from=&to=&freq=` | 解析票池 + 窗口 + 频率 → `{dataset_hash, metadata}` |
| GET | `/datasets/{hash}` | 按 hash 取数据（同一 hash 两次结果必须一致） |
| GET | `/factors/{name}@{hash}` | 按数据集 hash 取因子值 |

内部链路（不对外）：`connector` → `Normalizer` → `ValidatePoints` → `Deduplicate` → 存储。

## 2. 错误码与 fail-closed 语义

| 错误 | 触发 | 调用方行为 |
|---|---|---|
| `ErrNoTimeSemantics` | connector 未声明时间语义 | 拒绝该 connector（fail-closed，不许猜） |
| `ErrUnknownDataType` | 请求了未登记的数据类型 | 400，不返回空集 |
| `ErrVisibleAtMissing` | 数据点缺 `visible_at` | 拒收该批次；**不许用 mtime 兜底** |
| `ErrSnapshotMismatch` | hash 与返回内容不一致 | 500 + 告警（数据已被篡改或缓存串了） |

## 3. 幂等性与重试

- `dataset_hash` 是内容寻址：票池 + 窗口 + 频率 + 数据版本任一变化 → 新 hash。
- 同一 hash 的读请求可任意重试（无副作用）。
- 写入幂等靠 `ingest.data_sync_state` 的同步水位 + 数据点主键。

## 4. 版本与兼容

- 协议类型（`UnifiedDataPoint` 等）住在 contracts，版本随契约走。
- 端点只增不删；废弃走 `deprecated` 头 + 一个 MINOR 周期。
- 字段语义变更（尤其 `visible_at`）算 breaking，必须走 ADR。

## 5. 不提供什么（边界）

- 不提供逐 bar / 逐笔取数接口
- 不提供回测结果存储（那是 run）
- 不提供策略 / 因子**计算**（只提供已落库的因子值）
- 不提供跨 schema join（宁可多一次调用）
