package analytics

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestScoreSeriesLongHistory(t *testing.T) {
	s := growthSeries("UP", 300, 0.001)
	s.Bars[290].Dividend = 1.5 // inside the trailing year
	sc, err := ScoreSeries(s, time.Time{})
	if err != nil {
		t.Fatalf("ScoreSeries: %v", err)
	}
	sum, _ := Summarize(s, time.Time{})
	tr, _ := AnalyzeTrend(s, time.Time{})

	if sc.Symbol != "UP" || sc.Bars != 300 || sc.Trend != StateUptrend {
		t.Fatalf("Symbol/Bars/Trend = %q/%d/%q", sc.Symbol, sc.Bars, sc.Trend)
	}
	// Price 21 bars ago over price 252 bars ago, on a constant 0.1% daily log return.
	if want := math.Exp(0.001*(momentumBars-momentumSkipBars)) - 1; !approx(sc.Momentum12_1, want, 1e-12) {
		t.Fatalf("Momentum12_1 = %v, want %v", sc.Momentum12_1, want)
	}
	checks := []struct {
		name      string
		got, want float64
	}{
		{"Return1Y", sc.Return1Y, windowByLabel(t, sum, "1y").TotalReturn},
		{"Return3M", sc.Return3M, windowByLabel(t, sum, "3m").TotalReturn},
		{"Volatility1Y", sc.Volatility1Y, sum.Volatility1Y},
		{"MaxDrawdown1Y", sc.MaxDrawdown1Y, sum.MaxDrawdown1Y},
		{"DividendYield", sc.DividendYield, 1.5 / s.Bars[299].Close},
		{"PctVsSMA200", sc.PctVsSMA200, tr.PctVsSMA200},
	}
	for _, c := range checks {
		if math.IsNaN(c.got) || !approx(c.got, c.want, 1e-12) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if sc.Return1Y <= 0 || sc.PctVsSMA200 <= 0 || sc.DividendYield <= 0 {
		t.Fatalf("a rising series with a dividend must score positive: %+v", sc)
	}
}

func TestScoreSeriesShortHistory(t *testing.T) {
	tests := []struct {
		name    string
		bars    int
		wantNaN []string // fields that must be NaN
		wantNum []string // fields that must be numbers
		trend   string
	}{
		{
			name:    "100 bars",
			bars:    100,
			wantNaN: []string{RankByMomentum12_1, RankByReturn1Y, RankByVolatility1Y, RankByMaxDrawdown1Y, RankByDividendYield, RankByPctVsSMA200},
			wantNum: []string{RankByReturn3M},
			trend:   StateInsufficientHistory,
		},
		{
			// 220 weekdays span about 308 calendar days: enough for the
			// 200-day average, not for the trailing year or 12-1 momentum.
			name:    "220 bars",
			bars:    220,
			wantNaN: []string{RankByMomentum12_1, RankByReturn1Y, RankByVolatility1Y, RankByMaxDrawdown1Y, RankByDividendYield},
			wantNum: []string{RankByReturn3M, RankByPctVsSMA200},
			trend:   StateUptrend,
		},
		{
			name:    "300 bars",
			bars:    300,
			wantNum: RankKeys,
			trend:   StateUptrend,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, err := ScoreSeries(growthSeries("S", tt.bars, 0.001), time.Time{})
			if err != nil {
				t.Fatalf("ScoreSeries: %v", err)
			}
			if sc.Bars != tt.bars || sc.Trend != tt.trend {
				t.Fatalf("Bars/Trend = %d/%q, want %d/%q", sc.Bars, sc.Trend, tt.bars, tt.trend)
			}
			for _, key := range tt.wantNaN {
				if v, _ := sc.Value(key); !math.IsNaN(v) {
					t.Errorf("%s = %v, want NaN", key, v)
				}
			}
			for _, key := range tt.wantNum {
				if v, _ := sc.Value(key); math.IsNaN(v) {
					t.Errorf("%s is NaN, want a number", key)
				}
			}
		})
	}
}

func TestScoreSeriesErrors(t *testing.T) {
	if _, err := ScoreSeries(nil, time.Time{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil series: err = %v, want ErrInvalidInput", err)
	}
	s := growthSeries("S", 10, 0.001)
	if _, err := ScoreSeries(s, day(2019, time.June, 1)); !errors.Is(err, ErrInsufficientHistory) {
		t.Fatalf("before first bar: err = %v, want ErrInsufficientHistory", err)
	}
	sc, err := ScoreSeries(s, s.Bars[4].Date)
	if err != nil || sc.Bars != 5 {
		t.Fatalf("as-of anchoring: Bars = %d, err = %v; want 5, nil", sc.Bars, err)
	}
}

func TestScoreValue(t *testing.T) {
	sc := Score{Momentum12_1: 1, Return1Y: 2, Return3M: 3, Volatility1Y: 4, MaxDrawdown1Y: 5, DividendYield: 6, PctVsSMA200: 7}
	for i, key := range RankKeys {
		v, err := sc.Value(key)
		if err != nil || v != float64(i+1) {
			t.Fatalf("Value(%q) = %v, %v; want %d", key, v, err, i+1)
		}
	}
	_, err := sc.Value("sharpe")
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "momentum_12_1, return_1y") {
		t.Fatalf("unknown key: %v", err)
	}
}

