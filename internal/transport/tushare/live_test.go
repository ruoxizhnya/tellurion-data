package tushare

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveDailyOHLCV 是可选的线上日线腿（默认 skip），与 e2e 夹具同口径：
// 同一批标的（600519.SH / 600000.SH）、同一区间起点，证明真实链路仍能取到
// 夹具覆盖的那段日线。CI 不跑它；夹具的可重复性见
// `tellurion-ui:e2e/fixtures/market_e2e.sql` 的头注释。
func TestLiveDailyOHLCV(t *testing.T) {
	if os.Getenv("TUSHARE_ONLINE") != "1" {
		t.Skip("set TUSHARE_ONLINE=1 to hit api.tushare.pro")
	}
	token := os.Getenv("TUSHARE_TOKEN")
	if token == "" {
		t.Skip("TUSHARE_TOKEN is required")
	}

	c := NewDefault(token)
	// 线上腿同样需要账本（ADR-0011）；这里用内存替身，不碰任何库。
	c.Baselines = newMemLedger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, symbol := range []string{"600519.SH", "600000.SH"} {
		rows, err := c.FetchDailyOHLCV(ctx, symbol, "20240102", "20240131")
		if err != nil {
			t.Fatalf("FetchDailyOHLCV(%s): %v", symbol, err)
		}
		if len(rows) < 10 {
			t.Fatalf("%s daily rows = %d, want >= 10 (~20 trading days in Jan 2024)", symbol, len(rows))
		}
		for _, r := range rows {
			if r.Symbol != symbol {
				t.Fatalf("row symbol = %q, want %q", r.Symbol, symbol)
			}
			if r.Close <= 0 {
				t.Fatalf("non-positive close: %+v", r)
			}
		}
	}
}

// TestLiveReferenceShapes 是可选的线上形状核对（默认 skip）。
//
// 跑法：`$env:TUSHARE_ONLINE="1"; go test ./internal/transport/tushare -run TestLiveReferenceShapes -v`
// （需要 TUSHARE_TOKEN）。它证明的是**真实响应能被解析**，不是「fixture 能解析」。
func TestLiveReferenceShapes(t *testing.T) {
	if os.Getenv("TUSHARE_ONLINE") != "1" {
		t.Skip("set TUSHARE_ONLINE=1 to hit api.tushare.pro")
	}
	token := os.Getenv("TUSHARE_TOKEN")
	if token == "" {
		t.Skip("TUSHARE_TOKEN is required")
	}

	c := NewDefault(token)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	listed, err := c.FetchStocks(ctx, "", "L")
	if err != nil {
		t.Fatalf("FetchStocks(L): %v", err)
	}
	if len(listed) < 1000 {
		t.Fatalf("listed stocks = %d, want >= 1000", len(listed))
	}
	exchanges := map[string]int{}
	for _, s := range listed {
		if s.Symbol == "" {
			t.Fatalf("empty symbol in listed stocks: %+v", s)
		}
		exchanges[s.Exchange]++
	}
	for _, ex := range []string{"SSE", "SZSE", "BSE"} {
		if exchanges[ex] == 0 {
			t.Errorf("no %s stocks parsed; exchange histogram = %v", ex, exchanges)
		}
	}

	delisted, err := c.FetchStocks(ctx, "", "D")
	if err != nil {
		t.Fatalf("FetchStocks(D): %v", err)
	}
	if len(delisted) == 0 {
		t.Fatalf("delisted stocks = 0, expected historical delistings")
	}
	if delisted[0].DelistDate == nil || delisted[0].Status != "delisted" {
		t.Fatalf("delisted[0] = %+v (delist date/status not derived)", delisted[0])
	}

	days, err := c.FetchTradeCalendar(ctx, "SSE",
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchTradeCalendar: %v", err)
	}
	if len(days) != 10 {
		t.Fatalf("trade_cal days = %d, want 10", len(days))
	}
	// 源侧按日期倒序返回；不要假设顺序，只核 11-09 是交易日。
	var openOn1109 bool
	for _, d := range days {
		if d.Date.Format("2006-01-02") == "2026-11-09" && d.IsTradingDay {
			openOn1109 = true
		}
	}
	if !openOn1109 {
		t.Fatalf("2026-11-09 must be a trading day: %+v", days)
	}
}

// TestLiveIndexConstituents 是可选的线上真链路（默认 skip，需配额）。
//
// 跑法：`$env:TUSHARE_ONLINE="1"; go test ./internal/transport/tushare -run TestLiveIndexConstituents -v`
// （需要 TUSHARE_TOKEN 且该账号有 `index_weight` 权限）。
//
// 断言的是**真实响应能被归一成「一期当前成分」**：窗口内只保留最大
// trade_date、每行 in_date 等于该期、out_date 为零值（当前成分）。
// 若账号无该接口权限，Tushare 返回空集，本腿会红——这正是我们要的
// fail-closed：没有数据就说没有，不许静默成功。
func TestLiveIndexConstituents(t *testing.T) {
	if os.Getenv("TUSHARE_ONLINE") != "1" {
		t.Skip("set TUSHARE_ONLINE=1 to hit api.tushare.pro")
	}
	token := os.Getenv("TUSHARE_TOKEN")
	if token == "" {
		t.Skip("TUSHARE_TOKEN is required")
	}

	c := NewDefault(token)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	end := time.Now().UTC()
	start := end.AddDate(0, 0, -120)
	rows, err := c.FetchIndexConstituents(ctx, "000300.SH", start, end)
	if err != nil {
		t.Fatalf("FetchIndexConstituents(000300.SH): %v", err)
	}
	if len(rows) < 250 {
		t.Fatalf("CSI300 constituents = %d, want >= 250", len(rows))
	}

	snapshot := rows[0].InDate
	seen := map[string]bool{}
	for _, row := range rows {
		if row.IndexCode != "000300.SH" {
			t.Fatalf("index_code = %q, want 000300.SH", row.IndexCode)
		}
		if row.Symbol == "" || row.Weight <= 0 {
			t.Fatalf("row missing symbol/weight: %+v", row)
		}
		if !row.InDate.Equal(snapshot) {
			t.Fatalf("mixed snapshot dates: %s vs %s", row.InDate, snapshot)
		}
		if !row.OutDate.IsZero() {
			t.Fatalf("out_date = %s, want zero", row.OutDate)
		}
		if seen[row.Symbol] {
			t.Fatalf("duplicate symbol in one snapshot: %s", row.Symbol)
		}
		seen[row.Symbol] = true
	}
}
