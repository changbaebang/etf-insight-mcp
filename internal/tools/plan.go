package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/sim"
)

// defaultBaseline is the symbol a simulation is compared with unless
// compare_with says otherwise.
const defaultBaseline = "SPY"

// fxSymbol is the Yahoo ticker of the KRW per USD exchange rate.
const fxSymbol = "KRW=X"

// planInput holds the fields simulate_dca and simulate_portfolio_dca
// share; both embed it so the schema and the validation stay identical.
type planInput struct {
	Amount            float64 `json:"amount" jsonschema:"size of one contribution in currency before fees, e.g. 100 (USD) or 10000 (KRW); must be > 0"`
	Currency          string  `json:"currency,omitempty" jsonschema:"USD or KRW (default USD); with KRW each contribution is converted at that day's KRW=X rate and every money field is reported in KRW"`
	Cadence           string  `json:"cadence,omitempty" jsonschema:"daily (every trading day), weekly (first trading day of each ISO week) or monthly (first trading day of each calendar month); default daily"`
	Start             string  `json:"start" jsonschema:"first contribution date YYYY-MM-DD; moved forward with a note when the history starts later"`
	End               string  `json:"end,omitempty" jsonschema:"last valuation date YYYY-MM-DD, inclusive (default: the latest bar)"`
	FeeRate           float64 `json:"fee_rate,omitempty" jsonschema:"fraction of each contribution lost to commissions, e.g. 0.001 for 0.1% (default 0)"`
	ReinvestDividends *bool   `json:"reinvest_dividends,omitempty" jsonschema:"true (default) values shares at the dividend-adjusted close, i.e. dividends reinvested on the pay date; false buys at the raw close and keeps dividends as uninvested cash (cash_dividends)"`
	CompareWith       *string `json:"compare_with,omitempty" jsonschema:"symbol of a baseline run with the same plan over the same realised date range (default SPY); pass an empty string to skip the baseline"`
}

// allocationInput is one line of a portfolio.
type allocationInput struct {
	Symbol string  `json:"symbol" jsonschema:"ticker symbol, e.g. VOO"`
	Weight float64 `json:"weight" jsonschema:"share of each contribution, as a fraction (0.6) or a percentage (60); all weights together must sum to 1 or to 100"`
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
	StartRate       float64 `json:"start_rate"`
	EndRate         float64 `json:"end_rate"`
	AvgPurchaseRate float64 `json:"avg_purchase_rate"`
	FXEffect        float64 `json:"fx_effect"`
}

// simResultOutput is sim.Result on the wire. Money is in the plan
// currency; percentages are plain numbers.
type simResultOutput struct {
	Start               string          `json:"start"`
	End                 string          `json:"end"`
	Contributions       int             `json:"contributions"`
	Invested            float64         `json:"invested"`
	Fees                float64         `json:"fees"`
	FinalValue          float64         `json:"final_value"`
	Profit              float64         `json:"profit"`
	ReturnPct           float64         `json:"return_pct"`
	AnnualizedReturnPct float64         `json:"annualized_return_pct"`
	MaxDrawdownPct      float64         `json:"max_drawdown_pct"`
	CashDividends       float64         `json:"cash_dividends"`
	Holdings            []holdingOutput `json:"holdings"`
	Timeline            []pointOutput   `json:"timeline"`
	FX                  *fxOutput       `json:"fx,omitempty"`
	Notes               []string        `json:"notes,omitempty"`
}

// baselineOutput is the comparison run.
type baselineOutput struct {
	Symbol string `json:"symbol"`
	simResultOutput
}

