package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/schema"
)

// ─── 无库替身 ────────────────────────────────────────────────────────

type fakeTag struct{ n int64 }

func (t fakeTag) RowsAffected() int64 { return t.n }

type fakeRow struct {
	same bool
	err  error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	p, ok := dest[0].(*bool)
	if !ok {
		return errors.New("fake row: want *bool")
	}
	*p = r.same
	return nil
}

type fakeWriteTx struct {
	execs      []string
	args       [][]any
	affected   []int64
	compares   []bool
	compareErr error
	beginErr   error

	committed  bool
	rolledBack bool
}

func (t *fakeWriteTx) Exec(_ context.Context, sql string, args ...any) (CommandTag, error) {
	i := len(t.execs)
	t.execs = append(t.execs, sql)
	t.args = append(t.args, args)
	n := int64(1)
	if i < len(t.affected) {
		n = t.affected[i]
	}
	return fakeTag{n: n}, nil
}

func (t *fakeWriteTx) QueryRow(context.Context, string, ...any) Row {
	same := len(t.compares) == 0
	if len(t.compares) > 0 {
		same = t.compares[0]
		t.compares = t.compares[1:]
	}
	return fakeRow{same: same, err: t.compareErr}
}

func (t *fakeWriteTx) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *fakeWriteTx) Rollback(context.Context) error {
	t.rolledBack = true
	return nil
}

type fakeWriteDB struct {
	tx         *fakeWriteTx
	beginCalls int
}

func (d *fakeWriteDB) BeginTx(context.Context) (WriteTx, error) {
	d.beginCalls++
	if d.tx.beginErr != nil {
		return nil, d.tx.beginErr
	}
	return d.tx, nil
}

// ─── 用例 ────────────────────────────────────────────────────────────

func validPoint(symbol string) Point {
	return Point{
		Symbol:     symbol,
		TradeTime:  time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Source:     "tushare",
		DataType:   "ohlcv_daily",
		Data:       map[string]interface{}{"open": 1.0, "high": 2.0, "low": 0.5, "close": 1.5, "volume": 100.0},
		IngestTime: time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
		VisibleAt:  time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC),
	}
}

// 漏 visible_at ⇒ 整批拒收，事务都不开。这是 D3 的第二道防线：
// 若有人把这段校验删掉（只信 ingest 闸门），这条会红。
func TestWriter_RejectsWholeBatchWithoutVisibleAt(t *testing.T) {
	db := &fakeWriteDB{tx: &fakeWriteTx{}}
	w, err := NewWriter(db, Options{MarketSchema: "market"})
	require.NoError(t, err)

	points := []Point{validPoint("600519.SH"), validPoint("000001.SZ")}
	points[1].VisibleAt = time.Time{}

	_, _, err = w.BulkInsert(context.Background(), "ohlcv_daily", points)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVisibleAtMissing)
	assert.Zero(t, db.beginCalls, "整批拒收时不得开启事务")
	assert.Zero(t, len(db.tx.execs), "整批拒收时不得执行任何 INSERT")
}

// 三集合门：B 集 / C 集的类型必须被显式拒绝（而不是「未知类型」或放行）。
func TestWriter_RejectsTablesOutsideASet(t *testing.T) {
	db := &fakeWriteDB{tx: &fakeWriteTx{}}
	w, err := NewWriter(db, Options{MarketSchema: "market"})
	require.NoError(t, err)

	for _, dataType := range []string{"daily_basic", "macro_series", "fundamentals"} {
		_, _, err := w.BulkInsert(context.Background(), dataType, []Point{validPoint("600519.SH")})
		require.Error(t, err, dataType)
		assert.ErrorIs(t, err, ErrWriteTableNotInASet, dataType)
	}
	assert.Zero(t, db.beginCalls)
}

func TestWriter_RejectsUnknownDataType(t *testing.T) {
	db := &fakeWriteDB{tx: &fakeWriteTx{}}
	w, err := NewWriter(db, Options{MarketSchema: "market"})
	require.NoError(t, err)

	_, _, err = w.BulkInsert(context.Background(), "no_such_type", []Point{validPoint("600519.SH")})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnmappedDataType)
}

