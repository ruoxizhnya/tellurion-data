// Package tushare 是 `ingest.TushareTransport` 的实现：tushare.pro 的 HTTP
// 取数面（日线 / 财务 / 宏观 / 活跃度 / 资金流）。
//
// 为什么在本仓：core `SVC-CORE-DATA-RETIRE`（`a96d836`，2026-11-08）删掉了
// `pkg/data/**` 与 `cmd/data/**`，而 `docs/TASKS.md` 的 X1c 复核已确认「传输
// 为零 ⇒ 今天没有任何生产进程抓行情」。两条口径正面冲突（禁区写「传输留
// core」，core 已无传输），用户 2026-11-10 裁决 **B：本仓自持传输与调度**。
// 本包就是那笔裁决的落地。
//
// 语义搬运纪律（不许「顺手改进」）：
//   - 归一化口径逐条对齐 core `pkg/data/tushare.go` / `macro.go` /
//     `tushare_daily_basic.go` / `tushare_moneyflow.go`（只读参考，本仓不
//     import core 实现包）：qfq 复权用 `daily × adj_factor / 区间最新因子`、
//     vol 手→股要 round、amount 千元→CNY 不 round、资金流万元→元、宏观月频
//     取当月首日；
//   - 「缺失≠0」：可空数值一律 `*float64`，nil 原样上抛；
//   - 有行但全被跳过 → 数据质量哨兵（fail-loud，同 core 在 macro /
//     daily_basic / moneyflow 上的闸门），真空响应才是合法的 0 行；
//   - 错误按 `ingest` 的传输哨兵分类（`ErrTransportInvalidInput` /
//     `ErrTransportDataQuality`），其余归上游失败由 adapter 包装。
//
// 与 core 的**刻意差异（逐条登记）**：
//  1. HTTP 层用 `net/http` 而不是 kit `httpclient`：本包要在 `httptest` 下
//     可复现，且错误分类必须按哨兵而不是错误文本；kit 客户端还会把
//     `logging.Logger` 全局状态卷进来。
//  2. **不写 `ingest.raw`**：core 的 `call` 在成功响应后归档原文，而本仓
//     传输 port（`ingest/transport.go`）没有归档 sink；补它需要 store 端口，
//     属后续单（已在 `docs/TASKS.md` 的 X1c 施工记录里如实登记）。
//  3. 限流只保留「200 次/分」这条例，不再照抄 core 在锁内 sleep 的实现
//     （锁内 sleep 会把并发取数串行成同一堵墙，行为等价但更慢）。
package tushare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tellurion-data/internal/ingest"
)

// Tushare 网关与配额（与 core `pkg/data/tushare.go` 的常量同值）。
const (
	// DefaultBaseURL 是 tushare.pro 的取数网关。
	DefaultBaseURL = "https://api.tushare.pro"
	// requestsPerMinute 是免费档的配额上限（200 次/分）。
	requestsPerMinute = 200
	// rateWindow 是配额的统计窗口。
	rateWindow = time.Minute
	// defaultTimeout 是单次请求的超时（core 同值 30s）。
	defaultTimeout = 30 * time.Second
	// defaultMaxRetries 是网络/5xx 的重试次数。
	defaultMaxRetries = 2
)

// Config 是客户端配置。零值 Token = 空 token，调用会由上游拒绝；
// 装配层应当在 token 为空时**不构造**客户端（`Transports.Tushare = nil`
// 让该门以 disabled 注册）。
type Config struct {
	// Token 是 tushare.pro 的 API token（TUSHARE_TOKEN）。
	Token string
	// BaseURL 是网关地址；空 = DefaultBaseURL。
	BaseURL string
	// MaxRetries 是网络错误与 5xx 的重试次数；负值按 0 处理。
	MaxRetries int
	// HTTPClient 可注入（测试用）；nil = 30s 超时的默认客户端。
	HTTPClient *http.Client
	// Now 是时钟（测试注入固定时刻）；nil = 墙钟。
	Now func() time.Time
}

