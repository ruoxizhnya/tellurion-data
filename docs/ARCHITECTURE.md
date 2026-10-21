---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
---

# 架构 — tellurion-data

## 1. 定位

数据服务：**统一数据协议 + connector 归一化 + aggregator**。它存在的理由不是"把取数拆出去"，而是让**数据本身带上时间语义**，从根上堵住前视。

## 2. 分层

```
connector（12 个，可增）
      ↓  字段映射 + 声明 TimeSemantics/FrequencySpec
Normalizer + ValidatePoints + Deduplicate
      ↓  UnifiedDataPoint（含 visible_at）
market / ingest  （DDL 唯一 owner）
      ↓
aggregator  PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存
      ↓  对外唯一面
core / analytics / strategy / agent
```

## 3. 组件职责

| 层 | 职责 | 不该做什么 |
|---|---|---|
| connector | 对接源、字段映射、声明语义 | 不做归一化（那是共享函数的事） |
| Normalizer | 统一成协议类型 | 不做业务聚合 |
| 存储 | 真相源 | 不对外 |
| aggregator | PIT / 聚合 / 票池 / hash / 缓存 | 不做业务计算 |

## 4. 三份契约（功能的边界就在这三份文件里）

| 维度 | 文件 | 回答什么问题 |
|---|---|---|
| 数据形状 | [`db-schema.md`](db-schema.md) | 本仓拥有哪些表？谁能写？ |
| 接口形状 | [`api-schema.md`](api-schema.md) | 对外暴露什么？错误码与幂等怎么做？ |
| 流向与顺序 | [`dataflow.md`](dataflow.md) | 数据怎么流？哪里 fail-closed？ |

## 5. 依赖方向

```
contracts（协议类型） + kit（logging/http/db）
        ↑
  tellurion-data  ◄──(HTTP)──  core / analytics / strategy / agent
```

> **箭头图例**（与 `tellurion-docs:docs/REPO-MAP.md` 同向）：`A ──► B` = A 调用 B，箭头指向**被调方**。四方经 HTTP 调用本仓「对外面」（run 前取数 / 异步），本仓只供数、不反向调用（2026-10-21 审查 M24 订正：原图箭头反向，读作「data 调四方」）。

## 6. 关键决策

- [`adr/0001-统一协议与-visible-at-第二道防线.md`](adr/0001-统一协议与-visible-at-第二道防线.md)

## 7. 非目标（明确不做）

- 不做逐 bar / 逐笔远程取数
- 不做策略或因子计算（只提供已落库的因子值）
- 不做回测结果存储
- 不做跨 schema join

## 8. 分期与状态

| 阶段 | 内容 | 验收 |
|---|---|---|
| X1a | 协议冻结进 contracts（类型 + TimeSemantics + 错误码） | core/agent 只依赖 contracts |
| X1b | aggregator（PIT + 聚合 + 票池 + hash） | 同 hash 两次一致 |
| X1c | connector 契约收口 | 漏声明被拒收 |
| X1d | schema 归属迁移 | 无第二写者 |
| X0 | `dataset_snapshot_hash` 记入结果 | 与 `strategy_ref` 同款留痕 |
