// Package syncjob 是同步作业的执行面：作业模型、队列（`ingest.sync_jobs`）、
// 执行器（取数 → ETL → 落库）与排程（`ingest.sync_schedules` + 5 段 cron）。
//
// 为什么在本仓：core `SVC-CORE-DATA-RETIRE` 删掉了 `pkg/data/**` 与
// `cmd/data/**`，而作业链（`sync_jobs` → worker → ETL）就在那里面。用户
// 2026-11-10 裁决 **B：本仓自持传输与调度**，本包是「调度」那一半。
//
// 职责边界：
//   - 本包**不认识任何具体数据源**：取数/归一由 `ingest.Registry` +
//     `ingest.ETLPipeline` 完成，本包只负责「什么时候、按什么参数、把哪个
//     data_type 跑一遍，以及结果怎么记账」；
//   - 作业类型 = data_type（`job_type` 就是 data_type 的字面值）：本仓的
//     声明表已经回答了「哪些类型可跑、锚点在哪」，再造一套 job_type 别名
//     就是双真相。**唯二例外**是参考数据腿的两个保留字面值
//     （`stocks` / `trading_calendar`）：它们不是点位流，走 upsert 而不是
//     `BulkInsert`，见 `reference.go`；
//   - 队列状态只有五种（pending / running / succeeded / failed / cancelled），
//     不引入「部分成功」——部分成功会让「这个作业到底补没补完」变成猜谜。
package syncjob

// 本文件只放作业模型与参数解析；队列语义在 store.go，执行在 runner.go。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"tellurion-data/internal/ingest"
)

// 作业状态。与 `ingest.sync_jobs.status` 的取值逐字一致。
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// 队列层哨兵。HTTP 层按它们分诊成 404 / 409。
var (
	// ErrJobNotFound 表示作业不存在。
	ErrJobNotFound = errors.New("syncjob: job not found")
	// ErrJobNotCancellable 表示作业已不在 pending（运行中/已结束）。
	// 运行中的作业**不能**被标成 cancelled：那会让还没停下的 goroutine
	// 在结束后把状态覆盖回 succeeded，库里留下一个自相矛盾的终态。
	ErrJobNotCancellable = errors.New("syncjob: only pending jobs can be cancelled")
	// ErrJobNotRetryable 表示作业不是 failed/cancelled，或重试次数已用尽。
	ErrJobNotRetryable = errors.New("syncjob: job is not retryable (must be failed/cancelled and below max_retries)")
	// ErrLeaseLost 表示 worker 已不再持有该作业（租约过期被回收、
	// 已被别的 worker 领取，或作业已终态）。持有方必须立即停止并**禁止**
	// 再写终态，否则两个 worker 会各写一份。
	ErrLeaseLost = errors.New("syncjob: job lease lost")
	// ErrScheduleNotFound 表示排程不存在。
	ErrScheduleNotFound = errors.New("syncjob: schedule not found")
	// ErrInvalidParams 表示请求参数不合法（调用方修请求）。
	ErrInvalidParams = errors.New("syncjob: invalid job params")
)

