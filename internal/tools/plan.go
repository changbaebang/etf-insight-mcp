package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/sim"
)

// defaultBaseline is the symbol a simulation is compared with unless
// compare_with says otherwise.
const defaultBaseline = "SPY"

// fxSymbol is the Yahoo ticker of the KRW per USD exchange rate.
const fxSymbol = "KRW=X"

// costInput holds the fee and dividend fields every simulation tool
// shares; the tool inputs embed it so the schema and the validation stay
// identical.
type costInput struct {
	FeeRate           float64 `json:"fee_rate,omitempty" jsonschema:"fraction of each contribution lost to commissions, e.g. 0.001 for 0.1% (default 0)"`
	CommissionFixed   float64 `json:"commission_fixed,omitempty" jsonschema:"fixed commission per ETF purchased, in the plan currency, e.g. 0.99 (default 0): every contribution pays it once for each ETF it buys, as brokers charge per order. It is taken after fee_rate from each ETF's share of the contribution and must leave something of the smallest share to invest. For small daily purchases this is usually the dominant cost"`
	ReinvestDividends *bool   `json:"reinvest_dividends,omitempty" jsonschema:"true (default): every dividend buys more shares at the ex-dividend date's close; false: dividends accumulate as uninvested cash (cash_dividends). Shares are always real share counts bought at the close"`
}

// validate checks the fee fields against one contribution of amount split
// across allocs.
func (c costInput) validate(amount float64, allocs []sim.Allocation) error {
	if err := checkFeeRate(c.FeeRate); err != nil {
		return err
	}
	return checkFixedCommission(c.CommissionFixed, amount, c.FeeRate, allocs)
}

// reinvest applies the default of reinvest_dividends.
func (c costInput) reinvest() bool {
	return c.ReinvestDividends == nil || *c.ReinvestDividends
}

// planInput holds the fields simulate_dca and simulate_portfolio_dca
// share; both embed it so the schema and the validation stay identical.
type planInput struct {
	Amount   float64 `json:"amount" jsonschema:"size of one contribution in currency before fees, e.g. 100 (USD) or 10000 (KRW); must be > 0"`
	Currency string  `json:"currency,omitempty" jsonschema:"USD or KRW (default USD); with KRW each contribution is converted at that day's KRW=X rate and every money field is reported in KRW"`
	Cadence  string  `json:"cadence,omitempty" jsonschema:"daily (every trading day), weekly or monthly; default daily. The start date itself always contributes; later weekly/monthly contributions fall on the first trading day of each following ISO week / calendar month (a mid-period start is noted)"`
	Start    string  `json:"start" jsonschema:"first contribution date YYYY-MM-DD; moved forward with a note when the history starts later"`
	End      string  `json:"end,omitempty" jsonschema:"last valuation date YYYY-MM-DD, inclusive (default: the latest bar)"`
	costInput
	CompareWith *string `json:"compare_with,omitempty" jsonschema:"symbol of a baseline (default SPY) run with the same plan on exactly the days both it and the plan's symbols traded; see comparison. Pass an empty string to skip the baseline"`
}

// allocationInput is one line of a portfolio.
type allocationInput struct {
	Symbol string  `json:"symbol" jsonschema:"ticker symbol, e.g. VOO"`
	Weight float64 `json:"weight" jsonschema:"share of each contribution, as a fraction (0.6) or a percentage (60); all weights together must sum to 1 or to 100 within 1% and are then normalised, so thirds may be written 0.3333 or 33.3"`
}

// allocationOutput echoes a normalised allocation.
type allocationOutput struct {
	Symbol    string  `json:"symbol"`
	WeightPct float64 `json:"weight_pct"`
}

// holdingOutput is sim.Holding on the wire.
type holdingOutput struct {
	Symbol    string  `json:"symbol"`
	Shares    float64 `json:"shares"`
	Invested  float64 `json:"invested"`
	Value     float64 `json:"value"`
	WeightPct float64 `json:"weight_pct"`
}

// pointOutput is sim.Point on the wire.
type pointOutput struct {
	Date     string  `json:"date"`
	Invested float64 `json:"invested"`
	Value    float64 `json:"value"`
}

