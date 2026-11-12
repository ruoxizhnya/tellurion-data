package tushare

import (
	"context"
	"testing"
)

// 契约：按**响应自报 fields** 解包（列序打乱、缺列都不许错位）；
// `pe_ttm` 必须取自己的列，不能落到 `pe` 上；缺失字段是 nil（缺失≠0）。
func TestFetchDailyBasicRange_MapsPETTMFromItsOwnColumn(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		if api != "daily_basic" {
			t.Fatalf("unexpected api %q", api)
		}
		// 列序故意打乱，且把 pe_ttm 放在 pe 前面——按固定下标必错位。
		return []string{"trade_date", "pe_ttm", "ts_code", "pe", "pb", "total_mv"}, [][]any{
			{"20240102", float64(12.5), "600036.SH", float64(10.1), float64(0.9), float64(1.1e8)},
		}
	})

	rows, err := c.FetchDailyBasicRange(context.Background(), "600036.SH", "2024-01-01", "2025-12-31")
	if err != nil {
		t.Fatalf("FetchDailyBasicRange: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Symbol != "600036.SH" || row.Date.Format("2006-01-02") != "2024-01-02" {
		t.Fatalf("identity = %s/%s", row.Symbol, row.Date)
	}
	if row.PE == nil || *row.PE != 10.1 {
		t.Fatalf("pe = %v, want 10.1", row.PE)
	}
	if row.PETTM == nil || *row.PETTM != 12.5 {
		t.Fatalf("pe_ttm = %v, want 12.5 (must come from its own column)", row.PETTM)
	}
	if row.PB == nil || *row.PB != 0.9 {
		t.Fatalf("pb = %v, want 0.9", row.PB)
	}
	if row.TotalMV == nil || *row.TotalMV != 1.1e8 {
		t.Fatalf("total_mv = %v, want 1.1e8", row.TotalMV)
	}
	if row.PS != nil || row.PSTTM != nil || row.DVRatio != nil || row.CircMV != nil {
		t.Fatalf("missing columns must stay nil (nil≠0): ps=%v ps_ttm=%v dv_ratio=%v circ_mv=%v",
			row.PS, row.PSTTM, row.DVRatio, row.CircMV)
	}

	calls := rec.byAPI("daily_basic")
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if got := calls[0].Params["start_date"]; got != "20240101" {
		t.Fatalf("start_date = %v, want 20240101", got)
	}
	if got := calls[0].Params["end_date"]; got != "20251231" {
		t.Fatalf("end_date = %v, want 20251231", got)
	}
	if got := calls[0].Params["ts_code"]; got != "600036.SH" {
		t.Fatalf("ts_code = %v", got)
	}
}

// 响应缺估值列时，对应字段必须是 nil，不能变成 0。
func TestFetchDailyBasicRange_MissingColumnsStayNil(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"ts_code", "trade_date", "pe"}, [][]any{
			{"000001.SZ", "20240103", float64(5.5)},
		}
	})

	rows, err := c.FetchDailyBasicRange(context.Background(), "000001.SZ", "2024-01-01", "2024-12-31")
	if err != nil {
		t.Fatalf("FetchDailyBasicRange: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].PE == nil || *rows[0].PE != 5.5 {
		t.Fatalf("pe = %v, want 5.5", rows[0].PE)
	}
	if rows[0].PETTM != nil || rows[0].PB != nil || rows[0].TotalMV != nil {
		t.Fatalf("nothing may be fabricated: pe_ttm=%v pb=%v total_mv=%v",
			rows[0].PETTM, rows[0].PB, rows[0].TotalMV)
	}
}

// 交易日不可解析的行跳过（trade_date 是主键的一半）。
func TestFetchDailyBasicRange_SkipsBadTradeDate(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"ts_code", "trade_date", "pe"}, [][]any{
			{"000001.SZ", "", float64(1)},
			{"000001.SZ", "20240104", float64(2)},
		}
	})

	rows, err := c.FetchDailyBasicRange(context.Background(), "000001.SZ", "2024-01-01", "2024-12-31")
	if err != nil {
		t.Fatalf("FetchDailyBasicRange: %v", err)
	}
	if len(rows) != 1 || rows[0].Date.Format("2006-01-02") != "2024-01-04" {
		t.Fatalf("rows = %+v", rows)
	}
}
