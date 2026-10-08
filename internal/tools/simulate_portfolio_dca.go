package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type simulatePortfolioDCAInput struct {
	Allocations []allocationInput `json:"allocations" jsonschema:"the portfolio as a list of {symbol, weight}; weights are fractions summing to 1 or percentages summing to 100 and are normalised, e.g. [{VOO, 60}, {SCHD, 40}]"`
	planInput
}

type simulatePortfolioDCAOutput struct {
	Allocations []allocationOutput `json:"allocations"`
	Amount      float64            `json:"amount"`
	Currency    string             `json:"currency"`
	Cadence     string             `json:"cadence"`
	simResultOutput
	Baseline   *baselineOutput `json:"baseline,omitempty"`
	Diff       *diffOutput     `json:"diff,omitempty"`
	Disclaimer string          `json:"disclaimer"`
}

func registerSimulatePortfolioDCA(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "simulate_portfolio_dca",
		Description: "Like simulate_dca for a portfolio: each contribution is split across several ETFs by weight (fractions summing to 1 or percentages summing to 100; they are normalised, so 60/40 and 0.6/0.4 mean the same). Purchases happen only on days every symbol traded, the history is cut to the symbols' common range (see notes), and there is no rebalancing. Reports the same figures as simulate_dca plus a holding per symbol, and compares with compare_with (default SPY) over the same realised dates. Historical, not a forecast.",
		Annotations: readOnly("Simulate portfolio DCA"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in simulatePortfolioDCAInput) (*mcp.CallToolResult, simulatePortfolioDCAOutput, error) {
		out, err := d.simulatePortfolioDCA(ctx, in)
		return nil, out, err
	})
}

// simulatePortfolioDCA runs a multi-symbol plan.
func (d Deps) simulatePortfolioDCA(ctx context.Context, in simulatePortfolioDCAInput) (simulatePortfolioDCAOutput, error) {
	allocs, err := parseAllocations(in.Allocations)
	if err != nil {
		return simulatePortfolioDCAOutput{}, err
	}
	plan, baseline, err := in.toPlan(allocs)
	if err != nil {
		return simulatePortfolioDCAOutput{}, err
	}
	run, err := d.simulate(ctx, plan, baseline)
	if err != nil {
		return simulatePortfolioDCAOutput{}, err
	}
	return simulatePortfolioDCAOutput{
		Allocations:     toAllocationOutputs(allocs),
		Amount:          plan.Amount,
		Currency:        plan.Currency,
		Cadence:         string(plan.Cadence),
		simResultOutput: run.result,
		Baseline:        run.baseline,
		Diff:            run.diff,
		Disclaimer:      Disclaimer,
	}, nil
}
