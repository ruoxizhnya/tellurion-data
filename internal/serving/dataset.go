package serving

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
)

// knownAdjustments 是 resolve 允许的复权口径词表。
//
// 口径由**请求方（core）给**，本仓不猜、也不读 core 的
// `backtest_adjustment_anchors`（该表 owner = core 回测域；本仓去读就是
// 跨 owner 摸表 + hash 口径两处各算一遍）。
var knownAdjustments = []string{"qfq", "hfq", "none"}

// ResolveRequest 是 `GET /datasets/resolve` 的参数集合。
type ResolveRequest struct {
	// Universe / IndexCode / Symbols 三选一（口径二义即 400，不静默挑优先级）。
	Universe  string
	IndexCode string
	Symbols   []string

	From time.Time
	To   time.Time
	// Frequency 是目标频率；源频率由 aggregator 推导（15m ← 1m）。
	Frequency market.Frequency
	// Adjustment 是复权口径（qfq / hfq / none）。必填：空串不是「none」。
	Adjustment string
	// AsOf 是物化上界：任何返回记录的 visible_at 必须 <= AsOf。
	AsOf time.Time
}

// Validate 校验参数，fail-closed。
func (r ResolveRequest) Validate() error {
	if r.From.IsZero() {
		return fmt.Errorf("%w: from", ErrMissingParameter)
	}
	if r.To.IsZero() {
		return fmt.Errorf("%w: to", ErrMissingParameter)
	}
	if r.To.Before(r.From) {
		return fmt.Errorf("%w: to is before from", ErrInvalidParameter)
	}
	if !r.Frequency.Valid() {
		return fmt.Errorf("%w: freq %q is not a known frequency (known: 1m/15m/1d)", ErrInvalidParameter, r.Frequency.String())
	}
	if r.AsOf.IsZero() {
		return fmt.Errorf("%w: as_of", ErrMissingParameter)
	}
	adj := strings.TrimSpace(r.Adjustment)
	if adj == "" {
		return fmt.Errorf("%w: adjustment", ErrMissingParameter)
	}
	if !isKnownAdjustment(adj) {
		return fmt.Errorf("%w: adjustment %q is not one of qfq/hfq/none", ErrInvalidParameter, adj)
	}

	name := strings.TrimSpace(r.Universe)
	indexCode := strings.TrimSpace(r.IndexCode)
	switch {
	case name != "" && indexCode != "":
		return fmt.Errorf("%w: universe and index_code are both set", ErrUniverseAmbiguous)
	case (name != "" || indexCode != "") && len(r.Symbols) > 0:
		return fmt.Errorf("%w: symbols cannot be combined with universe/index_code", ErrUniverseAmbiguous)
	case name == "" && indexCode == "" && len(r.Symbols) == 0:
		return fmt.Errorf("%w: universe (or index_code / symbols)", ErrMissingParameter)
	}
	for _, s := range r.Symbols {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%w: symbol list contains an empty entry", ErrInvalidParameter)
		}
	}
	return nil
}

// adjustment 返回规范化后的复权口径。
func (r ResolveRequest) adjustment() string { return strings.TrimSpace(r.Adjustment) }

// Metadata 是 resolve 的六分量回显（`tellurion-data:docs/api-schema.md` §1）。
//
// 六分量 = universe_hash / window / frequency / data_version / calendar_hash /
// adjustment_basis（QT-03 唯一定义）。`AsOf` 与 `Universe` 是**回显**而不是
// hash 分量：
//   - `Universe` 进 hash 的是折好的 `universe_hash`，此处回显原始集合供人读；
//   - `AsOf` 决定可见上界，却**不在冻结六分量里** ⇒ 同六分量、不同 `as_of`
//     会撞同一个 hash。本仓的处置是 fail-closed：缓存命中同 hash 但 metadata
//     不同即报 `ErrSnapshotMismatch`，绝不静默返回别人的数据集。
//     这是一个已登记的风险项（docs/ARCHITECTURE.md §8 注），裁决属 QT-03 后继。
type Metadata struct {
	Universe        []string  `json:"universe"`
	UniverseHash    string    `json:"universe_hash"`
	WindowStart     time.Time `json:"window_start"`
	WindowEnd       time.Time `json:"window_end"`
	Frequency       string    `json:"frequency"`
	DataVersion     string    `json:"data_version"`
	CalendarHash    string    `json:"calendar_hash"`
	AdjustmentBasis string    `json:"adjustment_basis"`
	AsOf            time.Time `json:"as_of"`
}

