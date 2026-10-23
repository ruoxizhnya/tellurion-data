package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdapter 是取数面的测试替身：注册面 + 运行时面都实现，便于测链与管线。
type fakeAdapter struct {
	AdapterBase
	types   []string
	decls   []ConnectorDeclaration
	schemas map[string]DataSchema
	items   []DataItem
	err     error
	calls   int
}

func (f *fakeAdapter) SupportedTypes() []string { return append([]string(nil), f.types...) }
func (f *fakeAdapter) Declarations() []ConnectorDeclaration {
	return append([]ConnectorDeclaration(nil), f.decls...)
}

func (f *fakeAdapter) Schema(dataType string) (DataSchema, error) {
	if s, ok := f.schemas[dataType]; ok {
		return s, nil
	}
	return DataSchema{DataType: dataType}, nil
}

func (f *fakeAdapter) Type() AdapterType { return AdapterTypeHTTP }
func (f *fakeAdapter) RateLimit() RateLimitConfig {
	return RateLimitConfig{}
}
func (f *fakeAdapter) HealthCheck(context.Context) error { return nil }
func (f *fakeAdapter) Fetch(context.Context, FetchRequest) (*FetchResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &FetchResponse{Source: f.Name(), Items: append([]DataItem(nil), f.items...)}, nil
}

// fakeStore 是写面 port 的替身：记录调用与收到的点。
type fakeStore struct {
	calls     int
	points    []UnifiedDataPoint
	dataType  string
	persisted int
	skipped   int
	err       error
}

func (f *fakeStore) BulkInsert(_ context.Context, dataType string, points []UnifiedDataPoint) (int, int, error) {
	f.calls++
	f.dataType = dataType
	f.points = append([]UnifiedDataPoint(nil), points...)
	if f.err != nil {
		return 0, 0, f.err
	}
	if f.persisted == 0 && f.skipped == 0 {
		return len(points), 0, nil
	}
	return f.persisted, f.skipped, nil
}

func newFakeAdapter(name string, dataType string, decl ConnectorDeclaration, items ...DataItem) *fakeAdapter {
	return &fakeAdapter{
		AdapterBase: NewAdapterBase(name, true),
		types:       []string{dataType},
		decls:       []ConnectorDeclaration{decl},
		items:       items,
	}
}

func tradeTimePoint(symbol string, data map[string]interface{}) DataItem {
	return DataItem{
		Symbol:    symbol,
		TradeTime: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Data:      data,
	}
}

func passthroughNormalizer(item DataItem, source, dataType string) UnifiedDataPoint {
	return NewUnifiedDataPoint(item.Symbol, source, dataType, item.TradeTime, time.Time{}, item.Data)
}

// 正常腿：锚点存在 ⇒ 点被闸门落 VisibleAt（取自 TradeTime）、IngestTime
// 取自注入时钟、去重后写面只收到唯一一条。
func TestETLProcess_GatesAndWritesDeduplicatedPoints(t *testing.T) {
	decl := validDecl(DataTypeOHLCDaily)
	adapter := newFakeAdapter("src", DataTypeOHLCDaily, decl,
		tradeTimePoint("600519.SH", map[string]interface{}{"close": 10.0}),
		tradeTimePoint("600519.SH", map[string]interface{}{"close": 11.0}), // 同键重复
		tradeTimePoint("000001.SZ", map[string]interface{}{"close": 12.0}),
	)
	reg := NewRegistry()
	require.NoError(t, reg.Register(adapter))

	store := &fakeStore{}
	clock := func() time.Time { return time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC) }
	pipeline := NewETLPipeline(reg, store).WithClock(clock)

	result, err := pipeline.Process(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily}, passthroughNormalizer)
	require.NoError(t, err)

	assert.Equal(t, 3, result.Fetched)
	assert.Equal(t, 2, result.Persisted, "同 symbol/同 trade_time/data_type 的重复点必须去掉")
	assert.Equal(t, 1, result.Skipped)
	assert.Equal(t, DataTypeOHLCDaily, result.DataType)
	assert.Equal(t, "src", result.Source)

	require.Equal(t, 1, store.calls)
	require.Len(t, store.points, 2)
	for _, p := range store.points {
		assert.False(t, p.VisibleAt.IsZero(), "写面收到的点必须已落可见时刻")
		assert.Equal(t, clock(), p.IngestTime, "IngestTime 必须来自注入时钟")
	}
	assert.Equal(t, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), store.points[0].VisibleAt)
}

