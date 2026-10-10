package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptHorizonYears is the forecast horizon the dca_report prompt asks for.
const promptHorizonYears = 5

// promptSymbolPattern accepts the characters Yahoo symbols are made of
// (BRK-B, KRW=X, ^GSPC, BF.B), the same set the cache accepts, so the
// prompt never tells the model to call a tool with a symbol it rejects.
var promptSymbolPattern = regexp.MustCompile(`^[A-Z0-9.=^-]+$`)

// promptNumberPattern is a plain decimal number such as 5, 0.99 or 1e3:
// what a tool call can carry as a JSON number. strconv.ParseFloat also
// accepts "Inf", "NaN" and hex floats, none of which JSON can.
var promptNumberPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func registerDCAReportPrompt(s *mcp.Server, d Deps) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "dca_report",
		Title:       "DCA report",
		Description: "Runs get_etf_info, get_fund_profile, get_holdings, simulate_dca and project_dca_outcomes for one ETF and writes a short report that ends with the disclaimer. Defaults: cadence daily, start three years before today, no commissions.",
		Arguments: []*mcp.PromptArgument{
			{Name: "symbol", Description: "ticker symbol, e.g. VOO", Required: true},
			{Name: "amount", Description: "size of one contribution in the currency, e.g. 100", Required: true},
			{Name: "currency", Description: "USD or KRW (default USD)"},
			{Name: "start", Description: "first contribution date YYYY-MM-DD, not in the future (default: three years before today)"},
			{Name: "cadence", Description: "daily (every trading day), weekly or monthly (default daily)"},
			{Name: "fee_rate", Description: "fraction of each contribution lost to commissions, e.g. 0.001 for 0.1% (default 0)"},
			{Name: "commission_fixed", Description: "fixed commission per purchase in the currency, e.g. 0.99 (default 0)"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return d.dcaReport(req.Params.Arguments)
	})
}

// invalidParams is the JSON-RPC error the MCP spec prescribes for bad
// prompt arguments (-32602), so clients can classify it.
func invalidParams(msg string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: msg}
}

// promptNumber parses an optional numeric prompt argument, returning
// fallback when it is empty. Only plain decimal numbers are accepted.
func promptNumber(args map[string]string, name string, fallback float64) (float64, error) {
	text := strings.TrimSpace(args[name])
	if text == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if !promptNumberPattern.MatchString(text) || err != nil {
		return 0, fmt.Errorf("%s must be a plain decimal number such as 0.99, got %q", name, text)
	}
	return v, nil
}

// formatNumber renders a parsed number for a tool call in the prompt:
// shortest exact decimal, no exponent (1e3 becomes 1000).
func formatNumber(x float64) string {
	return strconv.FormatFloat(x, 'f', -1, 64)
}

// dcaReport validates the prompt arguments the way the tools it names
// would, so the instructions never ask for a call that must fail, and
// builds the instructions for the model.
func (d Deps) dcaReport(args map[string]string) (*mcp.GetPromptResult, error) {
	bad := func(err error) (*mcp.GetPromptResult, error) {
		return nil, invalidParams("dca_report: " + err.Error())
	}
	symbol := normalizeSymbol(args["symbol"])
	if symbol == "" {
		return nil, invalidParams("dca_report: symbol is required")
	}
	if !promptSymbolPattern.MatchString(symbol) {
		return nil, invalidParams(fmt.Sprintf("dca_report: symbol %q is not a ticker; use letters, digits and . - = ^ only", args["symbol"]))
	}
	if strings.TrimSpace(args["amount"]) == "" {
		return nil, invalidParams("dca_report: amount is required")
	}
	amount, err := promptNumber(args, "amount", 0)
	if err != nil {
		return bad(err)
	}
	if err := checkAmount(amount); err != nil {
		return bad(err)
	}
	currency, err := parseCurrency(args["currency"])
	if err != nil {
		return bad(err)
	}
	cadence, err := parseCadence(args["cadence"])
	if err != nil {
		return bad(err)
	}
	feeRate, err := promptNumber(args, "fee_rate", 0)
	if err != nil {
		return bad(err)
	}
	if err := checkFeeRate(feeRate); err != nil {
		return bad(err)
	}
	commission, err := promptNumber(args, "commission_fixed", 0)
	if err != nil {
		return bad(err)
	}
	if err := checkFixedCommission(commission, amount, feeRate, []sim.Allocation{{Symbol: symbol, Weight: 1}}); err != nil {
		return bad(err)
	}
	start, err := parseOptionalDate("start", args["start"])
	if err != nil {
		return bad(err)
	}
	today := market.Day(d.clock())
	if start.After(today) {
		return bad(fmt.Errorf("start %s is in the future; use a date on or before %s", formatDate(start), formatDate(today)))
	}
	if start.IsZero() {
		start = d.clock().AddDate(-3, 0, 0)
	}
	startText := formatDate(start)
	amountText := formatNumber(amount)
	costs := fmt.Sprintf("fee_rate %s and commission_fixed %s", formatNumber(feeRate), formatNumber(commission))

	text := fmt.Sprintf(`Write a short dollar-cost-averaging report for %[1]s: %[2]s %[3]s invested on a %[4]s cadence since %[5]s, paying %[6]s.

1. Call get_etf_info with symbol "%[1]s". Note its trailing returns, volatility, drawdowns, dividend yield and the trend state with its reasons.
2. Call get_fund_profile and get_holdings with symbol "%[1]s". Note the expense ratio and what the fund holds (top holdings, sectors, asset classes). If either fails or reports nothing, say the data is unavailable instead of guessing.
3. Call simulate_dca with symbol "%[1]s", amount %[2]s, currency "%[3]s", cadence "%[4]s", start "%[5]s", %[6]s, keeping the default SPY baseline. Report invested, fees, final value, return, annualized return, max drawdown and the difference to SPY. Mention any notes (for example a moved start date).
4. Call project_dca_outcomes with symbol "%[1]s", amount %[2]s, currency "%[3]s", cadence "%[4]s", horizon_years %[7]d, %[6]s. Report the p10, p50 and p90 final values, the probability of loss and the historical return and volatility it is based on, and repeat any warnings. Say plainly that these are bootstrapped historical outcomes, not price predictions.

Then write the report in four short sections: What it is, What the plan would have done, What the range of outcomes looks like, Caveats. Quote the dates used and the date of the latest bar. End with this disclaimer verbatim:

%[8]s`, symbol, amountText, currency, cadence, startText, costs, promptHorizonYears, Disclaimer)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("DCA report for %s (%s %s %s from %s, %s)", symbol, amountText, currency, cadence, startText, costs),
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: text},
		}},
	}, nil
}
