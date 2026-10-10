package tools

import (
	"strings"
	"testing"
	"time"
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

// TestGetFundProfileReviewFixes pins the caveats the review asked for:
// net assets of a multi-class fund, a price history that does not start at
// the inception date, and where the expense ratio comes from.
func TestGetFundProfileReviewFixes(t *testing.T) {
	sess, _, _ := newDataSession(t)

	voo := callRaw(t, sess, "get_fund_profile", map[string]any{"symbol": "VOO"})
	if !hasDataNote(rawStrings(voo["notes"]), "share classes") {
		t.Errorf("VOO notes = %v, want net assets explained as the whole fund's", voo["notes"])
	}
	if d := schemaDescription(t, sess, "get_fund_profile", "net_assets"); !strings.Contains(d, "all share classes") {
		t.Errorf("net_assets description = %q", d)
	}
	if d := schemaDescription(t, sess, "get_fund_profile", "expense_ratio_pct"); !strings.Contains(d, "annual report") {
		t.Errorf("expense_ratio_pct description = %q, want its source named", d)
	}

	qqq := callRaw(t, sess, "get_fund_profile", map[string]any{"symbol": "QQQ"})
	if qqq["price_history_from"] != "2021-01-04" {
		t.Errorf("price_history_from = %v, want 2021-01-04", qqq["price_history_from"])
	}
	notes := rawStrings(qqq["notes"])
	if !hasDataNote(notes, "2021-01-04") || !hasDataNote(notes, "2023-06-01") || !hasDataNote(notes, "predecessor") {
		t.Errorf("QQQ notes = %v, want the history before inception explained", notes)
	}
}

func TestInceptionNote(t *testing.T) {
	day := func(s string) time.Time { return date(t, s) }
	tests := []struct {
		name                   string
		inception, first, last string
		want                   []string // substrings; none means no note
		without                string   // a substring the note must not contain
	}{
		{name: "history before inception", inception: "2011-12-20", first: "2000-06-05", last: "2026-10-09",
			want: []string{"2000-06-05, 11.5 years before the 2011-12-20 inception date", "predecessor", "start simulations on or after 2011-12-20"}},
		{name: "inception near the end of the history", inception: "2026-10-02", first: "2021-06-14", last: "2026-10-09",
			want: []string{"5.3 years before the 2026-10-02 inception date"}, without: "start simulations"},
		{name: "history after inception", inception: "2010-09-07", first: "2021-01-04", last: "2023-12-29",
			want: []string{"10.3 years after the 2010-09-07 inception date", "since 2021-01-04"}},
		{name: "launch a few weeks apart", inception: "2020-01-02", first: "2020-02-12", last: "2023-12-29"},
		{name: "unknown inception", inception: "", first: "2020-02-12", last: "2023-12-29"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var inception time.Time
			if tt.inception != "" {
				inception = day(tt.inception)
			}
			got := inceptionNote(inception, day(tt.first), day(tt.last))
			if len(tt.want) == 0 && got != "" {
				t.Errorf("note = %q, want none", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("note = %q, want it to contain %q", got, w)
				}
			}
			if tt.without != "" && strings.Contains(got, tt.without) {
				t.Errorf("note = %q, want no %q", got, tt.without)
			}
		})
	}
}
