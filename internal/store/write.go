package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tellurion-data/internal/normalize"
	"tellurion-data/internal/schema"
)

// 本文件是 X1d 的**写面**：`BulkInsert` 把一批点位写进 A 集表。
//
// 相对 core `tellurion:pkg/storage/bulk_insert.go` 的两处刻意差异：
//  1. **幂等语义**从 `ON CONFLICT DO UPDATE`（last write wins）改为
//     `ON CONFLICT DO NOTHING` + 内容比对：
//     - 同内容重写 ⇒ no-op（`docs/dataflow.md` §4 的不变量）；
//     - 同主键不同内容 ⇒ `ErrContentConflict` + **整批回滚**
//       （覆盖旧数据等于让「同一 hash 不同内容」重新出现）。
//  2. **visible_at 是整批门**：任一点 `VisibleAt` 为零 ⇒ 整批拒收
//     （`docs/db-schema.md` §6.6）。core 没有这道门，不能照抄。
//
// 点位类型是本包的本地镜像（`Point`）：store 不 import ingest，保持
// 「存储不认识采集语义」的依赖方向；投影在装配层完成。

// Point 是写面点位的本地镜像，字段与 `ingest.UnifiedDataPoint` 逐字对齐。
type Point struct {
	Symbol     string
	TradeTime  time.Time
	Source     string
	DataType   string
	Data       map[string]interface{}
	IngestTime time.Time
	VisibleAt  time.Time
}

// 写面哨兵。errors.Is 分诊：
var (
	// ErrUnmappedDataType 是该 data_type 在写面没有映射（既不在 A 集 spec，
	// 也不在 B/C 集登记表）。它是**store 内部诊断**，不是 wire 层的
	// `ingest.ErrUnsupported` 第二名字——按 `api-schema.md` §2 的纪律，
	// 同义哨兵不许两义。
	ErrUnmappedDataType = fmt.Errorf("store: data type is not mapped for bulk insert")
	// ErrWriteTableNotInASet 是目标表不在 A 集（B 集走自己的同步腿，C 集在门外）。
	// 写面见到它必须拒，而不是猜一列塞可见时刻。
	ErrWriteTableNotInASet = fmt.Errorf("store: bulk insert is only allowed on visible_at anchor set (A)")
	// ErrVisibleAtMissing 与取数侧 / ingest 闸门是同一个哨兵：缺可见时间
	// 在三条路径上都是「整批拒收」，不该有三个名字。
	ErrVisibleAtMissing = normalize.ErrVisibleAtMissing
	// ErrContentConflict 是同主键已存在且内容不同 —— 覆盖旧行等于制造
	// 「同 hash 不同内容」，整批回滚。
	ErrContentConflict = fmt.Errorf("store: row exists with different content for the same primary key")
	// ErrInvalidPoint 是点位形态不合法（缺 symbol / trade_time / ingest_time、
	// 载荷字段类型不对）。整批拒收，不做「跳过坏行」。
	ErrInvalidPoint = fmt.Errorf("store: invalid data point")
)

// CommandTag 是 Exec 的最小返回面（`pgx.CommandTag` 直接满足）。
type CommandTag interface {
	RowsAffected() int64
}

// Row 是单行查询的最小返回面（`pgx.Row` 直接满足）。
type Row interface {
	Scan(dest ...any) error
}