// fxOutput is sim.FXSummary on the wire.
type fxOutput struct {
	StartRate       float64 `json:"start_rate" jsonschema:"KRW per 1 USD (the KRW=X close) on start"`
	EndRate         float64 `json:"end_rate" jsonschema:"KRW per 1 USD on end, the rate the holdings are valued at"`
	AvgPurchaseRate float64 `json:"avg_purchase_rate" jsonschema:"KRW per 1 USD the plan effectively paid: the KRW invested after commissions divided by the USD it bought"`
	FXEffect        float64 `json:"fx_effect" jsonschema:"KRW: final_value minus the final USD value converted at avg_purchase_rate, i.e. the part of final_value due to the rate moving after the dollars were bought. Positive when end_rate is above avg_purchase_rate (the won is weaker than at the average purchase), negative when below. It is measured against avg_purchase_rate, not start_rate"`
}

// simResultOutput is sim.Result on the wire. Money is in the plan
// currency; percentages are plain numbers.
type simResultOutput struct {
	Start               string          `json:"start"`
	End                 string          `json:"end"`
	Contributions       int             `json:"contributions"`
	Invested            float64         `json:"invested"`
	Fees                float64         `json:"fees" jsonschema:"total commissions (fee_rate plus commission_fixed), in the plan currency"`
	CostRatioPct        float64         `json:"cost_ratio_pct" jsonschema:"fees as a percentage of invested"`
	FinalValue          float64         `json:"final_value"`
	Profit              float64         `json:"profit" jsonschema:"final_value minus invested"`
	ReturnPct           float64         `json:"return_pct"`
	AnnualizedReturnPct *float64        `json:"annualized_return_pct,omitempty" jsonschema:"money-weighted annual rate (XIRR); omitted when the range is shorter than a year or no rate fits, see notes"`
	MaxDrawdownPct      float64         `json:"max_drawdown_pct" jsonschema:"largest peak-to-trough fall of the unitised value path, contributions excluded"`
	CashDividends       float64         `json:"cash_dividends"`
	Holdings            []holdingOutput `json:"holdings"`
	Timeline            []pointOutput   `json:"timeline" jsonschema:"amount invested and portfolio value on the last trading day of every month and on end; ranges longer than 120 months keep only quarter-ends, or year-ends when even those exceed 120 points (see timeline_step)"`
	TimelineStep        string          `json:"timeline_step" jsonschema:"month, quarter or year: which period-ends timeline keeps. The final day is always included"`
	FX                  *fxOutput       `json:"fx,omitempty" jsonschema:"KRW plans only: how the exchange rate moved the result"`
	Notes               []string        `json:"notes,omitempty"`
}

// headlineOutput is the summary of one simulation leg without holdings
// or timeline; the comparison block uses it for both legs.
type headlineOutput struct {
	Start               string   `json:"start"`
	End                 string   `json:"end"`
	Contributions       int      `json:"contributions"`
	Invested            float64  `json:"invested"`
	Fees                float64  `json:"fees"`
	CostRatioPct        float64  `json:"cost_ratio_pct" jsonschema:"fees as a percentage of invested"`
	FinalValue          float64  `json:"final_value"`
	Profit              float64  `json:"profit"`
	ReturnPct           float64  `json:"return_pct"`
	AnnualizedReturnPct *float64 `json:"annualized_return_pct,omitempty"`
	MaxDrawdownPct      float64  `json:"max_drawdown_pct"`
	Notes               []string `json:"notes,omitempty"`
}

// baselineOutput is the comparison leg run on compare_with.
type baselineOutput struct {
	Symbol string `json:"symbol"`
	headlineOutput
}

// diffOutput is plan minus baseline, computed from the rounded figures so
// it reconciles with the numbers shown.
type diffOutput struct {
	FinalValue          float64  `json:"final_value"`
	ReturnPct           float64  `json:"return_pct"`
	AnnualizedReturnPct *float64 `json:"annualized_return_pct,omitempty" jsonschema:"omitted when either leg has no annualized return"`
}

// comparisonOutput puts the plan and the baseline side by side on exactly
// the same contribution days, so the difference is about the funds and
// not about the dates.
type comparisonOutput struct {
	Start          string         `json:"start"`
	End            string         `json:"end"`
	Contributions  int            `json:"contributions"`
	SameDaysAsPlan bool           `json:"same_days_as_plan" jsonschema:"true when the plan run on the comparison days reproduces the main result exactly (same range, contributions and figures); false when the baseline's history or missing trading days cut the range or moved or dropped any purchase or dividend day, in which case plan is the plan re-run on the common days"`
	Plan           headlineOutput `json:"plan" jsonschema:"the plan run on the comparison days (identical to the main result when same_days_as_plan is true)"`
	Baseline       baselineOutput `json:"baseline"`
	Diff           diffOutput     `json:"diff" jsonschema:"plan minus baseline over the comparison days"`
	Note           string         `json:"note,omitempty"`
}

