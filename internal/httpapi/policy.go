package httpapi

import kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

// servedPolicy 是本仓**实际对外**的「路由 × 角色」表（裁-15）。
// `/health` 必须公开（探针不带 token）；数据面读端点按 viewer 起。
//
// `service`（裁-17）：这两条的调用方是 core / analytics，用 `service` 角色的长期
// token，不是人类会话的 `trader`。它就是一个普通 access token 上的角色串
// （kit 只按 kind=access + 角色 any-of 判定），所以本仓不因它升 kit 版本。
//
// `POST /ingest/points` 是**写面**，档位只有 `service` + `admin`：
//   - `service` 是目标态的机器凭证（core 的作业层经它投点）；
//   - `admin` 是今天唯一能签出的那一档 —— core 的 `pkg/auth` 角色集仍是
//     {viewer, trader, admin}，`service` 要能签发是 QT-71 的活。不开 `admin`
//     这条腿今天就不可用；开了的代价是「持 admin token 者能写 A 集表」，
//     在单人自托管实验室里这与「本人能写」是同一件事。
//   - 刻意**不开** viewer/trader：读档位与写档位混在同一档里，将来给写面
//     收紧就说不清谁在什么时候为什么放开的。
var servedPolicy = kithttp.MustNewPolicy(map[string]kithttp.Rule{
	"GET /health":           {Public: true},
	"GET /readyz":           {Public: true},
	"GET /datasets/resolve": {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /datasets/{hash}":  {Roles: []string{"service", "viewer", "trader", "admin"}},
	"POST /ingest/points":   {Roles: []string{"service", "admin"}},

	// ── 证据归档面（2026-11-12，`evidence.go`）──
	// `ingest.raw` 的唯一写者 = data（ADR-0009）。写档位**沿用 REV-17 的
	// `trader|admin`**（迁移不重拍角色）；此前它只在 legacyREV17 里
	// 「权限表有、路由表无」，现在与真实路由对齐。
	// 读面按数据面读档位开放：证据副本可能含业务数据，但落库前已按字段规则脱敏。
	"POST /api/ingest/raw":             {Roles: []string{"trader", "admin"}},
	"GET /api/evidence":                {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /api/evidence/{content_hash}": {Roles: []string{"service", "viewer", "trader", "admin"}},

	// ── G3 只读兼容面（2026-11-10，`legacy_read.go`）──
	// core 旧读链的只读路由。读档位与 `/datasets/*` 一致；
	// `POST /api/v1/ohlcv/bulk` 是**只读 POST**（请求体只承载查询参数，不写库），
	// 沿 REV-17 的 any-authenticated 档——它此前就在 `legacyREV17` 里，落地后
	// 必须移出（守卫测试禁止同一路由同时出现在两张表）。
	// `GET /fundamental/{symbol}` 已于 2026-11-12 删除（归属裁决：基本面是
	// core 自己的表，本服务不再提供 market 读面）。
	"GET /ohlcv/{symbol}":            {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /stocks":                    {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /index/{code}/constituents": {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /api/v1/trading/calendar":   {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /api/v1/stocks/{symbol}":    {Roles: []string{"service", "viewer", "trader", "admin"}},
	"POST /api/v1/ohlcv/bulk":        {AnyAuthenticated: true},

	// ── 作业层（2026-11-10，`sync.go`）──
	// 档位**沿用 REV-17 的结论**（迁移不重拍角色）：排程管理 = admin；
	// 作业创建/取消/重试与「立即跑一次」= trader|admin。
	// 新增的两条只读面（列表/详情）按数据面读档位开放——作业结果不含持仓，
	// 但运维要能看见队列；完全不给读会让「作业卡住了」只能进库查。
	"POST /api/sync/jobs":     {Roles: []string{"trader", "admin"}},
	"GET /api/sync/jobs":      {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /api/sync/jobs/{id}": {Roles: []string{"service", "viewer", "trader", "admin"}},
	// 作业层自省读面（2026-11-12 裁）：队列消费者与排程是「运维看得见」的
	// 一部分，档位与作业列表一致（只读，不含持仓）。进度流是 SSE 长连接，
	// 同样是只读。
	"GET /api/sync/workers":                {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /api/sync/scheduler/stats":        {Roles: []string{"service", "viewer", "trader", "admin"}},
	"GET /api/sync/jobs/{id}/progress":     {Roles: []string{"service", "viewer", "trader", "admin"}},
	"POST /api/sync/jobs/{id}/cancel":      {Roles: []string{"trader", "admin"}},
	"POST /api/sync/jobs/{id}/retry":       {Roles: []string{"trader", "admin"}},
	"POST /api/sync/schedules":             {Roles: []string{"admin"}},
	"GET /api/sync/schedules":              {Roles: []string{"service", "viewer", "trader", "admin"}},
	"PUT /api/sync/schedules/{id}":         {Roles: []string{"admin"}},
	"DELETE /api/sync/schedules/{id}":      {Roles: []string{"admin"}},
	"POST /api/sync/schedules/{id}/toggle": {Roles: []string{"admin"}},
	"POST /api/sync/schedules/{id}/run":    {Roles: []string{"trader", "admin"}},
})

// legacyREV17 是 core `cmd/data/route_policy.go`（REV-17）的**逐条移植**。
//
// core `cmd/data` 已于 2026-11-08（`a96d836`）整体删除，本仓路由表也不再使用
// 这些路径；保留这张表是为了「未来端点迁入时沿用同一档位结论」，不重新拍角色。
// 守卫 `TestLegacyREV17PolicyIsVerbatim` 钉住条目与档位。
//
// 档位映射：Admin → admin；Trader → trader|admin；ReadOnlyPOST → any-authenticated。
var legacyREV17 = map[string]kithttp.Rule{
	// ── ReadOnlyPOST：POST 承载查询 DSL，不写库 ──
	// `POST /api/v1/ohlcv/bulk` 已于 G3（2026-11-10）落地本仓，移入 servedPolicy。
	// 作业层 8 条（schedules CRUD/toggle/run、jobs create/cancel/retry）已于
	// 作业层施工（2026-11-10）落地本仓，同样移入 servedPolicy。
	"POST /screen": {AnyAuthenticated: true},

	// ── Trader：其余全部变更端点 ──
	// `POST /api/ingest/raw` 已于 2026-11-12 落地本仓归档面，移入 servedPolicy
	//（此前是「权限表有、路由表无」的僵尸条目）。
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
