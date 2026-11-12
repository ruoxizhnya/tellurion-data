package store_test

// 退市腿的真源 + 真写面端到端（默认 skip）。
//
// 跑法：
//
//	$env:TUSHARE_ONLINE="1"; $env:TUSHARE_TOKEN="<token>"; $env:DATABASE_URL="postgres://.../tellurion?sslmode=disable"
//	go test ./internal/store -run TestReferenceRunner_LiveDelistedStocks -v
//
// 它证明的是 `DATA-REFDATA-JOB-WRITE` 退市腿的四件事，全部走**正常作业路径**
// （`sync.ReferenceRunner` → tushare → `ReferenceWriter`），不手写 SQL 改数：
//   - 上游 `stock_basic` 的 D 段可达，作业参数 `list_status=D` 不被缺省 L 吞掉；
//   - D 行的 `delist_date` 真的写进表，`status` 归一成 `delisted`；
//   - 同一次 D 作业**不碰在市票**（active 行数前后不变，无一行被误标退市）；
//   - 重跑幂等（总行数不翻倍）。
//
// 写面隔离在临时 schema（`scratchSchema`），**不写部署库 `market.*`**。
// 把 D 段真正灌进部署库是另一次运维动作（需授权），不是本测试的职责。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/store"
	syncjob "tellurion-data/internal/sync"
	"tellurion-data/internal/transport/tushare"
)

func TestReferenceRunner_LiveDelistedStocks(t *testing.T) {
	if os.Getenv("TUSHARE_ONLINE") != "1" {
		t.Skip("set TUSHARE_ONLINE=1 to hit api.tushare.pro")
	}
	token := strings.TrimSpace(os.Getenv("TUSHARE_TOKEN"))
	if token == "" {
		t.Skip("TUSHARE_TOKEN is required")
	}

	pool := pgPool(t)
	scratch := scratchSchema(t, pool)
	writer, err := store.NewReferenceWriter(store.PoolWriteDB(pool), store.Options{MarketSchema: scratch})
	require.NoError(t, err)

	runner := &syncjob.ReferenceRunner{
		Transport: tushare.NewDefault(token),
		Writer:    writer,
		// "现在"用真实时钟：D 段的退市日必须早于今天才会被归一成 delisted。
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	countWhere := func(where string) int {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT count(*) FROM "+scratch+".stocks WHERE "+where).Scan(&n))
		return n
	}

	// 1) L 段：真实在市清单入库，作为「在市票」基线。
	listed, err := runner.Execute(ctx, syncjob.Job{JobType: syncjob.JobTypeStocks})
	require.NoError(t, err)
	require.Greater(t, listed.Persisted, 4000, "listed stocks look truncated")
	assert.Equal(t, listed.Fetched, listed.Persisted)
	activeBefore := countWhere("status = 'active'")
	assert.Equal(t, listed.Persisted, activeBefore, "every listed row must land as active")
	assert.Zero(t, countWhere("status = 'delisted'"), "L 段不该产出 delisted 行")

	// 2) D 段：显式 list_status=D 走作业路径。
	delisted, err := runner.Execute(ctx, syncjob.Job{
		JobType: syncjob.JobTypeStocks,
		Params:  map[string]any{"list_status": "D"},
	})
	require.NoError(t, err)
	require.Greater(t, delisted.Fetched, 0, "上游 D 段为空 = 无法验证退市识别")
	assert.Equal(t, delisted.Fetched, delisted.Persisted)
	assert.Equal(t, delisted.Fetched, countWhere("status = 'delisted'"))
	assert.Zero(t, countWhere("status = 'delisted' AND delist_date IS NULL"),
		"每一行 delisted 都必须带 delist_date")

	// 3) 在市票没有被 D 段误标：active 行数前后一致。
	assert.Equal(t, activeBefore, countWhere("status = 'active'"),
		"D 段不得把在市票改成别的状态")

	// 4) 重跑幂等：总行数不变、delisted 行数不变。
	totalBefore := countWhere("TRUE")
	_, err = runner.Execute(ctx, syncjob.Job{
		JobType: syncjob.JobTypeStocks,
		Params:  map[string]any{"list_status": "D"},
	})
	require.NoError(t, err)
	assert.Equal(t, totalBefore, countWhere("TRUE"), "D 段重跑必须幂等，不许追加重复行")
	assert.Equal(t, delisted.Fetched, countWhere("status = 'delisted'"))

	// 留一条可复核的样本在日志里（报告引用它）。
	var sampleSymbol string
	var sampleDate time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT symbol, delist_date FROM "+scratch+".stocks WHERE status = 'delisted' ORDER BY delist_date DESC LIMIT 1").
		Scan(&sampleSymbol, &sampleDate))
	t.Logf("D 段 %d 行；样本 %s delist_date=%s", delisted.Fetched, sampleSymbol, sampleDate.Format("2006-01-02"))
}
