package tushare

import (
	"context"
	"os"
	"testing"
	"time"
)

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
