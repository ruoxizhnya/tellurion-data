// Package httpapi 是数据服务的 HTTP 面（`docs/api-schema.md` §1）。
//
// aggregator 是唯一读入口：本包只做参数解析、哨兵 → HTTP 状态码映射与
// JSON 编码，不实现任何取数/哈希/写入语义。写面只有一条 `POST /ingest/points`
// —— 它把批次交给 `internal/assembly`，闸门与幂等判定都在那一边。
// 端口未接线的进程仍然可起（/health 常绿），数据端点一律 fail-closed 成
// 503 dependency_unavailable —— 与 tellurion-analytics 的同款处置。
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/assembly"
	"tellurion-data/internal/serving"
)

// maxIngestBodyBytes 是投点请求体的字节上限。它与
// `assembly.MaxPointsPerRequest` 一起构成「HTTP 灌数入口只许单次有界任务」
// 这条红线（`docs/dataflow.md` §2）的两道门：前者拦大报文，后者拦大批次。
const maxIngestBodyBytes = 8 << 20

// Server 持读面 aggregator 与写面装配。两者为 nil 都表示端口未接线
// （本仓唯一合法的两态：接了 PG 就可用，没接就 503）。
// 所有端点都过鉴权闸（裁-15）。
type Server struct {
	agg        *serving.Aggregator
	points     *assembly.Writer
	authorizer Authorizer
}

// Option 按需装配可选面（鉴权闸、写面装配）。
type Option func(*Server)

// New 构造 HTTP Server。写面装配用 `WithPoints` 注入（不塞进位置参数：
// 读面与写面的接线是两件事，一个进程可以只有其中一半）。
func New(agg *serving.Aggregator, opts ...Option) *Server {
	s := &Server{agg: agg}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
		// `/datasets/resolve` 与 `/datasets/{hash}`：Go 1.22 ServeMux 里字面段优先
		// 于通配段，"resolve" 不会被当成 hash。
		{"GET /datasets/resolve", s.handleResolve},
		{"GET /datasets/{hash}", s.handleSnapshot},
		// 写面唯一的一条：已归一的点位投进来，闸门 + 幂等 + A 集门都在
		// `internal/assembly` / `internal/store` 那一侧。
		{"POST /ingest/points", s.handleIngestPoints},
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

// Routes 返回路由表。每个路由都挂 requireAuth（未装配 Authorizer → 503）。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	for _, spec := range s.routes() {
		mux.Handle(spec.pattern, s.requireAuth(spec.handler))
	}
	// `/factors/{name}@{hash}` 已于裁-14（2026-10-25）撤掉：因子读面只由 core
	// 提供，本仓没有该端点。不要重新引入——常驻 404 会被调用方读成
	// 「该数据集没有该因子」；路由不存在才是明确边界。
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

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
	{serving.ErrSnapshotMismatch, http.StatusInternalServerError, "snapshot_mismatch"},
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
