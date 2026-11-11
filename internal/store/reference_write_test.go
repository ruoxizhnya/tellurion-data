package store

import (
	"context"
	"errors"
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
	delist := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	stocks := []market.Stock{
		{Symbol: "000001.SZ", Name: "平安银行", Exchange: "SZSE", Industry: "银行", MarketCap: 0,
			ListDate: time.Date(1991, 4, 3, 0, 0, 0, 0, time.UTC), DelistDate: &delist, Status: "active"},
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
	// industry 有值、market_cap 0 → NULL、list_date / delist_date 有值、status 保持。
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
	if args[6] == nil || args[6].(time.Time).Format("2006-01-02") != "2020-01-01" {
		t.Fatalf("delist_date arg = %v, want 2020-01-01", args[6])
	}
	if args[7] != "active" {
		t.Fatalf("status arg = %v, want active", args[7])
	}
	// 第二行：industry 空 → NULL；status 空 → active；list_date / delist_date 缺失 → NULL。
	second := db.tx.args[1]
	if second[3] != nil {
		t.Fatalf("empty industry must become NULL, got %v", second[3])
	}
	if second[6] != nil {
		t.Fatalf("nil delist_date must become NULL, got %v", second[6])
	}
	if second[7] != "active" {
		t.Fatalf("empty status must default to active, got %v", second[7])
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

// 单交易所假设：一批里混多个交易所必须拒写，且不开事务。
func TestReferenceWriter_UpsertTradingDaysRejectsMixedExchanges(t *testing.T) {
	w, db := newReferenceWriterForTest(t, "market")
	days := []TradingDay{
		{Date: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), Exchange: "SSE", IsTradingDay: false},
		{Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), Exchange: "SZSE", IsTradingDay: true},
	}

	_, err := w.UpsertTradingDays(context.Background(), days)
	if !errors.Is(err, ErrMixedTradingExchanges) {
		t.Fatalf("err = %v, want ErrMixedTradingExchanges", err)
	}
	if db.beginCalls != 0 || len(db.tx.execs) != 0 {
		t.Fatalf("mixed batch must fail before opening a transaction (begin=%d execs=%d)", db.beginCalls, len(db.tx.execs))
	}
}

// 空串/大小写差异归一后仍是同一交易所，不属于混批。
func TestReferenceWriter_UpsertTradingDaysNormalizesExchange(t *testing.T) {
	w, db := newReferenceWriterForTest(t, "market")
	days := []TradingDay{
		{Date: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), Exchange: " sse "},
		{Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), Exchange: "", IsTradingDay: true},
	}

	if _, err := w.UpsertTradingDays(context.Background(), days); err != nil {
		t.Fatalf("UpsertTradingDays: %v", err)
	}
	for i, execArgs := range db.tx.args {
		if got := execArgs[1]; got != "SSE" {
			t.Fatalf("args[%d] exchange = %v, want SSE", i, got)
		}
	}
}

// 整期快照替换：先把这些指数的「当前行」删掉，再插新快照。
// 断言的是「删了什么、插了什么」——这两步只要有一半被删，读面就会把
// 已调出的票算回票池。
func TestReferenceWriter_ReplaceIndexConstituentsClearsCurrentRowsFirst(t *testing.T) {
	w, db := newReferenceWriterForTest(t, "market")
	snapshot := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	rows := []market.IndexConstituent{
		{IndexCode: "000300.SH", Symbol: "600519.SH", InDate: snapshot, Weight: 3.2},
		{IndexCode: "000300.SH", Symbol: "000001.SZ", InDate: snapshot, Weight: 1.1},
		{IndexCode: "000905.SH", Symbol: "600000.SH", InDate: snapshot, Weight: 2.0},
	}

	written, err := w.ReplaceIndexConstituents(context.Background(), rows)
	if err != nil {
		t.Fatalf("ReplaceIndexConstituents: %v", err)
	}
	if written != 3 {
		t.Fatalf("written = %d, want 3", written)
	}
	if !db.tx.committed || db.tx.rolledBack {
		t.Fatalf("tx state: committed=%t rolledBack=%t", db.tx.committed, db.tx.rolledBack)
	}

	// 第一次 exec 是 DELETE（每个指数一条，按出现顺序去重），随后才是 INSERT。
	if len(db.tx.execs) != 5 {
		t.Fatalf("execs = %d, want 2 deletes + 3 inserts: %v", len(db.tx.execs), db.tx.execs)
	}
	for i := 0; i < 2; i++ {
		if !strings.Contains(db.tx.execs[i], "DELETE FROM market.index_constituents") {
			t.Fatalf("execs[%d] is not the snapshot delete: %s", i, db.tx.execs[i])
		}
		if !strings.Contains(db.tx.execs[i], "out_date IS NULL") {
			t.Fatalf("delete must only target current rows: %s", db.tx.execs[i])
		}
	}
	if db.tx.args[0][0] != "000300.SH" || db.tx.args[1][0] != "000905.SH" {
		t.Fatalf("delete codes = %v, %v", db.tx.args[0][0], db.tx.args[1][0])
	}
	for i := 2; i < 5; i++ {
		if !strings.Contains(db.tx.execs[i], "INSERT INTO market.index_constituents") {
			t.Fatalf("execs[%d] is not the snapshot insert: %s", i, db.tx.execs[i])
		}
	}
	// in_date 必须是快照日、out_date 在 SQL 里恒 NULL（当前成分）。
	if got := db.tx.args[2][2]; got.(time.Time).Format("2006-01-02") != "2026-10-31" {
		t.Fatalf("in_date arg = %v, want snapshot date", got)
	}
	if got := db.tx.args[2][3]; got != 3.2 {
		t.Fatalf("weight arg = %v, want 3.2", got)
	}
}

// 空行不写、不开事务；缺 index_code/symbol/in_date 的行拒写，且整批不落库。
func TestReferenceWriter_ReplaceIndexConstituentsValidatesRows(t *testing.T) {
	w, db := newReferenceWriterForTest(t, "market")
	if written, err := w.ReplaceIndexConstituents(context.Background(), nil); err != nil || written != 0 {
		t.Fatalf("empty input = (%d, %v), want (0, nil)", written, err)
	}
	if db.beginCalls != 0 {
		t.Fatalf("empty input must not open a transaction (begin=%d)", db.beginCalls)
	}

	snapshot := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	for name, row := range map[string]market.IndexConstituent{
		"empty index_code": {Symbol: "600519.SH", InDate: snapshot},
		"empty symbol":     {IndexCode: "000300.SH", InDate: snapshot},
		"zero in_date":     {IndexCode: "000300.SH", Symbol: "600519.SH"},
	} {
		_, err := w.ReplaceIndexConstituents(context.Background(), []market.IndexConstituent{row})
		if err == nil {
			t.Fatalf("%s: want error, got nil", name)
		}
	}
	if db.beginCalls != 0 {
		t.Fatalf("invalid rows must fail before opening a transaction (begin=%d)", db.beginCalls)
	}
}
