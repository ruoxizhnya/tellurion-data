package ingest

import (
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gateDecl(dataType, field string) ConnectorDeclaration {
	return ConnectorDeclaration{
		DataType:      dataType,
		TimeSemantics: market.VisibleAfterClose,
		VisibleField:  field,
		Frequency:     market.FreqSpecDaily,
	}
}

func gatePoint(symbol string, data map[string]interface{}) UnifiedDataPoint {
	return UnifiedDataPoint{
		Symbol:     symbol,
		TradeTime:  time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Source:     "tushare",
		DataType:   "capital_flow",
		Data:       data,
		IngestTime: time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
	}
}

// 锚点列有值：闸门放行并落 VisibleAt。
func TestGatePointsAcceptsWhenDeclaredAnchorPresent(t *testing.T) {
	decl := gateDecl("capital_flow", "trade_date")
	points := []UnifiedDataPoint{gatePoint("600519.SH", map[string]interface{}{
		"trade_date": "2026-01-15",
	})}
	gated, err := GatePoints(decl, points)
	require.NoError(t, err)
	require.Len(t, gated, 1)
	assert.Equal(t, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), gated[0].VisibleAt)
}

// 锚点列缺失 ⇒ 整批拒收，且**不许回落到其他任何列**：
// available_date（COALESCE 生成列，§6.3 点名的假护栏）即使非空也救不了这一批。
func TestGatePointsRejectsMissingAnchorEvenWhenGeneratedColumnPresent(t *testing.T) {
	decl := gateDecl("fundamentals", "ann_date")
	points := []UnifiedDataPoint{gatePoint("600519.SH", map[string]interface{}{
		"trade_date":     "2026-01-15",
		"available_date": "2026-01-15",
	})}
	_, err := GatePoints(decl, points)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVisibleAtMissing)
}

// 第二条腿（§6.6 ②）：判据必须是「声明的那一列」。锚点列在但为空串 / NULL，
// 同时另一列有合法时间 ⇒ 仍然拒收 —— 若有人把判据偷换成「任一列非空」，
// 这条测试变红。
func TestGatePointsRejectsEmptyAnchorEvenWhenOtherDatetimeColumnPresent(t *testing.T) {
	decl := gateDecl("fundamentals", "ann_date")
	for name, anchor := range map[string]interface{}{
		"empty string": "",
		"JSON null":    nil,
		"absent":       nil,
	} {
		data := map[string]interface{}{
			"ann_date":       anchor,
			"available_date": "2026-01-15",
		}
		if name == "absent" {
			delete(data, "ann_date")
		}
		_, err := GatePoints(decl, []UnifiedDataPoint{gatePoint("600519.SH", data)})
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ErrVisibleAtMissing, name)
	}
}

// 缺锚点拒收与坏锚点拒收是两个错误：坏值要修数据，缺列要修采集。
func TestGatePointsDistinguishesUnparsableAnchorFromMissing(t *testing.T) {
	decl := gateDecl("capital_flow", "trade_date")
	_, err := GatePoints(decl, []UnifiedDataPoint{gatePoint("600519.SH", map[string]interface{}{
		"trade_date": "not-a-date",
	})})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAnchorInvalid)
	assert.NotErrorIs(t, err, ErrVisibleAtMissing)
}

// 保留锚点 trade_time：TradeTime 即可见时刻；零值拒收。
func TestGatePointsReservedTradeTimeAnchor(t *testing.T) {
	decl := gateDecl("ohlcv_daily", VisibleFieldTradeTime)

	valid := []UnifiedDataPoint{{Symbol: "600519.SH", TradeTime: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)}}
	gated, err := GatePoints(decl, valid)
	require.NoError(t, err)
	assert.Equal(t, valid[0].TradeTime, gated[0].VisibleAt)

	_, err = GatePoints(decl, []UnifiedDataPoint{{Symbol: "600519.SH"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVisibleAtMissing)
}

// 整批语义：批内一条违规，整批不得放行（不许「少一条进统计」）。
func TestGatePointsRejectsWholeBatchOnSingleOffender(t *testing.T) {
	decl := gateDecl("capital_flow", "trade_date")
	points := []UnifiedDataPoint{
		gatePoint("600519.SH", map[string]interface{}{"trade_date": "2026-01-15"}),
		gatePoint("000001.SZ", map[string]interface{}{"trade_date": "2026-01-15"}),
		gatePoint("000002.SZ", map[string]interface{}{}),
	}
	gated, err := GatePoints(decl, points)
	require.Error(t, err)
	assert.Nil(t, gated)
	assert.ErrorIs(t, err, ErrVisibleAtMissing)
	assert.Contains(t, err.Error(), "000002.SZ")
}

// 输入不被修改：闸门写的是拷贝的 VisibleAt。
func TestGatePointsDoesNotMutateInput(t *testing.T) {
	decl := gateDecl("capital_flow", "trade_date")
	in := []UnifiedDataPoint{gatePoint("600519.SH", map[string]interface{}{"trade_date": "2026-01-15"})}
	_, err := GatePoints(decl, in)
	require.NoError(t, err)
	assert.True(t, in[0].VisibleAt.IsZero())
}

// 不声明锚点列的声明本身不配当闸门输入（注册门应当拦住它，这里双保险）。
func TestGatePointsRejectsUndeclaredAnchor(t *testing.T) {
	decl := gateDecl("capital_flow", "")
	_, err := GatePoints(decl, []UnifiedDataPoint{gatePoint("600519.SH", map[string]interface{}{"trade_date": "2026-01-15"})})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeclarationMismatch)
}

// 注册表按 data_type 取声明：没登记的类型 fail-closed，不给放行。
func TestRegistryDeclarationFor(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register(&catalogConnector{name: "eastmoney",
		types: []string{dataTypeCapitalFlow},
		decls: []ConnectorDeclaration{gateDecl("capital_flow", VisibleFieldTradeTime)},
		anchors: map[string]DataSchema{
			dataTypeCapitalFlow: anchorSchema(dataTypeCapitalFlow, "trade_date", "date"),
		},
	}))

	decl, err := reg.DeclarationFor("capital_flow")
	require.NoError(t, err)
	assert.Equal(t, "capital_flow", decl.DataType)

	_, err = reg.DeclarationFor("hot_search")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDeclarationMissing)
}
