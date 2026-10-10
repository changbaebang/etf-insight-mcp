package analytics

import (
	"fmt"
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// Bar counts behind the trend indicators.
const (
	shortSMABars     = 50  // "50-day average"
	longSMABars      = 200 // "200-day average"
	slopeBars        = 20  // SMA200Slope compares SMA200 now with 20 bars ago
	momentumSkipBars = 21  // 12-1 momentum skips the most recent month
	momentumBars     = 252 // ...and looks back one year
	sixMonthBars     = 126
	shortVolBars     = 20
)

// Trend states reported in Trend.State.
const (
	StateUptrend             = "uptrend"
	StateDowntrend           = "downtrend"
	StateSideways            = "sideways"
	StateInsufficientHistory = "insufficient-history"
)

// Trend is a rule-based reading of where one series sits relative to its
// moving averages as of a date. It describes the recent past; it does not
// say where the price goes next.
//
// Classification rule:
//   - "insufficient-history" when fewer than 200 bars exist up to AsOf;
//   - "uptrend" when Close > SMA200 and SMA50 > SMA200 and SMA200Slope > 0;
//   - "downtrend" when Close < SMA200 and SMA50 < SMA200 and SMA200Slope < 0;
//   - "sideways" otherwise.
//
// With 200 to 219 bars the slope cannot be computed, so the slope
// condition is dropped on both sides and the state follows the moving
// averages alone; Reasons says so. Every indicator that lacks history is
// mentioned in Reasons, whatever the state, including a volatility
// computed from fewer daily returns than its window. Percentages are in
// percent (4.2 means 4.2%); the other ratios are fractions.
type Trend struct {
	// AsOf is the date of the bar used: the last bar on or before the
	// requested date.
	AsOf time.Time
	// Close is the raw close on AsOf.
	Close float64
	// SMA50 and SMA200 are simple moving averages of Close.
	SMA50, SMA200 float64
	// PctVsSMA50 and PctVsSMA200 are (Close / SMA - 1) * 100.
	PctVsSMA50, PctVsSMA200 float64
	// Momentum12_1 is the classic 12-1 momentum on AdjClose: the price 21
	// bars ago divided by the price 252 bars ago, minus 1. 0 with fewer
	// than 253 bars.
	Momentum12_1 float64
	// Return6M is AdjClose(AsOf) / AdjClose 126 bars ago - 1. 0 with fewer
	// than 127 bars.
	Return6M float64
	// Volatility20D and Volatility1Y are annualized sample standard
	// deviations of the last 20 and 252 daily log returns of AdjClose, or
	// of the fewer returns a shorter series has (0 with fewer than two).
	Volatility20D, Volatility1Y float64
	// SMA200Slope is SMA200(AsOf) / SMA200 20 bars ago - 1. 0 with fewer
	// than 220 bars.
	SMA200Slope float64
	// State is one of the State* constants.
	State string
	// Reasons lists the rule inputs in plain words, e.g. "close 4.2% above
	// 200-day average", plus any indicator that lacked history.
	Reasons []string
}

// AnalyzeTrend computes a Trend of s as of asOf, using the last bar on or
// before asOf. A zero asOf means the last bar. Errors mirror Summarize.
func AnalyzeTrend(s *market.Series, asOf time.Time) (Trend, error) {
	idx, err := anchorIndex(s, asOf)
	if err != nil {
		return Trend{}, err
	}
	bars := s.Bars[:idx+1]
	last := bars[idx]
	cl := closes(bars)
	adj := adjCloses(bars)
	sma50 := SMA(cl, shortSMABars)
	sma200 := SMA(cl, longSMABars)

	t := Trend{
		AsOf:          last.Date,
		Close:         last.Close,
		SMA50:         orZero(sma50[idx]),
		SMA200:        orZero(sma200[idx]),
		PctVsSMA50:    orZero((last.Close/sma50[idx] - 1) * 100),
		PctVsSMA200:   orZero((last.Close/sma200[idx] - 1) * 100),
		Momentum12_1:  ratioMinusOne(adj, idx-momentumSkipBars, idx-momentumBars),
		Return6M:      ratioMinusOne(adj, idx, idx-sixMonthBars),
		Volatility20D: AnnualizedVolatility(LogReturns(tail(adj, shortVolBars+1))),
		Volatility1Y:  AnnualizedVolatility(LogReturns(tail(adj, tradingDaysPerYear+1))),
		SMA200Slope:   ratioMinusOne(sma200, idx, idx-slopeBars),
	}
	t.State, t.Reasons = classify(t, len(bars))
	return t, nil
}

// ratioMinusOne returns values[num] / values[den] - 1, or 0 when either
// index is out of range or either value is NaN.
func ratioMinusOne(values []float64, num, den int) float64 {
	if num < 0 || den < 0 || num >= len(values) || den >= len(values) {
		return 0
	}
	if math.IsNaN(values[num]) || math.IsNaN(values[den]) {
		return 0
	}
	return values[num]/values[den] - 1
}

// classify applies the rule documented on Trend and explains its inputs.
func classify(t Trend, bars int) (state string, reasons []string) {
	if bars < longSMABars {
		reasons = []string{fmt.Sprintf(
			"only %d of the %d bars needed for the 200-day average; the average and the close's distance from it are reported as 0",
			bars, longSMABars)}
		return StateInsufficientHistory, append(reasons, missingHistory(bars)...)
	}
	reasons = []string{
		describeVsAverage(t.PctVsSMA50, "50-day"),
		describeVsAverage(t.PctVsSMA200, "200-day"),
		describeCross(t.SMA50, t.SMA200),
	}
	slopeKnown := bars >= longSMABars+slopeBars
	if slopeKnown {
		reasons = append(reasons, describeSlope(t.SMA200Slope))
	}
	reasons = append(reasons, missingHistory(bars)...)
	if !slopeKnown {
		reasons = append(reasons, "without the slope, the state is judged on the averages alone")
	}

	above := t.Close > t.SMA200 && t.SMA50 > t.SMA200
	below := t.Close < t.SMA200 && t.SMA50 < t.SMA200
	switch {
	case above && (!slopeKnown || t.SMA200Slope > 0):
		return StateUptrend, reasons
	case below && (!slopeKnown || t.SMA200Slope < 0):
		return StateDowntrend, reasons
	default:
		return StateSideways, reasons
	}
}

// missingHistory names every Trend indicator, other than the 200-day
// average itself, that a series of the given number of bars is too short
// for.
func missingHistory(bars int) []string {
	var out []string
	needs := []struct {
		name string
		bars int
	}{
		{"50-day average", shortSMABars},
		{"200-day average slope", longSMABars + slopeBars},
		{"12-1 momentum", momentumBars + 1},
		{"6-month return", sixMonthBars + 1},
	}
	for _, n := range needs {
		if bars < n.bars {
			out = append(out, fmt.Sprintf("%s needs %d bars, only %d available; reported as 0", n.name, n.bars, bars))
		}
	}
	vols := []struct {
		name    string
		returns int
	}{
		{"20-day volatility", shortVolBars},
		{"1-year volatility", tradingDaysPerYear},
	}
	for _, v := range vols {
		switch returns := bars - 1; {
		case returns < 2:
			out = append(out, fmt.Sprintf("%s needs at least 3 bars, only %d available; reported as 0", v.name, bars))
		case returns < v.returns:
			out = append(out, fmt.Sprintf("%s uses only the %d daily returns available", v.name, returns))
		}
	}
	return out
}

// describeVsAverage words a percentage distance from a moving average.
func describeVsAverage(pct float64, name string) string {
	switch {
	case pct > 0:
		return fmt.Sprintf("close %.1f%% above %s average", pct, name)
	case pct < 0:
		return fmt.Sprintf("close %.1f%% below %s average", -pct, name)
	default:
		return fmt.Sprintf("close equal to %s average", name)
	}
}

// describeCross words the relation between the two moving averages.
func describeCross(sma50, sma200 float64) string {
	switch {
	case sma50 > sma200:
		return "50-day average above 200-day average"
	case sma50 < sma200:
		return "50-day average below 200-day average"
	default:
		return "50-day average equal to 200-day average"
	}
}

// describeSlope words the 20-bar change of the 200-day average.
func describeSlope(slope float64) string {
	switch {
	case slope > 0:
		return fmt.Sprintf("200-day average rising %.2f%% over the last %d bars", slope*100, slopeBars)
	case slope < 0:
		return fmt.Sprintf("200-day average falling %.2f%% over the last %d bars", -slope*100, slopeBars)
	default:
		return fmt.Sprintf("200-day average flat over the last %d bars", slopeBars)
	}
}
