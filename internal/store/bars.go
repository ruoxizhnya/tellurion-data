package store

import (
	"context"
	"fmt"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// ReadBars 实现 `serving.BarSource`。
//
// 频率口径（`docs/dataflow.md` §1 注）：
//   - `1d` 直读日线表（**不从分钟合成**，否则日线口径会随分钟完整度漂移）；
//   - `1m` 直读分钟表；
//   - `15m` **不在这里读**：aggregator 会先要 1m 再聚合。请求 15m 直接
//     fail-closed，避免把 1m bar 原样贴成 15m。
//
// `VisibleAt` 只从列里取。列值为 NULL ⇒ 该 bar 的 VisibleAt 保持零值，
// 由 `normalize.VisibleAsOf` **整批拒收**（`ErrVisibleAtMissing`）。这里
// **不做**「按 trade_date 推一个 15:00」的现场兜底：`ohlcv_daily_qfq` 的
// 回填档位是 `created_at`（§6.4 第二档），现场推 15:00 会把「三天后才入库」
// 的行说成「当天收盘就可见」——正是前视方向。
func (s *Store) ReadBars(ctx context.Context, symbols []string, from, to time.Time, frequency market.Frequency) ([]market.OHLCV, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	switch frequency {
	case market.Freq1d:
		return s.readDailyBars(ctx, symbols, from, to)
	case market.Freq1m:
		return s.readMinuteBars(ctx, symbols, from, to)
	default:
		return nil, fmt.Errorf("%w: %q (15m must be aggregated from 1m by the caller)", ErrUnsupportedFrequency, frequency)
	}
}

func (s *Store) readDailyBars(ctx context.Context, symbols []string, from, to time.Time) ([]market.OHLCV, error) {
	sql := `SELECT symbol, trade_date, open, high, low, close, volume, turnover, trade_days, visible_at
		FROM ` + s.table("ohlcv_daily_qfq") + `
		WHERE symbol = ANY($1) AND trade_date BETWEEN $2 AND $3
		ORDER BY symbol, trade_date`
	out := make([]market.OHLCV, 0, 256)
	err := s.scanAll(ctx, sql, []any{symbols, utcDate(from), utcDate(to)}, func(rows Rows) error {
		var (
			bar       market.OHLCV
			visibleAt nullTimePtr
		)
		if err := rows.Scan(&bar.Symbol, &bar.Date, &bar.Open, &bar.High, &bar.Low, &bar.Close,
			&bar.Volume, &bar.Turnover, &bar.TradeDays, &visibleAt); err != nil {
			return err
		}
		bar.Date = utcDate(bar.Date)
		bar.Frequency = market.Freq1d
		bar.VisibleAt = deref(visibleAt)
		out = append(out, bar)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: read daily bars: %w", err)
	}
	return out, nil
}

func (s *Store) readMinuteBars(ctx context.Context, symbols []string, from, to time.Time) ([]market.OHLCV, error) {
	sql := `SELECT symbol, ts, open, high, low, close, volume, amount, visible_at
		FROM ` + s.table("ohlcv_minute") + `
		WHERE symbol = ANY($1) AND ts >= $2 AND ts <= $3
		ORDER BY symbol, ts`
	out := make([]market.OHLCV, 0, 256)
	err := s.scanAll(ctx, sql, []any{symbols, from.UTC(), to.UTC()}, func(rows Rows) error {
		var (
			bar       market.OHLCV
			visibleAt nullTimePtr
		)
		if err := rows.Scan(&bar.Symbol, &bar.Date, &bar.Open, &bar.High, &bar.Low, &bar.Close,
			&bar.Volume, &bar.Turnover, &visibleAt); err != nil {
			return err
		}
		bar.Date = bar.Date.UTC()
		bar.Frequency = market.Freq1m
		bar.VisibleAt = deref(visibleAt)
		out = append(out, bar)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: read minute bars: %w", err)
	}
	return out, nil
}

// utcDate 把任意时刻折成 UTC 日历日零点——PG `DATE` 经 pgx 扫出来的形态
// 就是它，比较时两侧必须同形，否则 `BETWEEN` 会把边界日算丢。
func utcDate(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// deref 把可空时间列折成零值：NULL 与「没有可见时刻」是同一件事，
// 调用侧的 PIT 闸门会据此整批拒收。
func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}
