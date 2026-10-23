# tellurion-data

> module: `tellurion-data` ｜ 状态：**X1 施工中** —— `internal/ingest` / `internal/normalize` / `internal/serving` 已落地（注册门 + 频率聚合/PIT + aggregator/缓存），HTTP 面与取数存储 port 未接线（见 `docs/TASKS.md` QT-40）

数据服务：统一数据协议 + connector / aggregator + `dataset_snapshot_hash`。**只走高频/低频路径，逐 bar / 逐笔永不远程**。

## 目录

| 路径 | 内容 |
|---|---|
| `docs/` | 本仓文档：台账 / 边界 / 决策 / 归档 —— 从这里进：`docs/README.md` |
| `AGENTS.md` | 本仓入口（构建、边界、禁止、依赖版本） |
| `internal/ingest` | connector 注册门 + 12 connector 声明清单（catalog） |
| `internal/normalize` | 频率聚合（1m → 15m/1d）+ PIT 闸门 |
| `internal/serving` | aggregator（票池解析 · 取数 · 聚合 · PIT · hash · 缓存） |
| `doc.go` | 根包文档占位（无实现） |

## 快速开始

```bash
go build ./...
go vet ./...
go test ./...
```

本仓**单独 checkout 就能跑通**上面三条。需要别的仓源码才过 → 边界错了，不是加 `replace` 能解决的问题。

## 边界

**可以**：统一数据协议与 connector / aggregator；`dataset_snapshot_hash` 的计算与登记；数据质量与缺口报告

**不可以**：逐 bar / 逐笔的远程取数（硬否决：延迟预算不允许）；import core 的实现包；直连 core 的业务表

## 文档

- 文档导航：`docs/README.md`
- 本仓台账：`docs/TASKS.md`（唯一；跨仓任务只留指针）
- 边界与依赖方向：`docs/ARCHITECTURE.md`
- 决策记录：`docs/adr/`

## 许可

MIT（见 `LICENSE`）。
