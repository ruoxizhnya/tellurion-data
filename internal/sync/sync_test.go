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
	req     ingest.FetchRequest
	norm    ingest.Normalizer
	result  ingest.ProcessResult
	err     error
	seenJob string
}

func (f *fakeExecutor) Process(ctx context.Context, req ingest.FetchRequest, normalizer ingest.Normalizer) (ingest.ProcessResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.req = req
	f.norm = normalizer
	return f.result, f.err
}

type finishedJob struct {
	status  string
	result  map[string]any
	errText string
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

func (f *fakeJobStore) FinishJob(_ context.Context, id, status string, result map[string]any, errMessage string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finish[id] = finishedJob{status: status, result: result, errText: errMessage}
	return nil
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

	// 失败终态：error_message 落库、completed_at 非空、状态不是 running。
	if err := store.FinishJob(ctx, job.ID, StatusFailed, nil, "boom"); err != nil {
		t.Fatalf("FinishJob(failed): %v", err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusFailed || got.ErrorMessage != "boom" || got.CompletedAt == nil {
		t.Fatalf("failed 终态 = %+v", got)
	}

	// 成功终态：progress 翻到 100，result 是 JSONB 而不是字符串。
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
	if err := store.FinishJob(ctx, job2.ID, StatusSucceeded, map[string]any{"fetched": 1}, ""); err != nil {
		t.Fatalf("FinishJob(succeeded): %v", err)
	}
	got2, err := store.GetJob(ctx, job2.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got2.Status != StatusSucceeded || got2.ProgressPercent != 100 || got2.Result["fetched"] != float64(1) {
		t.Fatalf("succeeded 终态 = %+v", got2)
	}
}
