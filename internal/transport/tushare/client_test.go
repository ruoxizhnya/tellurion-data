package tushare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"tellurion-data/internal/ingest"
)

// fixedNow 是测试的统一时钟（跨年边界上的窗口计算要可复现）。
var fixedNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// recorder 记录本包发出的每一次调用，并允许按 api_name 返回预设响应。
type recorder struct {
	mu    sync.Mutex
	calls []request
}

func (r *recorder) add(req request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
}

func (r *recorder) byAPI(api string) []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []request
	for _, c := range r.calls {
		if c.APIName == api {
			out = append(out, c)
		}
	}
	return out
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// newClient 起一个 httptest 服务端；respond 按 api_name 返回 fields/items。
func newClient(t *testing.T, respond func(api string) (fields []string, items [][]any)) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rec.add(req)

		fields, items := respond(req.APIName)
		if items == nil {
			items = [][]any{}
		}
		resp := map[string]any{
			"code": 0,
			"msg":  "",
			"data": map[string]any{"fields": fields, "items": items},
		}
		if fields == nil {
			resp["data"] = map[string]any{"fields": []string{}, "items": []any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return New(Config{
		Token:      "test-token",
		BaseURL:    srv.URL,
		MaxRetries: 0,
		Now:        func() time.Time { return fixedNow },
	}), rec
}

func TestMacroIndicators_IsSortedAndComplete(t *testing.T) {
	c := New(Config{Token: "x"})
	got := c.MacroIndicators()
	want := []string{
		"CPI_YOY", "LPR_1Y", "LPR_5Y", "M2", "M2_YOY",
		"PMI", "PPI_YOY", "TSF_INC", "USDCNY_CLOSE",
	}
	if len(got) != len(want) {
		t.Fatalf("MacroIndicators() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MacroIndicators()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestFetchStocks_DerivesDelistStatusAndExchange(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		if api != "stock_basic" {
			t.Fatalf("unexpected api %q", api)
		}
		return []string{"ts_code", "symbol", "name", "area", "industry", "market", "list_date", "delist_date", "is_hs"}, [][]any{
			{"000001.SZ", "000001", "平安银行", "深圳", "银行", "主板", "19910403", "", "N"},
			{"600001.SH", "600001", "退市票", "上海", "钢铁", "主板", "19970101", "20200101", "N"},
		}
	})

	stocks, err := c.FetchStocks(context.Background(), "", "L")
	if err != nil {
		t.Fatalf("FetchStocks: %v", err)
	}
	if len(stocks) != 2 {
		t.Fatalf("got %d stocks, want 2", len(stocks))
	}
	if stocks[0].Symbol != "000001.SZ" || stocks[0].Exchange != "SZSE" || stocks[0].Status != "active" {
		t.Fatalf("stock[0] = %+v", stocks[0])
	}
	if stocks[0].ListDate.Format("2006-01-02") != "1991-04-03" {
		t.Fatalf("list date = %v", stocks[0].ListDate)
	}
	if stocks[1].Status != "delisted" || stocks[1].DelistDate == nil {
		t.Fatalf("stock[1] = %+v (delist status/date not derived)", stocks[1])
	}

	calls := rec.byAPI("stock_basic")
	if len(calls) != 1 {
		t.Fatalf("stock_basic calls = %d, want 1", len(calls))
	}
	if calls[0].Token != "test-token" {
		t.Fatalf("token = %q", calls[0].Token)
	}
	if calls[0].Params["list_status"] != "L" {
		t.Fatalf("params = %v", calls[0].Params)
	}
}

