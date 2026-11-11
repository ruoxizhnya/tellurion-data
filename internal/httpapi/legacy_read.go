package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/serving"
)

// 本文件是 **G3 只读兼容面**（SVC-DATA-G3，2026-11-10）。
//
// 背景：core 的 `pkg/marketdata/http_provider.go` 仍按旧 cmd/data 的只读路由
// 取数（`/ohlcv/{symbol}`、`/stocks`、`/index/{code}/constituents`、
// `/api/v1/trading/calendar`、`/api/v1/stocks/{symbol}`、
// `POST /api/v1/ohlcv/bulk`）。旧服务已随 core 的数据面退役删除，这些路由在
// 本服务里**不存在**，读链实际是 404。
//
// 注意 `GET /fundamental/{symbol}` **不在**这组里（2026-11-12 归属裁决）：
// `stock_fundamentals` 是 core 自己的表（DDL 在 `pkg/storage/postgres.go`，
// 写者/读者都在 core）。本服务此前把它读成 `market.stock_fundamentals`，
// 而那张表从未建立 —— 空态被冒成 500。裁决后由 core 的
// `GET /api/fundamental/{symbol}` 读回 `public.stock_fundamentals`，本服务
// 删除该读路径，不补空表。
//
// 主任裁决（2026-11-10）：由本服务补上这组**只读兼容端点**，core 零改。它
// 推翻了 K1「data 不补齐 core 的 5 条旧读面」的字面口径，理由写在
// `docs/TASKS.md` SVC-DATA-G3 的施工记录里（产品读链优先，且这些形状是逐表
// 兼容、不是新的供给面）。
//
// 与供给面（`/datasets/resolve`）的分工：这里**不做** PIT 过滤、频率聚合、
// 快照哈希，只按表读、按旧字段名编码。任何新调用方都应该用供给面。

// WithLegacyReader 注入 G3 只读兼容面的端口。nil = 未接线 ⇒ 这组端点 503。
func WithLegacyReader(reader serving.LegacyReader) Option {
	return func(s *Server) { s.legacy = reader }
}

// legacyReadAsOf 是「没有 as_of 参数」的旧读面的缺省时点。
//
// `GET /index/{code}/constituents` 的旧形状没有时点参数，语义就是「现在的
// 成分」；这里显式读一次墙钟（store 层不读钟），并截到 UTC 日期。
func legacyReadAsOf() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// handleLegacyOHLCV 是 `GET /ohlcv/{symbol}`：core 的日线读面。
func (s *Server) handleLegacyOHLCV(w http.ResponseWriter, r *http.Request) {
	if !s.legacyReady(w, s.legacy) {
		return
	}
	q := r.URL.Query()
	from, err := parseCompactDateParam(q, "start_date")
	if err != nil {
		writeError(w, err)
		return
	}
	to, err := parseCompactDateParam(q, "end_date")
	if err != nil {
		writeError(w, err)
		return
	}
	bars, err := s.legacy.ReadBars(r.Context(), []string{r.PathValue("symbol")}, from, to, market.Freq1d)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ohlcv": bars})
}

// handleLegacyStocks 是 `GET /stocks[?exchange=]`。
//
// `latest_date` 是 2026-11-12 裁的**派生增加**：core 的 `/api/stocks/count`
// 需要「最近有数据的交易日」，而该信息本就在本仓读面（日线最大值）里，
// 不应为此新增一个端点。空库时为 null，不是 1970。
func (s *Server) handleLegacyStocks(w http.ResponseWriter, r *http.Request) {
	if !s.legacyReady(w, s.legacy) {
		return
	}
	stocks, err := s.legacy.Stocks(r.Context(), r.URL.Query().Get("exchange"))
	if err != nil {
		writeError(w, err)
		return
	}
	body := map[string]any{"stocks": stocks}
	if day, found, err := s.legacy.LatestTradeDate(r.Context()); err != nil {
		writeError(w, err)
		return
	} else if found {
		body["latest_date"] = day.Format("2006-01-02")
	} else {
		body["latest_date"] = nil
	}
	writeJSON(w, http.StatusOK, body)
}

// handleLegacyStock 是 `GET /api/v1/stocks/{symbol}`；不在库里 ⇒ 404。
func (s *Server) handleLegacyStock(w http.ResponseWriter, r *http.Request) {
	if !s.legacyReady(w, s.legacy) {
		return
	}
	stock, found, err := s.legacy.Stock(r.Context(), r.PathValue("symbol"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		writeNotFound(w, "stock not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stock": stock})
}

// handleLegacyConstituents 是 `GET /index/{code}/constituents[?date=YYYYMMDD]`。
//
// 读面必须自报口径：本仓 `market.index_constituents` 当前只有 Tushare
// `index_weight` 的**月频当前快照**（`in_date` = 快照日、`out_date` 恒 NULL），
// 没有完整成分变更史。因此响应固定带 `pit_eligible: false` 与一句 usage——
// 「早于快照日的 as_of 解析为空」是可观察的行为，不是文档里的一句话；
// 把它当历史成分做 PIT 回测会拿到幸存者偏差的结果。
func (s *Server) handleLegacyConstituents(w http.ResponseWriter, r *http.Request) {
	if !s.legacyReady(w, s.legacy) {
		return
	}
	asOf := legacyReadAsOf()
	if raw := r.URL.Query().Get("date"); raw != "" {
		parsed, err := time.Parse("20060102", raw)
		if err != nil {
			writeError(w, fmt.Errorf("%w: date %q is not YYYYMMDD", serving.ErrInvalidParameter, raw))
			return
		}
		asOf = parsed
	}
	items, err := s.legacy.IndexConstituents(r.Context(), r.PathValue("code"), asOf)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"constituents": items,
		"pit_eligible": false,
		"usage":        "current snapshot only; not for point-in-time backtests",
	})
}

