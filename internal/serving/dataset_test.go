package serving

import (
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/require"
)

func TestResolveRequest_Validate(t *testing.T) {
	valid := baseRequest()

	cases := []struct {
		name    string
		mutate  func(*ResolveRequest)
		wantErr error
	}{
		{"universe", func(*ResolveRequest) {}, nil},
		{"index code", func(r *ResolveRequest) { r.Universe = ""; r.IndexCode = "000300.SH" }, nil},
		{"explicit symbols", func(r *ResolveRequest) { r.Universe = ""; r.Symbols = []string{"600000.SH"} }, nil},
		{"missing from", func(r *ResolveRequest) { r.From = time.Time{} }, ErrMissingParameter},
		{"missing to", func(r *ResolveRequest) { r.To = time.Time{} }, ErrMissingParameter},
		{"inverted window", func(r *ResolveRequest) { r.To = r.From.Add(-24 * time.Hour) }, ErrInvalidParameter},
		{"unknown frequency", func(r *ResolveRequest) { r.Frequency = "5m" }, ErrInvalidParameter},
		{"undeclared frequency", func(r *ResolveRequest) { r.Frequency = "" }, ErrInvalidParameter},
		{"missing as_of", func(r *ResolveRequest) { r.AsOf = time.Time{} }, ErrMissingParameter},
		{"missing adjustment", func(r *ResolveRequest) { r.Adjustment = " " }, ErrMissingParameter},
		{"unknown adjustment", func(r *ResolveRequest) { r.Adjustment = "adj" }, ErrInvalidParameter},
		{"universe and index code", func(r *ResolveRequest) { r.IndexCode = "000300.SH" }, ErrUniverseAmbiguous},
		{"universe and symbols", func(r *ResolveRequest) { r.Symbols = []string{"600000.SH"} }, ErrUniverseAmbiguous},
		{"no universe at all", func(r *ResolveRequest) { r.Universe = "" }, ErrMissingParameter},
		{"empty symbol entry", func(r *ResolveRequest) { r.Universe = ""; r.Symbols = []string{"600000.SH", "   "} }, ErrInvalidParameter},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)
			err := req.Validate()
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestNormalizeSymbols_TrimsDedupesAndSorts(t *testing.T) {
	got, err := normalizeSymbols([]string{" 600001.SH ", "600000.SH", "600001.SH"})
	require.NoError(t, err)
	require.Equal(t, []string{"600000.SH", "600001.SH"}, got)
}

func TestNormalizeSymbols_RejectsEmptyEntry(t *testing.T) {
	_, err := normalizeSymbols([]string{"600000.SH", "   "})
	require.ErrorIs(t, err, ErrInvalidParameter)
}

func TestNormalizeSymbols_EmptyInput(t *testing.T) {
	got, err := normalizeSymbols(nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestValidateCalendar(t *testing.T) {
	require.NoError(t, validateCalendar([]time.Time{utcDay(2026, 10, 22), utcDay(2026, 10, 23)}))

	require.ErrorIs(t, validateCalendar(nil), ErrEmptyCalendar)

	err := validateCalendar([]time.Time{utcDay(2026, 10, 23), utcDay(2026, 10, 22)})
	require.ErrorIs(t, err, ErrInvalidParameter)

	err = validateCalendar([]time.Time{utcDay(2026, 10, 22), utcDay(2026, 10, 22)})
	require.ErrorIs(t, err, ErrInvalidParameter, "重复日期破坏「保序轴」语义")

	err = validateCalendar([]time.Time{{}, utcDay(2026, 10, 23)})
	require.ErrorIs(t, err, ErrInvalidParameter, "零值日期会让 calendar_hash 失真")

	err = validateCalendar([]time.Time{utcDay(2026, 10, 22), {}})
	require.ErrorIs(t, err, ErrInvalidParameter)
}

func TestEncodeBars_IsDeterministicAndSensitive(t *testing.T) {
	a := dailyBar("600000.SH", utcDay(2026, 10, 22), 10)
	b := dailyBar("600001.SH", utcDay(2026, 10, 22), 20)

	first := map[string][]market.OHLCV{"600000.SH": {a}, "600001.SH": {b}}
	second := map[string][]market.OHLCV{"600001.SH": {b}, "600000.SH": {a}} // 插入顺序不同

	_, digest1, err := encodeBars(first)
	require.NoError(t, err)
	_, digest2, err := encodeBars(second)
	require.NoError(t, err)
	require.Equal(t, digest1, digest2, "map 键序不得影响指纹")

	changed := map[string][]market.OHLCV{"600000.SH": {a}, "600001.SH": {b}}
	changed["600000.SH"][0].Close += 0.01
	_, digest3, err := encodeBars(changed)
	require.NoError(t, err)
	require.NotEqual(t, digest1, digest3, "内容变了指纹必须变")
	require.Equal(t, 20.0, b.Close, "输入必须保持不被修改")
}

func TestMetadata_HashUsesContractsDefinition(t *testing.T) {
	calendar := []time.Time{utcDay(2026, 10, 22)}
	meta := Metadata{
		Universe:        []string{"600000.SH", "600001.SH"},
		UniverseHash:    contracts.UniverseHash([]string{"600000.SH", "600001.SH"}),
		WindowStart:     utcDay(2026, 10, 22),
		WindowEnd:       utcDay(2026, 10, 23),
		Frequency:       "1d",
		DataVersion:     "v1",
		CalendarHash:    contracts.CalendarHash(calendar),
		AdjustmentBasis: "qfq",
		AsOf:            utcDay(2026, 10, 24),
	}

	want := contracts.DatasetSnapshotInput{
		Universe:        meta.Universe,
		Window:          contracts.DateRange{Start: meta.WindowStart, End: meta.WindowEnd},
		Frequency:       meta.Frequency,
		DataVersion:     meta.DataVersion,
		Calendar:        calendar,
		AdjustmentBasis: meta.AdjustmentBasis,
	}.Hash()
	require.Equal(t, want, meta.snapshotInput(calendar).Hash(), "本包不得自实现 canonical 编码")

	// as_of 不在冻结六分量里：同六分量、不同 as_of ⇒ 同 hash（缓存层 fail-closed）。
	other := meta
	other.AsOf = meta.AsOf.Add(48 * time.Hour)
	require.Equal(t, meta.snapshotInput(calendar).Hash(), other.snapshotInput(calendar).Hash())
}

func TestCloneBars_DeepCopies(t *testing.T) {
	in := map[string][]market.OHLCV{"600000.SH": {dailyBar("600000.SH", utcDay(2026, 10, 22), 10)}}
	out := cloneBars(in)
	out["600000.SH"][0].Close = 99
	require.Equal(t, 10.0, in["600000.SH"][0].Close)
	require.Nil(t, cloneBars(nil))
}
