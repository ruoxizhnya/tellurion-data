// Package evidence 是 `ingest.raw` 证据归档面的领域规则：内容寻址哈希、
// 落库前脱敏与保留期口径（`docs/db-schema.md` §1.5 / §9）。
//
// 归档**只追加、不覆盖、不回放**：一张表按 content_hash 内容寻址，读到什么
// 就是当时归档了什么。写面只有 data 服务一条（单写者），core 只做读反代。
package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultRetentionDays 是 `ingest.raw` 的默认保留期。
// 口径：按 `fetched_at` 计，超过保留期的记录由清理任务删除；0 / 负数非法。
const DefaultRetentionDays = 365

// ErrHashCollision 表示同一个 content_hash 已存在但 payload 不同。
// 内容寻址表里出现这种情况只可能是上游哈希实现有 bug 或人为投毒，
// 必须明确失败（500）而不是静默保留旧值。
var ErrHashCollision = errors.New("evidence: content_hash already exists with a different payload")

// Record 是 `ingest.raw` 的一行，JSON 形状与 `docs/openapi.yaml` 的 RawIngest 对齐。
type Record struct {
	ContentHash string          `json:"content_hash"`
	Source      string          `json:"source"`
	Dataset     string          `json:"dataset"`
	Key         string          `json:"key"`
	AsOf        *time.Time      `json:"as_of,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	FetchedAt   time.Time       `json:"fetched_at"`
}

// WriteRequest 是 `POST /api/ingest/raw` 的请求体。
// `fetched_at` 由服务端按接收时刻生成，调用方不得指定。
type WriteRequest struct {
	Source      string          `json:"source"`
	Dataset     string          `json:"dataset"`
	Key         string          `json:"key"`
	AsOf        *time.Time      `json:"as_of,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	ContentHash string          `json:"content_hash,omitempty"`
}

// Validate 校验归档请求的必填字段与可选哈希格式。
func (r WriteRequest) Validate() error {
	switch {
	case strings.TrimSpace(r.Source) == "":
		return errors.New("evidence: source is required")
	case strings.TrimSpace(r.Dataset) == "":
		return errors.New("evidence: dataset is required")
	case strings.TrimSpace(r.Key) == "":
		return errors.New("evidence: key is required")
	case len(r.Payload) == 0 || bytes.Equal(bytes.TrimSpace(r.Payload), []byte("null")):
		return errors.New("evidence: payload is required")
	}
	if r.ContentHash != "" && !IsContentHash(r.ContentHash) {
		return fmt.Errorf("evidence: content_hash %q is not 64 lowercase hex chars", r.ContentHash)
	}
	return nil
}

// IsContentHash 判定字符串是否为合法的 sha256 小写 hex。
func IsContentHash(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CanonicalHash 对**已脱敏**的 payload 求内容哈希。
//
// 规范化：`json.Decoder.UseNumber` 解码（不把金融数值折成 float64）后
// 重新 `json.Marshal`——map 键按字典序输出，因此同一内容的不同键序得到
// 同一个哈希。返回 64 位小写 hex。
func CanonicalHash(payload json.RawMessage) (string, error) {
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(payload json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("evidence: payload is not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("evidence: payload has trailing JSON content")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("evidence: payload cannot be canonicalized: %w", err)
	}
	return out, nil
}

// RedactedPlaceholder 是命中脱敏规则后写入的值。
const RedactedPlaceholder = "[REDACTED]"

// redactedKeys 是脱敏键集合（归一化后：小写、去掉 `-` 与 `_`）。
// 规则同步写在 `docs/db-schema.md` §9；改这里必须同步改文档与测试。
var redactedKeys = map[string]struct{}{
	"token":         {},
	"accesstoken":   {},
	"refreshtoken":  {},
	"secret":        {},
	"clientsecret":  {},
	"password":      {},
	"passwd":        {},
	"apikey":        {},
	"authorization": {},
	"cookie":        {},
	"setcookie":     {},
	"credential":    {},
	"credentials":   {},
	"session":       {},
	"sessionid":     {},
	"privatekey":    {},
}

// RedactedKeyNames 返回脱敏键集合的原始可读形式（文档/测试用）。
func RedactedKeyNames() []string {
	out := make([]string, 0, len(redactedKeys))
	for k := range redactedKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Redact 递归遮蔽 payload 中的敏感字段，返回可落库的规范化 JSON。
//
// 键名归一化（小写、去 `-`/`_`）后命中 `redactedKeys` 即把**值**替换为
// `"[REDACTED]"`——嵌套对象与数组都会遍历，非字符串值同样替换。
// 哈希在脱敏之后计算，保证「引用的哈希」与「库里能读到的内容」一致。
func Redact(payload json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("evidence: payload is not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("evidence: payload has trailing JSON content")
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return nil, fmt.Errorf("evidence: payload cannot be encoded after redaction: %w", err)
	}
	return out, nil
}

func redactValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		for k, child := range typed {
			if _, hit := redactedKeys[normalizeKey(k)]; hit {
				typed[k] = RedactedPlaceholder
				continue
			}
			typed[k] = redactValue(child)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = redactValue(child)
		}
		return typed
	default:
		return v
	}
}

func normalizeKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "-", "")
	k = strings.ReplaceAll(k, "_", "")
	return k
}

// RetentionDays 解析保留期（天）：空串 ⇒ 默认 365；非整数或 <= 0 ⇒ error。
// 0 / 负数等于「关掉保留期」，不允许隐式生效，必须在启动期被拒。
func RetentionDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultRetentionDays, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("evidence: EVIDENCE_RETENTION_DAYS %q is not an integer", raw)
	}
	if days <= 0 {
		return 0, fmt.Errorf("evidence: EVIDENCE_RETENTION_DAYS must be positive, got %d", days)
	}
	return days, nil
}

// RetentionCutoff 返回保留期下界：`fetched_at < cutoff` 的记录应被清理。
func RetentionCutoff(now time.Time, days int) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}
