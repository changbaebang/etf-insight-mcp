// Package sim is the dollar-cost-averaging engine. Given the price history
// of one or more ETFs and a Plan, it buys fractional shares at the close on
// every contribution day and reports what the investor ended up with.
//
// Money conventions, repeated on the fields they apply to:
//
//   - A contribution is Plan.Amount in the plan currency, split by
//     Allocation.Weight into one order per symbol. Each order loses
//     FeeRate of itself and the fixed commission FeeFixed; the remainder
//     is invested at that day's close.
//   - Shares are fractional; nothing is rounded to whole shares.
//   - Shares are always bought and valued at Bar.Close, which the provider
//     adjusts for splits but not for dividends; Holding.Shares converts the
//     count back across splits after the range, so it is the number of
//     shares actually held at the end. Plan.Reinvest selects the dividend
//     model: true spends every dividend on more shares at the ex-dividend
//     date's close, false keeps cash dividends uninvested.
//   - A KRW plan converts each contribution to USD at that day's exchange
//     rate and values holdings back into KRW at the valuation day's rate.
//   - The trading calendar is the set of days on which every allocated
//     symbol (and every Plan.CalendarSymbols symbol) has a bar, bounded
//     for KRW plans by the exchange-rate history. A dividend dated on a
//     day missing from it is credited on the next calendar day.
//
// Run is the engine. RunRolling slides a fixed-length window over the
// history and runs the plan in each ("what if I had started in year X?");
// RunLumpSumVsDCA runs the same money as one purchase and as a recurring
// plan side by side.
package sim

import (
	"errors"
	"fmt"
	"math"
	"strconv"
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
	// Once contributes a single time, on the first trading day of the
	// range, and then only values the position. It turns Run into a
	// lump-sum simulation; RunLumpSumVsDCA uses it for the lump-sum leg.
	Once Cadence = "once"
)