// Client 实现 `ingest.TushareTransport`。
//
// 并发安全：限流状态自带互斥；其余字段构造后只读。
type Client struct {
	token      string
	endpoint   string
	httpClient *http.Client
	maxRetries int
	now        func() time.Time

	mu          sync.Mutex
	windowStart time.Time
	windowCount int
}

// 编译期证明：本包就是 `ingest.TushareTransport` 的实现。
var _ ingest.TushareTransport = (*Client)(nil)

// New 构造客户端。BaseURL 空则用生产网关；MaxRetries 负值按 0。
func New(cfg Config) *Client {
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		token:      cfg.Token,
		endpoint:   base,
		httpClient: cfg.HTTPClient,
		maxRetries: cfg.MaxRetries,
		now:        cfg.Now,
	}
}

// NewDefault 以默认网关与默认重试次数构造客户端。
func NewDefault(token string) *Client {
	return New(Config{Token: token, MaxRetries: defaultMaxRetries})
}

// request / response 是 tushare 的线格式（逐字对齐 core）。
type request struct {
	APIName string                 `json:"api_name"`
	Token   string                 `json:"token"`
	Params  map[string]interface{} `json:"params,omitempty"`
	Fields  string                 `json:"fields,omitempty"`
}

type response struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Fields []string `json:"fields"`
		Items  [][]any  `json:"items"`
	} `json:"data"`
}

// waitForRateLimit 保证滚动窗口内不超过 200 次请求。
//
// 与 core 的差异：sleep 在解锁之后，且可被 ctx 取消——配额墙不该把一个
// 已经取消的作业再按住一分钟。
func (c *Client) waitForRateLimit(ctx context.Context) error {
	c.mu.Lock()
	now := c.now()
	if c.windowStart.IsZero() || now.Sub(c.windowStart) >= rateWindow {
		c.windowStart = now
		c.windowCount = 0
	}
	if c.windowCount < requestsPerMinute {
		c.windowCount++
		c.mu.Unlock()
		return nil
	}
	wait := rateWindow - now.Sub(c.windowStart)
	c.windowStart = now.Add(wait)
	c.windowCount = 1
	c.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// call 发一次 tushare 调用（限流 + 网络/5xx 重试 + 错误码分诊）。
//
// 重试只覆盖**传输级**失败（网络错误 / 5xx）：4xx 与业务错误码重试不会
// 变好，只会把配额烧得更快。
func (c *Client) call(ctx context.Context, apiName string, params map[string]interface{}, fields string) (*response, error) {
	if err := c.waitForRateLimit(ctx); err != nil {
		return nil, fmt.Errorf("tushare %s: wait for rate limit: %w", apiName, err)
	}

	payload, err := json.Marshal(request{
		APIName: apiName,
		Token:   c.token,
		Params:  params,
		Fields:  fields,
	})
	if err != nil {
		return nil, fmt.Errorf("tushare %s: marshal request: %w", apiName, err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}

		body, status, retryable, err := c.post(ctx, payload)
		if err != nil {
			lastErr = err
			if !retryable {
				return nil, fmt.Errorf("tushare %s: %w", apiName, err)
			}
			continue
		}
		if status != http.StatusOK {
			err = fmt.Errorf("tushare %s returned HTTP %d: %s", apiName, status, truncate(string(body), 256))
			lastErr = err
			if status < 500 {
				return nil, err
			}
			continue
		}

		var resp response
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("tushare %s: decode response: %w (body %s)", apiName, err, truncate(string(body), 256))
		}
		if resp.Code != 0 {
			return nil, fmt.Errorf("tushare %s: API error %d: %s", apiName, resp.Code, resp.Msg)
		}
		return &resp, nil
	}
	return nil, fmt.Errorf("tushare %s: %d attempts failed: %w", apiName, c.maxRetries+1, lastErr)
}

