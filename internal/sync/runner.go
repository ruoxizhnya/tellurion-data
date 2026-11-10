package syncjob

// 作业执行：把一行 `sync_jobs` 变成一次「取数 → 归一 → 校验 → 闸门 → 去重
// → 落库」。管线本身（`ingest.ETLPipeline`）一行不改，本文件只做参数翻译与
// 计数记账。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tellurion-data/internal/ingest"
)

// ErrMisconfigured 是装配错误（nil store / nil executor）。启动期 bug，
// 与「作业参数不合法」分开。
var ErrMisconfigured = errors.New("syncjob: misconfigured")

// Executor 是执行面 port。`*ingest.ETLPipeline` 直接满足。
type Executor interface {
	Process(ctx context.Context, req ingest.FetchRequest, normalizer ingest.Normalizer) (ingest.ProcessResult, error)
}

// PassthroughNormalizer 把取数条目原样转成点位。
//
// 为什么是透传而不是再写一层归一：本仓的 connector（`internal/ingest`）
// 已经按 `DataSchema` 把源格式折成规范字段名，`DataItem.Data` 就是写面要的
// 列。再归一一次只会引入第二套口径（两套口径都「看起来对」时没人能判对错）。
// 缺失值语义由 connector 保证（nil = 缺失），本函数不补默认值。
func PassthroughNormalizer(item ingest.DataItem, source, dataType string) ingest.UnifiedDataPoint {
	return ingest.UnifiedDataPoint{
		Symbol:    item.Symbol,
		TradeTime: item.TradeTime,
		Source:    source,
		DataType:  dataType,
		Data:      item.Data,
	}
}

// Runner 执行单个作业。
type Runner struct {
	Executor Executor
}

// NewRunner 构造执行器；executor 为 nil 时 Execute 返回 ErrMisconfigured。
func NewRunner(executor Executor) *Runner { return &Runner{Executor: executor} }

// Execute 跑一个作业并返回管线计数。
//
// 参数不合法（`FetchRequestFromParams` 失败）不会触达任何上游——这类失败
// 重试一万次也一样，worker 会把它记成 failed 而不是重试。
func (r *Runner) Execute(ctx context.Context, job Job) (ingest.ProcessResult, error) {
	if r == nil || r.Executor == nil {
		return ingest.ProcessResult{}, ErrMisconfigured
	}
	req, err := FetchRequestFromParams(job.JobType, job.Params)
	if err != nil {
		return ingest.ProcessResult{}, err
	}
	return r.Executor.Process(ctx, req, PassthroughNormalizer)
}

// ResultMap 把管线计数折成 `sync_jobs.result`（JSONB）的形状。
//
// 字段名与 `ingest.ProcessResult` 同义：调用方读作业结果时不需要记两套词。
func ResultMap(result ingest.ProcessResult, duration time.Duration) map[string]any {
	return map[string]any{
		"data_type":   result.DataType,
		"source":      result.Source,
		"fetched":     result.Fetched,
		"persisted":   result.Persisted,
		"skipped":     result.Skipped,
		"duration_ms": duration.Milliseconds(),
	}
}

// errorText 把失败折成一行可读文本（写进 error_message 列）。
func errorText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 4000 {
		// error_message 是 TEXT，但一行超长错误只会让日志/UI 都难读。
		msg = msg[:4000] + "…(truncated)"
	}
	return msg
}

// describeJob 是日志行的统一形状（不要在这里拼业务语义）。
func describeJob(job Job) string {
	return fmt.Sprintf("job=%s type=%s retry=%d/%d", job.ID, job.JobType, job.RetryCount, job.MaxRetries)
}
