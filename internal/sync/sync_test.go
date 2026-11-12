package syncjob

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/schema"
)

// ── 假实现 ──

type fakeExecutor struct {
	mu      sync.Mutex
	calls   int
	req     ingest.FetchRequest
	norm    ingest.Normalizer
	result  ingest.ProcessResult
	err     error
	seenJob string
}

func (f *fakeExecutor) Process(ctx context.Context, req ingest.FetchRequest, normalizer ingest.Normalizer) (ingest.ProcessResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.req = req
	f.norm = normalizer
	return f.result, f.err
}

type finishedJob struct {
	workerID string
	status   string
	result   map[string]any
	errText  string
}

type releasedJob struct {
	id       string
	workerID string
	reason   string
}

type fakeJobStore struct {
	mu       sync.Mutex
	created  []Job
	claim    *Job
	finish   map[string]finishedJob
	cancels  []string
	retries  []string
	getErr   error
	listCall int

	// 租约面（DATA-GRACEFUL-SHUTDOWN）
	heartbeats   int
	heartbeatErr error
	released     []releasedJob
	releaseErr   error
	reapCount    int
	reapErr      error
	reapedAt     []time.Time
}

func newFakeJobStore() *fakeJobStore {
	return &fakeJobStore{finish: map[string]finishedJob{}}
}

func (f *fakeJobStore) CreateJob(_ context.Context, job Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, job)
	return nil
}

func (f *fakeJobStore) GetJob(_ context.Context, id string) (Job, error) {
	if f.getErr != nil {
		return Job{}, f.getErr
	}
	return Job{ID: id, Status: StatusPending}, nil
}

func (f *fakeJobStore) ListJobs(_ context.Context, _ JobFilter) ([]Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCall++
	return nil, nil
}

func (f *fakeJobStore) ClaimJob(_ context.Context, _ string) (*Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job := f.claim
	f.claim = nil
	return job, nil
}

func (f *fakeJobStore) FinishJob(_ context.Context, id, workerID, status string, result map[string]any, errMessage string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finish[id] = finishedJob{workerID: workerID, status: status, result: result, errText: errMessage}
	return nil
}

func (f *fakeJobStore) HeartbeatJob(_ context.Context, _, workerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats++
	return f.heartbeatErr
}

func (f *fakeJobStore) ReleaseJob(_ context.Context, id, workerID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, releasedJob{id: id, workerID: workerID, reason: reason})
	return f.releaseErr
}

func (f *fakeJobStore) ReapExpired(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reapedAt = append(f.reapedAt, now)
	return f.reapCount, f.reapErr
}

func (f *fakeJobStore) CancelJob(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, id)
	return nil
}

func (f *fakeJobStore) RetryJob(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retries = append(f.retries, id)
	return nil
}

type enqueuedSchedule struct {
	id   int64
	job  Job
	next time.Time
}

type fakeScheduleStore struct {
	mu       sync.Mutex
	due      []Schedule
	enqueued []enqueuedSchedule
	dueErr   error
}

func (f *fakeScheduleStore) CreateSchedule(context.Context, ScheduleInput) (Schedule, error) {
	return Schedule{}, nil
}
func (f *fakeScheduleStore) UpdateSchedule(context.Context, int64, ScheduleInput) (Schedule, error) {
	return Schedule{}, nil
}
func (f *fakeScheduleStore) DeleteSchedule(context.Context, int64) error { return nil }
func (f *fakeScheduleStore) ToggleSchedule(context.Context, int64) (Schedule, error) {
	return Schedule{}, nil
}
func (f *fakeScheduleStore) GetSchedule(context.Context, int64) (Schedule, error) {
	return Schedule{}, nil
}
func (f *fakeScheduleStore) ListSchedules(context.Context) ([]Schedule, error) { return nil, nil }
func (f *fakeScheduleStore) DueSchedules(context.Context, time.Time) ([]Schedule, error) {
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	return f.due, nil
}
func (f *fakeScheduleStore) EnqueueFromSchedule(_ context.Context, id int64, job Job, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, enqueuedSchedule{id: id, job: job, next: next})
	return nil
}

// ── 参数解析 ──

