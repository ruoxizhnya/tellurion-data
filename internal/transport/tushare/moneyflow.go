package tushare

// 资金流分档净额取数腿 —— 对齐 core `pkg/data/tushare_moneyflow.go`
// （`moneyflow` → `capital_flow`）。本包只取数 + 归一，不落库。
//
// 口径映射（「字段对不齐就落 NULL，不硬凑」）：
//
//	特大单净额 → super_net     大单净额 → large_net
//	中单净额   → medium_net    小单净额 → small_net
//
// 四档净额各取**同档** buy − sell，单位万元 → 元（CNY）×10000。
// 刻意不映射 `net_mf_amount`：实测同一天四档 (buy−sell) 之和为 0（买卖必
// 相等，结构性抵消），而 net_mf_amount 非 0 —— 口径无从确证，拿它填东财
// 含义的列就是硬凑。

import (
	"context"
	"strings"
	"time"

	"tellurion-data/internal/ingest"
)

// cnyPerWanYuanUnit 把资金流接口的「万元」折成元（CNY）。
const cnyPerWanYuanUnit = 10000.0

// moneyflowFields 只取身份列 + 四档买卖额（不请求 net_mf_amount / *_vol）。
const moneyflowFields = "ts_code,trade_date," +
	"buy_elg_amount,sell_elg_amount," +
	"buy_lg_amount,sell_lg_amount," +
	"buy_md_amount,sell_md_amount," +
	"buy_sm_amount,sell_sm_amount"

// moneyflowTier 是一个档位在 capital_flow 的净额列名 + 源侧买卖列名。
type moneyflowTier struct {
	netColumn string
	buyField  string
	sellField string
}

// moneyflowTiers 是四档映射（顺序只影响报错文案的稳定性，解包按列名）。
var moneyflowTiers = []moneyflowTier{
	{netColumn: "super_net", buyField: "buy_elg_amount", sellField: "sell_elg_amount"},
	{netColumn: "large_net", buyField: "buy_lg_amount", sellField: "sell_lg_amount"},
	{netColumn: "medium_net", buyField: "buy_md_amount", sellField: "sell_md_amount"},
	{netColumn: "small_net", buyField: "buy_sm_amount", sellField: "sell_sm_amount"},
}

// moneyflowSkipped 是 normalize 的跳过计数（NilTier 照常入库，不计入 Skipped）。
type moneyflowSkipped struct {
	NilTier  int // 四档全 nil（身份完整、数值未发布——照常入库）
	BadValue int // 某档买卖非 nil 但脏，或行比表头短
	BadCode  int // ts_code 为空
	BadDate  int // trade_date 不可解析
}

// Skipped 是总跳过行数。
func (n moneyflowSkipped) Skipped() int { return n.BadCode + n.BadDate + n.BadValue }

// FetchMoneyflowTiers 取**单只票**在 [startDate,endDate] 的分档净额（单位元）。
//
// 日期为 "YYYYMMDD"，两者都必须给出：全历史区间在 moneyflow 上没有可控行数
// 上界，「不给就拉全历史」是隐藏默认值。非交易日上游返回空 items —— 合法。
// 单行跳过三种形状（身份不全 / 日期坏 / 脏值），全跳过 → fail-loud。
func (c *Client) FetchMoneyflowTiers(ctx context.Context, symbol, startDate, endDate string) ([]ingest.MoneyflowTiers, error) {
	if strings.TrimSpace(symbol) == "" {
		return nil, invalidInput("moneyflow ts_code 必填（本接口按票取数，不给票就是无界请求）")
	}
	for _, p := range []struct {
		name  string
		value string
	}{
		{"start_date", startDate},
		{"end_date", endDate},
	} {
		if _, err := time.Parse("20060102", p.value); err != nil {
			return nil, invalidInput("moneyflow %s 必须是 YYYYMMDD（got %q）", p.name, p.value)
		}
	}

	resp, err := c.call(ctx, "moneyflow", map[string]interface{}{
		"ts_code":    symbol,
		"start_date": startDate,
		"end_date":   endDate,
	}, moneyflowFields)
	if err != nil {
		return nil, err
	}

	rows, skipped, err := normalizeMoneyflow(resp)
	if err != nil {
		return nil, err
	}
	if err := moneyflowAllSkippedError(symbol, len(resp.Data.Items), skipped); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, nil
}

// ProbeMoneyflow 是只取不落的连通性探针（不 normalize、不落库）。
func (c *Client) ProbeMoneyflow(ctx context.Context, symbol, startDate, endDate string) error {
	_, err := c.call(ctx, "moneyflow", map[string]interface{}{
		"ts_code":    symbol,
		"start_date": startDate,
		"end_date":   endDate,
	}, moneyflowFields)
	return err
}

