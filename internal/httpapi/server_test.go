package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/serving"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// ---- fakes：四个 port 的最小实现，只有确定性输出（不读时钟/随机）。 ----

type fakeBars struct {
	freq market.Frequency
}

func (f *fakeBars) ReadBars(_ context.Context, symbols []string, from, to time.Time, frequency market.Frequency) ([]market.OHLCV, error) {
	out := make([]market.OHLCV, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, market.OHLCV{
			Symbol:    s,
			Date:      from,
			Frequency: frequency,
			Open:      1, High: 2, Low: 0.5, Close: 1.5, Volume: 100,
			VisibleAt: from,
		})
	}
	_ = to
	_ = f.freq
	return out, nil
}

type fakeUniverse struct {
	symbols []string
}

func (f *fakeUniverse) ResolveUniverse(_ context.Context, _ serving.UniverseRequest) ([]string, error) {
	return f.symbols, nil
}

type fakeCalendar struct{}

func (fakeCalendar) TradingDays(_ context.Context, from, to time.Time) ([]time.Time, error) {
	out := []time.Time{from}
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out, nil
}

type fakeVersions struct{}

func (fakeVersions) DataVersion(_ context.Context, _ market.Frequency) (string, error) {
	return "tushare-2026.01", nil
}

func newTestAggregator(t *testing.T, universe []string) *serving.Aggregator {
	t.Helper()
	agg, err := serving.New(serving.Options{
		Bars:     &fakeBars{},
		Universe: &fakeUniverse{symbols: universe},
		Calendar: fakeCalendar{},
		Versions: fakeVersions{},
	})
	require.NoError(t, err)
	return agg
}

const resolveQuery = "/datasets/resolve?symbols=600519.SH&from=2026-01-05T00:00:00Z&to=2026-01-09T00:00:00Z&freq=1d&adjustment=qfq&as_of=2026-01-10T00:00:00Z"

// resolve 两次同参数 ⇒ 同 hash（强幂等直达 wire）。
func TestResolveIsIdempotentAcrossRequests(t *testing.T) {
	srv := New(newTestAggregator(t, []string{"600519.SH"}))
	rec1 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusOK, rec1.Code)
	rec2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusOK, rec2.Code)

	var r1, r2 struct {
		Hash string `json:"dataset_hash"`
	}
	require.NoError(t, jsonUnmarshal(rec1.Body.Bytes(), &r1))
	require.NoError(t, jsonUnmarshal(rec2.Body.Bytes(), &r2))
	require.NotEmpty(t, r1.Hash)
	assert.Equal(t, r1.Hash, r2.Hash)
}

// 同一 hash 两次拉取逐位一致（api-schema §3 的判据直达 wire）。
func TestSnapshotSameHashReturnsByteIdenticalBody(t *testing.T) {
	srv := New(newTestAggregator(t, []string{"600519.SH"}))
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resolved struct {
		Hash string `json:"dataset_hash"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &resolved))

	b1 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(b1, httptest.NewRequest(http.MethodGet, "/datasets/"+resolved.Hash, nil))
	require.Equal(t, http.StatusOK, b1.Code)
	b2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(b2, httptest.NewRequest(http.MethodGet, "/datasets/"+resolved.Hash, nil))
	require.Equal(t, http.StatusOK, b2.Code)
	assert.Equal(t, b1.Body.Bytes(), b2.Body.Bytes(), "same hash must return byte-identical body")
	assert.NotEmpty(t, b1.Body.Bytes())
}

// 快照返回内容要过自检：digest 与 metadata 六分量回显在 body 里。
func TestSnapshotBodyCarriesDigestAndMetadata(t *testing.T) {
	srv := New(newTestAggregator(t, []string{"600519.SH"}))
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resolved struct {
		Hash string `json:"dataset_hash"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &resolved))

	got := httptest.NewRecorder()
	srv.Routes().ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/datasets/"+resolved.Hash, nil))
	require.Equal(t, http.StatusOK, got.Code)
	var dataset serving.Dataset
	require.NoError(t, jsonUnmarshal(got.Body.Bytes(), &dataset))
	assert.Equal(t, resolved.Hash, dataset.Hash)
	assert.NotEmpty(t, dataset.Digest)
	assert.Equal(t, "qfq", dataset.Metadata.AdjustmentBasis)
	assert.NotEmpty(t, dataset.Metadata.CalendarHash)
}

