package tushare

// 行情 / 标的 / 财务三条取数腿 —— 逐条对齐 core `pkg/data/tushare.go` 的
// `FetchStocks` / `FetchDailyOHLCV` / `FetchFundamentals`。
//
// 共同点：core 这三条都是「取数 + 归一 + 落库」三合一，本包**只做前两步**
// （传输 port 的契约就是返回值，不落库；落库走 `ingest.ETLPipeline`）。

import (
	"context"
	"math"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// sharesPerLot 是 A 股「一手」的股数：tushare daily 的 `vol` 单位是手，
// 规范层 volume 是股。
const sharesPerLot = 100

// cnyPerAmountUnit 把 tushare daily 的 `amount`（千元）折成规范层 turnover
// 的单位 CNY。**不取整**：源侧 amount 带元以下真实精度，round 会抹掉它。
const cnyPerAmountUnit = 1000

// FetchStocks 取交易所上市清单（health probe 用的最便宜调用）。
//
// 归一化口径（同 core）：
//   - Symbol 用完整 ts_code（`000001.SZ`），与读面 API 的 symbol 口径一致；
//   - Exchange 从 ts_code 后缀推（.SH→SSE / .SZ→SZSE）；
//   - Status 由摘牌日推导：摘牌日已过 = delisted，否则 active。写死 active
//     会让退市票在下游被当成可交易标的（幸存者偏差治不了）。
func (c *Client) FetchStocks(ctx context.Context, exchange, listStatus string) ([]market.Stock, error) {
	resp, err := c.call(ctx, "stock_basic", map[string]interface{}{
		"exchange":    exchange,
		"list_status": listStatus,
	}, "ts_code,symbol,name,area,industry,market,list_date,delist_date,is_hs")
	if err != nil {
		return nil, err
	}
	// 契约：官方响应必须带这 5 列（2026-11-11 线上核对：L/D 两种查询均返回
	// 完整 9 列，空值以空串出现）。缺列 = 改名或形状变故，fail-closed。
	if err := requireFields(resp, "ts_code", "symbol", "name", "list_date", "delist_date"); err != nil {
		return nil, err
	}
	stocks, skipped := c.normalizeStocks(resp)
	if len(resp.Data.Items) > 0 && len(stocks) == 0 {
		return nil, dataQuality("stock_basic 返回 %d 行但一行都没能归一（跳过 %d 行）",
			len(resp.Data.Items), skipped)
	}
	return stocks, nil
}

// normalizeStocks 把 stock_basic 响应转成 market.Stock；返回 (行, 跳过行数)。
//
// 列按名字定位而不是固定下标：线上 `list_status=D` 的响应里 `area` /
// `industry` / `market` 可以是空串，但 fields 头仍是 9 列——按名取才不会
// 因列序变化把 list_date 读成 delist_date。`area` / `market` / `is_hs`
// 是**故意忽略**的多余列（本仓不落库）。
func (c *Client) normalizeStocks(resp *response) ([]market.Stock, int) {
	tsIdx := columnIndex(resp, "ts_code")
	symIdx := columnIndex(resp, "symbol")
	nameIdx := columnIndex(resp, "name")
	indIdx := columnIndex(resp, "industry")
	listIdx := columnIndex(resp, "list_date")
	delistIdx := columnIndex(resp, "delist_date")

	out := make([]market.Stock, 0, len(resp.Data.Items))
	skipped := 0
	for _, item := range resp.Data.Items {
		tsCode := tushareCell(item, tsIdx)
		symbol := tushareCell(item, symIdx)
		if tsCode == "" && symbol == "" {
			skipped++
			continue
		}
		useSymbol := tsCode
		if useSymbol == "" {
			useSymbol = symbol
		}

		stock := market.Stock{
			Symbol:   useSymbol,
			Name:     tushareCell(item, nameIdx),
			Exchange: extractExchange(useSymbol),
			Industry: tushareCell(item, indIdx),
			Status:   "active",
		}
		if listDate := tushareCell(item, listIdx); listDate != "" {
			t, err := time.Parse("20060102", listDate)
			if err != nil {
				// 日期非空但不可解析 = 脏行；静默写 NULL 会把「上市日未知」
				// 混进历史票池，跳过并由调用方按「全跳过」判数据质量。
				skipped++
				continue
			}
			stock.ListDate = t
		}
		if delistDate := tushareCell(item, delistIdx); delistDate != "" {
			t, err := time.Parse("20060102", delistDate)
			if err != nil {
				skipped++
				continue
			}
			d := t
			stock.DelistDate = &d
			if !d.After(c.now()) {
				stock.Status = "delisted"
			}
		}
		out = append(out, stock)
	}
	return out, skipped
}

// FetchDailyOHLCV 取某票区间的**前复权**日线。
//
// 复权口径（Tushare 官方 adj_factor 文档示例 4，**别凭记忆改**）：
//
//	前复权价 = 当日价格 × 当日复权因子 / 区间最新复权因子
//
// 三个必须知道的性质：
//   - adj_factor 是累计后复权因子；停牌日因子会补齐而 daily 没有行情，
//     两个序列按日期不对齐 ⇒ 逐日取因子，取不到的那天**跳过**而不是补 1
//     （补 1 等于宣称「这天没除权」，会在除权日附近造出假跳空）；
//   - qfq 基准是**区间内**最新交易日，将来同步到更晚日期时历史 qfq 会整体
//     变动——这是复权口径的固有性质，不是本实现引入的；
//   - vol 手→股要 round（浮点表示误差会让下游整数校验判整批损坏），
//     amount 千元→CNY 不 round（源侧有元以下真实精度）。
func (c *Client) FetchDailyOHLCV(ctx context.Context, symbol, startDate, endDate string) ([]market.OHLCV, error) {
	params := map[string]interface{}{
		"ts_code":    symbol,
		"start_date": formatDate(startDate),
		"end_date":   formatDate(endDate),
	}
	resp, err := c.call(ctx, "daily", params, "ts_code,trade_date,open,high,low,close,vol,amount")
	if err != nil {
		return nil, err
	}

	factors, err := c.fetchAdjFactors(ctx, symbol, startDate, endDate)
	if err != nil {
		return nil, err
	}

	records, skipped := c.normalizeDailyOHLCV(resp, symbol, factors)
	// 有行却一行都没归一出来 ⇒ 上游形状变故（典型：adj_factor 整段缺失）。
	// core 这里只 log warn 然后返回 0 行，等于把「拿不到复权因子」伪装成
	// 「区间内没有交易」；本仓按同款 fail-loud 口径拒绝。
	if len(resp.Data.Items) > 0 && len(records) == 0 {
		return nil, dataQuality("%s: daily 返回 %d 行但一行都没能归一（复权因子 %d 个，跳过 %d 行）",
			symbol, len(resp.Data.Items), len(factors), skipped)
	}
	return records, nil
}

// fetchAdjFactors 取 trade_date(YYYYMMDD) → adj_factor 的映射。
// 因子 <= 0 视为脏数据、不进 map（它既不是「没除权」，也不是「没有数据」）。
func (c *Client) fetchAdjFactors(ctx context.Context, symbol, startDate, endDate string) (map[string]float64, error) {
	resp, err := c.call(ctx, "adj_factor", map[string]interface{}{
		"ts_code":    symbol,
		"start_date": formatDate(startDate),
		"end_date":   formatDate(endDate),
	}, "ts_code,trade_date,adj_factor")
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		if len(item) < 3 {
			continue
		}
		d := tushareCell(item, 1)
		if d == "" {
			continue
		}
		f := fieldFloat(item, 2)
		if f <= 0 {
			continue
		}
		out[d] = f
	}
	return out, nil
}

