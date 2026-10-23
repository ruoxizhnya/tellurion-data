package store

import (
	"context"
	"fmt"
	"time"
)

// TradingDays 实现 `serving.TradingCalendar`。
//
// 返回**窗口内**的交易日轴（保序、严格递增）。三条判据：
//   - 只取 `is_trading_day = TRUE` 的行——日历表里也可能登记休市日，
//     拿它当轴会让 `calendar_hash` 与真实可交易日错位；
//   - 折成 UTC 日历日零点（`calendar_hash` 按 `d.UTC().Format("2006-01-02")`
//     编码，形态必须稳定）；
//   - 空轴不在这里补：`serving.Resolve` 用 `ErrEmptyCalendar` 拒收，
//     「这窗口没有交易日」是调用方要看见的事实，不是可以填一个默认值的空缺。
func (s *Store) TradingDays(ctx context.Context, from, to time.Time) ([]time.Time, error) {
	sql := `SELECT trade_date FROM ` + s.table("trading_calendar") + `
		WHERE is_trading_day AND trade_date BETWEEN $1 AND $2
		ORDER BY trade_date`
	out := make([]time.Time, 0, 64)
	err := s.scanAll(ctx, sql, []any{utcDate(from), utcDate(to)}, func(rows Rows) error {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return err
		}
		out = append(out, utcDate(d))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: trading days: %w", err)
	}
	return out, nil
}
