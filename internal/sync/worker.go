package syncjob

// 执行循环与排程循环。两者都是**长驻 goroutine**，由 `cmd/dataservice` 在
// 装配成功后启动，进程退出时随 ctx 一起结束。

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// defaultMaxRetries 与 `ingest.sync_jobs.max_retries` 的 DDL 默认值一致。
const defaultMaxRetries = 3

// 默认轮询节奏。作业队列是秒级的东西；排程是分钟级的。
const (
	DefaultPollInterval      = 2 * time.Second
	DefaultSchedulerInterval = 30 * time.Second
)

// releaseTimeout 是「停机/租约丢失后交还作业」这一笔写的预算。用独立
// ctx：停机时父 ctx 已取消，不能拿它去写库。
const releaseTimeout = 5 * time.Second

// Worker 从队列领取作业并执行。
type Worker struct {
	Store        WorkerStore
	Runner       *Runner
	ID           string
	PollInterval time.Duration
	// HeartbeatInterval 是续租间隔；零值用 DefaultHeartbeatInterval。
	// 测试可调小，生产口径写死（见 store.go 的租约常量）。
	HeartbeatInterval time.Duration
	Logger            *log.Logger
}

// Run 阻塞执行「领取-执行-记账」循环，直到 ctx 结束。
func (w *Worker) Run(ctx context.Context) {
	interval := w.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	logger := w.logger()
	logger.Printf("syncjob: worker %s started (poll=%s)", w.ID, interval)

	for {
		select {
		case <-ctx.Done():
			logger.Printf("syncjob: worker %s stopped", w.ID)
			return
		default:
		}

		job, err := w.Store.ClaimJob(ctx, w.ID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Printf("syncjob: worker %s claim failed: %v", w.ID, err)
			if !sleepCtx(ctx, interval) {
				return
			}
			continue
		}
		if job == nil {
			if !sleepCtx(ctx, interval) {
				return
			}
			continue
		}
		w.runOne(ctx, *job)
	}
}

// runOne 执行一个已领取的作业并写终态。
//
// 三条出口：
//   - 租约丢失（心跳报 ErrLeaseLost）⇒ 中止执行且**不写终态**：行已经归
//     别人，写了就是两份终态；
//   - 父 ctx 取消（停机）⇒ `ReleaseJob` 交还：预算未用尽回 pending 供重领，
//     用尽判 failed（不静默丢）；
//   - 正常结束 ⇒ `FinishJob`（带持有权校验）。
//
// 写终态失败只记日志：作业行会停在 running，运维能看见「卡住的作业」；
// 悄悄把它改成 failed 反而会掩盖「数据库写不进终态」这个更大的问题。
func (w *Worker) runOne(ctx context.Context, job Job) {
	logger := w.logger()
	logger.Printf("syncjob: worker %s running %s", w.ID, describeJob(job))

	jobCtx, cancelJob := context.WithCancel(ctx)
	var lost atomic.Bool
	watchCtx, stopWatch := context.WithCancel(context.Background())
	var watchWG sync.WaitGroup
	if interval := w.heartbeatInterval(); interval > 0 {
		watchWG.Add(1)
		go func() {
			defer watchWG.Done()
			w.heartbeat(watchCtx, job.ID, interval, &lost, cancelJob)
		}()
	}

	started := time.Now()
	result, err := w.Runner.Execute(jobCtx, job)
	elapsed := time.Since(started)
	stopWatch()
	watchWG.Wait()
	cancelJob()

	if lost.Load() {
		logger.Printf("syncjob: worker %s lost the lease on %s; not writing a terminal state (another worker owns it now)",
			w.ID, describeJob(job))
		return
	}

	if ctx.Err() != nil {
		releaseCtx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		defer cancel()
		if relErr := w.Store.ReleaseJob(releaseCtx, job.ID, w.ID, "syncjob: interrupted by worker shutdown"); relErr != nil {
			logger.Printf("syncjob: worker %s could not release job %s on shutdown: %v", w.ID, job.ID, relErr)
			return
		}
		logger.Printf("syncjob: worker %s released %s on shutdown (%s); it will be re-claimed or failed if the retry budget is exhausted",
			w.ID, job.ID, elapsed)
		return
	}

	if err != nil {
		logger.Printf("syncjob: worker %s failed %s after %s: %v", w.ID, describeJob(job), elapsed, err)
		if finishErr := w.Store.FinishJob(ctx, job.ID, w.ID, StatusFailed, nil, errorText(err)); finishErr != nil {
			logger.Printf("syncjob: worker %s could not record failure for job %s: %v", w.ID, job.ID, finishErr)
		}
		return
	}

	logger.Printf("syncjob: worker %s succeeded %s: fetched=%d persisted=%d skipped=%d in %s",
		w.ID, describeJob(job), result.Fetched, result.Persisted, result.Skipped, elapsed)
	if finishErr := w.Store.FinishJob(ctx, job.ID, w.ID, StatusSucceeded, ResultMap(result, elapsed), ""); finishErr != nil {
		logger.Printf("syncjob: worker %s could not record success for job %s: %v", w.ID, job.ID, finishErr)
	}
}