func TestFetchRequestFromParams_ParsesBothDateLayouts(t *testing.T) {
	req, err := FetchRequestFromParams("ohlcv_daily_qfq", map[string]any{
		"symbols":    []any{"600519.SH", "000001.SZ", ""},
		"start_date": "2026-01-05",
		"end_date":   "20260106",
		"period":     "1d",
		"extra":      map[string]any{"adjust": "qfq"},
	})
	if err != nil {
		t.Fatalf("FetchRequestFromParams: %v", err)
	}
	if req.DataType != "ohlcv_daily_qfq" {
		t.Fatalf("data type = %q", req.DataType)
	}
	if len(req.Symbols) != 2 || req.Symbols[0] != "600519.SH" {
		t.Fatalf("symbols = %v", req.Symbols)
	}
	if got := req.StartDate.Format("20060102"); got != "20260105" {
		t.Fatalf("start = %s", got)
	}
	if got := req.EndDate.Format("20060102"); got != "20260106" {
		t.Fatalf("end = %s", got)
	}
	if req.Period != "1d" || req.Extra["adjust"] != "qfq" {
		t.Fatalf("period/extra = %q/%v", req.Period, req.Extra)
	}
}

func TestFetchRequestFromParams_RejectsMismatchedDataType(t *testing.T) {
	_, err := FetchRequestFromParams("ohlcv_daily_qfq", map[string]any{"data_type": "capital_flow"})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v, want ErrInvalidParams", err)
	}
}

