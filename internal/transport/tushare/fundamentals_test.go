package tushare

import (
	"context"
	"testing"
)

// 契约：按**响应自报 fields** 解包（列序打乱、缺列都不许错位）；`ann_date`
// 原样保留；源端没给的数值列是 nil（缺失≠0）。
func TestFetchFundamentalsRange_ParsesByResponseFields(t *testing.T) {
	c, rec := newClient(t, func(api string) ([]string, [][]any) {
		if api != "fina_indicator" {
			t.Fatalf("unexpected api %q", api)
		}
		// 故意缺 pe/pb/ps（该期全空时源端会剔除），且列序打乱。
		return []string{"end_date", "ts_code", "ann_date", "roe", "revenue"}, [][]any{
			{"20240930", "600036.SH", "20241025", float64(12.5), float64(3.2e11)},
		}
	})

	rows, err := c.FetchFundamentalsRange(context.Background(), "600036.SH", "2020-01-01", "2025-12-31")
	if err != nil {
		t.Fatalf("FetchFundamentalsRange: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Symbol != "600036.SH" {
		t.Fatalf("symbol = %q", row.Symbol)
	}
	if row.Date.Format("2006-01-02") != "2024-09-30" {
		t.Fatalf("report date = %s, want 2024-09-30", row.Date)
	}
	if row.AnnDate == nil || row.AnnDate.Format("2006-01-02") != "2024-10-25" {
		t.Fatalf("ann_date must be preserved: %v", row.AnnDate)
	}
	if row.ROE == nil || *row.ROE != 12.5 {
		t.Fatalf("roe = %v, want 12.5", row.ROE)
	}
	if row.Revenue == nil || *row.Revenue != 3.2e11 {
		t.Fatalf("revenue = %v, want 3.2e11", row.Revenue)
	}
	if row.PE != nil || row.PB != nil || row.PS != nil {
		t.Fatalf("missing columns must stay nil (nil≠0): pe=%v pb=%v ps=%v", row.PE, row.PB, row.PS)
	}

	calls := rec.byAPI("fina_indicator")
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if got := calls[0].Params["start_date"]; got != "20200101" {
		t.Fatalf("start_date = %v, want 20200101", got)
	}
	if got := calls[0].Params["end_date"]; got != "20251231" {
		t.Fatalf("end_date = %v, want 20251231", got)
	}
	if got := calls[0].Params["ts_code"]; got != "600036.SH" {
		t.Fatalf("ts_code = %v", got)
	}
}

// 破坏验证的名场面（DBT-07）：响应缺 `pe` 列时，固定下标会把 roe 写进 pe。
// 这里 roe 必须落在 roe，pe 必须是 nil。
func TestFetchFundamentalsRange_MissingColumnDoesNotShiftValues(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"ts_code", "end_date", "ann_date", "roe"}, [][]any{
			{"000001.SZ", "20250331", "20250425", float64(9.9)},
		}
	})

	rows, err := c.FetchFundamentalsRange(context.Background(), "000001.SZ", "2025-01-01", "2025-12-31")
	if err != nil {
		t.Fatalf("FetchFundamentalsRange: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].PE != nil || rows[0].ROE == nil || *rows[0].ROE != 9.9 {
		t.Fatalf("column shift detected: pe=%v roe=%v", rows[0].PE, rows[0].ROE)
	}
}

// 报告期解析不出来就不猜：该行跳过（报告期是主键的一半）。
func TestFetchFundamentalsRange_SkipsUnparseableReportPeriod(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"ts_code", "end_date", "roe"}, [][]any{
			{"000001.SZ", "", float64(1)},
			{"000001.SZ", "20250331", float64(2)},
		}
	})

	rows, err := c.FetchFundamentalsRange(context.Background(), "000001.SZ", "2025-01-01", "2025-12-31")
	if err != nil {
		t.Fatalf("FetchFundamentalsRange: %v", err)
	}
	if len(rows) != 1 || rows[0].Date.Format("2006-01-02") != "2025-03-31" {
		t.Fatalf("rows = %+v", rows)
	}
}
