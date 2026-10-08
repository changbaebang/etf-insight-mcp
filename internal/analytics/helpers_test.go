package analytics

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// day builds a UTC-midnight date.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// isWeekday reports whether t falls Monday to Friday.
func isWeekday(t time.Time) bool {
	wd := t.Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

// weekdays returns n consecutive weekday dates starting at the first
// weekday on or after start.
func weekdays(start time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for t := market.Day(start); len(out) < n; t = t.AddDate(0, 0, 1) {
		if isWeekday(t) {
			out = append(out, t)
		}
	}
	return out
}

// weekdaysUntil returns every weekday from start through end, inclusive.
func weekdaysUntil(start, end time.Time) []time.Time {
	var out []time.Time
	for t := market.Day(start); !t.After(end); t = t.AddDate(0, 0, 1) {
		if isWeekday(t) {
			out = append(out, t)
		}
	}
	return out
}

// seriesFrom builds a series whose Close and AdjClose both equal
// price(i, date) for each date.
func seriesFrom(symbol string, dates []time.Time, price func(i int, date time.Time) float64) *market.Series {
	bars := make([]market.Bar, len(dates))
	for i, d := range dates {
		p := price(i, d)
		bars[i] = market.Bar{Date: d, Close: p, AdjClose: p}
	}
	return &market.Series{Meta: market.Meta{Symbol: symbol, Currency: "USD"}, Bars: bars}
}

// growthSeries builds n weekday bars from 2020-01-01 growing by a constant
// daily log return, so every daily log return equals dailyLog.
func growthSeries(symbol string, n int, dailyLog float64) *market.Series {
	return seriesFrom(symbol, weekdays(day(2020, time.January, 1), n), func(i int, _ time.Time) float64 {
		return 100 * math.Exp(dailyLog*float64(i))
	})
}

// randomWalkSeries builds n weekday bars from 2020-01-01 following a
// deterministic geometric random walk with the given daily drift and
// volatility (log-return units).
func randomWalkSeries(symbol string, n int, seed uint64, drift, vol float64) *market.Series {
	rng := rand.New(rand.NewPCG(seed, 1))
	price := 100.0
	return seriesFrom(symbol, weekdays(day(2020, time.January, 1), n), func(i int, _ time.Time) float64 {
		if i > 0 {
			price *= math.Exp(drift + vol*rng.NormFloat64())
		}
		return price
	})
}

// approx reports whether a and b differ by at most tol.
func approx(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// approxRel reports whether a and b differ by at most tol relative to the
// larger magnitude (absolute tol when both are near zero).
func approxRel(a, b, tol float64) bool {
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale < 1 {
		scale = 1
	}
	return math.Abs(a-b) <= tol*scale
}

// floatsMatch compares two slices element-wise with tolerance, treating
// NaN as equal to NaN.
func floatsMatch(got, want []float64, tol float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.IsNaN(want[i]) {
			if !math.IsNaN(got[i]) {
				return false
			}
			continue
		}
		if !approx(got[i], want[i], tol) {
			return false
		}
	}
	return true
}
