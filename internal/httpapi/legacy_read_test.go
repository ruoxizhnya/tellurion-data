package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/ruoxizhnya/tellurion-kit/auth"

	"tellurion-data/internal/serving"
)

// fakeLegacy 是 `serving.LegacyReader` 的测试实现：只喂确定值，
// SQL 正确性由 store 侧的装配测试负责，这里钉的是 HTTP 形状与分诊。
type fakeLegacy struct {
	bars         map[string][]market.OHLCV
	stocks       []market.Stock
	constituents []market.IndexConstituent
	calendar     []time.Time
	latestDate   time.Time
	failSymbols  map[string]bool
}

func (f *fakeLegacy) ReadBars(_ context.Context, symbols []string, _, _ time.Time, _ market.Frequency) ([]market.OHLCV, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	if f.failSymbols[symbols[0]] {
		return nil, errors.New("boom")
	}
	return f.bars[symbols[0]], nil
}

func (f *fakeLegacy) TradingDays(_ context.Context, _, _ time.Time) ([]time.Time, error) {
	return f.calendar, nil
}

func (f *fakeLegacy) Stocks(_ context.Context, _ string) ([]market.Stock, error) {
	return f.stocks, nil
}

func (f *fakeLegacy) LatestTradeDate(_ context.Context) (time.Time, bool, error) {
	return f.latestDate, !f.latestDate.IsZero(), nil
}

func (f *fakeLegacy) Stock(_ context.Context, symbol string) (market.Stock, bool, error) {
	for _, s := range f.stocks {
		if s.Symbol == symbol {
			return s, true, nil
		}
	}
	return market.Stock{}, false, nil
}

func (f *fakeLegacy) IndexConstituents(_ context.Context, _ string, _ time.Time) ([]market.IndexConstituent, error) {
	return f.constituents, nil
}

func legacyTestServer(reader serving.LegacyReader) http.Handler {
	// 空 secret ⇒ 开放访问（仅测试），否则 requireAuth 会在 handler 之前 503。
	return New(nil, WithAuthorizer(NewAuthorizer(auth.NewTokenService(auth.Config{}))), WithLegacyReader(reader)).Routes()
}

func newFakeLegacy() *fakeLegacy {
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	return &fakeLegacy{
		bars: map[string][]market.OHLCV{
			"000001.SZ": {{Symbol: "000001.SZ", Date: day, Close: 10.5, Frequency: market.Freq1d}},
		},
		stocks: []market.Stock{
			{Symbol: "000001.SZ", Name: "平安银行", Exchange: "SZ", Status: "active"},
		},
		constituents: []market.IndexConstituent{
			{IndexCode: "000300.SH", Symbol: "000001.SZ", InDate: day},
		},
		calendar:    []time.Time{day},
		latestDate:  day,
		failSymbols: map[string]bool{"BAD.SZ": true},
	}
}

func doLegacy(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dest); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
}

