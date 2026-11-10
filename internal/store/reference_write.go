package store

// 参考数据（reference data）写面：`stocks` 与 `trading_calendar`。
//
// 为什么不是 `BulkInsert`：这两张表是 C 集（无 `visible_at`、无 data_type
// 声明），它们不是观测流而是「参照系」——点位闸门（visible_at 整批门）对它
// 不成立。幂等语义也不同：
//   - 点位：同内容 no-op、异内容整批回滚（历史值不可改）；
//   - 参考数据：后到者覆盖可变列（名称/行业/状态会变，旧值没有保留价值）。
//
// 归属与门禁见 `docs/db-schema.md` §9；本文件只负责「把一行 upsert 进去」。

import (
	"context"
	"fmt"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// TradingDay 是 `market.trading_calendar` 的行（store 侧镜像）。
//
// 与 `ingest.TradingDay` 字段同形但**不是同一个类型**：store 不 import
// ingest（存储不认识采集语义），投影在 `internal/sync` 的参考数据执行器里做。
type TradingDay struct {
	Date         time.Time
	Exchange     string
	IsTradingDay bool
}

// ReferenceWriter 是参考数据的 PG 写面。
type ReferenceWriter struct {
	db     WriteDB
	prefix string
}

// NewReferenceWriter 构造参考数据写面。prefix 口径与 `New` / `NewWriter` 一致
// （空串 = public）。
func NewReferenceWriter(db WriteDB, opts Options) (*ReferenceWriter, error) {
	if db == nil {
		return nil, fmt.Errorf("store: nil reference write db")
	}
	prefix := opts.MarketSchema
	if prefix == "" {
		prefix = "public"
	}
	return &ReferenceWriter{db: db, prefix: prefix}, nil
}

// UpsertStocks 按 `symbol` 覆盖可变列，返回写入行数（pgx 的 RowsAffected 之和）。
//
// 空值语义：`industry` 空串 → NULL（源端没给分类）；`market_cap` 0 → NULL
// （Tushare `stock_basic` 不提供市值，0 不是「市值真的为 0」）；`list_date`
// 零值 → NULL。冲突时用 `COALESCE(EXCLUDED.col, 表.col)` 保住已有值——源端
// 某次没给的字段不该把上一轮已经写好的值擦掉。
//
// `delist_date` 不写：本仓 DDL 没有该列（读面用 `to_jsonb` 探针兼容旧库），
// 退市由 `status` 表达（`delisted`）。
func (w *ReferenceWriter) UpsertStocks(ctx context.Context, stocks []market.Stock) (int, error) {
	if len(stocks) == 0 {
		return 0, nil
	}

	tx, err := w.db.BeginTx(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	const stmt = `INSERT INTO %s AS s
		(symbol, name, exchange, industry, market_cap, list_date, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		ON CONFLICT (symbol) DO UPDATE SET
			name       = EXCLUDED.name,
			exchange   = EXCLUDED.exchange,
			industry   = COALESCE(EXCLUDED.industry, s.industry),
			market_cap = COALESCE(EXCLUDED.market_cap, s.market_cap),
			list_date  = COALESCE(EXCLUDED.list_date, s.list_date),
			status     = EXCLUDED.status,
			updated_at = NOW()`
	sql := fmt.Sprintf(stmt, w.table("stocks"))

	written := 0
	for _, stock := range stocks {
		status := stock.Status
		if status == "" {
			status = "active"
		}
		tag, err := tx.Exec(ctx, sql,
			stock.Symbol,
			stock.Name,
			stock.Exchange,
			nullIfEmpty(stock.Industry),
			nullIfZeroFloat(stock.MarketCap),
			nullIfZeroTime(stock.ListDate),
			status,
		)
		if err != nil {
			return 0, fmt.Errorf("store: upsert stock %s: %w", stock.Symbol, err)
		}
		written += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	committed = true
	return written, nil
}

// UpsertTradingDays 按 `trade_date` 覆盖 `exchange` / `is_trading_day`。
//
// 表的主键只有 `trade_date`：多交易所并写时后到者覆盖交易所列，这是既有
// DDL 的形状（`docs/db-schema.md` §9），不在本笔改。
func (w *ReferenceWriter) UpsertTradingDays(ctx context.Context, days []TradingDay) (int, error) {
	if len(days) == 0 {
		return 0, nil
	}

	tx, err := w.db.BeginTx(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	const stmt = `INSERT INTO %s AS c (trade_date, exchange, is_trading_day)
		VALUES ($1, $2, $3)
		ON CONFLICT (trade_date) DO UPDATE SET
			exchange       = EXCLUDED.exchange,
			is_trading_day = EXCLUDED.is_trading_day`
	sql := fmt.Sprintf(stmt, w.table("trading_calendar"))

	written := 0
	for _, day := range days {
		exchange := day.Exchange
		if exchange == "" {
			exchange = "SSE"
		}
		tag, err := tx.Exec(ctx, sql, utcDate(day.Date), exchange, day.IsTradingDay)
		if err != nil {
			return 0, fmt.Errorf("store: upsert trading day %s: %w", day.Date.Format("2006-01-02"), err)
		}
		written += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	committed = true
	return written, nil
}

// table 返回带 schema 前缀的表名（与 `Store.table` 同口径）。
func (w *ReferenceWriter) table(name string) string { return w.prefix + "." + name }

// nullIfEmpty 把空串折成 NULL（「源端没给」不是「值为空」）。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullIfZeroFloat 把 0 折成 NULL（只用于「0 = 未知」的可空数值列）。
func nullIfZeroFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

// nullIfZeroTime 把零值时间折成 NULL（`list_date` 缺失）。
func nullIfZeroTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return utcDate(t)
}