// toPlan validates the shared fields, applies the defaults and returns
// the sim.Plan for allocs together with the baseline symbol, which is ""
// when the caller disabled the comparison.
func (in planInput) toPlan(allocs []sim.Allocation) (plan sim.Plan, baseline string, err error) {
	start, err := parseDate("start", in.Start)
	if err != nil {
		return sim.Plan{}, "", err
	}
	end, err := parseOptionalDate("end", in.End)
	if err != nil {
		return sim.Plan{}, "", err
	}
	if !end.IsZero() && end.Before(start) {
		return sim.Plan{}, "", fmt.Errorf("end %s is before start %s", formatDate(end), formatDate(start))
	}
	currency, err := parseCurrency(in.Currency)
	if err != nil {
		return sim.Plan{}, "", err
	}
	cadence, err := parseCadence(in.Cadence)
	if err != nil {
		return sim.Plan{}, "", err
	}
	if err := checkAmount(in.Amount); err != nil {
		return sim.Plan{}, "", err
	}
	if err := in.validate(in.Amount, allocs); err != nil {
		return sim.Plan{}, "", err
	}

	baseline = defaultBaseline
	if in.CompareWith != nil {
		baseline = normalizeSymbol(*in.CompareWith)
	}
	plan = sim.Plan{
		Allocations: allocs,
		Amount:      in.Amount,
		Currency:    currency,
		Cadence:     cadence,
		Start:       start,
		End:         end,
		FeeRate:     in.FeeRate,
		FeeFixed:    in.CommissionFixed,
		Reinvest:    in.reinvest(),
	}
	return plan, baseline, nil
}

// maxAmount bounds a contribution so that totals stay finite and
// representable; nobody contributes a trillion per purchase.
const maxAmount = 1e12

// checkFixedCommission rejects a negative fixed commission or one that
// consumes a whole order. The commission is charged per ETF, on that ETF's
// share of the contribution after fee_rate, so the smallest allocation is
// the one it must leave something of.
func checkFixedCommission(fixed, amount, feeRate float64, allocs []sim.Allocation) error {
	if math.IsNaN(fixed) || fixed < 0 || math.IsInf(fixed, 0) {
		return fmt.Errorf("commission_fixed must be >= 0, got %v", fixed)
	}
	if fixed == 0 || len(allocs) == 0 {
		return nil
	}
	smallest := allocs[0]
	for _, a := range allocs[1:] {
		if a.Weight < smallest.Weight {
			smallest = a
		}
	}
	order := amount * (1 - feeRate) * smallest.Weight
	switch {
	case fixed < order:
		return nil
	case len(allocs) == 1:
		return fmt.Errorf("commission_fixed %v leaves nothing of a %v contribution to invest", fixed, amount)
	default:
		return fmt.Errorf("commission_fixed %v is charged per ETF and leaves nothing of %s's share of each %v contribution (%s after fee_rate) to invest; raise amount or the weight of %s",
			fixed, smallest.Symbol, amount, obsNum(order), smallest.Symbol)
	}
}

// maxAllocations caps a portfolio's symbols: each one is fetched and
// aligned on a common calendar, and the outputs list one holding each.
const maxAllocations = 20

// minAnnualizedSpan is the shortest realised range that gets an
// annualized return: about a year. A plan over one calendar year
// realises roughly 361 days once weekends and holidays trim its ends, so
// the bar sits a little below 365. (analytics.Summarize annualizes its
// return windows from 365 calendar days; the two thresholds serve
// different inputs and differ on purpose.)
const minAnnualizedSpan = 360 * 24 * time.Hour

// annualizedPct returns the annualized return for the wire, or nil when
// the engine could not fit one or the range is shorter than about a year,
// in which case an annual rate would be an extrapolation (get_etf_info
// follows the same idea for its return windows). The second value is a
// note explaining a nil for the short-range case.
func annualizedPct(res *sim.Result) (*float64, string) {
	if !res.AnnualizedReturnComputed {
		return nil, ""
	}
	if res.End.Sub(res.Start) < minAnnualizedSpan {
		return nil, "annualized_return_pct omitted: the range is shorter than a year, so an annual rate would be an extrapolation"
	}
	return ptr(pct(res.AnnualizedReturn)), ""
}

