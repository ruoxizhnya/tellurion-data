package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tellurion-data/internal/sync"
)

// fakeSyncStore 只实现测试用到的行为，其余返回零值。
type fakeSyncStore struct {
	jobs      map[string]syncjob.Job
	schedules map[int64]syncjob.Schedule
	createErr error
	cancelErr error
	retryErr  error
	updateErr error
	toggleErr error
	runErr    error
	enqueued  []string
}

func newFakeSyncStore() *fakeSyncStore {
	return &fakeSyncStore{
		jobs:      map[string]syncjob.Job{},
		schedules: map[int64]syncjob.Schedule{},
	}
}

func (f *fakeSyncStore) CreateJob(_ context.Context, job syncjob.Job) error {
	if f.createErr != nil {
		return f.createErr
	}
	job.CreatedAt = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	f.jobs[job.ID] = job
	return nil
}

func (f *fakeSyncStore) GetJob(_ context.Context, id string) (syncjob.Job, error) {
	job, ok := f.jobs[id]
	if !ok {
		return syncjob.Job{}, syncjob.ErrJobNotFound
	}
	return job, nil
}

func (f *fakeSyncStore) ListJobs(_ context.Context, _ syncjob.JobFilter) ([]syncjob.Job, error) {
	out := make([]syncjob.Job, 0, len(f.jobs))
	for _, job := range f.jobs {
		out = append(out, job)
	}
	return out, nil
}

func (f *fakeSyncStore) ClaimJob(context.Context, string) (*syncjob.Job, error) { return nil, nil }

func (f *fakeSyncStore) FinishJob(context.Context, string, string, string, map[string]any, string) error {
	return nil
}

func (f *fakeSyncStore) CancelJob(_ context.Context, id string) error {
	if f.cancelErr != nil {
		return f.cancelErr
	}
	job, ok := f.jobs[id]
	if !ok {
		return syncjob.ErrJobNotFound
	}
	job.Status = syncjob.StatusCancelled
	f.jobs[id] = job
	return nil
}

func (f *fakeSyncStore) RetryJob(_ context.Context, id string) error {
	if f.retryErr != nil {
		return f.retryErr
	}
	job, ok := f.jobs[id]
	if !ok {
		return syncjob.ErrJobNotFound
	}
	job.Status = syncjob.StatusPending
	f.jobs[id] = job
	return nil
}

func (f *fakeSyncStore) CreateSchedule(_ context.Context, in syncjob.ScheduleInput) (syncjob.Schedule, error) {
	sched := syncjob.Schedule{ID: 1, Name: in.Name, JobType: in.JobType, Cron: in.Cron, Params: in.Params, IsActive: in.IsActive}
	f.schedules[1] = sched
	return sched, nil
}

func (f *fakeSyncStore) UpdateSchedule(_ context.Context, id int64, in syncjob.ScheduleInput) (syncjob.Schedule, error) {
	if f.updateErr != nil {
		return syncjob.Schedule{}, f.updateErr
	}
	if _, ok := f.schedules[id]; !ok {
		return syncjob.Schedule{}, syncjob.ErrScheduleNotFound
	}
	sched := syncjob.Schedule{ID: id, Name: in.Name, JobType: in.JobType, Cron: in.Cron, Params: in.Params, IsActive: in.IsActive}
	f.schedules[id] = sched
	return sched, nil
}

func (f *fakeSyncStore) DeleteSchedule(_ context.Context, id int64) error {
	if _, ok := f.schedules[id]; !ok {
		return syncjob.ErrScheduleNotFound
	}
	delete(f.schedules, id)
	return nil
}

func (f *fakeSyncStore) ToggleSchedule(_ context.Context, id int64) (syncjob.Schedule, error) {
	if f.toggleErr != nil {
		return syncjob.Schedule{}, f.toggleErr
	}
	sched, ok := f.schedules[id]
	if !ok {
		return syncjob.Schedule{}, syncjob.ErrScheduleNotFound
	}
	sched.IsActive = !sched.IsActive
	f.schedules[id] = sched
	return sched, nil
}

func (f *fakeSyncStore) GetSchedule(_ context.Context, id int64) (syncjob.Schedule, error) {
	sched, ok := f.schedules[id]
	if !ok {
		return syncjob.Schedule{}, syncjob.ErrScheduleNotFound
	}
	return sched, nil
}

