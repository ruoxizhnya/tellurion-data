package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/ruoxizhnya/tellurion-kit/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/serving"
)

// TestProbesArePublicAndReadinessFlips 钉住 G1 ⑥：`/health` = 进程活着，
// `/readyz` = 接线就绪；两者都不鉴权、不限流。
func TestProbesArePublicAndReadinessFlips(t *testing.T) {
	// 没接库（readiness 未注入）⇒ /health 200、/readyz 503。
	unwired := New(nil, WithAuthorizer(allowAllAuthorizer{})).Routes()

	rec := httptest.NewRecorder()
	unwired.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	unwired.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var notReady readyBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &notReady))
	assert.Equal(t, "not_ready", notReady.Status)
	assert.Equal(t, "unwired", notReady.Store)
	assert.NotEmpty(t, notReady.Error)

	// 注入通过的就绪检查 ⇒ 200。
	ok := New(nil,
		WithAuthorizer(allowAllAuthorizer{}),
		WithReadiness(func(context.Context) error { return nil }),
	).Routes()
	rec = httptest.NewRecorder()
	ok.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var ready readyBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ready))
	assert.Equal(t, "ready", ready.Status)
	assert.Equal(t, "postgres", ready.Store)

	// 库不可达（检查返回错误）⇒ 503，且把原因带回报文。
	down := New(nil,
		WithAuthorizer(allowAllAuthorizer{}),
		WithReadiness(func(context.Context) error { return errors.New("postgres: connection refused") }),
	).Routes()
	rec = httptest.NewRecorder()
	down.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &notReady))
	assert.Equal(t, "postgres", notReady.Store)
	assert.Contains(t, notReady.Error, "connection refused")

	// 鉴权开启（真 kit）但探针仍公开：无 token 可达，不返回 401。
	svc := auth.NewTokenService(auth.Config{JWTSecret: []byte("data-probe-test")})
	gated := New(nil, WithAuthorizer(NewAuthorizer(svc))).Routes()
	for _, path := range []string{"/health", "/readyz"} {
		rec = httptest.NewRecorder()
		gated.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, path)
		assert.NotEqual(t, http.StatusForbidden, rec.Code, path)
	}

	// 闸未装配时探针也必须可用：鉴权配置坏了不等于进程死了。
	noAuth := New(nil, WithReadiness(func(context.Context) error { return nil })).Routes()
	for _, path := range []string{"/health", "/readyz"} {
		rec = httptest.NewRecorder()
		noAuth.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, rec.Code, path)
	}
}

// TestRateLimitDistinctFromAuthAndProbesExempt 钉住 G1 ⑤：429 与 401 可区分，
// 探针不占限流桶。
func TestRateLimitDistinctFromAuthAndProbesExempt(t *testing.T) {
	// allowAll 的身份固定为 test-user；突发 1 ⇒ 第二次即 429。
	limited := New(nil,
		WithAuthorizer(allowAllAuthorizer{}),
		WithRateLimit(0.0001, 1),
	).Routes()

	rec := httptest.NewRecorder()
	limited.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "第一次应过闸到 handler")

	rec = httptest.NewRecorder()
	limited.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "rate_limited", body.Error.Code)

	// 探针豁免：同一进程里连打 5 次，不会被 429。
	for i := 0; i < 5; i++ {
		rec = httptest.NewRecorder()
		limited.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, rec.Code, "probe %d", i)
	}

	// 鉴权先于限流：无 token 连打三次仍 401，不会被 429 顶替。
	svc := auth.NewTokenService(auth.Config{JWTSecret: []byte("data-limit-test")})
	gated := New(nil,
		WithAuthorizer(NewAuthorizer(svc)),
		WithRateLimit(0.0001, 1),
	).Routes()
	for i := 0; i < 3; i++ {
		rec = httptest.NewRecorder()
		gated.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i)
	}
}

// TestEmptyDataVersionMapsToConflict 钉住错误码闭合表的一行：数据版本为空
// 曾经落到 500 `internal`（未登记），现在必须 409 `empty_data_version`。
func TestEmptyDataVersionMapsToConflict(t *testing.T) {
	agg, err := serving.New(serving.Options{
		Bars:     &fakeBars{},
		Universe: &fakeUniverse{symbols: []string{"600519.SH"}},
		Calendar: fakeCalendar{},
		Versions: emptyVersion{},
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	newServer(agg).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "empty_data_version", body.Error.Code)
}

type emptyVersion struct{}

func (emptyVersion) DataVersion(context.Context, market.Frequency) (string, error) { return "", nil }
