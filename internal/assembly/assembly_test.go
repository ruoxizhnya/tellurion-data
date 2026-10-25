package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/normalize"
	"tellurion-data/internal/store"
)

// fixedClock 是所有用例共用的时钟：写侧「同输入同字节」只有在时钟可注入时
// 才是可断言的事实。
func fixedClock() time.Time { return time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC) }

// recordingSink 是 PointWriter 的替身，记录它**收到**了什么。
// 「整批拒收」只能从「一次都没被调用」这一侧证明，所以 calls 与批次都要留痕。
type recordingSink struct {
	calls     int
	dataType  string
	batches   [][]store.Point
	persisted int
	skipped   int
	err       error
}

func (s *recordingSink) BulkInsert(_ context.Context, dataType string, points []store.Point) (int, int, error) {
	s.calls++
	s.dataType = dataType
	s.batches = append(s.batches, points)
	if s.err != nil {
		return 0, 0, s.err
	}
	if s.persisted == 0 && s.skipped == 0 {
		return len(points), 0, nil
	}
	return s.persisted, s.skipped, nil
}

func afterCloseDecl(dataType string, anchor string) ingest.ConnectorDeclaration {
	return ingest.ConnectorDeclaration{
		DataType:      dataType,
		TimeSemantics: market.VisibleAfterClose,
		VisibleField:  anchor,
		Frequency:     market.FreqSpecDaily,
	}
}

// fakeConnector 只实现注册面（`Registry.Register` 收的是 4 方法窄接口）。
type fakeConnector struct {
	ingest.AdapterBase
	types []string
	decls []ingest.ConnectorDeclaration
}

func (f *fakeConnector) SupportedTypes() []string { return append([]string(nil), f.types...) }
func (f *fakeConnector) Declarations() []ingest.ConnectorDeclaration {
	return append([]ingest.ConnectorDeclaration(nil), f.decls...)
}
func (f *fakeConnector) Schema(dataType string) (ingest.DataSchema, error) {
	return ingest.DataSchema{DataType: dataType}, nil
}

// testRegistry 登记一条锚点 = trade_time 的 ohlcv_daily 声明。
func testRegistry(t *testing.T) *ingest.Registry {
	t.Helper()
	reg := ingest.NewRegistry()
	err := reg.Register(&fakeConnector{
		AdapterBase: ingest.NewAdapterBase("fake", true),
		types:       []string{dataprotocol.DataTypeOHLCDaily},
		decls:       []ingest.ConnectorDeclaration{afterCloseDecl(dataprotocol.DataTypeOHLCDaily, ingest.VisibleFieldTradeTime)},
	})
	require.NoError(t, err)
	return reg
}

// registryWithAnchor 登记一条「锚点在 Data 列上」的类型（复现 ann_date 族）。
func registryWithAnchor(t *testing.T, dataType, anchor string) *ingest.Registry {
	t.Helper()
	reg := ingest.NewRegistry()
	err := reg.Register(&fakeConnector{
		AdapterBase: ingest.NewAdapterBase("fake", true),
		types:       []string{dataType},
		decls:       []ingest.ConnectorDeclaration{afterCloseDecl(dataType, anchor)},
	})
	require.NoError(t, err)
	return reg
}

func point(symbol string, tradeTime time.Time) Point {
	return Point{
		Symbol:    symbol,
		TradeTime: tradeTime,
		Source:    "fake",
		Data:      map[string]any{"close": json.Number("12.5")},
	}
}

func TestNew_RejectsNilRegistryAndSink(t *testing.T) {
	_, err := New(nil, &recordingSink{}, fixedClock)
	require.ErrorIs(t, err, ErrMisconfigured, "nil registry 是装配 bug，不许留到第一次写入才炸")

	_, err = New(testRegistry(t), nil, fixedClock)
	require.ErrorIs(t, err, ErrMisconfigured, "nil sink 同理")
}

// 声明门 fail-closed：没登记过可见语义的 data_type 一次都不该触达写面。
func TestWriteRejectsUndeclaredDataTypeWithoutTouchingStore(t *testing.T) {
	sink := &recordingSink{}
	w, err := New(testRegistry(t), sink, fixedClock)
	require.NoError(t, err)

	_, err = w.Write(context.Background(), Request{
		DataType: dataprotocol.DataTypeFundamental,
		Points:   []Point{point("600000.SH", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))},
	})
	require.ErrorIs(t, err, ingest.ErrDeclarationMissing)
	assert.Zero(t, sink.calls, "声明缺失的批次不许触达 store")
}

