# tellurion-data

> module: `tellurion-data` ｜ 状态：**预留（触发式）**——当前为占位仓，**未启动**。触发条件：需要独立伸缩或独立部署，且只走高/低频路径（逐 bar / 逐笔永不远程）。启动前先在 `tellurion:docs/TASKS.md` 立单。

数据服务：行情与基本面的采集、归一、落库、对外查询。来源为 core 的 `cmd/data`（已有 `main`，:8081）+ `pkg/marketdata` + `pkg/data/*`。

这是六个服务里**唯二已有进程入口**的仓（另一个是 analytics），拆出成本最低。

## 边界

**可以**
- 持有 `market` / `ingest` schema 的 DDL（唯一 owner）
- 对外提供批量行情/基本面查询的 HTTP 接口

**不可以**
- 在回测内循环里被逐 bar 调用（硬否决）
- 把字段语义归一放到 connector 里（connector 只做字段映射，语义归一在共享 Normalizer）

## 依赖方向

```
tellurion-data
```

- 契约与工具一律以 **tag** 依赖（`github.com/ruoxizhnya/tellurion-contracts` / `-kit`），
  **禁止 `replace` 指向仓外路径**（单仓不可构建）。
- `go.work` 一律不入库。

## 施工入口

本仓所有任务登记在 **`tellurion` 仓的 `docs/TASKS.md`**（单一台账，避免双写）。
在台账出现本仓的单号之前，**不要在此写实现**。

## 权威文档

| 主题 | 位置 |
|---|---|
| 仓清单与命名 | `tellurion:docs/design/qt-decoupling/repo-inventory.md` |
| 分仓拓扑与阶梯 | `tellurion:docs/design/qt-decoupling/repo-topology-and-naming.md` |
| P0 修复基线 | `tellurion:docs/design/qt-decoupling/p0-round2-review.md` |
