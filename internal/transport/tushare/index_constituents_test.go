package tushare

// 指数成分腿的 fixture 测试（不依赖配额）：`index_weight` 的响应形状、
// 「窗口内只取最新一期」的归一规则、以及空响应/缺列必须 fail-closed。
//
// 真链路腿在 `live_test.go`（TUSHARE_ONLINE=1）；这条腿保证 CI 能跑。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// indexWeightServer 起一个假的 tushare 网关，按给定 fields/items 回应
// `index_weight`，并把收到的请求体记下来供断言。
func indexWeightServer(t *testing.T, fields []string, items [][]any) (*Client, *map[string]any) {
	t.Helper()

	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		got["api_name"] = req.APIName
		got["params"] = req.Params
		got["fields"] = req.Fields

		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "",
			"data": map[string]any{"fields": fields, "items": items},
		})
	}))
	t.Cleanup(srv.Close)

	c := New(Config{Token: "test-token", BaseURL: srv.URL, MaxRetries: 0})
	return c, &got
}

// TestFetchIndexConstituents_KeepsLatestSnapshotOnly：窗口里同时有两期时，
// 只保留最大 trade_date；in_date = 该期快照日、out_date 留空、权重按行带出。
func TestFetchIndexConstituents_KeepsLatestSnapshotOnly(t *testing.T) {
	fields := []string{"index_code", "con_code", "trade_date", "weight"}
	items := [][]any{
		{"000300.SH", "600519.SH", "20260930", 3.1},
		{"000300.SH", "000001.SZ", "20260930", 1.0},
		{"000300.SH", "600519.SH", "20261031", 3.2},
		{"000300.SH", "000001.SZ", "20261031", 1.1},
		{"", "600000.SH", "20261031", 0.5}, // index_code 缺列 → 回落入参
	}
	c, got := indexWeightServer(t, fields, items)

	start := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	rows, err := c.FetchIndexConstituents(context.Background(), "000300.SH", start, end)
	if err != nil {
		t.Fatalf("FetchIndexConstituents: %v", err)
	}

	if (*got)["api_name"] != "index_weight" {
		t.Fatalf("api_name = %v, want index_weight", (*got)["api_name"])
	}
	params := (*got)["params"].(map[string]any)
	if params["index_code"] != "000300.SH" {
		t.Fatalf("index_code param = %v", params["index_code"])
	}
	if params["start_date"] != "20260715" || params["end_date"] != "20261112" {
		t.Fatalf("window params = %v..%v", params["start_date"], params["end_date"])
	}

	if len(rows) != 3 {
		t.Fatalf("rows = %d (%+v), want 3 rows of the 20261031 snapshot", len(rows), rows)
	}
	snapshot := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	bySymbol := map[string]float64{}
	for _, row := range rows {
		if !row.InDate.Equal(snapshot) {
			t.Fatalf("%s in_date = %s, want snapshot date %s", row.Symbol, row.InDate, snapshot)
		}
		if !row.OutDate.IsZero() {
			t.Fatalf("%s out_date = %s, want zero (current constituents)", row.Symbol, row.OutDate)
		}
		if row.IndexCode != "000300.SH" {
			t.Fatalf("%s index_code = %q, want fallback 000300.SH", row.Symbol, row.IndexCode)
		}
		bySymbol[row.Symbol] = row.Weight
	}
	if bySymbol["600519.SH"] != 3.2 || bySymbol["000001.SZ"] != 1.1 || bySymbol["600000.SH"] != 0.5 {
		t.Fatalf("weights = %v", bySymbol)
	}
}

// TestFetchIndexConstituents_EmptyResponseFailsClosed：空集不是合法的 0 行。
// 指数代码写错与无该接口权限在 Tushare 侧都是空集；静默成功会让作业看起来
// 绿了而票池永远解析为空。
func TestFetchIndexConstituents_EmptyResponseFailsClosed(t *testing.T) {
	c, _ := indexWeightServer(t, []string{"index_code", "con_code", "trade_date", "weight"}, nil)

	_, err := c.FetchIndexConstituents(context.Background(), "000300.SH", time.Time{}, time.Time{})
	if err == nil {
		t.Fatal("empty index_weight response must be an error, got nil")
	}
}

// TestFetchIndexConstituents_MissingRequiredColumnFailsClosed：契约列缺失时
// 不能靠「全是空串」蒙混过去。
func TestFetchIndexConstituents_MissingRequiredColumnFailsClosed(t *testing.T) {
	c, _ := indexWeightServer(t, []string{"index_code", "trade_date", "weight"}, [][]any{
		{"000300.SH", "20261031", 3.2},
	})

	_, err := c.FetchIndexConstituents(context.Background(), "000300.SH", time.Time{}, time.Time{})
	if err == nil {
		t.Fatal("missing con_code column must be an error, got nil")
	}
}