// 闸门本体：任一点缺锚点 ⇒ 整批拒收，且 store 一次都没被调用。
//
// 破坏验证目标（本单）：把 Write 里 GatePoints 的错误吞掉（`gated, _ :=`）
// ⇒ 本用例必须红（store 收到缺可见时刻的批次）。
func TestWriteRejectsWholeBatchWithoutAnchorAndNeverCallsStore(t *testing.T) {
	sink := &recordingSink{}
	w, err := New(registryWithAnchor(t, dataprotocol.DataTypeAnnounce, ingest.VisibleFieldAnnDate), sink, fixedClock)
	require.NoError(t, err)

	tradeTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	res, err := w.Write(context.Background(), Request{
		DataType: dataprotocol.DataTypeAnnounce,
		Points: []Point{
			{Symbol: "600000.SH", TradeTime: tradeTime, Source: "fake",
				Data: map[string]any{"ann_title": "有锚点", "ann_date": "2026-01-15"}},
			{Symbol: "000001.SZ", TradeTime: tradeTime.AddDate(0, 0, 1), Source: "fake",
				Data: map[string]any{"ann_title": "没有锚点列"}},
		},
	})
	require.ErrorIs(t, err, normalize.ErrVisibleAtMissing, "第二点缺 ann_date ⇒ 整批拒收")
	assert.Equal(t, Result{}, res, "拒收时不返回半成品计数")
	assert.Zero(t, sink.calls, "闸门拦下的批次不许已经写进去一半")
}

// 阈值腿用绝对数值，不从常量推导：调大上界也要红在对的地方。
func TestWriteRejectsBatchAboveThePerRequestLimit(t *testing.T) {
	require.Equal(t, 10000, MaxPointsPerRequest, "上界被 docs 与 core 侧分片大小同时引用，改它要两处一起改")

	sink := &recordingSink{}
	w, err := New(testRegistry(t), sink, fixedClock)
	require.NoError(t, err)

	base := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	over := make([]Point, 0, 10001)
	for i := 0; i < 10001; i++ {
		over = append(over, point(fmt.Sprintf("6%05d.SZ", i), base.Add(time.Duration(i)*time.Minute)))
	}
	_, err = w.Write(context.Background(), Request{DataType: dataprotocol.DataTypeOHLCDaily, Points: over})
	require.ErrorIs(t, err, ErrBatchTooLarge)
	assert.Contains(t, err.Error(), "10000")
	assert.Zero(t, sink.calls, "超限批次不是「写前 10000 条」")

	// 反证腿：正好 10000 条必须放行，否则上界被实现成了「少一条」。
	res, err := w.Write(context.Background(), Request{DataType: dataprotocol.DataTypeOHLCDaily, Points: over[:10000]})
	require.NoError(t, err)
	assert.Equal(t, 10000, res.Received)
	assert.Equal(t, 1, sink.calls)
}

func TestWriteRejectsInvalidBatchShapes(t *testing.T) {
	tradeTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	mismatched := point("600000.SH", tradeTime)
	mismatched.DataType = dataprotocol.DataTypeRealtime
	blankSource := point("600000.SH", tradeTime)
	blankSource.Source = "  "

	cases := []struct {
		name  string
		req   Request
		claim string
	}{
		{"no data type", Request{Points: []Point{point("600000.SH", tradeTime)}}, "data_type 必填"},
		{"empty batch", Request{DataType: dataprotocol.DataTypeOHLCDaily}, "空批次不是「无事可做」的成功"},
		{"point data_type differs", Request{DataType: dataprotocol.DataTypeOHLCDaily, Points: []Point{mismatched}}, "点上的类型与批次不一致必须拒"},
		{"blank source", Request{DataType: dataprotocol.DataTypeOHLCDaily, Points: []Point{blankSource}}, "来源是内容比对的一部分，空白等于放弃换源判定"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			w, err := New(testRegistry(t), sink, fixedClock)
			require.NoError(t, err)
			_, err = w.Write(context.Background(), tc.req)
			require.ErrorIs(t, err, ErrInvalidBatch, tc.claim)
			assert.Zero(t, sink.calls)
		})
	}
}