// WriteTx 是写事务的窄接口。抽出来只为让 SQL 装配可被无库测试。
type WriteTx interface {
	Exec(ctx context.Context, sql string, args ...any) (CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// WriteDB 是写连接的最小面。`*pgxpool.Pool` 经 `PoolWriteDB` 适配。
type WriteDB interface {
	BeginTx(ctx context.Context) (WriteTx, error)
}

// Writer 是 `BulkInsert` 的 PG 实现。
type Writer struct {
	db     WriteDB
	prefix string
}

// NewWriter 构造写面。prefix 口径与 `New` 一致（空串 = public）。
func NewWriter(db WriteDB, opts Options) (*Writer, error) {
	if db == nil {
		return nil, fmt.Errorf("store: nil write db")
	}
	prefix := strings.TrimSpace(opts.MarketSchema)
	if prefix == "" {
		prefix = "public"
	}
	if strings.Contains(prefix, ".") {
		return nil, fmt.Errorf("store: market schema %q must not contain a dot", prefix)
	}
	return &Writer{db: db, prefix: prefix}, nil
}

// PoolWriteDB 把 `*pgxpool.Pool` 适配成 `WriteDB`。
func PoolWriteDB(pool *pgxpool.Pool) WriteDB { return pgxWriteDB{pool: pool} }

type pgxWriteDB struct{ pool *pgxpool.Pool }

func (d pgxWriteDB) BeginTx(ctx context.Context) (WriteTx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgxWriteTx{tx: tx}, nil
}

type pgxWriteTx struct{ tx pgx.Tx }

func (t pgxWriteTx) Exec(ctx context.Context, sql string, args ...any) (CommandTag, error) {
	return t.tx.Exec(ctx, sql, args...)
}

func (t pgxWriteTx) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

func (t pgxWriteTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t pgxWriteTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

// writeSpec 是 data_type → 表的写面形状。
type writeSpec struct {
	Table   string
	Columns []string
	PK      []string
	// Numeric 是必须强转成 float64 的列（其余从 Data 原样取）。
	Numeric []string
	// DateColumns 是 DATE 类型列（比对时显式 `::date`，避免与 timestamptz
	// 参数比较时被服务器时区截断到别的日期）。
	DateColumns []string
}

// writeSpecs 只覆盖 A 集 12 张表；列形状逐表对齐 core
// `pkg/storage/bulk_insert.go` 的 TableMapper（2026-10-24 实测），并在每张
// 表尾部追加 `visible_at`。
var writeSpecs = map[string]writeSpec{
	"ohlcv_daily": {
		Table:       "ohlcv_daily_qfq",
		Columns:     []string{"symbol", "trade_date", "open", "high", "low", "close", "volume", "turnover", "trade_days", "visible_at"},
		PK:          []string{"symbol", "trade_date"},
		Numeric:     []string{"open", "high", "low", "close", "volume", "turnover", "trade_days"},
		DateColumns: []string{"trade_date"},
	},
	"ohlcv_minute": {
		Table: "ohlcv_minute",
		Columns: []string{"symbol", "ts", "open", "high", "low", "close", "volume", "amount",
			"source", "ingest_time", "visible_at"},
		PK:      []string{"symbol", "ts"},
		Numeric: []string{"open", "high", "low", "close", "volume", "amount"},
	},
	"realtime_quote": {
		Table: "realtime_quote",
		Columns: []string{"symbol", "ts", "price", "open", "high", "low", "last_close",
			"volume", "amount",
			"bid1", "bid1_vol", "ask1", "ask1_vol",
			"bid2", "bid2_vol", "ask2", "ask2_vol",
			"bid3", "bid3_vol", "ask3", "ask3_vol",
			"bid4", "bid4_vol", "ask4", "ask4_vol",
			"bid5", "bid5_vol", "ask5", "ask5_vol",
			"source", "ingest_time", "visible_at"},
		PK:      []string{"symbol", "ts"},
		Numeric: []string{"price", "open", "high", "low", "last_close", "volume", "amount", "bid1", "bid1_vol", "ask1", "ask1_vol", "bid2", "bid2_vol", "ask2", "ask2_vol", "bid3", "bid3_vol", "ask3", "ask3_vol", "bid4", "bid4_vol", "ask4", "ask4_vol", "bid5", "bid5_vol", "ask5", "ask5_vol"},
	},
	"capital_flow": {
		Table: "capital_flow",
		Columns: []string{"symbol", "trade_date", "period", "main_net", "main_buy_amount",
			"main_sell_amount", "super_net", "large_net", "medium_net", "small_net",
			"main_net_ratio", "retail_net", "retail_net_ratio", "close_price", "change_pct",
			"source", "ingest_time", "visible_at"},
		PK:          []string{"symbol", "trade_date", "period"},
		Numeric:     []string{"main_net", "main_buy_amount", "main_sell_amount", "super_net", "large_net", "medium_net", "small_net", "main_net_ratio", "retail_net", "retail_net_ratio", "close_price", "change_pct"},
		DateColumns: []string{"trade_date"},
	},
	"sectors": {
		Table:       "sectors",
		Columns:     []string{"sector_code", "sector_name", "category", "trade_date", "change_pct", "leading_symbol", "source", "ingest_time", "visible_at"},
		PK:          []string{"sector_code"},
		Numeric:     []string{"change_pct"},
		DateColumns: []string{"trade_date"},
	},
	"stock_sector": {
		Table:   "stock_sector_map",
		Columns: []string{"symbol", "sector_code", "sector_name", "source", "ingest_time", "visible_at"},
		PK:      []string{"symbol", "sector_code"},
	},
	"top_list": {
		Table:       "top_list",
		Columns:     []string{"trade_date", "symbol", "name", "net_buy", "buy_amount", "sell_amount", "turnover", "reason", "source", "ingest_time", "visible_at"},
		PK:          []string{"trade_date", "symbol"},
		Numeric:     []string{"net_buy", "buy_amount", "sell_amount", "turnover"},
		DateColumns: []string{"trade_date"},
	},
	"limit_up_pool": {
		Table:       "limit_up_pool",
		Columns:     []string{"trade_date", "symbol", "name", "limit_price", "first_time", "last_time", "limit_times", "continuous", "industry", "source", "ingest_time", "visible_at"},
		PK:          []string{"trade_date", "symbol"},
		Numeric:     []string{"limit_price"},
		DateColumns: []string{"trade_date"},
	},
	"announcements": {
		Table:   "announcements",
		Columns: []string{"ann_id", "symbol", "ann_title", "ann_time", "ann_type", "pdf_url", "source", "ingest_time", "visible_at"},
		PK:      []string{"ann_id"},
	},
	"news": {
		Table:   "news",
		Columns: []string{"news_id", "symbol", "title", "content", "publish_time", "url", "source_name", "source", "ingest_time", "visible_at"},
		PK:      []string{"news_id"},
	},
	"hot_search": {
		Table:   "hot_search",
		Columns: []string{"rank", "keyword", "snapshot_time", "heat", "source", "ingest_time", "visible_at"},
		PK:      []string{"rank", "snapshot_time"},
		Numeric: []string{"heat"},
	},
	"global_ohlcv": {
		Table:       "global_ohlcv",
		Columns:     []string{"symbol", "trade_date", "open", "high", "low", "close", "volume", "adj_close", "source", "ingest_time", "visible_at"},
		PK:          []string{"symbol", "trade_date"},
		Numeric:     []string{"open", "high", "low", "close", "volume", "adj_close"},
		DateColumns: []string{"trade_date"},
	},
}

// writeExcludedTypes 让 B 集 / C 集有明确的类型名可查：写面见到它们必须报
// ErrWriteTableNotInASet（带分类），而不是「未知类型」——两者对运维的处置不同。
var writeExcludedTypes = map[string]string{
	"daily_basic":  "market_activity",    // B 集：as_of，走 daily_basic 同步腿
	"macro_series": "quant.macro_series", // B 集：as_of，走 macro 同步腿
	"fundamentals": "stock_fundamentals", // C 集：判据走 ann_date（§6.3）
}

// specFor 解析 data_type 的写面形状，并过三集合门。
func (w *Writer) specFor(dataType string) (writeSpec, error) {
	if spec, ok := writeSpecs[dataType]; ok {
		class, known := schema.GateClassFor(spec.Table)
		if !known || class != schema.GateClassA {
			// 映射表与 schema 三集合漂移：宁可拒写，也不猜哪一档。
			return writeSpec{}, fmt.Errorf("%w: %s maps to %s (gate class %q)", ErrWriteTableNotInASet, dataType, spec.Table, class)
		}
		return spec, nil
	}
	if table, ok := writeExcludedTypes[dataType]; ok {
		class, _ := schema.GateClassFor(table)
		return writeSpec{}, fmt.Errorf("%w: %s maps to %s (gate class %q)", ErrWriteTableNotInASet, dataType, table, class)
	}
	return writeSpec{}, fmt.Errorf("%w: %q", ErrUnmappedDataType, dataType)
}

// BulkInsert 写一批点。返回值口径：
//   - persisted = **请求点已就位**（新插入 + 同内容 no-op），不是「新增行数」；
//   - skipped = 因载荷不合法被跳过的行（与 core 同形；缺 visible_at 不在此列，
//     那是整批拒收）。
//
// 失败即整批回滚：`ErrContentConflict` 之前已插入的行也一并撤销。
func (w *Writer) BulkInsert(ctx context.Context, dataType string, points []Point) (int, int, error) {
	if len(points) == 0 {
		return 0, 0, nil
	}
	spec, err := w.specFor(dataType)
	if err != nil {
		return 0, 0, err
	}

	// 先做整批校验与参数构造：任一可见时刻缺失 ⇒ 整批拒收（事务都不开）。
	type row struct {
		symbol string
		args   []any
	}
	rows := make([]row, 0, len(points))
	skipped := 0
	for i, p := range points {
		if p.DataType != "" && p.DataType != dataType {
			return 0, 0, fmt.Errorf("%w: point[%d] data type %q != %q", ErrInvalidPoint, i, p.DataType, dataType)
		}
		if p.Symbol == "" || p.TradeTime.IsZero() {
			return 0, 0, fmt.Errorf("%w: point[%d] has empty symbol or zero trade time", ErrInvalidPoint, i)
		}
		if p.IngestTime.IsZero() {
			return 0, 0, fmt.Errorf("%w: point[%d] has zero ingest time (the pipeline must stamp it)", ErrInvalidPoint, i)
		}
		if p.VisibleAt.IsZero() {
			// D3 的第二道防线：即使调用方绕过了 ingest.GatePoints，这里也拒。
			return 0, 0, fmt.Errorf("%w: point[%d] %s has no visible_at", ErrVisibleAtMissing, i, p.Symbol)
		}
		args, err := buildInsertArgs(p, spec)
		if err != nil {
			// 与 core 同形：坏载荷跳过单行（不是整批）。
			skipped++
			continue
		}
		rows = append(rows, row{symbol: p.Symbol, args: args})
	}
	if len(rows) == 0 {
		return 0, skipped, nil
	}

	tx, err := w.db.BeginTx(ctx)
	if err != nil {
		return 0, skipped, fmt.Errorf("store: bulk insert begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	insertSQL := buildInsertSQL(w.prefix+"."+spec.Table, spec.Columns)
	compareSQL := buildCompareSQL(w.prefix+"."+spec.Table, spec.Columns, spec.DateColumns)
	persisted := 0
	for i, r := range rows {
		tag, err := tx.Exec(ctx, insertSQL, r.args...)
		if err != nil {
			return 0, skipped, fmt.Errorf("store: bulk insert row %d: %w", i, err)
		}
		if tag.RowsAffected() == 1 {
			persisted++
			continue
		}
		// 冲突：同主键是否已是同一内容。同内容 ⇒ no-op（幂等重写）；
		// 异内容 ⇒ 整批回滚 + fail-loud。
		compareArgs := compareArgsFor(spec, r.args)
		var same bool
		if err := tx.QueryRow(ctx, compareSQL, compareArgs...).Scan(&same); err != nil {
			return 0, skipped, fmt.Errorf("store: bulk insert compare row %d: %w", i, err)
		}
		if !same {
			return 0, skipped, fmt.Errorf("%w: point[%d] %s %s", ErrContentConflict, i, r.symbol, dataType)
		}
		persisted++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, skipped, fmt.Errorf("store: bulk insert commit: %w", err)
	}
	return persisted, skipped, nil
}

// buildInsertSQL 构造 `INSERT ... ON CONFLICT DO NOTHING`。
// 不指定冲突目标：主键与表级 UNIQUE 的冲突都应走 no-op 分支，再由内容比对定性。
func buildInsertSQL(table string, cols []string) string {
	ph := make([]string, len(cols))
	for i := range cols {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	return "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" +
		strings.Join(ph, ", ") + ") ON CONFLICT DO NOTHING"
}

// buildCompareSQL 构造「整行内容相同」的存在性查询。
//
// 比较全部列（主键 + 内容），排除易变列 `ingest_time`（每次作业不同，
// 比它必然假冲突）；`source` 保留：换源是行内容真实变化，应 fail-loud。
// NULL 一律用 `IS NOT DISTINCT FROM` 比较（`=` 遇 NULL 得 NULL）。
func buildCompareSQL(table string, cols, dateCols []string) string {
	dateSet := make(map[string]bool, len(dateCols))
	for _, c := range dateCols {
		dateSet[c] = true
	}

	conds := make([]string, 0, len(cols))
	args := 0
	for _, c := range cols {
		if c == "ingest_time" {
			continue
		}
		args++
		ph := fmt.Sprintf("$%d", args)
		if dateSet[c] {
			// DATE 列与 timestamptz 参数比较前显式截断，避免服务器时区换日。
			ph = "(" + ph + ")::date"
		}
		conds = append(conds, c+" IS NOT DISTINCT FROM "+ph)
	}
	return "SELECT EXISTS (SELECT 1 FROM " + table + " WHERE " + strings.Join(conds, " AND ") + ")"
}

// compareArgsFor 按 buildCompareSQL 的列序（全列排除 ingest_time）取参数。
func compareArgsFor(spec writeSpec, args []any) []any {
	out := make([]any, 0, len(args))
	for i, c := range spec.Columns {
		if c == "ingest_time" {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// buildInsertArgs 把 Point 投影成列值。列值规则与 core 同形：
// 标准列走类型化字段，其余从 Data 取；numeric 列强转 float64；缺失 = NULL。
func buildInsertArgs(p Point, spec writeSpec) ([]any, error) {
	numSet := make(map[string]bool, len(spec.Numeric))
	for _, c := range spec.Numeric {
		numSet[c] = true
	}
	args := make([]any, 0, len(spec.Columns))
	for _, c := range spec.Columns {
		var v any
		switch c {
		case "symbol", "ts_code":
			v = p.Symbol
		case "trade_date":
			v = p.TradeTime
		case "ts", "snapshot_time", "ann_time", "publish_time", "first_time", "last_time":
			v = p.TradeTime
		case "source":
			v = p.Source
		case "ingest_time":
			v = p.IngestTime
		case "visible_at":
			v = p.VisibleAt
		default:
			raw, ok := p.Data[c]
			if !ok || raw == nil {
				v = nil
				break
			}
			if numSet[c] {
				f, err := toFloat64(raw)
				if err != nil {
					return nil, fmt.Errorf("%w: column %s: %v", ErrInvalidPoint, c, err)
				}
				v = f
			} else {
				v = raw
			}
		}
		args = append(args, v)
	}
	return args, nil
}

// toFloat64 与 core 同形（含 json.Number；字符串拒绝强转）。
func toFloat64(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int:
		return float64(x), nil
	case int32:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case uint:
		return float64(x), nil
	case uint32:
		return float64(x), nil
	case uint64:
		return float64(x), nil
	case json.Number:
		return x.Float64()
	case string:
		return 0, fmt.Errorf("string %q is not numeric", x)
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
