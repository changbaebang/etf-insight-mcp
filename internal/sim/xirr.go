package sim

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// CashFlow is one dated amount: negative for money paid in (a purchase),
// positive for money received (a sale or the final value).
type CashFlow struct {
	// Date is the calendar day of the flow; the clock is ignored.
	Date time.Time
	// Amount is signed as described on CashFlow.
	Amount float64
}

const (
	// xirrLow and xirrHigh bracket the rates XIRR searches: −99.99% to
	// +1000% per year.
	xirrLow  = -0.9999
	xirrHigh = 10.0
	// xirrTolerance is the half-width of the bracket at which bisection
	// stops.
	xirrTolerance = 1e-12
	// xirrMaxIterations bounds the bisection; 2^-60 of the initial
	// bracket is far below xirrTolerance, so it is never reached.
	xirrMaxIterations = 200
	// daysPerYear is the Actual/365 day-count convention used to turn
	// day differences into years.
	daysPerYear = 365.0
)

// XIRR returns the annualised internal rate of return of dated cash flows:
// the rate r solving Σ amount_i / (1+r)^(years_i) = 0, where years_i is
// the number of days from the earliest flow divided by 365. The result is
// a fraction (0.10 = 10% per year).
//
// It needs at least two flows, both a positive and a negative amount and
// a span of at least one day, and it searches r in [-0.9999, 10] by
// bisection; an error reports which condition failed.
func XIRR(flows []CashFlow) (float64, error) {
	years, amounts, err := prepareFlows(flows)
	if err != nil {
		return 0, err
	}
	npv := func(r float64) float64 {
		sum := 0.0
		for i, a := range amounts {
			sum += a / math.Pow(1+r, years[i])
		}
		return sum
	}
	return bisect(npv, xirrLow, xirrHigh)
}

// prepareFlows validates flows and returns, per flow, the years since the
// earliest flow and the amount.
func prepareFlows(flows []CashFlow) (years, amounts []float64, err error) {
	if len(flows) < 2 {
		return nil, nil, errors.New("sim: xirr needs at least two cash flows")
	}
	first, last := market.Day(flows[0].Date), market.Day(flows[0].Date)
	hasPositive, hasNegative := false, false
	for i, f := range flows {
		if math.IsNaN(f.Amount) || math.IsInf(f.Amount, 0) {
			return nil, nil, fmt.Errorf("sim: xirr flow %d has amount %v", i, f.Amount)
		}
		hasPositive = hasPositive || f.Amount > 0
		hasNegative = hasNegative || f.Amount < 0
		day := market.Day(f.Date)
		if day.Before(first) {
			first = day
		}
		if day.After(last) {
			last = day
		}
	}
	if !hasPositive || !hasNegative {
		return nil, nil, errors.New("sim: xirr needs both positive and negative cash flows")
	}
	if !last.After(first) {
		return nil, nil, errors.New("sim: xirr cash flows must span at least one day")
	}
	years = make([]float64, len(flows))
	amounts = make([]float64, len(flows))
	for i, f := range flows {
		years[i] = market.Day(f.Date).Sub(first).Hours() / 24 / daysPerYear
		amounts[i] = f.Amount
	}
	return years, amounts, nil
}

// bisect finds a root of f in [lo, hi] by bisection. f must change sign
// between lo and hi, or an error is returned.
func bisect(f func(float64) float64, lo, hi float64) (float64, error) {
	flo, fhi := f(lo), f(hi)
	switch {
	case flo == 0:
		return lo, nil
	case fhi == 0:
		return hi, nil
	case (flo < 0) == (fhi < 0):
		return 0, fmt.Errorf("sim: xirr has no rate in [%v, %v]", lo, hi)
	}
	for range xirrMaxIterations {
		mid := (lo + hi) / 2
		fmid := f(mid)
		if fmid == 0 || (hi-lo)/2 < xirrTolerance {
			return mid, nil
		}
		if (fmid < 0) == (flo < 0) {
			lo, flo = mid, fmid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2, nil
}
