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

// 租约口径写死（不做配置项，DATA-GRACEFUL-SHUTDOWN ④）：
//   - 领取时写 `lease_until = NOW() + DefaultLease`；
//   - worker 每 DefaultHeartbeatInterval 续租一次（租约的 1/3，允许两次丢包）；
//   - reaper 每 DefaultReapInterval 扫一次过期租约。
//
// 故障 worker 的行最晚 `DefaultLease + DefaultReapInterval` 后重新可领。
const (
	DefaultLease             = 60 * time.Second
	DefaultHeartbeatInterval = DefaultLease / 3
	DefaultReapInterval      = 30 * time.Second
)

// JobStore 是作业队列的存储面。
type JobStore interface {
	// CreateJob 插入一个 pending 作业。
	CreateJob(ctx context.Context, job Job) error
	// GetJob 按 ID 取作业；不存在 ⇒ ErrJobNotFound。
	GetJob(ctx context.Context, id string) (Job, error)
	// ListJobs 按过滤条件倒序列出作业。
	ListJobs(ctx context.Context, filter JobFilter) ([]Job, error)
	// ClaimJob 原子领取一个 pending 作业（SKIP LOCKED）并写入租约；
	// 没有可领的 ⇒ (nil, nil)。
	ClaimJob(ctx context.Context, workerID string) (*Job, error)
	// FinishJob 写终态（succeeded / failed）、结果与错误文案。
	// 只有仍持有该作业（worker_id 匹配且仍是 running）才写得进；
	// 否则 ⇒ ErrLeaseLost。
	FinishJob(ctx context.Context, id, workerID, status string, result map[string]any, errMessage string) error
	// CancelJob 只允许取消 pending；否则 ErrJobNotCancellable。
	CancelJob(ctx context.Context, id string) error
	// RetryJob 把 failed/cancelled 且未超重试上限的作业重新置为 pending。
	RetryJob(ctx context.Context, id string) error
}

// LeaseReaper 是过期租约的回收面（进程级 reaper 只需要这一条）。
type LeaseReaper interface {
	// ReapExpired 回收「running 且 lease_until <= now」的行：retry_count+1 后
	// 回 pending，或用尽预算时判 failed。返回回收行数。
	ReapExpired(ctx context.Context, now time.Time) (int, error)
}

// LeaseStore 是作业租约面：worker 续租 / 停机交还，reaper 回收过期租约。
//
// 与 JobStore 分开：HTTP 面只做队列 CRUD，不该具备伪造心跳或回收的能力。
type LeaseStore interface {
	LeaseReaper
	// HeartbeatJob 续租。仍持有（running 且 worker_id 匹配）⇒ 延长 lease_until；
	// 否则 ⇒ ErrLeaseLost。
	HeartbeatJob(ctx context.Context, id, workerID string) error
	// ReleaseJob 交还作业（优雅停机/被中断时）。重试预算未用尽 ⇒ 回 pending
	// 供别的 worker 重领；用尽 ⇒ failed 并写明 reason。未持有 ⇒ ErrLeaseLost。
	ReleaseJob(ctx context.Context, id, workerID, reason string) error
}

// WorkerStore 是执行循环需要的面（队列 + 租约；不需要排程 CRUD）。
type WorkerStore interface {
	JobStore
	LeaseStore
}

// FullStore 是 PG 实现暴露的完整能力面：HTTP 面（队列 + 排程）+ 租约面。
// 进程装配只依赖这一层，再按消费点收窄成 `Store` / `WorkerStore` /
// `LeaseReaper`。
type FullStore interface {
	Store
	LeaseStore
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
