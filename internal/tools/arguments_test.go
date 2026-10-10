package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFriendlyArgumentError(t *testing.T) {
	const p = `validating "arguments": validating root: `
	tests := []struct {
		tool, msg, want string
	}{
		{"simulate_dca", p + `required: missing properties: ["symbol" "amount" "start"]`, "invalid arguments: missing required input: symbol, amount, start"},
		{"simulate_dca", p + `validating /properties/amount: type: 100 has type "string", want "number"`, "invalid arguments: amount must be a number, got the string 100"},
		{"simulate_dca", p + `unexpected additional properties ["allocations"]`, "invalid arguments: simulate_dca has no input named allocations; for several ETFs use simulate_portfolio_dca"},
		{"simulate_portfolio_dca", p + `unexpected additional properties ["symbol"]`, "invalid arguments: simulate_portfolio_dca has no input named symbol; for one ETF use simulate_dca, or give allocations with a single entry"},
		{"get_quote", p + `unexpected additional properties ["ticker"]`, "invalid arguments: get_quote has no input named ticker; check the input names in the tool's schema"},
		{"simulate_portfolio_dca", p + `validating /properties/allocations: minItems: array length 0 is less than 1`, "invalid arguments: allocations must list at least 1 item(s)"},
		{"simulate_portfolio_dca", p + `validating /properties/allocations: validating /properties/allocations/items: required: missing properties: ["weight"]`, "invalid arguments: each item of allocations needs weight"},
		{"project_dca_outcomes", p + `validating /properties/seed: type: 1.5 has type "number", want "integer"`, "invalid arguments: seed must be a whole number, got the number 1.5"},
		{"project_dca_outcomes", p + `validating /properties/seed: minimum: -3/1 is less than 0.000000`, "invalid arguments: seed must be at least 0"},
		{"project_dca_outcomes", p + `validating /properties/seed: maximum: 18446744073709551616/1 is greater than 9007199254740991.000000`, "invalid arguments: seed must be at most 9007199254740991"},
		{"get_quote", p + `validating /properties/symbols: type: VOO has type "string", want "array"`, "invalid arguments: symbols must be a list, got the string VOO"},
		{"screen_universe", p + `validating /properties/descending: type: yes has type "string", want one of "null, boolean"`, "invalid arguments: descending must be true or false, got the string yes"},
		// The exact text go-sdk v1.8.0 passes on for an integer too large for
		// int (captured from a real session).
		{"project_dca_outcomes", `json: cannot unmarshal number 100000000000000000000,"symbol":"... overflows into Go struct field tools.projectDCAInput.simulations of type int`, "invalid arguments: simulations is too large, got 100000000000000000000"},
		{"simulate_portfolio_dca", p + `validating /properties/allocations: validating /properties/allocations/items: unexpected additional properties ["pct"]`, "invalid arguments: each item of allocations has no field named pct; an allocation takes symbol and weight"},
		{"simulate_dca", p + `validating /properties/amount: type: map[x:1] has type "object", want "number"`, "invalid arguments: amount must be a number, got an object"},
		{"simulate_portfolio_dca", p + `validating /properties/allocations: type: <invalid reflect.Value> has type "null", want "array"`, "invalid arguments: allocations must be a list, got null"},
	}
	for _, tt := range tests {
		got, ok := friendlyArgumentError(tt.tool, tt.msg)
		if !ok || got != tt.want {
			t.Errorf("friendlyArgumentError(%q)\n got %q, %v\nwant %q", tt.msg, got, ok, tt.want)
		}
	}
	for _, msg := range []string{
		"symbol is required; use list_etfs to find one",
		p + "someNewRule: x",
		// An upstream payload problem is not the caller's mistake.
		"fetching SPY failed: decode chart: json: cannot unmarshal number 728317800.5 into Go struct field chartResponse.chart.result.0.meta.firstTradeDate of type int64",
	} {
		if got, ok := friendlyArgumentError("x", msg); ok {
			t.Errorf("unknown message %q was rewritten to %q", msg, got)
		}
	}
}

// TestArgumentErrorsReachTheClientRewritten checks the middleware on a
// real session: the model sees the friendly text, not the SDK's.
func TestArgumentErrorsReachTheClientRewritten(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "simulate_dca", Arguments: map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-01-03", "allocations": []any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || textOf(res) != "invalid arguments: simulate_dca has no input named allocations; for several ETFs use simulate_portfolio_dca" {
		t.Errorf("result = %v %q", res.IsError, textOf(res))
	}
	// Errors from the tools themselves are untouched.
	callErr(t, sess, "simulate_dca", map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-13-01"}, "use YYYY-MM-DD")
}

// TestRealSDKDecodeErrorAndRenamedTool goes through a real session: an
// out-of-range integer reaches the model as a field-level message, and the
// old forecast_dca name points at its replacement.
func TestRealSDKDecodeErrorAndRenamedTool(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "project_dca_outcomes", Arguments: json.RawMessage(`{"symbol":"VOO","amount":100,"horizon_years":1,"simulations":1e20}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); !res.IsError || got != "invalid arguments: simulations is too large, got 100000000000000000000" {
		t.Errorf("overflow = %v %q", res.IsError, got)
	}
	res, err = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "forecast_dca", Arguments: map[string]any{"symbol": "VOO"}})
	if err != nil {
		t.Fatalf("forecast_dca should be a tool error, not %v", err)
	}
	if got := textOf(res); !res.IsError || !strings.Contains(got, "renamed project_dca_outcomes") {
		t.Errorf("forecast_dca = %v %q", res.IsError, got)
	}
}
