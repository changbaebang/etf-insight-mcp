package sim

import (
	"time"
)

// engine holds the running state of one simulation. All money fields are
// in the plan currency unless their name says USD.
type engine struct {
	plan    Plan
	cal     *calendar
	fx      *converter
	contrib []bool // contrib[k] is true when days[k] is a contribution day

	shares     []float64 // per allocation, real (split-adjusted) shares
	investedBy []float64 // gross contributions per allocation

	contributions int
	invested      float64 // gross contributions
	fees          float64
	netInvested   float64 // contributions after fees
	netUSD        float64 // the same contributions in USD
	cash          float64 // uninvested cash dividends
	cashUSD       float64 // the same dividends before conversion
	units         float64 // units outstanding of the unitised NAV
	value         float64 // total value at the close of the current day

	firstRate, lastRate float64
	flows               []CashFlow
	navs                []float64
	timeline            []Point
}

// newEngine prepares an engine for a normalised plan on its calendar.
func newEngine(p Plan, cal *calendar, fx *converter) *engine {
	n := len(p.Allocations)
	return &engine{
		plan:       p,
		cal:        cal,
		fx:         fx,
		contrib:    contributionDays(p.Cadence, cal.days),
		shares:     make([]float64, n),
		investedBy: make([]float64, n),
		navs:       make([]float64, 0, len(cal.days)),
	}
}

// run walks the calendar day by day. On each day, in this order: dividends
// are handled (reinvested into more shares, or credited to cash), the NAV
// is recorded, the contribution is invested (when it is a contribution
// day) and the closing value is sampled for the timeline. Dividends come
// before the NAV so that the price drop on the ex-dividend date is offset
// by the shares or cash received, as in a total-return index; otherwise
// every dividend would register as a drawdown.
func (e *engine) run() (*Result, error) {
	for k, day := range e.cal.days {
		rate, err := e.fx.rate(day)
		if err != nil {
			return nil, err
		}
		if k == 0 {
			e.firstRate = rate
		}
		e.lastRate = rate

		if e.plan.Reinvest {
			e.reinvestDividends(k)
		} else {
			e.collectDividends(k, rate)
		}
		nav := e.nav(k, rate)
		e.navs = append(e.navs, nav)
		if e.contrib[k] {
			e.contribute(k, day, rate, nav)
		}
		e.value = e.totalValue(k, rate)
		if isLastOfMonth(e.cal.days, k) {
			e.timeline = append(e.timeline, Point{Date: day, Invested: e.invested, Value: e.value})
		}
	}
	return e.result(), nil
}

// reinvestDividends buys more shares with every dividend paid on day k:
// shares × Bar.Dividend is spent at that day's close, before the day's
// contribution, so shares bought that day earn nothing — which is how an
// ex-dividend date works. Buying at Close keeps the share count real,
// unlike valuing with Bar.AdjClose, which is back-adjusted by dividends
// paid after the range and so does not count shares anyone holds.
func (e *engine) reinvestDividends(k int) {
	for i := range e.plan.Allocations {
		b := e.cal.bars[i][k]
		if b.Dividend <= 0 || e.shares[i] <= 0 {
			continue
		}
		e.shares[i] += e.shares[i] * b.Dividend / b.Close
	}
}

// collectDividends credits shares × Bar.Dividend to cash for every symbol
// paying a dividend on day k, converted at that day's rate. It runs before
// the day's contribution, so shares bought on the ex-dividend date earn
// nothing.
func (e *engine) collectDividends(k int, rate float64) {
	for i := range e.plan.Allocations {
		div := e.cal.bars[i][k].Dividend
		if div <= 0 {
			continue
		}
		usd := e.shares[i] * div
		e.cashUSD += usd
		e.cash += usd * rate
	}
}

// nav returns the unitised net asset value before any contribution on day k.
//
// The portfolio is treated like a fund: every contribution buys units at the
// current NAV, so the NAV path reflects only market moves (and, for a KRW
// plan, exchange-rate moves). Drawdown is measured on this path rather than
// on total value because fresh contributions would otherwise mask falls: a
// portfolio that loses 30% in a month but receives a contribution of the
// same size shows no drop in total value at all. The first unit is issued
// at NAV 1, so the path starts at 1.
func (e *engine) nav(k int, rate float64) float64 {
	if e.units <= 0 {
		return 1
	}
	return e.totalValue(k, rate) / e.units
}

