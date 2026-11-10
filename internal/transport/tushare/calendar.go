package tushare

// 交易日历腿（`trade_cal`）。参考数据同步的第一条腿；取数语义逐条对齐 core
// `pkg/data/tushare.go` 的 `FetchTradingCalendar`（只读参考，不 import core）。

import (
	"context"
	"strings"
	"time"

	"tellurion-data/internal/ingest"
)

// 编译期证明：本客户端同时满足参考数据窄口。
var _ ingest.ReferenceTransport = (*Client)(nil)

// FetchTradeCalendar 取交易所日历。
//
// 归一化口径：
//   - `cal_date`（YYYYMMDD）→ `TradingDay.Date`（UTC 零点，与 `DATE` 列一致）；
//   - `is_open`（1/0）→ `IsTradingDay`；源端偶尔给 `"1"`/`"true"`，两种都认；
//   - `exchange` 缺列时回落到入参（再缺省 = SSE）：表的主键只有 `trade_date`，
//     不写交易所会让非 SSE 的行被读成 SSE。
//
// 有行但一行都没归一出来 ⇒ 数据质量哨兵（fail-loud），真空响应才是合法 0 行。
func (c *Client) FetchTradeCalendar(ctx context.Context, exchange string, start, end time.Time) ([]ingest.TradingDay, error) {
	params := map[string]interface{}{}
	if exchange != "" {
		params["exchange"] = exchange
	}
	if !start.IsZero() {
		params["start_date"] = start.Format("20060102")
	}
	if !end.IsZero() {
		params["end_date"] = end.Format("20060102")
	}

	resp, err := c.call(ctx, "trade_cal", params, "exchange,cal_date,is_open,pretrade_date")
	if err != nil {
		return nil, err
	}

	days, skipped := normalizeTradeCalendar(resp, exchange)
	if len(resp.Data.Items) > 0 && len(days) == 0 {
		return nil, dataQuality("trade_cal 返回 %d 行但一行都没能归一（跳过 %d 行）",
			len(resp.Data.Items), skipped)
	}
	return days, nil
}

// normalizeTradeCalendar 把 trade_cal 响应转成行；返回 (行, 跳过行数)。
//
// 列按名字定位而不是固定下标：tushare 会剔除该期全空的列，固定下标会把
// `is_open` 读成 `cal_date`（`client.go` 的 `columnIndex` 就是为这个存在的）。
func normalizeTradeCalendar(resp *response, fallbackExchange string) ([]ingest.TradingDay, int) {
	calIdx := columnIndex(resp, "cal_date")
	openIdx := columnIndex(resp, "is_open")
	exIdx := columnIndex(resp, "exchange")
	if calIdx < 0 {
		return nil, len(resp.Data.Items)
	}

	out := make([]ingest.TradingDay, 0, len(resp.Data.Items))
	skipped := 0
	for _, item := range resp.Data.Items {
		t, err := time.Parse("20060102", tushareCell(item, calIdx))
		if err != nil {
			skipped++
			continue
		}
		ex := tushareCell(item, exIdx)
		if ex == "" {
			ex = fallbackExchange
		}
		if ex == "" {
			ex = "SSE"
		}
		out = append(out, ingest.TradingDay{
			Date:         t,
			Exchange:     ex,
			IsTradingDay: tushareBool(item, openIdx),
		})
	}
	return out, skipped
}

// tushareBool 解析 1/0（以及 "1"/"true" 这类字符串形态）。
//
// 缺位按 false：`is_open` 缺席的行不能被读成交易日——那会把周末写成可交易，
// 比「少一天」更难发现。
func tushareBool(item []any, idx int) bool {
	if idx < 0 || idx >= len(item) || item[idx] == nil {
		return false
	}
	switch v := item[idx].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "y", "yes":
			return true
		}
		return false
	default:
		return false
	}
}
