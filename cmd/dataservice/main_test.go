package main

// compose root 的证据（SYNC-01 那一族：「写得对但没人调」）。
//
// 本包此前一个测试都没有，于是 `build()` 里那条写面装配线——
// `store.NewWriter` → `assembly.New` → `httpapi.WithPoints`——本身没有证据：
// 执行器、闸门、投影在 `internal/*` 各自的套件里都是绿的，而把它们接进进程
// 的这一跳如果漏了，红的是生产作业，不是任何测试。
//
// 三条腿各有分工：
//   1. **未配 DSN ⇒ 两面都是 503 形态**（真调用 `build()`，不是读源码）：
//      fail-closed 是设定，不是碰巧；
//   2. **statusLine 把两面的接线状态分开报**（启动日志是唯一给人看的地方，
//      「读面已接」被读成「写面已接」就是这次改动最容易被误判的一点）；
//   3. **DSN 分支里确实装了写面**（源码静态断言）—— 起真 PG 不属本套件，
//      所以这一腿只能钉装配语句本身；名单在 `docs/TASKS.md` 的未落项里。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ruoxizhnya/tellurion-kit/auth"

	"tellurion-data/internal/assembly"
	"tellurion-data/internal/httpapi"
	"tellurion-data/internal/ingest"
	"tellurion-data/internal/store"
)

// tokensWithSecret 造一个启用鉴权的 TokenService（与 build() 的入参同形）。
func tokensWithSecret() *auth.TokenService {
	return auth.NewTokenService(auth.Config{JWTSecret: []byte("compose-root-test-secret")})
}

// 未配 DATABASE_URL ⇒ 读写两面都不接线，且不是错误（503 是设定）。
func TestBuildWithoutDSNLeavesBothFacesUnwired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	srv, cleanup, err := build(tokensWithSecret())
	if err != nil {
		t.Fatalf("unwired build must not error (503 is the designed shape): %v", err)
	}
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if srv.HasAggregator() {
		t.Error("没有 DSN 却报读面已接：/datasets/* 会返回 200 形状而不是 503")
	}
	if srv.HasPointsWriter() {
		t.Error("没有 DSN 却报写面已接：POST /ingest/points 会收下点位而无处落")
	}
}

// statusLine 必须把两面分开说。它是启动日志里唯一说明「这个进程现在能干什么」
// 的一行；写成一句含糊的 "ready" 就等于让运维自己猜 503 是哪一半。
func TestStatusLineReportsReadAndWriteFacesSeparately(t *testing.T) {
	unwired := statusLine(httpapi.New(nil, httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokensWithSecret()))))
	for _, want := range []string{"data ports unwired", "write面 unwired"} {
		if !strings.Contains(unwired, want) {
			t.Errorf("statusLine(%q) 少了 %q", unwired, want)
		}
	}

	// 只装写面（读面仍空）：两半必须各说各的，不能被一个布尔一起翻。
	reg := ingest.NewRegistry()
	points, err := assembly.New(reg, nopSink{}, nil)
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	halfWired := statusLine(httpapi.New(nil,
		httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokensWithSecret())),
		httpapi.WithPoints(points)))
	if !strings.Contains(halfWired, "write面 wired to PG") {
		t.Errorf("装了写面却没报出来：%q", halfWired)
	}
	if !strings.Contains(halfWired, "data ports unwired") {
		t.Errorf("读面没装却不再报未接：%q", halfWired)
	}
}

// 装配语句本身的钉子：`build()` 里 DSN 那一条分支必须真的把声明注册表、
// store.Writer、assembly.New 与 httpapi.WithPoints 串起来。
//
// 这是源码静态断言而不是行为断言，原因是诚实的：起真 PG 不属这个套件
// （`internal/store` 的 PG 腿是 opt-in 的 `DATA_TEST_DSN`）。它能挡住的恰好是
// 本包唯一没有别处证据的那件事——**这一跳有没有被接上**。
func TestComposeRootWiresTheWriteFace(t *testing.T) {
	src := readOwnSource(t)
	for _, want := range []string{
		"store.NewWriter(store.PoolWriteDB(pool), opts)",
		"assembly.DeclarationsRegistry()",
		"assembly.New(declarations, writer, nil)",
		"httpapi.New(agg, authorizer, httpapi.WithPoints(points))",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("build() 的写面装配链少了这一段：%q\n（漏了它，投点端点会在运行时 503，而 internal/* 的套件全绿）", want)
		}
	}

	// 反证：DSN 为空那条腿**不许**顺手装一个假写面。fail-closed 靠的是这里
	// 什么都不传，而不是靠运行时的运气 —— 所以钉的是那条 return 的原样文本：
	// 谁给它加了 WithPoints（哪怕是 nil），这条就红，而 503 会当场变成 200 形状。
	if !strings.Contains(src, "return httpapi.New(nil, authorizer), nil, nil") {
		t.Errorf("未配 DSN 的分支形状变了，不再是最小 503 装配：\n%s",
			src[max(0, strings.Index(src, "if dsn == \"\"")-120):min(len(src), strings.Index(src, "if dsn == \"\"")+240)])
	}
}

// nopSink 只为造出一个「已装配」的 assembly.Writer；本用例不投点。
type nopSink struct{}

func (nopSink) BulkInsert(context.Context, string, []store.Point) (int, int, error) { return 0, 0, nil }

func readOwnSource(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 取不到本文件路径")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "main.go"))
	if err != nil {
		t.Fatalf("读 main.go：%v", err)
	}
	return string(src)
}
