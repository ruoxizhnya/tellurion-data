package syncjob

// `ingest.sync_jobs` / `ingest.sync_schedules` 的 pgx 实现。
//
// 三条口径：
//   - 领取用 `FOR UPDATE SKIP LOCKED`：多 worker 并跑时同一个作业只会被一个
//     worker 拿到，且不互相阻塞；
//   - 终态写入是**幂等覆盖**：worker 崩溃重启后同一作业不会再被领取
//     （status 已不是 pending），所以覆盖只发生在「本次执行结束」这一刻；
//   - 时间一律由数据库 `NOW()` 或调用方时钟给出，不在这里读墙钟拼 SQL
//     逻辑以外的判定。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgStore 是 Store 的 PostgreSQL 实现。
type pgStore struct {
	pool *pgxpool.Pool
	// now 是可注入时钟（排程 next_run 计算用）。
	now func() time.Time
}

// NewPGStore 用连接池构造存储。now 为 nil 时用墙钟。
func NewPGStore(pool *pgxpool.Pool, now func() time.Time) Store {
	if now == nil {
		now = time.Now
	}
	return &pgStore{pool: pool, now: now}
}

// jobColumns 是 Job 的列序（扫描顺序必须与 scanJob 一致）。
const jobColumns = `id, job_type, status, params, progress_percent, total_items,
	processed_items, failed_items, error_message, result, created_at, started_at,
	completed_at, retry_count, max_retries, scheduled_at, worker_id`

