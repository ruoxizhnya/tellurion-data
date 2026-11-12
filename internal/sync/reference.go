package syncjob

// 参考数据同步：`stocks` / `trading_calendar` / `index_constituents` / `fundamentals`。
//
// 为什么旁路 ETL：见 `internal/store/reference_write.go` 的头注释。这些表
// 是 C 集参照数据，没有 visible_at 锚点，走不了
// `FetchRequestFromParams → Pipeline → BulkInsert` 那条链。
//
// 作业类型因此是四个**保留字面值**：
//
//	`stocks`             → FetchStocks + UpsertStocks
//	`trading_calendar`   → FetchTradeCalendar + UpsertTradingDays
//	`index_constituents` → FetchIndexConstituents + ReplaceIndexConstituents
//	`fundamentals`       → FetchFundamentalsRange + UpsertFundamentals
//
// 判定发生在 `Runner.Execute` 的最前面；命中即走本文件，不经过
// `FetchRequestFromParams` 的 data_type 一致性校验（那层只属于点位管线）。
// 注意 `fundamentals` 与 data_type 同名：参考腿优先，点位管线永远拿不到它。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/store"
)

// 参考数据作业类型（保留字面值；`fundamentals` 与 data_type 同名，参考腿优先）。
const (
	JobTypeStocks            = "stocks"
	JobTypeTradingCalendar   = "trading_calendar"
	JobTypeIndexConstituents = "index_constituents"
	JobTypeFundamentals      = "fundamentals"
)

// IsReferenceJobType 判断一个 job_type 是否属于参考数据腿。
func IsReferenceJobType(jobType string) bool {
	switch jobType {
	case JobTypeStocks, JobTypeTradingCalendar, JobTypeIndexConstituents, JobTypeFundamentals:
		return true
	default:
		return false
	}
}

// ReferenceTransport 是参考数据同步的取数面（`*tushare.Client` 满足）。
type ReferenceTransport interface {
	FetchStocks(ctx context.Context, exchange, listStatus string) ([]market.Stock, error)
	FetchTradeCalendar(ctx context.Context, exchange string, start, end time.Time) ([]ingest.TradingDay, error)
	FetchIndexConstituents(ctx context.Context, indexCode string, start, end time.Time) ([]market.IndexConstituent, error)
	FetchFundamentalsRange(ctx context.Context, symbol, startPeriod, endPeriod string) ([]market.Fundamental, error)
}

// ReferenceWriter 是参考数据的写面（`*store.ReferenceWriter` 满足）。
type ReferenceWriter interface {
	UpsertStocks(ctx context.Context, stocks []market.Stock) (int, error)
	UpsertTradingDays(ctx context.Context, days []store.TradingDay) (int, error)
	ReplaceIndexConstituents(ctx context.Context, rows []market.IndexConstituent) (int, error)
	UpsertFundamentals(ctx context.Context, rows []market.Fundamental) (int, error)
}

// ReferenceRunner 执行参考数据作业。
//
// `Source` 写进作业结果的 `source` 字段（空 = tushare）；`Now` 只用于
// 「日历没给日期时取当年」的默认窗口，测试可注入固定时钟。
type ReferenceRunner struct {
	Transport ReferenceTransport
	Writer    ReferenceWriter
	Source    string
	Now       func() time.Time
}

// Supports 判断本执行器是否认领该 job_type。
func (r *ReferenceRunner) Supports(jobType string) bool {
	return r != nil && IsReferenceJobType(jobType)
}

// Execute 跑一个参考数据作业。
func (r *ReferenceRunner) Execute(ctx context.Context, job Job) (ingest.ProcessResult, error) {
	if r == nil || r.Transport == nil || r.Writer == nil {
		return ingest.ProcessResult{}, ErrMisconfigured
	}
	switch job.JobType {
	case JobTypeStocks:
		return r.syncStocks(ctx, job)
	case JobTypeTradingCalendar:
		return r.syncTradingCalendar(ctx, job)
	case JobTypeIndexConstituents:
		return r.syncIndexConstituents(ctx, job)
	case JobTypeFundamentals:
		return r.syncFundamentals(ctx, job)
	default:
		return ingest.ProcessResult{}, fmt.Errorf("%w: %q is not a reference job type", ErrInvalidParams, job.JobType)
	}
}

