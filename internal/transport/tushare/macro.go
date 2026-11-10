package tushare

// 宏观数据取数腿 —— 对齐 core `pkg/data/macro.go`（7 个 tushare 接口 →
// 9 个指标）。本包只取数 + 归一，不落库（落库走 `ingest.ETLPipeline`）。
//
// 指标目录：
//
//	cn_cpi      → CPI_YOY        cn_ppi → PPI_YOY      cn_pmi → PMI
//	cn_m        → M2 + M2_YOY    sf_month → TSF_INC
//	shibor_lpr  → LPR_1Y + LPR_5Y   fx_daily → USDCNY_CLOSE
//
// 两条流控教训（都是实测，不是推测）：
//   - shibor_lpr 限「1 次/小时」，所以 LPR_1Y / LPR_5Y **必须共用一次请求**；
//     分两次拉的话第二个指标永远拉不到；
//   - fx_daily 单次限 1000 行，2019 至今 2400+ 行，必须按年分段，否则静默丢数据。

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

// MacroSourceTushare 是 `macro_series.source` 的 tushare 取值。
const MacroSourceTushare = "tushare"

// 频率取值（macro_series.freq）。
const (
	MacroFreqMonthly = "M"
	MacroFreqDaily   = "D"
)

// macroIndicatorSpec 描述一个指标如何从 Tushare 取数。
//
// DateLayout 决定 obs_date 解析口径：月频 "200601"（取当月首日）、日频
// "20060102"（自然日）。SinceYear 非零表示日频指标按年分段请求。
type macroIndicatorSpec struct {
	Indicator  string
	APIName    string
	Field      string                 // 响应里的数值列（大小写不敏感匹配）
	Freq       string                 // M / D
	Unit       string                 // % / 亿元 / 点 / 汇率
	DateField  string                 // 响应里的日期列
	DateLayout string                 // 200601 / 20060102
	Fields     string                 // fields 参数（只取需要的列）
	Params     map[string]interface{} // 接口级固定参数
	SinceYear  int                    // 日频分段起始年（0 = 一次全量）
}

// macroIndicatorSpecs 是首版指标目录（9 个 indicator / 7 个 tushare 接口）。
var macroIndicatorSpecs = map[string]macroIndicatorSpec{
	"CPI_YOY": {
		Indicator: "CPI_YOY", APIName: "cn_cpi", Field: "nt_yoy",
		Freq: MacroFreqMonthly, Unit: "%", DateField: "month", DateLayout: "200601",
		Fields: "month,nt_val,nt_yoy",
	},
	"PPI_YOY": {
		Indicator: "PPI_YOY", APIName: "cn_ppi", Field: "ppi_yoy",
		Freq: MacroFreqMonthly, Unit: "%", DateField: "month", DateLayout: "200601",
		Fields: "month,ppi_yoy",
	},
	"PMI": {
		// cn_pmi 不传 fields 时响应字段名为大写（MONTH/PMI010000，实测），
		// 传 fields 则按小写返回；normalize 侧大小写不敏感，两条路径都成立。
		Indicator: "PMI", APIName: "cn_pmi", Field: "pmi010000",
		Freq: MacroFreqMonthly, Unit: "点", DateField: "month", DateLayout: "200601",
		Fields: "month,pmi010000",
	},
	"M2": {
		Indicator: "M2", APIName: "cn_m", Field: "m2",
		Freq: MacroFreqMonthly, Unit: "亿元", DateField: "month", DateLayout: "200601",
		Fields: "month,m2,m2_yoy",
	},
	"M2_YOY": {
		Indicator: "M2_YOY", APIName: "cn_m", Field: "m2_yoy",
		Freq: MacroFreqMonthly, Unit: "%", DateField: "month", DateLayout: "200601",
		Fields: "month,m2,m2_yoy",
	},
	"TSF_INC": {
		Indicator: "TSF_INC", APIName: "sf_month", Field: "inc_month",
		Freq: MacroFreqMonthly, Unit: "亿元", DateField: "month", DateLayout: "200601",
		Fields: "month,inc_month",
	},
	"LPR_1Y": {
		// SinceYear=0（一次全量），**不按年分段**：shibor_lpr 实测 1 次/小时，
		// 按年分段要 14 次必然撞限；单次限量 4000 行（≈18 年）足够。
		Indicator: "LPR_1Y", APIName: "shibor_lpr", Field: "1y",
		Freq: MacroFreqDaily, Unit: "%", DateField: "date", DateLayout: "20060102",
		Fields: "date,1y,5y",
	},
	"LPR_5Y": {
		Indicator: "LPR_5Y", APIName: "shibor_lpr", Field: "5y",
		Freq: MacroFreqDaily, Unit: "%", DateField: "date", DateLayout: "20060102",
		Fields: "date,1y,5y",
	},
	"USDCNY_CLOSE": {
		// fx_daily 的 trade_date 为 GMT 口径（官方文档明示「比北京时间晚一天」），
		// 原样入库不做时区转换——转换是解读，不是事实。
		Indicator: "USDCNY_CLOSE", APIName: "fx_daily", Field: "bid_close",
		Freq: MacroFreqDaily, Unit: "汇率", DateField: "trade_date", DateLayout: "20060102",
		Fields:    "trade_date,bid_close",
		Params:    map[string]interface{}{"ts_code": "USDCNH.FXCM"},
		SinceYear: 2019, // USDCNH.FXCM 实测数据始于 20190102
	},
}

