package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/evidence"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
)

// 证据归档面的 PG 腿：与写面同一口径——只在 `DATABASE_URL` 存在时跑。
//
// 隔离手段与写面不同：`ingest.raw` 的 schema 名是固定的（读面按真实路径
// 取数），所以这里用**唯一 source** 圈住本用例的行，结束时按 source 删除。
// 不 DROP ingest（那是本仓的正式 schema）。

func newPGEvidence(t *testing.T) (*store.EvidenceStore, string) {
	t.Helper()
	source := fmt.Sprintf("pgtest.evidence.%d", time.Now().UnixNano())
	pool := pgPool(t)
	archive, err := store.NewEvidenceStore(store.PoolEvidenceDB(pool))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ingest.raw WHERE source = $1`, source)
	})
	return archive, source
}

func mustJson(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	var out json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	return out
}

func TestEvidencePGPutGetAndIdempotent(t *testing.T) {
	archive, source := newPGEvidence(t)
	ctx := context.Background()

	payload := mustJson(t, `{"instrument":"600519.SH","close":1.5}`)
	hash, err := evidence.CanonicalHash(payload)
	require.NoError(t, err)
	asOf := time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC)
	rec := evidence.Record{
		ContentHash: hash,
		Source:      source,
		Dataset:     "pgtest.dataset",
		Key:         "600519.SH/2024Q1",
		AsOf:        &asOf,
		Payload:     payload,
		FetchedAt:   time.Now().UTC().Truncate(time.Microsecond),
	}

	inserted, err := archive.Put(ctx, rec)
	require.NoError(t, err)
	assert.True(t, inserted)

	// 重复写同 hash 同内容 ⇒ no-op，不报错。
	inserted, err = archive.Put(ctx, rec)
	require.NoError(t, err)
	assert.False(t, inserted)

	got, err := archive.Get(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, rec.Source, got.Source)
	assert.Equal(t, rec.Dataset, got.Dataset)
	assert.Equal(t, rec.Key, got.Key)
	require.NotNil(t, got.AsOf)
	assert.True(t, asOf.Equal(*got.AsOf))
	assert.JSONEq(t, string(payload), string(got.Payload))
	assert.WithinDuration(t, rec.FetchedAt, got.FetchedAt, time.Second)
}

func TestEvidencePGGetMissingReturnsNotFound(t *testing.T) {
	archive, _ := newPGEvidence(t)
	_, err := archive.Get(context.Background(), "0"+fmt.Sprintf("%063d", 0))
	assert.True(t, errors.Is(err, serving.ErrEvidenceNotFound), "不存在应映射 ErrEvidenceNotFound，得到 %v", err)
}

func TestEvidencePGListFiltersAndOrders(t *testing.T) {
	archive, source := newPGEvidence(t)
	ctx := context.Background()

	mk := func(key string, fetched time.Time) evidence.Record {
		payload := mustJson(t, `{"key":"`+key+`"}`)
		hash, err := evidence.CanonicalHash(payload)
		require.NoError(t, err)
		return evidence.Record{
			ContentHash: hash,
			Source:      source,
			Dataset:     "pgtest.dataset",
			Key:         key,
			Payload:     payload,
			FetchedAt:   fetched,
		}
	}
	older := mk("older", time.Now().UTC().Add(-2*time.Hour))
	newer := mk("newer", time.Now().UTC().Add(-1*time.Hour))
	require.True(t, mustPut(t, archive, older))
	require.True(t, mustPut(t, archive, newer))

	items, err := archive.List(ctx, store.EvidenceFilter{Source: source, Limit: 10})
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "newer", items[0].Key, "应按 fetched_at 倒序")
	assert.Equal(t, "older", items[1].Key)

	limited, err := archive.List(ctx, store.EvidenceFilter{Source: source, Limit: 1})
	require.NoError(t, err)
	require.Len(t, limited, 1)
	assert.Equal(t, "newer", limited[0].Key)

	none, err := archive.List(ctx, store.EvidenceFilter{Source: source, Dataset: "no-such-dataset", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestEvidencePGPurgeExpired(t *testing.T) {
	archive, source := newPGEvidence(t)
	ctx := context.Background()

	mk := func(key string, fetched time.Time) evidence.Record {
		payload := mustJson(t, `{"key":"`+key+`"}`)
		hash, err := evidence.CanonicalHash(payload)
		require.NoError(t, err)
		return evidence.Record{
			ContentHash: hash,
			Source:      source,
			Dataset:     "pgtest.dataset",
			Key:         key,
			Payload:     payload,
			FetchedAt:   fetched,
		}
	}
	old := mk("expired", time.Now().UTC().Add(-40*24*time.Hour))
	fresh := mk("fresh", time.Now().UTC())
	require.True(t, mustPut(t, archive, old))
	require.True(t, mustPut(t, archive, fresh))

	deleted, err := archive.PurgeExpired(ctx, time.Now().UTC().Add(-30*24*time.Hour))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, int64(1))

	_, err = archive.Get(ctx, old.ContentHash)
	assert.True(t, errors.Is(err, serving.ErrEvidenceNotFound), "过期记录应被清理")
	_, err = archive.Get(ctx, fresh.ContentHash)
	assert.NoError(t, err, "未过期记录不应被清理")
}

func mustPut(t *testing.T, archive *store.EvidenceStore, rec evidence.Record) bool {
	t.Helper()
	inserted, err := archive.Put(context.Background(), rec)
	require.NoError(t, err)
	return inserted
}
