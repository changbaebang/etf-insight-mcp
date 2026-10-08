package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/sim"
)

// runSim runs sim.Run directly, the reference the tool output must match.
func runSim(t *testing.T, src *fakeSource, plan sim.Plan) *sim.Result {
	t.Helper()
	in := sim.Input{Series: src.series, FX: src.series[fxSymbol]}
	res, err := sim.Run(plan, in)
	if err != nil {
		t.Fatalf("sim.Run: %v", err)
	}
	return res
}

// requireMatch compares a tool result with a sim.Result field by field.
func requireMatch(t *testing.T, label string, got simResultOutput, want *sim.Result) {
	t.Helper()
	checks := []struct {
		name      string
		got, want any
	}{
		{"start", got.Start, want.Start.Format(market.DateLayout)},
		{"end", got.End, want.End.Format(market.DateLayout)},
		{"contributions", got.Contributions, want.Contributions},
		{"invested", got.Invested, round2(want.Invested)},
		{"fees", got.Fees, round2(want.Fees)},
		{"final_value", got.FinalValue, round2(want.FinalValue)},
		{"profit", got.Profit, round2(want.Profit)},
		{"return_pct", got.ReturnPct, round2(want.ReturnPct)},
		{"annualized_return_pct", got.AnnualizedReturnPct, pct(want.AnnualizedReturn)},
		{"max_drawdown_pct", got.MaxDrawdownPct, round2(want.MaxDrawdownPct)},
		{"cash_dividends", got.CashDividends, round2(want.CashDividends)},
		{"holdings", len(got.Holdings), len(want.Holdings)},
		{"timeline", len(got.Timeline), len(want.Timeline)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s %s = %v, want %v", label, c.name, c.got, c.want)
		}
	}
	for i, h := range want.Holdings {
		if i >= len(got.Holdings) {
			break
		}
		g := got.Holdings[i]
		if g.Symbol != h.Symbol || g.Shares != round4(h.Shares) || g.Value != round2(h.Value) || g.WeightPct != pct(h.Weight) {
			t.Errorf("%s holding %d = %+v, want %+v", label, i, g, h)
		}
	}
}