// normalizeMoneyflow 把 moneyflow 响应转成分档净额行（纯函数）。
//
// 三条判定：
//   - ts_code / trade_date 两列**整列缺失** → 报错（身份列不接受缺省）；
//   - 档位列整列缺失 → 该档 net = nil（Tushare 剔除全空列是既定行为）；
//   - 单行某档买卖缺一侧 → 该档 net = nil（不拿 0 顶包：0 会被读成
//     「该档买卖恰好相等」，而事实是「源没给」）；
//   - 行比表头短 → 整行脏（形状变故），跳过。
func normalizeMoneyflow(resp *response) ([]ingest.MoneyflowTiers, moneyflowSkipped, error) {
	iCode := columnIndex(resp, "ts_code")
	iDate := columnIndex(resp, "trade_date")
	var missing []string
	if iCode < 0 {
		missing = append(missing, "ts_code")
	}
	if iDate < 0 {
		missing = append(missing, "trade_date")
	}
	if len(missing) > 0 {
		return nil, moneyflowSkipped{}, dataQuality(
			"moneyflow 响应缺身份列 %v（实际 fields=%v）—— 形状不符契约，身份列不接受缺省", missing, resp.Data.Fields)
	}

	type tierIndex struct {
		net       string
		buy, sell int
	}
	tiers := make([]tierIndex, 0, len(moneyflowTiers))
	for _, t := range moneyflowTiers {
		tiers = append(tiers, tierIndex{
			net:  t.netColumn,
			buy:  columnIndex(resp, t.buyField),
			sell: columnIndex(resp, t.sellField),
		})
	}

	var (
		out     []ingest.MoneyflowTiers
		skipped moneyflowSkipped
	)
	for _, item := range resp.Data.Items {
		symbol := strings.TrimSpace(tushareCell(item, iCode))
		if symbol == "" {
			skipped.BadCode++
			continue
		}
		d, ok := parseTradeDateCell(tushareCell(item, iDate))
		if !ok {
			skipped.BadDate++
			continue
		}

		row := ingest.MoneyflowTiers{Symbol: symbol, TradeDate: d}
		dirty := false
		for _, t := range tiers {
			if t.buy < 0 || t.sell < 0 {
				continue // 整列被剔除 → 该档 nil（缺失可见）
			}
			if t.buy >= len(item) || t.sell >= len(item) {
				dirty = true
				break
			}
			buyV, buyBad := moneyflowAmount(item, t.buy)
			sellV, sellBad := moneyflowAmount(item, t.sell)
			if buyBad || sellBad {
				dirty = true
				break
			}
			if buyV == nil || sellV == nil {
				continue // 缺一侧 ⇒ 净额不可得 ⇒ nil（不折 0）
			}
			net := (*buyV - *sellV) * cnyPerWanYuanUnit
			switch t.net {
			case "super_net":
				row.SuperNet = &net
			case "large_net":
				row.LargeNet = &net
			case "medium_net":
				row.MediumNet = &net
			case "small_net":
				row.SmallNet = &net
			}
		}
		if dirty {
			skipped.BadValue++
			continue
		}
		if row.SuperNet == nil && row.LargeNet == nil && row.MediumNet == nil && row.SmallNet == nil {
			skipped.NilTier++
		}
		out = append(out, row)
	}
	return out, skipped, nil
}

// moneyflowAmount 取一个可空数值列：(nil,false) 缺失；(值,false) 可解析；
// (nil,true) 非 nil 但脏。
func moneyflowAmount(item []any, idx int) (*float64, bool) {
	if idx < 0 || idx >= len(item) || item[idx] == nil {
		return nil, false
	}
	v, ok := tushareFloatOK(item, idx)
	if !ok {
		return nil, true
	}
	return &v, false
}

// moneyflowAllSkippedError 是 fail-loud 闸门：有行却一行都没归一出来时拒绝
// 静默成功（上游改格式 / 列整体变空的典型形态）。真空响应不在此列。
func moneyflowAllSkippedError(symbol string, items int, skipped moneyflowSkipped) error {
	if items == 0 || skipped.Skipped() < items {
		return nil
	}
	return dataQuality("资金流 %s：moneyflow 响应返回 %d 行但全部被跳过（坏代码 %d / 坏日期 %d / 脏值 %d）——上游形状/格式变故，拒绝以「导入 0 行」伪装成功",
		symbol, items, skipped.BadCode, skipped.BadDate, skipped.BadValue)
}
