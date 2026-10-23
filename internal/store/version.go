package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// DataVersion 实现 `serving.DataVersioner`：返回「这一版数据」的身份。
//
// 口径 = **表水位 + 行数**（`<freq>:<max>:<count>`），例如
// `1d:2026-10-23+n=3926510`。为什么行数也进版本：
//   - 只取 `max(trade_date)` 时，**补历史**（回填三年前的缺口）不会改水位，
//     同 hash 会指向内容不同的数据集——正是 `dataset_snapshot_hash` 要防的
//     「同 hash 不同结果」；
//   - 行数变了说明内容集合变了，hash 必须跟着变。
//
// 代价如实登记：`count(*)` 在千万级表上是一次全表扫描。它是 **run 前一次**
// 的物化成本（热读路径禁止在回测循环内调用，`docs/dataflow.md` §2 红线），
// 不在逐 bar 路径上。
func (s *Store) DataVersion(ctx context.Context, frequency market.Frequency) (string, error) {
	source, table, column, err := versionSource(frequency)
	if err != nil {
		return "", err
	}

	var (
		watermark string
		count     int64
	)
	sql := `SELECT COALESCE(MAX(` + column + `)::text, ''), COUNT(*) FROM ` + s.table(table)
	err = s.scanAll(ctx, sql, nil, func(rows Rows) error {
		return rows.Scan(&watermark, &count)
	})
	if err != nil {
		return "", fmt.Errorf("store: data version for %s: %w", source, err)
	}
	if strings.TrimSpace(watermark) == "" {
		// 空表不是「版本空」而是「没有数据」：返回空串让 aggregator 用
		// ErrEmptyDataVersion 拒收，而不是编一个 `1d:0` 看起来像正常的版本。
		return "", nil
	}
	return fmt.Sprintf("%s:%s+n=%d", source, watermark, count), nil
}

// versionSource 把目标频率映射到「读哪张表的水位」。15m 不物化 ⇒ 用水位
// 源 1m，与取数口径（`sourceFrequency`）保持一致，避免两处各写一份映射。
func versionSource(frequency market.Frequency) (source string, table string, column string, err error) {
	switch frequency {
	case market.Freq1d:
		return "1d", "ohlcv_daily_qfq", "trade_date", nil
	case market.Freq1m:
		return "1m", "ohlcv_minute", "ts", nil
	case market.Freq15m:
		return "1m", "ohlcv_minute", "ts", nil
	default:
		return "", "", "", fmt.Errorf("%w: %q", ErrUnsupportedFrequency, frequency)
	}
}
