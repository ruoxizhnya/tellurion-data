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
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// TestDecideAuthPosture 钉住三态启动判定：缺省不再自动放行；第 2 态必须回环
// 绑定（非回环 + 无声明 ⇒ 拒启）；只有逐字 loopback-published 才进入开放态；
// 拼错的值同样拒绝，而不是静默落默认。
func TestDecideAuthPosture(t *testing.T) {
	cases := []struct {
		name, secret, exposure, addr string
		want                         authPosture
		wantErr                      bool
	}{
		{"有 secret 全闸", "s3cret", "", "0.0.0.0:8081", authEnforced, false},
		{"无 secret 回环未声明⇒写面锁定", "", "", "127.0.0.1:8081", authLocked, false},
		{"无 secret localhost 未声明⇒写面锁定", "", "", "localhost:8081", authLocked, false},
		{"无 secret 非回环未声明⇒拒绝", "", "", "0.0.0.0:8081", authLocked, true},
		{"无 secret 空主机未声明⇒拒绝", "", "", ":8081", authLocked, true},
		{"无 secret 显式声明⇒任意绑定开放", "", "loopback-published", "0.0.0.0:8081", authOpen, false},
		{"无 secret 拼错声明⇒拒绝", "", "loopback_published", "127.0.0.1:8081", authLocked, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decideAuthPosture(tc.secret, tc.exposure, tc.addr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("posture = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestAuthWarningCarriesActualBind 钉住第 3 态 WARN 同时说出「写面无鉴权」与
// **实际绑定地址**；声明值保留且标成「声明」；非回环绑定必须额外点名「已对外暴露」。
func TestAuthWarningCarriesActualBind(t *testing.T) {
	cases := []struct {
		name, addr string
		exposed    bool
	}{
		{"回环", "127.0.0.1:8081", false},
		{"全网卡", "0.0.0.0:8081", true},
		{"空主机", ":8081", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authWarning(tc.addr)
			for _, want := range []string{tc.addr, "写面无鉴权", "实际绑定", "声明", "loopback-published"} {
				if !strings.Contains(got, want) {
					t.Fatalf("WARN 少了 %q：%s", want, got)
				}
			}
			if has := strings.Contains(got, "已对外暴露"); has != tc.exposed {
				t.Fatalf("「已对外暴露」= %t, want %t：%s", has, tc.exposed, got)
			}
		})
	}
}

// TestMainProcessRejectsNonLoopbackWithoutSecret 用真进程钉住第 2 态的非回环
// 拒绝：退出码必须是 1，stderr 必须说清「缺 secret 且未声明、绑定地址不是回环」。
func TestMainProcessRejectsNonLoopbackWithoutSecret(t *testing.T) {
	if os.Getenv("DATA_MAIN_HELPER") == "1" {
		os.Args = []string{"dataservice", "-addr", "0.0.0.0:18081"}
		main()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMainProcessRejectsNonLoopbackWithoutSecret")
	cmd.Env = append(envWithout(os.Environ(), "JWT_SECRET", "AUTH_JWT_SECRET", "AUTH_INSECURE_EXPOSURE", "DATABASE_URL"), "DATA_MAIN_HELPER=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("进程未在 5s 内退出：非回环 + 无声明必须拒启，不能降级起服务；output=%s", out)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("期望非零退出，实得 err=%v output=%s", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("退出码 = %d, want 1; output=%s", exitErr.ExitCode(), out)
	}
	for _, want := range []string{"不是回环", "JWT_SECRET", "AUTH_INSECURE_EXPOSURE"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("stderr 少了 %q:\n%s", want, out)
		}
	}
	t.Logf("exit=%d stderr=%s", exitErr.ExitCode(), strings.TrimSpace(string(out)))
}

// envWithout 去掉指定键，避免外层 shell 的 secret / 声明污染子进程判定。
func envWithout(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, k := range keys {
			if strings.HasPrefix(strings.ToUpper(kv), strings.ToUpper(k)+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// 未配 DATABASE_URL ⇒ 读写两面都不接线，且不是错误（503 是设定）。
func TestBuildWithoutDSNLeavesBothFacesUnwired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	srv, cleanup, err := build(httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokensWithSecret())))
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
		"tushareClient = tushare.NewDefault(token)",
		// 复权基准账本（ADR-0011）：漏了它日线取数会因「账本未装配」响亮失败。
		"store.NewAdjustBaselineStore(store.PoolBaselineDB(pool), opts)",
		"tushareClient.Baselines = baselines",
		"assembly.Registry(transports)",
		"assembly.New(registry, writer, nil)",
		// 传输多门（QT-40）：东财无凭证恒装配；Alpha Vantage 从环境变量取 key。
		"Eastmoney:       ingest.NewEastmoneyClient()",
		"AlphaVantageKey: strings.TrimSpace(os.Getenv(\"ALPHA_VANTAGE_KEY\"))",
		// 参考数据腿（stocks / trading_calendar）：只在本跳接线；漏了则这两类
		// 作业以 misconfigured 失败（不是静默成功）。
		"store.NewReferenceWriter(store.PoolWriteDB(pool), opts)",
		"runner.Reference = &syncjob.ReferenceRunner{",
		"httpapi.WithPoints(points)",
		// 证据归档面（ADR-0009）：漏了它 `/api/ingest/raw` 与 `/api/evidence`
		// 会 503，而 core 的反代会把 503 原样透给证据页。
		"store.NewEvidenceStore(store.PoolEvidenceDB(pool))",
		"httpapi.WithEvidence(evidenceArchive)",
		"go startEvidencePurgeWhenReady(jobCtx, &schemaReady, evidenceArchive, retentionDays)",
		"httpapi.WithReadiness(readinessCheck(pool))",
		// 作业层（X1c 的调度那一半）同样只在本跳接线：队列没挂上则
		// `/api/sync/jobs` 全 503，worker/scheduler 不起则作业永远 pending。
		"syncjob.NewPGStore(pool, nil)",
		"httpapi.WithSync(queue)",
		// 停机要等在途作业交还（DATA-GRACEFUL-SHUTDOWN）：done channel 漏了，
		// 关池会撞上还没写完 ReleaseJob 的 worker。
		"jobsDone := startJobsWhenReady(jobCtx, &schemaReady, &jobsRunning, worker, scheduler)",
		// 过期租约回收循环漏了，崩溃 worker 的作业会永远 running。
		"go startReaperWhenReady(jobCtx, &schemaReady, queue, syncjob.DefaultReapInterval)",
		// 作业循环自省面（2026-11-12 裁）：漏了它 `/api/sync/workers` 与
		// `/api/sync/scheduler/stats` 会 503，而「队列在跑」在别的套件里看不出来。
		"httpapi.WithSyncRuntime(httpapi.SyncRuntime{",
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

// TestRunHTTPDrainsInFlightRequests 钉住「信号 → 排空」：ctx 取消（等价
// SIGTERM）之后，已经在处理中的请求必须照常拿到 200，而不是被硬切。
func TestRunHTTPDrainsInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("done"))
	})
	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHTTP(ctx, srv, ln) }()

	respCh := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			respCh <- "ERR: " + err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		respCh <- resp.Status + " " + string(body)
	}()

	<-started
	cancel()       // 等价 SIGTERM：必须进入排空，而不是关连接
	close(release) // 放行在途请求

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runHTTP: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runHTTP did not return after shutdown")
	}
	select {
	case got := <-respCh:
		if got != "200 OK done" {
			t.Fatalf("in-flight response = %q, want it drained, not dropped", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request was dropped on shutdown")
	}
}

// fakeReaper 记录 reapLoop 的调用次数。
type fakeReaper struct{ n int32 }

func (f *fakeReaper) ReapExpired(context.Context, time.Time) (int, error) {
	atomic.AddInt32(&f.n, 1)
	return 0, nil
}

// TestReapLoopTicksUntilCancel 钉住回收循环真的被驱动、且随 ctx 退出。
func TestReapLoopTicksUntilCancel(t *testing.T) {
	reaper := &fakeReaper{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); reapLoop(ctx, reaper, 5*time.Millisecond) }()

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&reaper.n) < 2 {
		select {
		case <-deadline:
			t.Fatalf("reapLoop ticks = %d, want >= 2", atomic.LoadInt32(&reaper.n))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reapLoop did not stop after ctx cancel")
	}
}

// nopSink 只为造出一个「已装配」的 assembly.Writer；本用例不投点。
type nopSink struct{}

func (nopSink) BulkInsert(context.Context, string, []store.Point) (int, int, error) {
	return 0, 0, nil
}

func (nopSink) BulkInsertWithOptions(context.Context, string, []store.Point, store.BulkWriteOptions) (int, int, error) {
	return 0, 0, nil
}

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
