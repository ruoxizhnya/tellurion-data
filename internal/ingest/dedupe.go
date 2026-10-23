package ingest

import "time"

// 去重口径与 core `tellurion:pkg/data/source/unified.go` 逐字一致：
// `symbol | data_type | trade_time(RFC3339Nano, UTC)`。
//
// 为什么带 data_type：同一个 symbol 同一时刻可以同时有日线、资金流、
// 实时快照三个点，它们写不同的表，不能互相去重。
func DeduplicateKey(p UnifiedDataPoint) string {
	return p.Symbol + "|" + p.DataType + "|" + p.TradeTime.UTC().Format(time.RFC3339Nano)
}

// Deduplicate 保留每个键的**首次出现**，返回新切片（输入不变）。
func Deduplicate(points []UnifiedDataPoint) []UnifiedDataPoint {
	out, _ := DeduplicateWithCount(points)
	return out
}

// DeduplicateWithCount 同 Deduplicate，并返回被丢弃的重复条数。
//
// 保留首次而不是最后一次：同一次取数里后出现的重复项不算「更新」，
// 真正的更新由写入面的内容比对（同 hash no-op / 异内容 fail-loud）决定。
func DeduplicateWithCount(points []UnifiedDataPoint) ([]UnifiedDataPoint, int) {
	seen := make(map[string]struct{}, len(points))
	out := make([]UnifiedDataPoint, 0, len(points))
	for _, p := range points {
		key := DeduplicateKey(p)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out, len(points) - len(out)
}

// ValidatePoints 丢弃形态不完整的点，返回有效点与被丢弃条数。
//
// 三条判据与 core `ValidatePoints` 同形：
//   - Symbol 非空（无主的点写不进任何一张表）；
//   - TradeTime 非零（它是主键的一半）；
//   - Data 非 nil（空载荷不是「缺失≠0」，是「什么都没取到」）。
func ValidatePoints(points []UnifiedDataPoint) ([]UnifiedDataPoint, int) {
	out := make([]UnifiedDataPoint, 0, len(points))
	for _, p := range points {
		if p.Symbol == "" || p.TradeTime.IsZero() || p.Data == nil {
			continue
		}
		out = append(out, p)
	}
	return out, len(points) - len(out)
}