// Job 是 `ingest.sync_jobs` 的一行。
//
// JSON tag 即 HTTP 面（`docs/api-schema.md` §4）的线格式：一个作业在库里与
// 响应里用同一组字段名，避免「内部叫 job_type、对外叫 type」的二次映射。
type Job struct {
	ID              string         `json:"id"`
	JobType         string         `json:"job_type"`
	Status          string         `json:"status"`
	Params          map[string]any `json:"params"`
	ProgressPercent int            `json:"progress_percent"`
	TotalItems      int            `json:"total_items"`
	ProcessedItems  int            `json:"processed_items"`
	FailedItems     int            `json:"failed_items"`
	ErrorMessage    string         `json:"error_message,omitempty"`
	Result          map[string]any `json:"result,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	RetryCount      int            `json:"retry_count"`
	MaxRetries      int            `json:"max_retries"`
	ScheduledAt     *time.Time     `json:"scheduled_at,omitempty"`
	WorkerID        string         `json:"worker_id,omitempty"`
}

// Duration 返回已运行时长（未开始或未结束时的口径：未开始 = 0，运行中 =
// 到现在，已结束 = 终态时刻 − 开始时刻）。
func (j Job) Duration(now time.Time) time.Duration {
	if j.StartedAt == nil {
		return 0
	}
	if j.CompletedAt != nil {
		return j.CompletedAt.Sub(*j.StartedAt)
	}
	return now.Sub(*j.StartedAt)
}

// NewJob 生成一个 pending 作业（ID 为 16 位随机十六进制）。
//
// 随机而不是自增：作业 ID 会被调用方拼进回调/日志，自增值会泄露队列规模
// 且便于猜测。
func NewJob(jobType string, params map[string]any, maxRetries int) (Job, error) {
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return Job{}, fmt.Errorf("%w: job_type is required", ErrInvalidParams)
	}
	if maxRetries < 0 {
		return Job{}, fmt.Errorf("%w: max_retries must be >= 0", ErrInvalidParams)
	}
	if params == nil {
		params = map[string]any{}
	}
	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	return Job{
		ID:         id,
		JobType:    jobType,
		Status:     StatusPending,
		Params:     params,
		MaxRetries: maxRetries,
	}, nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("syncjob: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// FetchRequestFromParams 把作业参数解析成 `ingest.FetchRequest`。
//
// 契约：`job_type` 是权威的 data_type；params 里的 `data_type` 只允许缺省或
// 同名（不同名 = 调用方把两个作业串了，必须报错而不是二选一猜）。
//
// 日期接受 `YYYY-MM-DD` 与 `YYYYMMDD`；缺省为零值（该 connector 若不接受
// 零日期会在取数侧报错，本层不替它编一个默认窗口）。
func FetchRequestFromParams(jobType string, params map[string]any) (ingest.FetchRequest, error) {
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return ingest.FetchRequest{}, fmt.Errorf("%w: job_type is required", ErrInvalidParams)
	}
	req := ingest.FetchRequest{DataType: jobType}

	if raw, ok := params["data_type"]; ok {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" && strings.TrimSpace(s) != jobType {
			return ingest.FetchRequest{}, fmt.Errorf("%w: params.data_type %q != job_type %q", ErrInvalidParams, s, jobType)
		}
	}

	symbols, err := stringSliceField(params, "symbols")
	if err != nil {
		return ingest.FetchRequest{}, err
	}
	req.Symbols = symbols

	if req.StartDate, err = dateField(params, "start_date"); err != nil {
		return ingest.FetchRequest{}, err
	}
	if req.EndDate, err = dateField(params, "end_date"); err != nil {
		return ingest.FetchRequest{}, err
	}
	if !req.StartDate.IsZero() && !req.EndDate.IsZero() && req.EndDate.Before(req.StartDate) {
		return ingest.FetchRequest{}, fmt.Errorf("%w: end_date %s is before start_date %s",
			ErrInvalidParams, req.EndDate.Format("2006-01-02"), req.StartDate.Format("2006-01-02"))
	}

	if raw, ok := params["period"]; ok {
		if s, ok := raw.(string); ok {
			req.Period = s
		}
	}
	if raw, ok := params["extra"]; ok {
		if m, ok := raw.(map[string]any); ok {
			req.Extra = m
		}
	}
	return req, nil
}

// stringSliceField 读一个字符串数组字段（JSON 数组解码后是 []any）。
func stringSliceField(params map[string]any, name string) ([]string, error) {
	raw, ok := params[name]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %s[%d] is not a string", ErrInvalidParams, name, i)
			}
			if strings.TrimSpace(s) == "" {
				continue
			}
			out = append(out, strings.TrimSpace(s))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: %s must be an array of strings", ErrInvalidParams, name)
	}
}

// dateField 读一个日期字段（"" / 缺省 ⇒ 零值）。
func dateField(params map[string]any, name string) (time.Time, error) {
	raw, ok := params[name]
	if !ok || raw == nil {
		return time.Time{}, nil
	}
	s, ok := raw.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("%w: %s must be a string", ErrInvalidParams, name)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", "20060102", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: %s %q is not a date (YYYY-MM-DD or YYYYMMDD)", ErrInvalidParams, name, s)
}

// Schedule 是 `ingest.sync_schedules` 的一行。
type Schedule struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	JobType       string         `json:"job_type"`
	Cron          string         `json:"cron_expression"`
	Params        map[string]any `json:"params"`
	IsActive      bool           `json:"is_active"`
	LastRunAt     *time.Time     `json:"last_run_at,omitempty"`
	LastRunStatus string         `json:"last_run_status,omitempty"`
	LastRunJobID  string         `json:"last_run_job_id,omitempty"`
	NextRunAt     *time.Time     `json:"next_run_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	CreatedBy     string         `json:"created_by,omitempty"`
}

// ScheduleInput 是排程的创建/更新输入。
type ScheduleInput struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	JobType     string         `json:"job_type"`
	Cron        string         `json:"cron_expression"`
	Params      map[string]any `json:"params"`
	IsActive    bool           `json:"is_active"`
	CreatedBy   string         `json:"-"`
}

// Validate 校验排程输入：名字/类型必填、cron 可解析、参数可解析成请求。
//
// 在这里就解析一次参数与 cron，而不是等到触发时才失败：一个永远解析不了的
// 排程留在库里，每次 tick 都要失败一次，日志里全是同一条噪音。
func (in *ScheduleInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.JobType = strings.TrimSpace(in.JobType)
	in.Cron = strings.TrimSpace(in.Cron)
	if in.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidParams)
	}
	if in.JobType == "" {
		return fmt.Errorf("%w: job_type is required", ErrInvalidParams)
	}
	if in.Params == nil {
		in.Params = map[string]any{}
	}
	if _, err := FetchRequestFromParams(in.JobType, in.Params); err != nil {
		return err
	}
	if _, err := NextRun(in.Cron, time.Now()); err != nil {
		return fmt.Errorf("%w: cron_expression %q: %v", ErrInvalidParams, in.Cron, err)
	}
	return nil
}