func TestSimulateDCA(t *testing.T) {
	src := newFakeSource()
	sess := newSession(t, testDeps(src))

	t.Run("matches sim.Run and compares with SPY", func(t *testing.T) {
		plan := sim.Plan{
			Allocations: []sim.Allocation{{Symbol: "VOO", Weight: 1}},
			Amount:      100,
			Currency:    sim.CurrencyUSD,
			Cadence:     sim.Monthly,
			Start:       date(t, "2022-01-01"),
			End:         date(t, "2022-12-31"),
			Reinvest:    true,
		}
		want := runSim(t, src, plan)

		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "voo", "amount": 100, "cadence": "monthly", "start": "2022-01-01", "end": "2022-12-31"}, &out)
		if out.Symbol != "VOO" || out.Amount != 100 || out.Currency != "USD" || out.Cadence != "monthly" || out.Disclaimer != Disclaimer {
			t.Errorf("echo = %s %v %s %s, disclaimer ok %v", out.Symbol, out.Amount, out.Currency, out.Cadence, out.Disclaimer == Disclaimer)
		}
		requireMatch(t, "plan", out.simResultOutput, want)
		if out.Contributions != 12 || out.FX != nil {
			t.Errorf("contributions/fx = %d/%v, want 12/nil", out.Contributions, out.FX)
		}

		base := plan
		base.Allocations = []sim.Allocation{{Symbol: "SPY", Weight: 1}}
		base.Start, base.End = want.Start, want.End
		wantBase := runSim(t, src, base)
		if out.Baseline == nil || out.Baseline.Symbol != "SPY" {
			t.Fatalf("baseline = %+v, want SPY", out.Baseline)
		}
		requireMatch(t, "baseline", out.Baseline.simResultOutput, wantBase)
		if out.Diff == nil {
			t.Fatal("diff missing")
		}
		if out.Diff.FinalValue != round2(want.FinalValue-wantBase.FinalValue) || out.Diff.ReturnPct != round2(want.ReturnPct-wantBase.ReturnPct) {
			t.Errorf("diff = %+v", out.Diff)
		}
	})

	t.Run("KRW converts at KRW=X", func(t *testing.T) {
		plan := sim.Plan{
			Allocations: []sim.Allocation{{Symbol: "VOO", Weight: 1}},
			Amount:      10000,
			Currency:    sim.CurrencyKRW,
			Cadence:     sim.Daily,
			Start:       date(t, "2022-01-03"),
			End:         date(t, "2022-06-30"),
			Reinvest:    true,
		}
		want := runSim(t, src, plan)
		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "VOO", "amount": 10000, "currency": "krw", "start": "2022-01-03", "end": "2022-06-30", "compare_with": ""}, &out)
		requireMatch(t, "krw", out.simResultOutput, want)
		if out.Currency != "KRW" || out.FX == nil || want.FX == nil {
			t.Fatalf("currency/fx = %s/%+v", out.Currency, out.FX)
		}
		if out.FX.StartRate != round2(want.FX.StartRate) || out.FX.EndRate != round2(want.FX.EndRate) || out.FX.FXEffect != round2(want.FX.FXEffect) {
			t.Errorf("fx = %+v, want %+v", out.FX, want.FX)
		}
		if out.Baseline != nil || out.Diff != nil {
			t.Errorf("baseline/diff = %+v/%+v, want none when compare_with is empty", out.Baseline, out.Diff)
		}
	})

	t.Run("start before history is moved with a note", func(t *testing.T) {
		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "SCHD", "amount": 50, "cadence": "weekly", "start": "2021-06-01"}, &out)
		if out.Start != "2022-01-03" || len(out.Notes) == 0 || !strings.Contains(out.Notes[0], "start moved from 2021-06-01 to 2022-01-03") {
			t.Errorf("start/notes = %s/%v", out.Start, out.Notes)
		}
		if out.Baseline == nil || out.Baseline.Start != out.Start || out.Baseline.End != out.End {
			t.Errorf("baseline range = %+v, want the realised range %s..%s", out.Baseline, out.Start, out.End)
		}
	})

	t.Run("cash dividends when not reinvesting", func(t *testing.T) {
		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "SPY", "amount": 100, "start": "2022-01-03", "end": "2022-12-30", "reinvest_dividends": false, "compare_with": ""}, &out)
		if out.CashDividends <= 0 {
			t.Errorf("cash_dividends = %v, want > 0", out.CashDividends)
		}
	})

	t.Run("fee reduces profit", func(t *testing.T) {
		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "SPY", "amount": 100, "cadence": "monthly", "start": "2022-01-03", "end": "2022-12-30", "fee_rate": 0.01, "compare_with": ""}, &out)
		if out.Fees != 12 {
			t.Errorf("fees = %v, want 12", out.Fees)
		}
	})

	t.Run("baseline equal to the symbol is skipped", func(t *testing.T) {
		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "SPY", "amount": 100, "start": "2023-01-01"}, &out)
		if out.Baseline != nil || len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "baseline skipped") {
			t.Errorf("baseline/notes = %+v/%v", out.Baseline, out.Notes)
		}
	})

	t.Run("unknown baseline is a note, not an error", func(t *testing.T) {
		var out simulateDCAOutput
		callOK(t, sess, "simulate_dca", map[string]any{"symbol": "VOO", "amount": 100, "start": "2023-01-01", "compare_with": "XYZ"}, &out)
		if out.Baseline != nil || len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "baseline XYZ not computed") {
			t.Errorf("baseline/notes = %+v/%v", out.Baseline, out.Notes)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown symbol", args: map[string]any{"symbol": "XYZ", "amount": 100, "start": "2022-01-01"}, want: "unknown symbol XYZ: not in universe and the data source returned not found; use list_etfs"},
		{name: "missing start", args: map[string]any{"symbol": "VOO", "amount": 100}, want: "start"},
		{name: "bad start", args: map[string]any{"symbol": "VOO", "amount": 100, "start": "2022/01/01"}, want: `start "2022/01/01" is not a date; use YYYY-MM-DD`},
		{name: "bad end", args: map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-01-01", "end": "soon"}, want: "YYYY-MM-DD"},
		{name: "end before start", args: map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-06-01", "end": "2022-01-01"}, want: "end 2022-01-01 is before start 2022-06-01"},
		{name: "bad cadence", args: map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-01-01", "cadence": "yearly"}, want: "use daily, weekly or monthly"},
		{name: "bad currency", args: map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-01-01", "currency": "EUR"}, want: "use USD or KRW"},
		{name: "zero amount", args: map[string]any{"symbol": "VOO", "amount": 0, "start": "2022-01-01"}, want: "amount must be > 0"},
		{name: "fee too high", args: map[string]any{"symbol": "VOO", "amount": 100, "start": "2022-01-01", "fee_rate": 1}, want: "fee_rate"},
		{name: "no history in range", args: map[string]any{"symbol": "VOO", "amount": 100, "start": "2030-01-01"}, want: "no history in range"},
		{name: "wrong type", args: map[string]any{"symbol": "VOO", "amount": "100", "start": "2022-01-01"}, want: "amount"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "simulate_dca", tt.args, tt.want)
		})
	}
}

func TestSimulatePortfolioDCA(t *testing.T) {
	src := newFakeSource()
	sess := newSession(t, testDeps(src))
	plan := sim.Plan{
		Allocations: []sim.Allocation{{Symbol: "VOO", Weight: 0.6}, {Symbol: "SCHD", Weight: 0.4}},
		Amount:      100,
		Currency:    sim.CurrencyUSD,
		Cadence:     sim.Monthly,
		Start:       date(t, "2021-06-01"),
		End:         date(t, "2023-06-30"),
		Reinvest:    true,
	}
	want := runSim(t, src, plan)
	args := func(w1, w2 float64) map[string]any {
		return map[string]any{
			"allocations": []map[string]any{{"symbol": "voo", "weight": w1}, {"symbol": "schd", "weight": w2}},
			"amount":      100, "cadence": "monthly", "start": "2021-06-01", "end": "2023-06-30",
		}
	}

	t.Run("percentages are normalised", func(t *testing.T) {
		var out simulatePortfolioDCAOutput
		callOK(t, sess, "simulate_portfolio_dca", args(60, 40), &out)
		requireMatch(t, "portfolio", out.simResultOutput, want)
		if len(out.Allocations) != 2 || out.Allocations[0].WeightPct != 60 || out.Allocations[1].WeightPct != 40 || out.Allocations[1].Symbol != "SCHD" {
			t.Errorf("allocations = %+v, want VOO 60 / SCHD 40", out.Allocations)
		}
		if out.Start != "2022-01-03" || len(out.Notes) == 0 || !strings.Contains(out.Notes[0], "SCHD history begins there") {
			t.Errorf("start/notes = %s/%v, want the common range", out.Start, out.Notes)
		}
		if out.Baseline == nil || out.Baseline.Symbol != "SPY" || out.Diff == nil {
			t.Errorf("baseline/diff = %+v/%+v, want SPY", out.Baseline, out.Diff)
		}
		if out.Disclaimer != Disclaimer {
			t.Error("disclaimer missing")
		}
	})

	t.Run("fractions give the same result", func(t *testing.T) {
		var out simulatePortfolioDCAOutput
		callOK(t, sess, "simulate_portfolio_dca", args(0.6, 0.4), &out)
		requireMatch(t, "fractions", out.simResultOutput, want)
	})

	t.Run("cache prefetches each symbol once", func(t *testing.T) {
		cached := newFakeSource()
		store := cache.New(cached, t.TempDir(), time.Hour)
		deps := testDeps(store)
		deps.Cache = store
		var out simulatePortfolioDCAOutput
		callOK(t, newSession(t, deps), "simulate_portfolio_dca", args(60, 40), &out)
		requireMatch(t, "cached", out.simResultOutput, want)
		for _, sym := range []string{"VOO", "SCHD", "SPY"} {
			if n := cached.callCount(sym); n != 1 {
				t.Errorf("%s fetched %d times through the cache, want 1", sym, n)
			}
		}
		if len(out.Notes) != 1 { // only the moved start; nothing stale
			t.Errorf("notes = %v, want just the moved start", out.Notes)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "weights sum to 1.5", args: args(1, 0.5), want: "allocation weights sum to 1.5"},
		{name: "zero weight", args: args(1, 0), want: "weight of SCHD must be > 0"},
		{name: "empty allocations", args: map[string]any{"allocations": []map[string]any{}, "amount": 100, "start": "2022-01-01"}, want: "at least one symbol"},
		{name: "duplicate symbol", args: map[string]any{"allocations": []map[string]any{{"symbol": "VOO", "weight": 50}, {"symbol": "voo", "weight": 50}}, "amount": 100, "start": "2022-01-01"}, want: "listed twice"},
		{name: "unknown symbol", args: map[string]any{"allocations": []map[string]any{{"symbol": "VOO", "weight": 50}, {"symbol": "XYZ", "weight": 50}}, "amount": 100, "start": "2022-01-01"}, want: "unknown symbol XYZ"},
		{name: "bad date", args: map[string]any{"allocations": []map[string]any{{"symbol": "VOO", "weight": 1}}, "amount": 100, "start": "2022-13-01"}, want: "YYYY-MM-DD"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "simulate_portfolio_dca", tt.args, tt.want)
		})
	}
}
