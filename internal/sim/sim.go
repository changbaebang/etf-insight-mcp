// Package sim is the dollar-cost-averaging engine. Given the price history
// of one or more ETFs and a Plan, it buys fractional shares at the close on
// every contribution day and reports what the investor ended up with.
//
// Money conventions, repeated on the fields they apply to:
//
//   - A contribution is Plan.Amount in the plan currency. FeeRate of it is
//     lost to commissions; the net remainder is split by Allocation.Weight
//     and invested at that day's close.
//   - Shares are fractional; nothing is rounded to whole shares.
//   - Plan.Reinvest selects the dividend model: true buys and values shares
//     at Bar.AdjClose (dividends reinvested on the pay date), false uses
//     Bar.Close and keeps cash dividends uninvested.
//   - A KRW plan converts each contribution to USD at that day's exchange
//     rate and values holdings back into KRW at the valuation day's rate.
//   - The trading calendar is the set of days on which every allocated
//     symbol has a bar.
package sim

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// Cadence is how often a contribution is made.
type Cadence string

// Cadences a Plan can use. See Plan.Cadence for how contribution days are
// chosen on the trading calendar.
const (
	// Daily contributes on every trading day.
	Daily Cadence = "daily"
	// Weekly contributes on the first trading day of each ISO week.
	Weekly Cadence = "weekly"
	// Monthly contributes on the first trading day of each calendar month.
	Monthly Cadence = "monthly"
)

// ParseCadence parses a cadence name, ignoring case and surrounding spaces.
func ParseCadence(s string) (Cadence, error) {
	c := Cadence(strings.ToLower(strings.TrimSpace(s)))
	switch c {
	case Daily, Weekly, Monthly:
		return c, nil
	default:
		return "", fmt.Errorf("sim: bad cadence %q (want daily, weekly or monthly)", s)
	}
}

// Currencies a Plan can be denominated in.
const (
	// CurrencyUSD values everything in US dollars, the currency of the
	// price series.
	CurrencyUSD = "USD"
	// CurrencyKRW values everything in Korean won using Input.FX.
	CurrencyKRW = "KRW"
)

// weightTolerance is how far the allocation weights may stray from 1.
const weightTolerance = 1e-6

// Allocation is one symbol of a plan and the share of every contribution
// that goes to it.
type Allocation struct {
	// Symbol is the ticker, e.g. "VOO". It is trimmed and upper-cased
	// before it is looked up in Input.Series.
	Symbol string
	// Weight is the fraction of each net contribution invested in Symbol.
	// Every weight must be > 0 and the weights of a plan must sum to 1
	// within 1e-6.
	Weight float64
}

// Plan describes a recurring purchase programme.
type Plan struct {
	// Allocations lists the symbols to buy and their weights.
	Allocations []Allocation
	// Amount is the gross size of one contribution in Currency; > 0.
	Amount float64
	// Currency is "USD" or "KRW", case-insensitive. With KRW every
	// contribution is converted to USD at that day's rate and all reported
	// money is converted back to KRW.
	Currency string
	// Cadence picks the contribution days on the trading calendar:
	// Daily = every trading day, Weekly = the first trading day of each
	// ISO week, Monthly = the first trading day of each calendar month.
	// The first day of the calendar is always a contribution day.
	Cadence Cadence
	// Start and End are inclusive calendar dates; the clock and location
	// are ignored (see market.Day). A zero Start means the first day every
	// symbol has data for and a zero End means through the last such day.
	// A Start before some symbol's history, or an End after it, is moved
	// to the available range and reported in Result.Notes.
	Start, End time.Time
	// FeeRate is the fraction of every contribution lost to commissions,
	// e.g. 0.001 = 0.1%; 0 <= FeeRate < 1. The fee is taken before the
	// contribution is split between symbols.
	FeeRate float64
	// Reinvest selects the dividend model. true: shares are bought and
	// valued at Bar.AdjClose, which treats every dividend as reinvested on
	// its pay date. false: shares are bought and valued at Bar.Close, and
	// on every trading day shares × Bar.Dividend is credited to uninvested
	// cash that counts towards FinalValue but is never reinvested.
	Reinvest bool
}

// Input is the price data a plan is simulated against.
type Input struct {
	// Series holds the daily history of every allocated symbol, keyed by
	// upper-case symbol. Entries for other symbols are ignored.
	Series map[string]*market.Series
	// FX is the KRW per 1 USD series (Yahoo "KRW=X"). It is required when
	// Plan.Currency is "KRW" and ignored otherwise.
	FX *market.Series
}