// parseCurrency validates the plan currency, defaulting to USD.
func parseCurrency(s string) (string, error) {
	switch c := strings.ToUpper(strings.TrimSpace(s)); c {
	case "":
		return sim.CurrencyUSD, nil
	case sim.CurrencyUSD, sim.CurrencyKRW:
		return c, nil
	default:
		return "", fmt.Errorf("currency %q is not supported; use USD or KRW", s)
	}
}

// parseCadence validates a recurring cadence, defaulting to daily. The
// engine's "once" is reserved for the lump-sum leg of
// simulate_lump_sum_vs_dca and is not accepted from a caller: a single
// purchase is not a recurring plan.
func parseCadence(s string) (sim.Cadence, error) {
	if strings.TrimSpace(s) == "" {
		return sim.Daily, nil
	}
	c, err := sim.ParseCadence(s)
	if err != nil || c == sim.Once {
		return "", fmt.Errorf("cadence %q is not supported; use daily, weekly or monthly", s)
	}
	return c, nil
}

// checkAmount rejects a non-positive or absurdly large contribution.
func checkAmount(amount float64) error {
	if amount <= 0 || math.IsInf(amount, 0) || math.IsNaN(amount) {
		return fmt.Errorf("amount must be > 0, got %v", amount)
	}
	if amount > maxAmount {
		return fmt.Errorf("amount must be at most %g, got %v", maxAmount, amount)
	}
	return nil
}

// checkFeeRate rejects a fee outside [0, 1).
func checkFeeRate(rate float64) error {
	if rate < 0 || rate >= 1 || math.IsNaN(rate) {
		return fmt.Errorf("fee_rate must be a fraction in [0, 1), e.g. 0.001 for 0.1%%, got %v", rate)
	}
	return nil
}

// weightSumTolerance is how far, relative to 1 or 100, the weights of a
// portfolio may sum: 1%, so that thirds written as 0.3333 or 33.3 are
// accepted, while a weight typed as a percentage among fractions (60 and
// 0.4) is still caught.
const weightSumTolerance = 0.01

// parseAllocations validates a portfolio and normalises its weights to
// fractions summing to 1. Weights may be given as fractions (sum 1) or
// percentages (sum 100), each within weightSumTolerance; anything else is
// an error.
func parseAllocations(in []allocationInput) ([]sim.Allocation, error) {
	if len(in) == 0 {
		return nil, errors.New("allocations must list at least one symbol; use list_etfs to find symbols")
	}
	if len(in) > maxAllocations {
		return nil, fmt.Errorf("allocations lists %d symbols; at most %d are supported", len(in), maxAllocations)
	}
	out := make([]sim.Allocation, 0, len(in))
	seen := make(map[string]bool, len(in))
	sum := 0.0
	for i, a := range in {
		sym := normalizeSymbol(a.Symbol)
		if sym == "" {
			return nil, fmt.Errorf("allocation %d has an empty symbol", i)
		}
		if seen[sym] {
			return nil, fmt.Errorf("symbol %s is listed twice; merge its weights", sym)
		}
		seen[sym] = true
		if a.Weight <= 0 || math.IsInf(a.Weight, 0) {
			return nil, fmt.Errorf("weight of %s must be > 0, got %v", sym, a.Weight)
		}
		sum += a.Weight
		out = append(out, sim.Allocation{Symbol: sym, Weight: a.Weight})
	}
	if math.Abs(sum-1) > weightSumTolerance && math.Abs(sum-100) > weightSumTolerance*100 {
		return nil, fmt.Errorf("allocation weights sum to %s; use fractions summing to 1 or percentages summing to 100 (within 1%%)", obsNum(sum))
	}
	for i := range out {
		out[i].Weight /= sum
	}
	return out, nil
}

// toAllocationOutputs echoes allocations as percentages.
func toAllocationOutputs(allocs []sim.Allocation) []allocationOutput {
	out := make([]allocationOutput, 0, len(allocs))
	for _, a := range allocs {
		out = append(out, allocationOutput{Symbol: a.Symbol, WeightPct: pct(a.Weight)})
	}
	return out
}

