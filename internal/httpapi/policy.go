package httpapi

import kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

// servedPolicy 是本仓**实际对外**的「路由 × 角色」表（裁-15）。
// `/health` 必须公开（探针不带 token）；数据面读端点按 viewer 起。
var servedPolicy = kithttp.MustNewPolicy(map[string]kithttp.Rule{
	"GET /health":           {Public: true},
	"GET /datasets/resolve": {Roles: []string{"viewer", "trader", "admin"}},
	"GET /datasets/{hash}":  {Roles: []string{"viewer", "trader", "admin"}},
})

// legacyREV17 是 core `cmd/data/route_policy.go`（REV-17）的**逐条移植**。
//
// 这些端点目前仍由 core 的 `cmd/data` 提供，本仓路由表里没有它们；保留这张表
// 是为了「新旧服务同一路由的鉴权结论一致」——端点迁移进来时按这张表挂闸，
// 不再重新拍一次角色。守卫 `TestLegacyREV17PolicyIsVerbatim` 钉住条目与档位。
//
// 档位映射：Admin → admin；Trader → trader|admin；ReadOnlyPOST → any-authenticated。
var legacyREV17 = map[string]kithttp.Rule{
	// ── Admin：排程管理 ──
	"POST /api/sync/schedules":            {Roles: []string{"admin"}},
	"PUT /api/sync/schedules/:id":         {Roles: []string{"admin"}},
	"DELETE /api/sync/schedules/:id":      {Roles: []string{"admin"}},
	"POST /api/sync/schedules/:id/toggle": {Roles: []string{"admin"}},

	// ── ReadOnlyPOST：POST 承载查询 DSL，不写库 ──
	"POST /screen":            {AnyAuthenticated: true},
	"POST /api/v1/ohlcv/bulk": {AnyAuthenticated: true},

	// ── Trader：其余全部变更端点 ──
	"POST /api/ingest/raw":                           {Roles: []string{"trader", "admin"}},
	"POST /api/ingest/equitydeep":                    {Roles: []string{"trader", "admin"}},
	"POST /sync/stocks":                              {Roles: []string{"trader", "admin"}},
	"POST /sync/ohlcv":                               {Roles: []string{"trader", "admin"}},
	"POST /sync/ohlcv/all":                           {Roles: []string{"trader", "admin"}},
	"POST /sync/fundamental":                         {Roles: []string{"trader", "admin"}},
	"POST /sync/fundamentals":                        {Roles: []string{"trader", "admin"}},
	"POST /sync/dividends":                           {Roles: []string{"trader", "admin"}},
	"POST /sync/splits":                              {Roles: []string{"trader", "admin"}},
	"POST /sync/calendar":                            {Roles: []string{"trader", "admin"}},
	"POST /sync/sectors":                             {Roles: []string{"trader", "admin"}},
	"POST /sync/index-constituents/:index_code":      {Roles: []string{"trader", "admin"}},
	"POST /sync/index-daily/:index_code":             {Roles: []string{"trader", "admin"}},
	"POST /sync/factors/:factor_name":                {Roles: []string{"trader", "admin"}},
	"POST /sync/factors/all":                         {Roles: []string{"trader", "admin"}},
	"POST /api/sync/factor-attribution/:factor_name": {Roles: []string{"trader", "admin"}},
	"POST /api/sync/factor-ic/:factor_name":          {Roles: []string{"trader", "admin"}},
	"POST /api/sync/jobs":                            {Roles: []string{"trader", "admin"}},
	"POST /api/sync/jobs/:id/cancel":                 {Roles: []string{"trader", "admin"}},
	"POST /api/sync/jobs/:id/retry":                  {Roles: []string{"trader", "admin"}},
	"POST /api/sync/schedules/:id/run":               {Roles: []string{"trader", "admin"}},
	"POST /api/v1/cache/warm":                        {Roles: []string{"trader", "admin"}},
}

// ServedPolicyEntries 返回实际生效的策略表副本（守卫测试用）。
func ServedPolicyEntries() map[string]kithttp.Rule { return servedPolicy.Entries() }

// LegacyREV17Entries 返回 REV-17 移植表副本（守卫测试用）。
func LegacyREV17Entries() map[string]kithttp.Rule {
	out := make(map[string]kithttp.Rule, len(legacyREV17))
	for k, v := range legacyREV17 {
		cp := v
		cp.Roles = append([]string(nil), v.Roles...)
		out[k] = cp
	}
	return out
}
