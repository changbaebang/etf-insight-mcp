package tools

import (
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/sim"
)

func TestSimulateLumpSumVsDCA(t *testing.T) {
	src := newSimulationsSource()
	sess := newSession(t, testDeps(src))
	simIn := sim.Input{Series: src.series, FX: src.series[fxSymbol]}

	t.Run("lump sum wins on a rising series", func(t *testing.T) {
		plan := sim.Plan{
			Allocations: []sim.Allocation{{Symbol: "RISE", Weight: 1}},
			Currency:    sim.CurrencyUSD,
			Cadence:     sim.Monthly,
			Start:       date(t, "2021-01-04"),
			End:         date(t, "2023-12-29"),
			Reinvest:    true,
		}
		want, err := sim.RunLumpSumVsDCA(plan, simIn, 12000)
		if err != nil {
			t.Fatalf("RunLumpSumVsDCA: %v", err)
		}
		var out simulateLumpSumVsDCAOutput
		callOK(t, sess, "simulate_lump_sum_vs_dca", map[string]any{"symbol": "rise", "total_amount": 12000, "cadence": "monthly", "start": "2021-01-04", "end": "2023-12-29"}, &out)
		requireMatch(t, "lump_sum", out.LumpSum, want.LumpSum)
		requireMatch(t, "dca", out.DCA, want.DCA)
		if out.LumpSum.Contributions != 1 || out.DCA.Contributions != 36 || out.LumpSum.Invested != 12000 || out.DCA.Invested != 12000 {
			t.Errorf("contributions/invested = %d/%v and %d/%v, want 1/12000 and 36/12000", out.LumpSum.Contributions, out.LumpSum.Invested, out.DCA.Contributions, out.DCA.Invested)
		}
		if out.PerContributionAmount != round2(12000.0/36) {
			t.Errorf("per_contribution_amount = %v, want %v", out.PerContributionAmount, round2(12000.0/36))
		}
		if out.Diff.FinalValue <= 0 || out.Diff.ReturnPct <= 0 {
			t.Errorf("diff = %+v, want the lump sum ahead", out.Diff)
		}
		// Every dollar grows at the same constant rate on RISE, so the
		// money-weighted rates of the two legs coincide.
		if a := out.Diff.AnnualizedReturnPct; a == nil || *a > 0.01 || *a < -0.01 {
			t.Errorf("diff.annualized_return_pct = %s, want about 0 on a constant-growth series", derefPct(a))
		}
		if out.Diff.FinalValue != round2(out.LumpSum.FinalValue-out.DCA.FinalValue) {
			t.Errorf("diff.final_value %v does not reconcile with %v - %v", out.Diff.FinalValue, out.LumpSum.FinalValue, out.DCA.FinalValue)
		}
		if len(out.Allocations) != 1 || out.Allocations[0].Symbol != "RISE" || out.Currency != "USD" || out.Cadence != "monthly" || out.TotalAmount != 12000 {
			t.Errorf("echo = %+v %s %s %v", out.Allocations, out.Currency, out.Cadence, out.TotalAmount)
		}
		if !hasNote(out.Notes, "DCA leg: 36 monthly contributions of 333.33 USD") || !hasNote(out.Notes, "diff is lump_sum minus dca") {
			t.Errorf("notes = %v", out.Notes)
		}
		if out.Disclaimer != Disclaimer {
			t.Error("disclaimer missing")
		}
	})

	t.Run("fixed commission is paid once by the lump sum", func(t *testing.T) {
		var out simulateLumpSumVsDCAOutput
		callOK(t, sess, "simulate_lump_sum_vs_dca", map[string]any{"symbol": "SPY", "total_amount": 1200, "cadence": "monthly", "start": "2022-01-03", "end": "2022-12-30", "commission_fixed": 1.5}, &out)
		if out.LumpSum.Fees != 1.5 || out.DCA.Fees != 18 {
			t.Errorf("fees = %v (lump) and %v (dca), want 1.5 and 18", out.LumpSum.Fees, out.DCA.Fees)
		}
		if out.DCA.CostRatioPct != 1.5 {
			t.Errorf("dca cost_ratio_pct = %v, want 1.5", out.DCA.CostRatioPct)
		}
		if !hasNote(out.Notes, "paid once by the lump sum") {
			t.Errorf("notes = %v, want the commission comparison", out.Notes)
		}
	})

	t.Run("short range omits the annualized difference", func(t *testing.T) {
		var out simulateLumpSumVsDCAOutput
		callOK(t, sess, "simulate_lump_sum_vs_dca", map[string]any{"symbol": "VOO", "total_amount": 5000, "cadence": "weekly", "start": "2023-01-02", "end": "2023-06-30"}, &out)
		if out.Diff.AnnualizedReturnPct != nil || !hasNote(out.Notes, "diff.annualized_return_pct omitted") {
			t.Errorf("diff = %+v, notes = %v", out.Diff, out.Notes)
		}
		for _, n := range out.Notes {
			if hasNote([]string{n}, "Diff.AnnualizedReturn") {
				t.Errorf("engine field name leaked into notes: %q", n)
			}
		}
	})

	t.Run("portfolio in KRW", func(t *testing.T) {
		var out simulateLumpSumVsDCAOutput
		callOK(t, sess, "simulate_lump_sum_vs_dca", map[string]any{
			"allocations":  []map[string]any{{"symbol": "VOO", "weight": 60}, {"symbol": "SCHD", "weight": 40}},
			"total_amount": 10000000, "currency": "krw", "cadence": "monthly", "start": "2022-01-03",
		}, &out)
		if out.Currency != "KRW" || out.LumpSum.FX == nil || out.DCA.FX == nil || len(out.LumpSum.Holdings) != 2 {
			t.Errorf("currency/fx/holdings = %s/%v/%v/%d", out.Currency, out.LumpSum.FX, out.DCA.FX, len(out.LumpSum.Holdings))
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "symbol and allocations", args: map[string]any{"symbol": "VOO", "allocations": []map[string]any{{"symbol": "SPY", "weight": 1}}, "total_amount": 1000, "start": "2022-01-03"}, want: "either symbol or allocations"},
		{name: "neither", args: map[string]any{"total_amount": 1000, "start": "2022-01-03"}, want: "give symbol"},
		{name: "zero total", args: map[string]any{"symbol": "VOO", "total_amount": 0, "start": "2022-01-03"}, want: "total_amount must be > 0"},
		{name: "missing start", args: map[string]any{"symbol": "VOO", "total_amount": 1000}, want: "start"},
		{name: "blank start", args: map[string]any{"symbol": "VOO", "total_amount": 1000, "start": ""}, want: "start is required (YYYY-MM-DD)"},
		{name: "end before start", args: map[string]any{"symbol": "VOO", "total_amount": 1000, "start": "2022-06-01", "end": "2022-01-01"}, want: "end 2022-01-01 is before start 2022-06-01"},
		{name: "once is not a cadence", args: map[string]any{"symbol": "VOO", "total_amount": 1000, "start": "2022-01-03", "cadence": "once"}, want: "use daily, weekly or monthly"},
		{name: "unknown symbol", args: map[string]any{"symbol": "XYZ", "total_amount": 1000, "start": "2022-01-03"}, want: "unknown symbol XYZ"},
		{name: "commission swallows the lump sum", args: map[string]any{"symbol": "VOO", "total_amount": 1, "start": "2022-01-03", "commission_fixed": 1}, want: "commission_fixed 1 leaves nothing"},
		{name: "commission swallows each DCA purchase", args: map[string]any{"symbol": "VOO", "total_amount": 100, "start": "2022-01-03", "end": "2022-12-30", "commission_fixed": 0.5}, want: "raise total_amount, choose a sparser cadence"},
		{name: "negative commission", args: map[string]any{"symbol": "VOO", "total_amount": 100, "start": "2022-01-03", "commission_fixed": -1}, want: "commission_fixed must be >= 0"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "simulate_lump_sum_vs_dca", tt.args, tt.want)
		})
	}
}
