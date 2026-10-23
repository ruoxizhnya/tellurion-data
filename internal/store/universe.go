package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"tellurion-data/internal/serving"
)

// indexAliases 把「人类写法」的指数标识映射到库里存的代码。
//
// 口径照抄 core `pkg/backtest/index_alias.go`（DATA-01，2026-10-21 实测）：
// 探索链路默认写 `csi300`，而 `index_constituents` 里存 `000300.SH`。两边
// 对不上就是「票池为空 → 回测 fail-loud」，看起来像数据没灌，实际是口径
// 不一致。本表只登记**确有数据源**的指数，不认识的原样返回（随后展开为空，
// 由调用方按既有语义 fail-loud），**不静默猜一个**。
var indexAliases = map[string]string{
	"csi300": "000300.SH",
	"hs300":  "000300.SH",
	"沪深300":  "000300.SH",
	"csi500": "000905.SH",
	"zz500":  "000905.SH",
	"中证500":  "000905.SH",
	"csi800": "000906.SH",
	"zz800":  "000906.SH",
	"中证800":  "000906.SH",
	"sse50":  "000016.SH",
	"上证50":   "000016.SH",
}

// ResolveUniverse 实现 `serving.UniverseResolver`。
//
// 三条互斥入口（`serving.ResolveRequest.Validate` 已挡掉二义）：
//   - 显式 `Symbols`：归一化（去空 / 去重 / 排序）后直接返回；
//   - `IndexCode`：别名归一 → 按 `as_of` 取当时仍在成分内的标的；
//   - `Name`：先当别名表查（`csi300` 一类），查不到即拒。
//
// ⚠️ **as-of 语义**（幸存者偏差）：`in_date <= as_of AND (out_date IS NULL
// OR out_date > as_of)`。拿「今天的成分」跑三年前的回测会系统性高估收益，
// 而曲线照样漂亮——这条比较是本函数存在的理由。
func (s *Store) ResolveUniverse(ctx context.Context, req serving.UniverseRequest) ([]string, error) {
	if len(req.Symbols) > 0 {
		return normalizeSymbols(req.Symbols), nil
	}
	if req.AsOf.IsZero() {
		// 成分是时变的；没有 as_of 就没有正确的成分表，不拿「今天」顶替。
		return nil, fmt.Errorf("%w: universe resolution requires as_of", serving.ErrMissingParameter)
	}

	code := strings.TrimSpace(req.IndexCode)
	if code == "" {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: empty universe", serving.ErrMissingParameter)
		}
		mapped, ok := indexAliases[strings.ToLower(name)]
		if !ok {
			return nil, fmt.Errorf("%w: %q (known aliases: %v)", ErrUnknownUniverse, name, knownAliasKeys())
		}
		code = mapped
	}

	symbols, err := s.constituentsAsOf(ctx, code, req.AsOf)
	if err != nil {
		return nil, err
	}
	return normalizeSymbols(symbols), nil
}

// constituentsAsOf 取某指数在 asOf 时点的成分。
func (s *Store) constituentsAsOf(ctx context.Context, indexCode string, asOf time.Time) ([]string, error) {
	sql := `SELECT DISTINCT symbol FROM ` + s.table("index_constituents") + `
		WHERE index_code = $1
		  AND (in_date IS NULL OR in_date <= $2)
		  AND (out_date IS NULL OR out_date > $2)
		ORDER BY symbol`
	out := make([]string, 0, 300)
	err := s.scanAll(ctx, sql, []any{indexCode, utcDate(asOf)}, func(rows Rows) error {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			return err
		}
		out = append(out, symbol)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: index constituents %s: %w", indexCode, err)
	}
	return out, nil
}

// normalizeSymbols 去空、去重、排序。空 symbol 是参数错误，不静默丢
// （与 `serving.normalizeSymbols` 同口径；两处都留是因为一个在 port 内、
// 一个是 aggregator 的二次防线）。
func normalizeSymbols(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// knownAliasKeys 把别名表的键排序后返回，用于「不认识」时的错误信息
// ——让调用方看见「我们知道哪些」，而不是只看到一句拒绝。
func knownAliasKeys() []string {
	out := make([]string, 0, len(indexAliases))
	for k := range indexAliases {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