func TestFetchDailyOHLCV_AppliesQFQAndUnits(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		switch api {
		case "daily":
			return []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}, [][]any{
				{"600519.SH", "20260105", 10, 11, 9, 10, 1234.5, 5.5},
				{"600519.SH", "20260106", 20, 22, 18, 20, 100, 10},
			}
		case "adj_factor":
			return []string{"ts_code", "trade_date", "adj_factor"}, [][]any{
				{"600519.SH", "20260105", 1.0},
				{"600519.SH", "20260106", 2.0},
			}
		default:
			t.Fatalf("unexpected api %q", api)
			return nil, nil
		}
	})
	c.Baselines = newMemLedger()

	rows, err := c.FetchDailyOHLCV(context.Background(), "600519.SH", "2026-01-05", "2026-01-06")
	if err != nil {
		t.Fatalf("FetchDailyOHLCV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// 首次入库 ⇒ 基准 = 窗口最新因子 2.0 ⇒ 0105 scale 0.5，0106 scale 1.0。
	if rows[0].Close != 5 || rows[0].Open != 5 || rows[0].High != 5.5 || rows[0].Low != 4.5 {
		t.Fatalf("qfq prices = %+v", rows[0])
	}
	if rows[0].Volume != 123450 { // 1234.5 手 × 100
		t.Fatalf("volume = %v, want 123450", rows[0].Volume)
	}
	if rows[0].Turnover != 5500 { // 5.5 千元 × 1000，不取整
		t.Fatalf("turnover = %v, want 5500", rows[0].Turnover)
	}
	if rows[1].Close != 20 || rows[1].Volume != 10000 {
		t.Fatalf("row[1] = %+v", rows[1])
	}

	daily := rec.byAPI("daily")
	if len(daily) != 1 || daily[0].Params["start_date"] != "20260105" || daily[0].Params["end_date"] != "20260106" {
		t.Fatalf("daily call = %+v", daily)
	}
	if len(rec.byAPI("adj_factor")) != 1 {
		t.Fatalf("adj_factor calls = %d, want 1", len(rec.byAPI("adj_factor")))
	}
}

func TestFetchDailyOHLCV_AllRowsSkippedIsDataQuality(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		switch api {
		case "daily":
			return []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}, [][]any{
				{"600519.SH", "20260105", 10, 11, 9, 10, 1, 1},
			}
		case "adj_factor":
			// 因子只覆盖另一个日期：那天不可复权 ⇒ 整段被跳过。
			return []string{"ts_code", "trade_date", "adj_factor"}, [][]any{
				{"600519.SH", "20251231", 1.0},
			}
		}
		return nil, nil
	})
	c.Baselines = newMemLedger()

	_, err := c.FetchDailyOHLCV(context.Background(), "600519.SH", "20260105", "20260105")
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
}

func TestFetchDailyOHLCV_EmptyResponseIsLegalZeroRows(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		switch api {
		case "daily":
			return []string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"}, nil
		case "adj_factor":
			return []string{"ts_code", "trade_date", "adj_factor"}, nil
		}
		return nil, nil
	})
	c.Baselines = newMemLedger()

	rows, err := c.FetchDailyOHLCV(context.Background(), "600519.SH", "20260105", "20260105")
	if err != nil {
		t.Fatalf("FetchDailyOHLCV: %v (empty range must be legal)", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0", len(rows))
	}
}

func TestFetchFundamentals_MissingIsNilNotZero(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		if api != "fina_indicator" {
			t.Fatalf("unexpected api %q", api)
		}
		return []string{"ts_code", "ann_date", "end_date", "pe", "pb", "ps", "roe", "roa", "debt_to_equity", "gross_margin", "net_margin", "revenue", "net_profit", "total_assets", "total_liab"}, [][]any{
			{"600519.SH", "20260425", "20260331", 30.5, nil, nil, 8.1, nil, nil, nil, nil, nil, nil, nil, nil},
		}
	})

	rows, err := c.FetchFundamentals(context.Background(), "600519.SH", "20260425")
	if err != nil {
		t.Fatalf("FetchFundamentals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].PE == nil || *rows[0].PE != 30.5 {
		t.Fatalf("PE = %v, want 30.5", rows[0].PE)
	}
	if rows[0].PB != nil || rows[0].ROA != nil {
		t.Fatalf("missing values must be nil, got PB=%v ROA=%v", rows[0].PB, rows[0].ROA)
	}
	if rows[0].AnnDate == nil || rows[0].AnnDate.Format("20060102") != "20260425" {
		t.Fatalf("AnnDate = %v", rows[0].AnnDate)
	}
	if rows[0].Date.Format("20060102") != "20260331" {
		t.Fatalf("Date = %v", rows[0].Date)
	}
}

