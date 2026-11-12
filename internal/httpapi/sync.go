package httpapi

// 同步作业与排程的 HTTP 面（`docs/api-schema.md` §4）。
//
// 分诊口径：
//   - 参数不合法（job_type 缺失、日期坏、cron 解析不了）⇒ 400，调用方修请求；
//   - 作业/排程不存在 ⇒ 404；
//   - 状态不允许（运行中取消、超重试上限再重试）⇒ 409；
//   - 队列未接线（DATABASE_URL 为空）⇒ 503，与其余数据端点同口径。
//
// 为什么作业是 POST + 202 而不是同步返回结果：一次同步可能要跑几分钟
// （全市场日线），HTTP 连接不该被当成事务边界——这正是 `docs/dataflow.md` §2
// 那条「无界批量一律走作业层」的另一面。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tellurion-data/internal/sync"
)

// maxSyncBodyBytes 是作业/排程请求体的上限（参数体只有几十字节，1MiB 足够）。
const maxSyncBodyBytes = 1 << 20

// WithSync 装配作业队列与排程存储。nil = 未接线 ⇒ 相关端点 503。
func WithSync(store syncjob.Store) Option {
	return func(s *Server) { s.sync = store }
}

// HasSyncQueue 报告作业队列是否已接线（启动日志用）。
func (s *Server) HasSyncQueue() bool { return s.sync != nil }

// createJobRequest 是 `POST /api/sync/jobs` 的请求体。
type createJobRequest struct {
	JobType    string         `json:"job_type"`
	Params     map[string]any `json:"params"`
	MaxRetries *int           `json:"max_retries"`
}

// handleCreateSyncJob 建一个 pending 作业（worker 会在下一轮领取）。
func (s *Server) handleCreateSyncJob(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}

	var req createJobRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSyncBodyBytes))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		writeError(w, fmt.Errorf("%w: body is not a valid job request: %v", syncjob.ErrInvalidParams, err))
		return
	}

	// 默认重试次数与 DDL 一致（3）。显式 max_retries=0 表示「不重试」。
	maxRetries := defaultJobMaxRetries
	if req.MaxRetries != nil {
		maxRetries = *req.MaxRetries
	}
	job, err := syncjob.NewJob(req.JobType, req.Params, maxRetries)
	if err != nil {
		writeError(w, err)
		return
	}
	// 建作业前先解析一次参数：一个永远解析不了的作业不该入库后每轮失败一次。
	if _, err := syncjob.FetchRequestFromParams(job.JobType, job.Params); err != nil {
		writeError(w, err)
		return
	}
	if err := s.sync.CreateJob(r.Context(), job); err != nil {
		writeError(w, err)
		return
	}
	created, err := s.sync.GetJob(r.Context(), job.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, created)
}

// syncJobsListResponse 是列表读面的报文形状。`jobs` 字段是既有形状，
// `limit` / `offset` 是 2026-11-12 加的**回显**字段（加法，不动 `jobs`）。
type syncJobsListResponse struct {
	Jobs   []syncjob.Job `json:"jobs"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// handleListSyncJobs 列出作业（可按 status / job_type 过滤，倒序）。
// limit 缺省 50、上限 200；offset 缺省 0；越界一律 400，不静默截断。
func (s *Server) handleListSyncJobs(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	q := r.URL.Query()
	limit, err := parseBoundedInt(strings.TrimSpace(q.Get("limit")), "limit", defaultSyncJobsLimit, 1, maxSyncJobsLimit)
	if err != nil {
		writeError(w, err)
		return
	}
	offset, err := parseBoundedInt(strings.TrimSpace(q.Get("offset")), "offset", 0, 0, -1)
	if err != nil {
		writeError(w, err)
		return
	}
	filter := syncjob.JobFilter{
		Status:  strings.TrimSpace(q.Get("status")),
		JobType: strings.TrimSpace(q.Get("job_type")),
		Limit:   limit,
		Offset:  offset,
	}
	jobs, err := s.sync.ListJobs(r.Context(), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	if jobs == nil {
		jobs = []syncjob.Job{}
	}
	writeJSON(w, http.StatusOK, syncJobsListResponse{Jobs: jobs, Limit: limit, Offset: offset})
}

// handleGetSyncJob 取单个作业。
func (s *Server) handleGetSyncJob(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	job, err := s.sync.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleCancelSyncJob 取消 pending 作业。
func (s *Server) handleCancelSyncJob(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	id := r.PathValue("id")
	if err := s.sync.CancelJob(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	job, err := s.sync.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleRetrySyncJob 把 failed/cancelled 作业重新排队。
func (s *Server) handleRetrySyncJob(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	id := r.PathValue("id")
	if err := s.sync.RetryJob(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	job, err := s.sync.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleCreateSchedule 建排程。
func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	var in syncjob.ScheduleInput
	if !decodeScheduleInput(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, err)
		return
	}
	in.CreatedBy = actorOf(r)
	sched, err := s.sync.CreateSchedule(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sched)
}

// handleListSchedules 列出全部排程。
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	schedules, err := s.sync.ListSchedules(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": schedules})
}

// handleUpdateSchedule 全量覆盖排程。
func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	var in syncjob.ScheduleInput
	if !decodeScheduleInput(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, err)
		return
	}
	sched, err := s.sync.UpdateSchedule(r.Context(), id, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

// handleDeleteSchedule 删除排程。
func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	if err := s.sync.DeleteSchedule(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleToggleSchedule 翻转启用状态。
func (s *Server) handleToggleSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	sched, err := s.sync.ToggleSchedule(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

// handleRunScheduleNow 立即按排程建一个作业，并把 next_run_at 推到下一周期。
func (s *Server) handleRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	sched, err := s.sync.GetSchedule(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	job, err := syncjob.NewJob(sched.JobType, sched.Params, defaultJobMaxRetries)
	if err != nil {
		writeError(w, err)
		return
	}
	next, err := syncjob.NextRun(sched.Cron, time.Now())
	if err != nil {
		writeError(w, fmt.Errorf("%w: stored cron_expression %q: %v", syncjob.ErrInvalidParams, sched.Cron, err))
		return
	}
	if err := s.sync.EnqueueFromSchedule(r.Context(), sched.ID, job, next); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "next_run_at": next})
}

// defaultJobMaxRetries 与 `ingest.sync_jobs.max_retries` 的 DDL 默认值一致。
const defaultJobMaxRetries = 3

// 列表分页口径（与 `/api/evidence` 对齐）：limit 缺省 50、上限 200；offset 缺省 0。
// 越界一律 400，不静默截断——静默回退会把「翻页翻丢了」伪装成正常结果。
const (
	defaultSyncJobsLimit = 50
	maxSyncJobsLimit     = 200
)

// decodeScheduleInput 解码排程请求体；失败时已写 400。
func decodeScheduleInput(w http.ResponseWriter, r *http.Request, in *syncjob.ScheduleInput) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSyncBodyBytes))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(in); err != nil {
		writeError(w, fmt.Errorf("%w: body is not a valid schedule: %v", syncjob.ErrInvalidParams, err))
		return false
	}
	return true
}

// scheduleID 解析路径里的排程 ID；失败时已写 400。
func scheduleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, fmt.Errorf("%w: schedule id %q is not a positive integer", syncjob.ErrInvalidParams, raw))
		return 0, false
	}
	return id, true
}

// actorOf 返回请求的调用方标识（写进 created_by）。鉴权中间件把主体放进了
// context；未接鉴权时返回空串，由 store 兜底成 "system"。
func actorOf(r *http.Request) string {
	if r == nil {
		return ""
	}
	return subjectFromContext(r.Context())
}
