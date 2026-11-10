package syncjob

// 参考数据同步：`stocks` / `trading_calendar`。
//
// 为什么旁路 ETL：见 `internal/store/reference_write.go` 的头注释。这两张表
// 是 C 集参照数据，没有 data_type 声明、没有 visible_at 锚点，走不了
// `FetchRequestFromParams → Pipeline → BulkInsert` 那条链。
//
// 作业类型因此是两个**保留字面值**（不是 data_type）：
//
//	`stocks`           → FetchStocks + UpsertStocks
//	`trading_calendar` → FetchTradeCalendar + UpsertTradingDays
//
// 判定发生在 `Runner.Execute` 的最前面；命中即走本文件，不经过
// `FetchRequestFromParams` 的 data_type 一致性校验（那层只属于点位管线）。

import (
	"context"
	"fmt"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/store"
)

// 参考数据作业类型（保留字面值，与 data_type 命名空间互斥）。
const (
	JobTypeStocks          = "stocks"
	JobTypeTradingCalendar = "trading_calendar"
)

// IsReferenceJobType 判断一个 job_type 是否属于参考数据腿。
func IsReferenceJobType(jobType string) bool {
	return jobType == JobTypeStocks || jobType == JobTypeTradingCalendar
}

// ReferenceTransport 是参考数据同步的取数面（`*tushare.Client` 满足）。
type ReferenceTransport interface {
	FetchStocks(ctx context.Context, exchange, listStatus string) ([]market.Stock, error)
	FetchTradeCalendar(ctx context.Context, exchange string, start, end time.Time) ([]ingest.TradingDay, error)
}

// ReferenceWriter 是参考数据的写面（`*store.ReferenceWriter` 满足）。
type ReferenceWriter interface {
	UpsertStocks(ctx context.Context, stocks []market.Stock) (int, error)
	UpsertTradingDays(ctx context.Context, days []store.TradingDay) (int, error)
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
