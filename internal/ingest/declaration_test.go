package ingest

import (
	"fmt"
	"testing"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/require"
)

// stubConnector 是注册门的测试替身：只实现注册所需的窄接口。
type stubConnector struct {
	name    string
	types   []string
	decls   []ConnectorDeclaration
	schemas map[string]DataSchema
}

func (s *stubConnector) Name() string             { return s.name }
func (s *stubConnector) SupportedTypes() []string { return append([]string(nil), s.types...) }
func (s *stubConnector) Declarations() []ConnectorDeclaration {
	return append([]ConnectorDeclaration(nil), s.decls...)
}

func (s *stubConnector) Schema(dataType string) (DataSchema, error) {
	schema, ok := s.schemas[dataType]
	if !ok {
		return DataSchema{}, fmt.Errorf("stub: no schema for %q", dataType)
	}
	return schema, nil
}

func validDecl(dataType string) ConnectorDeclaration {
	return ConnectorDeclaration{
		DataType:      dataType,
		TimeSemantics: market.VisibleAfterClose,
		VisibleField:  VisibleFieldTradeTime,
		Frequency:     market.FreqSpecDaily,
	}
}

func stub(name string, types []string, decls []ConnectorDeclaration, schemas map[string]DataSchema) *stubConnector {
	return &stubConnector{name: name, types: types, decls: decls, schemas: schemas}
}

func TestRegistry_AcceptsValidConnector(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register(stub("ok", []string{"a", "b"}, []ConnectorDeclaration{validDecl("a"), validDecl("b")}, nil)))

	got, err := reg.Get("ok")
	require.NoError(t, err)
	require.Equal(t, "ok", got.Name())
	require.Equal(t, []string{"ok"}, reg.Names())
}

func TestRegistry_RejectsMissingDeclaration(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(stub("partial", []string{"a", "b"}, []ConnectorDeclaration{validDecl("a")}, nil))
	require.ErrorIs(t, err, ErrDeclarationMissing)
	require.Contains(t, err.Error(), `"b"`)
}

func TestRegistry_RejectsDuplicateDeclaration(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(stub("dup", []string{"a"}, []ConnectorDeclaration{validDecl("a"), validDecl("a")}, nil))
	require.ErrorIs(t, err, ErrDeclarationInvalid)
}

func TestRegistry_RejectsDeclarationForUnsupportedType(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(stub("extra", []string{"a"}, []ConnectorDeclaration{validDecl("a"), validDecl("b")}, nil))
	require.ErrorIs(t, err, ErrDeclarationInvalid)
	require.Contains(t, err.Error(), `"b"`)
}

func TestRegistry_RejectsUnknownTimeSemantics(t *testing.T) {
	reg := NewRegistry()
	decl := validDecl("a")
	decl.TimeSemantics = "whenever"
	err := reg.Register(stub("bad-semantics", []string{"a"}, []ConnectorDeclaration{decl}, nil))
	require.ErrorIs(t, err, ErrDeclarationInvalid)
}

func TestRegistry_RejectsCustomWithoutExpression(t *testing.T) {
	reg := NewRegistry()
	decl := validDecl("a")
	decl.TimeSemantics = market.VisibleCustom
	err := reg.Register(stub("custom-no-expr", []string{"a"}, []ConnectorDeclaration{decl}, nil))
	require.ErrorIs(t, err, ErrDeclarationInvalid)
	require.Contains(t, err.Error(), "VisibleExpr")
}

func TestRegistry_RejectsUnknownFrequencySpec(t *testing.T) {
	reg := NewRegistry()
	decl := validDecl("a")
	decl.Frequency = ""
	err := reg.Register(stub("no-freq", []string{"a"}, []ConnectorDeclaration{decl}, nil))
	require.ErrorIs(t, err, ErrDeclarationInvalid)
}

func TestRegistry_RejectsEmptyVisibleField(t *testing.T) {
	reg := NewRegistry()
	decl := validDecl("a")
	decl.VisibleField = " "
	err := reg.Register(stub("no-anchor", []string{"a"}, []ConnectorDeclaration{decl}, nil))
	require.ErrorIs(t, err, ErrDeclarationMismatch)
}

