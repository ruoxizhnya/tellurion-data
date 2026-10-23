package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 本文件是 connector 的**运行时契约**：`DataSourceAdapter` 的 9 方法面。
//
// 它与 `declaration.go` 的注册面是两件事、两层：
//   - 注册面（`Connector`，4 方法）回答「这个源声明了什么语义」，
//     `Registry.Register` 在**开机时**用它 fail-closed 拒收漏声明；
//   - 运行时面（本文件）回答「怎么取数」，`ETLPipeline` 在**作业时**用它。
//
// 形状逐字对齐 core `tellurion:pkg/data/source/adapter.go`（X1c 搬迁的接收面），
// 只有一处刻意差异：`SchemaField` 的 `Required` / `Unit` 由 `declaration.go`
// 承载（注册门只关心锚点列的名字与类型），**语义不变**。
//
// 为什么 `Fetch` 也进同一个接口：漏实现 Fetch 的 connector 能在注册期过门、
// 在第一次作业时 panic——把两边合成一个接口，漏实现就是编译错误。

// AdapterType categorizes the transport/protocol of a source.
type AdapterType string

const (
	// AdapterTypeHTTP indicates a plain HTTP/HTTPS client.
	AdapterTypeHTTP AdapterType = "http"
	// AdapterTypeSDK indicates a Go SDK (e.g. mootdx uses TCP/binary protocol).
	AdapterTypeSDK AdapterType = "sdk"
	// AdapterTypeWebSocket indicates a long-lived streaming connection.
	AdapterTypeWebSocket AdapterType = "websocket"
)

// 运行时哨兵。errors.Is 分诊：Registry 只对可重试的两类走兜底。
var (
	// ErrUnsupported 表示该 adapter 不服务请求的数据类型。**不另造
	// ErrUnknownDataType**（同义哨兵会让 errors.Is 分诊两义）。
	ErrUnsupported = errors.New("ingest: unsupported data type")
	// ErrRateLimited 表示配额耗尽（强信号：换下一个源，别重试本门）。
	ErrRateLimited = errors.New("ingest: rate limited")
	// ErrUpstreamUnavailable 表示传输级失败（超时 / 5xx / 连接重置）。
	ErrUpstreamUnavailable = errors.New("ingest: upstream unavailable")

	// ErrTransportInvalidInput / ErrTransportDataQuality 是**传输层的分类
	// 哨兵**：换源也治不好的两类失败（参数非法、数据质量）。
	//
	// 为什么在这里而不是直接判 core 的 `AppError.Code`：本仓不 import core。
	// 传输实现（今天在 core 的 TushareClient）在边界上把
	// `ErrCodeInvalidInput` / `ErrCodeDataQuality` 翻译成这两个哨兵即可，
	// **判据仍是 code 不是错误文本**。
	ErrTransportInvalidInput = errors.New("ingest: transport rejected the request as invalid")
	ErrTransportDataQuality  = errors.New("ingest: transport rejected the payload as low quality")
)

// FetchRequest is the universal input to DataSourceAdapter.Fetch.
//
// Symbols is optional for data types that are not symbol-scoped
// (e.g. sector list, hot search). StartDate/EndDate are inclusive.
type FetchRequest struct {
	// DataType identifies the kind of data being requested.
	DataType string
	// Symbols optionally scopes the request. Empty means "all symbols" for
	// adapters that support that mode.
	Symbols []string
	// StartDate / EndDate define the time window (inclusive).
	StartDate time.Time
	EndDate   time.Time
	// Period subdivides the time window (e.g. capital-flow 5d/10d/60d).
	Period string
	// Extra carries adapter-specific options.
	Extra map[string]interface{}
}

// DataItem is a single record returned by an adapter. The Data map preserves
// the raw fields; normalization to domain types happens in ETL pipelines.
type DataItem struct {
	// Symbol is the canonical ticker (e.g. "600519.SH" or "AAPL").
	Symbol string
	// TradeTime is the timestamp the data point refers to. For realtime, the
	// snapshot timestamp; for daily OHLCV, the trade date.
	TradeTime time.Time
	// Data is the raw field map; ETL normalizes from this.
	Data map[string]interface{}
}

// FetchResponse is the result of a successful Fetch.
type FetchResponse struct {
	// Source echoes the adapter's Name() so the caller can attribute.
	Source string
	// Items is the normalized data points (post-source-format conversion).
	Items []DataItem
	// FetchedAt is when the adapter completed the upstream call.
	FetchedAt time.Time
	// Latency is the time spent in the upstream call.
	Latency time.Duration
	// HasMore indicates whether more pages/cursor data is available.
	HasMore bool
	// NextCursor can be used for pagination (adapter-specific).
	NextCursor string
	// Metadata carries adapter-specific extra info.
	Metadata map[string]interface{}
}

// RateLimitConfig describes how often a source may be hit.
type RateLimitConfig struct {
	// RequestsPerMinute is the advisory ceiling. 0 means "no limit enforced by
	// the adapter" (caller is responsible).
	RequestsPerMinute int
	// Burst allows short spikes above the steady rate.
	Burst int
}

// DataSourceAdapter is the runtime contract every concrete source implements.
//
// Implementations must be safe for concurrent use.
type DataSourceAdapter interface {
	// Connector 是注册面：Name / SupportedTypes / Declarations / Schema。
	// 运行时面嵌它而不是另起一套，保证「注册的」与「能取数的」是同一个对象。
	Connector

	// Type returns the transport category.
	Type() AdapterType

	// Enabled reports whether the adapter is currently usable. The Registry
	// skips disabled adapters when picking fallbacks.
	Enabled() bool

	// Fetch retrieves data from the upstream source.
	//
	// Contract: on success (err == nil) the returned *FetchResponse MUST be
	// non-nil. Returning (nil, nil) is a contract violation — the Registry
	// treats it as an adapter failure and falls through to the next adapter.
	Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error)

	// HealthCheck performs a lightweight liveness probe (short timeout).
	HealthCheck(ctx context.Context) error

	// RateLimit returns the rate limit configuration.
	RateLimit() RateLimitConfig
}

// AdapterBase provides reusable state (name, enabled flag) for concrete
// adapters. Embedding it is optional but recommended.
type AdapterBase struct {
	adapterName string
	enabled     bool
}

// NewAdapterBase constructs an AdapterBase with sensible defaults.
func NewAdapterBase(name string, enabled bool) AdapterBase {
	return AdapterBase{adapterName: name, enabled: enabled}
}

// Name implements Connector.Name.
func (b AdapterBase) Name() string { return b.adapterName }

// Enabled implements DataSourceAdapter.Enabled.
func (b AdapterBase) Enabled() bool { return b.enabled }

// SetEnabled toggles the enabled flag (config reload).
func (b *AdapterBase) SetEnabled(v bool) { b.enabled = v }

// Validate checks a FetchRequest for common errors before passing it to an
// adapter. Adapters should call this as their first step.
func Validate(req FetchRequest) error {
	if req.DataType == "" {
		return fmt.Errorf("ingest: empty DataType")
	}
	if req.EndDate.Before(req.StartDate) {
		return fmt.Errorf("ingest: EndDate %s before StartDate %s", req.EndDate, req.StartDate)
	}
	return nil
}

// IsRetryable reports whether err is one of the recognized transient failures
// (rate limit, upstream unavailable) that justify falling back to the next
// adapter in the chain. Permanent errors (validation, parse) are not retryable.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrRateLimited) || errors.Is(err, ErrUpstreamUnavailable)
}