// allocationSymbols lists the symbols of allocs in order.
func allocationSymbols(allocs []sim.Allocation) []string {
	out := make([]string, 0, len(allocs))
	for _, a := range allocs {
		out = append(out, a.Symbol)
	}
	return out
}

// loadInput fetches the history of every symbol plan buys, the exchange
// rate when the plan is in KRW and any extra symbols, warming the cache
// concurrently, and returns the sim.Input together with the fetch error
// of each extra symbol. A plan symbol or the exchange rate that cannot be
// loaded is an error, and so is a plan symbol quoted in a currency other
// than USD (see requireUSD): the engine would read its prices as dollars.
// A missing extra symbol is left to the caller.
func (d Deps) loadInput(ctx context.Context, plan sim.Plan, extra ...string) (sim.Input, map[string]error, error) {
	symbols := append(allocationSymbols(plan.Allocations), extra...)
	krw := plan.Currency == sim.CurrencyKRW
	if krw {
		symbols = append(symbols, fxSymbol)
	}
	series, errs := d.fetchAll(ctx, symbols)
	for _, a := range plan.Allocations {
		if err := errs[a.Symbol]; err != nil {
			return sim.Input{}, nil, err
		}
		if err := requireUSD(series[a.Symbol]); err != nil {
			return sim.Input{}, nil, err
		}
	}
	if err := errs[fxSymbol]; krw && err != nil {
		return sim.Input{}, nil, fmt.Errorf("exchange rate: %w", err)
	}
	return sim.Input{Series: series, FX: series[fxSymbol]}, errs, nil
}

// planWarnings collects what the caller should know about the data a plan
// was run on: a caution for every allocated symbol that is not an
// investable fund (an index, an exchange rate), and the cache warnings of
// every symbol the plan depends on, its allocations and, for a KRW plan,
// the exchange rate, plus a note for each of them whose latest bar is an
// intraday price the plan reads: used is the last date the run read (its
// End), or zero when it read every series up to its last bar.
func (d Deps) planWarnings(plan sim.Plan, in sim.Input, used time.Time) []string {
	var out []string
	for _, a := range plan.Allocations {
		if note := instrumentNote(in.Series[a.Symbol]); note != "" {
			out = append(out, note)
		}
	}
	for _, a := range plan.Allocations {
		out = append(out, d.staleWarnings(a.Symbol)...)
		out = append(out, nonEmpty(provisionalNote(in.Series[a.Symbol], used, d.clock()))...)
	}
	if plan.Currency == sim.CurrencyKRW {
		out = append(out, d.staleWarnings(fxSymbol)...)
		out = append(out, nonEmpty(provisionalNote(in.FX, used, d.clock()))...)
	}
	return out
}

// simulation is one sim.Run mapped to the wire shape, with the optional
// side-by-side comparison against the baseline.
type simulation struct {
	result     simResultOutput
	comparison *comparisonOutput
}

// simulate fetches every series the plan needs and runs it. When a
// baseline is requested, both the plan and the baseline are then run once
// more with each other's symbols as calendar symbols, so the two legs
// contribute on exactly the same days (the intersection of all their
// trading days, bounded by the shortest history). A baseline that cannot
// be computed, for example because it is not quoted in USD, is reported
// in notes rather than failing the call; a missing plan symbol or
// exchange rate is an error.
func (d Deps) simulate(ctx context.Context, plan sim.Plan, baseline string) (simulation, error) {
	var extra []string
	if baseline != "" {
		extra = []string{baseline}
	}
	in, errs, err := d.loadInput(ctx, plan, extra...)
	if err != nil {
		return simulation{}, err
	}
	res, err := sim.Run(plan, in)
	if err != nil {
		return simulation{}, userError(err)
	}
	if err := checkFinite("result", res.Invested, res.FinalValue, res.Profit); err != nil {
		return simulation{}, err
	}
	out := simulation{result: toSimResult(res)}
	out.result.Notes = append(out.result.Notes, d.planWarnings(plan, in, res.End)...)

	planSymbols := allocationSymbols(plan.Allocations)
	switch {
	case baseline == "":
	case len(plan.Allocations) == 1 && plan.Allocations[0].Symbol == baseline:
		out.result.Notes = append(out.result.Notes, fmt.Sprintf("baseline skipped: compare_with %s is the simulated symbol", baseline))
	case errs[baseline] != nil:
		out.result.Notes = append(out.result.Notes, fmt.Sprintf("baseline %s not computed: %v", baseline, errs[baseline]))
	default:
		if err := requireUSD(in.Series[baseline]); err != nil {
			out.result.Notes = append(out.result.Notes, fmt.Sprintf("baseline %s not computed: %v", baseline, err))
			break
		}
		cmp, err := d.compare(plan, baseline, planSymbols, in, res)
		if err != nil {
			out.result.Notes = append(out.result.Notes, fmt.Sprintf("baseline %s not computed: %v", baseline, userError(err)))
			break
		}
		out.comparison = cmp
		if note := instrumentNote(in.Series[baseline]); note != "" {
			out.result.Notes = append(out.result.Notes, note)
		}
		out.result.Notes = append(out.result.Notes, d.staleWarnings(baseline)...)
		out.result.Notes = append(out.result.Notes, nonEmpty(provisionalNote(in.Series[baseline], res.End, d.clock()))...)
	}
	return out, nil
}

