package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"
)

// TestRouteTableEqualsServedPolicy 是本仓的鉴权守卫：实际注册的 3 条路由与
// servedPolicy 必须双向一一对应。漏登策略 / 表里写了不存在的路由都会红。
func TestRouteTableEqualsServedPolicy(t *testing.T) {
	srv := New(nil)
	routes := srv.routePatterns()
	policy := ServedPolicyEntries()

	if len(routes) != 3 {
		t.Fatalf("route table has %d entries, want 3: %v", len(routes), routes)
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

// TestLegacyREV17PolicyIsVerbatim 钉住「逐条移植」：条目与档位必须与旧
// `cmd/data/route_policy.go`（REV-17）一致。端点迁进本仓时按这张表挂闸，
// 不再重新拍一次角色。
func TestLegacyREV17PolicyIsVerbatim(t *testing.T) {
	want := map[string]string{
		// Admin
		"POST /api/sync/schedules":            "admin",
		"PUT /api/sync/schedules/:id":         "admin",
		"DELETE /api/sync/schedules/:id":      "admin",
		"POST /api/sync/schedules/:id/toggle": "admin",
		// ReadOnlyPOST
		"POST /screen":            "any",
		"POST /api/v1/ohlcv/bulk": "any",
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
		"POST /api/sync/jobs":                            "trader",
		"POST /api/sync/jobs/:id/cancel":                 "trader",
		"POST /api/sync/jobs/:id/retry":                  "trader",
		"POST /api/sync/schedules/:id/run":               "trader",
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
func TestLegacyREV17DecisionsMatchOldService(t *testing.T) {
	cases := []struct {
		name, key, role string
		allowed         bool
	}{
		{"证据链写门 viewer", "POST /api/ingest/raw", "viewer", false},
		{"证据链写门 trader", "POST /api/ingest/raw", "trader", true},
		{"全市场同步 viewer", "POST /sync/ohlcv/all", "viewer", false},
		{"全市场同步 trader", "POST /sync/ohlcv/all", "trader", true},
		{"作业创建 trader", "POST /api/sync/jobs", "trader", true},
		{"排程新增 trader", "POST /api/sync/schedules", "trader", false},
		{"排程新增 admin", "POST /api/sync/schedules", "admin", true},
		{"排程删除 admin", "DELETE /api/sync/schedules/:id", "admin", true},
		{"选股 viewer（只读 POST）", "POST /screen", "viewer", true},
		{"批量行情 viewer（只读 POST）", "POST /api/v1/ohlcv/bulk", "viewer", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, ok := LegacyREV17Entries()[tc.key]
			if !ok {
				t.Fatalf("missing legacy policy for %q", tc.key)
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