// MacroIndicators 是 `ingest.TushareTransport` 的目录方法：返回排序后的
// 指标代码（稳定视图，供 adapter 全量导入与测试使用）。
func (c *Client) MacroIndicators() []string {
	out := make([]string, 0, len(macroIndicatorSpecs))
	for k := range macroIndicatorSpecs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FetchMacroIndicators 拉取多个指标（空参数 = 全目录）。
//
// **同一 tushare 接口的多个 indicator 共用一次请求**（M2/M2_YOY、LPR_1Y/
// LPR_5Y）。这不是优化，是流控所迫——见文件头。分组键是 APIName，组不变量
// （fields / 日期列 / 日期格式 / 固定参数 / 分段窗口）必须逐项一致；不一致
// 是静态目录 bug，报错点名，不静默退化逐条拉。接口间顺序执行（tushare
// 流控按 token 计，并发只会更快撞限）。
func (c *Client) FetchMacroIndicators(ctx context.Context, indicators ...string) ([]domain.MacroSeries, error) {
	if len(indicators) == 0 {
		indicators = c.MacroIndicators()
	}

	var order []string
	groups := map[string][]macroIndicatorSpec{}
	for _, ind := range indicators {
		spec, ok := macroIndicatorSpecs[ind]
		if !ok {
			return nil, invalidInput("未知宏观指标 %q（目录：%v）", ind, c.MacroIndicators())
		}
		if _, seen := groups[spec.APIName]; !seen {
			order = append(order, spec.APIName)
		}
		groups[spec.APIName] = append(groups[spec.APIName], spec)
	}

	now := c.now().UTC() // as_of 与全部分组窗口共用同一时间点
	asOf := now
	var all []domain.MacroSeries
	for _, api := range order {
		specs := groups[api]
		if len(specs) > 1 {
			if share, reason := specsCanShareResponse(specs, now); !share {
				return nil, fmt.Errorf("宏观指标目录配置不自洽：同接口 %s 的 %v 无法共用一次响应（%s）——修正 internal/transport/tushare/macro.go 的 macroIndicatorSpecs",
					api, apiIndicatorNames(specs), reason)
			}
		}

		windows, err := macroWindowsOrErr(specs[0], now)
		if err != nil {
			return nil, err
		}
		var batch []domain.MacroSeries
		for _, params := range windows {
			resp, err := c.call(ctx, api, params, specs[0].Fields)
			if err != nil {
				return nil, err
			}
			for _, spec := range specs {
				res, err := normalizeMacroSeries(resp, spec, MacroSourceTushare, asOf)
				if err != nil {
					return nil, dataQuality("%s 响应形状不符合契约（缺 %s/%s 列）", api, spec.DateField, spec.Field)
				}
				if err := macroAllSkippedError(api, spec.Indicator, len(resp.Data.Items), res); err != nil {
					return nil, err
				}
				batch = append(batch, res.Rows...)
			}
		}
		all = append(all, batch...)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// ProbeMacro 对指标做一次「只取不落」的连通性探测。
//
// 只发**第一个**窗口：探测的语义是连通性，不该按数据量放大——遍历全部
// 窗口会让 USDCNY_CLOSE（2019–至今 8 段）每次探测发 8 次请求，白吃配额。
func (c *Client) ProbeMacro(ctx context.Context, indicator string) error {
	spec, ok := macroIndicatorSpecs[indicator]
	if !ok {
		return invalidInput("未知宏观指标 %q（目录：%v）", indicator, c.MacroIndicators())
	}
	windows, err := macroWindowsOrErr(spec, c.now().UTC())
	if err != nil {
		return err
	}
	_, err = c.call(ctx, spec.APIName, windows[0], spec.Fields)
	return err
}

// specsCanShareResponse 报告一组同接口 spec 能否共用一次响应。
func specsCanShareResponse(specs []macroIndicatorSpec, now time.Time) (bool, string) {
	base := specs[0]
	for _, spec := range specs[1:] {
		switch {
		case spec.Fields != base.Fields:
			return false, fmt.Sprintf("%s 与 %s 的 fields 不同（%q vs %q）",
				base.Indicator, spec.Indicator, base.Fields, spec.Fields)
		case spec.DateField != base.DateField:
			return false, fmt.Sprintf("%s 与 %s 的日期列不同（%q vs %q）",
				base.Indicator, spec.Indicator, base.DateField, spec.DateField)
		case spec.DateLayout != base.DateLayout:
			return false, fmt.Sprintf("%s 与 %s 的日期格式不同（%q vs %q）",
				base.Indicator, spec.Indicator, base.DateLayout, spec.DateLayout)
		case !equalMacroParams(spec.Params, base.Params):
			return false, fmt.Sprintf("%s 与 %s 的固定参数不同（%v vs %v）",
				base.Indicator, spec.Indicator, base.Params, spec.Params)
		}
	}
	if !sameWindows(specs, now) {
		return false, fmt.Sprintf("%v 的分段窗口序列不一致（检查 SinceYear）", apiIndicatorNames(specs))
	}
	return true, ""
}

// equalMacroParams 报告两组固定参数是否相等（nil 与空 map 等值）。
// 用 DeepEqual：Params 的值是 interface{}，非标量直接用 != 会 panic。
func equalMacroParams(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !reflect.DeepEqual(bv, av) {
			return false
		}
	}
	return true
}

// sameWindows 报告一组 spec 的分段窗口序列是否一致（能否共用一次响应）。
func sameWindows(specs []macroIndicatorSpec, now time.Time) bool {
	base := macroFetchWindows(specs[0], now)
	for _, spec := range specs[1:] {
		other := macroFetchWindows(spec, now)
		if len(base) != len(other) {
			return false
		}
		for i := range base {
			if len(base[i]) != len(other[i]) {
				return false
			}
			for k, v := range base[i] {
				if ov, ok := other[i][k]; !ok || !reflect.DeepEqual(ov, v) {
					return false
				}
			}
		}
	}
	return true
}

// apiIndicatorNames 取一组 spec 的 indicator 名（错误信息点名用）。
func apiIndicatorNames(specs []macroIndicatorSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Indicator)
	}
	return out
}

