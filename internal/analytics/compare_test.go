package analytics

import (
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// withoutDate returns a copy of s lacking the bar on date.
func withoutDate(s *market.Series, date time.Time) *market.Series {
	out := &market.Series{Meta: s.Meta}
	for _, b := range s.Bars {
		if !b.Date.Equal(date) {
			out.Bars = append(out.Bars, b)
		}
	}
	return out
}

// adjSeries builds bars on dates whose AdjClose is adj(i) and whose Close
// is twice that, so a test can tell which field was read.
func adjSeries(symbol string, dates []time.Time, adj func(i int) float64) *market.Series {
	bars := make([]market.Bar, len(dates))
	for i, d := range dates {
		bars[i] = market.Bar{Date: d, Close: 2 * adj(i), AdjClose: adj(i)}
	}
	return &market.Series{Meta: market.Meta{Symbol: symbol}, Bars: bars}
}

// adjCloseOn returns s's AdjClose on date, failing the test when absent.
func adjCloseOn(t *testing.T, s *market.Series, date time.Time) float64 {
	t.Helper()
	idx, ok := s.IndexOn(date)
	if !ok || !s.Bars[idx].Date.Equal(date) {
		t.Fatalf("%s has no bar on %s", s.Meta.Symbol, date.Format(market.DateLayout))
	}
	return s.Bars[idx].AdjClose
}

func TestCommonRange(t *testing.T) {
	jan := weekdaysUntil(day(2024, time.January, 1), day(2024, time.January, 31))
	a := adjSeries("A", jan, func(i int) float64 { return float64(i + 1) })
	b := withoutDate(adjSeries("B", jan, func(i int) float64 { return 100 + float64(i) }), day(2024, time.January, 10))
	c := adjSeries("C", weekdaysUntil(day(2024, time.January, 5), day(2024, time.January, 31)), func(i int) float64 { return 50 + float64(i) })
	series := []*market.Series{a, b, c}

	tests := []struct {
		name       string
		start, end time.Time
		wantDates  []time.Time
	}{
		{
			name:  "bounded intersection skips the gap",
			start: day(2024, time.January, 3),
			end:   day(2024, time.January, 19),
			wantDates: []time.Time{
				day(2024, time.January, 5), day(2024, time.January, 8), day(2024, time.January, 9),
				day(2024, time.January, 11), day(2024, time.January, 12), day(2024, time.January, 15),
				day(2024, time.January, 16), day(2024, time.January, 17), day(2024, time.January, 18),
				day(2024, time.January, 19),
			},
		},
		{
			name: "zero bounds mean everything",
			wantDates: func() []time.Time {
				var out []time.Time
				for _, d := range weekdaysUntil(day(2024, time.January, 5), day(2024, time.January, 31)) {
					if !d.Equal(day(2024, time.January, 10)) {
						out = append(out, d)
					}
				}
				return out
			}(),
		},
		{
			name:      "open start",
			end:       day(2024, time.January, 8),
			wantDates: []time.Time{day(2024, time.January, 5), day(2024, time.January, 8)},
		},
		{
			name:      "open end",
			start:     day(2024, time.January, 30),
			wantDates: []time.Time{day(2024, time.January, 30), day(2024, time.January, 31)},
		},
		{
			name:      "range before the overlap",
			start:     day(2024, time.January, 1),
			end:       day(2024, time.January, 4),
			wantDates: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dates, aligned := CommonRange(series, tt.start, tt.end)
			if len(dates) != len(tt.wantDates) {
				t.Fatalf("dates = %v, want %v", dates, tt.wantDates)
			}
			for i, d := range dates {
				if !d.Equal(tt.wantDates[i]) {
					t.Fatalf("dates[%d] = %v, want %v", i, d, tt.wantDates[i])
				}
			}
			if len(aligned) != len(series) {
				t.Fatalf("aligned has %d rows, want %d", len(aligned), len(series))
			}
			for i, row := range aligned {
				if len(row) != len(dates) {
					t.Fatalf("aligned[%d] has %d values, want %d", i, len(row), len(dates))
				}
				for d, v := range row {
					if want := adjCloseOn(t, series[i], dates[d]); v != want {
						t.Fatalf("aligned[%d][%d] = %v, want AdjClose %v", i, d, v, want)
					}
				}
			}
		})
	}

	t.Run("nil series empties the intersection", func(t *testing.T) {
		dates, aligned := CommonRange([]*market.Series{a, nil}, time.Time{}, time.Time{})
		if len(dates) != 0 || len(aligned) != 2 || len(aligned[0]) != 0 || len(aligned[1]) != 0 {
			t.Fatalf("dates = %v, aligned = %v", dates, aligned)
		}
	})
	t.Run("no series", func(t *testing.T) {
		dates, aligned := CommonRange(nil, time.Time{}, time.Time{})
		if dates != nil || aligned != nil {
			t.Fatalf("got %v %v, want nil nil", dates, aligned)
		}
	})
}

