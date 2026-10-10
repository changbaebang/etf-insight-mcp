package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callJSON invokes a tool, requires success and returns the structured
// content as a generic map, so a test can look at fields the typed output
// does not have, or check that a field is absent.
func callJSON(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, sess, name, args)
	if res.IsError {
		t.Fatalf("%s(%v) returned tool error: %s", name, args, textOf(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s output: %v", name, err)
	}
	return out
}

// field walks a decoded JSON object along keys and returns what it finds,
// or nil when a key is missing.
func field(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[k]
	}
	return cur
}

// stringsOf returns a decoded JSON array of strings, or nil.
func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// newToolsplanSource is newSimulationsSource plus a fund quoted in KRW,
// an index quoted in USD, and HOLE, a copy of SPY without 2022-03-01: the
// first trading day of March 2022, a contribution day of every monthly
// plan that year.
func newToolsplanSource(t *testing.T) *fakeSource {
	t.Helper()
	src := newSimulationsSource()
	src.add(synthetic("KRFUND", "KRW", "ETF", seriesStart, 780, func(i int) float64 { return 10000 + 5*float64(i) }, 0, 0))
	src.add(synthetic("^IDX", "USD", "INDEX", seriesStart, 780, func(i int) float64 { return 4000 * math.Exp(0.0003*float64(i)) }, 0, 0))
	spy := src.series["SPY"]
	hole := &market.Series{Meta: spy.Meta}
	hole.Meta.Symbol = "HOLE"
	for _, b := range spy.Bars {
		if !b.Date.Equal(date(t, "2022-03-01")) {
			hole.Bars = append(hole.Bars, b)
		}
	}
	src.add(hole)
	return src
}

func TestFix2ToolsplanRejectsFundsNotQuotedInUSD(t *testing.T) {
	src := newToolsplanSource(t)
	deps := testDeps(src)
	deps.Fund = newSimFundSource()
	sess := newSession(t, deps)

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"simulate_dca", map[string]any{"symbol": "KRFUND", "amount": 10000, "currency": "KRW", "start": "2022-01-03"}},
		{"simulate_dca", map[string]any{"symbol": "KRFUND", "amount": 100, "start": "2022-01-03"}},
		{"simulate_portfolio_dca", map[string]any{"allocations": []map[string]any{{"symbol": "VOO", "weight": 50}, {"symbol": "KRFUND", "weight": 50}}, "amount": 100, "start": "2022-01-03"}},
		{"simulate_lump_sum_vs_dca", map[string]any{"symbol": "KRFUND", "total_amount": 1000, "start": "2022-01-03"}},
		{"simulate_rolling_dca", map[string]any{"symbol": "KRFUND", "amount": 100, "duration_years": 1}},
		{"forecast_dca", map[string]any{"symbol": "KRFUND", "amount": 100, "horizon_years": 1, "simulations": 50}},
		{"review_dca_plan", map[string]any{"symbol": "KRFUND", "amount": 5, "horizon_years": 1}},
	}
	for _, c := range calls {
		t.Run(c.tool, func(t *testing.T) {
			callErr(t, sess, c.tool, c.args, "KRFUND is quoted in KRW")
		})
	}

	t.Run("a non-USD baseline is skipped with a note", func(t *testing.T) {
		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-01-03", "compare_with": "KRFUND"}, &out)
		if out.Comparison != nil || !hasNote(out.Notes, "baseline KRFUND not computed: KRFUND is quoted in KRW") {
			t.Errorf("comparison/notes = %+v/%v", out.Comparison, out.Notes)
		}
	})

	t.Run("an index used as a fund is flagged", func(t *testing.T) {
		var simOut simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "^IDX", "amount": 100, "start": "2022-01-03", "compare_with": ""}, &simOut)
		if !hasNote(simOut.Notes, "^IDX is an index, not an investable fund") {
			t.Errorf("simulate_dca notes = %v", simOut.Notes)
		}
		var fc forecastDCAOutput
		callOK(t, sess, "forecast_dca", map[string]any{"symbol": "^IDX", "amount": 100, "horizon_years": 1, "simulations": 50}, &fc)
		if !hasNote(fc.Warnings, "^IDX is an index, not an investable fund") {
			t.Errorf("forecast_dca warnings = %v", fc.Warnings)
		}
	})
}