// post 发一次 HTTP POST，返回 (body, status, retryable, err)。
func (c *Client) post(ctx context.Context, payload []byte) ([]byte, int, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, false, ctx.Err()
		}
		return nil, 0, true, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, 0, resp.StatusCode >= 500, err
	}
	return body, resp.StatusCode, resp.StatusCode >= 500, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── 单元格解析（逐字对齐 core 的 fieldStr / fieldFloat / fieldFloatPtr） ──

// tushareCell 取一个单元格的字符串形态；越界或 nil 返回空串。
func tushareCell(item []any, idx int) string {
	if idx < 0 || idx >= len(item) || item[idx] == nil {
		return ""
	}
	switch v := item[idx].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// fieldFloat 取数值，缺位/不可解析折 0（**只用于非空语义列**：价格、量额；
// 可空列一律走 fieldFloatPtr）。
func fieldFloat(item []any, idx int) float64 {
	if idx < 0 || idx >= len(item) || item[idx] == nil {
		return 0
	}
	switch v := item[idx].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	default:
		return 0
	}
}

// fieldFloatPtr 取可空数值：nil / 不可解析 / 非有限 → nil（缺失≠0）。
func fieldFloatPtr(item []any, idx int) *float64 {
	f, ok := tushareFloatOK(item, idx)
	if !ok {
		return nil
	}
	return &f
}

// tushareFloatOK 是「可判定失败」的数值解析器：区分「值未发布（nil）」与
// 「非 nil 但形状变故（N/A / 13,42 / -- / bool / 数组）」。
func tushareFloatOK(item []any, idx int) (float64, bool) {
	if idx < 0 || idx >= len(item) || item[idx] == nil {
		return 0, false
	}
	var f float64
	switch v := item[idx].(type) {
	case float64:
		f = v
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// formatDate 把 YYYY-MM-DD 折成 tushare 的 YYYYMMDD；其它形状原样返回。
func formatDate(s string) string {
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		return s[:4] + s[5:7] + s[8:10]
	}
	return s
}

// extractExchange 从 ts_code 后缀推交易所；无后缀时返回点号前的前缀。
func extractExchange(tsCode string) string {
	if len(tsCode) >= 4 {
		switch tsCode[len(tsCode)-3:] {
		case ".SH":
			return "SSE"
		case ".SZ":
			return "SZSE"
		case ".BJ":
			return "BSE"
		}
	}
	return strings.Split(tsCode, ".")[0]
}

// invalidInput / dataQuality 把失败包成 ingest 的传输哨兵（adapter 用
// errors.Is 分诊「换源也治不好」的两类）。
func invalidInput(format string, args ...any) error {
	return wrapf(ingest.ErrTransportInvalidInput, format, args...)
}

func dataQuality(format string, args ...any) error {
	return wrapf(ingest.ErrTransportDataQuality, format, args...)
}

func wrapf(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{sentinel}, args...)...)
}

// columnIndex 在响应 Fields 头里按名字（大小写不敏感）找列；找不到 -1。
//
// 按名字而不是固定下标：tushare 会剔除该期全空的列（DBT-07 的根因），
// 固定下标会把整行右移，把 roe 写进 pe 列。
func columnIndex(resp *response, name string) int {
	for i, f := range resp.Data.Fields {
		if strings.EqualFold(f, name) {
			return i
		}
	}
	return -1
}

// requireFields 校验响应头包含全部必填列；缺一列即数据质量失败。
//
// 为什么 fail-closed：按名取不到列时 `tushareCell` 返回空串，于是
// `stock_basic` 会把退市票静默写成 active、`trade_cal` 会把整天静默写成
// 非交易日——错的是语义，作业却显示成功。缺列/改名只能响亮失败。
//
// 只校验**语义必需**列：可选列（`area`/`market`/`industry`/`exchange`）缺了
// 写 NULL 或回落到入参是合法语义，不在此列。
func requireFields(resp *response, names ...string) error {
	var missing []string
	for _, name := range names {
		if columnIndex(resp, name) < 0 {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return dataQuality("响应缺少必填列 %v（fields=%v）", missing, resp.Data.Fields)
	}
	return nil
}