// CreateJob 插入 pending 作业。
func (s *pgStore) CreateJob(ctx context.Context, job Job) error {
	params, err := json.Marshal(job.Params)
	if err != nil {
		return fmt.Errorf("syncjob: marshal params: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO ingest.sync_jobs (id, job_type, status, params, max_retries, scheduled_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6)`,
		job.ID, job.JobType, job.Status, string(params), job.MaxRetries, job.ScheduledAt)
	if err != nil {
		return fmt.Errorf("syncjob: create job %s: %w", job.ID, err)
	}
	return nil
}

// GetJob 按 ID 取作业。
func (s *pgStore) GetJob(ctx context.Context, id string) (Job, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM ingest.sync_jobs WHERE id = $1`, id)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("syncjob: get job %s: %w", id, err)
	}
	return job, nil
}

// ListJobs 按过滤条件倒序列出作业。
func (s *pgStore) ListJobs(ctx context.Context, filter JobFilter) ([]Job, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+jobColumns+` FROM ingest.sync_jobs
		WHERE ($1 = '' OR status = $1) AND ($2 = '' OR job_type = $2)
		ORDER BY created_at DESC
		LIMIT $3`, filter.Status, filter.JobType, limit)
	if err != nil {
		return nil, fmt.Errorf("syncjob: list jobs: %w", err)
	}
	defer rows.Close()

	out := make([]Job, 0, limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("syncjob: scan job: %w", err)
		}
		out = append(out, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncjob: list jobs: %w", err)
	}
	return out, nil
}

// ClaimJob 原子领取一个可运行的 pending 作业。
func (s *pgStore) ClaimJob(ctx context.Context, workerID string) (*Job, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE ingest.sync_jobs
		SET status = 'running', started_at = NOW(), worker_id = $1
		WHERE id = (
			SELECT id FROM ingest.sync_jobs
			WHERE status = 'pending' AND (scheduled_at IS NULL OR scheduled_at <= NOW())
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+jobColumns, workerID)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("syncjob: claim job: %w", err)
	}
	return &job, nil
}

// jobCounts 从结案结果里折出作业计数。
//
// 口径（与 `ingest.ProcessResult` 同义）：
//   - total_items = fetched（取到的行数，含被跳过/重复的行）；
//   - processed_items = persisted + skipped（已处理完的行：入库 + 判定跳过）；
//   - failed_items = 0（成功终态；失败终态没有 per-item 计数）。
//
// 为什么写这三个列：`Job` 的 JSON 一直带着它们，UI 的作业队列渲染
// `processed_items/total_items`；不写就永远是 0/0，等于界面在撒谎。
// 旧实现只在成功时写 progress_percent=100，这三列从未被任何语句赋值。
func jobCounts(result map[string]any) (total, processed, failed int) {
	if result == nil {
		return 0, 0, 0
	}
	total = intField(result, "fetched")
	processed = intField(result, "persisted") + intField(result, "skipped")
	if processed > total {
		processed = total
	}
	return total, processed, 0
}

// intField 读 result 里的整型字段（JSON 解码出的数字可能是 float64）。
func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// FinishJob 写终态。
func (s *pgStore) FinishJob(ctx context.Context, id, status string, result map[string]any, errMessage string) error {
	total, processed, failed := jobCounts(result)
	var payload []byte
	if result != nil {
		var err error
		if payload, err = json.Marshal(result); err != nil {
			return fmt.Errorf("syncjob: marshal result: %w", err)
		}
	}
	var resultArg any
	if payload != nil {
		resultArg = string(payload)
	}
	var errArg any
	if errMessage != "" {
		errArg = errMessage
	}
	// `$2::text` 两处都显式标注：左边 `status` 是 varchar(20)，右边 CASE 与字面量
	// 比较推成 text。PG 对同一参数在不同上下文推出不同类型会直接报
	// 42P08 inconsistent types，症状是「作业失败却写不进终态」。
	ct, err := s.pool.Exec(ctx, `
		UPDATE ingest.sync_jobs
		SET status = $2::text,
		    result = $3::jsonb,
		    error_message = $4,
		    completed_at = NOW(),
		    total_items = $5,
		    processed_items = $6,
		    failed_items = $7,
		    progress_percent = CASE WHEN $2::text = 'succeeded' THEN 100 ELSE progress_percent END
		WHERE id = $1`, id, status, resultArg, errArg, total, processed, failed)
	if err != nil {
		return fmt.Errorf("syncjob: finish job %s: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

// CancelJob 取消 pending 作业。
func (s *pgStore) CancelJob(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE ingest.sync_jobs
		SET status = 'cancelled', completed_at = NOW()
		WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("syncjob: cancel job %s: %w", id, err)
	}
	if ct.RowsAffected() > 0 {
		return nil
	}
	if _, err := s.GetJob(ctx, id); err != nil {
		return err
	}
	return ErrJobNotCancellable
}

// RetryJob 把失败的作业重新排队。
func (s *pgStore) RetryJob(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE ingest.sync_jobs
		SET status = 'pending', error_message = NULL, result = NULL,
		    started_at = NULL, completed_at = NULL, worker_id = NULL,
		    retry_count = retry_count + 1
		WHERE id = $1 AND status IN ('failed', 'cancelled') AND retry_count < max_retries`, id)
	if err != nil {
		return fmt.Errorf("syncjob: retry job %s: %w", id, err)
	}
	if ct.RowsAffected() > 0 {
		return nil
	}
	if _, err := s.GetJob(ctx, id); err != nil {
		return err
	}
	return ErrJobNotRetryable
}

// scheduleColumns 是 Schedule 的列序（与 scanSchedule 一致）。
const scheduleColumns = `id, name, COALESCE(description, ''), job_type, cron_expression,
	params, is_active, last_run_at, COALESCE(last_run_status, ''), COALESCE(last_run_job_id, ''),
	next_run_at, created_at, updated_at, COALESCE(created_by, 'system')`