func TestRegistry_RejectsNamedAnchorMissingFromSchema(t *testing.T) {
	reg := NewRegistry()
	decl := validDecl("a")
	decl.VisibleField = "ann_time"
	err := reg.Register(stub("anchor-missing", []string{"a"}, []ConnectorDeclaration{decl}, map[string]DataSchema{
		"a": {DataType: "a", Fields: []SchemaField{{Name: "trade_date", Type: "date"}}},
	}))
	require.ErrorIs(t, err, ErrDeclarationMismatch)
	require.Contains(t, err.Error(), "ann_time")
}

func TestRegistry_RejectsNamedAnchorWithNonDateType(t *testing.T) {
	reg := NewRegistry()
	decl := validDecl("a")
	decl.VisibleField = "ann_time"
	err := reg.Register(stub("anchor-type", []string{"a"}, []ConnectorDeclaration{decl}, map[string]DataSchema{
		"a": {DataType: "a", Fields: []SchemaField{{Name: "ann_time", Type: "string"}}},
	}))
	require.ErrorIs(t, err, ErrDeclarationMismatch)
	require.Contains(t, err.Error(), "string")
}

func TestRegistry_AcceptsReservedAnchorWithoutProjection(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register(stub("reserved", []string{"a"}, []ConnectorDeclaration{validDecl("a")}, nil)))
}

func TestRegistry_RejectsReservedAnchorProjectedWithWrongType(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(stub("reserved-bad", []string{"a"}, []ConnectorDeclaration{validDecl("a")}, map[string]DataSchema{
		"a": {DataType: "a", Fields: []SchemaField{{Name: VisibleFieldTradeTime, Type: "string"}}},
	}))
	require.ErrorIs(t, err, ErrDeclarationMismatch)
}

func TestRegistry_RejectsEmptyNameAndNilConnector(t *testing.T) {
	reg := NewRegistry()
	require.ErrorIs(t, reg.Register(nil), ErrDeclarationInvalid)
	require.ErrorIs(t, reg.Register(stub("  ", []string{"a"}, []ConnectorDeclaration{validDecl("a")}, nil)), ErrDeclarationInvalid)
}

func TestRegistry_RejectsDuplicateRegistration(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register(stub("same", []string{"a"}, []ConnectorDeclaration{validDecl("a")}, nil)))
	err := reg.Register(stub("same", []string{"a"}, []ConnectorDeclaration{validDecl("a")}, nil))
	require.ErrorIs(t, err, ErrAlreadyRegistered)
}

func TestRegistry_GetUnknown(t *testing.T) {
	_, err := NewRegistry().Get("nope")
	require.ErrorIs(t, err, ErrNotRegistered)
}

// ─── 12 个 connector 的声明（真源 = 各自的 Declarations()） ──────────
//
// 过渡期的 `Catalog()` 清单已删除：它手抄一份声明，而实现自己也有一份，
// 两份迟早漂移，且漂移方向恰好是「注册门放行了一个实际没声明的源」。
// 下面这些测试跑在**真实 connector** 上（传输注入 nil ⇒ 门 disabled，
// 但声明与 schema 仍然完整，正是注册门要检查的部分）。

func registerAll(t *testing.T, reg *Registry) {
	t.Helper()
	require.NoError(t, RegisterConnectors(reg, Transports{}))
}

func TestConnectors_AllPassTheRegistrationGate(t *testing.T) {
	reg := NewRegistry()
	registerAll(t, reg)

	require.Equal(t, []string{
		"alpha_vantage",
		"eastmoney",
		"eastmoney_sectors",
		"eastmoney_toplist",
		"juchao",
		"mootdx",
		"tushare",
		"tushare_daily_basic",
		"tushare_macro",
		"tushare_moneyflow",
		"xueqiu",
		"yahoo_finance",
	}, reg.Names(), "12 个 connector 的名字是跨仓契约，改名会静默改变注册面")
}