// compare runs the plan and the baseline on their common calendar and
// reports both legs with their difference. res is the plan's own result,
// used to tell whether the common calendar changed anything.
func (d Deps) compare(plan sim.Plan, baseline string, planSymbols []string, in sim.Input, res *sim.Result) (*comparisonOutput, error) {
	common := plan
	common.CalendarSymbols = append(append([]string(nil), plan.CalendarSymbols...), baseline)
	cres, err := sim.Run(common, in)
	if err != nil {
		return nil, err
	}
	bp := plan
	bp.Allocations = []sim.Allocation{{Symbol: baseline, Weight: 1}}
	bp.CalendarSymbols = append(append([]string(nil), plan.CalendarSymbols...), planSymbols...)
	bres, err := sim.Run(bp, in)
	if err != nil {
		return nil, err
	}
	if bres.Contributions != cres.Contributions || !bres.Start.Equal(cres.Start) || !bres.End.Equal(cres.End) {
		return nil, fmt.Errorf("the two legs did not land on the same days (%d vs %d contributions)", cres.Contributions, bres.Contributions)
	}

	planLeg := toHeadline(cres)
	baseLeg := toHeadline(bres)
	cmp := &comparisonOutput{
		Start:          planLeg.Start,
		End:            planLeg.End,
		Contributions:  cres.Contributions,
		SameDaysAsPlan: sameResult(cres, res),
		Plan:           planLeg,
		Baseline:       baselineOutput{Symbol: baseline, headlineOutput: baseLeg},
		Diff: diffOutput{
			FinalValue: round2(planLeg.FinalValue - baseLeg.FinalValue),
			ReturnPct:  round2(planLeg.ReturnPct - baseLeg.ReturnPct),
		},
	}
	if planLeg.AnnualizedReturnPct != nil && baseLeg.AnnualizedReturnPct != nil {
		cmp.Diff.AnnualizedReturnPct = ptr(round2(*planLeg.AnnualizedReturnPct - *baseLeg.AnnualizedReturnPct))
	}
	if !cmp.SameDaysAsPlan {
		cmp.Note = fmt.Sprintf("comparison covers %s to %s (%d contributions, final value %s), only the days on which %s and %s all traded; the plan on its own covers %s to %s (%d contributions, final value %s), and %s lacks some of its trading days, so a purchase or dividend moved or dropped. Compare comparison.plan with comparison.baseline, not with the main result",
			cmp.Start, cmp.End, cres.Contributions, obsMoney(cres.FinalValue), strings.Join(planSymbols, ", "), baseline,
			formatDate(res.Start), formatDate(res.End), res.Contributions, obsMoney(res.FinalValue), baseline)
	}
	return cmp, nil
}

// sameResult reports whether two runs of one plan came out identical:
// the same range, contributions, money, drawdown and month-end path. The
// engine is deterministic, so a run on the same trading days reproduces
// every figure exactly. A purchase or dividend moved to another day or
// dropped changes at least one of them even when the range and the number
// of contributions stay the same, unless the prices on both days happen to
// match, and then the results really are the same.
func sameResult(a, b *sim.Result) bool {
	if !a.Start.Equal(b.Start) || !a.End.Equal(b.End) || a.Contributions != b.Contributions ||
		a.Invested != b.Invested || a.Fees != b.Fees || a.FinalValue != b.FinalValue ||
		a.CashDividends != b.CashDividends || a.MaxDrawdownPct != b.MaxDrawdownPct ||
		len(a.Timeline) != len(b.Timeline) {
		return false
	}
	for i, p := range a.Timeline {
		q := b.Timeline[i]
		if !p.Date.Equal(q.Date) || p.Invested != q.Invested || p.Value != q.Value {
			return false
		}
	}
	return true
}

