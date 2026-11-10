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
	err    error

	gotStockExchange string
	gotStockStatus   string
	gotExchange      string
	gotStart         time.Time
	gotEnd           time.Time
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

type fakeReferenceWriter struct {
	stocks []market.Stock
	days   []store.TradingDay
}

func (f *fakeReferenceWriter) UpsertStocks(_ context.Context, stocks []market.Stock) (int, error) {
	f.stocks = stocks
	return len(stocks), nil
}

func (f *fakeReferenceWriter) UpsertTradingDays(_ context.Context, days []store.TradingDay) (int, error) {
	f.days = days
	return len(days), nil
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
