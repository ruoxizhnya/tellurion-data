package syncjob

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/store"
)

type fakeReferenceTransport struct {
	stocks       []market.Stock
	days         []ingest.TradingDay
	rows         []market.IndexConstituent
	fundamentals []market.Fundamental
	fundBySymbol map[string][]market.Fundamental
	dailyBasic   []ingest.DailyBasic
	dbBySymbol   map[string][]ingest.DailyBasic
	err          error

	gotStockExchange string
	gotStockStatus   string
	gotExchange      string
	gotStart         time.Time
	gotEnd           time.Time
	gotIndexCode     string
	gotFundSymbol    string
	gotFundStart     string
	gotFundEnd       string
	fundSymbols      []string
	gotDBSymbol      string
	gotDBStart       string
	gotDBEnd         string
	dbSymbols        []string
}

func (f *fakeReferenceTransport) FetchStocks(_ context.Context, exchange, listStatus string) ([]market.Stock, error) {
	f.gotStockExchange = exchange
	f.gotStockStatus = listStatus
	return f.stocks, f.err
}

func (f *fakeReferenceTransport) FetchTradeCalendar(_ context.Context, exchange string, start, end time.Time) ([]ingest.TradingDay, error) {
	f.gotExchange = exchange
	f.gotStart = start
	f.gotEnd = end
	return f.days, f.err
}

func (f *fakeReferenceTransport) FetchIndexConstituents(_ context.Context, indexCode string, start, end time.Time) ([]market.IndexConstituent, error) {
	f.gotIndexCode = indexCode
	f.gotStart = start
	f.gotEnd = end
	return f.rows, f.err
}

func (f *fakeReferenceTransport) FetchFundamentalsRange(_ context.Context, symbol, startPeriod, endPeriod string) ([]market.Fundamental, error) {
	f.gotFundSymbol = symbol
	f.fundSymbols = append(f.fundSymbols, symbol)
	f.gotFundStart = startPeriod
	f.gotFundEnd = endPeriod
	if f.err != nil {
		return nil, f.err
	}
	if rows, ok := f.fundBySymbol[symbol]; ok {
		return rows, nil
	}
	return f.fundamentals, nil
}

func (f *fakeReferenceTransport) FetchDailyBasicRange(_ context.Context, symbol, startDate, endDate string) ([]ingest.DailyBasic, error) {
	f.gotDBSymbol = symbol
	f.dbSymbols = append(f.dbSymbols, symbol)
	f.gotDBStart = startDate
	f.gotDBEnd = endDate
	if f.err != nil {
		return nil, f.err
	}
	if rows, ok := f.dbBySymbol[symbol]; ok {
		return rows, nil
	}
	return f.dailyBasic, nil
}

type fakeReferenceWriter struct {
	stocks       []market.Stock
	days         []store.TradingDay
	rows         []market.IndexConstituent
	fundamentals []market.Fundamental
	dailyBasic   []store.DailyBasic
}

func (f *fakeReferenceWriter) UpsertStocks(_ context.Context, stocks []market.Stock) (int, error) {
	f.stocks = stocks
	return len(stocks), nil
}

func (f *fakeReferenceWriter) UpsertTradingDays(_ context.Context, days []store.TradingDay) (int, error) {
	f.days = days
	return len(days), nil
}

func (f *fakeReferenceWriter) ReplaceIndexConstituents(_ context.Context, rows []market.IndexConstituent) (int, error) {
	f.rows = rows
	return len(rows), nil
}

func (f *fakeReferenceWriter) UpsertFundamentals(_ context.Context, rows []market.Fundamental) (int, error) {
	f.fundamentals = rows
	return len(rows), nil
}

func (f *fakeReferenceWriter) UpsertDailyBasic(_ context.Context, rows []store.DailyBasic) (int, error) {
	f.dailyBasic = rows
	return len(rows), nil
}