func TestFetchDailyBasic_KeepsNilValuesAndSkipsBadRows(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		if api != "daily_basic" {
			t.Fatalf("unexpected api %q", api)
		}
		return []string{"ts_code", "trade_date", "turnover_rate", "turnover_rate_f", "volume_ratio"}, [][]any{
			{"000001.SZ", "20260105", 1.5, 1.6, 0.9},
			{"000002.SZ", "bad-date", 1, 1, 1},
			{"000003.SZ", "20260105", nil, nil, nil},
			{"", "20260105", 1, 1, 1},
		}
	})

	rows, err := c.FetchDailyBasic(context.Background(), "20260105")
	if err != nil {
		t.Fatalf("FetchDailyBasic: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Source != "tushare" || rows[0].AsOf != fixedNow {
		t.Fatalf("row[0] source/asOf = %q/%v", rows[0].Source, rows[0].AsOf)
	}
	if rows[1].TurnoverRate != nil || rows[1].VolumeRatio != nil {
		t.Fatalf("nil values must stay nil (missing≠0), got %+v", rows[1])
	}
}

func TestFetchDailyBasic_MissingColumnIsDataQuality(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		return []string{"ts_code", "trade_date", "turnover_rate"}, [][]any{
			{"000001.SZ", "20260105", 1.5},
		}
	})

	_, err := c.FetchDailyBasic(context.Background(), "20260105")
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
}

func TestFetchDailyBasic_RejectsMalformedDateLocally(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) { return nil, nil })

	_, err := c.FetchDailyBasic(context.Background(), "2026-01-05")
	if !errors.Is(err, ingest.ErrTransportInvalidInput) {
		t.Fatalf("err = %v, want ErrTransportInvalidInput", err)
	}
	if rec.count() != 0 {
		t.Fatalf("upstream calls = %d, want 0 (invalid input must not hit the network)", rec.count())
	}
}

func TestFetchMoneyflowTiers_MapsByColumnNameAndConvertsUnits(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		if api != "moneyflow" {
			t.Fatalf("unexpected api %q", api)
		}
		// 列序故意打乱：按名字定位，不能按固定下标。
		return []string{"trade_date", "ts_code", "sell_elg_amount", "buy_elg_amount", "sell_lg_amount", "buy_lg_amount", "sell_md_amount", "buy_md_amount", "sell_sm_amount", "buy_sm_amount"}, [][]any{
			{"20260105", "600519.SH", 2.5, 10.0, nil, 4.0, 1.0, 1.0, 0.5, 0.2},
		}
	})

	rows, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260105", "20260105")
	if err != nil {
		t.Fatalf("FetchMoneyflowTiers: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.SuperNet == nil || *row.SuperNet != (10.0-2.5)*cnyPerWanYuanUnit {
		t.Fatalf("super net = %v, want %v", row.SuperNet, (10.0-2.5)*cnyPerWanYuanUnit)
	}
	if row.LargeNet != nil {
		t.Fatalf("large net must be nil when one side is missing, got %v", *row.LargeNet)
	}
	if row.MediumNet == nil || *row.MediumNet != 0 {
		t.Fatalf("medium net = %v, want 0 (present and equal)", row.MediumNet)
	}
	if row.SmallNet == nil || *row.SmallNet != (0.2-0.5)*cnyPerWanYuanUnit {
		t.Fatalf("small net = %v", row.SmallNet)
	}
	if got := row.NetByColumn("super_net"); got == nil || *got != *row.SuperNet {
		t.Fatalf("NetByColumn(super_net) = %v", got)
	}
	if row.NetByColumn("nope") != nil {
		t.Fatalf("unknown column must be nil")
	}
}

func TestFetchMoneyflowTiers_RejectsMissingSymbolAndBadDates(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) { return nil, nil })

	if _, err := c.FetchMoneyflowTiers(context.Background(), "  ", "20260105", "20260105"); !errors.Is(err, ingest.ErrTransportInvalidInput) {
		t.Fatalf("empty symbol err = %v, want ErrTransportInvalidInput", err)
	}
	if _, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "2026-01-05", "20260105"); !errors.Is(err, ingest.ErrTransportInvalidInput) {
		t.Fatalf("bad start err = %v, want ErrTransportInvalidInput", err)
	}
}

