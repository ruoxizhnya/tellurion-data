// cmd/dataservice 是数据服务的进程入口。
//
// 装配（G4 起）：`DATABASE_URL` 有值 ⇒ 建连接池（**不拨号**）→ 尝试执行本仓
// schema 的 DDL（`internal/schema.Statements()`，管 `market` / `ingest`）→
// 用 `internal/store` 的四个 port 装出 aggregator。DDL 失败（PG 不可达）
// 不再让进程退出：数据端点保持 503，后台每 10s 重试迁移，成功后同进程自愈。
//
// `DATABASE_URL` 为空 ⇒ 保持 **fail-closed 503**（`/health` 常绿）。
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
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

	"tellurion-data/internal/assembly"
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
		"JWT secret shared with analysis (empty = open access on loopback only)")
	exposure := flag.String("insecure-exposure", os.Getenv("AUTH_INSECURE_EXPOSURE"),
		"explicit exposure declaration for open access on a non-loopback bind (only loopback-published is accepted)")
	flag.Parse()

	// 启动闸（裁-15）：本服务默认 `:8081`（全网卡），无 secret 必须显式声明容器
	// 只在回环发布，否则拒绝启动——不允许把无鉴权数据面暴露到网络。
	startup := kithttp.DecideStartup(*jwtSecret, true, bindHost(*addr), *exposure)
	if startup.Refuse {
		log.Fatalf("data-service: %s", startup.Reason)
	}
	if startup.Insecure {
		log.Printf("data-service: WARNING authentication disabled (open access); bind address %q must be loopback or loopback-published", *addr)
	}
	tokens := auth.NewTokenService(auth.Config{JWTSecret: startup.Secret})

	srv, cleanup, err := build(tokens)
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
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// bindHost 从 -addr 取绑定主机；取不到端口时原样当主机比（空 = 全网卡）。
func bindHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
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
func build(tokens *auth.TokenService) (*httpapi.Server, func(), error) {
	authorizer := httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokens))
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return httpapi.New(nil, authorizer), nil, nil
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
	// 只在 schema 就绪后启动作业循环：降级启动时 sync_* 表可能还不存在，
	// 立刻轮询只会每 2s 往日志里灌一条「relation does not exist」。
	var jobsRunning atomic.Bool
	go startJobsWhenReady(jobCtx, &schemaReady, &jobsRunning, worker, scheduler)

	server := httpapi.New(agg, authorizer,
		httpapi.WithPoints(points),
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
	return server, func() { stopJobs(); stopRetry(); pool.Close() }, nil
}

// startJobsWhenReady 等 schema 就绪后启动 worker 与 scheduler。
//
// 两段式而不是直接起：降级启动（PG 不可达 / DDL 未跑完）时队列表可能不存在，
// 立刻轮询只会制造噪音；DDL 就绪后同一进程自愈启动，不需要重启。
func startJobsWhenReady(ctx context.Context, ready, running *atomic.Bool, worker *syncjob.Worker, scheduler *syncjob.Scheduler) {
	for !ready.Load() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	running.Store(true)
	go worker.Run(ctx)
	go scheduler.Run(ctx)
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
// 只重放 `schema.Statements()`（幂等）；不碰一次性回填。
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
}

// applySchema 按序执行本仓的建表/加列语句（幂等，可重入）。
//
// 不跑回填：见文件头注释。
func applySchema(ctx context.Context, db execer) error {
	for i, stmt := range schema.Statements() {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("schema statement %d failed: %w", i+1, err)
		}
	}
	return nil
}
