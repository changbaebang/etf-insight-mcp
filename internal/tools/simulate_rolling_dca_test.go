package tools

import (
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/sim"
)

// runRolling runs sim.RunRolling directly, the reference the tool output
// must match.
func runRolling(t *testing.T, src *fakeSource, plan sim.Plan, cfg sim.RollingConfig) *sim.RollingResult {
	t.Helper()
	res, err := sim.RunRolling(plan, sim.Input{Series: src.series, FX: src.series[fxSymbol]}, cfg)
	if err != nil {
		t.Fatalf("sim.RunRolling: %v", err)
	}
	return res
}

// requireWindow compares a wire window with an engine window.
func requireWindow(t *testing.T, label string, got rollingWindowOutput, want sim.RollingWindow) {
	t.Helper()
	if got.Start != want.Start.Format(market.DateLayout) || got.End != want.End.Format(market.DateLayout) ||
		got.Contributions != want.Contributions || got.Invested != round2(want.Invested) ||
		got.FinalValue != round2(want.FinalValue) || got.ReturnPct != round2(want.ReturnPct) {
		t.Errorf("%s = %+v, want %+v", label, got, want)
	}
}

func TestSimulateRollingDCA(t *testing.T) {
	src := newSimulationsSource()
	sess := newSession(t, testDeps(src))
	monthlyVOO := sim.Plan{
		Allocations: []sim.Allocation{{Symbol: "VOO", Weight: 1}},
		Amount:      100,
		Currency:    sim.CurrencyUSD,
		Cadence:     sim.Monthly,
		Reinvest:    true,
	}

	t.Run("one-year windows every month", func(t *testing.T) {
		want := runRolling(t, src, monthlyVOO, sim.RollingConfig{DurationYears: 1, StepMonths: 1})
		var out simulateRollingDCAOutput
		callOK(t, sess, "simulate_rolling_dca", map[string]any{"symbol": "voo", "amount": 100, "cadence": "monthly", "duration_years": 1}, &out)
		if out.WindowsCount != want.Count || out.WindowsCount != 24 || len(out.Windows) != 24 || out.WindowsDownsampled {
			t.Fatalf("windows_count/len/downsampled = %d/%d/%v, want 24/24/false", out.WindowsCount, len(out.Windows), out.WindowsDownsampled)
		}
		for _, key := range percentileKeys {
			if got, ok := out.ReturnPctPercentiles[key]; !ok || got != round2(want.ReturnPctPercentiles[key]) {
				t.Errorf("return_pct_percentiles[%s] = %v (present %v), want %v", key, got, ok, round2(want.ReturnPctPercentiles[key]))
			}
			if got, ok := out.AnnualizedPercentiles[key]; !ok || got != round2(want.AnnualizedPercentiles[key]*100) {
				t.Errorf("annualized_percentiles[%s] = %v (present %v), want %v", key, got, ok, round2(want.AnnualizedPercentiles[key]*100))
			}
		}
		if out.ProbLossPct != pct(want.ProbLoss) || out.DurationMonths != 12 || out.StepMonths != 1 {
			t.Errorf("prob_loss_pct/duration/step = %v/%d/%d", out.ProbLossPct, out.DurationMonths, out.StepMonths)
		}
		requireWindow(t, "best", out.Best, want.Best)
		requireWindow(t, "worst", out.Worst, want.Worst)
		requireWindow(t, "first window", out.Windows[0], want.Windows[0])
		if out.FirstStart != "2021-01-04" || out.LastEnd != want.Windows[want.Count-1].End.Format(market.DateLayout) || out.Windows[0].AnnualizedReturnPct == nil {
			t.Errorf("first window = %+v, first_start %s", out.Windows[0], out.FirstStart)
		}
		if out.Best.ReturnPct < out.Worst.ReturnPct {
			t.Errorf("best %v < worst %v", out.Best.ReturnPct, out.Worst.ReturnPct)
		}
		if !hasNote(out.Notes, "24 windows of 12 months") || !hasNote(out.Notes, "prob_loss_pct is the share of windows") || out.Disclaimer != Disclaimer {
			t.Errorf("notes = %v", out.Notes)
		}
	})

	t.Run("more than 60 windows lists the most recent", func(t *testing.T) {
		plan := monthlyVOO
		plan.Allocations = []sim.Allocation{{Symbol: "LONGRUN", Weight: 1}}
		want := runRolling(t, src, plan, sim.RollingConfig{DurationYears: 1, StepMonths: 1})
		var out simulateRollingDCAOutput
		callOK(t, sess, "simulate_rolling_dca", map[string]any{"symbol": "LONGRUN", "amount": 100, "cadence": "monthly", "duration_years": 1}, &out)
		if want.Count <= maxRollingWindowsShown || out.WindowsCount != want.Count {
			t.Fatalf("windows_count = %d, engine %d, want more than %d", out.WindowsCount, want.Count, maxRollingWindowsShown)
		}
		if len(out.Windows) != maxRollingWindowsShown || !out.WindowsDownsampled || !hasNote(out.Notes, "60 most recent") {
			t.Errorf("windows/downsampled/notes = %d/%v/%v", len(out.Windows), out.WindowsDownsampled, out.Notes)
		}
		requireWindow(t, "last window", out.Windows[len(out.Windows)-1], want.Windows[want.Count-1])
		requireWindow(t, "first listed window", out.Windows[0], want.Windows[want.Count-maxRollingWindowsShown])
	})

	t.Run("windows under a year carry no annualized figures", func(t *testing.T) {
		var out simulateRollingDCAOutput
		callOK(t, sess, "simulate_rolling_dca", map[string]any{"symbol": "VOO", "amount": 100, "cadence": "weekly", "duration_years": 0.25, "step_months": 2}, &out)
		if out.AnnualizedPercentiles != nil || !hasNote(out.Notes, "annualized_percentiles omitted") {
			t.Errorf("annualized_percentiles = %v, notes = %v", out.AnnualizedPercentiles, out.Notes)
		}
		for _, w := range out.Windows {
			if w.AnnualizedReturnPct != nil {
				t.Fatalf("window %+v has an annualized return", w)
			}
		}
		if out.DurationMonths != 3 || out.StepMonths != 2 || out.Cadence != "weekly" {
			t.Errorf("duration/step/cadence = %d/%d/%s", out.DurationMonths, out.StepMonths, out.Cadence)
		}
	})

	t.Run("fixed commission matches the engine", func(t *testing.T) {
		plan := monthlyVOO
		plan.FeeFixed = 2
		plan.Reinvest = false
		want := runRolling(t, src, plan, sim.RollingConfig{DurationYears: 1, StepMonths: 3})
		var out simulateRollingDCAOutput
		callOK(t, sess, "simulate_rolling_dca", map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "duration_years": 1, "step_months": 3, "commission_fixed": 2, "reinvest_dividends": false}, &out)
		if out.WindowsCount != want.Count || out.ReturnPctPercentiles["p50"] != round2(want.ReturnPctPercentiles["p50"]) {
			t.Errorf("count/p50 = %d/%v, want %d/%v", out.WindowsCount, out.ReturnPctPercentiles["p50"], want.Count, round2(want.ReturnPctPercentiles["p50"]))
		}
		var free simulateRollingDCAOutput
		callOK(t, sess, "simulate_rolling_dca", map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "duration_years": 1, "step_months": 3, "reinvest_dividends": false}, &free)
		if out.ReturnPctPercentiles["p50"] >= free.ReturnPctPercentiles["p50"] {
			t.Errorf("median with commission %v, without %v: the commission should cost something", out.ReturnPctPercentiles["p50"], free.ReturnPctPercentiles["p50"])
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "not whole months", args: map[string]any{"symbol": "VOO", "amount": 100, "duration_years": 0.3}, want: "not a whole number of months"},
		{name: "too short", args: map[string]any{"symbol": "VOO", "amount": 100, "duration_years": 0.1}, want: "duration_years must be between 0.25 and 30"},
		{name: "too long", args: map[string]any{"symbol": "VOO", "amount": 100, "duration_years": 31}, want: "duration_years must be between 0.25 and 30"},
		{name: "missing duration", args: map[string]any{"symbol": "VOO", "amount": 100}, want: "duration_years"},
		{name: "negative step", args: map[string]any{"symbol": "VOO", "amount": 100, "duration_years": 1, "step_months": -1}, want: "step_months must be between 1 and 120"},
		{name: "step too large", args: map[string]any{"symbol": "VOO", "amount": 100, "duration_years": 1, "step_months": 121}, want: "step_months must be between 1 and 120"},
		{name: "history too short", args: map[string]any{"symbol": "SCHD", "amount": 100, "duration_years": 2}, want: "not enough history for rolling windows"},
		{name: "history too short hint", args: map[string]any{"symbol": "SCHD", "amount": 100, "duration_years": 2}, want: "shorten duration_years or step_months"},
		{name: "unknown symbol", args: map[string]any{"symbol": "XYZ", "amount": 100, "duration_years": 1}, want: "unknown symbol XYZ"},
		{name: "zero amount", args: map[string]any{"symbol": "VOO", "amount": 0, "duration_years": 1}, want: "amount must be > 0"},
		{name: "commission swallows the purchase", args: map[string]any{"symbol": "VOO", "amount": 1, "duration_years": 1, "commission_fixed": 1}, want: "commission_fixed 1 leaves nothing"},
		{name: "once", args: map[string]any{"symbol": "VOO", "amount": 100, "duration_years": 1, "cadence": "once"}, want: "use daily, weekly or monthly"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "simulate_rolling_dca", tt.args, tt.want)
		})
	}
}
