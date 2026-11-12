package tushare

// 复权基准（ADR-0011）的判据：基准必须全局固定，不能随请求窗口漂移。
// 这些用例都跑在 httptest 上，账本用内存替身（真 PG 腿见
// `internal/store/adjust_baseline_pg_test.go`）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/ingest"
)

// memRecord 是账本替身里的一条记录（字段与 PG 表一致）。
type memRecord struct {
	date       time.Time
	factor     float64
	prevDate   *time.Time
	prevFactor *float64
	reason     string
}

// memLedger 是 BaselineLedger 的内存替身：append-only + bootstrap 唯一。
type memLedger struct {
	mu      sync.Mutex
	records map[string][]memRecord
	latest  map[string]time.Time
}

func newMemLedger() *memLedger {
	return &memLedger{records: map[string][]memRecord{}, latest: map[string]time.Time{}}
}

func (m *memLedger) Load(_ context.Context, symbol string) (time.Time, float64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs := m.records[symbol]
	if len(rs) == 0 {
		return time.Time{}, 0, false, nil
	}
	last := rs[len(rs)-1]
	return last.date, last.factor, true, nil
}

func (m *memLedger) Save(_ context.Context, symbol string, date time.Time, factor float64,
	prevDate *time.Time, prevFactor *float64, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if reason == BaselineReasonBootstrap {
		// 与 PG 的局部唯一索引同语义：并发首拉只让第一个赢家写入。
		for _, r := range m.records[symbol] {
			if r.reason == BaselineReasonBootstrap {
				return nil
			}
		}
	}
	m.records[symbol] = append(m.records[symbol], memRecord{
		date: date, factor: factor, prevDate: prevDate, prevFactor: prevFactor, reason: reason,
	})
	return nil
}

func (m *memLedger) LatestTradeDate(_ context.Context, symbol string) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.latest[symbol]
	return d, ok, nil
}

func (m *memLedger) all(symbol string) []memRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]memRecord(nil), m.records[symbol]...)
}

// dailyBar 是一条「tushare 原始行」：close 为未复权价，factor 为当日因子。
type dailyBar struct {
	date   string
	close  float64
	factor float64
}

var (
	dailyFields = []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}
	adjFields   = []string{"ts_code", "trade_date", "adj_factor"}
)

// newBarsClient 起一个按 start_date/end_date 过滤的假 tushare：
// 窗口语义必须真实生效，否则「窗口最新因子」这条老口径就复现不出来。
func newBarsClient(t *testing.T, symbol string, bars []dailyBar) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		start, _ := req.Params["start_date"].(string)
		end, _ := req.Params["end_date"].(string)
		items := make([][]any, 0, len(bars))
		switch req.APIName {
		case "daily":
			for _, b := range bars {
				if b.date >= start && b.date <= end {
					items = append(items, []any{symbol, b.date, b.close, b.close, b.close, b.close, 1.0, 1.0})
				}
			}
			writeTushare(t, w, dailyFields, items)
		case "adj_factor":
			for _, b := range bars {
				if b.date >= start && b.date <= end {
					items = append(items, []any{symbol, b.date, b.factor})
				}
			}
			writeTushare(t, w, adjFields, items)
		default:
			t.Errorf("unexpected api %q", req.APIName)
			writeTushare(t, w, nil, nil)
		}
	}))
	t.Cleanup(srv.Close)
	return New(Config{
		Token:      "test-token",
		BaseURL:    srv.URL,
		MaxRetries: 0,
		Now:        func() time.Time { return fixedNow },
	})
}