// snapshotInput 把 metadata 折成 contracts 的 hash 输入。
//
// 这是**唯一**允许产出 `dataset_snapshot_hash` 的路径：本包不自己实现
// canonical 编码（唯一定义处 = contracts/engine/provenance.go）。
func (m Metadata) snapshotInput(calendar []time.Time) contracts.DatasetSnapshotInput {
	return contracts.DatasetSnapshotInput{
		Universe:        m.Universe,
		Window:          contracts.DateRange{Start: m.WindowStart, End: m.WindowEnd},
		Frequency:       m.Frequency,
		DataVersion:     m.DataVersion,
		Calendar:        calendar,
		AdjustmentBasis: m.AdjustmentBasis,
	}
}

// validateEcho 校验回显字段与它们的**同源定义**一致。
//
// `UniverseHash` / `CalendarHash` 是回显，不进 snapshotInput（Hash() 会自己
// 重算）。回显与定义分离就有漂移的空间：改 `Universe` 忘了改回显，响应里的
// 两个 hash 会指向不同东西。这里 fail-closed 拦掉，不给「看起来对」的响应。
func (m Metadata) validateEcho(calendar []time.Time) error {
	if got := contracts.UniverseHash(m.Universe); got != m.UniverseHash {
		return fmt.Errorf("%w: universe_hash echo %s does not match universe %v (%s)",
			ErrSnapshotMismatch, m.UniverseHash, m.Universe, got)
	}
	if got := contracts.CalendarHash(calendar); got != m.CalendarHash {
		return fmt.Errorf("%w: calendar_hash echo %s does not match the calendar (%s)",
			ErrSnapshotMismatch, m.CalendarHash, got)
	}
	return nil
}

// ResolveResult 是 resolve 的返回：hash + 六分量回显。
type ResolveResult struct {
	Hash     string   `json:"dataset_hash"`
	Metadata Metadata `json:"metadata"`
}

// Dataset 是按 hash 取回的数据集（run 前一次物化）。
//
// `Bars` 的形状是 `map[string][]OHLCV`：窗口内缺数据的 symbol **直接缺席**，
// 不补零值（同 `DataEngine.Snapshot` 的既有语义）。
type Dataset struct {
	Hash     string                    `json:"hash"`
	Metadata Metadata                  `json:"metadata"`
	Bars     map[string][]market.OHLCV `json:"bars"`
	// Digest 是 Bars 的确定性序列化指纹（sha256 hex），用于「同 hash 两次
	// 拉取字节一致」的自证与缓存内容校验。
	Digest string `json:"digest"`
}

// encodeBars 产出确定性字节（map 键序由 encoding/json 保证升序）与指纹。
func encodeBars(bars map[string][]market.OHLCV) ([]byte, string, error) {
	payload, err := json.Marshal(bars)
	if err != nil {
		return nil, "", fmt.Errorf("serving: encode bars: %w", err)
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}

// cloneBars 深拷贝，防调用方改动缓存内容。
func cloneBars(bars map[string][]market.OHLCV) map[string][]market.OHLCV {
	if bars == nil {
		return nil
	}
	out := make(map[string][]market.OHLCV, len(bars))
	for symbol, series := range bars {
		if series == nil {
			out[symbol] = nil
			continue
		}
		cp := make([]market.OHLCV, len(series))
		copy(cp, series)
		out[symbol] = cp
	}
	return out
}

// normalizeSymbols 去空、去重、排序。空 symbol 是参数错误，不静默丢。
func normalizeSymbols(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			return nil, fmt.Errorf("%w: symbol list contains an empty entry", ErrInvalidParameter)
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

func isKnownAdjustment(adj string) bool {
	for _, k := range knownAdjustments {
		if adj == k {
			return true
		}
	}
	return false
}

// sortDays 校验交易日轴严格递增（保序是 calendar_hash 的语义前提）。
func validateCalendar(days []time.Time) error {
	if len(days) == 0 {
		return ErrEmptyCalendar
	}
	for i := 1; i < len(days); i++ {
		if !days[i].After(days[i-1]) {
			return fmt.Errorf("%w: calendar is not strictly increasing at index %d", ErrInvalidParameter, i)
		}
		if days[i].IsZero() {
			return fmt.Errorf("%w: calendar contains a zero date at index %d", ErrInvalidParameter, i)
		}
	}
	if days[0].IsZero() {
		return fmt.Errorf("%w: calendar contains a zero date at index 0", ErrInvalidParameter)
	}
	return nil
}