// 正常腿：入库时刻来自注入时钟，可见时刻来自声明锚点，载荷原样到达写面。
func TestWriteStampsIngestTimeAndDerivesVisibleAtFromAnchor(t *testing.T) {
	sink := &recordingSink{}
	w, err := New(testRegistry(t), sink, fixedClock)
	require.NoError(t, err)

	tradeTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	res, err := w.Write(context.Background(), Request{
		DataType: dataprotocol.DataTypeOHLCDaily,
		Points:   []Point{point("600000.SH", tradeTime)},
	})
	require.NoError(t, err)
	assert.Equal(t, Result{DataType: dataprotocol.DataTypeOHLCDaily, Received: 1, Persisted: 1}, res)
	assert.Equal(t, dataprotocol.DataTypeOHLCDaily, sink.dataType)

	require.Len(t, sink.batches, 1)
	require.Len(t, sink.batches[0], 1)
	got := sink.batches[0][0]
	assert.Equal(t, fixedClock(), got.IngestTime, "ingest_time 由本进程时钟填，不接受投送")
	assert.Equal(t, tradeTime, got.VisibleAt, "visible_at 由声明锚点（trade_time）推导")
	assert.Equal(t, "fake", got.Source)
	// 载荷必须是 json.Number 原样到达：折成 float64 在金融数值上是静默精度损失。
	assert.IsType(t, json.Number(""), got.Data["close"])
}

// 计数口径：skipped 要能归到「形态不合法」与「幂等坐标重复」两处。
func TestWriteCountsSkippedFromValidateAndDedupe(t *testing.T) {
	sink := &recordingSink{}
	w, err := New(testRegistry(t), sink, fixedClock)
	require.NoError(t, err)

	tradeTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	first := point("600000.SH", tradeTime)
	duplicate := point("600000.SH", tradeTime)
	invalid := Point{Symbol: "", TradeTime: tradeTime, Source: "fake", Data: map[string]any{"close": json.Number("1")}}

	res, err := w.Write(context.Background(), Request{
		DataType: dataprotocol.DataTypeOHLCDaily,
		Points:   []Point{first, duplicate, invalid},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Received)
	assert.Equal(t, 2, res.Skipped, "一条形态不合法 + 一条重复")
	assert.Equal(t, 1, res.Persisted)
	require.Len(t, sink.batches, 1)
	assert.Len(t, sink.batches[0], 1)
}

// 全部点被校验/去重吃掉 ⇒ 不开事务（sink 不被调用），但仍是成功收口。
func TestWriteWithoutDurablePointsDoesNotOpenTransaction(t *testing.T) {
	sink := &recordingSink{}
	w, err := New(testRegistry(t), sink, fixedClock)
	require.NoError(t, err)

	tradeTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	res, err := w.Write(context.Background(), Request{
		DataType: dataprotocol.DataTypeOHLCDaily,
		Points:   []Point{{Symbol: "", TradeTime: tradeTime, Source: "fake", Data: map[string]any{"close": json.Number("1")}}},
	})
	require.NoError(t, err)
	assert.Equal(t, Result{DataType: dataprotocol.DataTypeOHLCDaily, Received: 1, Skipped: 1}, res)
	assert.Zero(t, sink.calls, "没有可写的点就不该开事务")
}

// store 失败 ⇒ 装配不粉饰：零值结果 + 原样错误（内容冲突由 httpapi 分诊成 409）。
func TestWritePropagatesStoreFailure(t *testing.T) {
	sink := &recordingSink{err: fmt.Errorf("%w: 600000.SH ohlcv_daily", store.ErrContentConflict)}
	w, err := New(testRegistry(t), sink, fixedClock)
	require.NoError(t, err)

	res, err := w.Write(context.Background(), Request{
		DataType: dataprotocol.DataTypeOHLCDaily,
		Points:   []Point{point("600000.SH", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))},
	})
	require.ErrorIs(t, err, store.ErrContentConflict)
	assert.Equal(t, Result{}, res, "整批回滚后不许报半成品计数")
}