func TestFetchMacroIndicators_SharesOneRequestPerAPI(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		if api != "cn_m" {
			t.Fatalf("unexpected api %q", api)
		}
		return []string{"month", "m2", "m2_yoy"}, [][]any{
			{"202601", 100.5, 8.1},
			{"202602", nil, 8.2}, // m2 未发布：M2 少一行、M2_YOY 照常
		}
	})

	rows, err := c.FetchMacroIndicators(context.Background(), "M2", "M2_YOY")
	if err != nil {
		t.Fatalf("FetchMacroIndicators: %v", err)
	}
	if got := len(rec.byAPI("cn_m")); got != 1 {
		t.Fatalf("cn_m calls = %d, want 1 (paired indicators must share one request)", got)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (M2×1 + M2_YOY×2)", len(rows))
	}
	for _, r := range rows {
		if r.ObsDate.Format("2006-01-02") != "2026-01-01" && r.ObsDate.Format("2006-01-02") != "2026-02-01" {
			t.Fatalf("obs date = %v, want first day of month", r.ObsDate)
		}
		if r.Source != "tushare" || r.AsOf != fixedNow {
			t.Fatalf("source/asOf = %q/%v", r.Source, r.AsOf)
		}
	}
}

func TestFetchMacroIndicators_UnknownIndicatorIsInvalidInput(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) { return nil, nil })

	_, err := c.FetchMacroIndicators(context.Background(), "NO_SUCH_INDICATOR")
	if !errors.Is(err, ingest.ErrTransportInvalidInput) {
		t.Fatalf("err = %v, want ErrTransportInvalidInput", err)
	}
	if rec.count() != 0 {
		t.Fatalf("upstream calls = %d, want 0", rec.count())
	}
}

func TestFetchMacroIndicators_AllSkippedIsDataQuality(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		return []string{"month", "nt_val", "nt_yoy"}, [][]any{
			{"202601", 100, nil},
			{"bad-month", 100, 1.2},
		}
	})

	_, err := c.FetchMacroIndicators(context.Background(), "CPI_YOY")
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
}

func TestProbeMacro_SendsOnlyTheFirstWindow(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		if api != "fx_daily" {
			t.Fatalf("unexpected api %q", api)
		}
		return []string{"trade_date", "bid_close"}, [][]any{{"20260105", 7.1}}
	})

	if err := c.ProbeMacro(context.Background(), "USDCNY_CLOSE"); err != nil {
		t.Fatalf("ProbeMacro: %v", err)
	}
	calls := rec.byAPI("fx_daily")
	if len(calls) != 1 {
		t.Fatalf("fx_daily calls = %d, want 1 (probe must not walk every window)", len(calls))
	}
	if calls[0].Params["start_date"] != "20190101" || calls[0].Params["ts_code"] != "USDCNH.FXCM" {
		t.Fatalf("probe params = %v", calls[0].Params)
	}
}

func TestProbeDailyBasic_DoesNotNormalize(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		// 故意缺列：探针只判连通性，不该因形状不符而失败。
		return []string{"ts_code"}, [][]any{{"000001.SZ"}}
	})

	if err := c.ProbeDailyBasic(context.Background(), "20260105"); err != nil {
		t.Fatalf("ProbeDailyBasic: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("calls = %d, want 1", rec.count())
	}
}

func TestCall_RetriesOnServerError(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"fields": []string{"month", "nt_val", "nt_yoy"}, "items": []any{}},
		})
	}))
	defer srv.Close()

	c := New(Config{Token: "t", BaseURL: srv.URL, MaxRetries: 1, Now: func() time.Time { return fixedNow }})
	if _, err := c.FetchMacroIndicators(context.Background(), "CPI_YOY"); err != nil {
		t.Fatalf("FetchMacroIndicators after retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestCall_SurfacesAPIErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 40203, "msg": "frequency limited"})
	}))
	defer srv.Close()

	c := New(Config{Token: "t", BaseURL: srv.URL, MaxRetries: 0})
	if _, err := c.FetchDailyBasic(context.Background(), "20260105"); err == nil {
		t.Fatal("want error on non-zero API code, got nil")
	}
}