func TestFix2ToolsplanForecastChargesCommissionFixed(t *testing.T) {
	src := newFakeSource()
	sess := newSession(t, testDeps(src))

	var out forecastDCAOutput
	callOK(t, sess, "forecast_dca", map[string]any{"symbol": "VOO", "amount": 5, "horizon_years": 2, "simulations": 200, "commission_fixed": 0.99}, &out)
	ref, err := analytics.MonteCarlo(analytics.MCPlan{
		Symbols: []string{"VOO"}, Weights: []float64{1}, Amount: 5, Currency: "USD", Cadence: analytics.CadenceDaily, HorizonYears: 2, FeeRate: 0.99 / 5,
	}, analytics.MCConfig{Simulations: 200}, analytics.MCInput{Series: src.series})
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	for _, key := range percentileKeys {
		if out.Percentiles[key] != round2(ref.Percentiles[key]) {
			t.Errorf("%s = %v, want %v (commission folded into the fee rate)", key, out.Percentiles[key], round2(ref.Percentiles[key]))
		}
	}
	if !hasNote(out.Assumptions, "commission_fixed") {
		t.Errorf("assumptions = %v, want the commission explained", out.Assumptions)
	}

	t.Run("a portfolio pays it once per ETF", func(t *testing.T) {
		var pf forecastDCAOutput
		callOK(t, sess, "forecast_dca", map[string]any{
			"allocations": []map[string]any{{"symbol": "VOO", "weight": 50}, {"symbol": "SPY", "weight": 50}},
			"amount":      10, "horizon_years": 1, "simulations": 100, "commission_fixed": 0.5, "fee_rate": 0.01,
		}, &pf)
		ref, err := analytics.MonteCarlo(analytics.MCPlan{
			Symbols: []string{"VOO", "SPY"}, Weights: []float64{0.5, 0.5}, Amount: 10, Currency: "USD", Cadence: analytics.CadenceDaily, HorizonYears: 1, FeeRate: 0.01 + 2*0.5/10,
		}, analytics.MCConfig{Simulations: 100}, analytics.MCInput{Series: src.series})
		if err != nil {
			t.Fatalf("MonteCarlo: %v", err)
		}
		if pf.Percentiles["p50"] != round2(ref.Percentiles["p50"]) {
			t.Errorf("p50 = %v, want %v", pf.Percentiles["p50"], round2(ref.Percentiles["p50"]))
		}
	})

	callErr(t, sess, "forecast_dca", map[string]any{
		"allocations": []map[string]any{{"symbol": "VOO", "weight": 90}, {"symbol": "SPY", "weight": 10}},
		"amount":      10, "horizon_years": 1, "commission_fixed": 1,
	}, "SPY")
	callErr(t, sess, "forecast_dca", map[string]any{"symbol": "VOO", "amount": 1, "horizon_years": 1, "commission_fixed": 1}, "commission_fixed 1 leaves nothing")
}

func TestFix2ToolsplanShortResampledHistoryIsWarned(t *testing.T) {
	src := newSimulationsSource()
	deps := testDeps(src)
	deps.Fund = newSimFundSource()
	sess := newSession(t, deps)

	// VOO has three years of history: shorter than a five-year horizon.
	var short forecastDCAOutput
	callOK(t, sess, "forecast_dca", map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 5, "simulations": 50}, &short)
	if !hasNote(short.Warnings, "resamples only") {
		t.Errorf("warnings = %v, want the short history flagged", short.Warnings)
	}
	// LONGRUN has twelve years, but lookback_years cuts it to two.
	var cut forecastDCAOutput
	callOK(t, sess, "forecast_dca", map[string]any{"symbol": "LONGRUN", "amount": 100, "horizon_years": 1, "lookback_years": 2, "simulations": 50}, &cut)
	if !hasNote(cut.Warnings, "resamples only") {
		t.Errorf("warnings = %v, want a two-year lookback flagged", cut.Warnings)
	}
	var long forecastDCAOutput
	callOK(t, sess, "forecast_dca", map[string]any{"symbol": "LONGRUN", "amount": 100, "horizon_years": 5, "simulations": 50}, &long)
	if hasNote(long.Warnings, "resamples only") {
		t.Errorf("warnings = %v, want none for twelve years of history and a five-year horizon", long.Warnings)
	}

	var review reviewDCAPlanOutput
	callOK(t, sess, "review_dca_plan", map[string]any{"symbol": "VOO", "amount": 5, "horizon_years": 5}, &review)
	if !hasNote(review.Warnings, "resamples only") || !hasNote(review.Observations, "resampled from only") {
		t.Errorf("review warnings = %v, observations = %v", review.Warnings, review.Observations)
	}
}