// toHeadline maps the summary figures of a result to the wire shape.
func toHeadline(res *sim.Result) headlineOutput {
	h := headlineOutput{
		Start:          formatDate(res.Start),
		End:            formatDate(res.End),
		Contributions:  res.Contributions,
		Invested:       round2(res.Invested),
		Fees:           round2(res.Fees),
		FinalValue:     round2(res.FinalValue),
		ReturnPct:      round2(res.ReturnPct),
		MaxDrawdownPct: round2(res.MaxDrawdownPct),
		Notes:          append([]string(nil), res.Notes...),
	}
	if res.Invested > 0 {
		h.CostRatioPct = pct(res.Fees / res.Invested)
	}
	h.Profit = round2(h.FinalValue - h.Invested)
	var note string
	h.AnnualizedReturnPct, note = annualizedPct(res)
	if note != "" {
		h.Notes = append(h.Notes, note)
	}
	return h
}

// maxTimelinePoints caps the timeline of one result so that long ranges
// stay a readable size: 120 month-ends cover ten years, quarter-ends
// thirty, and year-ends any history the data source has.
const maxTimelinePoints = 120

// thinTimeline returns points unchanged when there are at most
// maxTimelinePoints, and otherwise only the quarter-ends or, when even
// those are too many, the year-ends, always keeping the final point. The
// second value names the period kept: "month", "quarter" or "year".
func thinTimeline(points []sim.Point) ([]sim.Point, string) {
	if len(points) <= maxTimelinePoints {
		return points, "month"
	}
	if quarters := periodEnds(points, 3); len(quarters) <= maxTimelinePoints {
		return quarters, "quarter"
	}
	return periodEnds(points, 12), "year"
}

// periodEnds keeps the month-end points whose month closes a period of
// months months (3: March, June, September, December; 12: December) and
// the final point, which is the end of the range.
func periodEnds(points []sim.Point, months int) []sim.Point {
	last := len(points) - 1
	out := make([]sim.Point, 0, len(points)/months+1)
	for i, p := range points {
		if i == last || int(p.Date.Month())%months == 0 {
			out = append(out, p)
		}
	}
	return out
}

// toSimResult maps a sim.Result to the wire shape, rounding money to
// cents and shares to four decimals and thinning long timelines (see
// thinTimeline). Data warnings are the caller's (see planWarnings), so a
// tool that shows several legs of one plan reports them once.
func toSimResult(res *sim.Result) simResultOutput {
	head := toHeadline(res)
	timeline, step := thinTimeline(res.Timeline)
	out := simResultOutput{
		Start:               head.Start,
		End:                 head.End,
		Contributions:       head.Contributions,
		Invested:            head.Invested,
		Fees:                head.Fees,
		CostRatioPct:        head.CostRatioPct,
		FinalValue:          head.FinalValue,
		Profit:              head.Profit,
		ReturnPct:           head.ReturnPct,
		AnnualizedReturnPct: head.AnnualizedReturnPct,
		MaxDrawdownPct:      head.MaxDrawdownPct,
		CashDividends:       round2(res.CashDividends),
		Holdings:            make([]holdingOutput, 0, len(res.Holdings)),
		Timeline:            make([]pointOutput, 0, len(timeline)),
		TimelineStep:        step,
		Notes:               head.Notes,
	}
	for _, h := range res.Holdings {
		out.Holdings = append(out.Holdings, holdingOutput{
			Symbol:    h.Symbol,
			Shares:    round4(h.Shares),
			Invested:  round2(h.Invested),
			Value:     round2(h.Value),
			WeightPct: pct(h.Weight),
		})
	}
	for _, p := range timeline {
		out.Timeline = append(out.Timeline, pointOutput{
			Date:     formatDate(p.Date),
			Invested: round2(p.Invested),
			Value:    round2(p.Value),
		})
	}
	if res.FX != nil {
		out.FX = &fxOutput{
			StartRate:       round2(res.FX.StartRate),
			EndRate:         round2(res.FX.EndRate),
			AvgPurchaseRate: round2(res.FX.AvgPurchaseRate),
			FXEffect:        round2(res.FX.FXEffect),
		}
	}
	return out
}
