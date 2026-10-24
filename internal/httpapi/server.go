// Package httpapi 是数据服务的 HTTP 面（`docs/api-schema.md` §1）。
//
// aggregator 是唯一入口：本包只做参数解析、哨兵 → HTTP 状态码映射与
// JSON 编码，不实现任何取数/哈希语义。端口未接线的进程仍然可起
// （/health 常绿），数据端点一律 fail-closed 成 503 dependency_unavailable
// —— 与 tellurion-analytics 的同款处置。
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

	"tellurion-data/internal/serving"
)

// Server 持 aggregator。agg 为 nil 表示端口未接线（X1d 的 PG 实现落地前
// 的唯一合法形态）。
type Server struct {
	agg *serving.Aggregator
}

// New 构造 HTTP Server。
func New(agg *serving.Aggregator) *Server { return &Server{agg: agg} }

// HasAggregator 报告取数 port 是否已接线（进程启动日志用；不影响路由）。
func (s *Server) HasAggregator() bool { return s.agg != nil }

// Routes 返回路由表。
//
// `/datasets/resolve` 与 `/datasets/{hash}`：Go 1.22 ServeMux 里字面段优先
// 于通配段，"resolve" 不会被当成 hash。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /datasets/resolve", s.handleResolve)
	mux.HandleFunc("GET /datasets/{hash}", s.handleSnapshot)
	// `/factors/{name}@{hash}` 已于裁-14（2026-10-25）撤掉：因子读面只由 core
	// 提供，本仓没有该端点。不要重新引入——常驻 404 会被调用方读成
	// 「该数据集没有该因子」；路由不存在才是明确边界。
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	writeJSON(w, http.StatusInternalServerError, errorBody{Error: errPayload{Code: "internal", Message: err.Error()}})
}

func writeDependencyUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errPayload{
		Code:    "dependency_unavailable",
		Message: "data ports are not wired (PG implementations land with X1d)",
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
