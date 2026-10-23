// Package store 是四个取数 port 的 **PG 实现**（X1d）：
// `serving.BarSource` / `serving.TradingCalendar` / `serving.DataVersioner` /
// `serving.UniverseResolver`。
//
// 边界（`docs/ARCHITECTURE.md` §2.1「供给 Serving」）：
//   - 本包只读 `market` schema，**不写**；写路径属 `internal/ingest` 与后续的
//     同步装配。
//   - 每个 port 的 SQL 都点名到表与列，`SELECT *` 一律不用：列集漂移必须在
//     编译期之外也能一眼看见。
//   - 表名前缀由 `Options.MarketSchema` 决定（默认 `market`）。今天的物理表
//     多数还在 `public`（`docs/db-schema.md` §1 注「迁移不是改名」），因此
//     部署侧可以用空串切回 `public`——但那是**过渡开关**，不是第二套语义。
//
// 可复现性：所有方法都是纯读、不读墙钟；`visible_at` 只从列里取，缺列的行
// 交给 `normalize.VisibleAsOf` **整批拒收**（`ErrVisibleAtMissing`），绝不
// 用 `trade_date` / `created_at` 现场补一个"看起来合理"的可见时刻。
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

// ErrUnsupportedFrequency 是「该 port 不服务这个频率」。fail-closed：
// 15m 由 aggregator 用 1m 聚合，本层不物化、也不假装能读。
var ErrUnsupportedFrequency = errors.New("store: unsupported source frequency")

// ErrUnknownUniverse 是票池既不是显式 symbols、也不认识 index_code / 名字。
// 不认识就拒，不猜一个指数（猜错 = 静默换票池，比报错危险得多）。
var ErrUnknownUniverse = errors.New("store: unknown universe")

// Rows 是 `pgx.Rows` 的最小读面。抽成接口只为让 SQL 装配可被无库测试
// （假的 Rows 逐行喂值），生产实现就是 `*pgxpool.Pool` 返回的 `pgx.Rows`。
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Querier 是只读查询面。`*pgxpool.Pool` 直接满足它。
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Options 是装配参数。
type Options struct {
	// MarketSchema 是行情表的 schema 前缀（不含点）。空串 = `public`
	// （D4 物理搬迁完成前的过渡值）。
	MarketSchema string
}

// Store 同时实现四个 port。并发安全取决于底层 Querier（pgx pool 是）。
type Store struct {
	db     Querier
	prefix string
}

// New 用已有 Querier 构造（测试注入假实现 / 复用进程内连接池）。
func New(db Querier, opts Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("store: nil querier")
	}
	prefix := strings.TrimSpace(opts.MarketSchema)
	if prefix == "" {
		prefix = "public"
	}
	if strings.Contains(prefix, ".") {
		return nil, fmt.Errorf("store: market schema %q must not contain a dot", prefix)
	}
	return &Store{db: db, prefix: prefix}, nil
}

// Open 用 DSN 建连接池并 ping 一次（misconfig 在启动时暴露，而不是首个请求）。
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("store: empty dsn")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

// table 返回带 schema 前缀的表名。
func (s *Store) table(name string) string { return s.prefix + "." + name }

// scanAll 把一次查询折成回调，统一收口 Close 与 Err 的处理——
// 每个 port 各自写一遍就一定会有人忘掉 rows.Err()。
func (s *Store) scanAll(ctx context.Context, sql string, args []any, fn func(Rows) error) error {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// 编译期自证：`*pgxpool.Pool` 的 Query 返回 `pgx.Rows`，与本包的 Rows 同形。
var _ = func(p *pgxpool.Pool) {
	var _ func(context.Context, string, ...any) (pgx.Rows, error) = p.Query
}

// PoolQuerier 把 `*pgxpool.Pool` 适配成本包的 `Querier`。
//
// 为什么需要这一层：Go 的方法签名不协变——`pgxpool.Pool.Query` 返回
// `pgx.Rows`，不能直接当作返回 `store.Rows` 的实现。`pgx.Rows` 的方法集
// 与本包的 `Rows` 同形，所以只需一次显式转换。
func PoolQuerier(pool *pgxpool.Pool) Querier { return pgxQuerier{pool: pool} }

type pgxQuerier struct{ pool *pgxpool.Pool }

func (q pgxQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// nullTimePtr 是「可空时间列」的扫描目标类型别名，写在这里是为了让每个
// port 的 Scan 调用长得一致（也提醒读者：NULL 是有意义的值，不是零值）。
type nullTimePtr = *time.Time