// handleLegacyCalendar 是 `GET /api/v1/trading/calendar?start=YYYY-MM-DD&end=YYYY-MM-DD`。
// 空窗口返回空数组（core 的 `IsTradingDay` 以「是否在数组里」为判据）。
func (s *Server) handleLegacyCalendar(w http.ResponseWriter, r *http.Request) {
	if !s.legacyReady(w, s.legacy) {
		return
	}
	q := r.URL.Query()
	from, err := parseISODateParam(q, "start")
	if err != nil {
		writeError(w, err)
		return
	}
	to, err := parseISODateParam(q, "end")
	if err != nil {
		writeError(w, err)
		return
	}
	days, err := s.legacy.TradingDays(r.Context(), from, to)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]string, 0, len(days))
	for _, d := range days {
		out = append(out, d.Format("2006-01-02"))
	}
	writeJSON(w, http.StatusOK, map[string]any{"trading_days": out})
}

// legacyBulkRequest 是 `POST /api/v1/ohlcv/bulk` 的请求体（旧字段名）。
type legacyBulkRequest struct {
	Symbols   []string `json:"symbols"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
}

// legacyBulkResult 是逐票结果；Error 非空时 core 跳过该票（不整体失败）。
type legacyBulkResult struct {
	Symbol string         `json:"symbol"`
	OHLCV  []market.OHLCV `json:"ohlcv"`
	Error  string         `json:"error,omitempty"`
}

// handleLegacyBulk 是 `POST /api/v1/ohlcv/bulk`（旧批量日线读面）。
func (s *Server) handleLegacyBulk(w http.ResponseWriter, r *http.Request) {
	if !s.legacyReady(w, s.legacy) {
		return
	}
	var req legacyBulkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if len(req.Symbols) == 0 {
		writeError(w, fmt.Errorf("%w: symbols", serving.ErrMissingParameter))
		return
	}
	from, err := parseCompactDate(req.StartDate, "start_date")
	if err != nil {
		writeError(w, err)
		return
	}
	to, err := parseCompactDate(req.EndDate, "end_date")
	if err != nil {
		writeError(w, err)
		return
	}
	results := make([]legacyBulkResult, 0, len(req.Symbols))
	for _, symbol := range req.Symbols {
		bars, err := s.legacy.ReadBars(r.Context(), []string{symbol}, from, to, market.Freq1d)
		if err != nil {
			results = append(results, legacyBulkResult{Symbol: symbol, Error: err.Error()})
			continue
		}
		results = append(results, legacyBulkResult{Symbol: symbol, OHLCV: bars})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// legacyReady 是这组端点的统一前置：依赖可用门 + 端口接线检查。
func (s *Server) legacyReady(w http.ResponseWriter, reader serving.LegacyReader) bool {
	if !s.dependencyReady(w) {
		return false
	}
	if reader == nil {
		writeDependency(w, "legacy read面 is not wired (no PG store)")
		return false
	}
	return true
}

// writeNotFound 用与其它错误一致的 JSON 形状回 404。
func writeNotFound(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusNotFound, errorBody{Error: errPayload{Code: "not_found", Message: message}})
}

// parseCompactDateParam 解析必填的 YYYYMMDD 参数。
func parseCompactDateParam(q url.Values, name string) (time.Time, error) {
	return parseCompactDate(q.Get(name), name)
}

func parseCompactDate(raw, name string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("%w: %s", serving.ErrMissingParameter, name)
	}
	t, err := time.Parse("20060102", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s %q is not YYYYMMDD", serving.ErrInvalidParameter, name, raw)
	}
	return t, nil
}

// parseISODateParam 解析必填的 YYYY-MM-DD 参数（日历读面用）。
func parseISODateParam(q url.Values, name string) (time.Time, error) {
	raw := q.Get(name)
	if raw == "" {
		return time.Time{}, fmt.Errorf("%w: %s", serving.ErrMissingParameter, name)
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s %q is not YYYY-MM-DD", serving.ErrInvalidParameter, name, raw)
	}
	return t, nil
}

// decodeJSON 解析有界请求体（与 /ingest/points 同口径：拒绝未知字段）。
func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngestBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("%w: body is not valid JSON: %v", serving.ErrInvalidParameter, err)
	}
	return nil
}
