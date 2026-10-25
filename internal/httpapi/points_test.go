package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
	"github.com/ruoxizhnya/tellurion-kit/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/assembly"
	"tellurion-data/internal/store"
)

// capturedSink 记录 HTTP 层**真正交到写面**的批次。测试判据要看的是
// 「解码之后剩下什么」，不是「handler 返回 200」——200 配一个被悄悄改空的
// 批次照样过闸。
type capturedSink struct {
	calls    int
	dataType string
	points   []store.Point
	err      error
}

func (s *capturedSink) BulkInsert(_ context.Context, dataType string, points []store.Point) (int, int, error) {
	s.calls++
	s.dataType = dataType
	s.points = append([]store.Point(nil), points...)
	if s.err != nil {
		return 0, 0, s.err
	}
	return len(points), 0, nil
}

// newPointsServer 装一条真实装配链（零传输的声明注册表 + 假 sink），
// 只替掉最底层的写库腿。用真 `assembly.Writer` 而不是替身：分诊表与
// 闸门顺序是这条端点的全部风险面，替身会把它们一起替掉。
func newPointsServer(t *testing.T, sink assembly.PointWriter) *Server {
	t.Helper()
	reg, err := assembly.DeclarationsRegistry()
	require.NoError(t, err)
	clock := func() time.Time { return time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC) }
	w, err := assembly.New(reg, sink, clock)
	require.NoError(t, err)
	return New(nil, WithAuthorizer(allowAllAuthorizer{}), WithPoints(w))
}

const pointsBody = `{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"close":1685.5,"volume":31200}}]}`

func postPoints(srv *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingest/points", strings.NewReader(body)))
	return rec
}

// 路由在位且能走通：200 + 计数 + 批次真的到达写面（不是 404，也不是空转）。
func TestIngestPointsIsPublishedAndReachesStore(t *testing.T) {
	sink := &capturedSink{}
	rec := postPoints(newPointsServer(t, sink), pointsBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var res assembly.Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.Equal(t, dataprotocol.DataTypeOHLCDaily, res.DataType)
	assert.Equal(t, 1, res.Received)
	assert.Equal(t, 1, res.Persisted)

	require.Equal(t, 1, sink.calls)
	require.Len(t, sink.points, 1)
	assert.Equal(t, "600519.SH", sink.points[0].Symbol)
	assert.Equal(t, "tushare", sink.points[0].Source)
	assert.Equal(t, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), sink.points[0].VisibleAt,
		"可见时刻由声明锚点推导，投送面上没有这个字段")
	assert.Equal(t, time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC), sink.points[0].IngestTime)
}

// 精度腿：金额经 wire 之后必须还是 json.Number。折成 float64 是静默的
// 精度损失，而这条只能在解码处拦住——store 那边两种都收，看不出差别。
func TestIngestPointsKeepsNumericPayloadPrecision(t *testing.T) {
	sink := &capturedSink{}
	body := `{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"close":1685.5,"turnover":123456789012345.678}}]}`
	rec := postPoints(newPointsServer(t, sink), body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, sink.points, 1)
	data := sink.points[0].Data
	assert.IsType(t, json.Number(""), data["turnover"], "解码必须 UseNumber，否则大位数被折成 float64")
	assert.Equal(t, "123456789012345.678", fmt.Sprint(data["turnover"]))
}

// 写面未接线 ⇒ 503，与读面同一档 fail-closed。
func TestIngestPointsUnwiredFailsClosed(t *testing.T) {
	rec := postPoints(New(nil, WithAuthorizer(allowAllAuthorizer{})), pointsBody)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "dependency_unavailable", errorCodeOf(t, rec.Body.Bytes()))
}