func TestFix2ToolsplanSameDaysAsPlanComparesTheDays(t *testing.T) {
	src := newToolsplanSource(t)
	sess := newSession(t, testDeps(src))
	var out simulateDCAOutput
	callOK(t, sess, "simulate_dca", map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "start": "2022-01-01", "end": "2022-12-31", "compare_with": "HOLE"}, &out)
	cmp := out.Comparison
	if cmp == nil {
		t.Fatalf("comparison missing; notes %v", out.Notes)
	}
	// Same range and count, but the March purchase moved to 2022-03-02.
	if cmp.Contributions != out.Contributions || cmp.Start != out.Start || cmp.End != out.End {
		t.Fatalf("comparison range %s..%s (%d) differs from the plan's %s..%s (%d); the test needs them equal", cmp.Start, cmp.End, cmp.Contributions, out.Start, out.End, out.Contributions)
	}
	if cmp.Plan.FinalValue == out.FinalValue {
		t.Fatalf("comparison.plan final value equals the main result; the moved purchase should change it")
	}
	if cmp.SameDaysAsPlan || cmp.Note == "" {
		t.Errorf("same_days_as_plan = %v, note %q: the March purchase moved, so comparison.plan is not the main result", cmp.SameDaysAsPlan, cmp.Note)
	}
}

func TestFix2ToolsplanTimelineIsCapped(t *testing.T) {
	src := newSimulationsSource()
	sess := newSession(t, testDeps(src))
	out := callJSON(t, sess, "simulate_dca", map[string]any{"symbol": "LONGRUN", "amount": 100, "cadence": "monthly", "start": "2012-01-02", "compare_with": ""})
	timeline, _ := out["timeline"].([]any)
	if len(timeline) == 0 || len(timeline) > maxTimelinePointsForTest {
		t.Fatalf("timeline has %d points, want 1 to %d", len(timeline), maxTimelinePointsForTest)
	}
	if step := field(out, "timeline_step"); step != "quarter" {
		t.Errorf("timeline_step = %v, want quarter for twelve years", step)
	}
	last, _ := timeline[len(timeline)-1].(map[string]any)
	if last["date"] != out["end"] || last["value"] != out["final_value"] {
		t.Errorf("last point = %v, want the end %v with the final value %v", last, out["end"], out["final_value"])
	}

	lump := callJSON(t, sess, "simulate_lump_sum_vs_dca", map[string]any{"symbol": "LONGRUN", "total_amount": 12000, "cadence": "monthly", "start": "2012-01-02"})
	for _, leg := range []string{"lump_sum", "dca"} {
		if pts, _ := field(lump, leg, "timeline").([]any); len(pts) > maxTimelinePointsForTest {
			t.Errorf("%s timeline has %d points", leg, len(pts))
		}
	}
}

// maxTimelinePointsForTest is the cap the descriptions promise.
const maxTimelinePointsForTest = 120

