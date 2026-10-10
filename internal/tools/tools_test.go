package tools

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolNames is every tool Register must expose.
var toolNames = []string{
	"ping", "list_etfs", "get_etf_info", "get_price_history", "simulate_dca", "simulate_portfolio_dca", "forecast_dca",
	// data
	"search_symbols", "get_quote", "get_dividends", "get_splits", "get_fund_profile", "get_holdings", "get_fund_performance", "get_news", "market_overview",
	// analysis
	"get_technical_indicators", "compare_etfs", "screen_universe", "find_alternatives",
	// simulations
	"simulate_lump_sum_vs_dca", "simulate_rolling_dca", "review_dca_plan",
}
// mutatingTools change the local cache, so they must not claim to be
// read-only; clear_cache also deletes files.
var mutatingTools = map[string]bool{"clear_cache": true, "refresh_prices": true}

func TestToolsAreListedWithDescriptions(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range toolNames {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("tool %s is not listed", name)
			continue
		}
		if tool.Description == "" {
			t.Errorf("tool %s has no description", name)
		}
		switch {
		case tool.Annotations == nil:
			t.Errorf("tool %s has no annotations", name)
		case mutatingTools[name] && tool.Annotations.ReadOnlyHint:
			t.Errorf("tool %s changes the cache but is marked read-only", name)
		case !mutatingTools[name] && !tool.Annotations.ReadOnlyHint:
			t.Errorf("tool %s is not marked read-only", name)
		}
		if name == "clear_cache" && (tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Error("clear_cache must be marked destructive")
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Errorf("tool %s input schema is %T, want object", name, tool.InputSchema)
			continue
		}
		checkDescribed(t, name, "", schema)
	}
	if len(byName) != len(toolNames) {
		t.Errorf("%d tools listed, want %d", len(byName), len(toolNames))
	}
}

// checkDescribed walks a JSON schema and fails for every property, at
// any depth, that has no description: the model relies on them.
func checkDescribed(t *testing.T, tool, path string, schema map[string]any) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("%s: property %s%s is %T", tool, path, name, raw)
			continue
		}
		if d, _ := p["description"].(string); d == "" {
			t.Errorf("%s: input %s%s has no description", tool, path, name)
		}
		if items, ok := p["items"].(map[string]any); ok {
			checkDescribed(t, tool, path+name+"[].", items)
		}
		checkDescribed(t, tool, path+name+".", p)
	}
}

func TestPing(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "default", args: map[string]any{}, want: "pong"},
		{name: "echo", args: map[string]any{"message": "hello"}, want: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out pingOutput
			callOK(t, sess, "ping", tt.args, &out)
			if out.Reply != tt.want {
				t.Errorf("reply = %q, want %q", out.Reply, tt.want)
			}
			if out.Version != "test" {
				t.Errorf("version = %q, want test", out.Version)
			}
		})
	}
}

func TestDescribeFetchError(t *testing.T) {
	tests := []struct {
		name string
		sym  string
		err  error
		want string
	}{
		{name: "unknown", sym: "XYZ", err: market.ErrNotFound, want: "unknown symbol XYZ: not in universe and the data source returned not found; use list_etfs"},
		{name: "known but missing upstream", sym: "VTI", err: market.ErrNotFound, want: "in the universe but the data source returned not found"},
		{name: "invalid", sym: "SP Y", err: cache.ErrInvalidSymbol, want: "invalid symbol"},
		{name: "other", sym: "SPY", err: errors.New("boom"), want: "fetching SPY failed: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeFetchError(tt.sym, tt.err)
			if !strings.Contains(got.Error(), tt.want) {
				t.Errorf("describeFetchError = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestRegisterRequiresSource(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register with a nil Source did not panic")
		}
	}()
	Register(mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil), Deps{})
}

func TestUniverseResource(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	ctx := context.Background()

	list, err := sess.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	found := false
	for _, r := range list.Resources {
		if r.URI == UniverseURI {
			found = true
			if r.MIMEType != "text/csv" || r.Description == "" {
				t.Errorf("resource = %+v, want text/csv with a description", r)
			}
		}
	}
	if !found {
		t.Fatalf("%s is not listed in %+v", UniverseURI, list.Resources)
	}

	res, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: UniverseURI})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d items, want 1", len(res.Contents))
	}
	text := res.Contents[0].Text
	for _, want := range []string{"symbol,name,issuer,category,leveraged,note", "\nVOO,", "\nSPY,"} {
		if !strings.Contains(text, want) {
			t.Errorf("resource text lacks %q", want)
		}
	}
}

func TestDCAReportPrompt(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	ctx := context.Background()

	list, err := sess.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(list.Prompts) != 1 || list.Prompts[0].Name != "dca_report" || len(list.Prompts[0].Arguments) != 4 {
		t.Fatalf("prompts = %+v, want one dca_report with 4 arguments", list.Prompts)
	}

	res, err := sess.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "dca_report",
		Arguments: map[string]string{"symbol": "voo", "amount": "100"},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want one user message", res.Messages)
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	wantStart := now.AddDate(-3, 0, 0).Format(market.DateLayout)
	for _, want := range []string{"get_etf_info", "simulate_dca", "forecast_dca", `"VOO"`, "amount 100", `"USD"`, wantStart, "horizon_years 5", Disclaimer} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt text lacks %q", want)
		}
	}

	bad := []struct {
		name string
		args map[string]string
	}{
		{name: "missing symbol", args: map[string]string{"amount": "100"}},
		{name: "bad amount", args: map[string]string{"symbol": "VOO", "amount": "lots"}},
		{name: "bad currency", args: map[string]string{"symbol": "VOO", "amount": "100", "currency": "EUR"}},
		{name: "bad start", args: map[string]string{"symbol": "VOO", "amount": "100", "start": "yesterday"}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := sess.GetPrompt(ctx, &mcp.GetPromptParams{Name: "dca_report", Arguments: tt.args}); err == nil {
				t.Errorf("get prompt with %v succeeded, want error", tt.args)
			}
		})
	}
}

func TestInstructionsMentionTheEssentials(t *testing.T) {
	for _, want := range append(slices.Clone(toolNames[1:]), "Yahoo Finance", "KRW=X", "etf://universe", "dca_report", Disclaimer) {
		if !strings.Contains(Instructions, want) {
			t.Errorf("Instructions lack %q", want)
		}
	}
}

func TestHelpers(t *testing.T) {
	if got := round2(1.005); got != 1.01 && got != 1 { // float rounding either way is fine, but it must be 2 decimals
		t.Errorf("round2(1.005) = %v", got)
	}
	if got := round4(0.123456); got != 0.1235 {
		t.Errorf("round4(0.123456) = %v, want 0.1235", got)
	}
	if got := pct(0.075); got != 7.5 {
		t.Errorf("pct(0.075) = %v, want 7.5", got)
	}
	if got := formatDate(time.Time{}); got != "" {
		t.Errorf("formatDate(zero) = %q, want empty", got)
	}
	if got := uniqueSymbols([]string{" voo", "VOO", "", "spy", "voo"}); !slices.Equal(got, []string{"VOO", "SPY"}) {
		t.Errorf("uniqueSymbols = %v, want [VOO SPY]", got)
	}
	if _, err := parseDate("start", "2021-02-30"); err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
		t.Errorf("parseDate(2021-02-30) error = %v, want format hint", err)
	}
	if _, err := parseDate("start", ""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("parseDate(empty) error = %v, want required", err)
	}
	if got := userError(errors.New("sim: amount must be > 0")); got.Error() != "amount must be > 0" {
		t.Errorf("userError = %q", got)
	}
}