// ParseCadence parses a cadence name, ignoring case and surrounding spaces.
func ParseCadence(s string) (Cadence, error) {
	c := Cadence(strings.ToLower(strings.TrimSpace(s)))
	switch c {
	case Daily, Weekly, Monthly, Once:
		return c, nil
	default:
		return "", fmt.Errorf("sim: bad cadence %q (want daily, weekly, monthly or once)", s)
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
	// ISO week, Monthly = the first trading day of each calendar month,
	// Once = the first day only. The first day of the calendar is always
	// a contribution day.
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
	// FeeFixed is a constant commission per ETF purchased, in the plan
	// currency, e.g. 0.99 USD, charged on top of FeeRate. Brokers charge
	// it per order, so a contribution pays it once for every allocation:
	// the order for a symbol of weight w is Amount × w × (1 − FeeRate) −
	// FeeFixed after fees, and must stay positive for the smallest
	// weight. A single-ETF plan pays it once per contribution. For small
	// daily purchases it is usually the dominant cost: 0.99 on a 5 USD
	// purchase is 19.8% of every contribution.
	FeeFixed float64
	// Reinvest selects the dividend model. Shares are always bought and
	// valued at Bar.Close. true: on every ex-dividend date shares ×
	// Bar.Dividend buys more shares at that day's close. false: the same
	// amount is credited to uninvested cash that counts towards FinalValue
	// but is never reinvested.
	Reinvest bool
	// CalendarSymbols lists symbols that are not bought but whose trading
	// days further restrict the calendar and whose history bounds the
	// range, exactly like an allocated symbol. Two plans that share the
	// same CalendarSymbols ∪ allocations therefore contribute on identical
	// days, which makes their results comparable. Each symbol needs an
	// entry in Input.Series.
	CalendarSymbols []string
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
	pr, err := prepare(p, in)
	if err != nil {
		return nil, err
	}
	res, err := newEngine(pr.plan, pr.cal, pr.fx).run()
	if err != nil {
		return nil, err
	}
	res.Notes = append(pr.notes, res.Notes...)
	return res, nil
}

// prepared is everything an engine needs: the normalised plan, its trading
// calendar, the currency converter and the notes buildCalendar produced
// while fitting the requested range to the available history.
type prepared struct {
	plan  Plan
	cal   *calendar
	fx    *converter
	notes []string
}

// prepare validates p against in and builds its calendar without running
// anything. Run, RunRolling and RunLumpSumVsDCA all start here, so a plan
// that is valid for one is valid for the others.
func prepare(p Plan, in Input) (*prepared, error) {
	p, err := normalise(p)
	if err != nil {
		return nil, err
	}
	allocated, err := lookupSeries(allocationSymbols(p.Allocations), in.Series)
	if err != nil {
		return nil, err
	}
	extra, err := lookupSeries(p.CalendarSymbols, in.Series)
	if err != nil {
		return nil, err
	}
	fx, err := newConverter(p.Currency, in.FX)
	if err != nil {
		return nil, err
	}
	cal, notes, err := buildCalendar(p, allocated, extra, fx.series())
	if err != nil {
		return nil, err
	}
	return &prepared{plan: p, cal: cal, fx: fx, notes: notes}, nil
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
	if math.IsNaN(p.FeeFixed) || p.FeeFixed < 0 || math.IsInf(p.FeeFixed, 0) {
		return Plan{}, fmt.Errorf("sim: fixed fee must be >= 0, got %v", p.FeeFixed)
	}
	if smallest := smallestAllocation(p.Allocations); p.FeeFixed >= p.Amount*smallest.Weight*(1-p.FeeRate) {
		if len(p.Allocations) == 1 {
			return Plan{}, fmt.Errorf("sim: fixed fee %s leaves nothing of a %s contribution to invest", amountText(p.FeeFixed), amountText(p.Amount))
		}
		return Plan{}, fmt.Errorf("sim: fixed fee %s per ETF leaves nothing of the %s order for %s (%s%% of a %s contribution) to invest",
			amountText(p.FeeFixed), amountText(p.Amount*smallest.Weight), smallest.Symbol, amountText(smallest.Weight*100), amountText(p.Amount))
	}

	p.CalendarSymbols, err = normaliseCalendarSymbols(p.CalendarSymbols, p.Allocations)
	if err != nil {
		return Plan{}, err
	}
	return p, nil
}

// normaliseCalendarSymbols trims and upper-cases the calendar symbols and
// drops duplicates and symbols that are already allocated.
func normaliseCalendarSymbols(syms []string, allocs []Allocation) ([]string, error) {
	if len(syms) == 0 {
		return nil, nil
	}
	allocated := make(map[string]bool, len(allocs))
	for _, a := range allocs {
		allocated[a.Symbol] = true
	}
	seen := make(map[string]bool, len(syms))
	out := make([]string, 0, len(syms))
	for i, raw := range syms {
		sym := strings.ToUpper(strings.TrimSpace(raw))
		if sym == "" {
			return nil, fmt.Errorf("sim: calendar symbol %d is empty", i)
		}
		if allocated[sym] || seen[sym] {
			continue
		}
		seen[sym] = true
		out = append(out, sym)
	}
	return out, nil
}

// smallestAllocation returns the allocation with the lowest weight, the
// first one on a tie. allocs must not be empty.
func smallestAllocation(allocs []Allocation) Allocation {
	smallest := allocs[0]
	for _, a := range allocs[1:] {
		if a.Weight < smallest.Weight {
			smallest = a
		}
	}
	return smallest
}

// allocationSymbols lists the symbols of allocs in order.
func allocationSymbols(allocs []Allocation) []string {
	out := make([]string, len(allocs))
	for i, a := range allocs {
		out[i] = a.Symbol
	}
	return out
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

// lookupSeries resolves every symbol to its series and checks the series
// invariants.
func lookupSeries(symbols []string, series map[string]*market.Series) ([]named, error) {
	out := make([]named, len(symbols))
	for i, sym := range symbols {
		s, ok := series[sym]
		if !ok || s == nil {
			return nil, fmt.Errorf("sim: no price series for %s", sym)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("sim: series %s: %w", sym, err)
		}
		if s.Len() == 0 {
			return nil, fmt.Errorf("sim: series %s has no bars", sym)
		}
		out[i] = named{sym: sym, s: s}
	}
	return out, nil
}

// formatDate renders d in the wire date format.
func formatDate(d time.Time) string {
	return d.Format(market.DateLayout)
}

// amountText renders an amount for an error message: rounded to four
// decimals, without trailing zeros (0.0588 rather than 0.058788947677836566).
func amountText(x float64) string {
	return strconv.FormatFloat(math.Round(x*1e4)/1e4, 'f', -1, 64)
}