// 同主键同内容：第二次写必须 no-op（插入被 ON CONFLICT 吞掉 + 比对一致）。
func TestWriter_SameContentRewriteIsNoOp(t *testing.T) {
	tx := &fakeWriteTx{affected: []int64{1, 0}, compares: []bool{true}}
	db := &fakeWriteDB{tx: tx}
	w, err := NewWriter(db, Options{MarketSchema: "market"})
	require.NoError(t, err)

	point := validPoint("600519.SH")
	persisted, skipped, err := w.BulkInsert(context.Background(), "ohlcv_daily", []Point{point})
	require.NoError(t, err)
	assert.Equal(t, 1, persisted)
	assert.Zero(t, skipped)

	persisted, skipped, err = w.BulkInsert(context.Background(), "ohlcv_daily", []Point{point})
	require.NoError(t, err)
	assert.Equal(t, 1, persisted, "同内容重写计为「已就位」")
	assert.Zero(t, skipped)
	assert.True(t, tx.committed)

	require.Len(t, tx.execs, 2)
	assert.Contains(t, tx.execs[0], "ON CONFLICT DO NOTHING")
}

// 同主键异内容：fail-loud + 整批回滚。
func TestWriter_DifferentContentFailsLoudAndRollsBack(t *testing.T) {
	tx := &fakeWriteTx{affected: []int64{1, 0}, compares: []bool{false}}
	db := &fakeWriteDB{tx: tx}
	w, err := NewWriter(db, Options{MarketSchema: "market"})
	require.NoError(t, err)

	first := validPoint("600519.SH")
	second := validPoint("000001.SZ")
	second.Data = map[string]interface{}{"open": 9.0, "high": 9.0, "low": 9.0, "close": 9.0, "volume": 1.0}

	// 第一条新写入，第二条与库里同主键但内容不同。
	_, _, err = w.BulkInsert(context.Background(), "ohlcv_daily", []Point{first, second})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContentConflict)
	assert.False(t, tx.committed, "冲突必须整批回滚，不许半批落库")
	assert.True(t, tx.rolledBack)
}

// 比对 SQL 的形状：排除易变列 ingest_time，DATE 列显式截断。
func TestWriter_CompareSQLExcludesVolatileColumn(t *testing.T) {
	spec := writeSpecs["ohlcv_daily"]
	sql := buildCompareSQL("market.ohlcv_daily_qfq", spec.Columns, spec.DateColumns)
	assert.NotContains(t, sql, "ingest_time")
	assert.Contains(t, sql, "trade_date IS NOT DISTINCT FROM ($2)::date")
	assert.Contains(t, sql, "visible_at IS NOT DISTINCT FROM")
}

func TestWriter_RejectsTypeMismatchAndMissingIngestTime(t *testing.T) {
	db := &fakeWriteDB{tx: &fakeWriteTx{}}
	w, err := NewWriter(db, Options{MarketSchema: "market"})
	require.NoError(t, err)

	bad := validPoint("600519.SH")
	bad.DataType = "capital_flow"
	_, _, err = w.BulkInsert(context.Background(), "ohlcv_daily", []Point{bad})
	require.ErrorIs(t, err, ErrInvalidPoint)

	noIngest := validPoint("600519.SH")
	noIngest.IngestTime = time.Time{}
	_, _, err = w.BulkInsert(context.Background(), "ohlcv_daily", []Point{noIngest})
	require.ErrorIs(t, err, ErrInvalidPoint)
}

func TestNewWriter_RejectsNilAndDottedSchema(t *testing.T) {
	_, err := NewWriter(nil, Options{})
	require.Error(t, err)

	_, err = NewWriter(&fakeWriteDB{tx: &fakeWriteTx{}}, Options{MarketSchema: "a.b"})
	require.Error(t, err)
}

// 所有 A 集 data_type 都必须有写面形状，且映射的表确实属 A 集：
// 新增声明忘了补 spec ⇒ 这条红（而不是作业时才报未知类型）。
func TestWriteSpecs_MatchASetAndConnectorTypes(t *testing.T) {
	for dataType, spec := range writeSpecs {
		class, ok := schema.GateClassFor(spec.Table)
		require.True(t, ok, "%s → %s 必须可分类", dataType, spec.Table)
		assert.Equal(t, schema.GateClassA, class, "%s 的写面表必须在 A 集", dataType)

		require.NotEmpty(t, spec.Columns)
		require.NotEmpty(t, spec.PK)
		assert.Contains(t, spec.Columns, "visible_at", "%s 必须落 visible_at", dataType)
		for _, pk := range spec.PK {
			assert.Contains(t, spec.Columns, pk, "%s 的主键列 %s 必须在写入列里", dataType, pk)
		}
		// 主键列不得出现在 numeric 里（数值化会让字符串主键写坏）。
		for _, pk := range spec.PK {
			assert.NotContains(t, spec.Numeric, pk)
		}
	}
	// 12 个 A 集 wire 类型：少一个 = 写面有洞；多一个 = 偷放 B/C 进门。
	assert.Len(t, writeSpecs, 12)
}
