package analytics

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// Rank keys accepted by RankScores and Score.Value.
const (
	RankByMomentum12_1  = "momentum_12_1"
	RankByReturn1Y      = "return_1y"
	RankByReturn3M      = "return_3m"
	RankByVolatility1Y  = "volatility_1y"
	RankByMaxDrawdown1Y = "max_drawdown_1y"
	RankByDividendYield = "dividend_yield"
	RankByPctVsSMA200   = "pct_vs_sma_200"
)

// RankKeys lists every rank key in the order the Score fields are
// declared; tool layers can expose it as an enum.
var RankKeys = []string{
	RankByMomentum12_1,
	RankByReturn1Y,
	RankByReturn3M,
	RankByVolatility1Y,
	RankByMaxDrawdown1Y,
	RankByDividendYield,
	RankByPctVsSMA200,
}

// Score is the screening row of one series as of a date. Every float
// field is a fraction except PctVsSMA200, which is in percent, and is NaN
// when the series is too short for it, so that RankScores can push the
// row to the end. Callers that encode JSON must map NaN themselves.
type Score struct {
	// Symbol is the series symbol.
	Symbol string
	// Momentum12_1 is Trend.Momentum12_1; NaN with fewer than 253 bars.
	Momentum12_1 float64
	// Return1Y and Return3M are the trailing total returns on AdjClose
	// (Summary windows "1y" and "3m"); NaN when the window is unavailable.
	Return1Y, Return3M float64
	// Volatility1Y, MaxDrawdown1Y and DividendYield are the trailing-year
	// figures of Summary; NaN unless the series covers the trailing year
	// (the "1y" window is available), because a partial year would make
	// a young fund look calmer, shallower or less generous than it is.
	Volatility1Y, MaxDrawdown1Y, DividendYield float64
	// PctVsSMA200 is (Close / SMA200 - 1) * 100; NaN with fewer than 200
	// bars.
	PctVsSMA200 float64
	// Trend is Trend.State.
	Trend string
	// Bars is the number of bars up to and including the anchor date.
	Bars int
}

// Value returns the field selected by a rank key, or an error wrapping
// ErrInvalidInput for an unknown key.
func (sc Score) Value(by string) (float64, error) {
	switch by {
	case RankByMomentum12_1:
		return sc.Momentum12_1, nil
	case RankByReturn1Y:
		return sc.Return1Y, nil
	case RankByReturn3M:
		return sc.Return3M, nil
	case RankByVolatility1Y:
		return sc.Volatility1Y, nil
	case RankByMaxDrawdown1Y:
		return sc.MaxDrawdown1Y, nil
	case RankByDividendYield:
		return sc.DividendYield, nil
	case RankByPctVsSMA200:
		return sc.PctVsSMA200, nil
	default:
		return 0, fmt.Errorf("%w: unknown rank key %q, want one of %s",
			ErrInvalidInput, by, strings.Join(RankKeys, ", "))
	}
}

// ScoreSeries computes the Score of s as of asOf, using the last bar on or
// before asOf (zero means the last bar), by reusing Summarize and
// AnalyzeTrend. Errors mirror Summarize.
func ScoreSeries(s *market.Series, asOf time.Time) (Score, error) {
	sum, err := Summarize(s, asOf)
	if err != nil {
		return Score{}, err
	}
	tr, err := AnalyzeTrend(s, asOf)
	if err != nil {
		return Score{}, err
	}
	yearCovered := windowReturn(sum, "1y")
	return Score{
		Symbol:        sum.Symbol,
		Momentum12_1:  nanUnless(sum.Bars >= momentumBars+1, tr.Momentum12_1),
		Return1Y:      yearCovered,
		Return3M:      windowReturn(sum, "3m"),
		Volatility1Y:  nanUnless(!math.IsNaN(yearCovered), sum.Volatility1Y),
		MaxDrawdown1Y: nanUnless(!math.IsNaN(yearCovered), sum.MaxDrawdown1Y),
		DividendYield: nanUnless(!math.IsNaN(yearCovered), sum.TTMDividendYield),
		PctVsSMA200:   nanUnless(sum.Bars >= longSMABars, tr.PctVsSMA200),
		Trend:         tr.State,
		Bars:          sum.Bars,
	}, nil
}

// windowReturn returns the total return of the labelled window, NaN when
// the window is missing or unavailable.
func windowReturn(sum Summary, label string) float64 {
	for _, w := range sum.Windows {
		if w.Label == label && w.Available {
			return w.TotalReturn
		}
	}
	return math.NaN()
}

// nanUnless returns v when ok, else NaN.
func nanUnless(ok bool, v float64) float64 {
	if ok {
		return v
	}
	return math.NaN()
}

// RankScores returns a sorted copy of scores ordered by the field that the
// rank key selects, descending when asked and ascending otherwise. The
// sort is stable, so ties keep their input order, and rows whose value is
// NaN (insufficient history) always come last in input order. The input
// slice is not modified. An unknown key returns an error wrapping
// ErrInvalidInput.
func RankScores(scores []Score, by string, descending bool) ([]Score, error) {
	if _, err := (Score{}).Value(by); err != nil {
		return nil, err
	}
	out := slices.Clone(scores)
	slices.SortStableFunc(out, func(a, b Score) int {
		va, _ := a.Value(by)
		vb, _ := b.Value(by)
		return compareNaNLast(va, vb, descending)
	})
	return out, nil
}

// compareNaNLast orders two values for sorting with NaN after every number
// regardless of direction.
func compareNaNLast(a, b float64, descending bool) int {
	switch aNaN, bNaN := math.IsNaN(a), math.IsNaN(b); {
	case aNaN && bNaN:
		return 0
	case aNaN:
		return 1
	case bNaN:
		return -1
	case descending:
		return cmp.Compare(b, a)
	default:
		return cmp.Compare(a, b)
	}
}