func writeTushare(t *testing.T, w http.ResponseWriter, fields []string, items [][]any) {
	t.Helper()
	if fields == nil {
		fields = []string{}
	}
	if items == nil {
		items = [][]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"msg":  "",
		"data": map[string]any{"fields": fields, "items": items},
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func closeByDate(rows []market.OHLCV) map[string]float64 {
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		out[r.Date.Format("20060102")] = r.Close
	}
	return out
}

func day(s string) time.Time {
	t, err := time.Parse("20060102", s)
	if err != nil {
		panic(err)
	}
	return t
}

// 同一区间在不同窗口里拉两次，重叠日期的 qfq 价必须完全相同。
//
// 老口径（区间最新因子）下：窗口 A 基准 2026-01-07/2.0，窗口 B 基准
// 2026-01-09/4.0，于是 2026-01-06 的 close 在两次拉取里是 10 vs 5。
func TestFetchDailyOHLCV_BaselineIsPinnedAcrossWindows(t *testing.T) {
	bars := []dailyBar{
		{"20260105", 10, 1.0},
		{"20260106", 20, 1.0},
		{"20260107", 30, 2.0},
		{"20260108", 40, 2.0},
		{"20260109", 50, 4.0},
	}
	c := newBarsClient(t, "600519.SH", bars)
	ledger := newMemLedger()
	c.Baselines = ledger

	first, err := c.FetchDailyOHLCV(context.Background(), "600519.SH", "20260105", "20260107")
	if err != nil {
		t.Fatalf("first window: %v", err)
	}
	firstClose := closeByDate(first)
	if firstClose["20260105"] != 5 || firstClose["20260106"] != 10 || firstClose["20260107"] != 30 {
		t.Fatalf("first window closes = %v, want 0105=5 0106=10 0107=30", firstClose)
	}

	// 第二个窗口扩到了更晚的日期（因子 4.0）：基准必须仍是第一次钉下的 2.0。
	second, err := c.FetchDailyOHLCV(context.Background(), "600519.SH", "20260106", "20260109")
	if err != nil {
		t.Fatalf("second window: %v", err)
	}
	secondClose := closeByDate(second)
	if got, want := secondClose["20260106"], firstClose["20260106"]; got != want {
		t.Fatalf("2026-01-06 close = %v in window B, %v in window A (基准随窗口漂移了)", got, want)
	}
	if secondClose["20260107"] != 30 {
		t.Fatalf("2026-01-07 close = %v, want 30 (同一基准因子 2.0)", secondClose["20260107"])
	}
	if secondClose["20260109"] != 100 { // 50 × 4.0 / 2.0
		t.Fatalf("2026-01-09 close = %v, want 100", secondClose["20260109"])
	}

	recs := ledger.all("600519.SH")
	if len(recs) != 1 {
		t.Fatalf("ledger records = %d, want 1 (基准只在首次入库时钉一次)", len(recs))
	}
	if recs[0].reason != BaselineReasonBootstrap || recs[0].date.Format("20060102") != "20260107" || recs[0].factor != 2.0 {
		t.Fatalf("bootstrap record = %+v, want 20260107/2.0", recs[0])
	}
}

func TestFetchDailyOHLCV_RequiresBaselineLedger(t *testing.T) {
	c := newBarsClient(t, "600519.SH", []dailyBar{{"20260105", 10, 1.0}})
	// Baselines 故意不装配：不许悄悄退回「窗口最新因子」。
	_, err := c.FetchDailyOHLCV(context.Background(), "600519.SH", "20260105", "20260105")
	if err == nil || !strings.Contains(err.Error(), "基准账本未装配") {
		t.Fatalf("err = %v, want 基准账本未装配", err)
	}
}

// 无账本但有已落库数据 ⇒ 用 MAX(trade_date) 重建老口径的基准，
// 并在窗口外单查那一天的因子。
func TestFetchDailyOHLCV_BootstrapFromPersistedData(t *testing.T) {
	bars := []dailyBar{
		{"20251231", 99, 1.5}, // 仅用于单日 adj_factor 回填
		{"20260105", 15, 1.0},
		{"20260106", 15, 2.0},
	}
	c := newBarsClient(t, "600519.SH", bars)
	ledger := newMemLedger()
	ledger.latest["600519.SH"] = day("20251231")
	c.Baselines = ledger

	rows, err := c.FetchDailyOHLCV(context.Background(), "600519.SH", "20260105", "20260106")
	if err != nil {
		t.Fatalf("FetchDailyOHLCV: %v", err)
	}
	closes := closeByDate(rows)
	if closes["20260105"] != 10 { // 15 × 1.0 / 1.5
		t.Fatalf("2026-01-05 close = %v, want 10 (基准 = 已落库最新交易日的因子 1.5)", closes["20260105"])
	}
	if closes["20260106"] != 20 { // 15 × 2.0 / 1.5
		t.Fatalf("2026-01-06 close = %v, want 20", closes["20260106"])
	}

	recs := ledger.all("600519.SH")
	if len(recs) != 1 {
		t.Fatalf("ledger records = %d, want 1", len(recs))
	}
	if recs[0].date.Format("20060102") != "20251231" || recs[0].factor != 1.5 || recs[0].reason != BaselineReasonBootstrap {
		t.Fatalf("bootstrap record = %+v, want 20251231/1.5", recs[0])
	}
}

// 人工改基准：结果按新口径变化，账本留下 previous_*，重放不追加记录。
func TestFetchDailyOHLCV_ManualBaselineChangeIsAuditableAndIdempotent(t *testing.T) {
	bars := []dailyBar{
		{"20260105", 10, 1.0},
		{"20260106", 10, 2.0},
	}
	c := newBarsClient(t, "600519.SH", bars)
	ledger := newMemLedger()
	ledger.records["600519.SH"] = []memRecord{{date: day("20251231"), factor: 6.32, reason: BaselineReasonBootstrap}}
	c.Baselines = ledger

	rows, err := c.FetchDailyOHLCVWithBaseline(context.Background(), "600519.SH", "20260105", "20260106", "2026-01-06")
	if err != nil {
		t.Fatalf("FetchDailyOHLCVWithBaseline: %v", err)
	}
	if got := closeByDate(rows)["20260105"]; got != 5 { // 10 × 1.0 / 2.0
		t.Fatalf("2026-01-05 close = %v, want 5 (新基准 2.0)", got)
	}

	recs := ledger.all("600519.SH")
	if len(recs) != 2 {
		t.Fatalf("ledger records = %d, want 2", len(recs))
	}
	manual := recs[1]
	if manual.reason != BaselineReasonManual || manual.date.Format("20060102") != "20260106" || manual.factor != 2.0 {
		t.Fatalf("manual record = %+v, want 20260106/2.0/manual", manual)
	}
	if manual.prevDate == nil || manual.prevDate.Format("20060102") != "20251231" ||
		manual.prevFactor == nil || *manual.prevFactor != 6.32 {
		t.Fatalf("manual previous_* = %v/%v, want 20251231/6.32", manual.prevDate, manual.prevFactor)
	}

	// 同基准重放：账本不追加（幂等）。
	if _, err := c.FetchDailyOHLCVWithBaseline(context.Background(), "600519.SH", "20260105", "20260106", "20260106"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := len(ledger.all("600519.SH")); got != 2 {
		t.Fatalf("ledger records after replay = %d, want 2 (幂等重放不许追加)", got)
	}
}

func TestFetchDailyOHLCV_ManualBaselineWithoutFactorIsInvalidInput(t *testing.T) {
	bars := []dailyBar{{"20260105", 10, 1.0}, {"20260106", 10, 2.0}}
	c := newBarsClient(t, "600519.SH", bars)
	c.Baselines = newMemLedger()

	// 2026-01-09 没有因子（非交易日/未发布）：不能当基准。
	_, err := c.FetchDailyOHLCVWithBaseline(context.Background(), "600519.SH", "20260105", "20260106", "2026-01-09")
	if !errors.Is(err, ingest.ErrTransportInvalidInput) {
		t.Fatalf("err = %v, want ErrTransportInvalidInput", err)
	}
}