// 投送 `visible_at` 必须解码失败，而不是「被忽略」。忽略的含义是调用方
// 以为自己指定的可见时刻生效了——那比 400 坏得多。
func TestIngestPointsRejectsServerOwnedFields(t *testing.T) {
	sink := &capturedSink{}
	rec := postPoints(newPointsServer(t, sink),
		`{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{},"visible_at":"2020-01-01T00:00:00Z"}]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "invalid_parameter", errorCodeOf(t, rec.Body.Bytes()))
	assert.Zero(t, sink.calls, "解码失败的批次不许已经落库")

	// 未知顶层字段同理（防「以后偷偷加字段」把这条门绕过去）。
	rec = postPoints(newPointsServer(t, &capturedSink{}), `{"data_type":"ohlcv_daily","points":[],"strict":false}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// 写侧每一种失败都要有自己的码；未登记的失败仍走 500，不伪装成 4xx。
func TestIngestPointsMapsWriteFailuresToCodes(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		sinkErr error
		status  int
		code    string
	}{
		{"empty batch", `{"data_type":"ohlcv_daily","points":[]}`, nil, http.StatusBadRequest, "invalid_batch"},
		{"undeclared data type", `{"data_type":"not_a_declared_type","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{}}]}`, nil,
			http.StatusBadRequest, "data_type_undeclared"},
		{"no source", `{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"","data":{}}]}`, nil,
			http.StatusBadRequest, "invalid_batch"},
		{"content conflict", pointsBody, fmt.Errorf("%w: 600519.SH ohlcv_daily", store.ErrContentConflict),
			http.StatusConflict, "content_conflict"},
		{"b set table", pointsBody, fmt.Errorf("%w: ohlcv_daily maps to market_activity (gate class \"B\")", store.ErrWriteTableNotInASet),
			http.StatusBadRequest, "write_table_not_in_a_set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postPoints(newPointsServer(t, &capturedSink{err: tc.sinkErr}), tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, tc.code, errorCodeOf(t, rec.Body.Bytes()))
		})
	}
}

// 有界门在 wire 上也成立：超上限的整批拒收，一行都不落。
func TestIngestPointsRejectsBatchOverTheLimitOnTheWire(t *testing.T) {
	points := make([]map[string]any, 0, 10001)
	base := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10001; i++ {
		points = append(points, map[string]any{
			"symbol":     fmt.Sprintf("6%05d.SZ", i),
			"trade_time": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"source":     "tushare",
			"data":       map[string]any{"close": json.Number("1")},
		})
	}
	body, err := json.Marshal(map[string]any{"data_type": dataprotocol.DataTypeOHLCDaily, "points": points})
	require.NoError(t, err)

	sink := &capturedSink{}
	rec := postPoints(newPointsServer(t, sink), string(body))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	assert.Equal(t, "batch_too_large", errorCodeOf(t, rec.Body.Bytes()))
	assert.Zero(t, sink.calls, "超限不是「写前 10000 条」")
}

// 大报文在解码前就被拦住（这条门的另一半：点很小但报文很大）。
func TestIngestPointsRejectsOversizedBody(t *testing.T) {
	huge := `{"data_type":"ohlcv_daily","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"blob":"` +
		strings.Repeat("x", (8<<20)+1024) + `"}}]}`
	sink := &capturedSink{}
	rec := postPoints(newPointsServer(t, sink), huge)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_parameter", errorCodeOf(t, rec.Body.Bytes()))
	assert.Zero(t, sink.calls)
}

// 经**真** `store.Writer` 的一条腿：上面的替身把 A 集门、内容比对、
// visible_at 判零全替掉了，所以「写入路径的表只出现 A 集」这条判据必须
// 在真写面上再走一次。fake 只放在最底层的 Exec 处（记录 SQL，不连库）。
func TestIngestPointsThroughRealWriter(t *testing.T) {
	reg, err := assembly.DeclarationsRegistry()
	require.NoError(t, err)

	// B 集（daily_basic）与 C 集（fundamentals）都有合法声明 ⇒ 闸门放行，
	// 但真写面必须拒。上面那条替身腿拦不到这一层。
	// 两条的锚点各在自己的 Data 列上（trade_date / ann_date），所以要带着
	// 锚点投——不然先被闸门拦下，测的就不是 A 集门了。
	cases := []struct {
		dataType string
		body     string
	}{
		{dataprotocol.DataTypeDailyBasic, `{"data_type":"daily_basic","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"trade_date":"2026-01-15","close":1}}]}`},
		{dataprotocol.DataTypeFundamental, `{"data_type":"fundamentals","points":[{"symbol":"600519.SH","trade_time":"2026-01-15T00:00:00Z","source":"tushare","data":{"ann_date":"2026-01-15","roe":1}}]}`},
	}
	for _, tc := range cases {
		db := &recordingWriteDB{}
		w, err := store.NewWriter(db, store.Options{})
		require.NoError(t, err)
		points, err := assembly.New(reg, w, func() time.Time { return time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC) })
		require.NoError(t, err)

		rec := postPoints(New(nil, WithAuthorizer(allowAllAuthorizer{}), WithPoints(points)), tc.body)
		require.Equal(t, http.StatusBadRequest, rec.Code, tc.dataType+": "+rec.Body.String())
		assert.Equal(t, "write_table_not_in_a_set", errorCodeOf(t, rec.Body.Bytes()), tc.dataType)
		assert.Zero(t, db.execs, tc.dataType+": 被拒的批次不许已经发过 INSERT")
	}

	// A 集正常腿：真写面对 ohlcv_daily 发出带 visible_at 列的 INSERT。
	db := &recordingWriteDB{affected: 1}
	writer, err := store.NewWriter(db, store.Options{})
	require.NoError(t, err)
	points, err := assembly.New(reg, writer, func() time.Time { return time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC) })
	require.NoError(t, err)
	rec := postPoints(New(nil, WithAuthorizer(allowAllAuthorizer{}), WithPoints(points)), pointsBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, db.execs, 1)
	assert.Contains(t, db.execs[0], "INSERT INTO public.ohlcv_daily_qfq")
	assert.Contains(t, db.execs[0], "visible_at")
	assert.Contains(t, db.execs[0], "ON CONFLICT DO NOTHING", "写面用内容比对，不继承 core 的 DO UPDATE")
}

