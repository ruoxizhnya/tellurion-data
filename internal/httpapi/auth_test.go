package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouteTableEqualsServedPolicy 是本仓的鉴权守卫：实际注册的 26 条路由与
// servedPolicy 必须双向一一对应。漏登策略 / 表里写了不存在的路由都会红。
func TestRouteTableEqualsServedPolicy(t *testing.T) {
	srv := New(nil)
	routes := srv.routePatterns()
	policy := ServedPolicyEntries()

	// 逐条点名，不是只数条数：加一条没登记策略的路由要红，删一条路由也要红，
	// 而「条数刚好对上但内容换了」这种漂移只有清单能拦住。
	want := []string{
		"DELETE /api/sync/schedules/{id}",
		"GET /api/sync/jobs",
		"GET /api/sync/jobs/{id}",
		"GET /api/sync/jobs/{id}/progress",
		"GET /api/sync/scheduler/stats",
		"GET /api/sync/schedules",
		"GET /api/sync/workers",
		"GET /api/v1/stocks/{symbol}",
		"GET /api/v1/trading/calendar",
		"GET /datasets/resolve",
		"GET /datasets/{hash}",
		"GET /fundamental/{symbol}",
		"GET /health",
		"GET /index/{code}/constituents",
		"GET /ohlcv/{symbol}",
		"GET /readyz",
		"GET /stocks",
		"POST /api/sync/jobs",
		"POST /api/sync/jobs/{id}/cancel",
		"POST /api/sync/jobs/{id}/retry",
		"POST /api/sync/schedules",
		"POST /api/sync/schedules/{id}/run",
		"POST /api/sync/schedules/{id}/toggle",
		"POST /api/v1/ohlcv/bulk",
		"POST /ingest/points",
		"PUT /api/sync/schedules/{id}",
	}
	got := append([]string(nil), routes...)
	sort.Strings(got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("route table = %v, want %v", got, want)
	}
	if len(routes) != len(policy) {
		t.Fatalf("route table has %d entries, policy has %d", len(routes), len(policy))
	}
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		if _, ok := policy[r]; !ok {
			t.Errorf("route %q has no policy entry", r)
		}
		seen[r] = true
	}
	for k := range policy {
		if !seen[k] {
			t.Errorf("policy entry %q has no registered route", k)
		}
	}
}

// 写面的档位单独钉住：读端点对外开放到 viewer，写端点不许跟着一起放开。
func TestServedPolicyWriteRouteRolesAreNarrow(t *testing.T) {
	entries := ServedPolicyEntries()
	rule, ok := entries["POST /ingest/points"]
	require.True(t, ok, "写端点必须有策略条目（缺条目 = 未登记的写面）")
	assert.Equal(t, []string{"service", "admin"}, rule.Roles,
		"写面只开 service（目标态机器凭证）+ admin（今天唯一能签发的档）")
	assert.False(t, rule.Public, "写面永远不公开")
	assert.False(t, rule.AnyAuthenticated, "写面不许 any-authenticated：viewer 也是 authenticated")
}

// TestServedPolicyLegacyReadRoutes 钉住 G3 只读兼容面的档位：
// 6 条 GET 认 service/viewer/trader/admin；`POST /api/v1/ohlcv/bulk` 是只读
// POST，沿用 REV-17 的 any-authenticated（匿名仍拒）。
func TestServedPolicyLegacyReadRoutes(t *testing.T) {
	entries := ServedPolicyEntries()
	for _, key := range []string{
		"GET /ohlcv/{symbol}",
		"GET /fundamental/{symbol}",
		"GET /stocks",
		"GET /index/{code}/constituents",
		"GET /api/v1/trading/calendar",
		"GET /api/v1/stocks/{symbol}",
	} {
		rule, ok := entries[key]
		require.True(t, ok, "G3 读端点必须有策略条目: %s", key)
		for _, role := range []string{"service", "viewer", "trader", "admin"} {
			assert.True(t, allowedByRule(rule, role), "%s must admit role %q", key, role)
		}
		assert.False(t, rule.Public, "%s 不公开", key)
	}

	bulk, ok := entries["POST /api/v1/ohlcv/bulk"]
	require.True(t, ok, "bulk 落地后必须有 servedPolicy 条目")
	assert.True(t, bulk.AnyAuthenticated, "bulk 是只读 POST：保持 any-authenticated")
	assert.False(t, bulk.Public, "bulk 不公开")
	assert.True(t, allowedByRule(bulk, "viewer"), "bulk 允许 viewer（REV-17 口径）")
	assert.False(t, allowedByRule(bulk, ""), "bulk 拒绝匿名")

	// 落地后不得再与 legacy 表重叠（反之 legacy 守卫也会红）。
	_, stillLegacy := LegacyREV17Entries()["POST /api/v1/ohlcv/bulk"]
	assert.False(t, stillLegacy, "bulk 已落地，必须从 legacyREV17 移出")
}