// 漏锚点 ⇒ 整批拒收，**store 一次都不许被调用**。这是 D3 的核心用例：
// 若有人删掉 GatePoints（或把它挪到 store 之后），这条会红。
func TestETLProcess_MissingVisibleAnchorRejectsWholeBatchWithoutWriting(t *testing.T) {
	decl := validDecl(DataTypeCapitalFlow)
	decl.VisibleField = "trade_date"
	adapter := newFakeAdapter("moneyflow", DataTypeCapitalFlow, decl,
		tradeTimePoint("600519.SH", map[string]interface{}{"trade_date": "2026-01-15"}),
		tradeTimePoint("000001.SZ", map[string]interface{}{"main_net": 1.0}), // 漏锚点
	)
	adapter.schemas = map[string]DataSchema{
		DataTypeCapitalFlow: {DataType: DataTypeCapitalFlow, Fields: []SchemaField{{Name: "trade_date", Type: "date"}}},
	}
	reg := NewRegistry()
	require.NoError(t, reg.Register(adapter))

	store := &fakeStore{}
	pipeline := NewETLPipeline(reg, store)

	_, err := pipeline.Process(context.Background(), FetchRequest{DataType: DataTypeCapitalFlow}, passthroughNormalizer)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVisibleAtMissing)
	assert.Zero(t, store.calls, "整批拒收时写面不得被调用（不许写半批）")
}

// 声明缺失的类型在取数前就拒：不浪费一次上游调用，也不写。
func TestETLProcess_UndeclaredDataTypeFailsBeforeFetch(t *testing.T) {
	reg := NewRegistry()
	store := &fakeStore{}
	pipeline := NewETLPipeline(reg, store)

	_, err := pipeline.Process(context.Background(), FetchRequest{DataType: DataTypeHotSearch}, passthroughNormalizer)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeclarationMissing)
	assert.Zero(t, store.calls)
}

// 装配错误必须显式报错，不许「nil store 静默丢数据」。
func TestETLProcess_MisconfiguredPipeline(t *testing.T) {
	reg := NewRegistry()
	decl := validDecl(DataTypeOHLCDaily)
	require.NoError(t, reg.Register(newFakeAdapter("src", DataTypeOHLCDaily, decl)))

	_, err := NewETLPipeline(reg, nil).Process(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily}, passthroughNormalizer)
	require.ErrorIs(t, err, ErrPipelineMisconfigured)

	_, err = NewETLPipeline(reg, &fakeStore{}).Process(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily}, nil)
	require.ErrorIs(t, err, ErrPipelineMisconfigured)

	_, err = NewETLPipeline(nil, &fakeStore{}).Process(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily}, passthroughNormalizer)
	require.ErrorIs(t, err, ErrPipelineMisconfigured)
}

// 载荷为 nil 的点由管线计为 skipped，不进闸门。
func TestETLProcess_SkipsNilPayload(t *testing.T) {
	decl := validDecl(DataTypeOHLCDaily)
	adapter := newFakeAdapter("src", DataTypeOHLCDaily, decl,
		tradeTimePoint("600519.SH", nil),
		tradeTimePoint("000001.SZ", map[string]interface{}{"close": 1.0}),
	)
	reg := NewRegistry()
	require.NoError(t, reg.Register(adapter))
	store := &fakeStore{}

	result, err := NewETLPipeline(reg, store).Process(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily}, passthroughNormalizer)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Fetched)
	assert.Equal(t, 1, result.Persisted)
	assert.Equal(t, 1, result.Skipped)
	require.Len(t, store.points, 1)
}

// ─── 兜底链 ──────────────────────────────────────────────────────────