// TestLegacyReadEndpointsShape 逐条钉住 6 条只读兼容端点的成功形状。
//
// `GET /fundamental/{symbol}` 已于 2026-11-12 删除（归属裁决：基本面是 core
// 自己的表，读面在 core 的 `/api/fundamental/{symbol}`）。
func TestLegacyReadEndpointsShape(t *testing.T) {
	h := legacyTestServer(newFakeLegacy())

	t.Run("ohlcv", func(t *testing.T) {
		rec := doLegacy(t, h, http.MethodGet, "/ohlcv/000001.SZ?start_date=20260101&end_date=20261231", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var got struct {
			OHLCV []market.OHLCV `json:"ohlcv"`
		}
		decodeBody(t, rec, &got)
		if len(got.OHLCV) != 1 || got.OHLCV[0].Symbol != "000001.SZ" {
			t.Fatalf("unexpected ohlcv: %+v", got.OHLCV)
		}
	})

	t.Run("stocks", func(t *testing.T) {
		rec := doLegacy(t, h, http.MethodGet, "/stocks?exchange=SZ", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var got struct {
			Stocks     []market.Stock `json:"stocks"`
			LatestDate string         `json:"latest_date"`
		}
		decodeBody(t, rec, &got)
		if len(got.Stocks) != 1 || got.Stocks[0].Name != "平安银行" {
			t.Fatalf("unexpected stocks: %+v", got.Stocks)
		}
		// latest_date 是 2026-11-12 裁的派生字段：core 的 /api/stocks/count
		// 靠它算「最近有数据的交易日」，不新增端点。
		if got.LatestDate != "2026-11-02" {
			t.Fatalf("latest_date = %q, want 2026-11-02", got.LatestDate)
		}
	})

	t.Run("stock", func(t *testing.T) {
		rec := doLegacy(t, h, http.MethodGet, "/api/v1/stocks/000001.SZ", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var got struct {
			Stock market.Stock `json:"stock"`
		}
		decodeBody(t, rec, &got)
		if got.Stock.Symbol != "000001.SZ" {
			t.Fatalf("unexpected stock: %+v", got.Stock)
		}
	})

	t.Run("constituents", func(t *testing.T) {
		rec := doLegacy(t, h, http.MethodGet, "/index/000300.SH/constituents", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var got struct {
			Constituents []market.IndexConstituent `json:"constituents"`
		}
		decodeBody(t, rec, &got)
		if len(got.Constituents) != 1 || got.Constituents[0].Symbol != "000001.SZ" {
			t.Fatalf("unexpected constituents: %+v", got.Constituents)
		}
	})

	t.Run("calendar", func(t *testing.T) {
		rec := doLegacy(t, h, http.MethodGet, "/api/v1/trading/calendar?start=2026-11-01&end=2026-11-02", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var got struct {
			TradingDays []string `json:"trading_days"`
		}
		decodeBody(t, rec, &got)
		if len(got.TradingDays) != 1 || got.TradingDays[0] != "2026-11-02" {
			t.Fatalf("unexpected trading_days: %+v", got.TradingDays)
		}
	})

	t.Run("bulk", func(t *testing.T) {
		body := `{"symbols":["000001.SZ","BAD.SZ"],"start_date":"20260101","end_date":"20261231"}`
		rec := doLegacy(t, h, http.MethodPost, "/api/v1/ohlcv/bulk", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var got struct {
			Results []legacyBulkResult `json:"results"`
		}
		decodeBody(t, rec, &got)
		if len(got.Results) != 2 {
			t.Fatalf("results = %+v", got.Results)
		}
		if got.Results[0].Symbol != "000001.SZ" || len(got.Results[0].OHLCV) != 1 || got.Results[0].Error != "" {
			t.Fatalf("first result = %+v", got.Results[0])
		}
		// 逐票错误不整体失败：200 + 该票 error 非空，另一票照常返回。
		if got.Results[1].Symbol != "BAD.SZ" || got.Results[1].Error == "" {
			t.Fatalf("second result = %+v", got.Results[1])
		}
	})
}

// TestLegacyReadNotFound 钉住 404：core 把「无该票」当缺省而非错误。
func TestLegacyReadNotFound(t *testing.T) {
	h := legacyTestServer(newFakeLegacy())

	for name, target := range map[string]string{
		"stock": "/api/v1/stocks/999999.SZ",
	} {
		t.Run(name, func(t *testing.T) {
			rec := doLegacy(t, h, http.MethodGet, target, "")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if code := errorCodeOf(t, rec.Body.Bytes()); code != "not_found" {
				t.Fatalf("error code = %q, want not_found", code)
			}
		})
	}
}

// TestLegacyReadBadParams 钉住参数分诊：缺失/坏值一律 400，不是 500。
func TestLegacyReadBadParams(t *testing.T) {
	h := legacyTestServer(newFakeLegacy())
	cases := map[string]string{
		"ohlcv missing start":   "/ohlcv/000001.SZ?end_date=20261231",
		"ohlcv bad end":         "/ohlcv/000001.SZ?start_date=20260101&end_date=nope",
		"constituents bad date": "/index/000300.SH/constituents?date=nope",
		"calendar missing end":  "/api/v1/trading/calendar?start=2026-11-01",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doLegacy(t, h, http.MethodGet, target, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("bulk empty symbols", func(t *testing.T) {
		rec := doLegacy(t, h, http.MethodPost, "/api/v1/ohlcv/bulk",
			`{"symbols":[],"start_date":"20260101","end_date":"20261231"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("bulk unknown field", func(t *testing.T) {
		rec := doLegacy(t, h, http.MethodPost, "/api/v1/ohlcv/bulk",
			`{"symbols":["000001.SZ"],"start_date":"20260101","end_date":"20261231","extra":1}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
	})
}

// TestLegacyReadUnwired 钉住未接线形态：端口 nil ⇒ 503 dependency_unavailable。
func TestLegacyReadUnwired(t *testing.T) {
	h := New(nil, WithAuthorizer(NewAuthorizer(auth.NewTokenService(auth.Config{})))).Routes()
	rec := doLegacy(t, h, http.MethodGet, "/stocks", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, rec.Body.Bytes()); code != "dependency_unavailable" {
		t.Fatalf("error code = %q", code)
	}
}
