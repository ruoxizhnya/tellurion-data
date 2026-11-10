package tushare

// 市场活跃度取数腿 —— 对齐 core `pkg/data/tushare_daily_basic.go`
// （`daily_basic` → `market_activity`）。本包只取数 + 归一，不落库。
//
// 为什么单交易日一次请求（不传区间）：官方限量「单次最高 6000 条」，全市场
// 单日约 5500–5600 行，单日请求在限额内且形状可预期；跨日区间请求的行数
// 上限未文档化（可能静默截断，同 fx_daily 前科）。按日迭代由调用方负责。

import (
	"context"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

// ActivitySourceTushare 是 `market_activity.source` 的 tushare 取值。
const ActivitySourceTushare = "tushare"

// dailyBasicFields 只取身份列 + 活跃度三列（压响应体积）。
const dailyBasicFields = "ts_code,trade_date,turnover_rate,turnover_rate_f,volume_ratio"

// dailyBasicSkipped 是 normalize 的跳过计数（NilValue 照常入库，不计入 Skipped）。
type dailyBasicSkipped struct {
	BadDate  int // trade_date 不可解析（PIT 身份不完整）
	NilValue int // 三个数值列全为 nil（当日未发布——照常入库）
	BadValue int // 数值列非 nil 但不可解析/非有限（脏值）
	BadCode  int // ts_code 为空（身份不完整）
}

// Skipped 是总跳过行数。
func (n dailyBasicSkipped) Skipped() int { return n.BadDate + n.BadValue + n.BadCode }

// FetchDailyBasic 取某交易日的全市场活跃度截面。
//
// tradeDate 必须是 "YYYYMMDD"：格式错在**本地**拦下（InvalidInput），不让它
// 变成一次无谓的上游请求 + 上游的口径不明报错。
// 节假日/非交易日上游返回空 items ⇒ (nil, nil) 合法；有行但全被跳过 ⇒
// 数据质量哨兵（上游形状变故，拒绝以「导入 0 行」伪装成功）。
func (c *Client) FetchDailyBasic(ctx context.Context, tradeDate string) ([]domain.MarketActivity, error) {
	if _, err := time.Parse("20060102", tradeDate); err != nil {
		return nil, invalidInput("daily_basic trade_date 必须是 YYYYMMDD（got %q）", tradeDate)
	}
	resp, err := c.call(ctx, "daily_basic", map[string]interface{}{
		"trade_date": tradeDate,
	}, dailyBasicFields)
	if err != nil {
		return nil, err
	}

	rows, skipped, err := normalizeDailyBasic(resp, tradeDate, c.now().UTC())
	if err != nil {
		return nil, err
	}
	if err := dailyBasicAllSkippedError(tradeDate, len(resp.Data.Items), skipped); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, nil
}

// ProbeDailyBasic 是只取不落的连通性探针（不 normalize、不落库）。
func (c *Client) ProbeDailyBasic(ctx context.Context, tradeDate string) error {
	_, err := c.call(ctx, "daily_basic", map[string]interface{}{
		"trade_date": tradeDate,
	}, dailyBasicFields)
	return err
}

// normalizeDailyBasic 把 daily_basic 响应转成 market_activity 行。
//
// 口径：
//   - 三个数值列：nil → 指针 nil（缺失≠0，**照常入库**）；非 nil 但不可
//     解析/非有限 → 跳过该行（脏值不进库）；三列全 nil 不是跳过（身份完整
//     的「当日未发布」观测照常入库）；
//   - 缺任何一列（整个响应没有该列）→ 报错，形状不符契约。
func normalizeDailyBasic(resp *response, tradeDate string, asOf time.Time) ([]domain.MarketActivity, dailyBasicSkipped, error) {
	iCode := columnIndex(resp, "ts_code")
	iDate := columnIndex(resp, "trade_date")
	iTurnover := columnIndex(resp, "turnover_rate")
	iTurnoverF := columnIndex(resp, "turnover_rate_f")
	iVolRatio := columnIndex(resp, "volume_ratio")

	var missing []string
	for _, col := range []struct {
		name string
		idx  int
	}{
		{"ts_code", iCode}, {"trade_date", iDate}, {"turnover_rate", iTurnover},
		{"turnover_rate_f", iTurnoverF}, {"volume_ratio", iVolRatio},
	} {
		if col.idx < 0 {
			missing = append(missing, col.name)
		}
	}
	if len(missing) > 0 {
		return nil, dailyBasicSkipped{}, dataQuality(
			"daily_basic 响应缺列（%s）：缺 %v，实际 fields=%v", tradeDate, missing, resp.Data.Fields)
	}

	var (
		out     []domain.MarketActivity
		skipped dailyBasicSkipped
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
		turnover, badT := activityValue(item, iTurnover)
		turnoverF, badTF := activityValue(item, iTurnoverF)
		volRatio, badVR := activityValue(item, iVolRatio)
		if badT || badTF || badVR {
			skipped.BadValue++
			continue
		}
		if turnover == nil && turnoverF == nil && volRatio == nil {
			skipped.NilValue++
		}
		out = append(out, domain.MarketActivity{
			Symbol:        symbol,
			TradeDate:     d,
			TurnoverRate:  turnover,
			TurnoverRateF: turnoverF,
			VolumeRatio:   volRatio,
			Source:        ActivitySourceTushare,
			AsOf:          asOf,
		})
	}
	return out, skipped, nil
}

// activityValue 取一个可空数值列：(nil,false) 缺失；(值,false) 可解析；
// (nil,true) 非 nil 但脏（调用方跳过该行）。
func activityValue(item []any, idx int) (*float64, bool) {
	if idx < 0 || idx >= len(item) || item[idx] == nil {
		return nil, false
	}
	v, ok := tushareFloatOK(item, idx)
	if !ok {
		return nil, true
	}
	return &v, false
}

// dailyBasicAllSkippedError 是 fail-loud 闸门：有行却一行都没能入库时拒绝
// 静默成功。真空响应（items==0，节假日）不在此列。
func dailyBasicAllSkippedError(tradeDate string, items int, skipped dailyBasicSkipped) error {
	if items == 0 || skipped.Skipped() < items {
		return nil
	}
	return dataQuality("活跃度观测 %s：daily_basic 响应返回 %d 行但全部被跳过（坏代码 %d / 坏日期 %d / 脏值 %d）——上游形状/格式变故，拒绝以「导入 0 行」伪装成功",
		tradeDate, items, skipped.BadCode, skipped.BadDate, skipped.BadValue)
}

// parseTradeDateCell 解析 "YYYYMMDD" 交易日；空串/解析失败 → (零值, false)。
func parseTradeDateCell(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
