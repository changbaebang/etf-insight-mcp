package tools

import (
	"strings"
	"testing"
)

func TestGetFundProfile(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("full profile", func(t *testing.T) {
		var out getFundProfileOutput
		callOK(t, sess, "get_fund_profile", map[string]any{"symbol": " voo"}, &out)
		if out.Symbol != "VOO" || out.ExpenseRatioPct == nil || *out.ExpenseRatioPct != 0.03 || out.AnnualCostPer10K == nil || *out.AnnualCostPer10K != 3 {
			t.Errorf("symbol/expense = %s/%v/%v, want VOO 0.03%% costing 3 per 10k", out.Symbol, out.ExpenseRatioPct, out.AnnualCostPer10K)
		}
		if out.Family != "Vanguard" || out.Category != "Large Blend" || out.LegalType != "Exchange Traded Fund" || out.InceptionDate != "2010-09-07" {
			t.Errorf("descriptive fields = %+v", out)
		}
		if out.TurnoverPct == nil || *out.TurnoverPct != 2 || out.YieldPct == nil || *out.YieldPct != 1.36 || out.NetAssets == nil || *out.NetAssets != 1.0123456789e12 {
			t.Errorf("turnover/yield/net assets = %v/%v/%v", out.TurnoverPct, out.YieldPct, out.NetAssets)
		}
		if !out.InUniverse || out.Universe == nil || out.Universe.Category != "US Large Cap" {
			t.Errorf("universe = %v/%+v", out.InUniverse, out.Universe)
		}
		if out.FetchedAt != "2024-01-03T08:00:00Z" || len(out.Warnings) != 0 {
			t.Errorf("fetched_at/warnings = %s/%v", out.FetchedAt, out.Warnings)
		}
		if !hasDataNote(out.Notes, "already net of the expense ratio") {
			t.Errorf("notes = %v", out.Notes)
		}
	})

	t.Run("expense ratio keeps thousandths of a percent", func(t *testing.T) {
		var out getFundProfileOutput
		callOK(t, sess, "get_fund_profile", map[string]any{"symbol": "SPY"}, &out)
		if out.ExpenseRatioPct == nil || *out.ExpenseRatioPct != 0.0945 || *out.AnnualCostPer10K != 9.45 {
			t.Errorf("expense = %v/%v, want 0.0945 and 9.45", out.ExpenseRatioPct, out.AnnualCostPer10K)
		}
	})

	t.Run("stale document is flagged", func(t *testing.T) {
		var out getFundProfileOutput
		callOK(t, sess, "get_fund_profile", map[string]any{"symbol": "BND"}, &out)
		if len(out.Warnings) != 1 || !hasDataNote(out.Warnings, "fetched 5 days ago") {
			t.Errorf("warnings = %v, want one '5 days ago'", out.Warnings)
		}
	})

	t.Run("not a fund", func(t *testing.T) {
		res := call(t, sess, "get_fund_profile", map[string]any{"symbol": "AAPL"})
		if res.IsError {
			t.Fatalf("AAPL: %s", textOf(res))
		}
		var out getFundProfileOutput
		callOK(t, sess, "get_fund_profile", map[string]any{"symbol": "AAPL"}, &out)
		if out.ExpenseRatioPct != nil || out.InUniverse || !hasDataNote(out.Notes, "no expense ratio") || !hasDataNote(out.Notes, "may not be a fund") {
			t.Errorf("out = %+v", out)
		}
		if raw := textOf(res); !strings.Contains(raw, `"expense_ratio_pct":null`) {
			t.Errorf("expense_ratio_pct must be echoed even when unknown: %s", raw)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown symbol", args: map[string]any{"symbol": "NOPE"}, want: "unknown symbol NOPE: not in universe and the data source returned not found; use list_etfs"},
		{name: "universe symbol missing upstream", args: map[string]any{"symbol": "VTI"}, want: "in the universe but the data source returned not found"},
		{name: "empty symbol", args: map[string]any{"symbol": " "}, want: "symbol is required"},
		{name: "missing symbol", args: map[string]any{}, want: "symbol"},
		{name: "upstream failure", args: map[string]any{"symbol": "BOOM"}, want: "fetching the fund profile for BOOM failed: connection reset by peer"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_fund_profile", tt.args, tt.want)
		})
	}
}
