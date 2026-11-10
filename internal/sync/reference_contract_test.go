package syncjob

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/store"
	"tellurion-data/internal/transport/tushare"
)

// 本文件是参考数据的**解析契约测试**：以录制的官方响应（testdata/*.json）
// 为输入，走真实 `tushare.Client` → 真实 `store.ReferenceWriter`，断言解析
// 出的结构体与落库参数。fixture 形状来自 2026-11-11 线上核对：
//   - stock_basic：L/D 查询都返回 9 列，空值是空串；
//   - trade_cal：4 列，`is_open` 是 JSON 数字 0/1。

// recordingWriteDB 只记录 Exec，不连库（upsert SQL 的语法由真库腿证明）。
type recordingWriteDB struct {
	beginCalls int
	tx         *recordingWriteTx
}

type recordedExec struct {
	sql  string
	args []any
}

type recordingWriteTx struct {
	execs      []recordedExec
	committed  bool
	rolledBack bool
}

func (d *recordingWriteDB) BeginTx(context.Context) (store.WriteTx, error) {
	d.beginCalls++
	if d.tx == nil {
		d.tx = &recordingWriteTx{}
	}
	return d.tx, nil
}

func (t *recordingWriteTx) Exec(_ context.Context, sql string, args ...any) (store.CommandTag, error) {
	t.execs = append(t.execs, recordedExec{sql: sql, args: append([]any(nil), args...)})
	return recordingTag(1), nil
}

func (t *recordingWriteTx) QueryRow(context.Context, string, ...any) store.Row {
	return recordingRow{}
}

func (t *recordingWriteTx) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *recordingWriteTx) Rollback(context.Context) error {
	t.rolledBack = true
	return nil
}

type recordingTag int

func (t recordingTag) RowsAffected() int64 { return int64(t) }

type recordingRow struct{}

func (recordingRow) Scan(...any) error { return errors.New("recording row: no scan") }

// fixtureServer 忽略请求体，始终返回指定录制的响应。
func fixtureServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(raw); err != nil {
			t.Errorf("write fixture: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newContractRunner(t *testing.T, fixture string) (*ReferenceRunner, *recordingWriteDB) {
	t.Helper()
	srv := fixtureServer(t, fixture)
	client := tushare.New(tushare.Config{Token: "test-token", BaseURL: srv.URL, MaxRetries: 0})
	db := &recordingWriteDB{}
	writer, err := store.NewReferenceWriter(db, store.Options{MarketSchema: "market"})
	if err != nil {
		t.Fatalf("NewReferenceWriter: %v", err)
	}
	return &ReferenceRunner{
		Transport: client,
		Writer:    writer,
		Now:       func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) },
	}, db
}

// 官方 stock_basic 形状 ⇒ 解析出完整结构体，并按列写入 upsert。
func TestReferenceContract_StockBasicFixture(t *testing.T) {
	runner, db := newContractRunner(t, "stock_basic.json")

	// 先直接断言解析出的结构体（不经 writer 转述）。
	stocks, err := runner.Transport.FetchStocks(context.Background(), "", "L")
	if err != nil {
		t.Fatalf("FetchStocks: %v", err)
	}
	if len(stocks) != 3 {
		t.Fatalf("fetched %d stocks, want 3", len(stocks))
	}
	if got := stocks[0]; got.Symbol != "000001.SZ" || got.Name != "平安银行" ||
		got.Exchange != "SZSE" || got.Industry != "银行" || got.Status != "active" ||
		got.ListDate.Format("2006-01-02") != "1991-04-03" || got.DelistDate != nil {
		t.Fatalf("stocks[0] = %+v", got)
	}
	if stocks[1].Exchange != "BSE" {
		t.Fatalf("stocks[1].Exchange = %q, want BSE", stocks[1].Exchange)
	}
	if stocks[2].Status != "delisted" || stocks[2].DelistDate == nil ||
		stocks[2].DelistDate.Format("2006-01-02") != "2020-01-01" {
		t.Fatalf("stocks[2] = %+v delist=%v", stocks[2], stocks[2].DelistDate)
	}

	res, err := runner.Execute(context.Background(), Job{JobType: JobTypeStocks})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Fetched != 3 || res.Persisted != 3 || res.Source != "tushare" {
		t.Fatalf("result = %+v", res)
	}
	if db.beginCalls != 1 || !db.tx.committed || db.tx.rolledBack {
		t.Fatalf("tx state: begin=%d committed=%t rolledBack=%t", db.beginCalls, db.tx.committed, db.tx.rolledBack)
	}
	if len(db.tx.execs) != 3 {
		t.Fatalf("execs = %d, want 3", len(db.tx.execs))
	}
	if !strings.Contains(db.tx.execs[0].sql, "market.stocks") {
		t.Fatalf("sql does not target market.stocks: %s", db.tx.execs[0].sql)
	}

	first := db.tx.execs[0].args
	if first[0] != "000001.SZ" || first[1] != "平安银行" || first[2] != "SZSE" ||
		first[3] != "银行" || first[7] != "active" {
		t.Fatalf("upsert args[0] = %v", first)
	}
	if _, ok := first[5].(time.Time); !ok {
		t.Fatalf("list_date arg = %v (%T), want time.Time", first[5], first[5])
	}
	if first[4] != nil {
		t.Fatalf("market_cap 0 must become NULL, got %v", first[4])
	}
	if first[6] != nil {
		t.Fatalf("active stock delist_date must be NULL, got %v", first[6])
	}
	delisted := db.tx.execs[2].args
	if delisted[7] != "delisted" {
		t.Fatalf("delisted status arg = %v", delisted[7])
	}
	if got, ok := delisted[6].(time.Time); !ok || got.Format("2006-01-02") != "2020-01-01" {
		t.Fatalf("delisted delist_date arg = %v, want 2020-01-01", delisted[6])
	}
}

