// Package data 是 tellurion-data 的根包（仅文档占位，不放实现）。
//
// 实现按 `docs/ARCHITECTURE.md` §2.1 的限界上下文拆子包，今天已落地三个：
//
//   - `internal/ingest`：connector **注册门**（声明缺失 / 非法 / 与 wire schema
//     矛盾者 fail-closed 拒收）+ 12 个 connector 的声明清单（catalog 目标形状）。
//     取数传输（Fetch / HealthCheck / RateLimit）仍在 core，随 X1c 迁入；
//   - `internal/normalize`：频率聚合（1m → 15m / 1d）与 PIT 闸门
//     （`visible_at <= as_of`，缺可见时间点整批拒收）；
//   - `internal/serving`：aggregator —— 票池解析 → 取数 → 聚合 → PIT →
//     `dataset_snapshot_hash`（调 contracts 唯一定义）→ 强幂等缓存。
//
// `internal/derived` / `internal/evidence` / `internal/sync` 尚未建包（代码仍在
// core）。本文件只让 `go build ./...` 与 `go list ./...` 在根目录有文件。
package data