// TestLegacyREV17PolicyIsVerbatim 钉住「逐条移植」：条目与档位必须与旧
// `cmd/data/route_policy.go`（REV-17）一致。端点迁进本仓时按这张表挂闸，
// 不再重新拍一次角色。
func TestLegacyREV17PolicyIsVerbatim(t *testing.T) {
	want := map[string]string{
		// ReadOnlyPOST
		"POST /screen": "any",
		// Trader
		"POST /api/ingest/raw":                           "trader",
		"POST /api/ingest/equitydeep":                    "trader",
		"POST /sync/stocks":                              "trader",
		"POST /sync/ohlcv":                               "trader",
		"POST /sync/ohlcv/all":                           "trader",
		"POST /sync/fundamental":                         "trader",
		"POST /sync/fundamentals":                        "trader",
		"POST /sync/dividends":                           "trader",
		"POST /sync/splits":                              "trader",
		"POST /sync/calendar":                            "trader",
		"POST /sync/sectors":                             "trader",
		"POST /sync/index-constituents/:index_code":      "trader",
		"POST /sync/index-daily/:index_code":             "trader",
		"POST /sync/factors/:factor_name":                "trader",
		"POST /sync/factors/all":                         "trader",
		"POST /api/sync/factor-attribution/:factor_name": "trader",
		"POST /api/sync/factor-ic/:factor_name":          "trader",
		"POST /api/v1/cache/warm":                        "trader",
	}
	got := LegacyREV17Entries()
	if len(got) != len(want) {
		t.Fatalf("legacy REV-17 has %d entries, want %d: %v", len(got), len(want), sortedRouteKeys(got))
	}
	for key, tier := range want {
		rule, ok := got[key]
		if !ok {
			t.Errorf("missing REV-17 entry %q", key)
			continue
		}
		switch tier {
		case "admin":
			if len(rule.Roles) != 1 || rule.Roles[0] != "admin" {
				t.Errorf("%q tier = %+v, want admin", key, rule)
			}
		case "trader":
			if len(rule.Roles) != 2 || rule.Roles[0] != "trader" || rule.Roles[1] != "admin" {
				t.Errorf("%q tier = %+v, want trader|admin", key, rule)
			}
		case "any":
			if !rule.AnyAuthenticated || rule.Public || len(rule.Roles) != 0 {
				t.Errorf("%q tier = %+v, want any-authenticated", key, rule)
			}
		default:
			t.Fatalf("unknown tier %q in expected table", tier)
		}
	}

	// 移植表是迁移期参照，不得与当前实际路由重叠（避免「表里有、路由没有」被误读为已保护）。
	for key := range got {
		if _, served := ServedPolicyEntries()[key]; served {
			t.Errorf("legacy entry %q overlaps the served policy; move it out of legacy when the route lands", key)
		}
	}
}

// TestLegacyREV17DecisionsMatchOldService 用旧服务的行为用例做对拍：
// 同一路由 + 同一角色，新旧策略必须得出同一结论（放行 / 403）。
// 不跑 HTTP——旧端点尚未迁入本仓，这里钉的是**策略结论**一致。
//
// 作业层 8 条已落地（2026-11-10），它们改用 servedPolicy 的 `{id}` 形状对拍；
// 结论必须与 REV-17 那时逐条相同（迁移不重拍角色）。
func TestLegacyREV17DecisionsMatchOldService(t *testing.T) {
	cases := []struct {
		name, key, role string
		allowed         bool
		served          bool
	}{
		{"证据链写门 viewer", "POST /api/ingest/raw", "viewer", false, false},
		{"证据链写门 trader", "POST /api/ingest/raw", "trader", true, false},
		{"全市场同步 viewer", "POST /sync/ohlcv/all", "viewer", false, false},
		{"全市场同步 trader", "POST /sync/ohlcv/all", "trader", true, false},
		{"选股 viewer（只读 POST）", "POST /screen", "viewer", true, false},
		{"作业创建 trader", "POST /api/sync/jobs", "trader", true, true},
		{"作业取消 viewer", "POST /api/sync/jobs/{id}/cancel", "viewer", false, true},
		{"作业重试 trader", "POST /api/sync/jobs/{id}/retry", "trader", true, true},
		{"排程新增 trader", "POST /api/sync/schedules", "trader", false, true},
		{"排程新增 admin", "POST /api/sync/schedules", "admin", true, true},
		{"排程删除 admin", "DELETE /api/sync/schedules/{id}", "admin", true, true},
		{"排程开关 trader", "POST /api/sync/schedules/{id}/toggle", "trader", false, true},
		{"排程立即跑 trader", "POST /api/sync/schedules/{id}/run", "trader", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				rule kithttp.Rule
				ok   bool
			)
			if tc.served {
				rule, ok = ServedPolicyEntries()[tc.key]
			} else {
				rule, ok = LegacyREV17Entries()[tc.key]
			}
			if !ok {
				t.Fatalf("missing policy for %q (served=%t)", tc.key, tc.served)
			}
			if got := allowedByRule(rule, tc.role); got != tc.allowed {
				t.Fatalf("%s as %s = %t, want %t", tc.key, tc.role, got, tc.allowed)
			}
		})
	}
}