func TestReferenceRunner_StocksUsesDefaultsAndReportsCounts(t *testing.T) {
	tr := &fakeReferenceTransport{stocks: []market.Stock{{Symbol: "000001.SZ", Name: "平安银行"}}}
	wr := &fakeReferenceWriter{}
	runner := &ReferenceRunner{Transport: tr, Writer: wr}

	res, err := runner.Execute(context.Background(), Job{JobType: JobTypeStocks})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if tr.gotStockStatus != "L" {
		t.Fatalf("list_status = %q, want L", tr.gotStockStatus)
	}
	if res.Fetched != 1 || res.Persisted != 1 || res.Source != "tushare" {
		t.Fatalf("result = %+v", res)
	}
	if len(wr.stocks) != 1 || wr.stocks[0].Symbol != "000001.SZ" {
		t.Fatalf("writer did not receive stocks: %+v", wr.stocks)
	}
}

// 退市腿：显式 `list_status=D` 必须原样传到传输层，不能被「缺省 L」吞掉。
func TestReferenceRunner_StocksForwardsExplicitListStatus(t *testing.T) {
	tr := &fakeReferenceTransport{stocks: []market.Stock{{Symbol: "600001.SH", Name: "退市样本", Status: "delisted"}}}
	wr := &fakeReferenceWriter{}
	runner := &ReferenceRunner{Transport: tr, Writer: wr}

	res, err := runner.Execute(context.Background(), Job{
		JobType: JobTypeStocks,
		Params:  map[string]any{"list_status": "D"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if tr.gotStockStatus != "D" {
		t.Fatalf("list_status = %q, want D", tr.gotStockStatus)
	}
	if res.Fetched != 1 || res.Persisted != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(wr.stocks) != 1 || wr.stocks[0].Status != "delisted" {
		t.Fatalf("writer did not receive the delisted row: %+v", wr.stocks)
	}
}

func TestReferenceRunner_CalendarDefaultsToCurrentYear(t *testing.T) {
	tr := &fakeReferenceTransport{days: []ingest.TradingDay{{Date: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}}}
	wr := &fakeReferenceWriter{}
	runner := &ReferenceRunner{
		Transport: tr,
		Writer:    wr,
		Now:       func() time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) },
	}

	if _, err := runner.Execute(context.Background(), Job{JobType: JobTypeTradingCalendar}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if tr.gotExchange != "SSE" {
		t.Fatalf("exchange = %q, want SSE", tr.gotExchange)
	}
	if tr.gotStart.Format("2006-01-02") != "2026-01-01" || tr.gotEnd.Format("2006-01-02") != "2026-12-31" {
		t.Fatalf("window = %s..%s", tr.gotStart, tr.gotEnd)
	}
	if len(wr.days) != 1 || wr.days[0].Exchange != "SSE" {
		t.Fatalf("writer did not receive days: %+v", wr.days)
	}
}

func TestReferenceRunner_RejectsReversedWindow(t *testing.T) {
	tr := &fakeReferenceTransport{}
	runner := &ReferenceRunner{Transport: tr, Writer: &fakeReferenceWriter{}}

	_, err := runner.Execute(context.Background(), Job{
		JobType: JobTypeTradingCalendar,
		Params: map[string]any{
			"start_date": "2026-11-10",
			"end_date":   "2026-11-01",
		},
	})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v, want ErrInvalidParams", err)
	}
}

func TestRunner_DispatchesReferenceBeforePointPipeline(t *testing.T) {
	exec := &fakeExecutor{}
	ref := &ReferenceRunner{
		Transport: &fakeReferenceTransport{stocks: []market.Stock{{Symbol: "000001.SZ"}}},
		Writer:    &fakeReferenceWriter{},
	}
	runner := NewRunner(exec)
	runner.Reference = ref

	if _, err := runner.Execute(context.Background(), Job{JobType: JobTypeStocks}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exec.calls != 0 {
		t.Fatalf("point pipeline must not see reference jobs (calls=%d)", exec.calls)
	}
}

func TestRunner_ReferenceJobWithoutRunnerIsMisconfigured(t *testing.T) {
	_, err := NewRunner(&fakeExecutor{}).Execute(context.Background(), Job{JobType: JobTypeStocks})
	if !errors.Is(err, ErrMisconfigured) {
		t.Fatalf("err = %v, want ErrMisconfigured", err)
	}
}

// index_constituents：index_code 必填、默认窗口 = 以 Now 为终点回看 120 天、
// 计数按「归一后的行数」报。
func TestReferenceRunner_IndexConstituentsDefaultsAndReportsCounts(t *testing.T) {
	wr := &fakeReferenceWriter{}
	tr := &fakeReferenceTransport{rows: []market.IndexConstituent{
		{IndexCode: "000300.SH", Symbol: "600519.SH", InDate: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), Weight: 3.2},
		{IndexCode: "000300.SH", Symbol: "000001.SZ", InDate: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), Weight: 1.1},
	}}
	runner := &ReferenceRunner{
		Transport: tr,
		Writer:    wr,
		Now:       func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) },
	}

	res, err := runner.Execute(context.Background(), Job{
		JobType: JobTypeIndexConstituents,
		Params:  map[string]any{"index_code": "000300.SH"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if tr.gotIndexCode != "000300.SH" {
		t.Fatalf("index_code = %q", tr.gotIndexCode)
	}
	if tr.gotStart.Format("2006-01-02") != "2026-07-15" || tr.gotEnd.Format("2006-01-02") != "2026-11-12" {
		t.Fatalf("window = %s..%s, want 2026-07-15..2026-11-12",
			tr.gotStart.Format("2006-01-02"), tr.gotEnd.Format("2006-01-02"))
	}
	if res.Fetched != 2 || res.Persisted != 2 || res.DataType != JobTypeIndexConstituents {
		t.Fatalf("result = %+v", res)
	}
	if len(wr.rows) != 2 || wr.rows[0].Symbol != "600519.SH" {
		t.Fatalf("writer did not receive rows: %+v", wr.rows)
	}
}

// 空 index_code 必须 fail-closed：默认一个指数等于替调用方决定票池。
func TestReferenceRunner_IndexConstituentsRequiresIndexCode(t *testing.T) {
	runner := &ReferenceRunner{Transport: &fakeReferenceTransport{}, Writer: &fakeReferenceWriter{}}
	_, err := runner.Execute(context.Background(), Job{JobType: JobTypeIndexConstituents})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v, want ErrInvalidParams", err)
	}
}

// fundamentals：逐票按报告期拉取、汇总后一次 upsert；`ann_date` 原样透传。
func TestReferenceRunner_FundamentalsAggregatesSymbols(t *testing.T) {
	ann := time.Date(2024, 10, 25, 0, 0, 0, 0, time.UTC)
	roe := 12.5
	tr := &fakeReferenceTransport{fundBySymbol: map[string][]market.Fundamental{
		"600036.SH": {{
			Symbol:  "600036.SH",
			Date:    time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC),
			AnnDate: &ann,
			ROE:     &roe,
		}},
		"300750.SZ": {
			{Symbol: "300750.SZ", Date: time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC)},
			{Symbol: "300750.SZ", Date: time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC), AnnDate: &ann},
		},
	}}
	wr := &fakeReferenceWriter{}
	runner := &ReferenceRunner{Transport: tr, Writer: wr}

	res, err := runner.Execute(context.Background(), Job{
		JobType: JobTypeFundamentals,
		Params: map[string]any{
			"symbols":    []any{"600036.SH", "300750.SZ"},
			"start_date": "2020-01-01",
			"end_date":   "2025-12-31",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Join(tr.fundSymbols, ",") != "600036.SH,300750.SZ" {
		t.Fatalf("fetched symbols = %v", tr.fundSymbols)
	}
	if tr.gotFundStart != "2020-01-01" || tr.gotFundEnd != "2025-12-31" {
		t.Fatalf("period = %s..%s", tr.gotFundStart, tr.gotFundEnd)
	}
	if res.Fetched != 3 || res.Persisted != 3 || res.DataType != JobTypeFundamentals {
		t.Fatalf("result = %+v", res)
	}
	if len(wr.fundamentals) != 3 || wr.fundamentals[0].AnnDate == nil ||
		wr.fundamentals[0].AnnDate.Format("2006-01-02") != "2024-10-25" {
		t.Fatalf("writer did not receive ann_date: %+v", wr.fundamentals)
	}
}

// daily_basic：逐票按交易日区间取估值截面、汇总后一次 upsert。
func TestReferenceRunner_DailyBasicAggregatesSymbols(t *testing.T) {
	pe := 10.0
	peTTM := 12.0
	tr := &fakeReferenceTransport{dbBySymbol: map[string][]ingest.DailyBasic{
		"600036.SH": {{
			Symbol: "600036.SH",
			Date:   time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
			PE:     &pe,
			PETTM:  &peTTM,
		}},
		"300750.SZ": {
			{Symbol: "300750.SZ", Date: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)},
			{Symbol: "300750.SZ", Date: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), PETTM: &peTTM},
		},
	}}
	wr := &fakeReferenceWriter{}
	runner := &ReferenceRunner{Transport: tr, Writer: wr}

	res, err := runner.Execute(context.Background(), Job{
		JobType: JobTypeDailyBasic,
		Params: map[string]any{
			"symbols":    []any{"600036.SH", "300750.SZ"},
			"start_date": "2024-01-01",
			"end_date":   "2024-12-31",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Join(tr.dbSymbols, ",") != "600036.SH,300750.SZ" {
		t.Fatalf("fetched symbols = %v", tr.dbSymbols)
	}
	if tr.gotDBStart != "2024-01-01" || tr.gotDBEnd != "2024-12-31" {
		t.Fatalf("period = %s..%s", tr.gotDBStart, tr.gotDBEnd)
	}
	if res.Fetched != 3 || res.Persisted != 3 || res.DataType != JobTypeDailyBasic {
		t.Fatalf("result = %+v", res)
	}
	if len(wr.dailyBasic) != 3 || wr.dailyBasic[0].PETTM == nil || *wr.dailyBasic[0].PETTM != 12.0 {
		t.Fatalf("writer did not receive pe_ttm: %+v", wr.dailyBasic)
	}
}

// daily_basic：symbols 与交易日区间全部必填；参数不合法时一次请求都不发。
func TestReferenceRunner_DailyBasicRequiresSymbolsAndPeriod(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"no symbols", map[string]any{"start_date": "2024-01-01", "end_date": "2024-12-31"}},
		{"no period", map[string]any{"symbols": []any{"600036.SH"}}},
		{"reversed", map[string]any{"symbols": []any{"600036.SH"}, "start_date": "2024-12-31", "end_date": "2024-01-01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &fakeReferenceTransport{}
			runner := &ReferenceRunner{Transport: tr, Writer: &fakeReferenceWriter{}}
			_, err := runner.Execute(context.Background(), Job{JobType: JobTypeDailyBasic, Params: tc.params})
			if !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("err = %v, want ErrInvalidParams", err)
			}
			if len(tr.dbSymbols) != 0 {
				t.Fatalf("invalid params must not fetch: %v", tr.dbSymbols)
			}
		})
	}
}

// fundamentals：symbols 与报告期区间全部必填；参数不合法时一次请求都不发。
func TestReferenceRunner_FundamentalsRequiresSymbolsAndPeriod(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"no symbols", map[string]any{"start_date": "2020-01-01", "end_date": "2025-12-31"}},
		{"no period", map[string]any{"symbols": []any{"600036.SH"}}},
		{"reversed", map[string]any{"symbols": []any{"600036.SH"}, "start_date": "2025-12-31", "end_date": "2020-01-01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &fakeReferenceTransport{}
			runner := &ReferenceRunner{Transport: tr, Writer: &fakeReferenceWriter{}}
			_, err := runner.Execute(context.Background(), Job{JobType: JobTypeFundamentals, Params: tc.params})
			if !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("err = %v, want ErrInvalidParams", err)
			}
			if len(tr.fundSymbols) != 0 {
				t.Fatalf("invalid params must not fetch: %v", tr.fundSymbols)
			}
		})
	}
}