func (f *fakeSyncStore) ListSchedules(context.Context) ([]syncjob.Schedule, error) {
	out := make([]syncjob.Schedule, 0, len(f.schedules))
	for _, sched := range f.schedules {
		out = append(out, sched)
	}
	return out, nil
}

func (f *fakeSyncStore) DueSchedules(context.Context, time.Time) ([]syncjob.Schedule, error) {
	return nil, nil
}

func (f *fakeSyncStore) EnqueueFromSchedule(_ context.Context, _ int64, job syncjob.Job, _ time.Time) error {
	if f.runErr != nil {
		return f.runErr
	}
	f.enqueued = append(f.enqueued, job.ID)
	return nil
}

// syncServer 起一个只装了作业队列的 server（显式 allow-all 替身，避免鉴权在
// handler 之前 503；无 secret 的真实语义见 auth_test.go 的第 2 态用例）。
func syncServer(store syncjob.Store) http.Handler {
	return New(nil,
		WithAuthorizer(allowAllAuthorizer{}),
		WithSync(store),
	).Routes()
}

// unwiredSyncServer 是「队列未接线」的 server（不传 WithSync）。
func unwiredSyncServer() http.Handler {
	return New(nil,
		WithAuthorizer(allowAllAuthorizer{}),
	).Routes()
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateSyncJob_AcceptsAndReturnsJob(t *testing.T) {
	store := newFakeSyncStore()
	rec := doJSON(t, syncServer(store), http.MethodPost, "/api/sync/jobs",
		`{"job_type":"ohlcv_daily_qfq","params":{"symbols":["600519.SH"],"start_date":"2026-01-05","end_date":"2026-01-06"}}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var job syncjob.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if job.ID == "" || job.Status != syncjob.StatusPending || job.JobType != "ohlcv_daily_qfq" {
		t.Fatalf("job = %+v", job)
	}
	if len(store.jobs) != 1 {
		t.Fatalf("stored jobs = %d", len(store.jobs))
	}
}

func TestCreateSyncJob_RejectsBadParams(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodPost, "/api/sync/jobs",
		`{"job_type":"ohlcv_daily_qfq","params":{"start_date":"05/01/2026"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "invalid_parameter")
}

func TestCreateSyncJob_RejectsUnknownFields(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodPost, "/api/sync/jobs",
		`{"job_type":"ohlcv_daily_qfq","bogus":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestGetSyncJob_NotFoundIs404(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodGet, "/api/sync/jobs/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "job_not_found")
}

func TestCancelSyncJob_ConflictIs409(t *testing.T) {
	store := newFakeSyncStore()
	store.jobs["j1"] = syncjob.Job{ID: "j1", Status: syncjob.StatusRunning}
	store.cancelErr = syncjob.ErrJobNotCancellable

	rec := doJSON(t, syncServer(store), http.MethodPost, "/api/sync/jobs/j1/cancel", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "job_not_cancellable")
}

func TestListSchedules_EmptyIsEmptyArray(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodGet, "/api/sync/schedules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Schedules []syncjob.Schedule `json:"schedules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if body.Schedules == nil {
		t.Fatalf("schedules = null, want empty array (body %s)", rec.Body.String())
	}
	if len(body.Schedules) != 0 {
		t.Fatalf("schedules = %v, want empty", body.Schedules)
	}
}

// TestSyncWorkers_FailsClosedWhenRuntimeUnwired 钉住自省面的未接线形态：
// 队列接了但运行态没注入（例如有人只装了 Store）⇒ 503，不是回半张表。
func TestSyncWorkers_FailsClosedWhenRuntimeUnwired(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodGet, "/api/sync/workers", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "dependency_unavailable")
}

// TestSyncWorkers_ReturnsRuntimeSnapshot 钉住 workers 读面的形状。
func TestSyncWorkers_ReturnsRuntimeSnapshot(t *testing.T) {
	h := New(nil,
		WithAuthorizer(allowAllAuthorizer{}),
		WithSync(newFakeSyncStore()),
		WithSyncRuntime(SyncRuntime{
			WorkerIDs:       func() []string { return []string{"data-1"} },
			RegisteredTypes: func() []string { return []string{"ohlcv_daily", "stocks"} },
			IsRunning:       func() bool { return true },
		}),
	).Routes()

	rec := doJSON(t, h, http.MethodGet, "/api/sync/workers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var body workerStatsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if body.NumWorkers != 1 || len(body.WorkerIDs) != 1 || body.WorkerIDs[0] != "data-1" {
		t.Fatalf("workers = %+v", body)
	}
	if len(body.RegisteredTypes) != 2 || !body.IsRunning {
		t.Fatalf("workers = %+v", body)
	}
}

// TestSchedulerStats_CountsActiveWithNextRun 钉住 stats 口径：
// total_entries = 已激活且已算出 next_run_at 的排程数。
func TestSchedulerStats_CountsActiveWithNextRun(t *testing.T) {
	store := newFakeSyncStore()
	next := time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)
	store.schedules[1] = syncjob.Schedule{ID: 1, Name: "daily", IsActive: true, NextRunAt: &next}
	store.schedules[2] = syncjob.Schedule{ID: 2, Name: "paused", IsActive: false, NextRunAt: &next}

	rec := doJSON(t, syncServer(store), http.MethodGet, "/api/sync/scheduler/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var body schedulerStatsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if body.ActiveSchedules != 1 || body.TotalEntries != 1 {
		t.Fatalf("stats = %+v", body)
	}
	if len(body.NextRuns) != 1 || body.NextRuns[0].ScheduleID != 1 {
		t.Fatalf("next_runs = %+v", body.NextRuns)
	}
}