// 官方 trade_cal 形状 ⇒ 日期/开市标记完整落库。
func TestReferenceContract_TradeCalFixture(t *testing.T) {
	runner, db := newContractRunner(t, "trade_cal.json")

	res, err := runner.Execute(context.Background(), Job{JobType: JobTypeTradingCalendar})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Fetched != 4 || res.Persisted != 4 {
		t.Fatalf("result = %+v", res)
	}
	if len(db.tx.execs) != 4 {
		t.Fatalf("execs = %d, want 4", len(db.tx.execs))
	}
	first := db.tx.execs[0].args
	if got := first[0].(time.Time).Format("2006-01-02"); got != "2026-11-06" {
		t.Fatalf("date arg = %s, want 2026-11-06", got)
	}
	if first[1] != "SSE" || first[2] != true {
		t.Fatalf("calendar args = %v", first)
	}
	if db.tx.execs[1].args[2] != false {
		t.Fatalf("weekend is_trading_day = %v, want false", db.tx.execs[1].args[2])
	}
}

// 改名（delist_date → delist_dt）⇒ fail-closed，且一条 SQL 都不发。
func TestReferenceContract_RenamedDelistFieldFailsClosed(t *testing.T) {
	runner, db := newContractRunner(t, "stock_basic_renamed_delist.json")

	_, err := runner.Execute(context.Background(), Job{JobType: JobTypeStocks})
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
	if db.beginCalls != 0 || (db.tx != nil && len(db.tx.execs) != 0) {
		t.Fatalf("must fail before touching store (begin=%d)", db.beginCalls)
	}
}

// 缺 is_open ⇒ fail-closed，且一条 SQL 都不发。
func TestReferenceContract_MissingIsOpenFailsClosed(t *testing.T) {
	runner, db := newContractRunner(t, "trade_cal_missing_is_open.json")

	_, err := runner.Execute(context.Background(), Job{JobType: JobTypeTradingCalendar})
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
	if db.beginCalls != 0 || (db.tx != nil && len(db.tx.execs) != 0) {
		t.Fatalf("must fail before touching store (begin=%d)", db.beginCalls)
	}
}

// 多余列（act_name / act_ent_type）显式忽略，不影响解析。
func TestReferenceContract_ExtraFieldsAreIgnored(t *testing.T) {
	runner, _ := newContractRunner(t, "stock_basic_extra_fields.json")

	stocks, err := runner.Transport.FetchStocks(context.Background(), "", "L")
	if err != nil {
		t.Fatalf("FetchStocks: %v", err)
	}
	if len(stocks) != 2 {
		t.Fatalf("fetched %d stocks, want 2", len(stocks))
	}
	if stocks[1].Symbol != "600519.SH" || stocks[1].Name != "贵州茅台" || stocks[1].Industry != "白酒" {
		t.Fatalf("stocks[1] = %+v", stocks[1])
	}
}
