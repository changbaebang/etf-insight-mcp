package tools

import "testing"

func TestGetFundPerformance(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("returns and risk", func(t *testing.T) {
		var out getFundPerformanceOutput
		callOK(t, sess, "get_fund_performance", map[string]any{"symbol": "SCHD"}, &out)
		if out.Symbol != "SCHD" || len(out.Trailing) != 3 || len(out.Annual) != 3 || len(out.Risk) != 2 {
			t.Fatalf("out = %+v", out)
		}
		if r := out.Trailing[0]; r.Period != "ytd" || *r.FundPct != 21.46 || *r.CategoryPct != 18 {
			t.Errorf("ytd = %+v", r)
		}
		if r := out.Trailing[1]; r.Period != "1y" || *r.FundPct != 23.36 || r.CategoryPct != nil {
			t.Errorf("1y = %+v, want an unreported category", r)
		}
		if r := out.Annual[1]; r.Year != 2022 || *r.FundPct != -3.22 || *r.CategoryPct != -5.95 {
			t.Errorf("2022 = %+v", r)
		}
		r := out.Risk[0]
		if r.Period != "3y" || *r.Alpha != 1.4 || *r.Beta != 0.56 || *r.StdDevPct != 13.75 || *r.MeanAnnualReturnPct != 1.28 || *r.RSquared != 25.04 || *r.Sharpe != 0.79 || *r.Treynor != 19.49 {
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
		if !hasDataNote(out.Notes, "30 calendar years reported") || out.Trailing == nil || out.Risk == nil {
			t.Errorf("notes/trailing/risk = %v/%v/%v", out.Notes, out.Trailing, out.Risk)
		}
	})

	t.Run("unknown symbol", func(t *testing.T) {
		callErr(t, sess, "get_fund_performance", map[string]any{"symbol": "NOPE"}, "unknown symbol NOPE")
	})
}
