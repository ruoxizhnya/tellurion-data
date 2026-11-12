// cmd/dataservice 是数据服务的进程入口。
//
// 装配（G4 起）：`DATABASE_URL` 有值 ⇒ 建连接池（**不拨号**）→ 尝试执行本仓
// schema 的 DDL（`internal/schema.Statements()`，管 `market` / `ingest`）→
// 用 `internal/store` 的四个 port 装出 aggregator。DDL 失败（PG 不可达）
// 不再让进程退出：数据端点保持 503，后台每 10s 重试迁移，成功后同进程自愈。
//
// `DATABASE_URL` 为空 ⇒ 保持 **fail-closed 503**（`/healthz` 常绿）。
// 这不是「能用但没数据」，而是「明确没接线」：接上假实现让 200 看起来正常，
// 比 503 危险得多。
//
// 回填（`schema.BackfillStatements()`）**不在这里跑**：它是一次性重活，必须
// 靠 `data_migrations` 哨兵防重放，且只允许出现在显式启动开关或 tagged 集成
// 套件里（`docs/db-schema.md` §6.5 D2 的实测教训）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

	"tellurion-data/internal/assembly"
	"tellurion-data/internal/evidence"
	"tellurion-data/internal/httpapi"
	"tellurion-data/internal/ingest"
	"tellurion-data/internal/schema"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
	"tellurion-data/internal/sync"
	"tellurion-data/internal/transport/tushare"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	jwtSecret := flag.String("jwt-secret", envFirst("JWT_SECRET", "AUTH_JWT_SECRET"),
		"JWT secret shared with analysis (empty = write surface locked on loopback bind; non-loopback bind refuses to start)")
	exposure := flag.String("insecure-exposure", os.Getenv("AUTH_INSECURE_EXPOSURE"),
		"explicit declaration allowing open access without a secret (only loopback-published is accepted)")
	flag.Parse()

	// 鉴权三态（2026-11-12）：有 secret 全闸；无 secret + 回环绑定默认「读公开、写 503」；
	// 无 secret + 非回环绑定且未声明 ⇒ 拒启；只有逐字声明
	// AUTH_INSECURE_EXPOSURE=loopback-published 才允许任意绑定全放行。
	// 修复前这里把 allowInsecure 硬编码成 true，等价于「没配 secret = 自动开放」。
	posture, err := decideAuthPosture(*jwtSecret, *exposure, *addr)
	if err != nil {
		log.Fatalf("data-service: %v", err)
	}
	var tokens *auth.TokenService
	var authOption httpapi.Option
	switch posture {
	case authEnforced:
		tokens = auth.NewTokenService(auth.Config{JWTSecret: []byte(strings.TrimSpace(*jwtSecret))})
		authOption = httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokens))
	case authLocked:
		tokens = auth.NewTokenService(auth.Config{})
		authOption = httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokens))
		log.Printf("data-service: WARNING authentication unavailable (no JWT_SECRET): read surface public, write surface locked (503 auth_unavailable); set JWT_SECRET, or AUTH_INSECURE_EXPOSURE=%s for local open access", kithttp.InsecureExposureLoopbackPublished)
	case authOpen:
		tokens = auth.NewTokenService(auth.Config{})
		authOption = httpapi.WithAuthorizer(httpapi.NewOpenAuthorizer())
		log.Print(authWarning(*addr))
	}

	srv, cleanup, err := build(authOption)
	if err != nil {
		log.Fatalf("data-service wiring failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	log.Printf("data-service listening on %s (%s, auth=%t)", *addr, statusLine(srv), tokens.Enabled())
	httpServer := &http.Server{
		Addr:    *addr,
		Handler: srv.Routes(),
		// HTTP 面硬边界（G4 ②）。ReadHeader 5s 拦慢速打头；Read 30s 覆盖
		// 8 MiB 上限的投点批次；Write 120s 是**声明值**（未在真实负载下定标），
		// 超过表现为连接中断而不是 5xx；Idle 120s 回收空闲连接。
		// DB 侧另有拨号 5s / 语句 30s（`internal/store`）。
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// SIGTERM/SIGINT ⇒ 优雅停机：先排空 HTTP 在途请求，再停作业循环
	// （`cleanup` 会等在途作业交还，见 `build`）。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("data-service: listen %s: %v", *addr, err)
	}
	if err := runHTTP(ctx, httpServer, ln); err != nil {
		log.Fatalf("data-service: %v", err)
	}
	log.Print("data-service: stopped")
}

// httpShutdownTimeout 是排空在途 HTTP 请求的预算（与 agent 的 30s 对齐）。
const httpShutdownTimeout = 30 * time.Second

// runHTTP 提供服务直到 ctx 取消（信号）或监听失败，然后 `Shutdown` 排空
// 在途请求。抽成函数是为了让「信号 → 排空」这条路径可以被单测直接驱动。
func runHTTP(ctx context.Context, srv *http.Server, ln net.Listener) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		log.Print("data-service: shutdown signal received; draining in-flight HTTP requests")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("data-service: graceful HTTP shutdown failed, forcing close: %v", err)
			_ = srv.Close()
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}
}

