package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 兜底取数面（与 core `tellurion:pkg/data/source/registry.go` 的 Fetch 同语义）：
// 按 data_type 的链顺序逐个尝试，只对**可重试**失败换源。
//
// 两条哨兵让运维能区分「配置错」和「上游挂了」：
var (
	// ErrAdapterNotRegistered 是链里引用了一个没登记的名字（部署/配置 bug）。
	ErrAdapterNotRegistered = errors.New("ingest: adapter in chain is not registered")
	// ErrAdapterNotRunnable 是登记的对象只有注册面、没有取数面
	// （`Connector` 4 方法 vs `DataSourceAdapter` 9 方法）。
	ErrAdapterNotRunnable = errors.New("ingest: adapter does not implement DataSourceAdapter")
)

// Fetch 用 req.DataType 的兜底链取数。
//
// 行为（逐条对齐 core）：
//   - 链为空 ⇒ ErrUnsupported（不是「返回空结果」）；
//   - disabled 的源跳过（kill-switch）；
//   - `IsRetryable(err)` ⇒ 换下一个源；其它错误 ⇒ 立即返回
//     （参数非法 / 数据质量换源也治不好）；
//   - 成功返回的 `FetchResponse.Source` 缺省填 adapter 名；
//   - `(nil, nil)` 是契约违规，按该源失败继续走链。
//
// 全部源耗尽时返回最后一个错误（`%w` 保留 errors.Is 分诊）。
func (r *Registry) Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}

	r.mu.RLock()
	chain := append([]string(nil), r.chains[req.DataType]...)
	r.mu.RUnlock()

	if len(chain) == 0 {
		return nil, fmt.Errorf("%w: %s (no chain configured)", ErrUnsupported, req.DataType)
	}

	var lastErr error
	tried := make([]string, 0, len(chain))
	for _, name := range chain {
		c, err := r.Get(name)
		if err != nil {
			lastErr = fmt.Errorf("%w: %q (chain %v)", ErrAdapterNotRegistered, name, chain)
			tried = append(tried, name)
			continue
		}
		a, ok := c.(DataSourceAdapter)
		if !ok {
			lastErr = fmt.Errorf("%w: %q", ErrAdapterNotRunnable, name)
			tried = append(tried, name)
			continue
		}
		if !a.Enabled() {
			continue
		}
		if !containsString(a.SupportedTypes(), req.DataType) {
			// 链与 SupportedTypes 漂移：跳过，但记进 tried，耗尽时报出来。
			lastErr = fmt.Errorf("adapter %q in chain does not list %q in SupportedTypes", name, req.DataType)
			tried = append(tried, name)
			continue
		}

		start := time.Now()
		resp, err := a.Fetch(ctx, req)
		if err == nil && resp != nil {
			if resp.Source == "" {
				resp.Source = a.Name()
			}
			resp.FetchedAt = start
			return resp, nil
		}
		if err == nil {
			lastErr = fmt.Errorf("adapter %q returned (nil, nil) response", name)
			tried = append(tried, name)
			continue
		}
		lastErr = err
		tried = append(tried, name)
		if !IsRetryable(err) {
			return nil, err
		}
	}

	if lastErr == nil {
		// 链非空但全部被 disabled 跳过：这不是上游故障，是配置把门全关了。
		return nil, fmt.Errorf("ingest: all adapters for %s are disabled (chain %v)", req.DataType, chain)
	}
	return nil, fmt.Errorf("ingest: all adapters exhausted for %s (tried %v): %w", req.DataType, tried, lastErr)
}
