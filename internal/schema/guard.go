package schema

// 既有库的**受保护约束回填**（ADR-0009「既有库回填路径」，2026-11-12）。
//
// 为什么不能塞进 `Statements()`：一条 `ALTER TABLE ... ADD CONSTRAINT` 在历史
// 脏库上会**直接失败**，把服务启动打死；而它要不要执行又取决于库里的数据实况
// （目标列有没有 NULL / 越界值）。`Statements()` 的契约是「无条件幂等、空库
// 一次跑完」，装不下这个判断。因此单开一条受保护路径，由启动期
// `applySchema` 在全部建表语句之后调用：
//
//   - 约束已存在 ⇒ 跳过（幂等）；
//   - 目标列有 NULL / 越界值 ⇒ **只 WARN**（违规行数 + 样例），不加约束，
//     服务照常启动；
//   - 校验通过 ⇒ 才执行 `SET NOT NULL` / `ADD CONSTRAINT`。
//
// 回填**不改任何数据、不改语义**：它只是把新建库建表时就有的约束，补到建于
// 该约束之前的既有库上；写侧守卫（`store.ErrMixedTradingExchanges`）在这条
// 约束出现前就已经在拦同样的输入。

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Execer 是约束回填需要的最小 DB 面（`*pgxpool.Pool` 满足）。
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// GuardedConstraint 是一条「建表定义里已有、既有库可能缺」的约束。
type GuardedConstraint struct {
	Table   string   // 形如 market.trading_calendar
	Column  string   // 受约束的列
	Name    string   // 约束名（pg_constraint.conname）
	Allowed []string // CHECK 白名单；空 = 只补 NOT NULL
	NotNull bool     // 是否回填 NOT NULL
}

// GuardedConstraints 返回需要回填的约束（顺序即执行顺序）。
func GuardedConstraints() []GuardedConstraint {
	return []GuardedConstraint{{
		Table:   marketSchema + ".trading_calendar",
		Column:  "exchange",
		Name:    "ck_trading_calendar_exchange",
		Allowed: []string{"SSE", "SZSE", "BSE"},
		NotNull: true,
	}}
}

// ApplyGuardedConstraints 幂等、受保护地补齐既有库缺失的约束。
//
// `warnf` 为 nil 时丢弃告警；返回 error 只表示**技术失败**（连不上、DDL 被拒），
// 数据违规不算 error（那正是「不能击穿启动」的部分）。
func ApplyGuardedConstraints(ctx context.Context, db Execer, warnf func(string, ...any)) error {
	for _, g := range GuardedConstraints() {
		if err := ApplyGuardedConstraint(ctx, db, g, warnf); err != nil {
			return err
		}
	}
	return nil
}

// ApplyGuardedConstraint 是单条约束的回填入口（`GuardedConstraints()` 按序调用它；
// 测试也直接用它指向临时 schema 的 legacy 表）。
func ApplyGuardedConstraint(ctx context.Context, db Execer, g GuardedConstraint, warnf func(string, ...any)) error {
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	if err := g.apply(ctx, db, warnf); err != nil {
		return fmt.Errorf("guarded constraint %s: %w", g.Name, err)
	}
	return nil
}

func (g GuardedConstraint) apply(ctx context.Context, db Execer, warnf func(string, ...any)) error {
	hasConstraint, err := g.constraintExists(ctx, db)
	if err != nil {
		return err
	}
	notNull, err := g.columnNotNull(ctx, db)
	if err != nil {
		return err
	}
	if hasConstraint && (!g.NotNull || notNull) {
		return nil
	}

	needNotNull := g.NotNull && !notNull
	bad, sample, err := g.violations(ctx, db, needNotNull)
	if err != nil {
		return err
	}
	if bad > 0 {
		warnf("约束回填被跳过：%s.%s 有 %d 行违规（样例 %q），本次不加 %s；修复数据后重启即补",
			g.Table, g.Column, bad, sample, g.Name)
		return nil
	}

	if needNotNull {
		if _, err := db.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL", g.Table, g.Column)); err != nil {
			return fmt.Errorf("set not null: %w", err)
		}
	}
	if !hasConstraint && len(g.Allowed) > 0 {
		if _, err := db.Exec(ctx, g.addConstraintSQL()); err != nil {
			return fmt.Errorf("add constraint: %w", err)
		}
	}
	return nil
}

func (g GuardedConstraint) constraintExists(ctx context.Context, db Execer) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1 AND conrelid = to_regclass($2))`,
		g.Name, g.Table).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("probe constraint: %w", err)
	}
	return exists, nil
}

func (g GuardedConstraint) columnNotNull(ctx context.Context, db Execer) (bool, error) {
	var notNull bool
	err := db.QueryRow(ctx,
		`SELECT attnotnull FROM pg_attribute
		 WHERE attrelid = to_regclass($1) AND attname = $2 AND attnum > 0 AND NOT attisdropped`,
		g.Table, g.Column).Scan(&notNull)
	if err != nil {
		return false, fmt.Errorf("probe column %s.%s: %w", g.Table, g.Column, err)
	}
	return notNull, nil
}

// violations 返回违规行数与一个样例值。`needNotNull` 决定是否把 NULL 算违规
// （约束已存在、只差 NOT NULL 时，NULL 才是要拦的那批）。
func (g GuardedConstraint) violations(ctx context.Context, db Execer, needNotNull bool) (int, string, error) {
	where := g.violationWhere(needNotNull)
	if where == "" {
		return 0, "", nil
	}
	var (
		bad    int
		sample string
	)
	err := db.QueryRow(ctx,
		`SELECT count(*), COALESCE(min(COALESCE(`+g.Column+`::text, '<NULL>')), '<none>')
		   FROM `+g.Table+` WHERE `+where).Scan(&bad, &sample)
	if err != nil {
		return 0, "", fmt.Errorf("scan violations: %w", err)
	}
	return bad, sample, nil
}

func (g GuardedConstraint) violationWhere(needNotNull bool) string {
	parts := make([]string, 0, 2)
	if needNotNull {
		parts = append(parts, g.Column+" IS NULL")
	}
	if len(g.Allowed) > 0 {
		parts = append(parts, g.Column+" NOT IN ("+quoteList(g.Allowed)+")")
	}
	return strings.Join(parts, " OR ")
}

func (g GuardedConstraint) addConstraintSQL() string {
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s IN (%s))",
		g.Table, g.Name, g.Column, quoteList(g.Allowed))
}

func quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(quoted, ", ")
}
