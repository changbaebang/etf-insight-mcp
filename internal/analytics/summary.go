package analytics

import (
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// trailingYearDays is the span of the "trailing year": 52 weeks. The
// window holds the bars dated after the anchor minus this many days,
// through the anchor, see trailingYear.
const trailingYearDays = 364

// Window is the total return of a series over one trailing period that
// ends at Summary.AsOf.
type Window struct {
	// Label names the period: "1m", "3m", "6m", "1y", "3y", "5y", "10y"
	// or "max" (since the first bar).
	Label string
	// TotalReturn is AdjClose(AsOf) / AdjClose(From) - 1, i.e. the growth
	// with dividends reinvested, as a fraction.
	TotalReturn float64
	// Annualized is the compound annual growth rate of windows of a year
	// or more (AnnualizedAvailable true). The 1y, 3y, 5y and 10y windows
	// are annualized over their nominal 1, 3, 5 and 10 years, so the 1y
	// rate equals TotalReturn; "max" is annualized over its calendar span
	// at 365.25 days a year once that span reaches 365 days. Shorter
	// windows are not extrapolated (annualizing a few months is
	// misleading): Annualized then equals TotalReturn and
	// AnnualizedAvailable is false, so callers can omit it.
	Annualized float64
	// AnnualizedAvailable reports whether Annualized is a real CAGR.
	AnnualizedAvailable bool
	// From is the date of the window's first bar: the last bar on or
	// before AsOf minus the period.
	From time.Time
	// Available is false when the series does not reach back far enough
	// to cover the period; the other fields are then zero.
	Available bool
}

// windowSpec pairs a Window label with the window's length in calendar
// months, counted back from AsOf with monthsBack. Zero months means "since
// the first bar".
type windowSpec struct {
	label  string
	months int
}

var windowSpecs = []windowSpec{
	{label: "1m", months: 1},
	{label: "3m", months: 3},
	{label: "6m", months: 6},
	{label: "1y", months: 12},
	{label: "3y", months: 36},
	{label: "5y", months: 60},
	{label: "10y", months: 120},
	{label: "max"},
}

// monthsBack returns the date n months before t, clamped to the last day
// of the target month when t's day does not exist there. time.AddDate
// alone would normalise "31 April" to 1 May and "29 February 2023" to 1
// March, which starts a window a few days late and shortens it.
func monthsBack(t time.Time, n int) time.Time {
	back := t.AddDate(0, -n, 0)
	if back.Day() < t.Day() {
		// The day overflowed into the next month: step back to the last
		// day of the intended month.
		back = back.AddDate(0, 0, -back.Day())
	}
	return back
}

// Summary is a descriptive snapshot of one series as of a date. All
// return and drawdown figures are fractions (0.1 = 10%), computed on
// AdjClose unless stated otherwise.
//
// For a series younger than 52 weeks, Volatility1Y, MaxDrawdown1Y, the
// TTM dividend figures and High52W/Low52W cover only the bars it has,
// which makes a young fund look calmer, shallower or less generous than a
// full year would. Summary does not mark this: callers that present them
// as one-year figures must check the history length themselves
// (ScoreSeries reports them as NaN instead).
type Summary struct {
	// Symbol is the series symbol.
	Symbol string
	// AsOf is the date of the bar the snapshot was taken on: the last bar
	// on or before the requested date.
	AsOf time.Time
	// Close and AdjClose are the prices on AsOf.
	Close, AdjClose float64
	// FirstDate is the date of the first bar in the series.
	FirstDate time.Time
	// Bars is the number of bars up to and including AsOf.
	Bars int
	// Windows holds one entry per label in order 1m, 3m, 6m, 1y, 3y, 5y,
	// 10y, max, including entries whose Available is false.
	Windows []Window
	// Volatility1Y is the annualized sample standard deviation of the
	// last 252 daily log returns of AdjClose (fewer when the series is
	// shorter; 0 with fewer than two returns).
	Volatility1Y float64
	// MaxDrawdown1Y is the largest decline from a prior high within the
	// trailing 52 weeks: the bars dated after AsOf minus 364 days, through
	// AsOf.
	MaxDrawdown1Y float64
	// MaxDrawdownAll is the largest decline from a prior high over the
	// whole series up to AsOf.
	MaxDrawdownAll float64
	// TTMDividendPerShare is the sum of Bar.Dividend over the same
	// trailing 52 weeks. The day exactly 52 weeks back is left out, so on
	// an ex-date that falls 52 weeks after the previous year's one a
	// quarterly payer counts 4 payments and a monthly payer 12, not one
	// more.
	TTMDividendPerShare float64
	// TTMDividendYield is TTMDividendPerShare / Close.
	TTMDividendYield float64
	// High52W and Low52W are the highest and lowest Close over the same
	// trailing 52 weeks.
	High52W, Low52W float64
}

// Summarize computes a Summary of s as of asOf, using the last bar on or
// before asOf. A zero asOf means the last bar. It returns an error
// wrapping ErrInvalidInput when the series fails market.Series.Validate,
// and one wrapping ErrInsufficientHistory when no bar exists on or before
// asOf.
func Summarize(s *market.Series, asOf time.Time) (Summary, error) {
	idx, err := anchorIndex(s, asOf)
	if err != nil {
		return Summary{}, err
	}
	// sub restricts every lookup below to bars on or before AsOf.
	sub := &market.Series{Meta: s.Meta, Bars: s.Bars[:idx+1]}
	last := sub.Bars[idx]
	adj := adjCloses(sub.Bars)
	year := trailingYear(sub, last.Date)

	sum := Summary{
		Symbol:         s.Meta.Symbol,
		AsOf:           last.Date,
		Close:          last.Close,
		AdjClose:       last.AdjClose,
		FirstDate:      sub.Bars[0].Date,
		Bars:           sub.Len(),
		Windows:        computeWindows(sub, last),
		Volatility1Y:   AnnualizedVolatility(LogReturns(tail(adj, tradingDaysPerYear+1))),
		MaxDrawdown1Y:  MaxDrawdown(adjCloses(year)),
		MaxDrawdownAll: MaxDrawdown(adj),
	}
	for _, b := range year {
		sum.TTMDividendPerShare += b.Dividend
	}
	sum.TTMDividendYield = sum.TTMDividendPerShare / last.Close
	sum.High52W, sum.Low52W = highLow(closes(year))
	return sum, nil
}

// trailingYear returns the bars of s in the 52 weeks ending on end: those
// dated after end minus 364 days, through end. Starting one day after the
// date 52 weeks back keeps two events exactly 52 weeks apart, such as the
// ex-dates of a quarterly payer, from both falling inside one year.
func trailingYear(s *market.Series, end time.Time) []market.Bar {
	return s.Between(end.AddDate(0, 0, 1-trailingYearDays), end)
}

// computeWindows evaluates every windowSpec against sub, whose last bar is
// last.
func computeWindows(sub *market.Series, last market.Bar) []Window {
	out := make([]Window, 0, len(windowSpecs))
	for _, spec := range windowSpecs {
		out = append(out, computeWindow(sub, spec, last))
	}
	return out
}

// computeWindow returns the Window for one spec, unavailable when sub does
// not reach back to the spec's start date.
func computeWindow(sub *market.Series, spec windowSpec, last market.Bar) Window {
	w := Window{Label: spec.label}
	from := sub.Bars[0]
	if spec.months > 0 {
		idx, ok := sub.IndexOn(monthsBack(last.Date, spec.months))
		if !ok {
			return w
		}
		from = sub.Bars[idx]
	}
	w.Available = true
	w.From = from.Date
	w.TotalReturn = last.AdjClose/from.AdjClose - 1
	if spec.months > 0 {
		w.Annualized, w.AnnualizedAvailable = annualizeMonths(w.TotalReturn, spec.months)
	} else {
		w.Annualized, w.AnnualizedAvailable = annualize(w.TotalReturn, from.Date, last.Date)
	}
	return w
}

// annualizeMonths converts a total return over a window of whole calendar
// months into a compound annual growth rate over its nominal length,
// months/12 years, when the window is at least a year long (ok true). A
// 12-month window's rate is therefore its total return. Shorter windows
// return the total return unchanged with ok false.
func annualizeMonths(total float64, months int) (cagr float64, ok bool) {
	switch {
	case months < 12:
		return total, false
	case months == 12:
		return total, true
	default:
		return math.Pow(1+total, 12/float64(months)) - 1, true
	}
}

// annualize converts a total return over [from, to] into a compound
// annual growth rate when the span is at least 365 days (ok true), and
// returns the total return unchanged with ok false otherwise.
func annualize(total float64, from, to time.Time) (cagr float64, ok bool) {
	days := to.Sub(from).Hours() / 24
	if days < 365 {
		return total, false
	}
	return math.Pow(1+total, daysPerYear/days) - 1, true
}

// highLow returns the maximum and minimum of values, or zeros when empty.
func highLow(values []float64) (high, low float64) {
	for i, v := range values {
		if i == 0 || v > high {
			high = v
		}
		if i == 0 || v < low {
			low = v
		}
	}
	return high, low
}