// walkPrices returns n prices of a seeded geometric random walk plus the
// cumulative log returns that produced them.
func walkPrices(n int, seed uint64, drift, vol float64) (prices, cum []float64) {
	rng := rand.New(rand.NewPCG(seed, 1))
	prices, cum = make([]float64, n), make([]float64, n)
	for i := 1; i < n; i++ {
		cum[i] = cum[i-1] + drift + vol*rng.NormFloat64()
	}
	for i := range prices {
		prices[i] = 100 * math.Exp(cum[i])
	}
	return prices, cum
}

// scaledWalk returns prices whose log returns are factor times those
// behind cum, plus shift per day.
func scaledWalk(cum []float64, factor, shift float64) []float64 {
	out := make([]float64, len(cum))
	for i, c := range cum {
		out[i] = 100 * math.Exp(factor*c+shift*float64(i))
	}
	return out
}

func TestCorrelationMatrix(t *testing.T) {
	base, cum := walkPrices(300, 1, 0.0005, 0.01)
	other, _ := walkPrices(300, 2, 0.0005, 0.01)
	aligned := [][]float64{base, append([]float64(nil), base...), scaledWalk(cum, -1, 0), other}

	m := CorrelationMatrix(aligned)
	if len(m) != 4 {
		t.Fatalf("matrix has %d rows, want 4", len(m))
	}
	for i := range m {
		if len(m[i]) != 4 {
			t.Fatalf("row %d has %d entries, want 4", i, len(m[i]))
		}
		if m[i][i] != 1 {
			t.Fatalf("m[%d][%d] = %v, want 1", i, i, m[i][i])
		}
		for j := range m[i] {
			if m[i][j] != m[j][i] {
				t.Fatalf("m[%d][%d] = %v but m[%d][%d] = %v", i, j, m[i][j], j, i, m[j][i])
			}
			if math.Abs(m[i][j]) > 1+1e-12 {
				t.Fatalf("m[%d][%d] = %v outside [-1, 1]", i, j, m[i][j])
			}
		}
	}
	if !approx(m[0][1], 1, 1e-9) {
		t.Fatalf("identical series: %v, want 1", m[0][1])
	}
	if !approx(m[0][2], -1, 1e-9) {
		t.Fatalf("negated returns: %v, want -1", m[0][2])
	}
	if math.Abs(m[0][3]) > 0.3 {
		t.Fatalf("independent walks: %v, want near 0", m[0][3])
	}

	t.Run("undefined entries are NaN", func(t *testing.T) {
		flat := []float64{5, 5, 5, 5}
		m := CorrelationMatrix([][]float64{flat, base[:4]})
		if !math.IsNaN(m[0][1]) || !math.IsNaN(m[1][0]) || m[0][0] != 1 || m[1][1] != 1 {
			t.Fatalf("constant row: %v", m)
		}
		m = CorrelationMatrix([][]float64{base[:2], other[:2]})
		if !math.IsNaN(m[0][1]) {
			t.Fatalf("one return: %v, want NaN", m[0][1])
		}
	})
	t.Run("empty", func(t *testing.T) {
		if m := CorrelationMatrix(nil); len(m) != 0 {
			t.Fatalf("got %v, want empty", m)
		}
	})
}

