package tushare

// 复权基准（ADR-0011）：qfq 的基准日/基准因子必须是**该 symbol 全局唯一**的，
// 不随请求窗口漂移。本文件是基准的解析与记账规则；取数与归一在 `stocks.go`。
//
// 解析顺序（固定，别改）：
//  1. 账本里该 symbol 的最新记录 —— 唯一权威；
//  2. 无账本但 qfq 表已有该 symbol 数据 —— 用 `MAX(trade_date)`（老口径的基准
//     就是当时窗口的最新交易日，且那一行必然已落库）作为初始基准并记账；
//  3. 首次入库 —— 用本次窗口的最新因子作为初始基准并记账；
//  4. 账本一旦建立**不自动推进**：否则每个增量作业都要重写全部历史；
//     人工改基准走 `baseline_date`（必须配合 overwrite）。
//
// 手工改基准会写一条 `previous_*` 齐全的 manual 记录，变更可审计。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BaselineLedger 是复权基准账本端口（PG 实现见 `store.AdjustBaselineStore`）。
// 传输入口只依赖这三个方法，不 import store。
type BaselineLedger interface {
	// Load 返回该 symbol 当前基准；账本为空 ok=false。
	Load(ctx context.Context, symbol string) (baseDate time.Time, baseFactor float64, ok bool, err error)
	// Save 追加一条基准记录（append-only）。
	Save(ctx context.Context, symbol string, baseDate time.Time, baseFactor float64,
		prevDate *time.Time, prevFactor *float64, reason string) error
	// LatestTradeDate 返回该 symbol 已落库的最新交易日；无数据 ok=false。
	LatestTradeDate(ctx context.Context, symbol string) (date time.Time, ok bool, err error)
}

// 账本 reason 取值（与 `market.adjust_baseline_changes` 的 CHECK 一致）。
const (
	BaselineReasonBootstrap = "bootstrap"
	BaselineReasonManual    = "manual"
)

// resolveBaseline 按文件头的顺序解析基准并完成必要的记账。
//
// factors 是本次窗口内的 trade_date(YYYYMMDD) → adj_factor。返回的因子一定
// > 0；窗口内一个因子都没有时返回 0（调用方据此判定「合法空窗口」，
// 连账本都不碰）。
func (c *Client) resolveBaseline(ctx context.Context, symbol string, factors map[string]float64, requested string) (float64, error) {
	if key := strings.TrimSpace(requested); key != "" {
		return c.manualBaseline(ctx, symbol, formatDate(key), factors)
	}

	_, baseFactor, ok, err := c.Baselines.Load(ctx, symbol)
	if err != nil {
		return 0, fmt.Errorf("tushare: load adjust baseline for %s: %w", symbol, err)
	}
	if ok {
		return baseFactor, nil
	}

	// 无账本：优先用已落库数据重建老口径的基准。
	if latest, hasData, err := c.Baselines.LatestTradeDate(ctx, symbol); err != nil {
		return 0, fmt.Errorf("tushare: latest trade date for %s: %w", symbol, err)
	} else if hasData {
		key := latest.Format("20060102")
		f, found, err := c.adjFactorOn(ctx, symbol, key, factors)
		if err != nil {
			return 0, err
		}
		if !found {
			// 库里有这一行却没有因子：账本无法重建，继续按别的基准算会
			// 悄悄改变历史价格。只能响亮失败。
			return 0, dataQuality("%s: 已落库的最新交易日 %s 查不到 adj_factor，无法重建全局复权基准", symbol, key)
		}
		return c.bootstrap(ctx, symbol, key, f)
	}

	// 首次入库：用本次窗口的最新因子。
	latest := latestFactorDate(factors)
	if latest == "" {
		return 0, nil
	}
	return c.bootstrap(ctx, symbol, latest, factors[latest])
}

// manualBaseline 处理显式基准日：因子必须可查（窗口内没有就单查一天）。
func (c *Client) manualBaseline(ctx context.Context, symbol, key string, factors map[string]float64) (float64, error) {
	t, err := time.Parse("20060102", key)
	if err != nil {
		return 0, invalidInput("%s: baseline_date %q is not a date (YYYY-MM-DD or YYYYMMDD)", symbol, key)
	}
	f, found, err := c.adjFactorOn(ctx, symbol, key, factors)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, invalidInput("%s: baseline_date %s has no adj_factor (没有因子的日期不能当基准)", symbol, key)
	}

	prevDate, prevFactor, hasPrev, err := c.Baselines.Load(ctx, symbol)
	if err != nil {
		return 0, fmt.Errorf("tushare: load adjust baseline for %s: %w", symbol, err)
	}
	if hasPrev && prevDate.Format("20060102") == key && prevFactor == f {
		// 幂等：同基准重放不追加账本记录。
		return f, nil
	}
	var prevD *time.Time
	var prevF *float64
	if hasPrev {
		prevD, prevF = &prevDate, &prevFactor
	}
	if err := c.Baselines.Save(ctx, symbol, t, f, prevD, prevF, BaselineReasonManual); err != nil {
		return 0, fmt.Errorf("tushare: save manual baseline for %s: %w", symbol, err)
	}
	return f, nil
}

// bootstrap 用初始基准记账，并读回生效值。
//
// 读回是必须的：并发的两个 worker 可能同时首拉同一只票，`bootstrap` 的局部
// 唯一索引只让一个赢家写入；输的一方必须用赢家的基准算价格，否则会写下
// 一批没被账本承认的价格。
func (c *Client) bootstrap(ctx context.Context, symbol, key string, factor float64) (float64, error) {
	t, err := time.Parse("20060102", key)
	if err != nil {
		return 0, dataQuality("%s: 基准日 %q 不可解析", symbol, key)
	}
	if err := c.Baselines.Save(ctx, symbol, t, factor, nil, nil, BaselineReasonBootstrap); err != nil {
		return 0, fmt.Errorf("tushare: save bootstrap baseline for %s: %w", symbol, err)
	}
	_, effective, ok, err := c.Baselines.Load(ctx, symbol)
	if err != nil {
		return 0, fmt.Errorf("tushare: reload adjust baseline for %s: %w", symbol, err)
	}
	if !ok || effective <= 0 {
		return 0, fmt.Errorf("tushare: bootstrap baseline for %s was not persisted", symbol)
	}
	return effective, nil
}

// adjFactorOn 取某一天的复权因子：优先用本次窗口的映射，缺了就单查一天。
func (c *Client) adjFactorOn(ctx context.Context, symbol, key string, factors map[string]float64) (float64, bool, error) {
	if f, ok := factors[key]; ok && f > 0 {
		return f, true, nil
	}
	resp, err := c.call(ctx, "adj_factor", map[string]interface{}{
		"ts_code":    symbol,
		"start_date": key,
		"end_date":   key,
	}, "ts_code,trade_date,adj_factor")
	if err != nil {
		return 0, false, err
	}
	for _, item := range resp.Data.Items {
		if len(item) < 3 || tushareCell(item, 1) != key {
			continue
		}
		if f := fieldFloat(item, 2); f > 0 {
			return f, true, nil
		}
	}
	return 0, false, nil
}

// latestFactorDate 返回 factors 里最大的日期（YYYYMMDD 的字典序即时间序）。
func latestFactorDate(factors map[string]float64) string {
	latest := ""
	for d, f := range factors {
		if f > 0 && d > latest {
			latest = d
		}
	}
	return latest
}
