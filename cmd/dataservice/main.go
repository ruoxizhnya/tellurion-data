// cmd/dataservice 是数据服务的进程入口。
//
// 装配（X1d 起）：`DATABASE_URL` 有值 ⇒ 建连接池 → 执行本仓 schema 的 DDL
// （`internal/schema.Statements()`，管 `market` / `ingest`）→ 用 `internal/store`
// 的四个 port 装出 aggregator，数据端点真正可用。
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
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

	"tellurion-data/internal/httpapi"
	"tellurion-data/internal/schema"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
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
	if err := http.ListenAndServe(*addr, srv.Routes()); err != nil {
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
	if srv.HasAggregator() {
		return "data ports wired to PG"
	}
	return "data ports unwired: /datasets/* -> 503"
}

// build 装配 HTTP 面。DSN 为空时返回 nil aggregator（503 形态）。
func build(tokens *auth.TokenService) (*httpapi.Server, func(), error) {
	authorizer := httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokens))
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return httpapi.New(nil, authorizer), nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	if err := applySchema(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	}

	st, err := store.New(store.PoolQuerier(pool), store.Options{MarketSchema: os.Getenv("DATA_MARKET_SCHEMA")})
	if err != nil {
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
		pool.Close()
		return nil, nil, err
	}
	return httpapi.New(agg, authorizer), pool.Close, nil
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
