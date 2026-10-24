package schema_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backfillTestDSNEnv 是 tagged 回填套件**唯一**读取的环境变量。
//
// 生产装配用 `DATABASE_URL`（`cmd/dataservice`），本套件刻意不读它：
// 即便 shell 里带着生产 DSN，`go test -tags backfill` 也只会 skip。
const backfillTestDSNEnv = "DATA_TEST_DSN"

// backfillTestDSN 决定回填套件能否跑、跑在哪（`docs/db-schema.md` §6.5 D2）。
//
// 三条硬约束：
//  1. 只读 DATA_TEST_DSN——生产 DSN 变量不参与判定；
//  2. 库名必须以 `_test` 结尾：生产库 DSN 即使被误设到 DATA_TEST_DSN，
//     也只返回 skip 理由，不连接、不建 schema、不执行回填；
//  3. DSN 已设置但解析失败 ⇒ 返回 error（fail-loud，不当成「没配」跳过）。
func backfillTestDSN(getenv func(string) string) (dsn, skip string, err error) {
	raw := strings.TrimSpace(getenv(backfillTestDSNEnv))
	if raw == "" {
		return "", backfillTestDSNEnv + " not set; skipping visible_at backfill suite (test DB only)", nil
	}

	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse %s: %w", backfillTestDSNEnv, err)
	}

	db := strings.ToLower(strings.TrimSpace(cfg.Database))
	if !strings.HasSuffix(db, "_test") {
		return "", fmt.Sprintf("refusing to backfill database %q: %s must name a *_test database",
			cfg.Database, backfillTestDSNEnv), nil
	}
	return raw, "", nil
}

// 生产 DSN 必须**自动 skip**：无论是只设了 DATABASE_URL，还是把生产库
// 误设进 DATA_TEST_DSN，套件都不能连上去。
func TestBackfillTestDSN_ProductionDatabaseIsSkippedNotRun(t *testing.T) {
	const prodDSN = "postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable"

	env := func(pairs map[string]string) func(string) string {
		return func(key string) string { return pairs[key] }
	}

	// ① 生产变量在册、测试变量缺失 ⇒ 跳过（生产 DSN 不参与判定）。
	_, skip, err := backfillTestDSN(env(map[string]string{"DATABASE_URL": prodDSN}))
	require.NoError(t, err)
	assert.Contains(t, skip, backfillTestDSNEnv+" not set")

	// ② 生产库被误设进 DATA_TEST_DSN ⇒ 跳过，且报文点名库名与 _test 规则。
	dsn, skip, err := backfillTestDSN(env(map[string]string{backfillTestDSNEnv: prodDSN}))
	require.NoError(t, err)
	assert.Empty(t, dsn, "生产库 DSN 不许被返回给调用方")
	assert.Contains(t, skip, "must name a *_test database")
	assert.Contains(t, skip, "quant_trading")

	// ③ 测试库 ⇒ 放行；后缀大小写不敏感。
	const testDSN = "postgres://postgres:postgres@127.0.0.1:5432/tellurion_data_test?sslmode=disable"
	dsn, skip, err = backfillTestDSN(env(map[string]string{backfillTestDSNEnv: testDSN}))
	require.NoError(t, err)
	assert.Empty(t, skip)
	assert.Equal(t, testDSN, dsn)

	const mixedCase = "postgres://postgres:postgres@127.0.0.1:5432/Tellurion_Data_TEST?sslmode=disable"
	dsn, skip, err = backfillTestDSN(env(map[string]string{backfillTestDSNEnv: mixedCase}))
	require.NoError(t, err)
	assert.Empty(t, skip)
	assert.Equal(t, mixedCase, dsn)
}

// 已设置但解析不了 ⇒ fail-loud，不能静默 skip（否则 DSN 写坏会被当成「没配」）。
func TestBackfillTestDSN_BadDSNFailsLoud(t *testing.T) {
	_, _, err := backfillTestDSN(func(string) string {
		return "postgres://postgres@127.0.0.1:not-a-port/tellurion_data_test"
	})
	require.Error(t, err)
}
