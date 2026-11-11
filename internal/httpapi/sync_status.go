package httpapi

// 作业层自省读面（2026-11-12 裁：`docs/dataflow.md` §5.1 的待裁边）。
//
// 三条端点都在本仓（队列与 worker/scheduler 的归属在这里），core 只做反代：
//   - GET /api/sync/workers           队列消费者是谁、能跑哪些数据类型；
//   - GET /api/sync/scheduler/stats   排程有多少条在跑、下一次触发是什么时候；
//   - GET /api/sync/jobs/{id}/progress SSE 进度流（UI 的 EventSource 直连）。
//
// 它们回答的是「队列是否在动」而不是「作业内容」：作业内容走 jobs 详情。
// 早前 core 的反代目标不存在，UI 拿到 404/502 —— 这里补的是**正式读面**，
// 不是临时反代替身。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"tellurion-data/internal/sync"
)

// progressPollInterval 是 SSE 重放/轮询的节奏。作业进度按秒级变化，
// 500ms 足够；再密只会把库打成热点。
const progressPollInterval = 500 * time.Millisecond

// maxSchedulerNextRuns 是 stats 里回显的「下几次触发」上限。
const maxSchedulerNextRuns = 10

// SyncRuntime 描述进程内作业循环的运行态。用函数而不是快照：workers
// 是自省面，调用时看到的就是当下。
type SyncRuntime struct {
	// WorkerIDs 返回本进程的 worker 标识（当前实现是单 worker）。
	WorkerIDs func() []string
	// RegisteredTypes 返回当前注册表声明的数据类型（= 可接的 job_type）。
	RegisteredTypes func() []string
	// IsRunning 报告作业循环是否已启动（schema 就绪后才启动）。
	IsRunning func() bool
}

// WithSyncRuntime 装配作业循环自省面。未注入 ⇒ `/api/sync/workers` 与
// `/api/sync/scheduler/stats` fail-closed 503（与未接队列同口径）。
func WithSyncRuntime(rt SyncRuntime) Option {
	return func(s *Server) { s.runtime = rt }
}

// wired 报告自省面三个探针是否都已注入。缺一个就说明装配不完整：
// 这时候回半张表（例如 num_workers=0 但 types 有值）比 503 更容易被误读。
func (r SyncRuntime) wired() bool {
	return r.WorkerIDs != nil && r.RegisteredTypes != nil && r.IsRunning != nil
}

type workerStatsBody struct {
	NumWorkers      int      `json:"num_workers"`
	WorkerIDs       []string `json:"worker_ids"`
	RegisteredTypes []string `json:"registered_types"`
	IsRunning       bool     `json:"is_running"`
}

// handleSyncWorkers 返回队列消费者自省。
func (s *Server) handleSyncWorkers(w http.ResponseWriter, _ *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil || !s.runtime.wired() {
		writeDependency(w, "sync runtime is not wired")
		return
	}
	ids := append([]string(nil), s.runtime.WorkerIDs()...)
	types := append([]string(nil), s.runtime.RegisteredTypes()...)
	sort.Strings(ids)
	sort.Strings(types)
	writeJSON(w, http.StatusOK, workerStatsBody{
		NumWorkers:      len(ids),
		WorkerIDs:       ids,
		RegisteredTypes: types,
		IsRunning:       s.runtime.IsRunning(),
	})
}

type schedulerNextRun struct {
	ScheduleID int64     `json:"schedule_id"`
	Name       string    `json:"name,omitempty"`
	NextRun    time.Time `json:"next_run"`
}

type schedulerStatsBody struct {
	ActiveSchedules int                `json:"active_schedules"`
	TotalEntries    int                `json:"total_entries"`
	NextRuns        []schedulerNextRun `json:"next_runs"`
}

// handleSchedulerStats 返回排程自省。`total_entries` 的口径 = 已激活且
// 已算出 next_run_at 的排程数（cron 真正持有条目的那些）。
func (s *Server) handleSchedulerStats(w http.ResponseWriter, r *http.Request) {
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
	body := schedulerStatsBody{NextRuns: []schedulerNextRun{}}
	for _, sc := range schedules {
		if !sc.IsActive {
			continue
		}
		body.ActiveSchedules++
		if sc.NextRunAt == nil {
			continue
		}
		body.NextRuns = append(body.NextRuns, schedulerNextRun{
			ScheduleID: sc.ID,
			Name:       sc.Name,
			NextRun:    *sc.NextRunAt,
		})
	}
	body.TotalEntries = len(body.NextRuns)
	sort.Slice(body.NextRuns, func(i, j int) bool {
		if body.NextRuns[i].NextRun.Equal(body.NextRuns[j].NextRun) {
			return body.NextRuns[i].ScheduleID < body.NextRuns[j].ScheduleID
		}
		return body.NextRuns[i].NextRun.Before(body.NextRuns[j].NextRun)
	})
	if len(body.NextRuns) > maxSchedulerNextRuns {
		body.NextRuns = body.NextRuns[:maxSchedulerNextRuns]
	}
	writeJSON(w, http.StatusOK, body)
}

// handleSyncJobProgress 是 SSE 进度流：连上先重放当前状态，之后每
// `progressPollInterval` 推一条，终态推完即关。作业不存在 ⇒ 404（在任何
// SSE 头写出之前判定）。
func (s *Server) handleSyncJobProgress(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.sync == nil {
		writeDependency(w, "sync queue is not wired")
		return
	}
	id := r.PathValue("id")
	job, err := s.sync.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	// SSE 是长连接：取消写超时（访问日志包了一层的 statusWriter 已带 Unwrap）。
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()

	ticker := time.NewTicker(progressPollInterval)
	defer ticker.Stop()
	for {
		if err := writeJobEvent(w, job); err != nil {
			return
		}
		if _ = rc.Flush(); err != nil {
			return
		}
		if terminalJobStatus(job.Status) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			next, err := s.sync.GetJob(r.Context(), id)
			if err != nil {
				_ = writeSSEEvent(w, map[string]string{"id": id, "status": "error", "error": err.Error()})
				_ = rc.Flush()
				return
			}
			job = next
		}
	}
}

// writeJobEvent 把一条作业快照写成 SSE `data:` 行。
func writeJobEvent(w http.ResponseWriter, job syncjob.Job) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return writeSSERaw(w, payload)
}

func writeSSEEvent(w http.ResponseWriter, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeSSERaw(w, payload)
}

func writeSSERaw(w http.ResponseWriter, payload []byte) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}

// terminalJobStatus 报告作业是否已到终态（终态之后不再推）。
func terminalJobStatus(status string) bool {
	switch status {
	case syncjob.StatusSucceeded, syncjob.StatusFailed, syncjob.StatusCancelled:
		return true
	default:
		return false
	}
}
