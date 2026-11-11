package store

import (
	"context"
	"fmt"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// 本文件实现 `serving.LegacyReader` 的参考数据半边（G3 只读兼容面）。
//
// 表归属（如实登记，见 `docs/TASKS.md` SVC-DATA-G3 与 K3）：
//   - `stocks` / `index_constituents` 的 DDL 在本仓 `internal/schema`；
//   - `stock_fundamentals` 的 DDL **仍是 core 的迁移**（`tellurion:pkg/storage`），
//     本仓只读。在 `DATA_MARKET_SCHEMA` 指向新 schema 的部署里这张表尚不存在，
//     `/fundamental/{symbol}` 会 500 —— 这是 K3 收编前必须知道的限制，不是静默降级。

// Stocks 实现 `serving.LegacyReader`：股票列表（exchange 为空 = 全市场）。
func (s *Store) Stocks(ctx context.Context, exchange string) ([]market.Stock, error) {
	sql := `SELECT t.symbol, t.name, t.exchange, COALESCE(t.industry, ''),
			COALESCE(t.market_cap, 0), t.list_date, COALESCE(t.status, 'active'),
			t.delist_date
		FROM ` + s.table("stocks") + ` t`
	args := []any{}
	if exchange != "" {
		sql += ` WHERE t.exchange = $1`
		args = append(args, exchange)
	}
	sql += ` ORDER BY t.symbol`

	out := make([]market.Stock, 0, 256)
	err := s.scanAll(ctx, sql, args, func(rows Rows) error {
		var (
			stock     market.Stock
			listDate  *time.Time
			delistDay *time.Time
		)
		if err := rows.Scan(&stock.Symbol, &stock.Name, &stock.Exchange, &stock.Industry,
			&stock.MarketCap, &listDate, &stock.Status, &delistDay); err != nil {
			return err
		}
		stock.ListDate = deref(listDate)
		stock.DelistDate = delistDay
		out = append(out, stock)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: read stocks: %w", err)
	}
	return out, nil
}

// LatestTradeDate 实现 `serving.LegacyReader`：库内日线的最新交易日。
//
// `MAX(trade_date)` 走 `(symbol, trade_date)` 主键的前缀之外的分支，但这是
// 运维读面（低频），不值得为它加一条额外索引；`found=false`（空表）不报错。
func (s *Store) LatestTradeDate(ctx context.Context) (time.Time, bool, error) {
	sql := `SELECT MAX(trade_date) FROM ` + s.table("ohlcv_daily_qfq")

	var (
		day   *time.Time
		found bool
	)
	err := s.scanAll(ctx, sql, nil, func(rows Rows) error {
		if err := rows.Scan(&day); err != nil {
			return err
		}
		found = day != nil
		return nil
	})
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: read latest trade date: %w", err)
	}
	if !found {
		return time.Time{}, false, nil
	}
	return utcDate(*day), true, nil
}

// Stock 实现 `serving.LegacyReader`：按代码取单只，found=false 即库里没有。
func (s *Store) Stock(ctx context.Context, symbol string) (market.Stock, bool, error) {
	sql := `SELECT t.symbol, t.name, t.exchange, COALESCE(t.industry, ''),
			COALESCE(t.market_cap, 0), t.list_date, COALESCE(t.status, 'active'),
			t.delist_date
		FROM ` + s.table("stocks") + ` t
		WHERE t.symbol = $1
		LIMIT 1`

	var (
		stock     market.Stock
		found     bool
		listDate  *time.Time
		delistDay *time.Time
	)
	err := s.scanAll(ctx, sql, []any{symbol}, func(rows Rows) error {
		if err := rows.Scan(&stock.Symbol, &stock.Name, &stock.Exchange, &stock.Industry,
			&stock.MarketCap, &listDate, &stock.Status, &delistDay); err != nil {
			return err
		}
		stock.ListDate = deref(listDate)
		stock.DelistDate = delistDay
		found = true
		return nil
	})
	if err != nil {
		return market.Stock{}, false, fmt.Errorf("store: read stock %s: %w", symbol, err)
	}
	return stock, found, nil
}

// Fundamental 实现 `serving.LegacyReader`：asOf 当日已可用的最新一期。
//
// PIT 口径与 core 的 `pkg/storage` 一致：可用日 = `available_date`
// （`COALESCE(ann_date, trade_date)` 的生成列；老行可能为空，故再 COALESCE
// 一次到 trade_date）。数值列一律用指针承接 NULL——「没披露」和「是 0」在
// 估值因子里是两回事（core 的 P2-10）。
func (s *Store) Fundamental(ctx context.Context, symbol string, asOf time.Time) (market.Fundamental, bool, error) {
	sql := `SELECT ts_code, COALESCE(available_date, trade_date) AS available,
			ann_date, pe, pb, ps, roe, roa, debt_to_equity, gross_margin, net_margin,
			revenue, net_profit, total_assets, total_liab
		FROM ` + s.table("stock_fundamentals") + `
		WHERE ts_code = $1 AND COALESCE(available_date, trade_date) <= $2
		ORDER BY COALESCE(available_date, trade_date) DESC, end_date DESC NULLS LAST
		LIMIT 1`

	var (
		f       market.Fundamental
		found   bool
		annDate *time.Time
	)
	err := s.scanAll(ctx, sql, []any{symbol, utcDate(asOf)}, func(rows Rows) error {
		if err := rows.Scan(&f.Symbol, &f.Date, &annDate,
			&f.PE, &f.PB, &f.PS, &f.ROE, &f.ROA, &f.DebtToEquity,
			&f.GrossMargin, &f.NetMargin, &f.Revenue, &f.NetProfit,
			&f.TotalAssets, &f.TotalLiab); err != nil {
			return err
		}
		f.Date = utcDate(f.Date)
		f.AnnDate = annDate
		found = true
		return nil
	})
	if err != nil {
		return market.Fundamental{}, false, fmt.Errorf("store: read fundamental %s: %w", symbol, err)
	}
	return f, found, nil
}

// IndexConstituents 实现 `serving.LegacyReader`：asOf 当日生效的成分。
//
// 区间是半开的 `[in_date, out_date)`：out_date 当天已不在成分内（旧读面
// 的调用方按「当前成分」消费，含上界会把当日调出的票算回来）。
func (s *Store) IndexConstituents(ctx context.Context, indexCode string, asOf time.Time) ([]market.IndexConstituent, error) {
	sql := `SELECT id, index_code, symbol, in_date, out_date, COALESCE(weight, 0)
		FROM ` + s.table("index_constituents") + `
		WHERE index_code = $1
			AND (in_date IS NULL OR in_date <= $2)
			AND (out_date IS NULL OR out_date > $2)
		ORDER BY symbol`

	day := utcDate(asOf)
	out := make([]market.IndexConstituent, 0, 64)
	err := s.scanAll(ctx, sql, []any{indexCode, day}, func(rows Rows) error {
		var (
			c       market.IndexConstituent
			inDate  *time.Time
			outDate *time.Time
		)
		if err := rows.Scan(&c.ID, &c.IndexCode, &c.Symbol, &inDate, &outDate, &c.Weight); err != nil {
			return err
		}
		c.InDate = deref(inDate)
		c.OutDate = deref(outDate)
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: read index constituents %s: %w", indexCode, err)
	}
	return out, nil
}