// authPosture 是进程的鉴权三态（六仓统一口径，2026-11-12）。
type authPosture int

const (
	authEnforced authPosture = iota // 有 JWT_SECRET：全闸，写面必须带 token + 角色
	authLocked                      // 无 secret + 回环绑定、未声明开放：读公开、写面 503 auth_unavailable
	authOpen                        // 无 secret + 逐字声明 loopback-published：全放行（启动 WARN）
)

// decideAuthPosture 只做三态判定，不碰网络也不读全局配置，便于单测。
//
// 与修复前的差别：无 secret 不再因为 allowInsecure 硬编码 true 而自动放行；
// 只有逐字匹配的显式声明才进入第 3 态。第 2 态与其余五仓同口径：**必须回环绑定**
// ——写面虽然锁死，但无 secret 的数据面不允许绑到全网卡；非回环且未声明一律拒启。
// 未知声明值同样拒绝，不静默落到默认态。
func decideAuthPosture(secret, exposure, addr string) (authPosture, error) {
	if strings.TrimSpace(secret) != "" {
		return authEnforced, nil
	}
	switch strings.TrimSpace(exposure) {
	case "":
		if !isLoopbackAddr(addr) {
			return authLocked, fmt.Errorf("auth: 缺 JWT_SECRET 且未声明 AUTH_INSECURE_EXPOSURE，绑定地址 %q 不是回环；第 2 态只允许回环绑定（例如 -addr 127.0.0.1:8081 或 localhost:8081）；容器请注入 JWT_SECRET，或显式声明 AUTH_INSECURE_EXPOSURE=%s", addr, kithttp.InsecureExposureLoopbackPublished)
		}
		return authLocked, nil
	case kithttp.InsecureExposureLoopbackPublished:
		return authOpen, nil
	default:
		return authLocked, fmt.Errorf("auth: AUTH_INSECURE_EXPOSURE=%q is not recognized; the only accepted value is %q (or set JWT_SECRET)", exposure, kithttp.InsecureExposureLoopbackPublished)
	}
}

// authWarning 是第 3 态启动 WARN 的文本：**声明值**与**实际绑定地址**分开写；
// 实际绑定非回环时额外点名「已对外暴露」，避免运维只看声明值就以为发布层
// 只在回环（容器里 `loopback-published` 常配 `-addr 0.0.0.0`，靠 ports 前缀收口）。
func authWarning(addr string) string {
	msg := fmt.Sprintf("data-service: WARNING 写面无鉴权：write endpoints accept unauthenticated requests; 实际绑定 bind address=%q; 声明 AUTH_INSECURE_EXPOSURE=%s (声明值，不等于实际绑定)", addr, kithttp.InsecureExposureLoopbackPublished)
	if !isLoopbackAddr(addr) {
		msg += "；绑定非回环，已对外暴露 (non-loopback bind)"
	}
	return msg
}