// CreateSchedule 插入排程并算好首次 next_run_at。
func (s *pgStore) CreateSchedule(ctx context.Context, in ScheduleInput) (Schedule, error) {
	params, err := json.Marshal(in.Params)
	if err != nil {
		return Schedule{}, fmt.Errorf("syncjob: marshal schedule params: %w", err)
	}
	next, err := s.nextRunFor(in)
	if err != nil {
		return Schedule{}, err
	}
	creator := in.CreatedBy
	if creator == "" {
		creator = "system"
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO ingest.sync_schedules
			(name, description, job_type, cron_expression, params, is_active, next_run_at, created_by)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		RETURNING `+scheduleColumns,
		in.Name, in.Description, in.JobType, in.Cron, string(params), in.IsActive, next, creator)
	sched, err := scanSchedule(row)
	if err != nil {
		return Schedule{}, fmt.Errorf("syncjob: create schedule: %w", err)
	}
	return sched, nil
}

// UpdateSchedule 覆盖排程的可编辑字段。
func (s *pgStore) UpdateSchedule(ctx context.Context, id int64, in ScheduleInput) (Schedule, error) {
	params, err := json.Marshal(in.Params)
	if err != nil {
		return Schedule{}, fmt.Errorf("syncjob: marshal schedule params: %w", err)
	}
	next, err := s.nextRunFor(in)
	if err != nil {
		return Schedule{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE ingest.sync_schedules
		SET name = $2, description = $3, job_type = $4, cron_expression = $5,
		    params = $6::jsonb, is_active = $7, next_run_at = $8, updated_at = NOW()
		WHERE id = $1
		RETURNING `+scheduleColumns,
		id, in.Name, in.Description, in.JobType, in.Cron, string(params), in.IsActive, next)
	sched, err := scanSchedule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrScheduleNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("syncjob: update schedule %d: %w", id, err)
	}
	return sched, nil
}

