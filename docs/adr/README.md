# ADR — tellurion-data 的决策记录

> 只记**本仓**的决策。跨仓/全局决策在 `tellurion:docs/design/qt-decoupling/project-engineering-file.md`，
> 不要在这里抄一遍（双写必然漂移）。
> 与 ODR 的分工见 [`../odr/README.md`](../odr/README.md)。

## 什么时候写一条

1. 做了一个**以后会被问「为什么」**的选择（依赖方向、分层、命名、存储形态）；
2. 推翻 / 替换了本仓既有决策（**新增一条**并注明替代哪条，不删旧的）；
3. 接受了一个已知代价（写清代价，否则半年后没人知道这是有意为之）。

不写：实现细节、临时 workaround、一次性调试结论。

## 编号与命名

`NNNN-kebab-title.md`，四位递增。`0000-template.md` 是模板，不编号。

## 索引

| 编号 | 标题 | 状态 |
|---|---|---|
| [0001](0001-统一协议与-visible-at-第二道防线.md) | 统一协议与 visible-at 第二道防线 | 已裁决 |
| [0002](0002-market-ingest-ddl归属与-visible-at-三集合.md) | market / ingest 的 DDL 归属本仓 + visible_at 三集合 | 已裁决 |
| [0003](0003-connector搬入本仓传输以窄-port-留在-core.md) | connector 搬入本仓，传输以窄 port 留在 core | 已裁决 |
| [0004](0004-写面归属-store-与幂等内容比对.md) | 写面归属 store，幂等用内容比对而非 last-write-wins | 已裁决 |
| [0005](0005-鉴权接入-kit适配与REV-17移植.md) | 鉴权接入：kit net/http 适配 + REV-17 移植 | 已裁决（2026-10-25） |