// bindHost 从 -addr 取绑定主机；取不到端口时原样当主机比（空 = 全网卡）。
func bindHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// isLoopbackAddr 判定 -addr 是否只绑回环：localhost 或 IP 回环地址。
// 空主机（":8081"）、0.0.0.0、:: 都算非回环。
func isLoopbackAddr(addr string) bool {
	host := bindHost(strings.TrimSpace(addr))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// envFirst 按顺序取第一个非空环境变量（core 历史用法 JWT_SECRET、配置口径 AUTH_JWT_SECRET）。
func envFirst(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func statusLine(srv *httpapi.Server) string {
	read := "data ports unwired: /datasets/* -> 503"
	if srv.HasAggregator() {
		read = "data ports wired to PG"
	}
	write := "write面 unwired: POST /ingest/points -> 503"
	if srv.HasPointsWriter() {
		write = "write面 wired to PG"
	}
	return read + "; " + write
}

// 迁移重试的节奏（G4 ⑤）：启动那次失败不算致命，后台每 10s 再试一次，
// 成功后数据端点从 503 翻成正常，进程不需要重启。
const (
	schemaStartupTimeout = 30 * time.Second
	schemaRetryInterval  = 10 * time.Second
)

// build 装配 HTTP 面。DSN 为空时返回 nil aggregator 与 nil 写面（503 形态）。
// authOption 由 main 按鉴权三态选定（enforced / locked / open），本函数只负责装上。
func build(authOption httpapi.Option) (*httpapi.Server, func(), error) {
	authorizer := authOption
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return httpapi.New(nil, authorizer), nil, nil
	}

	// 保留期口径在启动期校验：0 / 负数 / 非整数一律拒起，不允许「关掉保留期」
	// 隐式生效（`docs/db-schema.md` §9）。
	retentionDays, err := evidence.RetentionDays(os.Getenv("EVIDENCE_RETENTION_DAYS"))
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), schemaStartupTimeout)
	defer cancel()

	pool, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}

	// 降级启动（G4 ⑤）：DDL 失败不退出进程。数据端点由 availability 门
	// 立即回 503（不逐个等连接超时），后台重试成功后自愈。
	var schemaReady atomic.Bool
	retryCtx, stopRetry := context.WithCancel(context.Background())
	if err := applySchema(ctx, pool); err != nil {
		log.Printf("data-service: schema not applied yet (data endpoints stay 503, retry every %s): %v", schemaRetryInterval, err)
		go retrySchema(retryCtx, pool, &schemaReady)
	} else {
		schemaReady.Store(true)
	}

	opts := store.Options{MarketSchema: os.Getenv("DATA_MARKET_SCHEMA")}
	st, err := store.New(store.PoolQuerier(pool), opts)
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}
	agg, err := serving.New(serving.Options{
		Bars:     st,
		Universe: st,
		Calendar: st,
		Versions: st,
	})
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}

	// 写面装配：声明来自注册表（闸门要知道每个 data_type 的锚点列名），
	// 落库走 `store.Writer`。
	writer, err := store.NewWriter(store.PoolWriteDB(pool), opts)
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}

	// 证据归档面（ADR-0009）：`ingest.raw` 的唯一写者与读面都在本进程。
	evidenceArchive, err := store.NewEvidenceStore(store.PoolEvidenceDB(pool))
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}

	// 复权基准账本（ADR-0011）：qfq 基准必须全局固定且可审计，日线取数依赖它。
	baselines, err := store.NewAdjustBaselineStore(store.PoolBaselineDB(pool), opts)
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}

	// 取数链：`TUSHARE_TOKEN` 有值才装真传输；否则注册表只有声明，作业会被
	// connector 判成 disabled 并在作业行上响亮失败（不会静默写 0 行）。
	// 东财 HTTP 客户端无凭证，恒装配；Alpha Vantage 仅在 key 有值时启用
	// （Juchao / Xueqiu / Yahoo 自带默认端点，由 connector 恒启用）。
	transports := ingest.Transports{
		Eastmoney:       ingest.NewEastmoneyClient(),
		AlphaVantageKey: strings.TrimSpace(os.Getenv("ALPHA_VANTAGE_KEY")),
	}
	var tushareClient *tushare.Client
	if token := strings.TrimSpace(os.Getenv("TUSHARE_TOKEN")); token != "" {
		tushareClient = tushare.NewDefault(token)
		tushareClient.Baselines = baselines
		transports.Tushare = tushareClient
	}
	registry, err := assembly.Registry(transports)
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}
	points, err := assembly.New(registry, writer, nil)
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}

	// 作业层（X1c 的「调度」那一半）：队列在 `ingest.sync_jobs` /
	// `ingest.sync_schedules`，worker 与 scheduler 都在本进程内。
	queue := syncjob.NewPGStore(pool, nil)
	workerID, err := hostnameOr("data-1")
	if err != nil {
		stopRetry()
		pool.Close()
		return nil, nil, err
	}
	jobCtx, stopJobs := context.WithCancel(context.Background())
	runner := syncjob.NewRunner(assembly.Pipeline(registry, writer, nil))
	// 参考数据腿（`stocks` / `trading_calendar`）：也归 Tushare。没有 token 时
	// 不装配——这类作业会以 misconfigured 响亮失败，而不是掉进点位管线。
	if tushareClient != nil {
		referenceWriter, refErr := store.NewReferenceWriter(store.PoolWriteDB(pool), opts)
		if refErr != nil {
			stopJobs()
			stopRetry()
			pool.Close()
			return nil, nil, refErr
		}
		runner.Reference = &syncjob.ReferenceRunner{
			Transport: tushareClient,
			Writer:    referenceWriter,
		}
	}
	worker := &syncjob.Worker{
		Store:  queue,
		Runner: runner,
		ID:     workerID,
		Logger: log.Default(),
	}
	scheduler := &syncjob.Scheduler{Store: queue, Logger: log.Default()}
	// 过期租约回收（DATA-GRACEFUL-SHUTDOWN）：`NewPGStore` 的静态类型已含
	// `LeaseReaper`，装配期即保证回收循环不会落空。
	// 只在 schema 就绪后启动作业循环：降级启动时 sync_* 表可能还不存在，
	// 立刻轮询只会每 2s 往日志里灌一条「relation does not exist」。
	var jobsRunning atomic.Bool
	jobsDone := startJobsWhenReady(jobCtx, &schemaReady, &jobsRunning, worker, scheduler)
	go startReaperWhenReady(jobCtx, &schemaReady, queue, syncjob.DefaultReapInterval)
	// 保留期清理与作业循环同一条 ctx：进程关停时一起停。
	go startEvidencePurgeWhenReady(jobCtx, &schemaReady, evidenceArchive, retentionDays)

	server := httpapi.New(agg, authorizer,
		httpapi.WithPoints(points),
		httpapi.WithEvidence(evidenceArchive),
		// G3 只读兼容面：core 旧读链的 7 条路由，读同一批表（只读）。
		httpapi.WithLegacyReader(st),
		httpapi.WithSync(queue),
		// 作业循环自省面（2026-11-12 裁）：worker 与注册表都在本进程内，
		// 这三条读面只能在这里回答。`IsRunning` 走原子布尔而不是查库：
		// 它回答的是「循环起了没有」，不是「有没有活干」。
		httpapi.WithSyncRuntime(httpapi.SyncRuntime{
			WorkerIDs:       func() []string { return []string{workerID} },
			RegisteredTypes: func() []string { return registeredTypes(registry) },
			IsRunning:       jobsRunning.Load,
		}),
		httpapi.WithReadiness(readinessCheck(pool)),
		httpapi.WithAvailability(schemaReady.Load),
	)
	// 停机顺序（cleanup）：先停领取（cancel jobCtx），再等 worker 把在途作业
	// 交还（`ReleaseJob`：预算未用尽回 pending 供重领，用尽判 failed），
	// 最后关池。等待有上限：个别取数调用可能不响应取消，超时则如实记日志
	// 后关池，不把停机无限期挂住。
	return server, func() {
		stopJobs()
		select {
		case <-jobsDone:
		case <-time.After(jobDrainTimeout):
			log.Printf("data-service: sync loops did not stop within %s; closing the pool with a job possibly still in flight (its lease will be reaped)", jobDrainTimeout)
		}
		stopRetry()
		pool.Close()
	}, nil
}

