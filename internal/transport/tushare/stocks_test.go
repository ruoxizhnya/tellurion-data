package tushare

import (
	"context"
	"errors"
	"testing"

	"tellurion-data/internal/ingest"
)

// 契约：官方字段形状（2026-11-11 在线核对）按**列名**解析；多余列忽略，
// 交易所后缀 .BJ 映射 BSE；退市行由退市日推导 status。
func TestFetchStocks_ParsesByNameAndIgnoresExtraColumns(t *testing.T) {
	c, _ := newClient(t, func(api string) ([]string, [][]any) {
		if api != "stock_basic" {
			t.Fatalf("unexpected api %q", api)
		}
		// 列序故意打乱，并混入本仓不落库的多余列（area / is_hs）。
		return []string{"name", "ts_code", "delist_date", "symbol", "industry", "list_date", "area", "is_hs"}, [][]any{
			{"平安银行", "000001.SZ", "", "000001", "银行", "19910403", "深圳", "N"},
			{"北交所样本", "920085.BJ", "", "920085", "机械基件", "20261106", "山东", "N"},
			{"退市样本", "600001.SH", "20200101", "600001", "钢铁", "19970101", "上海", "N"},
		}
	})

	stocks, err := c.FetchStocks(context.Background(), "", "L")
	if err != nil {
		t.Fatalf("FetchStocks: %v", err)
	}
	if len(stocks) != 3 {
		t.Fatalf("got %d stocks, want 3", len(stocks))
	}
	first := stocks[0]
	if first.Symbol != "000001.SZ" || first.Name != "平安银行" || first.Exchange != "SZSE" ||
		first.Industry != "银行" || first.Status != "active" {
		t.Fatalf("first = %+v", first)
	}
	if first.ListDate.Format("2006-01-02") != "1991-04-03" || first.DelistDate != nil {
		t.Fatalf("first dates = %v / %v", first.ListDate, first.DelistDate)
	}
	if stocks[1].Exchange != "BSE" {
		t.Fatalf("BJ exchange = %q, want BSE", stocks[1].Exchange)
	}
	if stocks[2].Status != "delisted" || stocks[2].DelistDate == nil ||
		stocks[2].DelistDate.Format("2006-01-02") != "2020-01-01" {
		t.Fatalf("delisted = %+v / %v", stocks[2], stocks[2].DelistDate)
	}
}

// 改名/缺列 ⇒ 数据质量哨兵，而不是把所有票静默写成 active。
func TestFetchStocks_MissingDelistDateIsDataQuality(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"ts_code", "symbol", "name", "industry", "list_date"}, [][]any{
			{"000001.SZ", "000001", "平安银行", "银行", "19910403"},
		}
	})

	_, err := c.FetchStocks(context.Background(), "", "L")
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
}

// 有行但日期全不可解析 ⇒ 不能返回「合法 0 行」。
func TestFetchStocks_AllRowsSkippedIsDataQuality(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"ts_code", "symbol", "name", "industry", "list_date", "delist_date"}, [][]any{
			{"000001.SZ", "000001", "平安银行", "银行", "bad-date", ""},
		}
	})

	_, err := c.FetchStocks(context.Background(), "", "L")
	if !errors.Is(err, ingest.ErrTransportDataQuality) {
		t.Fatalf("err = %v, want ErrTransportDataQuality", err)
	}
}

// 真空响应（fields 齐全、items 为空）是合法的 0 行。
func TestFetchStocks_EmptyResponseIsLegalZeroRows(t *testing.T) {
	c, _ := newClient(t, func(string) ([]string, [][]any) {
		return []string{"ts_code", "symbol", "name", "list_date", "delist_date"}, nil
	})

	stocks, err := c.FetchStocks(context.Background(), "", "P")
	if err != nil {
		t.Fatalf("FetchStocks: %v (empty listing status must be legal)", err)
	}
	if len(stocks) != 0 {
		t.Fatalf("got %d stocks, want 0", len(stocks))
	}
}