func TestBeta(t *testing.T) {
	bench, cum := walkPrices(500, 3, 0.0003, 0.012)
	tests := []struct {
		name                string
		asset, benchmark    []float64
		wantBeta, wantAlpha float64
		wantNaN             bool
	}{
		{name: "two times levered", asset: scaledWalk(cum, 2, 0), benchmark: bench, wantBeta: 2, wantAlpha: 0},
		{name: "inverse", asset: scaledWalk(cum, -1, 0), benchmark: bench, wantBeta: -1, wantAlpha: 0},
		{name: "benchmark plus 10 bp a day", asset: scaledWalk(cum, 1, 0.001), benchmark: bench, wantBeta: 1, wantAlpha: 0.001 * 252},
		{name: "benchmark against itself", asset: bench, benchmark: bench, wantBeta: 1, wantAlpha: 0},
		{name: "constant benchmark", asset: bench[:10], benchmark: []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, wantNaN: true},
		{name: "mismatched lengths", asset: bench[:10], benchmark: bench[:9], wantNaN: true},
		{name: "one return", asset: bench[:2], benchmark: bench[:2], wantNaN: true},
		{name: "empty", asset: nil, benchmark: nil, wantNaN: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beta, alpha := Beta(tt.asset, tt.benchmark)
			if tt.wantNaN {
				if !math.IsNaN(beta) || !math.IsNaN(alpha) {
					t.Fatalf("Beta = (%v, %v), want NaN NaN", beta, alpha)
				}
				return
			}
			if !approx(beta, tt.wantBeta, 1e-9) || !approx(alpha, tt.wantAlpha, 1e-9) {
				t.Fatalf("Beta = (%v, %v), want (%v, %v)", beta, alpha, tt.wantBeta, tt.wantAlpha)
			}
		})
	}
}

// compareFixture builds a benchmark walk, a 2x levered version and an
// inverse version that lacks one date, all on the same weekdays.
func compareFixture() (spy, lev, inv *market.Series) {
	dates := weekdays(day(2021, time.January, 4), 400)
	prices, cum := walkPrices(400, 7, 0.0003, 0.01)
	levered, inverse := scaledWalk(cum, 2, 0), scaledWalk(cum, -1, 0)
	spy = seriesFrom("SPY", dates, func(i int, _ time.Time) float64 { return prices[i] })
	lev = seriesFrom("LEV", dates, func(i int, _ time.Time) float64 { return levered[i] })
	inv = withoutDate(seriesFrom("INV", dates, func(i int, _ time.Time) float64 { return inverse[i] }), dates[100])
	return spy, lev, inv
}