// jobDrainTimeout 是停服时等待「worker 交还在途作业」的上限。
const jobDrainTimeout = 15 * time.Second

// waitSchema 等 schema 就绪；ctx 结束返回 false。
func waitSchema(ctx context.Context, ready *atomic.Bool) bool {
	for !ready.Load() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
	return true
}

// startJobsWhenReady 等 schema 就绪后启动 worker 与 scheduler，返回的 channel
// 在两者都退出后关闭（停服时用于确认在途作业已交还）。
//
// 两段式而不是直接起：降级启动（PG 不可达 / DDL 未跑完）时队列表可能不存在，
// 立刻轮询只会制造噪音；DDL 就绪后同一进程自愈启动，不需要重启。
func startJobsWhenReady(ctx context.Context, ready, running *atomic.Bool, worker *syncjob.Worker, scheduler *syncjob.Scheduler) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !waitSchema(ctx, ready) {
			return
		}
		running.Store(true)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); worker.Run(ctx) }()
		go func() { defer wg.Done(); scheduler.Run(ctx) }()
		wg.Wait()
	}()
	return done
}

// startReaperWhenReady 等 schema 就绪后启动过期租约回收循环。
func startReaperWhenReady(ctx context.Context, ready *atomic.Bool, reaper syncjob.LeaseReaper, every time.Duration) {
	if !waitSchema(ctx, ready) {
		return
	}
	reapLoop(ctx, reaper, every)
}

