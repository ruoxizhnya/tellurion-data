// Package data 是 tellurion-data 的根包（仅文档占位，不放实现）。
//
// 实现按 `docs/ARCHITECTURE.md` §2.1 的限界上下文拆子包，今天已落地三个：
//
//   - `internal/ingest`：connector **注册门**（声明缺失 / 非法 / 与 wire schema
//     矛盾者 fail-closed 拒收）+ 12 个 connector 的取数与归一化实现；
//     声明真源是每个 connector 自己的 `Declarations()`（过渡期 `Catalog()`
//     清单已删除）。传输实现仍在 core，经 `Transports` 注入；
//   - `internal/schema`：`market` / `ingest` 两个 schema 的 DDL 唯一 owner
//     （建表 + `visible_at` 三集合 + 哨兵防重放的回填腿）；
//   - `internal/store`：四个取数 port 的 PG 实现（只读 `market`）；
//   - `internal/normalize`：频率聚合（1m → 15m / 1d）与 PIT 闸门
//     （`visible_at <= as_of`，缺可见时间点整批拒收）；
//   - `internal/serving`：aggregator —— 票池解析 → 取数 → 聚合 → PIT →
//     `dataset_snapshot_hash`（调 contracts 唯一定义）→ 强幂等缓存。
//
// `internal/derived` / `internal/evidence` / `internal/sync` 尚未建包（代码仍在
// core）。本文件只让 `go build ./...` 与 `go list ./...` 在根目录有文件。
package data