// contribute invests one contribution on day k. The fees (the rate on the
// gross amount plus the fixed commission) come off first, the net
// remainder is converted to USD at rate and split by weight, and each
// symbol's share count grows by its slice divided by that day's close.
func (e *engine) contribute(k int, day time.Time, rate, nav float64) {
	gross := e.plan.Amount
	fee := gross*e.plan.FeeRate + e.plan.FeeFixed
	net := gross - fee
	netUSD := net / rate

	e.contributions++
	e.invested += gross
	e.fees += fee
	e.netInvested += net
	e.netUSD += netUSD
	e.units += net / nav
	e.flows = append(e.flows, CashFlow{Date: day, Amount: -gross})

	for i, a := range e.plan.Allocations {
		e.shares[i] += netUSD * a.Weight / e.cal.bars[i][k].Close
		e.investedBy[i] += gross * a.Weight
	}
}

// holdingsUSD returns the USD value of all shares at day k's close.
func (e *engine) holdingsUSD(k int) float64 {
	total := 0.0
	for i := range e.plan.Allocations {
		total += e.shares[i] * e.cal.bars[i][k].Close
	}
	return total
}

// totalValue returns holdings plus uninvested cash at day k's close, in
// the plan currency.
func (e *engine) totalValue(k int, rate float64) float64 {
	return e.holdingsUSD(k)*rate + e.cash
}

// result assembles the Result after the last day has been processed.
func (e *engine) result() *Result {
	last := len(e.cal.days) - 1
	res := &Result{
		Start:          e.cal.days[0],
		End:            e.cal.days[last],
		Contributions:  e.contributions,
		Invested:       e.invested,
		Fees:           e.fees,
		FinalValue:     e.value,
		Profit:         e.value - e.invested,
		MaxDrawdownPct: MaxDrawdown(e.navs) * 100,
		CashDividends:  e.cash,
		Holdings:       e.holdings(last),
		Timeline:       e.timeline,
	}
	if e.invested > 0 {
		res.ReturnPct = res.Profit / e.invested * 100
	}

	e.flows = append(e.flows, CashFlow{Date: res.End, Amount: e.value})
	rate, err := XIRR(e.flows)
	if err != nil {
		res.Notes = append(res.Notes, annualizedUnavailableNote)
	} else {
		res.AnnualizedReturn = rate
		res.AnnualizedReturnComputed = true
	}

	if e.fx.enabled() {
		res.FX = e.fxSummary(last)
	}
	return res
}

// annualizedUnavailableNote explains a missing AnnualizedReturn in words a
// reader can act on; the raw XIRR error is deliberately not exposed.
const annualizedUnavailableNote = "annualized return not computed: the range is too short to annualise or no rate between -99.99% and +1000% per year fits the cash flows"

// holdings reports the final position per symbol, valued at day k's close
// and the last exchange rate.
func (e *engine) holdings(k int) []Holding {
	out := make([]Holding, len(e.plan.Allocations))
	for i, a := range e.plan.Allocations {
		value := e.shares[i] * e.cal.bars[i][k].Close * e.lastRate
		h := Holding{
			Symbol:   a.Symbol,
			Shares:   e.shares[i],
			Invested: e.investedBy[i],
			Value:    value,
		}
		if e.value > 0 {
			h.Weight = value / e.value
		}
		out[i] = h
	}
	return out
}

// fxSummary explains the exchange-rate contribution to a KRW result. The
// final USD value counts cash dividends at their original USD amount, so
// FXEffect attributes each dividend's rate move as well as the holdings'.
func (e *engine) fxSummary(last int) *FXSummary {
	avg := e.netInvested / e.netUSD
	finalUSD := e.holdingsUSD(last) + e.cashUSD
	return &FXSummary{
		StartRate:       e.firstRate,
		EndRate:         e.lastRate,
		AvgPurchaseRate: avg,
		FXEffect:        e.value - finalUSD*avg,
	}
}