// TestSyncJobProgress_ReplaysTerminalAndCloses 钉住 SSE：连上重放一条，
// 终态推完即关（不挂住测试）。
func TestSyncJobProgress_ReplaysTerminalAndCloses(t *testing.T) {
	store := newFakeSyncStore()
	store.jobs["j1"] = syncjob.Job{ID: "j1", Status: syncjob.StatusSucceeded}

	rec := doJSON(t, syncServer(store), http.MethodGet, "/api/sync/jobs/j1/progress", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"id":"j1"`) {
		t.Fatalf("body = %q, want a data: line with the job id", rec.Body.String())
	}
}

// TestSyncJobProgress_UnknownJobIs404：作业不存在时必须在 SSE 头之前就 404，
// 否则 EventSource 拿到的是「200 空流」，前端会永远等下去。
func TestSyncJobProgress_UnknownJobIs404(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodGet, "/api/sync/jobs/nope/progress", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "job_not_found")
}

func TestCreateSchedule_ValidatesCron(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodPost, "/api/sync/schedules",
		`{"name":"bad","job_type":"ohlcv_daily_qfq","cron_expression":"*/0 * * * *","is_active":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "invalid_parameter")
}

func TestCreateSchedule_AcceptsFiveFieldCron(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodPost, "/api/sync/schedules",
		`{"name":"daily","job_type":"ohlcv_daily_qfq","cron_expression":"0 18 * * 1-5","is_active":true,"params":{"symbols":["600519.SH"]}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdateSchedule_MissingIs404(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodPut, "/api/sync/schedules/9",
		`{"name":"x","job_type":"ohlcv_daily_qfq","cron_expression":"0 18 * * *","is_active":true}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "schedule_not_found")
}

func TestScheduleID_MustBePositiveInteger(t *testing.T) {
	rec := doJSON(t, syncServer(newFakeSyncStore()), http.MethodPost, "/api/sync/schedules/abc/toggle", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestRunScheduleNow_EnqueuesJob(t *testing.T) {
	store := newFakeSyncStore()
	store.schedules[3] = syncjob.Schedule{
		ID: 3, Name: "daily", JobType: "ohlcv_daily_qfq", Cron: "0 18 * * 1-5",
		Params: map[string]any{"symbols": []any{"600519.SH"}},
	}
	rec := doJSON(t, syncServer(store), http.MethodPost, "/api/sync/schedules/3/run", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if len(store.enqueued) != 1 {
		t.Fatalf("enqueued = %v, want 1 job", store.enqueued)
	}
}

func TestSyncEndpoints_UnwiredIs503(t *testing.T) {
	rec := doJSON(t, unwiredSyncServer(), http.MethodGet, "/api/sync/jobs", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "dependency_unavailable")
}

// assertErrorCode 断言错误体里的 code（线格式是 {"error":{"code":...}}）。
func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body %s)", err, rec.Body.String())
	}
	if body.Error.Code != want {
		t.Fatalf("code = %q, want %q (body %s)", body.Error.Code, want, rec.Body.String())
	}
}