// heartbeat 周期续租。租约丢失时置 lost 并取消执行（继续跑只会白干，
// 结果也写不进去）；其余错误视为瞬时故障，下一拍重试。
//
// 用独立于 jobCtx 的 ctx：停机时 jobCtx 先取消，心跳应当随 watchCtx 停止，
// 而不是把「停机」误报成心跳失败。
func (w *Worker) heartbeat(ctx context.Context, jobID string, interval time.Duration, lost *atomic.Bool, cancelJob context.CancelFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hctx, cancel := context.WithTimeout(ctx, interval)
			err := w.Store.HeartbeatJob(hctx, jobID, w.ID)
			cancel()
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrLeaseLost) {
				lost.Store(true)
				w.logger().Printf("syncjob: worker %s lost the lease on job %s: %v; aborting execution", w.ID, jobID, err)
				cancelJob()
				return
			}
			w.logger().Printf("syncjob: worker %s heartbeat for job %s failed (will retry): %v", w.ID, jobID, err)
		}
	}
}

func (w *Worker) heartbeatInterval() time.Duration {
	if w.HeartbeatInterval > 0 {
		return w.HeartbeatInterval
	}
	return DefaultHeartbeatInterval
}

func (w *Worker) logger() *log.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return log.Default()
}

// Scheduler 扫描到期排程并建作业。
type Scheduler struct {
	Store    ScheduleStore
	Interval time.Duration
	Clock    func() time.Time
	Logger   *log.Logger
}

// Run 阻塞执行排程扫描，直到 ctx 结束。
//
// 启动先 tick 一次：进程重启后，错过的排程（next_run_at 已过期）应当立刻
// 补建作业，而不是再等一个扫描周期。
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultSchedulerInterval
	}
	logger := s.logger()
	logger.Printf("syncjob: scheduler started (interval=%s)", interval)

	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			logger.Printf("syncjob: scheduler tick failed: %v", err)
		}
		if !sleepCtx(ctx, interval) {
			logger.Printf("syncjob: scheduler stopped")
			return
		}
	}
}

// Tick 处理一轮到期排程（导出以便测试）。
//
// 单个排程失败不中断整轮：一个坏 cron 进不了库（创建时已校验），
// 但「作业 ID 撞车」这类瞬时失败不该让其余排程一起停摆。
func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.now()
	due, err := s.Store.DueSchedules(ctx, now)
	if err != nil {
		return err
	}
	for _, sched := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		job, err := NewJob(sched.JobType, sched.Params, defaultMaxRetries)
		if err != nil {
			s.logger().Printf("syncjob: schedule %d (%s) has unusable params: %v", sched.ID, sched.Name, err)
			continue
		}
		next, err := NextRun(sched.Cron, now)
		if err != nil {
			s.logger().Printf("syncjob: schedule %d (%s) has unusable cron: %v", sched.ID, sched.Name, err)
			continue
		}
		if err := s.Store.EnqueueFromSchedule(ctx, sched.ID, job, next); err != nil {
			s.logger().Printf("syncjob: schedule %d (%s) enqueue failed: %v", sched.ID, sched.Name, err)
			continue
		}
		s.logger().Printf("syncjob: schedule %d (%s) enqueued %s (next run %s)",
			sched.ID, sched.Name, job.ID, next.Format(time.RFC3339))
	}
	return nil
}

func (s *Scheduler) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Scheduler) logger() *log.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return log.Default()
}

// sleepCtx 睡眠 d；ctx 结束时返回 false（调用方应当退出循环）。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