// recordingWriteDB 是 store.WriteDB 的无库替身：记录 SQL，按预设返回影响行数。
type recordingWriteDB struct {
	execs    []string
	affected int64
}

func (d *recordingWriteDB) BeginTx(context.Context) (store.WriteTx, error) {
	return &recordingWriteTx{db: d}, nil
}

type recordingWriteTx struct{ db *recordingWriteDB }

func (tx *recordingWriteTx) Exec(_ context.Context, sql string, _ ...any) (store.CommandTag, error) {
	tx.db.execs = append(tx.db.execs, sql)
	affected := tx.db.affected
	tx.db.affected = 0 // 只有首行是「新插入」，后续行走内容比对分支
	return recordingCommandTag{n: affected}, nil
}

func (tx *recordingWriteTx) QueryRow(context.Context, string, ...any) store.Row {
	return recordingRow{same: true}
}

func (tx *recordingWriteTx) Commit(context.Context) error   { return nil }
func (tx *recordingWriteTx) Rollback(context.Context) error { return nil }

type recordingCommandTag struct{ n int64 }

func (t recordingCommandTag) RowsAffected() int64 { return t.n }

type recordingRow struct{ same bool }

func (r recordingRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return fmt.Errorf("recordingRow: want 1 dest, got %d", len(dest))
	}
	p, ok := dest[0].(*bool)
	if !ok {
		return fmt.Errorf("recordingRow: want *bool, got %T", dest[0])
	}
	*p = r.same
	return nil
}

// 写面的鉴权档位单独验一遍：service 能过闸（走到业务 503），
// viewer / trader 仍 403，无凭证 401。读端点放开到 viewer 不代表写面跟着放开。
func TestIngestPointsAuthTiers(t *testing.T) {
	svc := auth.NewTokenService(auth.Config{JWTSecret: []byte("data-service-points-role-test")})
	unwired := New(nil, WithAuthorizer(NewAuthorizer(svc))).Routes()

	post := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/ingest/points", strings.NewReader(pointsBody))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		unwired.ServeHTTP(rec, req)
		return rec
	}

	mint := func(role string) string {
		t.Helper()
		token, _, err := svc.IssueTokens(0, "service:core", role)
		require.NoError(t, err)
		return token
	}

	for _, role := range []string{"service", "admin"} {
		rec := post(mint(role))
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, role)
		assert.Equal(t, "dependency_unavailable", errorCodeOf(t, rec.Body.Bytes()), role)
	}
	for _, role := range []string{"viewer", "trader", "writer"} {
		rec := post(mint(role))
		require.Equal(t, http.StatusForbidden, rec.Code, role)
		assert.Equal(t, "forbidden", errorCodeOf(t, rec.Body.Bytes()), role)
	}
	rec := post("")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// `/health` 与三条数据端点的发布面对得上 `docs/api-schema.md` §1：
// 写端点在位（不是 404），且 GET 不被接受（405 由 ServeMux 给）。
func TestRouteTableIsThePublishedSurfaceWithWriteRoute(t *testing.T) {
	sink := &capturedSink{}
	r := newPointsServer(t, sink).Routes()

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingest/points", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "写端点只接受 POST")

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingest/points", strings.NewReader(pointsBody)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, sink.calls)
}