// 投影恒等：七个字段逐字搬，不解释、不补默认值。
func TestProjectIsFieldForField(t *testing.T) {
	tradeTime := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	visible := time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC)
	data := map[string]any{"close": json.Number("12.5"), "note": "x"}

	src := ingest.NewUnifiedDataPoint("600000.SH", "tushare", dataprotocol.DataTypeOHLCDaily, tradeTime, fixedClock(), data)
	src.VisibleAt = visible

	assert.Equal(t, store.Point{
		Symbol: "600000.SH", TradeTime: tradeTime, Source: "tushare",
		DataType: dataprotocol.DataTypeOHLCDaily, Data: data,
		IngestTime: fixedClock(), VisibleAt: visible,
	}, Project(src))

	assert.NotNil(t, ProjectAll(nil), "空投影返回空切片而不是 nil：让「没东西可写」可判定")
	assert.Len(t, ProjectAll(nil), 0)
	assert.Len(t, ProjectAll([]ingest.UnifiedDataPoint{src, src}), 2)
}

// 投送面上没有 visible_at / ingest_time：结构体里就没有这两个字段，且解码
// 拒收未知字段（这一条不成立，闸门就拦不住自称「我可见」的批次）。
func TestWirePointCarriesNoServerOwnedFields(t *testing.T) {
	raw, err := json.Marshal(Point{Symbol: "600000.SH", TradeTime: time.Unix(0, 0).UTC(), Source: "fake"})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "visible_at")
	assert.NotContains(t, string(raw), "ingest_time")

	var decoded Request
	dec := json.NewDecoder(strings.NewReader(
		`{"data_type":"ohlcv_daily","points":[{"symbol":"600000.SH","trade_time":"2026-01-15T00:00:00Z","source":"fake","data":{},"visible_at":"2020-01-01T00:00:00Z"}]}`))
	dec.DisallowUnknownFields()
	require.Error(t, dec.Decode(&decoded), "投送 visible_at 必须解码失败")
}

// 两条入口的写侧次序必须一致：HTTP 投点与作业管线对同一批数据要产出逐字段
// 相同的 store.Point 批次。分叉了就是「两个真相」在本仓内部复活。
func TestWriteAndPipelineAgreeOnTheSameBatch(t *testing.T) {
	tradeTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	data := map[string]any{"close": json.Number("12.5")}
	decl := afterCloseDecl(dataprotocol.DataTypeOHLCDaily, ingest.VisibleFieldTradeTime)

	jobReg := ingest.NewRegistry()
	require.NoError(t, jobReg.Register(&pipelineConnector{
		AdapterBase: ingest.NewAdapterBase("fake", true),
		decl:        decl,
		items:       []ingest.DataItem{{Symbol: "600000.SH", TradeTime: tradeTime, Data: data}},
	}))
	jobSink := &recordingSink{}
	_, err := Pipeline(jobReg, jobSink, fixedClock).Process(context.Background(),
		ingest.FetchRequest{DataType: dataprotocol.DataTypeOHLCDaily},
		func(item ingest.DataItem, source, dataType string) ingest.UnifiedDataPoint {
			return ingest.NewUnifiedDataPoint(item.Symbol, source, dataType, item.TradeTime, time.Time{}, item.Data)
		})
	require.NoError(t, err)

	httpSink := &recordingSink{}
	w, err := New(testRegistry(t), httpSink, fixedClock)
	require.NoError(t, err)
	_, err = w.Write(context.Background(), Request{
		DataType: dataprotocol.DataTypeOHLCDaily,
		Points:   []Point{{Symbol: "600000.SH", TradeTime: tradeTime, Source: "fake", Data: data}},
	})
	require.NoError(t, err)

	require.Len(t, jobSink.batches, 1)
	require.Len(t, httpSink.batches, 1)
	assert.Equal(t, jobSink.batches[0], httpSink.batches[0], "同一批数据在两条入口上必须落成同一批写面点")

	jobJSON, err := json.Marshal(jobSink.batches[0])
	require.NoError(t, err)
	httpJSON, err := json.Marshal(httpSink.batches[0])
	require.NoError(t, err)
	assert.Equal(t, string(jobJSON), string(httpJSON), "逐字节一致才算口径没漂移")
}

// pipelineConnector 是实现运行时面的假门（Pipeline 要 Fetch 得动）。
type pipelineConnector struct {
	ingest.AdapterBase
	decl  ingest.ConnectorDeclaration
	items []ingest.DataItem
}

