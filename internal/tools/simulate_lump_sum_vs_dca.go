package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type simulateLumpSumVsDCAInput struct {
	Symbol      string            `json:"symbol,omitempty" jsonschema:"single ticker to buy, e.g. VOO; give either symbol or allocations, not both"`
	Allocations []allocationInput `json:"allocations,omitempty" jsonschema:"portfolio as a list of {symbol, weight} with weights summing to 1 or 100; alternative to symbol. Both legs split their money by these weights"`
	TotalAmount float64           `json:"total_amount" jsonschema:"the whole sum to invest in the plan currency before fees, e.g. 12000; the lump-sum leg invests all of it on the first trading day, the DCA leg splits it evenly over every contribution day; must be > 0"`
	Currency    string            `json:"currency,omitempty" jsonschema:"USD or KRW (default USD); with KRW every purchase is converted at that day's KRW=X rate and every money field is reported in KRW"`
	Cadence     string            `json:"cadence,omitempty" jsonschema:"how often the DCA leg buys: daily (every trading day), weekly (first trading day of each ISO week) or monthly (first trading day of each month); default daily"`
	Start       string            `json:"start" jsonschema:"day the lump sum is invested and the DCA leg starts, YYYY-MM-DD; moved forward with a note when the history starts later"`
	End         string            `json:"end,omitempty" jsonschema:"valuation date of both legs YYYY-MM-DD, inclusive (default: the latest bar); the DCA leg contributes up to it"`
	costInput
}

type simulateLumpSumVsDCAOutput struct {
	Allocations           []allocationOutput `json:"allocations"`
	TotalAmount           float64            `json:"total_amount"`
	Currency              string             `json:"currency"`
	Cadence               string             `json:"cadence" jsonschema:"cadence of the DCA leg"`
	PerContributionAmount float64            `json:"per_contribution_amount" jsonschema:"gross size of each DCA contribution: total_amount divided by the DCA leg's contributions"`
	LumpSum               simResultOutput    `json:"lump_sum" jsonschema:"total_amount invested on the first trading day of the range and held to end"`
	DCA                   simResultOutput    `json:"dca" jsonschema:"total_amount split evenly over every contribution day of the cadence and valued at end"`
	Diff                  diffOutput         `json:"diff" jsonschema:"lump_sum minus dca from the rounded figures: positive means investing everything on the first day did better"`
	Notes                 []string           `json:"notes,omitempty"`
	Disclaimer            string             `json:"disclaimer"`
}

// lumpSumNoteNames renames the engine's field names in sim's lump-sum
// notes to the wire names the model sees.
var lumpSumNoteNames = strings.NewReplacer("Diff is lump sum minus DCA", "diff is lump_sum minus dca")

func (d Deps) registerSimulateLumpSumVsDCA(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "simulate_lump_sum_vs_dca",
		Title:       "Simulate lump sum vs DCA",
		Description: "Answers \"invest it all now or spread it out?\" with history. The same total_amount is put into the symbol (or portfolio) twice on one trading calendar: lump_sum invests all of it on the first trading day on or after start, dca splits it evenly over every daily, weekly or monthly contribution day from start to end (per_contribution_amount = total_amount / contributions); both legs are valued at end with the same fees and dividend model. Each leg reports what simulate_dca reports (invested, fees, cost_ratio_pct, final value, profit, return, money-weighted annualized return omitted for ranges under a year, max drawdown, holdings, month-end timeline) and diff = lump_sum minus dca (positive: the lump sum did better). commission_fixed is paid once by the lump sum and once per purchase by the DCA leg, which can decide small plans. Defaults: currency USD, cadence daily, end = latest bar, fee_rate 0, commission_fixed 0, dividends reinvested. One start date only; for how the answer depends on the start date use simulate_rolling_dca. Historical, not a forecast.",
		Annotations: readOnly("Simulate lump sum vs DCA", true),
		InputSchema: inputSchema[simulateLumpSumVsDCAInput](schemaTweaks{
			defaults: costDefaults(map[string]any{"currency": "USD", "cadence": "daily"}),
			minItems: map[string]int{"allocations": 1},
		}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in simulateLumpSumVsDCAInput) (*mcp.CallToolResult, simulateLumpSumVsDCAOutput, error) {
		out, err := d.simulateLumpSumVsDCA(ctx, in)
		return nil, out, err
	})
}

