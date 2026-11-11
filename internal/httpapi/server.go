// Package httpapi 是数据服务的 HTTP 面（`docs/api-schema.md` §1）。
//
// aggregator 是唯一读入口：本包只做参数解析、哨兵 → HTTP 状态码映射与
// JSON 编码，不实现任何取数/哈希/写入语义。写面只有一条 `POST /ingest/points`
// —— 它把批次交给 `internal/assembly`，闸门与幂等判定都在那一边。
// 端口未接线的进程仍然可起（/health 常绿），数据端点一律 fail-closed 成
// 503 dependency_unavailable —— 与 tellurion-analytics 的同款处置。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/assembly"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
	"tellurion-data/internal/sync"
)

// maxIngestBodyBytes 是投点请求体的字节上限。它与
// `assembly.MaxPointsPerRequest` 一起构成「HTTP 灌数入口只许单次有界任务」
// 这条红线（`docs/dataflow.md` §2）的两道门：前者拦大报文，后者拦大批次。
const maxIngestBodyBytes = 8 << 20

// Server 持读面 aggregator 与写面装配。两者为 nil 都表示端口未接线
// （本仓唯一合法的两态：接了 PG 就可用，没接就 503）。
// 所有业务端点都过鉴权闸（裁-15）；`/health` 与 `/readyz` 是公开探针（G1），
// 不鉴权、不限流。
type Server struct {
	agg        *serving.Aggregator
	points     *assembly.Writer
	legacy     serving.LegacyReader
	evidence   EvidenceArchive
	sync       syncjob.Store
	runtime    SyncRuntime
	authorizer Authorizer
	readiness  func(context.Context) error
	available  func() bool
	limiter    *rateLimiter
}

// Option 按需装配可选面（鉴权闸、写面装配、就绪检查、限流）。
type Option func(*Server)

