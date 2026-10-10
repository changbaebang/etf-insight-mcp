package tools

import (
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestGetFundPerformance(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("returns and risk", func(t *testing.T) {
		var out getFundPerformanceOutput
		callOK(t, sess, "get_fund_performance", map[string]any{"symbol": "SCHD"}, &out)
		if out.Symbol != "SCHD" || len(out.Trailing) != 3 || len(out.Annual) != 3 || len(out.Risk) != 2 {
			t.Fatalf("out = %+v", out)
		}
		if r := out.Trailing[0]; r.Period != "ytd" || *r.FundPct != 21.46 {
			t.Errorf("ytd = %+v", r)
		}
		if r := out.Trailing[1]; r.Period != "1y" || *r.FundPct != 23.36 {
			t.Errorf("1y = %+v", r)
		}
		if r := out.Annual[1]; r.Year != 2022 || *r.FundPct != -3.22 || *r.CategoryPct != -5.95 {
			t.Errorf("2022 = %+v", r)
		}
		r := out.Risk[0]
		if r.Period != "3y" || *r.Alpha != 1.4 || *r.Beta != 0.56 || *r.StdDevPct != 13.75 || *r.MeanMonthlyReturnPct != 1.28 || *r.RSquared != 25.04 || *r.Sharpe != 0.79 || *r.Treynor != 19.49 {
			t.Errorf("3y risk = %+v, want provider units rounded, not scaled", r)
		}
		if out.Risk[1].Beta != nil || *out.Risk[1].Alpha != -0.74 {
			t.Errorf("5y risk = %+v", out.Risk[1])
		}
		if out.Disclaimer != Disclaimer || out.FetchedAt == "" {
			t.Errorf("disclaimer/fetched_at = %q/%q", out.Disclaimer, out.FetchedAt)
		}
	})

	t.Run("annual list keeps the most recent years", func(t *testing.T) {
		var out getFundPerformanceOutput
		callOK(t, sess, "get_fund_performance", map[string]any{"symbol": "BIGF"}, &out)
		if len(out.Annual) != maxAnnualReturnRows || out.Annual[0].Year != 1999 || out.Annual[24].Year != 2023 {
			t.Errorf("annual = %d rows %d..%d, want 25 rows 1999..2023", len(out.Annual), out.Annual[0].Year, out.Annual[len(out.Annual)-1].Year)
		}
		if !hasDataNote(out.Notes, "30 calendar years of fund returns reported") || out.Trailing == nil || out.Risk == nil {
			t.Errorf("notes/trailing/risk = %v/%v/%v", out.Notes, out.Trailing, out.Risk)
		}
	})

	t.Run("unknown symbol", func(t *testing.T) {
		callErr(t, sess, "get_fund_performance", map[string]any{"symbol": "NOPE"}, "unknown symbol NOPE")
	})
}

// TestGetFundPerformanceReviewFixes pins the wire format the review asked
// for: no category figures in the trailing table (the provider measures
// them to an older date), the trailing returns' as-of date, the mean
// monthly return under an honest name, calendar years from the fund's
// first year, and the benchmark the risk statistics really use.
func TestGetFundPerformanceReviewFixes(t *testing.T) {
	sess, _, _ := newDataSession(t)
	out := callRaw(t, sess, "get_fund_performance", map[string]any{"symbol": "SCHD"})

	trailing, _ := out["trailing"].([]any)
	for _, r := range trailing {
		if _, ok := r.(map[string]any)["category_pct"]; ok {
			t.Errorf("trailing row %v carries category_pct, measured to another date than fund_pct", r)
		}
	}
	if !hasDataNote(rawStrings(out["notes"]), "calendar years only") {
		t.Errorf("notes = %v, want why trailing category figures are left out", out["notes"])
	}
	if out["trailing_as_of"] != "2023-12-31" {
		t.Errorf("trailing_as_of = %v, want 2023-12-31", out["trailing_as_of"])
	}

	risk, _ := out["risk"].([]any)
	row, _ := risk[0].(map[string]any)
	if _, ok := row["mean_annual_return_pct"]; ok {
		t.Errorf("risk row %v still names a monthly mean mean_annual_return_pct", row)
	}
	if row["mean_monthly_return_pct"] != 1.28 {
		t.Errorf("mean_monthly_return_pct = %v, want 1.28", row["mean_monthly_return_pct"])
	}

	annual, _ := out["annual"].([]any)
	if first, _ := annual[0].(map[string]any); first["year"] != float64(2021) || len(annual) != 3 {
		t.Errorf("annual = %v, want 2021..2023: years before the fund's first return are dropped", annual)
	}

	for _, field := range []string{"alpha", "beta", "r_squared"} {
		desc := schemaDescription(t, sess, "get_fund_performance", "risk", field)
		if !strings.Contains(desc, "S&P 500") || strings.Contains(desc, "category benchmark") {
			t.Errorf("%s description = %q, want the provider's standard index (S&P 500), not the category benchmark", field, desc)
		}
	}
}

func TestGetFundPerformanceWithoutAsOf(t *testing.T) {
	d := testDeps(newFakeSource())
	out := d.toFundPerformanceOutput("X", &market.Performance{Trailing: []market.PeriodReturn{{Period: "1y", Fund: ptr(0.1)}}})
	if out.TrailingAsOf != "" || !hasDataNote(out.Notes, "last month-end") {
		t.Errorf("trailing_as_of/notes = %q/%v, want empty with a month-end caution", out.TrailingAsOf, out.Notes)
	}
	if len(out.Notes) != 1 {
		t.Errorf("notes = %v, want only the as-of caution: no category figure was dropped", out.Notes)
	}
}