func (c *pipelineConnector) SupportedTypes() []string { return []string{c.decl.DataType} }
func (c *pipelineConnector) Declarations() []ingest.ConnectorDeclaration {
	return []ingest.ConnectorDeclaration{c.decl}
}
func (c *pipelineConnector) Schema(string) (ingest.DataSchema, error) {
	return ingest.DataSchema{DataType: c.decl.DataType}, nil
}
func (c *pipelineConnector) Type() ingest.AdapterType          { return ingest.AdapterTypeHTTP }
func (c *pipelineConnector) RateLimit() ingest.RateLimitConfig { return ingest.RateLimitConfig{} }
func (c *pipelineConnector) HealthCheck(context.Context) error { return nil }
func (c *pipelineConnector) Fetch(context.Context, ingest.FetchRequest) (*ingest.FetchResponse, error) {
	return &ingest.FetchResponse{Source: c.Name(), Items: append([]ingest.DataItem(nil), c.items...)}, nil
}

// 声明面在零传输下仍然完整：写端点靠的就是这一条，而取数面必须明确不可用。
func TestDeclarationsRegistryHoldsDeclarationsButNotTransports(t *testing.T) {
	reg, err := DeclarationsRegistry()
	require.NoError(t, err, "传输 = nil 的门必须能登记：写端点只消费声明，不取数")

	for _, dataType := range []string{
		dataprotocol.DataTypeOHLCDaily, dataprotocol.DataTypeCapitalFlow,
		dataprotocol.DataTypeTopList, dataprotocol.DataTypeAnnounce,
	} {
		decl, err := reg.DeclarationFor(dataType)
		require.NoError(t, err, dataType)
		assert.NotEmpty(t, decl.VisibleField, dataType, "锚点列为空 ⇒ 闸门会当场拒收，这里要早暴露")
	}

	_, err = reg.Fetch(context.Background(), ingest.FetchRequest{DataType: dataprotocol.DataTypeOHLCDaily})
	require.Error(t, err, "传输仍在 core：这里必须失败，不许假装有数")
}

// WireError 是写侧唯一的分诊表：每个失败都要有码，且一码一义。
func TestWireErrorCoversEveryWriteFailure(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: x", ErrBatchTooLarge), http.StatusRequestEntityTooLarge, "batch_too_large"},
		{fmt.Errorf("%w: x", ErrInvalidBatch), http.StatusBadRequest, "invalid_batch"},
		{fmt.Errorf("%w: x", normalize.ErrVisibleAtMissing), http.StatusBadRequest, "visible_at_missing"},
		{fmt.Errorf("%w: x", store.ErrVisibleAtMissing), http.StatusBadRequest, "visible_at_missing"},
		{fmt.Errorf("%w: x", ingest.ErrAnchorInvalid), http.StatusBadRequest, "anchor_invalid"},
		{fmt.Errorf("%w: x", ingest.ErrDeclarationMissing), http.StatusBadRequest, "data_type_undeclared"},
		{fmt.Errorf("%w: x", ingest.ErrDeclarationInvalid), http.StatusInternalServerError, "declaration_invalid"},
		{fmt.Errorf("%w: x", store.ErrWriteTableNotInASet), http.StatusBadRequest, "write_table_not_in_a_set"},
		{fmt.Errorf("%w: x", store.ErrUnmappedDataType), http.StatusBadRequest, "data_type_unmapped"},
		{fmt.Errorf("%w: x", store.ErrInvalidPoint), http.StatusBadRequest, "invalid_point"},
		{fmt.Errorf("%w: x", store.ErrContentConflict), http.StatusConflict, "content_conflict"},
	}
	seen := map[string]int{}
	for _, tc := range cases {
		status, code, ok := WireError(tc.err)
		require.True(t, ok, "%v 未登记分诊", tc.err)
		assert.Equal(t, tc.status, status, "%v", tc.err)
		assert.Equal(t, tc.code, code, "%v", tc.err)
		seen[code]++
	}
	assert.Equal(t, 2, seen["visible_at_missing"], "store 与 normalize 的同一个哨兵必须落同一码")

	status, _, ok := WireError(errors.New("assembly: something nobody predicted"))
	assert.False(t, ok, "未知失败不许被伪装成 4xx")
	assert.Zero(t, status)
}