// reapLoop 周期回收过期租约。N=0 不打印，避免空转刷日志。
func reapLoop(ctx context.Context, reaper syncjob.LeaseReaper, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := reaper.ReapExpired(ctx, time.Now())
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("data-service: reap expired job leases failed: %v", err)
				}
				continue
			}
			if n > 0 {
				log.Printf("data-service: reaped %d expired job lease(s)", n)
			}
		}
	}
}

// evidencePurgeInterval 是过期证据的清理周期。保留期口径见
// `internal/evidence` / `docs/db-schema.md` §9；清理是维护活，独立于读面。
const evidencePurgeInterval = 24 * time.Hour

// startEvidencePurgeWhenReady 等 schema 就绪后先清一次过期记录，此后每 24h 一次。
// 与作业循环同款两段式：降级启动时 `ingest.raw` 可能还不存在。
func startEvidencePurgeWhenReady(ctx context.Context, ready *atomic.Bool, archive *store.EvidenceStore, retentionDays int) {
	for !ready.Load() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	for {
		cutoff := evidence.RetentionCutoff(time.Now().UTC(), retentionDays)
		if n, err := archive.PurgeExpired(ctx, cutoff); err != nil {
			log.Printf("data-service: evidence purge failed (retention %d days): %v", retentionDays, err)
		} else if n > 0 {
			log.Printf("data-service: purged %d expired ingest.raw records (retention %d days)", n, retentionDays)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(evidencePurgeInterval):
		}
	}
}

// registeredTypes 返回注册表声明的数据类型（= 可接的 job_type），升序。
//
// 用链表的键而不是 connector 名：`job_type` 的取值是 data_type，两者不是
// 同一套字面值。
func registeredTypes(reg *ingest.Registry) []string {
	chains := reg.Chains()
	out := make([]string, 0, len(chains))
	for dataType := range chains {
		out = append(out, dataType)
	}
	sort.Strings(out)
	return out
}

// hostnameOr 取主机名作 worker ID 前缀；取不到用 fallback。
func hostnameOr(fallback string) (string, error) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return fallback, nil
	}
	// 多实例部署时同主机多进程会撞 ID；拼 pid 让日志可分辨。
	return fmt.Sprintf("%s-%d", host, os.Getpid()), nil
}

// retrySchema 在降级启动后重放本仓 DDL，成功即翻 availability 并退出。
// 只重放 `schema.Statements()`（幂等）；不碰一次性回填（`schema.BackfillStatements()`）。
// 受保护的约束回填随 `applySchema` 一起重跑——它本身幂等，且脏数据只告警。
func retrySchema(ctx context.Context, pool *pgxpool.Pool, ready *atomic.Bool) {
	ticker := time.NewTicker(schemaRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attemptCtx, cancel := context.WithTimeout(ctx, schemaStartupTimeout)
			err := applySchema(attemptCtx, pool)
			cancel()
			if err == nil {
				ready.Store(true)
				log.Printf("data-service: schema applied after retry; data endpoints are ready")
				return
			}
			log.Printf("data-service: schema retry failed (data endpoints stay 503): %v", err)
		}
	}
}

// readinessCheck 是 `/readyz` 的检查（G1 ⑥）：PG 池可连 + 本仓 DDL 已执行。
//
// 用 `to_regclass` 探一张 A 集表而不是只 Ping：池连得上但建表没跑（例如迁移
// 半途失败后进程被拉起），接单必然全 500 —— 那就不是就绪。
func readinessCheck(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("postgres unreachable: %w", err)
		}
		var present bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('market.ohlcv_daily_qfq') IS NOT NULL`).Scan(&present); err != nil {
			return fmt.Errorf("schema probe failed: %w", err)
		}
		if !present {
			return errors.New("schema not applied: market.ohlcv_daily_qfq is missing")
		}
		return nil
	}
}

// execer 是执行 DDL 的最小面（`*pgxpool.Pool` 满足它）。
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// applySchema 按序执行本仓的建表/加列语句（幂等，可重入），再跑受保护的
// 约束回填（见 `schema.ApplyGuardedConstraints` 与 ADR-0009）。
//
// 回填在数据违规时只 WARN、不返回 error：历史脏库不许把启动打死。
func applySchema(ctx context.Context, db execer) error {
	for i, stmt := range schema.Statements() {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("schema statement %d failed: %w", i+1, err)
		}
	}
	return schema.ApplyGuardedConstraints(ctx, db, func(format string, args ...any) {
		log.Printf("WARN: "+format, args...)
	})
}