// diffOutput is plan minus baseline.
type diffOutput struct {
	FinalValue          float64 `json:"final_value"`
	ReturnPct           float64 `json:"return_pct"`
	AnnualizedReturnPct float64 `json:"annualized_return_pct"`
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
	if err := checkFeeRate(in.FeeRate); err != nil {
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
		Reinvest:    in.ReinvestDividends == nil || *in.ReinvestDividends,
	}
	return plan, baseline, nil
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

// parseCadence validates the cadence, defaulting to daily.
func parseCadence(s string) (sim.Cadence, error) {
	if strings.TrimSpace(s) == "" {
		return sim.Daily, nil
	}
	c, err := sim.ParseCadence(s)
	if err != nil {
		return "", fmt.Errorf("cadence %q is not supported; use daily, weekly or monthly", s)
	}
	return c, nil
}

// checkAmount rejects a non-positive contribution.
func checkAmount(amount float64) error {
	if amount <= 0 || math.IsInf(amount, 0) {
		return fmt.Errorf("amount must be > 0, got %v", amount)
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

// parseAllocations validates a portfolio and normalises its weights to
// fractions summing to 1. Weights may be given as fractions (sum 1) or
// percentages (sum 100); anything else is an error.
func parseAllocations(in []allocationInput) ([]sim.Allocation, error) {
	if len(in) == 0 {
		return nil, errors.New("allocations must list at least one symbol; use list_etfs to find symbols")
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
	const tolerance = 1e-6
	if math.Abs(sum-1) > tolerance && math.Abs(sum-100) > tolerance*100 {
		return nil, fmt.Errorf("allocation weights sum to %v; use fractions summing to 1 or percentages summing to 100", sum)
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

// simulation is one sim.Run mapped to the wire shape, with the optional
// baseline and the difference between the two.
type simulation struct {
	result   simResultOutput
	baseline *baselineOutput
	diff     *diffOutput
}

// simulate fetches every series the plan needs, runs it, then runs the
// baseline symbol with the same plan over the realised date range so both
// cover exactly the same days. A baseline that cannot be computed is
// reported in notes rather than failing the call; a missing plan symbol
// or exchange rate is an error.
func (d Deps) simulate(ctx context.Context, plan sim.Plan, baseline string) (simulation, error) {
	symbols := make([]string, 0, len(plan.Allocations)+2)
	for _, a := range plan.Allocations {
		symbols = append(symbols, a.Symbol)
	}
	krw := plan.Currency == sim.CurrencyKRW
	if krw {
		symbols = append(symbols, fxSymbol)
	}
	if baseline != "" {
		symbols = append(symbols, baseline)
	}
	series, errs := d.fetchAll(ctx, symbols)
	for _, a := range plan.Allocations {
		if err := errs[a.Symbol]; err != nil {
			return simulation{}, err
		}
	}
	if err := errs[fxSymbol]; krw && err != nil {
		return simulation{}, fmt.Errorf("exchange rate: %w", err)
	}

	in := sim.Input{Series: series, FX: series[fxSymbol]}
	res, err := sim.Run(plan, in)
	if err != nil {
		return simulation{}, userError(err)
	}
	out := simulation{result: d.toSimResult(res, plan)}

	switch {
	case baseline == "":
	case len(plan.Allocations) == 1 && plan.Allocations[0].Symbol == baseline:
		out.result.Notes = append(out.result.Notes, fmt.Sprintf("baseline skipped: compare_with %s is the simulated symbol", baseline))
	case errs[baseline] != nil:
		out.result.Notes = append(out.result.Notes, fmt.Sprintf("baseline %s not computed: %v", baseline, errs[baseline]))
	default:
		bp := plan
		bp.Allocations = []sim.Allocation{{Symbol: baseline, Weight: 1}}
		bp.Start, bp.End = res.Start, res.End
		bres, err := sim.Run(bp, in)
		if err != nil {
			out.result.Notes = append(out.result.Notes, fmt.Sprintf("baseline %s not computed: %v", baseline, userError(err)))
			break
		}
		out.baseline = &baselineOutput{Symbol: baseline, simResultOutput: d.toSimResult(bres, bp)}
		out.diff = &diffOutput{
			FinalValue:          round2(res.FinalValue - bres.FinalValue),
			ReturnPct:           round2(res.ReturnPct - bres.ReturnPct),
			AnnualizedReturnPct: pct(res.AnnualizedReturn - bres.AnnualizedReturn),
		}
	}
	return out, nil
}

// toSimResult maps a sim.Result to the wire shape, rounding money to
// cents and shares to four decimals, and appends a note for every symbol
// the cache served stale.
func (d Deps) toSimResult(res *sim.Result, plan sim.Plan) simResultOutput {
	out := simResultOutput{
		Start:               formatDate(res.Start),
		End:                 formatDate(res.End),
		Contributions:       res.Contributions,
		Invested:            round2(res.Invested),
		Fees:                round2(res.Fees),
		FinalValue:          round2(res.FinalValue),
		Profit:              round2(res.Profit),
		ReturnPct:           round2(res.ReturnPct),
		AnnualizedReturnPct: pct(res.AnnualizedReturn),
		MaxDrawdownPct:      round2(res.MaxDrawdownPct),
		CashDividends:       round2(res.CashDividends),
		Holdings:            make([]holdingOutput, 0, len(res.Holdings)),
		Timeline:            make([]pointOutput, 0, len(res.Timeline)),
		Notes:               append([]string(nil), res.Notes...),
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
	for _, p := range res.Timeline {
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
	for _, a := range plan.Allocations {
		if w := d.staleWarning(a.Symbol); w != "" {
			out.Notes = append(out.Notes, w)
		}
	}
	if plan.Currency == sim.CurrencyKRW {
		if w := d.staleWarning(fxSymbol); w != "" {
			out.Notes = append(out.Notes, w)
		}
	}
	return out
}