// normalizeDailyOHLCV 把 daily 响应按 qfq 口径转成行；返回 (行, 跳过行数)。
func (c *Client) normalizeDailyOHLCV(resp *response, symbol string, factors map[string]float64) ([]market.OHLCV, int) {
	if len(factors) == 0 {
		return nil, 0
	}
	// YYYYMMDD 的字典序 == 时间序。
	latest := ""
	for d := range factors {
		if d > latest {
			latest = d
		}
	}
	base := factors[latest]
	if base <= 0 {
		return nil, 0
	}

	records := make([]market.OHLCV, 0, len(resp.Data.Items))
	skipped := 0
	for _, item := range resp.Data.Items {
		if len(item) < 8 {
			continue
		}
		tradeDate := tushareCell(item, 1)
		if tradeDate == "" {
			continue
		}
		t, err := time.Parse("20060102", tradeDate)
		if err != nil {
			continue
		}
		f, ok := factors[tradeDate]
		if !ok || f <= 0 {
			skipped++
			continue
		}
		scale := f / base
		records = append(records, market.OHLCV{
			Symbol:   symbol,
			Date:     t,
			Open:     fieldFloat(item, 2) * scale,
			High:     fieldFloat(item, 3) * scale,
			Low:      fieldFloat(item, 4) * scale,
			Close:    fieldFloat(item, 5) * scale,
			Volume:   math.Round(fieldFloat(item, 6) * sharesPerLot),
			Turnover: fieldFloat(item, 7) * cnyPerAmountUnit,
		})
	}
	return records, skipped
}