// New 构造 HTTP Server。写面装配用 `WithPoints` 注入（不塞进位置参数：
// 读面与写面的接线是两件事，一个进程可以只有其中一半）。
func New(agg *serving.Aggregator, opts ...Option) *Server {
	s := &Server{agg: agg, limiter: newRateLimiter(defaultRatePerSecond, defaultRateBurst)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithReadiness 注入就绪检查（G1）：`/readyz` = PG 池可连 + 本仓 DDL 已执行。
// 不注入 ⇒ 503（`DATABASE_URL` 为空的进程就是没接线，不应被读成就绪）。
func WithReadiness(check func(context.Context) error) Option {
	return func(s *Server) { s.readiness = check }
}

// WithAvailability 注入数据面的「依赖可用」判据（G4）。nil = 不设门
// （测试与纯内存装配）。
//
// 与 `/readyz` 的分工：`/readyz` 是**探针**（会真的 Ping），本门是**每个
// 业务请求的快速判据**（一个原子布尔），不额外打一次库。进程降级启动
// （PG 不可达）时它是 false：数据端点立即 503 而不是逐个等连接超时；
// 后台迁移重试成功后被翻成 true，同一个进程自愈。
func WithAvailability(available func() bool) Option {
	return func(s *Server) { s.available = available }
}

// dependencyReady 在依赖不可用时写 503 并返回 false。
func (s *Server) dependencyReady(w http.ResponseWriter) bool {
	if s.available != nil && !s.available() {
		writeDependency(w, "postgres is not ready (schema not applied or pool unreachable); check GET /readyz")
		return false
	}
	return true
}

// WithRateLimit 覆盖默认限流（每秒 perSecond 个令牌、突发 burst）。
// perSecond <= 0 或 burst <= 0 ⇒ 关闭限流（仅测试用）。
func WithRateLimit(perSecond float64, burst int) Option {
	return func(s *Server) {
		if perSecond <= 0 || burst <= 0 {
			s.limiter = nil
			return
		}
		s.limiter = newRateLimiter(perSecond, float64(burst))
	}
}

// WithPoints 装配写面（`POST /ingest/points`）。nil = 未接线 ⇒ 该端点 503。
func WithPoints(w *assembly.Writer) Option {
	return func(s *Server) { s.points = w }
}

// HasAggregator 报告取数 port 是否已接线（进程启动日志用；不影响路由）。
func (s *Server) HasAggregator() bool { return s.agg != nil }

// HasPointsWriter 报告写面是否已接线（同上）。
func (s *Server) HasPointsWriter() bool { return s.points != nil }

// routeSpec 是「注册模式 + handler」；路由表与策略表由守卫测试双向对拍。
type routeSpec struct {
	pattern string
	handler http.HandlerFunc
}

func (s *Server) routes() []routeSpec {
	return []routeSpec{
		{"GET /health", s.handleHealth},
		{"GET /readyz", s.handleReadyz},
		// `/datasets/resolve` 与 `/datasets/{hash}`：Go 1.22 ServeMux 里字面段优先
		// 于通配段，"resolve" 不会被当成 hash。
		{"GET /datasets/resolve", s.handleResolve},
		{"GET /datasets/{hash}", s.handleSnapshot},
		// 写面唯一的一条：已归一的点位投进来，闸门 + 幂等 + A 集门都在
		// `internal/assembly` / `internal/store` 那一侧。
		{"POST /ingest/points", s.handleIngestPoints},

		// ── 证据归档面（`evidence.go`）──
		// `ingest.raw` 的唯一写者是本服务；core 只做 `GET /api/evidence/{hash}`
		// 反代（ADR-0009）。读面按时间列最近 N 条，分页上限 100。
		{"POST /api/ingest/raw", s.handleIngestRaw},
		{"GET /api/evidence", s.handleListEvidence},
		{"GET /api/evidence/{content_hash}", s.handleGetEvidence},

		// ── G3 只读兼容面（`legacy_read.go`）──
		// core 的 `pkg/marketdata/http_provider.go` 仍打这 6 条旧路由；旧 cmd/data
		// 已退役，不在本服务补上就整条读链 404。只读、不参与快照哈希。
		// `GET /fundamental/{symbol}` 已于 2026-11-12 删除：`stock_fundamentals`
		// 是 core 自己的表（DDL/写者/读者都在 core），本服务不补空表、不冒充
		// 读面（归属裁决，见 core 的 handlers_fundamental.go）。
		{"GET /ohlcv/{symbol}", s.handleLegacyOHLCV},
		{"GET /stocks", s.handleLegacyStocks},
		{"GET /index/{code}/constituents", s.handleLegacyConstituents},
		{"GET /api/v1/trading/calendar", s.handleLegacyCalendar},
		{"GET /api/v1/stocks/{symbol}", s.handleLegacyStock},
		{"POST /api/v1/ohlcv/bulk", s.handleLegacyBulk},

		// ── 作业层（`sync.go`）──
		// 队列即 `ingest.sync_jobs`；worker/scheduler 由 `cmd/dataservice` 启动。
		// `/api/sync/jobs` 的 GET 是新增只读面（core 旧服务只有 POST），
		// 排程的 GET 同理——「建了却看不见」的队列不可运维。
		{"POST /api/sync/jobs", s.handleCreateSyncJob},
		{"GET /api/sync/jobs", s.handleListSyncJobs},
		{"GET /api/sync/jobs/{id}", s.handleGetSyncJob},
		// 作业进度是 SSE 长连接（`sync_status.go`）：UI 的 EventSource 直接打这条。
		{"GET /api/sync/jobs/{id}/progress", s.handleSyncJobProgress},
		// 队列/排程的自省读面：worker 与 cron 是否真的在跑，只能从这里看。
		{"GET /api/sync/workers", s.handleSyncWorkers},
		{"GET /api/sync/scheduler/stats", s.handleSchedulerStats},
		{"POST /api/sync/jobs/{id}/cancel", s.handleCancelSyncJob},
		{"POST /api/sync/jobs/{id}/retry", s.handleRetrySyncJob},
		{"POST /api/sync/schedules", s.handleCreateSchedule},
		{"GET /api/sync/schedules", s.handleListSchedules},
		{"PUT /api/sync/schedules/{id}", s.handleUpdateSchedule},
		{"DELETE /api/sync/schedules/{id}", s.handleDeleteSchedule},
		{"POST /api/sync/schedules/{id}/toggle", s.handleToggleSchedule},
		{"POST /api/sync/schedules/{id}/run", s.handleRunScheduleNow},
	}
}

// routePatterns 只返回注册模式，守卫测试用它与策略表对拍。
func (s *Server) routePatterns() []string {
	specs := s.routes()
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.pattern)
	}
	return out
}

// Routes 返回路由表。业务路由挂 requireAuth（未装配 Authorizer → 503）；
// 策略表标 `Public` 的探针直接注册，不鉴权、不限流。
//
// 每条路由外层再包一层访问日志（G4 ③ 的可观测最小面）：一行一个请求，
// 带路由模式、状态码与耗时。它是「不接 Prometheus」时的唯一错误率/延迟来源，
// 字段格式被 `docs/runbook.md` 的统计命令依赖，改动要同步改命令。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	for _, spec := range s.routes() {
		var handler http.Handler = spec.handler
		if rule, ok := servedPolicy.Rule(spec.pattern); !ok || !rule.Public {
			handler = s.requireAuth(spec.handler)
		}
		mux.Handle(spec.pattern, withAccessLog(spec.pattern, handler))
	}
	// `/factors/{name}@{hash}` 已于裁-14（2026-10-25）撤掉：因子读面只由 core
	// 提供，本仓没有该端点。不要重新引入——常驻 404 会被调用方读成
	// 「该数据集没有该因子」；路由不存在才是明确边界。
	return mux
}

