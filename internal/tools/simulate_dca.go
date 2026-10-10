package tools

import (
	"context"
	"errors"

	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type simulateDCAInput struct {
	Symbol string `json:"symbol" jsonschema:"ticker symbol to buy, e.g. VOO"`
	planInput
}

type simulateDCAOutput struct {
	Symbol   string  `json:"symbol"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Cadence  string  `json:"cadence"`
	simResultOutput
	Comparison *comparisonOutput `json:"comparison,omitempty" jsonschema:"the plan and compare_with run on exactly the same days; absent when the baseline was skipped (see notes)"`
	Disclaimer string            `json:"disclaimer"`
}

func registerSimulateDCA(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "simulate_dca",
		Title:       "Simulate DCA",
		Description: "Simulates buying ONE ETF with a fixed amount on every trading day, week or month between start and end, at each day's close with fractional shares, and reports what the investor ended up with: contributions, invested, fees (fee_rate and commission_fixed) and their share of the outlay, final value, profit, simple return, money-weighted (XIRR) annualized return (omitted for ranges under a year), max drawdown of the unitised value path, final holding (real share count) and a month-end timeline (quarter- or year-ends beyond 120 months, see timeline_step). The symbol must be quoted in USD (a US listing); an index such as ^GSPC is accepted with a note that it is not an investable fund. With currency KRW every contribution is converted at that day's KRW=X rate and an fx block explains how the rate moved the result: fx_effect is the part of the final value due to the rate moving after the dollars were bought, measured against avg_purchase_rate. The comparison block runs the plan and compare_with (default SPY) on exactly the same days and gives their difference. For several ETFs use simulate_portfolio_dca. Historical, not a forecast; read notes for any adjustment such as a moved start date.",
		Annotations: readOnly("Simulate DCA", true),
		InputSchema: inputSchema[simulateDCAInput](planTweaks(nil)),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in simulateDCAInput) (*mcp.CallToolResult, simulateDCAOutput, error) {
		out, err := d.simulateDCA(ctx, in)
		return nil, out, err
	})
}

// simulateDCA runs a single-symbol plan.
func (d Deps) simulateDCA(ctx context.Context, in simulateDCAInput) (simulateDCAOutput, error) {
	sym := normalizeSymbol(in.Symbol)
	if sym == "" {
		return simulateDCAOutput{}, errors.New("symbol is required; use list_etfs to find one")
	}
	plan, baseline, err := in.toPlan([]sim.Allocation{{Symbol: sym, Weight: 1}})
	if err != nil {
		return simulateDCAOutput{}, err
	}
	run, err := d.simulate(ctx, plan, baseline)
	if err != nil {
		return simulateDCAOutput{}, err
	}
	return simulateDCAOutput{
		Symbol:          sym,
		Amount:          plan.Amount,
		Currency:        plan.Currency,
		Cadence:         string(plan.Cadence),
		simResultOutput: run.result,
		Comparison:      run.comparison,
		Disclaimer:      Disclaimer,
	}, nil
}
