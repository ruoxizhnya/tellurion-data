package syncjob

// 队列/排程的存储 port。
//
// 拆成 JobStore / ScheduleStore 两个面而不是一个：worker 只该看见作业队列，
// scheduler 只该看见排程。合成一个宽接口会让「worker 也能改排程」这种越界
// 在编译期看不出来。
//
// 实现：`pgStore`（`ingest.sync_jobs` / `ingest.sync_schedules`）。

import (
	"context"
	"time"
)

// JobFilter 是列表查询的过滤条件。零值 = 最近 50 条。
type JobFilter struct {
	Status  string
	JobType string
	Limit   int
}

// JobStore 是作业队列的存储面。
type JobStore interface {
	// CreateJob 插入一个 pending 作业。
	CreateJob(ctx context.Context, job Job) error
	// GetJob 按 ID 取作业；不存在 ⇒ ErrJobNotFound。
	GetJob(ctx context.Context, id string) (Job, error)
	// ListJobs 按过滤条件倒序列出作业。
	ListJobs(ctx context.Context, filter JobFilter) ([]Job, error)
	// ClaimJob 原子领取一个 pending 作业（SKIP LOCKED）；没有可领的 ⇒ (nil, nil)。
	ClaimJob(ctx context.Context, workerID string) (*Job, error)
	// FinishJob 写终态（succeeded / failed）、结果与错误文案。
	FinishJob(ctx context.Context, id, status string, result map[string]any, errMessage string) error
	// CancelJob 只允许取消 pending；否则 ErrJobNotCancellable。
	CancelJob(ctx context.Context, id string) error
	// RetryJob 把 failed/cancelled 且未超重试上限的作业重新置为 pending。
	RetryJob(ctx context.Context, id string) error
}

// ScheduleStore 是排程的存储面。
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, in ScheduleInput) (Schedule, error)
	UpdateSchedule(ctx context.Context, id int64, in ScheduleInput) (Schedule, error)
	DeleteSchedule(ctx context.Context, id int64) error
	// ToggleSchedule 翻转 is_active，并按新状态重算 next_run_at。
	ToggleSchedule(ctx context.Context, id int64) (Schedule, error)
	GetSchedule(ctx context.Context, id int64) (Schedule, error)
	ListSchedules(ctx context.Context) ([]Schedule, error)
	// DueSchedules 返回 is_active 且 next_run_at <= now 的排程。
	DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error)
	// EnqueueFromSchedule 在同一事务里插入作业并推进排程的
	// last_run_* / next_run_at —— 两步必须同笔，否则「作业建了但排程没推进」
	// 会让同一个 cron 在下一个 tick 再建一遍。
	EnqueueFromSchedule(ctx context.Context, scheduleID int64, job Job, nextRun time.Time) error
}

// Store 是完整存储面（HTTP 面与 scheduler 都按需取子集）。
type Store interface {
	JobStore
	ScheduleStore
}