func TestFetchRequestFromParams_RejectsBadDateAndInvertedWindow(t *testing.T) {
	if _, err := FetchRequestFromParams("x", map[string]any{"start_date": "05/01/2026"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("bad date err = %v", err)
	}
	if _, err := FetchRequestFromParams("x", map[string]any{
		"start_date": "2026-02-01", "end_date": "2026-01-01",
	}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("inverted window err = %v", err)
	}
}

// ── 执行 ──

func TestRunner_UsesPassthroughNormalizer(t *testing.T) {
	exec := &fakeExecutor{result: ingest.ProcessResult{
		DataType: "ohlcv_daily_qfq", Source: "tushare", Fetched: 3, Persisted: 3,
	}}
	runner := NewRunner(exec)

	job := Job{ID: "j1", JobType: "ohlcv_daily_qfq", Params: map[string]any{"symbols": []any{"600519.SH"}}}
	if _, err := runner.Execute(context.Background(), job); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exec.req.DataType != "ohlcv_daily_qfq" || len(exec.req.Symbols) != 1 {
		t.Fatalf("request = %+v", exec.req)
	}

	point := exec.norm(ingest.DataItem{
		Symbol:    "600519.SH",
		TradeTime: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		Data:      map[string]any{"close": 10.5},
	}, "tushare", "ohlcv_daily_qfq")
	if point.Symbol != "600519.SH" || point.Source != "tushare" || point.DataType != "ohlcv_daily_qfq" {
		t.Fatalf("point = %+v", point)
	}
	if point.Data["close"] != 10.5 {
		t.Fatalf("data = %v", point.Data)
	}
	// 透传归一不填服务器独占字段。
	if !point.IngestTime.IsZero() || !point.VisibleAt.IsZero() {
		t.Fatalf("passthrough must not fill ingest_time/visible_at: %+v", point)
	}
}

func TestRunner_NilExecutorIsMisconfigured(t *testing.T) {
	_, err := NewRunner(nil).Execute(context.Background(), Job{JobType: "x"})
	if !errors.Is(err, ErrMisconfigured) {
		t.Fatalf("err = %v, want ErrMisconfigured", err)
	}
}

func TestWorker_RecordsSuccess(t *testing.T) {
	store := newFakeJobStore()
	exec := &fakeExecutor{result: ingest.ProcessResult{
		DataType: "ohlcv_daily_qfq", Source: "tushare", Fetched: 5, Persisted: 4, Skipped: 1,
	}}
	worker := &Worker{Store: store, Runner: NewRunner(exec), ID: "w1"}

	job := Job{ID: "job-1", JobType: "ohlcv_daily_qfq", Status: StatusRunning}
	worker.runOne(context.Background(), job)

	got, ok := store.finish["job-1"]
	if !ok {
		t.Fatalf("job was not finished: %+v", store.finish)
	}
	if got.status != StatusSucceeded {
		t.Fatalf("status = %q, want succeeded (err=%q)", got.status, got.errText)
	}
	if got.result["fetched"] != 5 || got.result["persisted"] != 4 || got.result["skipped"] != 1 {
		t.Fatalf("result = %v", got.result)
	}
}

func TestWorker_RecordsFailure(t *testing.T) {
	store := newFakeJobStore()
	exec := &fakeExecutor{err: errors.New("upstream exploded")}
	worker := &Worker{Store: store, Runner: NewRunner(exec), ID: "w1"}

	worker.runOne(context.Background(), Job{ID: "job-2", JobType: "ohlcv_daily_qfq"})

	got := store.finish["job-2"]
	if got.status != StatusFailed {
		t.Fatalf("status = %q, want failed", got.status)
	}
	if !strings.Contains(got.errText, "upstream exploded") {
		t.Fatalf("error text = %q", got.errText)
	}
	if got.result != nil {
		t.Fatalf("failed job must not carry a result, got %v", got.result)
	}
}

func TestWorker_SkipsWhenQueueEmpty(t *testing.T) {
	store := newFakeJobStore()
	worker := &Worker{Store: store, Runner: NewRunner(&fakeExecutor{}), ID: "w1", PollInterval: time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	worker.Run(ctx) // 队列空 ⇒ 只轮询，不执行任何作业
	if len(store.finish) != 0 {
		t.Fatalf("finished jobs = %v, want none", store.finish)
	}
}

// ── 租约与停机交还（DATA-GRACEFUL-SHUTDOWN） ──

// sleepyExecutor 睡一小段再成功（用于观察执行期间的心跳）。
type sleepyExecutor struct{ d time.Duration }

func (s sleepyExecutor) Process(context.Context, ingest.FetchRequest, ingest.Normalizer) (ingest.ProcessResult, error) {
	time.Sleep(s.d)
	return ingest.ProcessResult{DataType: "ohlcv_daily_qfq", Source: "tushare", Fetched: 1, Persisted: 1}, nil
}

// blockingExecutor 一直执行到 ctx 结束（模拟长作业）。
type blockingExecutor struct{ ran chan struct{} }

func (b *blockingExecutor) Process(ctx context.Context, _ ingest.FetchRequest, _ ingest.Normalizer) (ingest.ProcessResult, error) {
	select {
	case <-b.ran:
	default:
		close(b.ran)
	}
	<-ctx.Done()
	return ingest.ProcessResult{}, ctx.Err()
}

func TestWorker_HeartbeatsWhileRunning(t *testing.T) {
	store := newFakeJobStore()
	worker := &Worker{
		Store:             store,
		Runner:            NewRunner(sleepyExecutor{d: 60 * time.Millisecond}),
		ID:                "w1",
		HeartbeatInterval: 5 * time.Millisecond,
	}
	worker.runOne(context.Background(), Job{ID: "job-hb", JobType: "ohlcv_daily_qfq"})

	if store.heartbeats < 2 {
		t.Fatalf("heartbeats = %d, want >= 2 while the job runs", store.heartbeats)
	}
	got, ok := store.finish["job-hb"]
	if !ok || got.status != StatusSucceeded || got.workerID != "w1" {
		t.Fatalf("finish = %+v (ok=%t)", got, ok)
	}
}

// 租约丢失 ⇒ 立即中止执行，且**不写终态**、不交还：行已经归别人。
func TestWorker_LeaseLostAbortsWithoutTerminalState(t *testing.T) {
	store := newFakeJobStore()
	store.heartbeatErr = ErrLeaseLost
	exec := &blockingExecutor{ran: make(chan struct{})}
	worker := &Worker{
		Store:             store,
		Runner:            NewRunner(exec),
		ID:                "w1",
		HeartbeatInterval: 5 * time.Millisecond,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.runOne(context.Background(), Job{ID: "job-lost", JobType: "ohlcv_daily_qfq"})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not abort after losing the lease")
	}

	if _, ok := store.finish["job-lost"]; ok {
		t.Fatalf("lease-lost worker must not write a terminal state: %+v", store.finish["job-lost"])
	}
	if len(store.released) != 0 {
		t.Fatalf("lease-lost worker must not release a job it no longer owns: %+v", store.released)
	}
}

// 停机取消（父 ctx）⇒ 交还作业而不是留在 running / 伪造成终态。
func TestWorker_ReleasesJobOnShutdown(t *testing.T) {
	store := newFakeJobStore()
	exec := &blockingExecutor{ran: make(chan struct{})}
	worker := &Worker{Store: store, Runner: NewRunner(exec), ID: "w1"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); worker.runOne(ctx, Job{ID: "job-stop", JobType: "ohlcv_daily_qfq"}) }()
	<-exec.ran
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after shutdown")
	}

	if len(store.released) != 1 {
		t.Fatalf("released = %+v, want exactly one release", store.released)
	}
	if got := store.released[0]; got.id != "job-stop" || got.workerID != "w1" || !strings.Contains(got.reason, "shutdown") {
		t.Fatalf("release = %+v", got)
	}
	if _, ok := store.finish["job-stop"]; ok {
		t.Fatalf("interrupted job must be released, not finished: %+v", store.finish["job-stop"])
	}
}