// costDefaults adds the schema defaults of costInput's fields to extra.
func costDefaults(extra map[string]any) map[string]any {
	out := map[string]any{"fee_rate": 0, "commission_fixed": 0, "reinvest_dividends": true}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// simulateLumpSumVsDCA validates the request, loads the histories and
// runs both legs.
func (d Deps) simulateLumpSumVsDCA(ctx context.Context, in simulateLumpSumVsDCAInput) (simulateLumpSumVsDCAOutput, error) {
	allocs, err := resolveAllocations(in.Symbol, in.Allocations)
	if err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}
	if t := in.TotalAmount; t <= 0 || math.IsNaN(t) || math.IsInf(t, 0) || t > maxAmount {
		return simulateLumpSumVsDCAOutput{}, fmt.Errorf("total_amount must be > 0 and at most %g, got %v", maxAmount, t)
	}
	start, err := parseDate("start", in.Start)
	if err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}
	end, err := parseOptionalDate("end", in.End)
	if err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}
	if !end.IsZero() && end.Before(start) {
		return simulateLumpSumVsDCAOutput{}, fmt.Errorf("end %s is before start %s", formatDate(end), formatDate(start))
	}
	currency, err := parseCurrency(in.Currency)
	if err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}
	cadence, err := parseCadence(in.Cadence)
	if err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}
	// The lump-sum leg pays the commission on total_amount; the DCA leg's
	// per-purchase check needs the calendar and is the engine's.
	if err := in.validate(in.TotalAmount); err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}
	plan := sim.Plan{
		Allocations: allocs,
		Amount:      in.TotalAmount,
		Currency:    currency,
		Cadence:     cadence,
		Start:       start,
		End:         end,
		FeeRate:     in.FeeRate,
		FeeFixed:    in.CommissionFixed,
		Reinvest:    in.reinvest(),
	}
	simIn, _, err := d.loadInput(ctx, plan)
	if err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}
	res, err := sim.RunLumpSumVsDCA(plan, simIn, in.TotalAmount)
	if err != nil {
		return simulateLumpSumVsDCAOutput{}, lumpSumError(err)
	}
	if err := checkFinite("result", res.LumpSum.FinalValue, res.DCA.FinalValue, res.LumpSum.Invested, res.DCA.Invested); err != nil {
		return simulateLumpSumVsDCAOutput{}, err
	}

	lump, dca := toSimResult(res.LumpSum), toSimResult(res.DCA)
	out := simulateLumpSumVsDCAOutput{
		Allocations:           toAllocationOutputs(allocs),
		TotalAmount:           in.TotalAmount,
		Currency:              currency,
		Cadence:               string(cadence),
		PerContributionAmount: round2(res.TotalAmount / float64(res.DCA.Contributions)),
		LumpSum:               lump,
		DCA:                   dca,
		Diff: diffOutput{
			FinalValue: round2(lump.FinalValue - dca.FinalValue),
			ReturnPct:  round2(lump.ReturnPct - dca.ReturnPct),
		},
		Disclaimer: Disclaimer,
	}
	if lump.AnnualizedReturnPct != nil && dca.AnnualizedReturnPct != nil {
		out.Diff.AnnualizedReturnPct = ptr(round2(*lump.AnnualizedReturnPct - *dca.AnnualizedReturnPct))
	}
	for _, n := range res.Notes {
		if strings.HasPrefix(n, "Diff.AnnualizedReturn") {
			continue // replaced below with the wire rule, which also omits ranges under a year
		}
		out.Notes = append(out.Notes, lumpSumNoteNames.Replace(n))
	}
	if out.Diff.AnnualizedReturnPct == nil {
		out.Notes = append(out.Notes, "diff.annualized_return_pct omitted: at least one leg has no annualized return (see lump_sum.notes and dca.notes)")
	}
	out.Notes = append(out.Notes, d.planStaleWarnings(plan)...)
	return out, nil
}

// lumpSumError rewrites an engine error for the model: every package
// prefix goes, and a DCA leg whose purchases the fixed commission would
// swallow says what to change.
func lumpSumError(err error) error {
	msg := strings.ReplaceAll(err.Error(), "sim: ", "")
	if strings.Contains(msg, "DCA leg") && strings.Contains(msg, "leaves nothing") {
		msg = strings.Replace(msg, "fixed fee", "commission_fixed", 1)
		msg += "; the DCA leg splits total_amount over every contribution day, so raise total_amount, choose a sparser cadence (weekly or monthly) or lower commission_fixed"
	}
	return errors.New(msg)
}