// syncStocks 取上市清单并 upsert。
//
// 参数：`exchange`（空 = 全市场）、`list_status`（空 = `L`，只取上市中）。
// 退市票要单独跑一次 `D`（Tushare 的 `stock_basic` 不接受逗号拼接多状态）。
func (r *ReferenceRunner) syncStocks(ctx context.Context, job Job) (ingest.ProcessResult, error) {
	exchange := stringField(job.Params, "exchange")
	status := stringField(job.Params, "list_status")
	if status == "" {
		status = "L"
	}

	stocks, err := r.Transport.FetchStocks(ctx, exchange, status)
	if err != nil {
		return ingest.ProcessResult{}, fmt.Errorf("reference stocks: fetch: %w", err)
	}
	written, err := r.Writer.UpsertStocks(ctx, stocks)
	if err != nil {
		return ingest.ProcessResult{}, fmt.Errorf("reference stocks: persist: %w", err)
	}
	return ingest.ProcessResult{
		DataType:  job.JobType,
		Source:    r.source(),
		Fetched:   len(stocks),
		Persisted: written,
	}, nil
}

// syncTradingCalendar 取日历并 upsert。
//
// 参数：`exchange`（空 = `SSE`）、`start_date` / `end_date`（空 = 当年）。
// 默认窗口刻意收窄到当年：Tushare `trade_cal` 单次有行数上限，全历史会把
// 一次作业拆成不可控的多页；需要历史就显式给区间。
func (r *ReferenceRunner) syncTradingCalendar(ctx context.Context, job Job) (ingest.ProcessResult, error) {
	exchange := stringField(job.Params, "exchange")
	if exchange == "" {
		exchange = "SSE"
	}
	start, err := dateField(job.Params, "start_date")
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	end, err := dateField(job.Params, "end_date")
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	if start.IsZero() || end.IsZero() {
		now := r.now()
		if start.IsZero() {
			start = time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		if end.IsZero() {
			end = time.Date(now.Year(), time.December, 31, 0, 0, 0, 0, time.UTC)
		}
	}
	if end.Before(start) {
		return ingest.ProcessResult{}, fmt.Errorf("%w: end_date %s is before start_date %s",
			ErrInvalidParams, end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	days, err := r.Transport.FetchTradeCalendar(ctx, exchange, start, end)
	if err != nil {
		return ingest.ProcessResult{}, fmt.Errorf("reference trading calendar: fetch: %w", err)
	}
	rows := make([]store.TradingDay, 0, len(days))
	for _, d := range days {
		ex := d.Exchange
		if ex == "" {
			ex = exchange
		}
		rows = append(rows, store.TradingDay{
			Date:         d.Date,
			Exchange:     ex,
			IsTradingDay: d.IsTradingDay,
		})
	}
	written, err := r.Writer.UpsertTradingDays(ctx, rows)
	if err != nil {
		return ingest.ProcessResult{}, fmt.Errorf("reference trading calendar: persist: %w", err)
	}
	return ingest.ProcessResult{
		DataType:  job.JobType,
		Source:    r.source(),
		Fetched:   len(days),
		Persisted: written,
	}, nil
}

// syncIndexConstituents 取指数当前成分/权重并整期替换。
//
// 参数：`index_code`（必填，如 `000300.SH`）；`start_date` / `end_date`
// （空 = 以 `Now` 为终点回看 120 天）。窗口刻意不默认全历史：`index_weight`
// 是月频快照，120 天保证至少覆盖一期月末；只想取更早的期时显式给区间。
//
// 只保留窗口内最新一期（口径见 `tushare.FetchIndexConstituents`），写面按
// 「整期快照」替换该指数的当前成分——不做逐行 upsert（表无唯一键，
// 见 `store.ReferenceWriter.ReplaceIndexConstituents`）。
func (r *ReferenceRunner) syncIndexConstituents(ctx context.Context, job Job) (ingest.ProcessResult, error) {
	indexCode := strings.TrimSpace(stringField(job.Params, "index_code"))
	if indexCode == "" {
		return ingest.ProcessResult{}, fmt.Errorf("%w: index_code is required", ErrInvalidParams)
	}
	start, err := dateField(job.Params, "start_date")
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	end, err := dateField(job.Params, "end_date")
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	if start.IsZero() || end.IsZero() {
		now := r.now()
		if end.IsZero() {
			end = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		}
		if start.IsZero() {
			start = end.AddDate(0, 0, -120)
		}
	}
	if end.Before(start) {
		return ingest.ProcessResult{}, fmt.Errorf("%w: end_date %s is before start_date %s",
			ErrInvalidParams, end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	rows, err := r.Transport.FetchIndexConstituents(ctx, indexCode, start, end)
	if err != nil {
		return ingest.ProcessResult{}, fmt.Errorf("reference index constituents: fetch: %w", err)
	}
	written, err := r.Writer.ReplaceIndexConstituents(ctx, rows)
	if err != nil {
		return ingest.ProcessResult{}, fmt.Errorf("reference index constituents: persist: %w", err)
	}
	return ingest.ProcessResult{
		DataType:  job.JobType,
		Source:    r.source(),
		Fetched:   len(rows),
		Persisted: written,
	}, nil
}

// syncFundamentals 按**报告期**区间一次拉全多只票的财报并 upsert。
//
// 参数全部必填，刻意不给默认：
//   - `symbols`：要补的票池；
//   - `start_date` / `end_date`：**报告期**截止日区间（不是披露日）。
//
// 为什么不给默认窗口：`fina_indicator` 不传区间时按源端默认页返回，一只票
// 五年有 20 期，静默截断等于让作业「成功但少数据」。调用方必须给出自己需要
// 的报告期范围。
//
// 任一只票取数失败即整体失败：财报写面是幂等 upsert，重跑无副作用；
// 「部分票成功」只会让调用方无法判断还缺哪些票。
func (r *ReferenceRunner) syncFundamentals(ctx context.Context, job Job) (ingest.ProcessResult, error) {
	symbols, err := stringSliceField(job.Params, "symbols")
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	if len(symbols) == 0 {
		return ingest.ProcessResult{}, fmt.Errorf("%w: symbols is required", ErrInvalidParams)
	}
	start, err := dateField(job.Params, "start_date")
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	end, err := dateField(job.Params, "end_date")
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	if start.IsZero() || end.IsZero() {
		return ingest.ProcessResult{}, fmt.Errorf("%w: start_date and end_date (report periods) are required", ErrInvalidParams)
	}
	if end.Before(start) {
		return ingest.ProcessResult{}, fmt.Errorf("%w: end_date %s is before start_date %s",
			ErrInvalidParams, end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	const layout = "2006-01-02"
	startPeriod := start.Format(layout)
	endPeriod := end.Format(layout)

	rows := make([]market.Fundamental, 0, len(symbols)*20)
	for _, symbol := range symbols {
		batch, err := r.Transport.FetchFundamentalsRange(ctx, symbol, startPeriod, endPeriod)
		if err != nil {
			return ingest.ProcessResult{}, fmt.Errorf("reference fundamentals: fetch %s: %w", symbol, err)
		}
		rows = append(rows, batch...)
	}
	written, err := r.Writer.UpsertFundamentals(ctx, rows)
	if err != nil {
		return ingest.ProcessResult{}, fmt.Errorf("reference fundamentals: persist: %w", err)
	}
	return ingest.ProcessResult{
		DataType:  job.JobType,
		Source:    r.source(),
		Fetched:   len(rows),
		Persisted: written,
	}, nil
}

func (r *ReferenceRunner) source() string {
	if r.Source != "" {
		return r.Source
	}
	return "tushare"
}

func (r *ReferenceRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// stringField 读一个字符串参数（缺省 / 非字符串 ⇒ 空串）。
func stringField(params map[string]any, name string) string {
	raw, ok := params[name]
	if !ok || raw == nil {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return s
}
