package analytics

import (
	"fmt"
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// tradingDaysPerYear is the convention used to annualize daily figures.
const tradingDaysPerYear = 252

// daysPerYear converts calendar day spans into years for CAGR math.
const daysPerYear = 365.25

// nanSlice returns a slice of n NaN values.
func nanSlice(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// mean returns the arithmetic mean, or 0 for an empty slice.
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// sampleStdev returns the sample (n-1) standard deviation, or 0 when fewer
// than two values are given.
func sampleStdev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	var ss float64
	for _, v := range values {
		d := v - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(values)-1))
}

// orZero maps NaN and ±Inf to 0 so that results stay JSON-encodable and
// callers can treat "not computable" as a plain zero.
func orZero(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// tail returns the last n elements of values, or all of them when n is
// not smaller than the length.
func tail(values []float64, n int) []float64 {
	if n >= len(values) {
		return values
	}
	return values[len(values)-n:]
}

// closes extracts Bar.Close from bars, oldest first.
func closes(bars []market.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.Close
	}
	return out
}

// adjCloses extracts Bar.AdjClose from bars, oldest first.
func adjCloses(bars []market.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.AdjClose
	}
	return out
}

// anchorIndex validates s and returns the index of the last bar on or
// before asOf. A zero asOf selects the last bar.
func anchorIndex(s *market.Series, asOf time.Time) (int, error) {
	if err := s.Validate(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if s.Len() == 0 {
		return 0, fmt.Errorf("%w: %s has no bars", ErrInsufficientHistory, s.Meta.Symbol)
	}
	if asOf.IsZero() {
		return s.Len() - 1, nil
	}
	idx, ok := s.IndexOn(asOf)
	if !ok {
		return 0, fmt.Errorf("%w: %s has no bar on or before %s",
			ErrInsufficientHistory, s.Meta.Symbol, asOf.Format(market.DateLayout))
	}
	return idx, nil
}
