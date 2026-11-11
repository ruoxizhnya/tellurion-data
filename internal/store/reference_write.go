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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// ErrMixedTradingExchanges 表示一批交易日历混入了多个交易所。
//
// `market.trading_calendar` 是**单一 A 股交易日历**（主键只有 `trade_date`，
// 没有交易所维度）：同一天 SSE/SZSE/BSE 的开闭市一致，`exchange` 列只是
// 「最近一次写入来源」的 provenance，不是 key。一批里混多个交易所说明调用方
// 在按交易所分片写，后到者会覆盖前者——这是本表语义不允许的用法，拒写。
var ErrMixedTradingExchanges = errors.New("store: trading calendar batch mixes exchanges")

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
// （Tushare `stock_basic` 不提供市值，0 不是「市值真的为 0」）；`list_date` /
// `delist_date` 零值/未给 → NULL。冲突时用 `COALESCE(EXCLUDED.col, 表.col)`
// 保住已有值——源端某次没给的字段不该把上一轮已经写好的值擦掉。
//
// `delist_date` 必须落库（ADR-0009 决策二）：回测的历史票池要按退市日截断，
// 只靠 `status='delisted'` 无法回答「某日是否已退市」，会引入幸存者偏差。
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
		(symbol, name, exchange, industry, market_cap, list_date, delist_date, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		ON CONFLICT (symbol) DO UPDATE SET
			name        = EXCLUDED.name,
			exchange    = EXCLUDED.exchange,
			industry    = COALESCE(EXCLUDED.industry, s.industry),
			market_cap  = COALESCE(EXCLUDED.market_cap, s.market_cap),
			list_date   = COALESCE(EXCLUDED.list_date, s.list_date),
			delist_date = COALESCE(EXCLUDED.delist_date, s.delist_date),
			status      = EXCLUDED.status,
			updated_at  = NOW()`
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
			nullIfNilTime(stock.DelistDate),
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
// 单交易所假设（ADR-0009）：主键只有 `trade_date`，表是**一张** A 股交易日历；
// `exchange` 只记录来源，不是维度。因此一批里出现多个交易所就拒写——否则
// 后到者会静默覆盖前者，而读方（`GET /api/v1/trading/calendar`）根本不区分
// 交易所。真要做分交易所日历时，先把主键改成 `(exchange, trade_date)` 并改
// 读方，属另立单（ADR-0009 的重开条件）。
func (w *ReferenceWriter) UpsertTradingDays(ctx context.Context, days []TradingDay) (int, error) {
	if len(days) == 0 {
		return 0, nil
	}

	exchanges := make([]string, len(days))
	first := ""
	for i, day := range days {
		exchanges[i] = normalizeExchange(day.Exchange)
		if first == "" {
			first = exchanges[i]
			continue
		}
		if exchanges[i] != first {
			return 0, fmt.Errorf("%w: first=%s, got=%s", ErrMixedTradingExchanges, first, exchanges[i])
		}
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
	for i, day := range days {
		tag, err := tx.Exec(ctx, sql, utcDate(day.Date), exchanges[i], day.IsTradingDay)
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

// ReplaceIndexConstituents 以「整期快照」语义替换给定指数的**当前**成分。
//
// 为什么不是逐行 upsert：`market.index_constituents` 没有可用来判等的唯一键
// （DDL 里 `in_date/out_date` 都可空，`id` 是代理键），而当前只有「指数 → 当前
// 成分」一个口径。同一事务里：
//   - 删掉这些指数 `out_date IS NULL` 的行（上一期当前快照）；
//   - 插入新快照行（`in_date` = 快照日，`out_date` = NULL）。
//
// 不删 `out_date` 非空的行：那是将来补历史区间时要留的（本单不产生它）。
// 留两期当前快照会让读面把已调出的票算回票池——本函数存在的理由就是把它
// 换成「只有一期」。空 `index_code` / `symbol` 拒写，不静默丢掉。
func (w *ReferenceWriter) ReplaceIndexConstituents(ctx context.Context, rows []market.IndexConstituent) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	codes := make([]string, 0, 1)
	seen := map[string]struct{}{}
	for i, row := range rows {
		if strings.TrimSpace(row.IndexCode) == "" || strings.TrimSpace(row.Symbol) == "" || row.InDate.IsZero() {
			return 0, fmt.Errorf("store: index constituent row %d has empty index_code/symbol or zero in_date", i)
		}
		if _, ok := seen[row.IndexCode]; ok {
			continue
		}
		seen[row.IndexCode] = struct{}{}
		codes = append(codes, row.IndexCode)
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

	deleteSQL := fmt.Sprintf(`DELETE FROM %s WHERE index_code = $1 AND out_date IS NULL`,
		w.table("index_constituents"))
	for _, code := range codes {
		if _, err := tx.Exec(ctx, deleteSQL, code); err != nil {
			return 0, fmt.Errorf("store: clear current index constituents %s: %w", code, err)
		}
	}

	insertSQL := fmt.Sprintf(`INSERT INTO %s (index_code, symbol, in_date, out_date, weight)
		VALUES ($1, $2, $3, NULL, $4)`, w.table("index_constituents"))
	written := 0
	for _, row := range rows {
		if _, err := tx.Exec(ctx, insertSQL,
			row.IndexCode,
			row.Symbol,
			utcDate(row.InDate),
			nullIfZeroFloat(row.Weight),
		); err != nil {
			return 0, fmt.Errorf("store: insert index constituent %s/%s: %w", row.IndexCode, row.Symbol, err)
		}
		written++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	committed = true
	return written, nil
}

// normalizeExchange 把缺省/大小写差异折成 DDL CHECK 认可的取值。
func normalizeExchange(exchange string) string {
	exchange = strings.ToUpper(strings.TrimSpace(exchange))
	if exchange == "" {
		return "SSE"
	}
	return exchange
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

// nullIfNilTime 把 nil 指针折成 NULL（`delist_date` 缺失 = 仍在市）。
func nullIfNilTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return utcDate(*t)
}
