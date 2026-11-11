package tushare

// 指数成分/权重腿（`index_weight`）。参考数据同步的第三条腿（DATA-INDEX-CONSTITUENTS）。
//
// 口径：`index_weight` 是**月频快照**——每期在月末交易日发布一整张「指数 →
// 成分 + 权重」表，不给成分的进出日期。本仓因此只承接「当前快照」一个口径：
//   - 窗口内返回多期时，只保留 `trade_date` 最大的一期，不把多期拼成区间
//     （`in_date/out_date` 的推导需要多期差分，不在本单）；
//   - `in_date` = 该期快照日（「我们能在这一天确认它在成分内」），
//     `out_date` 恒 NULL（当前成分）；
//   - 读面按 as_of 做半开区间过滤，早于快照日的 as_of 解析为空——
//     不知为不知，不拿今天的快照冒充历史（`pit_eligible=false`）。

import (
	"context"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// FetchIndexConstituents 取指数在 [start, end] 内**最新一期**的成分与权重。
//
// 零值日期 = 不传该参数（由调用方决定窗口；本层不替它编默认窗口）。
// 空响应是**数据质量失败**而不是合法的 0 行：指数代码写错、无该接口权限时
// Tushare 都返回空集，静默写 0 行会让作业显示成功而票池永远为空。
func (c *Client) FetchIndexConstituents(ctx context.Context, indexCode string, start, end time.Time) ([]market.IndexConstituent, error) {
	params := map[string]interface{}{"index_code": indexCode}
	if !start.IsZero() {
		params["start_date"] = start.Format("20060102")
	}
	if !end.IsZero() {
		params["end_date"] = end.Format("20060102")
	}

	resp, err := c.call(ctx, "index_weight", params, "index_code,con_code,trade_date,weight")
	if err != nil {
		return nil, err
	}
	// 契约：`con_code` / `trade_date` 是语义必需列；`index_code` / `weight`
	// 缺列时分别回落到入参与 0（权重为空是源端允许的）。
	if err := requireFields(resp, "con_code", "trade_date"); err != nil {
		return nil, err
	}
	if len(resp.Data.Items) == 0 {
		return nil, dataQuality("index_weight 对 %q 在 %s..%s 返回 0 行（代码错误或无该接口权限）",
			indexCode, formatDay(start), formatDay(end))
	}

	out, skipped := normalizeIndexConstituents(resp, indexCode)
	if len(out) == 0 {
		return nil, dataQuality("index_weight 返回 %d 行但一行都没能归一（跳过 %d 行）",
			len(resp.Data.Items), skipped)
	}
	return out, nil
}

// normalizeIndexConstituents 只保留最新一期快照；返回 (行, 跳过行数)。
//
// 两遍扫描：第一遍找窗口内最大的 `trade_date`，第二遍只收该期的行。列为空/
// 日期不可解析的行计入 skipped（不静默当成「该期没有这只票」）。
func normalizeIndexConstituents(resp *response, fallbackIndex string) ([]market.IndexConstituent, int) {
	codeIdx := columnIndex(resp, "index_code")
	conIdx := columnIndex(resp, "con_code")
	dateIdx := columnIndex(resp, "trade_date")
	weightIdx := columnIndex(resp, "weight")
	if conIdx < 0 || dateIdx < 0 {
		return nil, len(resp.Data.Items)
	}

	var latest time.Time
	skipped := 0
	for _, item := range resp.Data.Items {
		d, err := time.Parse("20060102", tushareCell(item, dateIdx))
		if err != nil {
			skipped++
			continue
		}
		if d.After(latest) {
			latest = d
		}
	}
	if latest.IsZero() {
		return nil, skipped
	}

	out := make([]market.IndexConstituent, 0, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		d, err := time.Parse("20060102", tushareCell(item, dateIdx))
		if err != nil || !d.Equal(latest) {
			continue
		}
		symbol := tushareCell(item, conIdx)
		if symbol == "" {
			skipped++
			continue
		}
		code := tushareCell(item, codeIdx)
		if code == "" {
			code = fallbackIndex
		}
		out = append(out, market.IndexConstituent{
			IndexCode: code,
			Symbol:    symbol,
			InDate:    latest,
			Weight:    fieldFloat(item, weightIdx),
		})
	}
	return out, skipped
}

// formatDay 只服务于错误文案：零值日期写「不限」而不是 0001-01-01。
func formatDay(d time.Time) string {
	if d.IsZero() {
		return "不限"
	}
	return d.Format("2006-01-02")
}
