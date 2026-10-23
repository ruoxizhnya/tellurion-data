package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion-data/internal/normalize"
	"tellurion-data/internal/serving"
)

// fakeDB 记录 SQL/参数并按预置行返回，让 SQL 装配可在无库环境下验证。
type fakeDB struct {
	lastSQL  string
	lastArgs []any
	rows     [][]any
	err      error
}

func (f *fakeDB) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	f.lastSQL = sql
	f.lastArgs = args
	if f.err != nil {
		return nil, f.err
	}
	return &fakeRows{data: f.rows}, nil
}

type fakeRows struct {
	data [][]any
	pos  int
	cur  []any
	err  error
}

func (r *fakeRows) Next() bool {
	if r.pos >= len(r.data) {
		return false
	}
	r.cur = r.data[r.pos]
	r.pos++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if len(dest) != len(r.cur) {
		return fmt.Errorf("fake rows: %d dests for %d columns", len(dest), len(r.cur))
	}
	for i := range dest {
		if err := assign(dest[i], r.cur[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *fakeRows) Err() error { return r.err }
func (r *fakeRows) Close()     {}

// assign 只支持本包 port 真正用到的目标类型；新增列时必须在这里加一种，
// 否则测试会以「不支持的目标类型」失败——这正是我们要的提醒。
func assign(dest, src any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Ptr || target.IsNil() {
		return fmt.Errorf("fake rows: dest must be a non-nil pointer, got %T", dest)
	}
	if src == nil {
		// 可空列：目标必须是 **T（指针的指针），赋 nil 表示 SQL NULL。
		if target.Elem().Kind() != reflect.Ptr {
			return fmt.Errorf("fake rows: NULL into non-pointer dest %T", dest)
		}
		target.Elem().Set(reflect.Zero(target.Elem().Type()))
		return nil
	}

	elem := target.Elem()
	value := reflect.ValueOf(src)
	if value.Type().AssignableTo(elem.Type()) {
		elem.Set(value)
		return nil
	}
	// **time.Time 收 time.Time：取地址即可（模拟 pgx 对可空时间列的扫描）。
	if elem.Kind() == reflect.Ptr && value.Type().AssignableTo(elem.Type().Elem()) {
		p := reflect.New(elem.Type().Elem())
		p.Elem().Set(value)
		elem.Set(p)
		return nil
	}
	return fmt.Errorf("fake rows: cannot assign %T to %T", src, dest)
}

func newStore(t *testing.T, db Querier) *Store {
	t.Helper()
	s, err := New(db, Options{MarketSchema: "market"})
	require.NoError(t, err)
	return s
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ─── BarSource ────────────────────────────────────────────────

func TestReadBars_DailyUsesStoredVisibleAt(t *testing.T) {
	visible := time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC) // 北京 15:00
	db := &fakeDB{rows: [][]any{{
		"600519.SH", day(2026, 1, 15), 1.0, 2.0, 0.5, 1.5, 100.0, 150.0, 3, visible,
	}}}
	s := newStore(t, db)

	bars, err := s.ReadBars(context.Background(), []string{"600519.SH"}, day(2026, 1, 1), day(2026, 1, 31), market.Freq1d)
	require.NoError(t, err)
	require.Len(t, bars, 1)

	assert.Contains(t, db.lastSQL, "market.ohlcv_daily_qfq")
	assert.Contains(t, db.lastSQL, "visible_at", "可见时刻必须从列里取，不许现场按 trade_date 推")
	assert.Equal(t, market.Freq1d, bars[0].Frequency)
	assert.Equal(t, visible, bars[0].VisibleAt)
	assert.Equal(t, "600519.SH", bars[0].Symbol)
}

// 列值为 NULL ⇒ VisibleAt 零值，让 PIT 闸门整批拒收（fail-closed）。
// 这条测试是「不许顺手补一个看起来合理的时刻」的第二条腿。
func TestReadBars_MissingVisibleAtPropagatesToPITRejection(t *testing.T) {
	db := &fakeDB{rows: [][]any{{
		"600519.SH", day(2026, 1, 15), 1.0, 2.0, 0.5, 1.5, 100.0, 150.0, 3, nil,
	}}}
	s := newStore(t, db)

	bars, err := s.ReadBars(context.Background(), []string{"600519.SH"}, day(2026, 1, 1), day(2026, 1, 31), market.Freq1d)
	require.NoError(t, err)
	require.Len(t, bars, 1)
	assert.True(t, bars[0].VisibleAt.IsZero())

	_, err = normalize.VisibleAsOf(bars, day(2026, 2, 1))
	require.Error(t, err)
	assert.ErrorIs(t, err, normalize.ErrVisibleAtMissing)
}

func TestReadBars_MinuteMapsAmountToTurnover(t *testing.T) {
	ts := time.Date(2026, 1, 15, 1, 30, 0, 0, time.UTC)
	visible := ts.Add(time.Minute)
	db := &fakeDB{rows: [][]any{{"600519.SH", ts, 1.0, 2.0, 0.5, 1.5, 10.0, 1500.0, visible}}}
	s := newStore(t, db)

	bars, err := s.ReadBars(context.Background(), []string{"600519.SH"}, ts.Add(-time.Hour), ts.Add(time.Hour), market.Freq1m)
	require.NoError(t, err)
	require.Len(t, bars, 1)
	assert.Contains(t, db.lastSQL, "market.ohlcv_minute")
	assert.Equal(t, market.Freq1m, bars[0].Frequency)
	assert.Equal(t, 1500.0, bars[0].Turnover)
	assert.Equal(t, visible, bars[0].VisibleAt)
}

// 15m 必须由 aggregator 从 1m 聚合；port 直接拒绝，不许把 1m 贴成 15m。
func TestReadBars_Rejects15m(t *testing.T) {
	s := newStore(t, &fakeDB{})
	_, err := s.ReadBars(context.Background(), []string{"600519.SH"}, day(2026, 1, 1), day(2026, 1, 31), market.Freq15m)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedFrequency)
}

func TestReadBars_EmptySymbolsSkipsQuery(t *testing.T) {
	db := &fakeDB{}
	s := newStore(t, db)
	bars, err := s.ReadBars(context.Background(), nil, day(2026, 1, 1), day(2026, 1, 31), market.Freq1d)
	require.NoError(t, err)
	assert.Empty(t, bars)
	assert.Empty(t, db.lastSQL, "空票池不该发查询")
}

// ─── TradingCalendar ─────────────────────────────────────────

func TestTradingDays_FiltersAndNormalisesToUTCDate(t *testing.T) {
	db := &fakeDB{rows: [][]any{
		{time.Date(2026, 1, 5, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))},
		{day(2026, 1, 6)},
	}}
	s := newStore(t, db)

	days, err := s.TradingDays(context.Background(), day(2026, 1, 1), day(2026, 1, 31))
	require.NoError(t, err)
	require.Len(t, days, 2)
	assert.Contains(t, db.lastSQL, "is_trading_day", "休市日不能进日历轴")
	assert.Equal(t, day(2026, 1, 5), days[0])
	assert.Equal(t, day(2026, 1, 6), days[1])
}

// ─── DataVersioner ───────────────────────────────────────────

func TestDataVersion_IsWatermarkPlusCount(t *testing.T) {
	db := &fakeDB{rows: [][]any{{"2026-10-23", int64(3926510)}}}
	s := newStore(t, db)

	got, err := s.DataVersion(context.Background(), market.Freq1d)
	require.NoError(t, err)
	assert.Equal(t, "1d:2026-10-23+n=3926510", got)
	assert.Contains(t, db.lastSQL, "market.ohlcv_daily_qfq")
}

// 15m 不物化 ⇒ 水位取 1m；两处映射若各自写一份，这里会先红。
func TestDataVersion_15mUsesMinuteWatermark(t *testing.T) {
	db := &fakeDB{rows: [][]any{{"2026-10-23 15:00:00+08", int64(9)}}}
	s := newStore(t, db)

	got, err := s.DataVersion(context.Background(), market.Freq15m)
	require.NoError(t, err)
	assert.Equal(t, "1m:2026-10-23 15:00:00+08+n=9", got)
	assert.Contains(t, db.lastSQL, "market.ohlcv_minute")
}

// 空表返回空串，由 aggregator 用 ErrEmptyDataVersion 拒收——不编 `1d:0`。
func TestDataVersion_EmptyTableIsEmptyNotFake(t *testing.T) {
	s := newStore(t, &fakeDB{rows: [][]any{{"", int64(0)}}})
	got, err := s.DataVersion(context.Background(), market.Freq1d)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDataVersion_RejectsUnknownFrequency(t *testing.T) {
	s := newStore(t, &fakeDB{})
	_, err := s.DataVersion(context.Background(), market.Frequency("7m"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedFrequency)
}

// ─── UniverseResolver ────────────────────────────────────────

func TestResolveUniverse_ExplicitSymbolsAreNormalised(t *testing.T) {
	db := &fakeDB{}
	s := newStore(t, db)

	got, err := s.ResolveUniverse(context.Background(), serving.UniverseRequest{
		Symbols: []string{" 600519.SH ", "000001.SZ", "600519.SH", ""},
		AsOf:    day(2026, 1, 15),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"000001.SZ", "600519.SH"}, got)
	assert.Empty(t, db.lastSQL)
}

// 别名展开 + as-of 语义必须落到 SQL：这是幸存者偏差的唯一防线。
func TestResolveUniverse_AliasExpandsWithAsOfFilter(t *testing.T) {
	db := &fakeDB{rows: [][]any{{"600519.SH"}, {"000001.SZ"}}}
	s := newStore(t, db)

	got, err := s.ResolveUniverse(context.Background(), serving.UniverseRequest{
		Name: "csi300", AsOf: day(2026, 1, 15),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"000001.SZ", "600519.SH"}, got)
	assert.Contains(t, db.lastSQL, "market.index_constituents")
	assert.Contains(t, db.lastSQL, "in_date <= $2")
	assert.Contains(t, db.lastSQL, "out_date IS NULL OR out_date > $2",
		"as-of 过滤：退市/调出的成分不能在 as_of 之后继续参与")
	assert.Equal(t, "000300.SH", db.lastArgs[0], "csi300 必须展开成库里的 000300.SH")
}

func TestResolveUniverse_UnknownNameFailsClosed(t *testing.T) {
	s := newStore(t, &fakeDB{})
	_, err := s.ResolveUniverse(context.Background(), serving.UniverseRequest{
		Name: "csi1000", AsOf: day(2026, 1, 15),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownUniverse)
}

// 成分是时变的：没有 as_of 就不解析，不拿「今天」顶替。
func TestResolveUniverse_RequiresAsOf(t *testing.T) {
	s := newStore(t, &fakeDB{})
	_, err := s.ResolveUniverse(context.Background(), serving.UniverseRequest{Name: "csi300"})
	require.Error(t, err)
	assert.ErrorIs(t, err, serving.ErrMissingParameter)
}

// ─── 装配 ───────────────────────────────────────────────────

func TestNew_DefaultSchemaIsPublic(t *testing.T) {
	s, err := New(&fakeDB{}, Options{})
	require.NoError(t, err)
	assert.Equal(t, "public", s.prefix)
}

func TestNew_RejectsNilQuerierAndDottedSchema(t *testing.T) {
	_, err := New(nil, Options{})
	require.Error(t, err)

	_, err = New(&fakeDB{}, Options{MarketSchema: "market.x"})
	require.Error(t, err)
}

// 请求级失败必须能被 HTTP 面识别成 400（不是 500）：两个 port 哨兵都把
// serving.ErrInvalidParameter 包在里面。这条测试钉住分诊语义。
func TestPortErrors_MapToInvalidParameter(t *testing.T) {
	assert.ErrorIs(t, ErrUnsupportedFrequency, serving.ErrInvalidParameter)
	assert.ErrorIs(t, ErrUnknownUniverse, serving.ErrInvalidParameter)
}

// 表前缀是过渡开关：空串 → public（今天），"market" → 目标。
func TestTablePrefix_SwitchesBetweenTargetAndToday(t *testing.T) {
	today, err := New(&fakeDB{}, Options{MarketSchema: ""})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(today.table("ohlcv_daily_qfq"), "public."))

	target, err := New(&fakeDB{}, Options{MarketSchema: "market"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.table("ohlcv_daily_qfq"), "market."))
}
