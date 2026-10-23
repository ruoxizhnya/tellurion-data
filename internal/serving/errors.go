package serving

import "errors"

// Fail-closed 哨兵。HTTP 映射（`tellurion-data:docs/api-schema.md` §1/§2）：
//   - ErrMissingParameter / ErrInvalidParameter / ErrUniverseAmbiguous / ErrUnexpectedSymbol ⇒ 400
//   - ErrEmptyUniverse / ErrEmptyCalendar ⇒ 409（明确失败，不返回空数据集）
//   - ErrSnapshotNotFound ⇒ 404（「没这个 hash」）
//   - ErrFactorNotFound ⇒ 404（因子端点未实现 / 没这个因子；**不得用伪数据填**）
//   - ErrSnapshotMismatch ⇒ 500 + 告警（内容与 hash 不符 / 缓存串号）
//
// 哨兵只增不删、不改语义。
var (
	// ErrMissingParameter 是必填查询参数缺失。
	ErrMissingParameter = errors.New("serving: missing required parameter")
	// ErrInvalidParameter 是参数值非法（频率词表外 / 窗口倒置 / 复权口径不识别）。
	ErrInvalidParameter = errors.New("serving: invalid parameter")
	// ErrUniverseAmbiguous 是票池口径二义（universe 与 index_code 同时给，
	// 或显式 symbols 与票池同时给）—— 不静默挑一个优先级。
	ErrUniverseAmbiguous = errors.New("serving: universe is ambiguous")
	// ErrEmptyUniverse 是票池解析为空。返回空数据集等于把「没数据」谎报成
	// 「数据是空的」，必须明确失败。
	ErrEmptyUniverse = errors.New("serving: universe resolved to an empty set")
	// ErrEmptyCalendar 是窗口内交易日为空（日历缺失时算出的 calendar_hash
	// 会把任何窗口都映射成同一个 hash，必须拒）。
	ErrEmptyCalendar = errors.New("serving: trading calendar is empty for the window")
	// ErrEmptyDataVersion 是数据版本为空（它参与 hash，空串不是「未知」的合法表示）。
	ErrEmptyDataVersion = errors.New("serving: data version is empty")
	// ErrUnexpectedSymbol 是 bar 源返回了票池外的 symbol —— 说明上游越界取数，
	// 静默丢弃会让「返回内容」与「metadata 回显」不一致。
	ErrUnexpectedSymbol = errors.New("serving: bar source returned a symbol outside the requested universe")
	// ErrSnapshotNotFound 是按 hash 取数据集时不存在（404）。
	ErrSnapshotNotFound = errors.New("serving: dataset snapshot not found")
	// ErrSnapshotMismatch 是内容与 hash 不符（500 + 告警）。
	ErrSnapshotMismatch = errors.New("serving: dataset snapshot content does not match its hash")
	// ErrFactorNotFound 是因子端点暂无该 (name, hash) 的内容。
	// 今天 `factor_cache` 没有 dataset hash 列，真实实现要等表结构变更；
	// 在那之前该端点**恒 404**，绝不返回「看起来像因子」的伪数据。
	ErrFactorNotFound = errors.New("serving: factor not found")
)
