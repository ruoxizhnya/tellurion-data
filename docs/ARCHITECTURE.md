# 本仓边界与依赖方向 — tellurion-data

## 定位

数据服务：统一数据协议 + connector / aggregator + `dataset_snapshot_hash`。**只走高频/低频路径，逐 bar / 逐笔永不远程**。

状态：**预留（S0 占位，触发式启动）——尚未启动，无实现**

## 依赖方向（编译器强制）

```
contracts + kit
        ↑
  tellurion-data   ──(HTTP)──▶  core（运行期）
```

- 箭头 = import 方向。**不允许反向**，也不允许成环 —— 反向 import 应当**编译失败**，这是判据不是口号。
- 跨仓只认版本号：`tellurion-data` 以需要的版本出现在下游 `go.mod`。

## 边界表

| | 内容 |
|---|---|
| 允许 | 统一数据协议与 connector / aggregator；`dataset_snapshot_hash` 的计算与登记；数据质量与缺口报告 |
| 禁止 | 逐 bar / 逐笔的远程取数（硬否决：延迟预算不允许）；import core 的实现包；直连 core 的业务表 |

## 关键约束

1. **单仓可构建**：`go build ./...` / `go vet ./...` / `go test ./...` 不依赖任何兄弟目录。
2. **无 `replace`**：指向仓外的 `replace` 一律不接受（CI 会查）。
3. **无 `go.work` 入库**：本地联调可以建，提交即错。
4. **单段短名 module**：`tellurion-data` 无点号，Go 拒绝 `require` 它 —— 这是有意的（应用/服务不可被 import）。

## 未决 / 待定

- 本仓是否真启动取决于 X 线触发条件（多机部署 / 算力常态化打满 / 团队按服务划分）。触发前**不要写实现**。
