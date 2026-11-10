package tushare

import (
	"context"
	"errors"
	"testing"
	"time"

	"tellurion-data/internal/ingest"
)

func TestFetchTradeCalendar_ParsesDatesAndOpenFlag(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		if api != "trade_cal" {
			t.Fatalf("unexpected api %q", api)
		}
		return []string{"exchange", "cal_date", "is_open", "pretrade_date"}, [][]any{
			{"SSE", "20261106", 1, "20261105"},
			{"SSE", "20261107", 0, "20261106"},
			{"SSE", "20261108", "0", "20261106"},
			{"SSE", "20261109", "1", "20261106"},
		}
	})

	start := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	days, err := c.FetchTradeCalendar(context.Background(), "SSE", start, end)
	if err != nil {
		t.Fatalf("FetchTradeCalendar: %v", err)
	}
	if len(days) != 4 {
		t.Fatalf("got %d days, want 4", len(days))
	}
	if days[0].Date.Format("2006-01-02") != "2026-11-06" || !days[0].IsTradingDay {
		t.Fatalf("day[0] = %+v", days[0])
	}
	if days[1].IsTradingDay || days[2].IsTradingDay {
		t.Fatalf("weekend rows must be is_trading_day=false: %+v", days[1:3])
	}
	if !days[3].IsTradingDay {
		t.Fatalf("string \"1\" must parse as open: %+v", days[3])
	}

	calls := rec.byAPI("trade_cal")
	if len(calls) != 1 {
		t.Fatalf("trade_cal calls = %d, want 1", len(calls))
	}
	if got := calls[0].Params["start_date"]; got != "20261106" {
		t.Fatalf("start_date = %v, want 20261106", got)
	}
	if got := calls[0].Params["end_date"]; got != "20261109" {
		t.Fatalf("end_date = %v, want 20261109", got)
	}
}

func TestFetchTradeCalendar_FallsBackToRequestedExchange(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"cal_date", "is_open"}, [][]any{{"20261109", 1}}
	})

	days, err := c.FetchTradeCalendar(context.Background(), "SZSE", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("FetchTradeCalendar: %v", err)
	}
	if len(days) != 1 || days[0].Exchange != "SZSE" {
		t.Fatalf("exchange fallback failed: %+v", days)
	}
}

func TestFetchTradeCalendar_AllRowsUnparsableIsDataQuality(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"exchange", "cal_date", "is_open"}, [][]any{
			{"SSE", "not-a-date", 1},
		}
	})

	_, err := c.FetchTradeCalendar(context.Background(), "SSE", time.Time{}, time.Time{})
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
}

func TestFetchTradeCalendar_EmptyResponseIsZeroRows(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"exchange", "cal_date", "is_open"}, nil
	})

	days, err := c.FetchTradeCalendar(context.Background(), "SSE", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("FetchTradeCalendar: %v", err)
	}
	if len(days) != 0 {
		t.Fatalf("got %d days, want 0", len(days))
	}
}

// 缺 is_open ⇒ 不能把整日静默判成非交易日。
func TestFetchTradeCalendar_MissingIsOpenIsDataQuality(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"exchange", "cal_date", "pretrade_date"}, [][]any{
			{"SSE", "20261109", "20261106"},
		}
	})

	_, err := c.FetchTradeCalendar(context.Background(), "SSE", time.Time{}, time.Time{})
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
}