// ── 排程 ──

func TestScheduler_EnqueuesDueScheduleAndAdvances(t *testing.T) {
	store := &fakeScheduleStore{due: []Schedule{{
		ID:      7,
		Name:    "daily-ohlcv",
		JobType: "ohlcv_daily_qfq",
		Cron:    "0 18 * * 1-5",
		Params:  map[string]any{"symbols": []any{"600519.SH"}},
	}}}
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	sched := &Scheduler{Store: store, Clock: func() time.Time { return now }}

	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(store.enqueued) != 1 {
		t.Fatalf("enqueued = %d, want 1", len(store.enqueued))
	}
	item := store.enqueued[0]
	if item.id != 7 || item.job.JobType != "ohlcv_daily_qfq" {
		t.Fatalf("enqueued = %+v", item)
	}
	if item.job.ID == "" || item.job.MaxRetries != defaultMaxRetries {
		t.Fatalf("job = %+v", item.job)
	}
	if want := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC); !item.next.Equal(want) {
		t.Fatalf("next = %s, want %s", item.next, want)
	}
}

func TestScheduler_PropagatesQueryError(t *testing.T) {
	store := &fakeScheduleStore{dueErr: errors.New("db down")}
	sched := &Scheduler{Store: store, Clock: time.Now}
	if err := sched.Tick(context.Background()); err == nil {
		t.Fatal("want error when DueSchedules fails")
	}
}

// ── PG 腿（opt-in） ──