// macroFetchWindows 构造一次导入的请求参数：
//   - 月频（SinceYear==0）：单次全量，params 只含接口级固定参数；
//   - 日频（SinceYear>0）：按年分段 [SinceYear, 当前年]，每段叠加
//     start_date/end_date（YYYYMMDD）。
func macroFetchWindows(spec macroIndicatorSpec, now time.Time) []map[string]interface{} {
	base := map[string]interface{}{}
	for k, v := range spec.Params {
		base[k] = v
	}
	if spec.SinceYear == 0 {
		return []map[string]interface{}{base}
	}
	windows := make([]map[string]interface{}, 0, now.Year()-spec.SinceYear+1)
	for y := spec.SinceYear; y <= now.Year(); y++ {
		w := map[string]interface{}{
			"start_date": fmt.Sprintf("%04d0101", y),
			"end_date":   fmt.Sprintf("%04d1231", y),
		}
		for k, v := range base {
			w[k] = v
		}
		windows = append(windows, w)
	}
	return windows
}

// macroWindowsOrErr 取 spec 的请求窗口；空序列是目录配置错误（SinceYear
// 晚于当前年份——导入与探测都会静默什么都不做），fail-loud。
func macroWindowsOrErr(spec macroIndicatorSpec, now time.Time) ([]map[string]interface{}, error) {
	windows := macroFetchWindows(spec, now)
	if len(windows) == 0 {
		return nil, fmt.Errorf("宏观指标 %s 无可用请求窗口（SinceYear=%d 晚于当前年份 %d，目录配置不自洽）",
			spec.Indicator, spec.SinceYear, now.Year())
	}
	return windows, nil
}

