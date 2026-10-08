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
	Baseline   *baselineOutput `json:"baseline,omitempty"`
	Diff       *diffOutput     `json:"diff,omitempty"`
	Disclaimer string          `json:"disclaimer"`
}

func registerSimulateDCA(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "simulate_dca",
		Description: "Simulates buying one ETF with a fixed amount on every trading day, week or month between start and end, at each day's close with fractional shares, and reports what the investor ended up with: contributions, invested, fees, final value, profit, simple and money-weighted (XIRR) annualized return, max drawdown of the unitised value path, final holding and a month-end timeline. With currency KRW every contribution is converted at that day's KRW=X rate and an fx block explains how the rate moved the result. The same plan is also run on compare_with (default SPY) over the same realised dates, with the difference in diff. Historical, not a forecast; read notes for any adjustment such as a moved start date.",
		Annotations: readOnly("Simulate DCA"),
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
		Baseline:        run.baseline,
		Diff:            run.diff,
		Disclaimer:      Disclaimer,
	}, nil
}