// 18 条声明 / 15 个 wire data_type：与 core `pkg/data/source/declarations.go`
// 逐条对齐。数字变了就是声明面变了，必须同步文档与 core。
func TestConnectors_CoverAllWireDataTypes(t *testing.T) {
	covered := map[string]bool{}
	total := 0
	for _, c := range Connectors(Transports{}) {
		for _, d := range c.Declarations() {
			covered[d.DataType] = true
			total++
		}
	}

	require.Equal(t, 18, total, "18 条声明（与 core declarations.go 逐条对齐）")
	expected := []string{
		DataTypeOHLCDaily, DataTypeOHLCMinute, DataTypeRealtime, DataTypeCapitalFlow,
		DataTypeFundamental, DataTypeSectors, DataTypeStockSector, DataTypeTopList,
		DataTypeLimitUpPool, DataTypeAnnounce, DataTypeNews, DataTypeHotSearch,
		DataTypeGlobalOHLCV, DataTypeMacro, DataTypeDailyBasic,
	}
	require.Len(t, covered, len(expected), "15 个 wire data_type 全部有声明，且不引入仓外新类型")
	for _, dt := range expected {
		require.True(t, covered[dt], "data type %q must be declared by some connector", dt)
	}
}

func TestConnectors_FundamentalAnchorIsAnnDateNotTradeTime(t *testing.T) {
	for _, conn := range Connectors(Transports{}) {
		if conn.Name() != "tushare" {
			continue
		}
		for _, d := range conn.Declarations() {
			if d.DataType != DataTypeFundamental {
				continue
			}
			require.Equal(t, VisibleFieldAnnDate, d.VisibleField,
				"财报的 TradeTime 承载报告期截止日；锚点必须是披露日，否则前视洞回来")
			require.Equal(t, market.VisibleOnAnnouncementDay, d.TimeSemantics)
			return
		}
	}
	t.Fatal("tushare must declare fundamentals")
}

func TestConnectors_MacroAnchorIsAsOf(t *testing.T) {
	for _, conn := range Connectors(Transports{}) {
		if conn.Name() != "tushare_macro" {
			continue
		}
		require.Len(t, conn.Declarations(), 1)
		d := conn.Declarations()[0]
		require.Equal(t, DataTypeMacro, d.DataType)
		require.Equal(t, "as_of", d.VisibleField,
			"观测日 obs_date 早于真实发布日；锚点必须是导入时刻 as_of（2026-10-16 裁决 A）")
		return
	}
	t.Fatal("tushare_macro must be in the catalog")
}

func TestConnectors_ReturnIndependentCopies(t *testing.T) {
	first := Connectors(Transports{})
	decls := first[0].Declarations()
	decls[0].DataType = "hacked"
	decls[0].VisibleField = "hacked"

	second := Connectors(Transports{})
	require.NotEqual(t, "hacked", second[0].Declarations()[0].DataType)
	require.NotEqual(t, "hacked", second[0].Declarations()[0].VisibleField)

	types := second[1].SupportedTypes()
	types[0] = "hacked"
	require.NotEqual(t, "hacked", Connectors(Transports{})[1].SupportedTypes()[0])
}

// 声明清单必须是 connector 自己产出的，不能有第二个来源：把 12 个门的
// 声明逐条与「同名 wire 契约」对比，任一门的 SupportedTypes 与其声明集合
// 不等即红（这正是删掉 Catalog() 后要保住的判据）。
func TestConnectors_DeclarationsMatchSupportedTypes(t *testing.T) {
	for _, c := range Connectors(Transports{}) {
		supported := map[string]bool{}
		for _, dt := range c.SupportedTypes() {
			supported[dt] = true
		}
		declared := map[string]bool{}
		for _, d := range c.Declarations() {
			declared[d.DataType] = true
		}
		require.Equal(t, supported, declared, "connector %s 的 SupportedTypes 与 Declarations 必须一一对应", c.Name())
	}
}
