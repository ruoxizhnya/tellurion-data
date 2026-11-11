package syncjob

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/store"
)

type fakeReferenceTransport struct {
	stocks []market.Stock
	days   []ingest.TradingDay
	rows   []market.IndexConstituent
	err    error

	gotStockExchange string
	gotStockStatus   string
	gotExchange      string
	gotStart         time.Time
	gotEnd           time.Time
	gotIndexCode     string
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

type fakeReferenceWriter struct {
	stocks []market.Stock
	days   []store.TradingDay
	rows   []market.IndexConstituent
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
