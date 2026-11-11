package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tellurion-data/internal/evidence"
	"tellurion-data/internal/serving"
)

// 本文件是 `ingest.raw` 证据归档面的 PG 写/读实现（`docs/db-schema.md` §1.5 / §9）。
//
// 单写者：只有 data 服务写这张表；core 只做读反代，不持有第二份证据存储。
// 不可变：同 content_hash 重复写为 no-op；同 hash 不同 payload 明确失败。
// 保留期：写读之外由 `PurgeExpired` 按 `fetched_at` 清理（口径见 evidence 包）。

// EvidenceDB 是归档面需要的最小连接面。`*pgxpool.Pool` 经 `PoolEvidenceDB` 适配。
type EvidenceDB interface {
	Exec(ctx context.Context, sql string, args ...any) (CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// EvidenceStore 是 `ingest.raw` 的 PG 实现。
type EvidenceStore struct {
	db EvidenceDB
}

// NewEvidenceStore 构造归档面。nil 连接 ⇒ error（未接线不是合法状态）。
func NewEvidenceStore(db EvidenceDB) (*EvidenceStore, error) {
	if db == nil {
		return nil, errors.New("store: nil evidence db")
	}
	return &EvidenceStore{db: db}, nil
}

// PoolEvidenceDB 把 `*pgxpool.Pool` 适配成 `EvidenceDB`。
func PoolEvidenceDB(pool *pgxpool.Pool) EvidenceDB { return pgxEvidenceDB{pool: pool} }

type pgxEvidenceDB struct{ pool *pgxpool.Pool }

func (d pgxEvidenceDB) Exec(ctx context.Context, sql string, args ...any) (CommandTag, error) {
	tag, err := d.pool.Exec(ctx, sql, args...)
	return tag, classifyDependency(err)
}

func (d pgxEvidenceDB) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return d.pool.QueryRow(ctx, sql, args...)
}

func (d pgxEvidenceDB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rows, err := d.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, classifyDependency(err)
	}
	return rows, nil
}

// evidenceColumns 是读面列顺序，Get 与 List 共用，避免两处漂移。
const evidenceColumns = `content_hash, source, dataset, key, as_of, payload, fetched_at`

// Put 归档一条记录。返回 inserted=false 表示同 hash 的记录已存在且内容一致。
//
// 冲突时**不覆盖**：内容寻址表里「同 hash 不同内容」只可能是哈希实现错误或
// 人为投毒，必须报 `evidence.ErrHashCollision` 让写请求失败。
func (s *EvidenceStore) Put(ctx context.Context, rec evidence.Record) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO ingest.raw (content_hash, source, dataset, key, as_of, payload, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (content_hash) DO NOTHING`,
		rec.ContentHash, rec.Source, rec.Dataset, rec.Key, rec.AsOf, []byte(rec.Payload), rec.FetchedAt)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}

	var existing []byte
	err = s.db.QueryRow(ctx,
		`SELECT payload FROM ingest.raw WHERE content_hash = $1`, rec.ContentHash).Scan(&existing)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 并发清理把行删掉了（保留期边界）：调用方重试即可，不能伪装成 404。
			return false, errors.New("store: evidence row vanished during concurrent purge")
		}
		return false, classifyDependency(err)
	}
	existingHash, err := evidence.CanonicalHash(existing)
	if err != nil {
		return false, fmt.Errorf("%w: stored payload is not valid JSON: %v", evidence.ErrHashCollision, err)
	}
	incomingHash, err := evidence.CanonicalHash(rec.Payload)
	if err != nil {
		return false, err
	}
	if existingHash != incomingHash {
		return false, evidence.ErrHashCollision
	}
	return false, nil
}

// Get 按 content_hash 取一条。不存在 ⇒ `serving.ErrEvidenceNotFound`（HTTP 404）。
func (s *EvidenceStore) Get(ctx context.Context, contentHash string) (*evidence.Record, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+evidenceColumns+` FROM ingest.raw WHERE content_hash = $1`, contentHash)
	rec, err := scanEvidence(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, serving.ErrEvidenceNotFound
		}
		return nil, classifyDependency(err)
	}
	return rec, nil
}

// EvidenceFilter 是列表读面的过滤与分页参数。
type EvidenceFilter struct {
	Source  string
	Dataset string
	Limit   int
	Offset  int
}

// List 按接收时间倒序列出最近若干条（分页上限由 HTTP 层保证，本层只执行）。
func (s *EvidenceStore) List(ctx context.Context, f EvidenceFilter) ([]evidence.Record, error) {
	var (
		conds []string
		args  []any
	)
	if f.Source != "" {
		args = append(args, f.Source)
		conds = append(conds, fmt.Sprintf("source = $%d", len(args)))
	}
	if f.Dataset != "" {
		args = append(args, f.Dataset)
		conds = append(conds, fmt.Sprintf("dataset = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit, f.Offset)
	query := fmt.Sprintf(`SELECT %s FROM ingest.raw%s
		ORDER BY fetched_at DESC, content_hash DESC
		LIMIT $%d OFFSET $%d`, evidenceColumns, where, len(args)-1, len(args))

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]evidence.Record, 0, f.Limit)
	for rows.Next() {
		rec, err := scanEvidence(rows)
		if err != nil {
			return nil, classifyDependency(err)
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDependency(err)
	}
	return out, nil
}

// PurgeExpired 删除 `fetched_at < cutoff` 的记录，返回删除条数。
// 保留期口径见 `internal/evidence`；清理只做删除，不参与读面判定。
func (s *EvidenceStore) PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM ingest.raw WHERE fetched_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// scanner 是 `Row` / `Rows` 共同的 Scan 面（两者方法集一致）。
type scanner interface {
	Scan(dest ...any) error
}

func scanEvidence(row scanner) (*evidence.Record, error) {
	var (
		rec     evidence.Record
		payload []byte
	)
	if err := row.Scan(&rec.ContentHash, &rec.Source, &rec.Dataset, &rec.Key,
		&rec.AsOf, &payload, &rec.FetchedAt); err != nil {
		return nil, err
	}
	rec.Payload = payload
	return &rec, nil
}