// Run simulates p against in and returns what happened. It validates both
// arguments and returns a descriptive error for any problem; it never
// modifies its inputs.
func Run(p Plan, in Input) (*Result, error) {
	p, err := normalise(p)
	if err != nil {
		return nil, err
	}
	series, err := lookupSeries(p.Allocations, in.Series)
	if err != nil {
		return nil, err
	}
	cal, notes, err := buildCalendar(p, series)
	if err != nil {
		return nil, err
	}
	fx, err := newConverter(p.Currency, in.FX, cal.days[0])
	if err != nil {
		return nil, err
	}
	res, err := newEngine(p, cal, fx).run()
	if err != nil {
		return nil, err
	}
	res.Notes = append(notes, res.Notes...)
	return res, nil
}

// normalise validates p and returns a copy with trimmed upper-case symbols
// and currency, a parsed cadence and Day-normalised dates. It does not
// modify p or its slices.
func normalise(p Plan) (Plan, error) {
	allocs, err := normaliseAllocations(p.Allocations)
	if err != nil {
		return Plan{}, err
	}
	p.Allocations = allocs

	if !positiveFinite(p.Amount) {
		return Plan{}, fmt.Errorf("sim: amount must be > 0, got %v", p.Amount)
	}

	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	switch p.Currency {
	case CurrencyUSD, CurrencyKRW:
	default:
		return Plan{}, fmt.Errorf("sim: bad currency %q (want USD or KRW)", p.Currency)
	}

	p.Cadence, err = ParseCadence(string(p.Cadence))
	if err != nil {
		return Plan{}, err
	}

	if !p.Start.IsZero() {
		p.Start = market.Day(p.Start)
	}
	if !p.End.IsZero() {
		p.End = market.Day(p.End)
	}
	if !p.Start.IsZero() && !p.End.IsZero() && p.End.Before(p.Start) {
		return Plan{}, fmt.Errorf("sim: end %s is before start %s", formatDate(p.End), formatDate(p.Start))
	}

	if math.IsNaN(p.FeeRate) || p.FeeRate < 0 || p.FeeRate >= 1 {
		return Plan{}, fmt.Errorf("sim: fee rate must be in [0, 1), got %v", p.FeeRate)
	}
	return p, nil
}

// normaliseAllocations validates the allocations and returns a copy with
// trimmed upper-case symbols.
func normaliseAllocations(allocs []Allocation) ([]Allocation, error) {
	if len(allocs) == 0 {
		return nil, errors.New("sim: plan has no allocations")
	}
	out := make([]Allocation, 0, len(allocs))
	seen := make(map[string]bool, len(allocs))
	sum := 0.0
	for i, a := range allocs {
		sym := strings.ToUpper(strings.TrimSpace(a.Symbol))
		if sym == "" {
			return nil, fmt.Errorf("sim: allocation %d has an empty symbol", i)
		}
		if seen[sym] {
			return nil, fmt.Errorf("sim: symbol %s is allocated twice", sym)
		}
		seen[sym] = true
		if !positiveFinite(a.Weight) {
			return nil, fmt.Errorf("sim: weight of %s must be > 0, got %v", sym, a.Weight)
		}
		sum += a.Weight
		out = append(out, Allocation{Symbol: sym, Weight: a.Weight})
	}
	if math.Abs(sum-1) > weightTolerance {
		return nil, fmt.Errorf("sim: allocation weights sum to %v, want 1", sum)
	}
	return out, nil
}

// positiveFinite reports whether x is a finite number greater than zero.
// It is false for NaN, since NaN compares false with everything.
func positiveFinite(x float64) bool {
	return x > 0 && !math.IsInf(x, 1)
}

// lookupSeries resolves every allocation to its series and checks the
// series invariants.
func lookupSeries(allocs []Allocation, series map[string]*market.Series) ([]*market.Series, error) {
	out := make([]*market.Series, len(allocs))
	for i, a := range allocs {
		s, ok := series[a.Symbol]
		if !ok || s == nil {
			return nil, fmt.Errorf("sim: no price series for %s", a.Symbol)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("sim: series %s: %w", a.Symbol, err)
		}
		if s.Len() == 0 {
			return nil, fmt.Errorf("sim: series %s has no bars", a.Symbol)
		}
		out[i] = s
	}
	return out, nil
}

// formatDate renders d in the wire date format.
func formatDate(d time.Time) string {
	return d.Format(market.DateLayout)
}