func TestFix2ToolsplanDCAReportPrompt(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	ctx := context.Background()
	get := func(args map[string]string) (string, error) {
		res, err := sess.GetPrompt(ctx, &mcp.GetPromptParams{Name: "dca_report", Arguments: args})
		if err != nil {
			return "", err
		}
		return res.Messages[0].Content.(*mcp.TextContent).Text, nil
	}

	text, err := get(map[string]string{"symbol": "VOO", "amount": "5"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`cadence "daily"`, "fee_rate 0", "commission_fixed 0", "get_holdings", "get_fund_profile"} {
		if !strings.Contains(text, want) {
			t.Errorf("default prompt lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "monthly") {
		t.Errorf("default prompt still asks for a monthly plan:\n%s", text)
	}
	text, err = get(map[string]string{"symbol": "VOO", "amount": "5", "cadence": "Weekly", "fee_rate": "0.001", "commission_fixed": "0.99"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`cadence "weekly"`, "fee_rate 0.001", "commission_fixed 0.99"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt lacks %q:\n%s", want, text)
		}
	}

	bad := []map[string]string{
		{"symbol": "VOO", "amount": "NaN"},
		{"symbol": "VOO", "amount": "Inf"},
		{"symbol": "VOO", "amount": "infinity"},
		{"symbol": "VOO", "amount": "0x1p4"},
		{"symbol": "VOO", "amount": "5e12"},
		{"symbol": "bad symbol!", "amount": "100"},
		{"symbol": "VOO", "amount": "100", "start": "2030-01-01"},
		{"symbol": "VOO", "amount": "100", "cadence": "once"},
		{"symbol": "VOO", "amount": "5", "commission_fixed": "5"},
		{"symbol": "VOO", "amount": "5", "fee_rate": "1"},
	}
	for _, args := range bad {
		if _, err := get(args); err == nil {
			t.Errorf("dca_report %v succeeded, want invalid params", args)
		}
	}
}

func TestFix2ToolsplanReviewProjectsOnTheNetAmount(t *testing.T) {
	src := newSimulationsSource()
	deps := testDeps(src)
	deps.Fund = newSimFundSource()
	sess := newSession(t, deps)
	var out reviewDCAPlanOutput
	callOK(t, sess, "review_dca_plan", reviewArgs(map[string]any{"horizon_years": 1}), &out)

	// 5 USD a day with 0.99 commission: 1260 contributed, 249.48 of it to
	// commissions, so 1010.52 buys shares.
	const net = 1260 - 249.48
	// The yield is shown rounded to 0.01 percentage points, which moves
	// yield × 1010.52 by up to 0.051.
	if diff := out.History.ProjectedAnnualDividendsAtHorizon - out.History.TTMDividendYieldPct/100*net; math.Abs(diff) > 0.06 {
		t.Errorf("projected dividends %v, want about yield %v%% × %v (net of commissions)", out.History.ProjectedAnnualDividendsAtHorizon, out.History.TTMDividendYieldPct, net)
	}
	if c := out.Costs.ExpenseCostPerYearOnProjectedHoldings; c == nil || *c != round2(0.0003*net/2) {
		t.Errorf("expense cost = %v, want %v", c, round2(0.0003*net/2))
	}
}

func TestFix2ToolsplanReviewSeparatesCommissionsFromTheFund(t *testing.T) {
	src := newSimulationsSource()
	deps := testDeps(src)
	deps.Fund = newSimFundSource()
	sess := newSession(t, deps)

	out := callJSON(t, sess, "review_dca_plan", reviewArgs(map[string]any{"horizon_years": 1}))
	before, ok := field(out, "short_term", "rolling", "before_commissions").(map[string]any)
	if !ok {
		t.Fatalf("short_term.rolling.before_commissions missing: %v", field(out, "short_term", "rolling"))
	}
	withPL, _ := field(out, "short_term", "rolling", "prob_loss_pct").(float64)
	if pl, _ := before["prob_loss_pct"].(float64); pl > withPL {
		t.Errorf("prob loss before commissions %v exceeds the %v with them", pl, withPL)
	}
	summary, _ := field(out, "short_term", "rolling", "summary").(string)
	if !strings.Contains(summary, "before commissions") || !strings.Contains(summary, "19.8% of every purchase") {
		t.Errorf("summary = %q, want the commission-free figure and the commission share", summary)
	}
	if !hasNote(stringsOf(out["observations"]), "before commissions") {
		t.Errorf("observations do not separate commissions: %v", out["observations"])
	}

	free := callJSON(t, sess, "review_dca_plan", map[string]any{"symbol": "VOO", "amount": 5, "horizon_years": 1})
	if b := field(free, "short_term", "rolling", "before_commissions"); b != nil {
		t.Errorf("before_commissions = %v without any commission, want it omitted", b)
	}
}

func TestFix2ToolsplanLumpSumDiffHasNoAnnualizedRate(t *testing.T) {
	sess := newSession(t, testDeps(newSimulationsSource()))
	// RISE compounds at a constant rate, so both legs' money-weighted rates
	// coincide while the lump sum ends with more: a rate difference of 0
	// would contradict "positive means the lump sum did better".
	out := callJSON(t, sess, "simulate_lump_sum_vs_dca", map[string]any{"symbol": "RISE", "total_amount": 12000, "cadence": "monthly", "start": "2021-01-04", "end": "2023-12-29"})
	diff, _ := out["diff"].(map[string]any)
	if fv, _ := diff["final_value"].(float64); fv <= 0 {
		t.Fatalf("diff.final_value = %v, want the lump sum ahead", diff["final_value"])
	}
	if a, present := diff["annualized_return_pct"]; present {
		t.Errorf("diff.annualized_return_pct = %v, want it absent: money-weighted rates of the two legs are not comparable", a)
	}
	notes := stringsOf(out["notes"])
	if !hasNote(notes, "money-weighted") || !hasNote(notes, "max_drawdown_pct") {
		t.Errorf("notes = %v, want the annualized and drawdown caveats", notes)
	}
}

// toolsplanFlakySource fails every request for one symbol while down is set.
type toolsplanFlakySource struct {
	*fakeSource
	symbol string
	down   atomic.Bool
}

func (f *toolsplanFlakySource) Series(ctx context.Context, symbol string) (*market.Series, error) {
	if f.down.Load() && strings.EqualFold(strings.TrimSpace(symbol), f.symbol) {
		return nil, errors.New("toolsplan fake: upstream down")
	}
	return f.fakeSource.Series(ctx, symbol)
}

func TestFix2ToolsplanStaleBaselineIsWarned(t *testing.T) {
	flaky := &toolsplanFlakySource{fakeSource: newFakeSource(), symbol: "SPY"}
	clock := time.Now()
	store := cache.New(flaky, t.TempDir(), time.Hour, cache.WithClock(func() time.Time { return clock }))
	deps := testDeps(store)
	deps.Cache = store
	sess := newSession(t, deps)
	args := map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "start": "2022-01-03"}
	var out simulateDCAOutput
	callOK(t, sess, "simulate_dca", args, &out)
	flaky.down.Store(true)
	clock = clock.Add(2 * time.Hour)
	callOK(t, sess, "simulate_dca", args, &out)
	if out.Comparison == nil || !hasNote(out.Notes, "SPY may be stale") {
		t.Errorf("comparison present %v, notes %v: want the stale baseline flagged", out.Comparison != nil, out.Notes)
	}
}

func TestFix2ToolsplanLowFindings(t *testing.T) {
	src := newSimulationsSource()
	deps := testDeps(src)
	deps.Fund = newSimFundSource()
	sess := newSession(t, deps)

	t.Run("weights within 1% are normalised", func(t *testing.T) {
		var out simulatePortfolioDCAOutput
		callOK(t, sess, "simulate_portfolio_dca", map[string]any{
			"allocations": []map[string]any{{"symbol": "VOO", "weight": 0.3333}, {"symbol": "SPY", "weight": 0.3333}, {"symbol": "SCHD", "weight": 0.3333}},
			"amount":      90, "cadence": "monthly", "start": "2022-01-03", "compare_with": "",
		}, &out)
		if len(out.Allocations) != 3 || out.Allocations[0].WeightPct != 33.33 {
			t.Errorf("allocations = %+v", out.Allocations)
		}
	})

	t.Run("short horizon must be shorter than the long one", func(t *testing.T) {
		callErr(t, sess, "review_dca_plan", reviewArgs(map[string]any{"horizon_years": 0.25, "short_horizon_months": 60}), "short_horizon_months")
	})

	t.Run("sub-cent commissions are shown so the formulas reconcile", func(t *testing.T) {
		var out reviewDCAPlanOutput
		callOK(t, sess, "review_dca_plan", map[string]any{"symbol": "VOO", "amount": 5, "fee_rate": 0.0025, "horizon_years": 1}, &out)
		c := out.Costs
		if c.CommissionPerContribution != 0.0125 || c.CommissionPerYear != round2(0.0125*252) || c.CommissionPctOfContribution != 0.25 {
			t.Errorf("costs = %+v, want 0.0125 per purchase, 3.15 a year, 0.25%%", c)
		}
		if !strings.HasPrefix(out.Observations[0], "A 0.0125 USD commission") {
			t.Errorf("observations[0] = %q", out.Observations[0])
		}
	})

	t.Run("overlapping windows are called out", func(t *testing.T) {
		var out reviewDCAPlanOutput
		callOK(t, sess, "review_dca_plan", map[string]any{"symbol": "LONGRUN", "amount": 100, "cadence": "monthly", "horizon_years": 5}, &out)
		for _, r := range []*reviewRollingOutput{out.ShortTerm.Rolling, out.LongTerm.Rolling} {
			if r == nil || !strings.Contains(r.Summary, "overlap") {
				t.Errorf("rolling summary lacks the overlap caveat: %+v", r)
			}
		}
	})

	t.Run("cost drag adds the fund's own expense ratio back", func(t *testing.T) {
		var out reviewDCAPlanOutput
		callOK(t, sess, "review_dca_plan", reviewArgs(map[string]any{"horizon_years": 1}), &out)
		mc := out.LongTerm.MonteCarlo
		if mc == nil {
			t.Fatal("monte_carlo missing")
		}
		want := pct((1+mc.HistoricalAnnualReturnPct/100)/(1-0.0003) - 1)
		if out.LongTerm.CostDrag.AssumedGrowthPct != want {
			t.Errorf("assumed_growth_pct = %v, want %v", out.LongTerm.CostDrag.AssumedGrowthPct, want)
		}
	})

	t.Run("fx block is described", func(t *testing.T) {
		list, err := sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range list.Tools {
			if tool.Name != "simulate_dca" {
				continue
			}
			raw, _ := json.Marshal(tool.OutputSchema)
			var schema map[string]any
			_ = json.Unmarshal(raw, &schema)
			props, _ := field(schema, "properties", "fx", "properties").(map[string]any)
			for _, name := range []string{"start_rate", "end_rate", "avg_purchase_rate", "fx_effect"} {
				if d, _ := field(props, name, "description").(string); d == "" {
					t.Errorf("fx.%s has no description", name)
				}
			}
		}
	})

	t.Run("seed above 2^53 is rejected by the schema", func(t *testing.T) {
		callErr(t, sess, "forecast_dca", map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 1, "seed": 9007199254740993.0}, "seed")
	})
}

func TestFix2ToolsplanWindowSpacing(t *testing.T) {
	tests := []struct {
		count, months, step int
		want                string
	}{
		{count: 5, months: 1, step: 1, want: ""},
		{count: 5, months: 12, step: 12, want: " (one starting every 12 months)"},
		{count: 24, months: 12, step: 1, want: " (one starting every month; they overlap, so only 2 of them cover separate periods)"},
		// Starts 0, 12, ..., 108 months for 30-month windows: 0, 36, 72 and
		// 108 do not overlap.
		{count: 10, months: 30, step: 12, want: " (one starting every 12 months; they overlap, so only 4 of them cover separate periods)"},
		// Starts 0, 5, ..., 60 for 12-month windows: 0, 15, 30, 45, 60.
		{count: 13, months: 12, step: 5, want: " (one starting every 5 months; they overlap, so only 5 of them cover separate periods)"},
	}
	for _, tt := range tests {
		if got := windowSpacing(tt.count, tt.months, tt.step); got != tt.want {
			t.Errorf("windowSpacing(%d, %d, %d) = %q, want %q", tt.count, tt.months, tt.step, got, tt.want)
		}
	}
}

func TestFix2ToolsplanThinTimeline(t *testing.T) {
	monthEnds := func(n int) []sim.Point {
		out := make([]sim.Point, n)
		for i := range out {
			// The last day of month i after January 1990.
			out[i] = sim.Point{Date: time.Date(1990, time.Month(2+i), 0, 0, 0, 0, 0, time.UTC), Value: float64(i)}
		}
		return out
	}
	if got, step := thinTimeline(monthEnds(120)); len(got) != 120 || step != "month" {
		t.Errorf("120 month-ends: %d points, step %s", len(got), step)
	}
	got, step := thinTimeline(monthEnds(121))
	if step != "quarter" || len(got) != 41 || got[len(got)-1].Value != 120 {
		t.Errorf("121 month-ends: %d points, step %s, last %+v; want 40 quarter-ends and the final point", len(got), step, got[len(got)-1])
	}
	// 100 years of month-ends: 400 quarter-ends are too many, 100 year-ends
	// are not; the final month is a December, so it is not repeated.
	got, step = thinTimeline(monthEnds(1200))
	if step != "year" || len(got) != 100 || got[0].Date.Month() != time.December {
		t.Errorf("1200 month-ends: %d points, step %s", len(got), step)
	}
}
