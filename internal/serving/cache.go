package serving

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// Cache 是按 `dataset_snapshot_hash` 寻址的内存快照缓存。
//
// 语义（`tellurion-data:docs/api-schema.md` §3）：强幂等——同一 hash 的内容
// 一旦落定就不许原地改；内容变必然是**新 hash**。因此 `Store` 对同 hash 的
// 两次写入只允许「完全相同」，任何差异都报 `ErrSnapshotMismatch`，而不是
// 覆盖（覆盖 = 悄悄让历史 hash 指向另一份数据）。
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	metadata Metadata
	calendar []time.Time
	bars     map[string][]market.OHLCV
	digest   string
}

// NewCache 返回空缓存。
func NewCache() *Cache {
	return &Cache{entries: make(map[string]*cacheEntry)}
}

// Store 落一个快照并返回可交付的 Dataset。同 hash 重复写入是幂等的；
// 同 hash 不同内容 / 不同 metadata（例如 `as_of` 不同）⇒ ErrSnapshotMismatch。
func (c *Cache) Store(hash string, metadata Metadata, calendar []time.Time, bars map[string][]market.OHLCV) (Dataset, error) {
	if err := metadata.validateEcho(calendar); err != nil {
		return Dataset{}, err
	}
	_, digest, err := encodeBars(bars)
	if err != nil {
		return Dataset{}, err
	}
	entry := &cacheEntry{
		metadata: metadata,
		calendar: append([]time.Time(nil), calendar...),
		bars:     cloneBars(bars),
		digest:   digest,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[hash]; ok {
		if !sameMetadata(existing.metadata, entry.metadata) {
			return Dataset{}, fmt.Errorf("%w: %s already materialized with different metadata (same six components, different as_of?)",
				ErrSnapshotMismatch, hash)
		}
		if existing.digest != entry.digest {
			return Dataset{}, fmt.Errorf("%w: %s already materialized with different content", ErrSnapshotMismatch, hash)
		}
		return existing.dataset(hash), nil
	}
	c.entries[hash] = entry
	return entry.dataset(hash), nil
}

// Get 按 hash 取快照，并**重算** hash 与内容指纹做自证。
//
//   - 没有这个 hash ⇒ ErrSnapshotNotFound（404）；
//   - 重算 hash 或内容指纹不符 ⇒ ErrSnapshotMismatch（500 + 告警，缓存串号）。
func (c *Cache) Get(hash string) (Dataset, error) {
	c.mu.RLock()
	entry, ok := c.entries[hash]
	c.mu.RUnlock()
	if !ok {
		return Dataset{}, fmt.Errorf("%w: %s", ErrSnapshotNotFound, hash)
	}
	if err := entry.metadata.validateEcho(entry.calendar); err != nil {
		return Dataset{}, err
	}
	if got := entry.metadata.snapshotInput(entry.calendar).Hash(); got != hash {
		return Dataset{}, fmt.Errorf("%w: %s recomputed as %s", ErrSnapshotMismatch, hash, got)
	}
	_, digest, err := encodeBars(entry.bars)
	if err != nil {
		return Dataset{}, err
	}
	if digest != entry.digest {
		return Dataset{}, fmt.Errorf("%w: %s content fingerprint drifted", ErrSnapshotMismatch, hash)
	}
	return entry.dataset(hash), nil
}

func (e *cacheEntry) dataset(hash string) Dataset {
	return Dataset{
		Hash:     hash,
		Metadata: cloneMetadata(e.metadata),
		Bars:     cloneBars(e.bars),
		Digest:   e.digest,
	}
}

func cloneMetadata(m Metadata) Metadata {
	m.Universe = append([]string(nil), m.Universe...)
	return m
}

// sameMetadata 比较完整 metadata（含 `as_of`）。两者都 marshal 成 JSON 再比，
// 免得逐字段维护一份会漂的等价实现。
func sameMetadata(a, b Metadata) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ja, jb)
}
