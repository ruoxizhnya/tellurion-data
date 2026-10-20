# AGENTS — tellurion-data

## 这是什么仓

数据服务：行情与基本面的采集、归一、落库、对外查询。来源为 core 的 `cmd/data`（已有 `main`，:8081）+ `pkg/marketdata` + `pkg/data/*`。

状态：**预留（触发式）**——当前为占位仓，**未启动**。触发条件：需要独立伸缩或独立部署，且只走高/低频路径（逐 bar / 逐笔永不远程）。启动前先在 `tellurion:docs/TASKS.md` 立单。

## 构建 / 测试

```bash
go build ./...
go vet ./...
go test ./...
```

本仓必须**单独 checkout 就能跑通**这三条（R1/R2）。若某一步需要别的仓的源码目录，
那是**契约画错了**，不是正常状态——停下来先修边界，不要加 `replace ../xxx`。

## 边界（硬约束）

允许：
- 持有 `market` / `ingest` schema 的 DDL（唯一 owner）
- 对外提供批量行情/基本面查询的 HTTP 接口

禁止：
- 在回测内循环里被逐 bar 调用（硬否决）
- 把字段语义归一放到 connector 里（connector 只做字段映射，语义归一在共享 Normalizer）
- `replace` 指向仓外路径；`go.work*` 提交入库。
- 引入未在本文件声明的第三方依赖（先改本文件，再改代码）。

## 依赖的契约版本

| 依赖 | 版本 | 说明 |
|---|---|---|
| `github.com/ruoxizhnya/tellurion-contracts` | 待定（S2 后 `v0.1.0`） | 契约变更只走 tag，不走源码路径 |
| `github.com/ruoxizhnya/tellurion-kit` | 待定（S2 后 `v0.1.0`） | 同上 |

## 该做什么

**唯一台账是 `tellurion` 仓的 `docs/TASKS.md`。** 本仓没有独立 TASKS，
跨仓只留指针、不镜像正文（避免双写漂移）。

开工前先读：
1. `tellurion:docs/design/qt-decoupling/repo-inventory.md` —— 仓名与 module 规则
2. `tellurion:docs/TASKS.md` —— 找本仓的单号；没有单号就别开工
3. `tellurion:docs/design/qt-decoupling/p0-round2-review.md` §4 —— wave 顺序