func allowedByRule(rule kithttp.Rule, role string) bool {
	if rule.Public {
		return true
	}
	if rule.AnyAuthenticated {
		return role != ""
	}
	for _, r := range rule.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func sortedRouteKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestAuthWireThreeStates 用真 kit TokenService 跑 wire：/health 公开，
// 数据面无凭证 401、viewer 可读、坏 token 401、闸未装配 503。
func TestAuthWireThreeStates(t *testing.T) {
	svc := auth.NewTokenService(auth.Config{JWTSecret: []byte("data-auth-test")})
	if !svc.Enabled() {
		t.Fatal("token service must be enabled")
	}
	srv := New(nil, WithAuthorizer(NewAuthorizer(svc)))
	h := srv.Routes()

	// /health 公开：无 token 也可用。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health must stay public: %d %s", rec.Code, rec.Body.String())
	}

	// 数据面无凭证 / 坏 token ⇒ 401。
	for name, token := range map[string]string{"none": "", "bad": "not-a-jwt"} {
		req := httptest.NewRequest(http.MethodGet, "/datasets/resolve", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// viewer 可读：agg 未接线 ⇒ 业务 503 dependency_unavailable（证明已过鉴权）。
	viewer, _, err := svc.IssueTokens(1, "alice", "viewer")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil)
	req.Header.Set("Authorization", "Bearer "+viewer)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("viewer snapshot: %d %s", rec.Code, rec.Body.String())
	}

	// 闸未装配 ⇒ 数据面 503 auth_unavailable（不是放行）。
	noGate := New(nil).Routes()
	rec = httptest.NewRecorder()
	noGate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no authorizer: %d %s", rec.Code, rec.Body.String())
	}

	// 显式开放（无 secret）⇒ 数据面继续走业务路径（503 dependency_unavailable）。
	open := New(nil, WithAuthorizer(NewAuthorizer(auth.NewTokenService(auth.Config{})))).Routes()
	rec = httptest.NewRecorder()
	open.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("open access: %d %s", rec.Code, rec.Body.String())
	}
}

// TestServedPolicyServiceRole 钉住裁-17：数据面两条读端点接受 `service` 角色
// （调用方是 core / analytics 的长期 token，不是人类会话的 trader），
// 未列进策略表的角色仍 403，`/health` 仍 Public 而不是改成按角色起。
//
// 503 有两种，必须按报文 code 区分：走到 handler 才是 `dependency_unavailable`
// （agg 未接线），闸没装配是 `auth_unavailable`。只看状态码会把「被闸拦下」
// 误读成「已过闸」。
func TestServedPolicyServiceRole(t *testing.T) {
	svc := auth.NewTokenService(auth.Config{JWTSecret: []byte("data-service-role-test")})
	h := New(nil, WithAuthorizer(NewAuthorizer(svc))).Routes()

	serviceToken, _, err := svc.IssueTokens(0, "service:core", "service")
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}
	for _, path := range []string{"/datasets/resolve", "/datasets/deadbeef"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+serviceToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || errorCodeOf(t, rec.Body.Bytes()) != "dependency_unavailable" {
			t.Fatalf("%s as service: code=%d err=%q body=%s, want 503 dependency_unavailable",
				path, rec.Code, errorCodeOf(t, rec.Body.Bytes()), rec.Body.String())
		}
	}

	// 合法签名但角色不在策略表 ⇒ 403：加 service 不是「放开一切 token」。
	other, _, err := svc.IssueTokens(0, "someone", "writer")
	if err != nil {
		t.Fatalf("issue other-role token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/datasets/deadbeef", nil)
	req.Header.Set("Authorization", "Bearer "+other)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || errorCodeOf(t, rec.Body.Bytes()) != "forbidden" {
		t.Fatalf("writer role: code=%d err=%q, want 403 forbidden", rec.Code, errorCodeOf(t, rec.Body.Bytes()))
	}

	// 策略表逐条含 service；/health 保持 Public（静默改回按角色起也红）。
	for _, key := range []string{"GET /datasets/resolve", "GET /datasets/{hash}"} {
		rule, ok := ServedPolicyEntries()[key]
		if !ok {
			t.Fatalf("missing policy for %q", key)
		}
		if !allowedByRule(rule, "service") {
			t.Errorf("%s does not admit role %q: %v", key, "service", rule.Roles)
		}
	}
	health, ok := ServedPolicyEntries()["GET /health"]
	if !ok || !health.Public {
		t.Errorf("GET /health must stay Public (裁-17), got %+v ok=%t", health, ok)
	}
}

func errorCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return payload.Error.Code
}
