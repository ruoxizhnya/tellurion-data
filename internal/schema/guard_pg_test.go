package schema_test

// 受保护约束回填（`schema.ApplyGuardedConstraints`）的真库腿。
//
// 默认 `go test ./...` 不连库：只读 `DATA_TEST_DSN`（库名必须以 `_test` 结尾，
// 复用 `replayDSN` 的判定）。隔离手段同重放套件：临时 schema + 用完 DROP。
//
// 覆盖两件事（ADR-0009「既有库回填路径」）：
//   - 无约束的 legacy 表 → 执行后 NOT NULL 与 CHECK 都在位，且再跑一次是 no-op；
//   - 有违规值的表 → 约束**不加**、列仍可空，WARN 带上违规行数与样例。

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/schema"
)

func TestApplyGuardedConstraints_AddsMissingConstraints(t *testing.T) {
	pool, table := guardedScratchTable(t, []string{"SSE", "SZSE"})

	var warnings []string
	warnf := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	require.NoError(t, schema.ApplyGuardedConstraint(context.Background(), pool, guardedConstraint(table), warnf))
	assert.Empty(t, warnings, "干净数据不该产生告警")

	constraint := guardedConstraint(table)
	exists, err := constraintExists(context.Background(), pool, table, constraint.Name)
	require.NoError(t, err)
	assert.True(t, exists, "CHECK 应被补上")
	assert.True(t, columnNotNull(context.Background(), pool, table, "exchange"), "NOT NULL 应被补上")

	// 幂等：再跑一次不报错、不重复加约束。
	require.NoError(t, schema.ApplyGuardedConstraint(context.Background(), pool, constraint, warnf))
	assert.Empty(t, warnings)
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_constraint WHERE conname = $1 AND conrelid = to_regclass($2)`,
		constraint.Name, table).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestApplyGuardedConstraints_SkipsAndWarnsOnDirtyData(t *testing.T) {
	pool, table := guardedScratchTable(t, []string{"SSE", "NYSE"})

	var warnings []string
	warnf := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	require.NoError(t, schema.ApplyGuardedConstraint(context.Background(), pool, guardedConstraint(table), warnf))

	constraint := guardedConstraint(table)
	exists, err := constraintExists(context.Background(), pool, table, constraint.Name)
	require.NoError(t, err)
	assert.False(t, exists, "有违规值时不许加约束")
	assert.False(t, columnNotNull(context.Background(), pool, table, "exchange"), "有违规值时不许收紧 NOT NULL")

	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "1 行违规")
	assert.Contains(t, warnings[0], "NYSE", "样例值要能定位违规行")
}

// guardedScratchTable 建一张**没有约束的 legacy 形状** trading_calendar，
// 灌入给定 exchange 值，返回池与临时表名。
func guardedScratchTable(t *testing.T, exchanges []string) (*pgxpool.Pool, string) {
	t.Helper()

	dsn, skip, err := replayDSN(os.Getenv)
	require.NoError(t, err, "DATA_TEST_DSN 已设置但无法解析")
	if skip != "" {
		t.Skip(skip)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, pool.Ping(pingCtx), "DATA_TEST_DSN 已设置但连不上（fail-loud）")

	name := fmt.Sprintf("guard_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	_, err = pool.Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})

	table := quoted + ".trading_calendar"
	_, err = pool.Exec(ctx, `CREATE TABLE `+table+` (
		trade_date DATE PRIMARY KEY,
		exchange VARCHAR(10) DEFAULT 'SSE',
		is_trading_day BOOLEAN DEFAULT TRUE
	)`)
	require.NoError(t, err)
	for i, ex := range exchanges {
		_, err = pool.Exec(ctx, "INSERT INTO "+table+" (trade_date, exchange) VALUES ($1, $2)",
			time.Date(2026, time.January, 5+i, 0, 0, 0, 0, time.UTC), ex)
		require.NoError(t, err)
	}
	return pool, table
}

func guardedConstraint(table string) schema.GuardedConstraint {
	return schema.GuardedConstraint{
		Table:   table,
		Column:  "exchange",
		Name:    "ck_trading_calendar_exchange",
		Allowed: []string{"SSE", "SZSE", "BSE"},
		NotNull: true,
	}
}

func constraintExists(ctx context.Context, pool *pgxpool.Pool, table, name string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1 AND conrelid = to_regclass($2))`,
		name, table).Scan(&exists)
	return exists, err
}

func columnNotNull(ctx context.Context, pool *pgxpool.Pool, table, column string) bool {
	var notNull bool
	err := pool.QueryRow(ctx,
		`SELECT attnotnull FROM pg_attribute
		 WHERE attrelid = to_regclass($1) AND attname = $2 AND attnum > 0 AND NOT attisdropped`,
		table, column).Scan(&notNull)
	if err != nil {
		return false
	}
	return notNull
}