func TestRegistryFetch_FallsBackOnRetryableError(t *testing.T) {
	decl := validDecl(DataTypeOHLCDaily)
	primary := newFakeAdapter("primary", DataTypeOHLCDaily, decl)
	primary.err = ErrUpstreamUnavailable
	secondary := newFakeAdapter("secondary", DataTypeOHLCDaily, decl,
		tradeTimePoint("600519.SH", map[string]interface{}{"close": 1.0}))

	reg := NewRegistry()
	require.NoError(t, reg.Register(primary))
	require.NoError(t, reg.Register(secondary))

	resp, err := reg.Fetch(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily})
	require.NoError(t, err)
	assert.Equal(t, "secondary", resp.Source)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 1, secondary.calls)
}

func TestRegistryFetch_DoesNotFallBackOnPermanentError(t *testing.T) {
	decl := validDecl(DataTypeOHLCDaily)
	primary := newFakeAdapter("primary", DataTypeOHLCDaily, decl)
	primary.err = errors.New("bad request")
	secondary := newFakeAdapter("secondary", DataTypeOHLCDaily, decl)

	reg := NewRegistry()
	require.NoError(t, reg.Register(primary))
	require.NoError(t, reg.Register(secondary))

	_, err := reg.Fetch(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily})
	require.Error(t, err)
	assert.Equal(t, 0, secondary.calls, "非可重试错误不许换源（换源治不好参数错误）")
}

func TestRegistryFetch_SkipsDisabledAdapter(t *testing.T) {
	decl := validDecl(DataTypeOHLCDaily)
	disabled := newFakeAdapter("disabled", DataTypeOHLCDaily, decl)
	disabled.SetEnabled(false)
	enabled := newFakeAdapter("enabled", DataTypeOHLCDaily, decl)

	reg := NewRegistry()
	require.NoError(t, reg.Register(disabled))
	require.NoError(t, reg.Register(enabled))

	resp, err := reg.Fetch(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily})
	require.NoError(t, err)
	assert.Equal(t, "enabled", resp.Source)
	assert.Zero(t, disabled.calls)
}

func TestRegistryFetch_EmptyChainIsUnsupported(t *testing.T) {
	_, err := NewRegistry().Fetch(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily})
	require.ErrorIs(t, err, ErrUnsupported)
}

func TestRegistryFetch_AllDisabledFailsLoud(t *testing.T) {
	decl := validDecl(DataTypeOHLCDaily)
	a := newFakeAdapter("a", DataTypeOHLCDaily, decl)
	a.SetEnabled(false)
	reg := NewRegistry()
	require.NoError(t, reg.Register(a))

	_, err := reg.Fetch(context.Background(), FetchRequest{DataType: DataTypeOHLCDaily})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
}

// DeclarationFor 必须可复现：多个源服务同一类型时按名字排序取第一条；
// 两条声明的可见锚点冲突时 fail-loud，而不是随机挑一个。
func TestDeclarationFor_IsDeterministicAndRejectsConflicts(t *testing.T) {
	decl := validDecl(DataTypeOHLCDaily)
	a := newFakeAdapter("a_source", DataTypeOHLCDaily, decl)
	b := newFakeAdapter("b_source", DataTypeOHLCDaily, decl)
	reg := NewRegistry()
	require.NoError(t, reg.Register(b))
	require.NoError(t, reg.Register(a))

	got, err := reg.DeclarationFor(DataTypeOHLCDaily)
	require.NoError(t, err)
	assert.Equal(t, decl.VisibleField, got.VisibleField)
	for i := 0; i < 20; i++ {
		again, err := reg.DeclarationFor(DataTypeOHLCDaily)
		require.NoError(t, err)
		assert.Equal(t, got.VisibleField, again.VisibleField)
		assert.Equal(t, got.TimeSemantics, again.TimeSemantics)
	}

	conflicting := validDecl(DataTypeOHLCDaily)
	conflicting.TimeSemantics = market.VisibleCustom
	conflicting.VisibleExpr = "now()"
	reg2 := NewRegistry()
	require.NoError(t, reg2.Register(newFakeAdapter("a_source", DataTypeOHLCDaily, decl)))
	require.NoError(t, reg2.Register(newFakeAdapter("b_source", DataTypeOHLCDaily, conflicting)))
	_, err = reg2.DeclarationFor(DataTypeOHLCDaily)
	require.ErrorIs(t, err, ErrDeclarationMismatch)
}