// symbolsOf lists the symbols of scores in order.
func symbolsOf(scores []Score) []string {
	out := make([]string, len(scores))
	for i, sc := range scores {
		out[i] = sc.Symbol
	}
	return out
}

func TestRankScores(t *testing.T) {
	nan := math.NaN()
	scores := []Score{
		{Symbol: "A", Return1Y: 0.10, Volatility1Y: 0.20, MaxDrawdown1Y: 0.3},
		{Symbol: "B", Return1Y: nan, Volatility1Y: 0.10, MaxDrawdown1Y: nan},
		{Symbol: "C", Return1Y: 0.30, Volatility1Y: 0.20, MaxDrawdown1Y: 0.1},
		{Symbol: "D", Return1Y: -0.05, Volatility1Y: nan, MaxDrawdown1Y: nan},
		{Symbol: "E", Return1Y: 0.10, Volatility1Y: 0.15, MaxDrawdown1Y: 0.2},
	}
	tests := []struct {
		name       string
		by         string
		descending bool
		want       []string
	}{
		{name: "return descending, tie keeps input order, NaN last", by: RankByReturn1Y, descending: true, want: []string{"C", "A", "E", "D", "B"}},
		{name: "return ascending, NaN still last", by: RankByReturn1Y, want: []string{"D", "A", "E", "C", "B"}},
		{name: "volatility ascending", by: RankByVolatility1Y, want: []string{"B", "E", "A", "C", "D"}},
		{name: "volatility descending", by: RankByVolatility1Y, descending: true, want: []string{"A", "C", "E", "B", "D"}},
		{name: "two NaNs keep input order", by: RankByMaxDrawdown1Y, want: []string{"C", "E", "A", "B", "D"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := symbolsOf(scores)
			got, err := RankScores(scores, tt.by, tt.descending)
			if err != nil {
				t.Fatalf("RankScores: %v", err)
			}
			if !slices.Equal(symbolsOf(got), tt.want) {
				t.Fatalf("order = %v, want %v", symbolsOf(got), tt.want)
			}
			if !slices.Equal(symbolsOf(scores), before) {
				t.Fatalf("input was reordered to %v", symbolsOf(scores))
			}
		})
	}

	t.Run("all NaN keeps input order", func(t *testing.T) {
		got, err := RankScores(scores, RankByMomentum12_1, true)
		if err != nil {
			t.Fatalf("RankScores: %v", err)
		}
		for i := range got {
			if got[i].Momentum12_1 != 0 { // zero value, not NaN, so ties everywhere
				t.Fatalf("fixture changed: %+v", got[i])
			}
		}
		if !slices.Equal(symbolsOf(got), symbolsOf(scores)) {
			t.Fatalf("order = %v, want input order", symbolsOf(got))
		}
	})
	t.Run("empty input", func(t *testing.T) {
		got, err := RankScores(nil, RankByReturn1Y, true)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("invalid key", func(t *testing.T) {
		got, err := RankScores(scores, "alpha", true)
		if got != nil || !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("got %v, %v; want nil, ErrInvalidInput", got, err)
		}
	})
}

func TestRankScoresFromSeries(t *testing.T) {
	// Three series scored and ranked end to end: the steeper growth wins
	// on 1-year return and the short one lands last with NaN.
	series := []*market.Series{
		growthSeries("SLOW", 300, 0.0005),
		growthSeries("FAST", 300, 0.0015),
		growthSeries("NEW", 60, 0.0030),
	}
	scores := make([]Score, 0, len(series))
	for _, s := range series {
		sc, err := ScoreSeries(s, time.Time{})
		if err != nil {
			t.Fatalf("ScoreSeries(%s): %v", s.Meta.Symbol, err)
		}
		scores = append(scores, sc)
	}
	ranked, err := RankScores(scores, RankByReturn1Y, true)
	if err != nil {
		t.Fatalf("RankScores: %v", err)
	}
	if want := []string{"FAST", "SLOW", "NEW"}; !slices.Equal(symbolsOf(ranked), want) {
		t.Fatalf("order = %v, want %v", symbolsOf(ranked), want)
	}
	if !math.IsNaN(ranked[2].Return1Y) || ranked[2].Trend != StateInsufficientHistory {
		t.Fatalf("NEW must be NaN with insufficient history: %+v", ranked[2])
	}
}
