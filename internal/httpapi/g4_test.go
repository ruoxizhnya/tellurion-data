package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/assembly"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
)

// G4 ⑤：依赖不可用时数据端点立即 503（不是 500、不是空 200），且门一翻回
// 同一进程就恢复——这就是「恢复后自愈」在 HTTP 层的判据。
func TestDataEndpointsFailClosedWhileDependencyUnavailableAndHeal(t *testing.T) {
	var up atomic.Bool
	sink := &capturedSink{}
	reg, err := assembly.DeclarationsRegistry()
	require.NoError(t, err)
	clock := func() time.Time { return time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC) }
	writer, err := assembly.New(reg, sink, clock)
	require.NoError(t, err)

	srv := New(newTestAggregator(t, []string{"600519.SH"}), WithAuthorizer(allowAllAuthorizer{}),
		WithPoints(writer), WithAvailability(up.Load))
	routes := srv.Routes()

	downResolve := httptest.NewRecorder()
	routes.ServeHTTP(downResolve, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusServiceUnavailable, downResolve.Code, downResolve.Body.String())
	assert.Equal(t, "dependency_unavailable", errorCodeOf(t, downResolve.Body.Bytes()))

	downIngest := postPoints(srv, pointsBody)
	require.Equal(t, http.StatusServiceUnavailable, downIngest.Code, downIngest.Body.String())
	assert.Equal(t, "dependency_unavailable", errorCodeOf(t, downIngest.Body.Bytes()))
	assert.Zero(t, sink.calls, "依赖不可用时不许碰写面")

	// 探针不读库：库挂了 /health 仍然 200，这是「进程活着」与「能接单」的分工。
	health := httptest.NewRecorder()
	routes.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, health.Code, health.Body.String())

	up.Store(true)

	upResolve := httptest.NewRecorder()
	routes.ServeHTTP(upResolve, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusOK, upResolve.Code, upResolve.Body.String())

	upIngest := postPoints(srv, pointsBody)
	require.Equal(t, http.StatusOK, upIngest.Code, upIngest.Body.String())
	assert.Equal(t, 1, sink.calls, "恢复后同一进程应能正常接单")
}

// G4 ①：读路径的 PG 连接失败必须映射成 503 dependency_unavailable，
// 而不是被 writeError 的兜底读成 500。
func TestResolveMapsDependencyUnavailableTo503(t *testing.T) {
	agg, err := serving.New(serving.Options{
		Bars:     &fakeBars{},
		Universe: unavailableUniverse{},
		Calendar: fakeCalendar{},
		Versions: fakeVersions{},
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	newServer(agg).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "dependency_unavailable", errorCodeOf(t, rec.Body.Bytes()))
}

// G4 ③：访问日志是唯一观测出口，字段形态被 runbook 的统计命令依赖。
func TestAccessLogLineCarriesRouteStatusAndDuration(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	rec := httptest.NewRecorder()
	newServer(newTestAggregator(t, []string{"600519.SH"})).Routes().
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resolveQuery, nil))
	require.Equal(t, http.StatusOK, rec.Code)

	line := buf.String()
	assert.Contains(t, line, `httpapi: route="GET /datasets/resolve" status=200 dur_ms=`)
	assert.NotContains(t, line, "dataset_hash", "日志里不许出现报文内容")
}

// 延迟字段不是摆设：慢 handler 必须量出量级（防止有人把 start 取在 handler
// 之后，日志永远是 0）。
func TestAccessLogDurationMeasuresSlowHandler(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	slow := withAccessLog("GET /slow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	slow.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))

	line := buf.String()
	require.Contains(t, line, `route="GET /slow" status=204 dur_ms=`)
	var dur float64
	_, err := fmt.Sscanf(line[strings.Index(line, "dur_ms="):], "dur_ms=%f", &dur)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, dur, 1.0, "2ms 的 handler 至少应量到 1ms：%s", line)
}

// unavailableUniverse 模拟 store 分诊后的 PG 不可达（哨兵穿过 aggregator 的包装）。
type unavailableUniverse struct{}

func (unavailableUniverse) ResolveUniverse(context.Context, serving.UniverseRequest) (serving.UniverseResolution, error) {
	return serving.UniverseResolution{}, fmt.Errorf("serving: resolve universe: %w", store.ErrDependencyUnavailable)
}