// accessLog 是 G4 的唯一观测出口：一行一个请求，不引指标客户端。
//
// 形态固定为 `httpapi: route="<pattern>" status=<int> dur_ms=<float>`；
// route 用注册模式而不是原始路径（`/datasets/{hash}` 才不会把每个 hash
// 变成一条独立时间序列）。
func withAccessLog(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Printf("httpapi: route=%q status=%d dur_ms=%.6f", pattern, status, time.Since(start).Seconds()*1000)
	})
}

// statusWriter 只记第一个 WriteHeader 的状态码（net/http 的语义）。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap 暴露底层 ResponseWriter：SSE 进度端点要 Flush，且要用
// `http.ResponseController` 取消写超时（长连接不能被 120s 的写上限切断）。
// 没有 Unwrap，包一层访问日志就会把 Flush 能力弄丢。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush 让 *statusWriter 直接满足 http.Flusher（SSE 需要）。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyBody 是 `/readyz` 的报文。`store` 说明检查的是哪一档
// （`postgres` = 真库检查已注入；`unwired` = 没接库）。
type readyBody struct {
	Status string `json:"status"`
	Store  string `json:"store"`
	Error  string `json:"error,omitempty"`
}

// handleReadyz 是就绪探针（G1）：PG 池可连 + 本仓 DDL 已执行。
//
// 与 `/health` 的分工：`/health` 只回答「进程活着」，不读库——库挂了它也 200；
// `/readyz` 回答「能不能接业务」，没接库或库不可达一律 503。两者都不鉴权、
// 不限流（探针被限流会被读成未登录，core AUD-57 的坑）。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.readiness == nil {
		writeJSON(w, http.StatusServiceUnavailable, readyBody{
			Status: "not_ready",
			Store:  "unwired",
			Error:  "DATABASE_URL is empty: data ports are not wired",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := s.readiness(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, readyBody{
			Status: "not_ready",
			Store:  "postgres",
			Error:  err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, readyBody{Status: "ready", Store: "postgres"})
}

// readinessTimeout 是就绪检查的上界：探针是 1s 级的东西，库卡住必须
// 明确报 not_ready，而不是把调用方挂在这里。
const readinessTimeout = 3 * time.Second

// handleIngestPoints 是写面唯一的入口（QT-40 H4 ②）。
//
// 三道门都在这里，缺一不可：
//   - 字节上限 + `DisallowUnknownFields`：`visible_at` / `ingest_time` 是
//     服务器独占的字段，投进来必须解码失败而不是「忽略掉」——被忽略的字段
//     会让调用方以为自己的可见时刻生效了。
//   - `UseNumber`：金融数值折成 float64 是静默精度损失，`store` 的
//     `toFloat64` 收 json.Number。
//   - 有界：`assembly.MaxPointsPerRequest` 拦批次，`maxIngestBodyBytes` 拦
//     报文。无界批量一律走作业层（`docs/dataflow.md` §2 红线）。
//
// 判定本身一条都不在这里：闸门、去重、A 集门、内容比对全在
// `internal/assembly` → `internal/store`，本 handler 只做编码与分诊。
func (s *Server) handleIngestPoints(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.points == nil {
		writeDependency(w, "write面 is not wired (no PG store or declaration registry)")
		return
	}
	var req assembly.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngestBodyBytes))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		writeError(w, fmt.Errorf("%w: body is not a valid point batch: %v", serving.ErrInvalidParameter, err))
		return
	}
	result, err := s.points.Write(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.agg == nil {
		writeDependencyUnavailable(w)
		return
	}
	q := r.URL.Query()
	req := serving.ResolveRequest{
		Universe:   q.Get("universe"),
		IndexCode:  q.Get("index_code"),
		Symbols:    splitSymbols(q["symbols"]),
		Adjustment: q.Get("adjustment"),
	}
	var err error
	if req.From, err = parseTimeParam(q, "from"); err != nil {
		writeError(w, err)
		return
	}
	if req.To, err = parseTimeParam(q, "to"); err != nil {
		writeError(w, err)
		return
	}
	if req.AsOf, err = parseTimeParam(q, "as_of"); err != nil {
		writeError(w, err)
		return
	}
	freq := q.Get("freq")
	if freq == "" {
		writeError(w, fmt.Errorf("%w: freq", serving.ErrMissingParameter))
		return
	}
	parsed, err := market.ParseFrequency(freq)
	if err != nil {
		writeError(w, fmt.Errorf("%w: freq %q is not a known frequency (known: 1m/15m/1d)", serving.ErrInvalidParameter, freq))
		return
	}
	req.Frequency = parsed

	result, err := s.agg.Resolve(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.agg == nil {
		writeDependencyUnavailable(w)
		return
	}
	dataset, err := s.agg.Snapshot(r.PathValue("hash"))
	if err != nil {
		writeError(w, err)
		return
	}
	// bytes.Buffer 直出：同一 hash 的两次响应必须逐位一致（api-schema §3）。
	body, err := json.Marshal(dataset)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// sentinelCodes 是哨兵 → HTTP 状态码与错误码的映射表（唯一真相 =
// `internal/serving/errors.go` 头注释的分诊口径）。errors.Is 分诊，
// 未登记的错误一律 500，绝不把未知失败伪装成 4xx。
var sentinelCodes = []struct {
	sentinel error
	status   int
	code     string
}{
	{serving.ErrMissingParameter, http.StatusBadRequest, "missing_parameter"},
	{serving.ErrInvalidParameter, http.StatusBadRequest, "invalid_parameter"},
	{serving.ErrUniverseAmbiguous, http.StatusBadRequest, "universe_ambiguous"},
	{serving.ErrUnexpectedSymbol, http.StatusBadRequest, "unexpected_symbol"},
	{serving.ErrEmptyUniverse, http.StatusConflict, "empty_universe"},
	{serving.ErrEmptyCalendar, http.StatusConflict, "empty_calendar"},
	{serving.ErrEmptyDataVersion, http.StatusConflict, "empty_data_version"},
	{serving.ErrSnapshotNotFound, http.StatusNotFound, "snapshot_not_found"},
	{serving.ErrEvidenceNotFound, http.StatusNotFound, "evidence_not_found"},
	{serving.ErrSnapshotMismatch, http.StatusInternalServerError, "snapshot_mismatch"},
	// G4：PG 不可达是 503（可等待恢复），不是 500。哨兵来自 `internal/store`
	// 的连接层分诊（`classifyDependency`），只包连接失败，不包 SQL 语义错误。
	{store.ErrDependencyUnavailable, http.StatusServiceUnavailable, "dependency_unavailable"},
	// 作业层分诊：不存在 ⇒ 404；状态不允许 ⇒ 409；参数坏 ⇒ 400。
	// 三条与「快照不存在」分开登记，是因为调用方的处置不同：改 ID、等状态、
	// 改内容，三种动作不能混成一个 not_found / conflict 的大口袋。
	{syncjob.ErrJobNotFound, http.StatusNotFound, "job_not_found"},
	{syncjob.ErrScheduleNotFound, http.StatusNotFound, "schedule_not_found"},
	{syncjob.ErrJobNotCancellable, http.StatusConflict, "job_not_cancellable"},
	{syncjob.ErrJobNotRetryable, http.StatusConflict, "job_not_retryable"},
	{syncjob.ErrInvalidParams, http.StatusBadRequest, "invalid_parameter"},
}

func writeError(w http.ResponseWriter, err error) {
	for _, m := range sentinelCodes {
		if errors.Is(err, m.sentinel) {
			writeJSON(w, m.status, errorBody{Error: errPayload{Code: m.code, Message: err.Error()}})
			return
		}
	}
	// 写面的分诊表在 `internal/assembly`（那些哨兵来自 assembly / ingest /
	// store 三层，本包不该 import 那三个包去自己凑一张表）。
	if status, code, ok := assembly.WireError(err); ok {
		writeJSON(w, status, errorBody{Error: errPayload{Code: code, Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorBody{Error: errPayload{Code: "internal", Message: err.Error()}})
}

func writeDependencyUnavailable(w http.ResponseWriter) {
	writeDependency(w, "data ports are not wired (PG implementations land with X1d)")
}

func writeDependency(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errPayload{
		Code:    "dependency_unavailable",
		Message: reason,
	}})
}

type errorBody struct {
	Error errPayload `json:"error"`
}

type errPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// parseTimeParam 解析必填时间参数（RFC3339）。缺失 ⇒ ErrMissingParameter，
// 坏值 ⇒ ErrInvalidParameter，与 ResolveRequest.Validate 的分诊一致。
func parseTimeParam(q url.Values, name string) (time.Time, error) {
	raw := q.Get(name)
	if raw == "" {
		return time.Time{}, fmt.Errorf("%w: %s", serving.ErrMissingParameter, name)
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s %q is not RFC3339", serving.ErrInvalidParameter, name, raw)
	}
	return t, nil
}

// splitSymbols 收 symbols：可重复出现、也接受逗号分隔（单 symbol 取数走
// 同一面，不给逐 bar 端点留口子）。
func splitSymbols(raw []string) []string {
	var out []string
	for _, item := range raw {
		for _, s := range strings.Split(item, ",") {
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