// normalizedMacroRows 是 normalize 的产出：Rows 可入库，三个跳过计数解释
// 「为什么少了行」（坏日期 / 数值未发布 / 脏值，三种不同变故不能混）。
type normalizedMacroRows struct {
	Rows            []domain.MacroSeries
	SkippedBadDate  int
	SkippedNilValue int
	SkippedBadValue int
}

// Skipped 是总跳过行数。
func (n normalizedMacroRows) Skipped() int {
	return n.SkippedBadDate + n.SkippedNilValue + n.SkippedBadValue
}

// normalizeMacroSeries 把宏观接口响应转成 macro_series 行。
//
//   - obs_date 按 spec.DateLayout 解析；月频 YYYYMM 取当月首日；
//   - value 为 nil（未发布）的行跳过——缺失折 0 等于编数据；
//   - 非 nil 但不可解析/非有限同样跳过并单独计数；
//   - 缺日期列或数值列（整个响应没有该列）→ 报错，形状不符契约。
func normalizeMacroSeries(resp *response, spec macroIndicatorSpec, source string, asOf time.Time) (normalizedMacroRows, error) {
	iDate := columnIndex(resp, spec.DateField)
	iVal := columnIndex(resp, spec.Field)
	if iDate < 0 || iVal < 0 {
		return normalizedMacroRows{}, fmt.Errorf("%s 响应缺 %s/%s 列（实际 fields=%v）",
			spec.APIName, spec.DateField, spec.Field, resp.Data.Fields)
	}

	var out normalizedMacroRows
	for _, item := range resp.Data.Items {
		obs, ok := parseMacroObsDate(tushareCell(item, iDate), spec.DateLayout)
		if !ok {
			out.SkippedBadDate++
			continue
		}
		if iVal >= len(item) || item[iVal] == nil {
			out.SkippedNilValue++
			continue
		}
		value, ok := tushareFloatOK(item, iVal)
		if !ok {
			out.SkippedBadValue++
			continue
		}
		out.Rows = append(out.Rows, domain.MacroSeries{
			Indicator: spec.Indicator,
			Freq:      spec.Freq,
			ObsDate:   obs,
			Value:     value,
			Unit:      spec.Unit,
			Source:    source,
			AsOf:      asOf,
		})
	}
	return out, nil
}

// macroAllSkippedError 是 fail-loud 闸门：一次响应有行却一行都没能入库时
// 拒绝静默成功（上游改格式、列整体变空的典型形态）。真空响应不在此列。
func macroAllSkippedError(api, indicator string, items int, res normalizedMacroRows) error {
	if items == 0 || len(res.Rows) > 0 {
		return nil
	}
	return dataQuality("宏观指标 %s：%s 响应返回 %d 行但全部被跳过（坏日期 %d / 数值 nil %d / 坏值 %d）——上游形状/格式变故，拒绝以「导入 0 行」伪装成功",
		indicator, api, items, res.SkippedBadDate, res.SkippedNilValue, res.SkippedBadValue)
}

// parseMacroObsDate 解析观测日期：月频 "200601" → 当月首日；日频 "20060102"
// → 自然日。空串/解析失败 → (零值, false)。
func parseMacroObsDate(s, layout string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
