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
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"tellurion-data/internal/httpapi"
	"tellurion-data/internal/schema"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	flag.Parse()

	srv, cleanup, err := build()
	if err != nil {
		log.Fatalf("data-service wiring failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	log.Printf("data-service listening on %s (%s)", *addr, statusLine(srv))
	if err := http.ListenAndServe(*addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}

func statusLine(srv *httpapi.Server) string {
	if srv.HasAggregator() {
		return "data ports wired to PG"
	}
	return "data ports unwired: /datasets/* -> 503"
}

// build 装配 HTTP 面。DSN 为空时返回 nil aggregator（503 形态）。
func build() (*httpapi.Server, func(), error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return httpapi.New(nil), nil, nil
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
	return httpapi.New(agg), pool.Close, nil
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