func TestCompare(t *testing.T) {
	spy, lev, inv := compareFixture()
	series := []*market.Series{spy, lev, inv}

	c, err := Compare(series, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if got := strings.Join(c.Symbols, ","); got != "SPY,LEV,INV" {
		t.Fatalf("Symbols = %q", got)
	}
	if !c.From.Equal(spy.Bars[0].Date) || !c.To.Equal(spy.Bars[399].Date) || c.Bars != 399 {
		t.Fatalf("range = %v..%v over %d bars, want %v..%v over 399", c.From, c.To, c.Bars, spy.Bars[0].Date, spy.Bars[399].Date)
	}
	if len(c.Summaries) != 3 {
		t.Fatalf("Summaries = %d, want 3", len(c.Summaries))
	}
	for i, sum := range c.Summaries {
		if sum.Symbol != c.Symbols[i] || !sum.AsOf.Equal(c.To) {
			t.Fatalf("Summaries[%d] = %s as of %v, want %s as of %v", i, sum.Symbol, sum.AsOf, c.Symbols[i], c.To)
		}
	}
	wantCorr := [][]float64{{1, 1, -1}, {1, 1, -1}, {-1, -1, 1}}
	for i := range wantCorr {
		if !floatsMatch(c.Correlation[i], wantCorr[i], 1e-9) {
			t.Fatalf("Correlation[%d] = %v, want %v", i, c.Correlation[i], wantCorr[i])
		}
	}
	if !floatsMatch(c.BetaToFirst, []float64{1, 2, -1}, 1e-9) {
		t.Fatalf("BetaToFirst = %v, want [1 2 -1]", c.BetaToFirst)
	}
	if !floatsMatch(c.AlphaToFirst, []float64{0, 0, 0}, 1e-9) {
		t.Fatalf("AlphaToFirst = %v, want zeros", c.AlphaToFirst)
	}

	t.Run("bounded range", func(t *testing.T) {
		start, end := spy.Bars[50].Date, spy.Bars[150].Date
		c, err := Compare(series, start, end)
		if err != nil {
			t.Fatalf("Compare: %v", err)
		}
		if !c.From.Equal(start) || !c.To.Equal(end) || c.Bars != 100 { // 101 dates minus the INV gap
			t.Fatalf("range = %v..%v over %d bars, want %v..%v over 100", c.From, c.To, c.Bars, start, end)
		}
		for _, sum := range c.Summaries {
			if !sum.AsOf.Equal(end) {
				t.Fatalf("Summary as of %v, want %v", sum.AsOf, end)
			}
		}
		if !floatsMatch(c.BetaToFirst, []float64{1, 2, -1}, 1e-9) {
			t.Fatalf("BetaToFirst = %v, want [1 2 -1]", c.BetaToFirst)
		}
	})
	t.Run("single series", func(t *testing.T) {
		c, err := Compare([]*market.Series{spy}, time.Time{}, time.Time{})
		if err != nil {
			t.Fatalf("Compare: %v", err)
		}
		if len(c.Correlation) != 1 || c.Correlation[0][0] != 1 || c.BetaToFirst[0] != 1 || c.AlphaToFirst[0] != 0 {
			t.Fatalf("Correlation = %v, BetaToFirst = %v, AlphaToFirst = %v", c.Correlation, c.BetaToFirst, c.AlphaToFirst)
		}
	})
	t.Run("undefined statistics become zero", func(t *testing.T) {
		flat := seriesFrom("FLAT", weekdays(day(2021, time.January, 4), 400), func(int, time.Time) float64 { return 50 })
		c, err := Compare([]*market.Series{flat, spy}, time.Time{}, time.Time{})
		if err != nil {
			t.Fatalf("Compare: %v", err)
		}
		if c.Correlation[0][1] != 0 || c.BetaToFirst[1] != 0 || c.AlphaToFirst[1] != 0 {
			t.Fatalf("Correlation = %v, BetaToFirst = %v, AlphaToFirst = %v", c.Correlation, c.BetaToFirst, c.AlphaToFirst)
		}
	})
}

func TestCompareErrors(t *testing.T) {
	spy, _, _ := compareFixture()
	later := seriesFrom("LATE", weekdays(spy.Bars[399].Date.AddDate(0, 0, 1), 30), func(int, time.Time) float64 { return 10 })
	tests := []struct {
		name       string
		series     []*market.Series
		start, end time.Time
		want       error
		wantText   string
	}{
		{name: "no series", series: nil, want: ErrInvalidInput, wantText: "at least one series"},
		{name: "nil series", series: []*market.Series{spy, nil}, want: ErrInvalidInput, wantText: "nil series"},
		{name: "invalid series", series: []*market.Series{{Meta: market.Meta{Symbol: "BAD"}, Bars: []market.Bar{{Date: day(2021, time.January, 4)}}}}, want: ErrInvalidInput, wantText: "non-positive price"},
		{name: "no overlap", series: []*market.Series{spy, later}, want: ErrInsufficientHistory, wantText: "0 common trading days, need at least 2"},
		{name: "one common day", series: []*market.Series{spy}, start: spy.Bars[10].Date, end: spy.Bars[10].Date, want: ErrInsufficientHistory, wantText: "1 common trading days between"},
		{name: "range after the data", series: []*market.Series{spy}, start: day(2030, time.January, 1), want: ErrInsufficientHistory, wantText: "from 2030-01-01"},
		{name: "range before the data", series: []*market.Series{spy}, end: day(2000, time.January, 1), want: ErrInsufficientHistory, wantText: "up to 2000-01-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Compare(tt.series, tt.start, tt.end)
			if c != nil || !errors.Is(err, tt.want) {
				t.Fatalf("Compare = %v, %v; want error %v", c, err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error %q lacks %q", err, tt.wantText)
			}
		})
	}
}