// TestPGStoreFinishJobWritesTerminalState 钉住 FinishJob 的真库行为。
//
// 为什么必须有这一腿：`FinishJob` 的 SQL 曾把同一个 `$2` 同时用在
// `status = $2`（varchar）与 `CASE WHEN $2 = 'succeeded'`（text），PG 判定
// 参数类型不一致直接报 42P08。假 store 看不到 SQL，套件全绿，而真作业会
// 永远停在 running（2026-11-10 端到端实测踩到）。
//
// 无 `DATABASE_URL` ⇒ skip（CI 不设，不算失败）；设置了但连不上 ⇒ fail-loud。
func TestPGStoreFinishJobWritesTerminalState(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PG leg")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("DATABASE_URL 已设置但连不上（fail-loud）：%v", err)
	}
	// 幂等 DDL：本腿只依赖 ingest.sync_jobs 的列形状，不碰回填。
	for i, stmt := range schema.Statements() {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("schema statement %d: %v", i+1, err)
		}
	}

	store := NewPGStore(pool, nil)
	job, err := NewJob("ohlcv_daily", map[string]any{"symbols": []any{"600519.SH"}}, 1)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ingest.sync_jobs WHERE id = $1`, job.ID)
	})
	// FinishJob 现在带持有权校验（租约面）：先把行置成「确实有人持有」，
	// 否则终态写不进去。这里直连 SQL 置 running，避免 ClaimJob 误领别的行。
	if _, err := pool.Exec(ctx, `
		UPDATE ingest.sync_jobs
		SET status = 'running', worker_id = 'w-test', started_at = NOW(),
		    lease_until = NOW() + interval '60 seconds'
		WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	// 失败终态：error_message 落库、completed_at 非空、状态不是 running。
	if err := store.FinishJob(ctx, job.ID, "w-test", StatusFailed, nil, "boom"); err != nil {
		t.Fatalf("FinishJob(failed): %v", err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusFailed || got.ErrorMessage != "boom" || got.CompletedAt == nil {
		t.Fatalf("failed 终态 = %+v", got)
	}

	// 成功终态：progress 翻到 100，result 是 JSONB 而不是字符串；
	// 三个计数列按 `jobCounts` 口径落库（UI 队列渲染 processed/total，
	// 不写就永远 0/0）。fetched=3、persisted=2、skipped=1 ⇒ 3 / 3。
	job2, err := NewJob("ohlcv_daily", nil, 1)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	if err := store.CreateJob(ctx, job2); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ingest.sync_jobs WHERE id = $1`, job2.ID)
	})
	if _, err := pool.Exec(ctx, `
		UPDATE ingest.sync_jobs
		SET status = 'running', worker_id = 'w-test', started_at = NOW(),
		    lease_until = NOW() + interval '60 seconds'
		WHERE id = $1`, job2.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := store.FinishJob(ctx, job2.ID, "w-test", StatusSucceeded, map[string]any{"fetched": 3, "persisted": 2, "skipped": 1}, ""); err != nil {
		t.Fatalf("FinishJob(succeeded): %v", err)
	}
	got2, err := store.GetJob(ctx, job2.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got2.Status != StatusSucceeded || got2.ProgressPercent != 100 || got2.Result["fetched"] != float64(3) {
		t.Fatalf("succeeded 终态 = %+v", got2)
	}
	if got2.TotalItems != 3 || got2.ProcessedItems != 3 || got2.FailedItems != 0 {
		t.Fatalf("计数列 = total:%d processed:%d failed:%d，want 3/3/0",
			got2.TotalItems, got2.ProcessedItems, got2.FailedItems)
	}
}

// ── PG 租约腿（opt-in，独立测试库） ──

// openLeaseTestPG 打开本仓约定的测试库（`DATA_TEST_DSN`，库名必须以 `_test`
// 结尾并自带业务表 DDL）。
//
// 为什么不复用 DATABASE_URL：`ClaimJob` / `ReapExpired` 会命中队列表里的
// **所有**可领/过期行，跑在业务库上会动到真实作业。独立库 + 先清空队列表，
// 让领取结果确定。
func openLeaseTestPG(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("DATA_TEST_DSN"))
	if dsn == "" {
		t.Skip("DATA_TEST_DSN not set; skipping PG lease leg")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATA_TEST_DSN: %v", err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Skipf("DATA_TEST_DSN points at %q (not *_test); refusing to reap a business queue", cfg.ConnConfig.Database)
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("DATA_TEST_DSN 已设置但连不上（fail-loud）：%v", err)
	}
	for i, stmt := range schema.Statements() {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("schema statement %d: %v", i+1, err)
		}
	}
	if _, err := pool.Exec(ctx, `TRUNCATE ingest.sync_jobs`); err != nil {
		t.Fatalf("truncate ingest.sync_jobs: %v", err)
	}
	return ctx, pool
}

// TestPGStoreLeaseClaimHeartbeatReap 钉住租约全生命周期：
// 领取写租约 → 未被回收 → 心跳/终态持有权校验 → 过期回收（回 pending）
// → 预算用尽判 failed。
func TestPGStoreLeaseClaimHeartbeatReap(t *testing.T) {
	ctx, pool := openLeaseTestPG(t)
	store := NewPGStore(pool, nil)

	job, err := NewJob("ohlcv_daily", map[string]any{"symbols": []any{"600519.SH"}}, 2)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	claimed, err := store.ClaimJob(ctx, "w1")
	if err != nil || claimed == nil {
		t.Fatalf("ClaimJob = %+v, %v", claimed, err)
	}
	if claimed.ID != job.ID || claimed.WorkerID != "w1" || claimed.Status != StatusRunning {
		t.Fatalf("claimed = %+v", claimed)
	}

	// 领取必须写租约（破坏验证点：删掉 lease 写入 ⇒ 这里红）。
	var lease time.Time
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(lease_until, 'epoch'::timestamptz) FROM ingest.sync_jobs WHERE id = $1`,
		job.ID).Scan(&lease); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if !lease.After(time.Now()) {
		t.Fatalf("lease_until = %v, want a future lease", lease)
	}

	// 租约未到期 ⇒ 不回收。
	if n, err := store.ReapExpired(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("ReapExpired(now) = %d, %v; want 0", n, err)
	}

	// 心跳与终态都做持有权校验：错 owner 一律 ErrLeaseLost。
	if err := store.HeartbeatJob(ctx, job.ID, "w9"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("HeartbeatJob(w9) = %v, want ErrLeaseLost", err)
	}
	if err := store.HeartbeatJob(ctx, job.ID, "w1"); err != nil {
		t.Fatalf("HeartbeatJob(w1): %v", err)
	}
	if err := store.FinishJob(ctx, job.ID, "w9", StatusSucceeded, nil, ""); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("FinishJob(w9) = %v, want ErrLeaseLost", err)
	}

	// 过期 ⇒ 回 pending，retry_count+1，持有者清空。
	if n, err := store.ReapExpired(ctx, time.Now().Add(2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("ReapExpired(expired) = %d, %v; want 1", n, err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusPending || got.RetryCount != 1 || got.WorkerID != "" || got.CompletedAt != nil {
		t.Fatalf("reaped 行 = %+v", got)
	}

	// 再领一次再过期 ⇒ 预算用尽（2/2）判 failed，错误文案可读。
	claimed2, err := store.ClaimJob(ctx, "w2")
	if err != nil || claimed2 == nil || claimed2.ID != job.ID {
		t.Fatalf("second ClaimJob = %+v, %v", claimed2, err)
	}
	if n, err := store.ReapExpired(ctx, time.Now().Add(2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("ReapExpired(expired #2) = %d, %v; want 1", n, err)
	}
	got, err = store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusFailed || got.RetryCount != 2 || !strings.Contains(got.ErrorMessage, "lease expired") {
		t.Fatalf("预算用尽行 = %+v", got)
	}
	// 被回收后原 worker 不得再写终态。
	if err := store.FinishJob(ctx, job.ID, "w2", StatusSucceeded, nil, ""); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale FinishJob = %v, want ErrLeaseLost", err)
	}
}

// TestPGStoreReleaseJobRequeuesThenFails 钉住停机交还：预算未用尽回 pending，
// 用尽判 failed 且 reason 落库。
func TestPGStoreReleaseJobRequeuesThenFails(t *testing.T) {
	ctx, pool := openLeaseTestPG(t)
	store := NewPGStore(pool, nil)

	job, err := NewJob("ohlcv_daily", nil, 2)
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.ClaimJob(ctx, "w1"); err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	// 非持有者不能交还。
	if err := store.ReleaseJob(ctx, job.ID, "w9", "not mine"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("ReleaseJob(w9) = %v, want ErrLeaseLost", err)
	}
	if err := store.ReleaseJob(ctx, job.ID, "w1", "syncjob: interrupted by worker shutdown"); err != nil {
		t.Fatalf("ReleaseJob(w1): %v", err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusPending || got.RetryCount != 1 || got.WorkerID != "" {
		t.Fatalf("第一次交还 = %+v", got)
	}

	// 重领后再次交还（2/2）⇒ failed，reason 落库（不静默丢）。
	if _, err := store.ClaimJob(ctx, "w2"); err != nil {
		t.Fatalf("second ClaimJob: %v", err)
	}
	if err := store.ReleaseJob(ctx, job.ID, "w2", "syncjob: interrupted by worker shutdown"); err != nil {
		t.Fatalf("ReleaseJob(w2): %v", err)
	}
	got, err = store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusFailed || got.RetryCount != 2 || !strings.Contains(got.ErrorMessage, "worker shutdown") {
		t.Fatalf("预算用尽交还 = %+v", got)
	}
}
