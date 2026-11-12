package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件是复权基准账本（`market.adjust_baseline_changes`）的唯一写者与读面。
//
// 为什么需要它（ADR-0011）：qfq 的基准曾经是「请求区间内最新交易日」，于是
// 同一 (symbol, trade_date) 的 close 取决于「当初用哪个窗口拉的」——扩窗补
// 历史会让旧价格整体变动，且写面 `ON CONFLICT DO NOTHING` 不允许覆盖，补不
// 回去。基准必须**全局固定**：建一次、记下来，之后所有窗口都用它。

// BaselineDB 是账本的最小数据库面（`*pgxpool.Pool` 直接满足）。
type BaselineDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Exec(ctx context.Context, sql string, args ...any) (CommandTag, error)
}

// AdjustBaselineStore 读写复权基准账本。prefix 口径与 `NewWriter` 一致
// （空串 = public）。
type AdjustBaselineStore struct {
	db     BaselineDB
	prefix string
}

// NewAdjustBaselineStore 构造账本。prefix 为空按 public，含点号拒收
// （schema 前缀与表名拼在 SQL 里，点号会开出第二个名字）。
func NewAdjustBaselineStore(db BaselineDB, opts Options) (*AdjustBaselineStore, error) {
	if db == nil {
		return nil, fmt.Errorf("store: nil baseline db")
	}
	prefix := strings.TrimSpace(opts.MarketSchema)
	if prefix == "" {
		prefix = "public"
	}
	if strings.Contains(prefix, ".") {
		return nil, fmt.Errorf("store: market schema %q must not contain a dot", prefix)
	}
	return &AdjustBaselineStore{db: db, prefix: prefix}, nil
}

// PoolBaselineDB 把 `*pgxpool.Pool` 适配成 BaselineDB（`Exec` 的返回类型
// 需要一次显式转换：`pgconn.CommandTag` 满足 `CommandTag`，但方法签名不同）。
func PoolBaselineDB(pool *pgxpool.Pool) BaselineDB { return pgxBaselineDB{pool: pool} }

type pgxBaselineDB struct{ pool *pgxpool.Pool }

func (d pgxBaselineDB) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return d.pool.QueryRow(ctx, sql, args...)
}

func (d pgxBaselineDB) Exec(ctx context.Context, sql string, args ...any) (CommandTag, error) {
	return d.pool.Exec(ctx, sql, args...)
}

// Load 返回该 symbol 当前生效的基准（最新一条记录）。
// 账本为空返回 ok=false（不是错误：这只票还没建过基准）。
func (s *AdjustBaselineStore) Load(ctx context.Context, symbol string) (baseDate time.Time, baseFactor float64, ok bool, err error) {
	const q = `SELECT base_date, base_factor FROM %s.adjust_baseline_changes
		WHERE symbol = $1 ORDER BY changed_at DESC, id DESC LIMIT 1`
	err = s.db.QueryRow(ctx, fmt.Sprintf(q, s.prefix), symbol).Scan(&baseDate, &baseFactor)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, 0, false, nil
		}
		return time.Time{}, 0, false, classifyDependency(err)
	}
	return baseDate, baseFactor, true, nil
}

// Save 追加一条基准记录（append-only）。
//
// `reason = bootstrap` 受每 symbol 唯一的局部索引约束：并发首拉时只有一个
// 赢家，输的一方 `ON CONFLICT DO NOTHING` 静默返回——调用方必须随后 `Load`
// 读回赢家的基准再算价格，否则会用一份没被记账的基准。
func (s *AdjustBaselineStore) Save(ctx context.Context, symbol string, baseDate time.Time, baseFactor float64,
	prevDate *time.Time, prevFactor *float64, reason string) error {
	const q = `INSERT INTO %s.adjust_baseline_changes
		(symbol, base_date, base_factor, previous_date, previous_factor, reason)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`
	if _, err := s.db.Exec(ctx, fmt.Sprintf(q, s.prefix), symbol, baseDate, baseFactor, prevDate, prevFactor, reason); err != nil {
		return classifyDependency(err)
	}
	return nil
}

// LatestTradeDate 返回该 symbol 在 qfq 表里的最新交易日；无行返回 ok=false。
//
// 用途是「没有账本时重建既有数据的基准」：老实现的基准就是当时窗口的最新
// 交易日，而那一行一定已落库，所以 MAX(trade_date) 是对旧口径最忠实的重建。
func (s *AdjustBaselineStore) LatestTradeDate(ctx context.Context, symbol string) (time.Time, bool, error) {
	const q = `SELECT MAX(trade_date) FROM %s.ohlcv_daily_qfq WHERE symbol = $1`
	var latest *time.Time
	if err := s.db.QueryRow(ctx, fmt.Sprintf(q, s.prefix), symbol).Scan(&latest); err != nil {
		return time.Time{}, false, classifyDependency(err)
	}
	if latest == nil {
		return time.Time{}, false, nil
	}
	return *latest, true, nil
}
