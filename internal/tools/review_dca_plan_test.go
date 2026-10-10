package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// simulationToolNames are the tools registerSimulations adds.
var simulationToolNames = []string{"simulate_lump_sum_vs_dca", "simulate_rolling_dca", "review_dca_plan"}

func TestSimulationToolsAreListed(t *testing.T) {
	sess := newSession(t, testDeps(newSimulationsSource()))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range simulationToolNames {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("tool %s is not listed", name)
			continue
		}
		if tool.Description == "" || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s lacks a description or the read-only hint", name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("tool %s input schema is %T", name, tool.InputSchema)
		}
		checkDescribed(t, name, "", schema)
	}
}

// reviewArgs is the 5 USD a day plan with a 0.99 USD commission the
// review examples use.
func reviewArgs(extra map[string]any) map[string]any {
	args := map[string]any{"symbol": "voo", "amount": 5, "commission_fixed": 0.99}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestReviewDCAPlan(t *testing.T) {
	src := newSimulationsSource()
	deps := testDeps(src)
	deps.Fund = newSimFundSource()
	sess := newSession(t, deps)

	var out reviewDCAPlanOutput
	callOK(t, sess, "review_dca_plan", reviewArgs(map[string]any{"horizon_years": 1}), &out)

	t.Run("plan", func(t *testing.T) {
		p := out.Plan
		if p.Symbol != "VOO" || p.Name == "" || p.Category == "" || p.Currency != "USD" || p.Cadence != "daily" {
			t.Errorf("plan = %+v", p)
		}
		if p.ContributionsPerYear != 252 || p.AnnualOutlay != 1260 || p.HorizonYears != 1 || p.CommissionFixed != 0.99 || !p.ReinvestDividends {
			t.Errorf("plan figures = %+v, want 252 contributions and 1260 USD a year", p)
		}
	})

	t.Run("commission arithmetic is exact", func(t *testing.T) {
		c := out.Costs
		if c.CommissionPerContribution != 0.99 || c.CommissionPctOfContribution != 19.8 || c.CommissionPerYear != 249.48 {
			t.Errorf("commission = %v / %v%% / %v a year, want 0.99 / 19.8%% / 249.48", c.CommissionPerContribution, c.CommissionPctOfContribution, c.CommissionPerYear)
		}
		if c.ExpenseRatioPct == nil || *c.ExpenseRatioPct != 0.03 {
			t.Fatalf("expense_ratio_pct = %v, want 0.03", c.ExpenseRatioPct)
		}
		if c.ExpenseCostPerYearOnProjectedHoldings == nil || *c.ExpenseCostPerYearOnProjectedHoldings != round2(0.0003*1260*1/2) {
			t.Errorf("expense cost = %v, want %v", c.ExpenseCostPerYearOnProjectedHoldings, round2(0.0003*1260*1/2))
		}
		if want := pct((249.48 + 0.0003*1260/2) / 1260); c.TotalCostPctOfOutlayYear1 != want {
			t.Errorf("total_cost_pct_of_outlay_year1 = %v, want %v", c.TotalCostPctOfOutlayYear1, want)
		}
		if !hasNote(c.Notes, "expense_ratio × annual_outlay × horizon_years / 2") {
			t.Errorf("costs notes = %v, want the formula", c.Notes)
		}
	})

	t.Run("observations are factual sentences ending with the disclaimer", func(t *testing.T) {
		obs := out.Observations
		if len(obs) < 5 {
			t.Fatalf("observations = %v", obs)
		}
		if want := "A 0.99 USD commission on a 5 USD contribution is 19.8% of each purchase; over a year that is 249.48 USD on 1,260 USD invested."; obs[0] != want {
			t.Errorf("observations[0] = %q, want %q", obs[0], want)
		}
		if want := "Expense ratio 0.03%: 0.38 USD per year per 1,260 USD held."; obs[1] != want {
			t.Errorf("observations[1] = %q, want %q", obs[1], want)
		}
		if obs[len(obs)-1] != Disclaimer || out.Disclaimer != Disclaimer {
			t.Error("observations do not end with the disclaimer")
		}
		for _, o := range obs {
			for _, word := range []string{"should", "recommend", "we suggest", "better choice"} {
				if strings.Contains(strings.ToLower(o), word) && o != Disclaimer {
					t.Errorf("observation %q reads as advice (%q)", o, word)
				}
			}
		}
		if !hasNote(obs, "historical 3-month windows the plan ended below cost") {
			t.Errorf("observations lack the short-term sentence: %v", obs)
		}
	})

	t.Run("history", func(t *testing.T) {
		h := out.History
		if len(h.Windows) != 8 || h.AsOf != "2023-12-29" || h.FirstDate != "2021-01-04" || h.Volatility1YPct <= 0 || h.MaxDrawdownAllPct <= 0 {
			t.Errorf("history = %+v", h)
		}
		if h.TTMDividendYieldPct <= 0 {
			t.Fatalf("ttm_dividend_yield_pct = %v, want > 0", h.TTMDividendYieldPct)
		}
		// The yield is shown rounded to 0.01 percentage points, which moves
		// yield × 1260 by up to 0.063.
		if diff := h.ProjectedAnnualDividendsAtHorizon - h.TTMDividendYieldPct/100*1260*1; diff > 0.07 || diff < -0.07 {
			t.Errorf("projected dividends %v, want about yield %v%% × 1260 × 1", h.ProjectedAnnualDividendsAtHorizon, h.TTMDividendYieldPct)
		}
	})

	t.Run("short term", func(t *testing.T) {
		s := out.ShortTerm
		if s.Months != 3 || s.Trend.State == "" || s.Rolling == nil {
			t.Fatalf("short_term = %+v", s)
		}
		r := s.Rolling
		if r.WindowsCount < 3 || r.DurationMonths != 3 || r.StepMonths != 1 || r.AnnualizedPercentiles != nil {
			t.Errorf("short rolling = %+v", r.rollingStatsOutput)
		}
		for _, key := range percentileKeys {
			if _, ok := r.ReturnPctPercentiles[key]; !ok {
				t.Errorf("short rolling percentiles lack %s", key)
			}
		}
		if !strings.HasPrefix(r.Summary, "over ") || !strings.Contains(r.Summary, "historical 3-month windows the plan ended below cost") {
			t.Errorf("summary = %q", r.Summary)
		}
	})

	t.Run("long term falls back to monthly windows", func(t *testing.T) {
		l := out.LongTerm
		if l.Years != 1 || l.Rolling == nil {
			t.Fatalf("long_term = %+v", l)
		}
		if l.Rolling.StepMonths != 1 || l.Rolling.WindowsCount != 24 || !hasNote(l.Notes, "rolling starts a window every month") {
			t.Errorf("long rolling step/count = %d/%d, notes %v", l.Rolling.StepMonths, l.Rolling.WindowsCount, l.Notes)
		}
		mc := l.MonteCarlo
		if mc == nil || mc.Simulations != 2000 || mc.Seed != 42 || mc.Contributions != 252 || mc.Invested != 1260 {
			t.Fatalf("monte_carlo = %+v", mc)
		}
		ref, err := analytics.MonteCarlo(analytics.MCPlan{
			Symbols: []string{"VOO"}, Weights: []float64{1}, Amount: 5, Currency: "USD", Cadence: analytics.CadenceDaily, HorizonYears: 1, FeeRate: 0.99 / 5,
		}, analytics.MCConfig{Simulations: 2000, Seed: 42}, analytics.MCInput{Series: src.series})
		if err != nil {
			t.Fatalf("MonteCarlo: %v", err)
		}
		for _, key := range percentileKeys {
			if mc.Percentiles[key] != round2(ref.Percentiles[key]) {
				t.Errorf("monte_carlo %s = %v, want %v", key, mc.Percentiles[key], round2(ref.Percentiles[key]))
			}
		}
		if mc.ProbLossPct != pct(ref.ProbLoss) || !hasNote(l.Notes, "commission_fixed as part of the fee rate") {
			t.Errorf("prob_loss_pct %v, want %v; notes %v", mc.ProbLossPct, pct(ref.ProbLoss), l.Notes)
		}
		cd := l.CostDrag
		if cd.LowExpenseRatioPct != 0.03 || cd.HighExpenseRatioPct != 0.2 || cd.FinalValueLow <= cd.FinalValueHigh || cd.Difference != round2(cd.FinalValueLow-cd.FinalValueHigh) {
			t.Errorf("cost_drag = %+v", cd)
		}
		if cd.NetInvested != round2(1260-249.48) || cd.AssumedGrowthPct != mc.HistoricalAnnualReturnPct {
			t.Errorf("cost_drag net/growth = %v/%v", cd.NetInvested, cd.AssumedGrowthPct)
		}
		if !strings.Contains(l.CostDragNote, "0.03% expense ratio ends at about") || !strings.Contains(l.CostDragNote, "(1 − expense ratio))^(1/12)") {
			t.Errorf("cost_drag_note = %q", l.CostDragNote)
		}
	})

	t.Run("long history keeps yearly windows", func(t *testing.T) {
		var long reviewDCAPlanOutput
		callOK(t, sess, "review_dca_plan", map[string]any{"symbol": "LONGRUN", "amount": 100, "cadence": "monthly", "horizon_years": 1}, &long)
		r := long.LongTerm.Rolling
		if r == nil || r.StepMonths != 12 || r.WindowsCount < reviewMinLongWindows || !strings.Contains(r.Summary, "(one starting every 12 months)") {
			t.Fatalf("long rolling = %+v", r)
		}
		if long.Plan.Name != "Long Run Index Fund" || long.Plan.Category != "Large Blend" || long.Plan.ContributionsPerYear != 12 {
			t.Errorf("plan = %+v, want the fund profile's name and category", long.Plan)
		}
		if long.Costs.ExpenseRatioPct != nil || !hasNote(long.Costs.Notes, "does not report an expense ratio") {
			t.Errorf("expense ratio = %v, notes = %v", long.Costs.ExpenseRatioPct, long.Costs.Notes)
		}
		if long.Observations[0] != "No commission was given (commission_fixed and fee_rate are 0), so all 1,200 USD a year buys shares." {
			t.Errorf("observations[0] = %q", long.Observations[0])
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing symbol", args: map[string]any{"amount": 5}, want: "symbol"},
		{name: "blank symbol", args: map[string]any{"symbol": " ", "amount": 5}, want: "symbol is required"},
		{name: "zero amount", args: map[string]any{"symbol": "VOO", "amount": 0}, want: "amount must be > 0"},
		{name: "once", args: reviewArgs(map[string]any{"cadence": "once"}), want: "use daily, weekly or monthly"},
		{name: "horizon not whole months", args: reviewArgs(map[string]any{"horizon_years": 1.3}), want: "not a whole number of months"},
		{name: "horizon too long", args: reviewArgs(map[string]any{"horizon_years": 41}), want: "horizon_years must be between"},
		{name: "short horizon too long", args: reviewArgs(map[string]any{"short_horizon_months": 61}), want: "short_horizon_months must be between 1 and 60"},
		{name: "negative short horizon", args: reviewArgs(map[string]any{"short_horizon_months": -1}), want: "short_horizon_months"},
		{name: "commission swallows the purchase", args: reviewArgs(map[string]any{"commission_fixed": 5}), want: "commission_fixed 5 leaves nothing"},
		{name: "unknown symbol", args: map[string]any{"symbol": "XYZ", "amount": 5}, want: "unknown symbol XYZ"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "review_dca_plan", tt.args, tt.want)
		})
	}
}

func TestReviewDCAPlanWithoutFundSource(t *testing.T) {
	sess := newSession(t, testDeps(newSimulationsSource()))
	res := call(t, sess, "review_dca_plan", reviewArgs(nil))
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var generic struct {
		Costs map[string]any `json:"costs"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"expense_ratio_pct", "expense_cost_per_year_on_projected_holdings"} {
		v, present := generic.Costs[key]
		if !present || v != nil {
			t.Errorf("costs.%s = %v (present %v), want null", key, v, present)
		}
	}

	var out reviewDCAPlanOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !hasNote(out.Costs.Notes, "the fund data source is not configured") || !hasNote(out.Observations, "The expense ratio is unavailable") {
		t.Errorf("notes = %v, observations = %v", out.Costs.Notes, out.Observations)
	}
	if out.Costs.TotalCostPctOfOutlayYear1 != 19.8 {
		t.Errorf("total_cost_pct_of_outlay_year1 = %v, want 19.8 (commissions only)", out.Costs.TotalCostPctOfOutlayYear1)
	}
	// Five-year windows do not fit three years of VOO history; the
	// projection still runs.
	if out.Plan.HorizonYears != 5 || out.ShortTerm.Months != 3 || out.LongTerm.Rolling != nil || !hasNote(out.LongTerm.Notes, "rolling is null: not enough history for rolling windows") || !hasNote(out.LongTerm.Notes, "a shorter horizon_years fits more windows") || out.LongTerm.MonteCarlo == nil {
		t.Errorf("defaults/long term = %v/%d/%+v", out.Plan.HorizonYears, out.ShortTerm.Months, out.LongTerm)
	}
	if out.LongTerm.MonteCarlo.Contributions != 1260 || !hasNote(out.Observations, "The history is too short for 60-month rolling windows.") {
		t.Errorf("monte_carlo contributions = %d, observations %v", out.LongTerm.MonteCarlo.Contributions, out.Observations)
	}
}

func TestReviewDCAPlanVariants(t *testing.T) {
	src := newSimulationsSource()

	t.Run("fund source failure leaves the expense ratio null", func(t *testing.T) {
		deps := testDeps(src)
		deps.Fund = simFundSource{err: errors.New("connection reset")}
		var out reviewDCAPlanOutput
		callOK(t, newSession(t, deps), "review_dca_plan", reviewArgs(map[string]any{"horizon_years": 1}), &out)
		if out.Costs.ExpenseRatioPct != nil || !hasNote(out.Costs.Notes, "the fund profile could not be fetched: connection reset") {
			t.Errorf("expense ratio = %v, notes = %v", out.Costs.ExpenseRatioPct, out.Costs.Notes)
		}
		if out.Plan.Name == "" || out.Plan.Category == "" {
			t.Errorf("plan = %+v, want the universe name and category without a fund profile", out.Plan)
		}
	})

	t.Run("unknown fund profile", func(t *testing.T) {
		deps := testDeps(src)
		deps.Fund = newSimFundSource()
		var out reviewDCAPlanOutput
		callOK(t, newSession(t, deps), "review_dca_plan", map[string]any{"symbol": "SPY", "amount": 20, "cadence": "weekly", "horizon_years": 2, "reinvest_dividends": false}, &out)
		if out.Costs.ExpenseRatioPct != nil || !hasNote(out.Costs.Notes, "has no profile for SPY") {
			t.Errorf("expense ratio = %v, notes = %v", out.Costs.ExpenseRatioPct, out.Costs.Notes)
		}
		if out.Plan.ContributionsPerYear != 52 || out.Plan.AnnualOutlay != 1040 || out.Plan.ReinvestDividends {
			t.Errorf("plan = %+v, want 52 weekly contributions of 20 USD without reinvestment", out.Plan)
		}
		if !hasNote(out.LongTerm.Notes, "assumes dividends are reinvested") || hasNote(out.LongTerm.Notes, "commission_fixed as part of the fee rate") {
			t.Errorf("long_term notes = %v", out.LongTerm.Notes)
		}
		if out.Costs.CommissionPerContribution != 0 || out.Costs.TotalCostPctOfOutlayYear1 != 0 {
			t.Errorf("costs = %+v, want none", out.Costs)
		}
	})

	t.Run("fee rate counts as commission", func(t *testing.T) {
		var out reviewDCAPlanOutput
		callOK(t, newSession(t, testDeps(src)), "review_dca_plan", map[string]any{"symbol": "VOO", "amount": 50, "cadence": "monthly", "fee_rate": 0.002, "commission_fixed": 0.5, "horizon_years": 1}, &out)
		// 0.5 + 0.2% of 50 = 0.6 per purchase, 1.2% of it, 7.2 a year on 600.
		if out.Costs.CommissionPerContribution != 0.6 || out.Costs.CommissionPctOfContribution != 1.2 || out.Costs.CommissionPerYear != 7.2 {
			t.Errorf("costs = %+v", out.Costs)
		}
		if want := "A 0.60 USD commission on a 50 USD contribution is 1.2% of each purchase; over a year that is 7.20 USD on 600 USD invested."; out.Observations[0] != want {
			t.Errorf("observations[0] = %q, want %q", out.Observations[0], want)
		}
	})
}

func TestObsFormatting(t *testing.T) {
	money := map[float64]string{1260: "1,260", 249.48: "249.48", 0.378: "0.38", 5: "5", 0.6: "0.60", 1234567.891: "1,234,567.89", -1500: "-1,500", -0.001: "0", 100: "100"}
	for in, want := range money {
		if got := obsMoney(in); got != want {
			t.Errorf("obsMoney(%v) = %q, want %q", in, got, want)
		}
	}
	nums := map[float64]string{19.8: "19.8", 0.03: "0.03", 5: "5", -0.00001: "0", 0.0945: "0.0945"}
	for in, want := range nums {
		if got := obsNum(in); got != want {
			t.Errorf("obsNum(%v) = %q, want %q", in, got, want)
		}
	}
	if obsYears(1) != "1 year" || obsYears(2.5) != "2.5 years" {
		t.Errorf("obsYears = %q / %q", obsYears(1), obsYears(2.5))
	}
}