// FetchFundamentals 取某票某公告日的财务指标。
//
// 字段口径同 core：`fina_indicator` 的 15 列，其中 12 个数值列为可空
// （`*float64`）——源端缺字段留 nil（未知），不能折 0（0 会被下游读成
// 「PE = 0，白送的股票」）。`ann_date` 必须保留：只存 end_date 的话
// 读取侧 PIT 会退化成报告期末，三季报在 9/30 就能被回测看见。
func (c *Client) FetchFundamentals(ctx context.Context, symbol, date string) ([]market.Fundamental, error) {
	resp, err := c.call(ctx, "fina_indicator", map[string]interface{}{
		"ts_code":  symbol,
		"ann_date": date,
	}, "ts_code,ann_date,end_date,pe,pb,ps,roe,roa,debt_to_equity,gross_margin,net_margin,revenue,net_profit,total_assets,total_liab")
	if err != nil {
		return nil, err
	}
	return normalizeFundamentals(resp), nil
}

// normalizeFundamentals 把 fina_indicator 响应转成 market.Fundamental。
func normalizeFundamentals(resp *response) []market.Fundamental {
	out := make([]market.Fundamental, 0, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		if len(item) < 3 {
			continue
		}
		symbol := tushareCell(item, 0)
		if symbol == "" {
			continue
		}
		endDate, _ := time.Parse("20060102", tushareCell(item, 2))

		var annDate *time.Time
		if s := tushareCell(item, 1); s != "" {
			if a, err := time.Parse("20060102", s); err == nil {
				annDate = &a
			}
		}

		out = append(out, market.Fundamental{
			Symbol:       symbol,
			Date:         endDate,
			AnnDate:      annDate,
			PE:           fieldFloatPtr(item, 3),
			PB:           fieldFloatPtr(item, 4),
			PS:           fieldFloatPtr(item, 5),
			ROE:          fieldFloatPtr(item, 6),
			ROA:          fieldFloatPtr(item, 7),
			DebtToEquity: fieldFloatPtr(item, 8),
			GrossMargin:  fieldFloatPtr(item, 9),
			NetMargin:    fieldFloatPtr(item, 10),
			Revenue:      fieldFloatPtr(item, 11),
			NetProfit:    fieldFloatPtr(item, 12),
			TotalAssets:  fieldFloatPtr(item, 13),
			TotalLiab:    fieldFloatPtr(item, 14),
		})
	}
	return out
}
