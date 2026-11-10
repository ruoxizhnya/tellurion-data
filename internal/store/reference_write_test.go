package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

func newReferenceWriterForTest(t *testing.T, prefix string) (*ReferenceWriter, *fakeWriteDB) {
	t.Helper()
	tx := &fakeWriteTx{affected: []int64{1, 1}}
	db := &fakeWriteDB{tx: tx}
	w, err := NewReferenceWriter(db, Options{MarketSchema: prefix})
	if err != nil {
		t.Fatalf("NewReferenceWriter: %v", err)
	}
	return w, db
}

func TestReferenceWriter_UpsertStocksWritesNullsForUnknowns(t *testing.T) {
	w, db := newReferenceWriterForTest(t, "market")
	stocks := []market.Stock{
		{Symbol: "000001.SZ", Name: "平安银行", Exchange: "SZSE", Industry: "银行", MarketCap: 0,
			ListDate: time.Date(1991, 4, 3, 0, 0, 0, 0, time.UTC), Status: "active"},
		{Symbol: "600001.SH", Name: "退市票", Exchange: "SSE", Status: ""},
	}

	written, err := w.UpsertStocks(context.Background(), stocks)
	if err != nil {
		t.Fatalf("UpsertStocks: %v", err)
	}
	if written != 2 {
		t.Fatalf("written = %d, want 2", written)
	}
	if db.beginCalls != 1 || !db.tx.committed || db.tx.rolledBack {
		t.Fatalf("tx state: begin=%d committed=%t rolledBack=%t", db.beginCalls, db.tx.committed, db.tx.rolledBack)
	}
	if len(db.tx.execs) != 2 {
		t.Fatalf("execs = %d, want 2", len(db.tx.execs))
	}
	if !strings.Contains(db.tx.execs[0], "market.stocks") {
		t.Fatalf("sql does not target market.stocks: %s", db.tx.execs[0])
	}
	// industry 有值、market_cap 0 → NULL、list_date 有值、status 保持。
	args := db.tx.args[0]
	if args[2] != "SZSE" {
		t.Fatalf("exchange arg = %v", args[2])
	}
	if args[4] != nil {
		t.Fatalf("market_cap 0 must become NULL, got %v", args[4])
	}
	if args[5] == nil {
		t.Fatalf("list_date must be preserved")
	}
	// 第二行：industry 空 → NULL；status 空 → active；list_date 零值 → NULL。
	second := db.tx.args[1]
	if second[3] != nil {
		t.Fatalf("empty industry must become NULL, got %v", second[3])
	}
	if second[6] != "active" {
		t.Fatalf("empty status must default to active, got %v", second[6])
	}
	if second[5] != nil {
		t.Fatalf("zero list_date must become NULL, got %v", second[5])
	}
}

func TestReferenceWriter_UpsertStocksEmptyInputIsNoop(t *testing.T) {
	w, db := newReferenceWriterForTest(t, "")

	written, err := w.UpsertStocks(context.Background(), nil)
	if err != nil {
		t.Fatalf("UpsertStocks: %v", err)
	}
	if written != 0 {
		t.Fatalf("written = %d, want 0", written)
	}
	if db.beginCalls != 0 {
		t.Fatalf("empty batch must not open a transaction (begin=%d)", db.beginCalls)
	}
}

func TestReferenceWriter_UpsertTradingDaysDefaultsExchange(t *testing.T) {
	w, db := newReferenceWriterForTest(t, "market")
	days := []TradingDay{
		{Date: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), IsTradingDay: false},
	}

	written, err := w.UpsertTradingDays(context.Background(), days)
	if err != nil {
		t.Fatalf("UpsertTradingDays: %v", err)
	}
	if written != 1 {
		t.Fatalf("written = %d, want 1", written)
	}
	if !strings.Contains(db.tx.execs[0], "market.trading_calendar") {
		t.Fatalf("sql does not target market.trading_calendar: %s", db.tx.execs[0])
	}
	if got := db.tx.args[0][1]; got != "SSE" {
		t.Fatalf("exchange arg = %v, want SSE", got)
	}
	if got := db.tx.args[0][2]; got != false {
		t.Fatalf("is_trading_day arg = %v, want false", got)
	}
}
