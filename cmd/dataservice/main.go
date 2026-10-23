// cmd/dataservice 是数据服务的进程入口。
//
// ⚠️ 端口未接线（诚实状态，不是占位谎言）：X1d 的四个 PG port 实现
// （BarSource / TradingCalendar / DataVersioner / UniverseResolver）落地前，
// 本进程以 nil aggregator 启动——/health 常绿，数据端点一律
// 503 dependency_unavailable。这不是「能用但没数据」，而是「明确没接线」：
// 接上假实现让 200 看起来正常，比 503 危险得多。
package main

import (
	"flag"
	"log"
	"net/http"

	"tellurion-data/internal/httpapi"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	flag.Parse()

	srv := httpapi.New(nil)
	log.Printf("data-service listening on %s (data ports unwired: /datasets/* -> 503)", *addr)
	if err := http.ListenAndServe(*addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
