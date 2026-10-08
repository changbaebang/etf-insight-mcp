package analytics

import (
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// trailingYearDays is the span of the "trailing year" windows (52 weeks).
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
	// Annualized is the compound annual growth rate when the window spans
	// at least 365 calendar days. Shorter windows are not extrapolated
	// (annualizing a few months is misleading), so Annualized then equals
	// TotalReturn.
	Annualized float64
	// From is the date of the window's first bar: the last bar on or
	// before AsOf minus the period.
	From time.Time
	// Available is false when the series does not reach back far enough
	// to cover the period; the other fields are then zero.
	Available bool
}

// windowSpec pairs a Window label with the function that derives the
// window's start date from AsOf. A nil back means "since the first bar".
type windowSpec struct {
	label string
	back  func(asOf time.Time) time.Time
}

var windowSpecs = []windowSpec{
	{label: "1m", back: func(t time.Time) time.Time { return t.AddDate(0, -1, 0) }},
	{label: "3m", back: func(t time.Time) time.Time { return t.AddDate(0, -3, 0) }},
	{label: "6m", back: func(t time.Time) time.Time { return t.AddDate(0, -6, 0) }},
	{label: "1y", back: func(t time.Time) time.Time { return t.AddDate(-1, 0, 0) }},
	{label: "3y", back: func(t time.Time) time.Time { return t.AddDate(-3, 0, 0) }},
	{label: "5y", back: func(t time.Time) time.Time { return t.AddDate(-5, 0, 0) }},
	{label: "10y", back: func(t time.Time) time.Time { return t.AddDate(-10, 0, 0) }},
	{label: "max", back: nil},
}

// Summary is a descriptive snapshot of one series as of a date. All
// return and drawdown figures are fractions (0.1 = 10%), computed on
// AdjClose unless stated otherwise.
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
	// trailing year (the 365 calendar days ending on AsOf, inclusive).
	MaxDrawdown1Y float64
	// MaxDrawdownAll is the largest decline from a prior high over the
	// whole series up to AsOf.
	MaxDrawdownAll float64
	// TTMDividendPerShare is the sum of Bar.Dividend over the trailing
	// year (the 365 calendar days ending on AsOf, inclusive).
	TTMDividendPerShare float64
	// TTMDividendYield is TTMDividendPerShare / Close.
	TTMDividendYield float64
	// High52W and Low52W are the highest and lowest Close over the
	// trailing year (the 365 calendar days ending on AsOf, inclusive).
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
	year := sub.Between(last.Date.AddDate(0, 0, -trailingYearDays), last.Date)

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
	if spec.back != nil {
		idx, ok := sub.IndexOn(spec.back(last.Date))
		if !ok {
			return w
		}
		from = sub.Bars[idx]
	}
	w.Available = true
	w.From = from.Date
	w.TotalReturn = last.AdjClose/from.AdjClose - 1
	w.Annualized = annualize(w.TotalReturn, from.Date, last.Date)
	return w
}

// annualize converts a total return over [from, to] into a compound
// annual growth rate when the span is at least 365 days, and returns the
// total return unchanged otherwise.
func annualize(total float64, from, to time.Time) float64 {
	days := to.Sub(from).Hours() / 24
	if days < 365 {
		return total
	}
	return math.Pow(1+total, daysPerYear/days) - 1
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
