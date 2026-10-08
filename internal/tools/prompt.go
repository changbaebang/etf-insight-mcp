package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptCadence is the cadence the dca_report prompt asks for. Monthly
// reads naturally in a report even though the tools default to daily.
const promptCadence = "monthly"

// promptHorizonYears is the forecast horizon the dca_report prompt asks for.
const promptHorizonYears = 5

func registerDCAReportPrompt(s *mcp.Server, d Deps) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "dca_report",
		Title:       "DCA report",
		Description: "Runs get_etf_info, simulate_dca and forecast_dca for one ETF and writes a short report that ends with the disclaimer.",
		Arguments: []*mcp.PromptArgument{
			{Name: "symbol", Description: "ticker symbol, e.g. VOO", Required: true},
			{Name: "amount", Description: "size of one contribution in the currency, e.g. 100", Required: true},
			{Name: "currency", Description: "USD or KRW (default USD)"},
			{Name: "start", Description: "first contribution date YYYY-MM-DD (default: three years before today)"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return d.dcaReport(req.Params.Arguments)
	})
}

// dcaReport validates the prompt arguments and builds the instructions
// for the model.
func (d Deps) dcaReport(args map[string]string) (*mcp.GetPromptResult, error) {
	symbol := normalizeSymbol(args["symbol"])
	if symbol == "" {
		return nil, errors.New("dca_report: symbol is required")
	}
	amountText := strings.TrimSpace(args["amount"])
	amount, err := strconv.ParseFloat(amountText, 64)
	if err != nil || amount <= 0 {
		return nil, fmt.Errorf("dca_report: amount must be a positive number, got %q", amountText)
	}
	currency, err := parseCurrency(args["currency"])
	if err != nil {
		return nil, fmt.Errorf("dca_report: %w", err)
	}
	start, err := parseOptionalDate("start", args["start"])
	if err != nil {
		return nil, fmt.Errorf("dca_report: %w", err)
	}
	if start.IsZero() {
		start = d.clock().AddDate(-3, 0, 0)
	}
	startText := formatDate(start)

	text := fmt.Sprintf(`Write a short dollar-cost-averaging report for %[1]s: %[2]s %[3]s invested %[4]s since %[5]s.

1. Call get_etf_info with symbol "%[1]s". Note what the fund holds, its trailing returns, volatility, drawdowns, dividend yield and the trend state with its reasons.
2. Call simulate_dca with symbol "%[1]s", amount %[2]s, currency "%[3]s", cadence "%[4]s" and start "%[5]s", keeping the default SPY baseline. Report invested, final value, return, annualized return, max drawdown and the difference to SPY. Mention any notes (for example a moved start date).
3. Call forecast_dca with symbol "%[1]s", amount %[2]s, currency "%[3]s", cadence "%[4]s" and horizon_years %[6]d. Report the p10, p50 and p90 final values, the probability of loss and the historical return and volatility it is based on. Say plainly that these are bootstrapped historical outcomes, not price predictions.

Then write the report in four short sections: What it is, What the plan would have done, What the range of outcomes looks like, Caveats. Quote the dates used and the date of the latest bar. End with this disclaimer verbatim:

%[7]s`, symbol, amountText, currency, promptCadence, startText, promptHorizonYears, Disclaimer)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("DCA report for %s (%s %s %s from %s)", symbol, amountText, currency, promptCadence, startText),
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: text},
		}},
	}, nil
}