// 错误映射：参数缺失 400、票池空 409、hash 不存在 404 —— 分诊口径见
// internal/serving/errors.go 头注释。
func TestErrorMapping(t *testing.T) {
	srv := New(newTestAggregator(t, []string{"600519.SH"}))
	r := srv.Routes()

	cases := []struct {
		name   string
		query  string
		status int
		code   string
	}{
		{"missing as_of", "/datasets/resolve?symbols=600519.SH&from=2026-01-05T00:00:00Z&to=2026-01-09T00:00:00Z&freq=1d&adjustment=qfq", 400, "missing_parameter"},
		{"bad adjustment", "/datasets/resolve?symbols=600519.SH&from=2026-01-05T00:00:00Z&to=2026-01-09T00:00:00Z&freq=1d&adjustment=nonsense&as_of=2026-01-10T00:00:00Z", 400, "invalid_parameter"},
		{"unknown freq", "/datasets/resolve?symbols=600519.SH&from=2026-01-05T00:00:00Z&to=2026-01-09T00:00:00Z&freq=2h&adjustment=qfq&as_of=2026-01-10T00:00:00Z", 400, "invalid_parameter"},
		{"universe ambiguous", "/datasets/resolve?symbols=600519.SH&universe=csi300&from=2026-01-05T00:00:00Z&to=2026-01-09T00:00:00Z&freq=1d&adjustment=qfq&as_of=2026-01-10T00:00:00Z", 400, "universe_ambiguous"},
		{"unknown hash", "/datasets/deadbeef", 404, "snapshot_not_found"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.query, nil))
		require.Equal(t, tc.status, rec.Code, tc.name)
		var body errorBody
		require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &body), tc.name)
		assert.Equal(t, tc.code, body.Error.Code, tc.name)
	}

	// 票池解析为空 ⇒ 409（明确失败，不返回空数据集）。
	empty := New(newTestAggregator(t, nil))
	rec := httptest.NewRecorder()
	empty.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusConflict, rec.Code)
	var body errorBody
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "empty_universe", body.Error.Code)
}

// 裁-14（2026-10-25）：`/factors/{name}@{hash}` 已撤掉。
//
// 判据两条：
//   - 路由表里没有它（ServeMux 的默认 404，而不是带 JSON 错误码的业务 404）；
//   - 响应里不出现 `factor_not_found` —— 常驻 404 会被读成「该数据集没有该因子」。
func TestFactorRouteIsRemoved(t *testing.T) {
	for _, agg := range []*serving.Aggregator{nil, newTestAggregator(t, []string{"600519.SH"})} {
		r := New(agg).Routes()
		for _, path := range []string{"/factors/momentum@deadbeef", "/factors/momentum", "/factors/@x"} {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusNotFound, rec.Code, path)
			assert.NotContains(t, rec.Body.String(), "factor_not_found", path)
			assert.NotContains(t, rec.Body.String(), "dataset_hash", path)
		}
	}
}

// TestRouteTableIsThePublishedSurface 钉住对外路由集合：`/health` + 两条数据面。
// 新增端点必须先改 `docs/api-schema.md`（路由表与契约双向对拍），再加到这里。
func TestRouteTableIsThePublishedSurface(t *testing.T) {
	srv := New(newTestAggregator(t, []string{"600519.SH"}))
	r := srv.Routes()

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	// resolve 缺参数 ⇒ 400（说明路由存在且 handler 在位），不是 404。
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasets/resolve", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 未知 hash ⇒ 业务 404 snapshot_not_found（说明 `/datasets/{hash}` 在位）。
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	var body errorBody
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "snapshot_not_found", body.Error.Code)
}

// 端口未接线：/health 常绿，数据端点 fail-closed 503 dependency_unavailable。
func TestUnwiredAggregatorFailsClosed(t *testing.T) {
	srv := New(nil)
	r := srv.Routes()

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	for _, path := range []string{resolveQuery, "/datasets/deadbeef"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, path)
		var body errorBody
		require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "dependency_unavailable", body.Error.Code, path)
	}
}