// DeleteSchedule 删除排程。
func (s *pgStore) DeleteSchedule(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM ingest.sync_schedules WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("syncjob: delete schedule %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// ToggleSchedule 翻转启用状态。
func (s *pgStore) ToggleSchedule(ctx context.Context, id int64) (Schedule, error) {
	current, err := s.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	next := (*time.Time)(nil)
	if !current.IsActive {
		computed, err := NextRun(current.Cron, s.now())
		if err != nil {
			return Schedule{}, fmt.Errorf("%w: stored cron %q: %v", ErrInvalidParams, current.Cron, err)
		}
		next = &computed
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE ingest.sync_schedules
		SET is_active = NOT is_active, next_run_at = $2, updated_at = NOW()
		WHERE id = $1
		RETURNING `+scheduleColumns, id, next)
	sched, err := scanSchedule(row)
	if err != nil {
		return Schedule{}, fmt.Errorf("syncjob: toggle schedule %d: %w", id, err)
	}
	return sched, nil
}

// GetSchedule 按 ID 取排程。
func (s *pgStore) GetSchedule(ctx context.Context, id int64) (Schedule, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM ingest.sync_schedules WHERE id = $1`, id)
	sched, err := scanSchedule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrScheduleNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("syncjob: get schedule %d: %w", id, err)
	}
	return sched, nil
}

// ListSchedules 列出全部排程（按 id 升序，稳定输出）。
func (s *pgStore) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+scheduleColumns+` FROM ingest.sync_schedules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("syncjob: list schedules: %w", err)
	}
	defer rows.Close()

	out := make([]Schedule, 0, 8)
	for rows.Next() {
		sched, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("syncjob: scan schedule: %w", err)
		}
		out = append(out, sched)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncjob: list schedules: %w", err)
	}
	return out, nil
}

// DueSchedules 返回到期排程。
func (s *pgStore) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+scheduleColumns+` FROM ingest.sync_schedules
		WHERE is_active AND next_run_at IS NOT NULL AND next_run_at <= $1
		ORDER BY next_run_at`, now)
	if err != nil {
		return nil, fmt.Errorf("syncjob: due schedules: %w", err)
	}
	defer rows.Close()

	out := make([]Schedule, 0, 4)
	for rows.Next() {
		sched, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("syncjob: scan due schedule: %w", err)
		}
		out = append(out, sched)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncjob: due schedules: %w", err)
	}
	return out, nil
}

// EnqueueFromSchedule 在同一事务里插入作业并推进排程。
func (s *pgStore) EnqueueFromSchedule(ctx context.Context, scheduleID int64, job Job, nextRun time.Time) error {
	params, err := json.Marshal(job.Params)
	if err != nil {
		return fmt.Errorf("syncjob: marshal job params: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("syncjob: begin enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingest.sync_jobs (id, job_type, status, params, max_retries, scheduled_at)
		VALUES ($1, $2, 'pending', $3::jsonb, $4, NOW())`,
		job.ID, job.JobType, string(params), job.MaxRetries); err != nil {
		return fmt.Errorf("syncjob: enqueue job %s: %w", job.ID, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ingest.sync_schedules
		SET last_run_at = NOW(), last_run_status = 'pending', last_run_job_id = $2,
		    next_run_at = $3, updated_at = NOW()
		WHERE id = $1`, scheduleID, job.ID, nextRun); err != nil {
		return fmt.Errorf("syncjob: advance schedule %d: %w", scheduleID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("syncjob: commit enqueue: %w", err)
	}
	return nil
}

// nextRunFor 计算排程的 next_run_at：停用 ⇒ NULL。
func (s *pgStore) nextRunFor(in ScheduleInput) (*time.Time, error) {
	if !in.IsActive {
		return nil, nil
	}
	next, err := NextRun(in.Cron, s.now())
	if err != nil {
		return nil, fmt.Errorf("%w: cron_expression %q: %v", ErrInvalidParams, in.Cron, err)
	}
	return &next, nil
}

// rowScanner 抽象 pgx.Row 与 pgx.Rows（两者都有 Scan）。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob 把一行扫成 Job。
func scanJob(row rowScanner) (Job, error) {
	var (
		job       Job
		params    []byte
		errorMsg  *string
		result    []byte
		startedAt *time.Time
		doneAt    *time.Time
		schedAt   *time.Time
		workerID  *string
	)
	if err := row.Scan(
		&job.ID, &job.JobType, &job.Status, &params, &job.ProgressPercent,
		&job.TotalItems, &job.ProcessedItems, &job.FailedItems, &errorMsg, &result,
		&job.CreatedAt, &startedAt, &doneAt, &job.RetryCount, &job.MaxRetries,
		&schedAt, &workerID,
	); err != nil {
		return Job{}, err
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &job.Params); err != nil {
			return Job{}, fmt.Errorf("decode params: %w", err)
		}
	}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &job.Result); err != nil {
			return Job{}, fmt.Errorf("decode result: %w", err)
		}
	}
	if errorMsg != nil {
		job.ErrorMessage = *errorMsg
	}
	if workerID != nil {
		job.WorkerID = *workerID
	}
	job.StartedAt = startedAt
	job.CompletedAt = doneAt
	job.ScheduledAt = schedAt
	return job, nil
}

// scanSchedule 把一行扫成 Schedule。
func scanSchedule(row rowScanner) (Schedule, error) {
	var (
		sched     Schedule
		params    []byte
		lastRunAt *time.Time
		nextRunAt *time.Time
	)
	if err := row.Scan(
		&sched.ID, &sched.Name, &sched.Description, &sched.JobType, &sched.Cron,
		&params, &sched.IsActive, &lastRunAt, &sched.LastRunStatus, &sched.LastRunJobID,
		&nextRunAt, &sched.CreatedAt, &sched.UpdatedAt, &sched.CreatedBy,
	); err != nil {
		return Schedule{}, err
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &sched.Params); err != nil {
			return Schedule{}, fmt.Errorf("decode schedule params: %w", err)
		}
	}
	sched.LastRunAt = lastRunAt
	sched.NextRunAt = nextRunAt
	return sched, nil
}
