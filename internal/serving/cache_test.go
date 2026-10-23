package serving

import (
	"sync"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/require"
)

func cacheFixture(t *testing.T) (hash string, meta Metadata, calendar []time.Time, bars map[string][]market.OHLCV) {
	t.Helper()
	calendar = []time.Time{utcDay(2026, 10, 22), utcDay(2026, 10, 23)}
	meta = Metadata{
		Universe:        []string{"600000.SH"},
		UniverseHash:    contracts.UniverseHash([]string{"600000.SH"}),
		WindowStart:     utcDay(2026, 10, 22),
		WindowEnd:       utcDay(2026, 10, 23),
		Frequency:       "1d",
		DataVersion:     "v1",
		CalendarHash:    contracts.CalendarHash(calendar),
		AdjustmentBasis: "qfq",
		AsOf:            utcDay(2026, 10, 24),
	}
	hash = meta.snapshotInput(calendar).Hash()
	bars = map[string][]market.OHLCV{"600000.SH": {dailyBar("600000.SH", utcDay(2026, 10, 22), 10)}}
	return
}

func TestCache_StoreIsIdempotentForIdenticalContent(t *testing.T) {
	c := NewCache()
	hash, meta, calendar, bars := cacheFixture(t)

	first, err := c.Store(hash, meta, calendar, bars)
	require.NoError(t, err)
	second, err := c.Store(hash, meta, calendar, bars)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestCache_StoreRejectsDifferentMetadataForSameHash(t *testing.T) {
	c := NewCache()
	hash, meta, calendar, bars := cacheFixture(t)
	_, err := c.Store(hash, meta, calendar, bars)
	require.NoError(t, err)

	// 同六分量、不同 as_of ⇒ 同 hash（as_of 不在冻结分量里）。
	other := meta
	other.AsOf = meta.AsOf.Add(time.Hour)
	_, err = c.Store(hash, other, calendar, bars)
	require.ErrorIs(t, err, ErrSnapshotMismatch)
}

func TestCache_StoreRejectsDifferentContentForSameHash(t *testing.T) {
	c := NewCache()
	hash, meta, calendar, bars := cacheFixture(t)
	_, err := c.Store(hash, meta, calendar, bars)
	require.NoError(t, err)

	other := map[string][]market.OHLCV{"600000.SH": {dailyBar("600000.SH", utcDay(2026, 10, 22), 11)}}
	_, err = c.Store(hash, meta, calendar, other)
	require.ErrorIs(t, err, ErrSnapshotMismatch)
}

func TestCache_StoreRejectsInconsistentMetadataEcho(t *testing.T) {
	t.Run("universe hash", func(t *testing.T) {
		c := NewCache()
		hash, meta, calendar, bars := cacheFixture(t)
		meta.UniverseHash = "drifted"
		_, err := c.Store(hash, meta, calendar, bars)
		require.ErrorIs(t, err, ErrSnapshotMismatch, "回显与 Universe 不同源必须拒收")
	})
	t.Run("calendar hash", func(t *testing.T) {
		c := NewCache()
		hash, meta, calendar, bars := cacheFixture(t)
		meta.CalendarHash = "drifted"
		_, err := c.Store(hash, meta, calendar, bars)
		require.ErrorIs(t, err, ErrSnapshotMismatch)
	})
}

func TestCache_GetUnknownHash(t *testing.T) {
	_, err := NewCache().Get("nope")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

func TestCache_GetReturnsIndependentCopies(t *testing.T) {
	c := NewCache()
	hash, meta, calendar, bars := cacheFixture(t)
	_, err := c.Store(hash, meta, calendar, bars)
	require.NoError(t, err)

	first, err := c.Get(hash)
	require.NoError(t, err)
	first.Bars["600000.SH"][0].Close = 999
	first.Metadata.Universe[0] = "hacked"

	second, err := c.Get(hash)
	require.NoError(t, err)
	require.Equal(t, 10.0, second.Bars["600000.SH"][0].Close)
	require.Equal(t, []string{"600000.SH"}, second.Metadata.Universe)
}

func TestCache_GetDetectsTamperedMetadata(t *testing.T) {
	c := NewCache()
	hash, meta, calendar, bars := cacheFixture(t)
	_, err := c.Store(hash, meta, calendar, bars)
	require.NoError(t, err)

	// 模拟缓存内容被改：metadata 变了但 hash 没变 ⇒ 重算即失配。
	c.entries[hash].metadata.DataVersion = "tampered"
	_, err = c.Get(hash)
	require.ErrorIs(t, err, ErrSnapshotMismatch)
}

func TestCache_GetDetectsTamperedContent(t *testing.T) {
	c := NewCache()
	hash, meta, calendar, bars := cacheFixture(t)
	_, err := c.Store(hash, meta, calendar, bars)
	require.NoError(t, err)

	c.entries[hash].digest = "0000000000000000000000000000000000000000000000000000000000000000"
	_, err = c.Get(hash)
	require.ErrorIs(t, err, ErrSnapshotMismatch)
}

func TestCache_ConcurrentStoresOfSameSnapshot(t *testing.T) {
	c := NewCache()
	hash, meta, calendar, bars := cacheFixture(t)

	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Store(hash, meta, calendar, bars)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "concurrent identical stores must be idempotent (goroutine %d)", i)
	}
	ds, err := c.Get(hash)
	require.NoError(t, err)
	require.Len(t, ds.Bars["600000.SH"], 1)
}
