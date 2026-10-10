package tools

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
)

// analysisCandidateSymbols lists the symbols of candidates in order.
func analysisCandidateSymbols(list []alternativeCandidate) []string {
	syms := make([]string, 0, len(list))
	for _, c := range list {
		syms = append(syms, c.Symbol)
	}
	return syms
}

// analysisCandidate returns the candidate for sym or fails the test.
func analysisCandidate(t *testing.T, list []alternativeCandidate, sym string) alternativeCandidate {
	t.Helper()
	i := slices.IndexFunc(list, func(c alternativeCandidate) bool { return c.Symbol == sym })
	if i < 0 {
		t.Fatalf("%s is not among %v", sym, analysisCandidateSymbols(list))
	}
	return list[i]
}

func TestFindAlternatives(t *testing.T) {
	src := newAnalysisSource()
	fund := newAnalysisFakeFund()
	sess := newSession(t, analysisDeps(src, fund))

	var out findAlternativesOutput
	callOK(t, sess, "find_alternatives", map[string]any{"symbol": "voo"}, &out)

	t.Run("base", func(t *testing.T) {
		b := out.Base
		if b.Symbol != "VOO" || b.Name != "Vanguard S&P 500 ETF" || b.Category != "US Large Cap" || b.AsOf != "2023-12-29" {
			t.Errorf("base = %+v", b)
		}
		sum := analysisMust(analytics.Summarize(src.series["VOO"], date(t, b.AsOf)))(t)
		if b.ExpenseRatioPct == nil || *b.ExpenseRatioPct != 0.03 || b.Return1YPct == nil || b.Return5YAnnualizedPct != nil {
			t.Errorf("base expense/1y/5y = %v/%v/%v, want 0.03, a 1-year return and no 5-year figure", b.ExpenseRatioPct, b.Return1YPct, b.Return5YAnnualizedPct)
		}
		if b.TTMDividendYieldPct != pct(sum.TTMDividendYield) || b.Volatility1YPct != pct(sum.Volatility1Y) || b.MaxDrawdownAllPct != pct(sum.MaxDrawdownAll) {
			t.Errorf("base figures = %+v, want the analytics summary", b)
		}
		if out.CommonRange != (analysisRange{From: "2021-01-04", To: "2023-12-29", Bars: 780}) {
			t.Errorf("common_range = %+v", out.CommonRange)
		}
		if !slices.Equal(out.CategoriesSearched, []string{"US Large Cap", "US Broad Market", "US Large Growth", "US Large Value", "Dividend", "Factor"}) {
			t.Errorf("categories = %v", out.CategoriesSearched)
		}
		if out.MinCorrelation != 0.9 || out.Disclaimer != Disclaimer {
			t.Errorf("min_correlation/disclaimer = %v/%q", out.MinCorrelation, out.Disclaimer)
		}
		if !slices.ContainsFunc(out.Notes, func(n string) bool { return strings.Contains(n, "already net of each fund's expense ratio") }) {
			t.Errorf("notes = %v, want the expense ratio note", out.Notes)
		}
	})

	t.Run("ranking: correlation to 3 decimals, then cheaper first", func(t *testing.T) {
		want := []string{"SPYM", "IVV", "MTUM", "DIA", "RSP", "DFAC", "QQQ"}
		if got := analysisCandidateSymbols(out.Candidates); !slices.Equal(got, want) {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
		if out.Matched != 7 || out.Considered != 9 {
			t.Errorf("matched/considered = %d/%d, want 7/9", out.Matched, out.Considered)
		}
		for _, c := range out.Candidates[:6] {
			if c.CorrelationToBase != 1 {
				t.Errorf("%s correlation = %v, want 1 (a scaled or squared copy of VOO)", c.Symbol, c.CorrelationToBase)
			}
		}
		qqq := analysisCandidate(t, out.Candidates, "QQQ")
		if qqq.CorrelationToBase < 0.9 || qqq.CorrelationToBase >= 0.995 {
			t.Errorf("QQQ correlation = %v, want high but not perfect", qqq.CorrelationToBase)
		}
	})

	t.Run("identical returns", func(t *testing.T) {
		ivv := analysisCandidate(t, out.Candidates, "IVV")
		if ivv.BetaToBase != 1 || ivv.TrackingDifference3YPct == nil || *ivv.TrackingDifference3YPct != 0 || ivv.ShortHistory {
			t.Errorf("IVV = %+v, want beta 1 and no tracking difference", ivv)
		}
		if ivv.ExpenseRatioPct == nil || *ivv.ExpenseRatioPct != 0.03 || ivv.ExpenseRatioDiffPctPoints == nil || *ivv.ExpenseRatioDiffPctPoints != 0 {
			t.Errorf("IVV expense = %v/%v", ivv.ExpenseRatioPct, ivv.ExpenseRatioDiffPctPoints)
		}
		if ivv.Observation != "practically the same exposure (corr 1.0000), same expense ratio" {
			t.Errorf("IVV observation = %q", ivv.Observation)
		}
		spym := analysisCandidate(t, out.Candidates, "SPYM")
		if spym.ExpenseRatioDiffPctPoints == nil || *spym.ExpenseRatioDiffPctPoints != -0.01 || !strings.Contains(spym.Observation, "lower expense ratio by 0.01 pp") {
			t.Errorf("SPYM diff/observation = %v/%q", spym.ExpenseRatioDiffPctPoints, spym.Observation)
		}
		if ivv.CommonRange != out.CommonRange || ivv.Return3YAnnualizedPct == nil {
			t.Errorf("IVV range/annualized = %+v/%v", ivv.CommonRange, ivv.Return3YAnnualizedPct)
		}
	})

	t.Run("squared prices: beta 2 and the tracking difference", func(t *testing.T) {
		mtum := analysisCandidate(t, out.Candidates, "MTUM")
		if mtum.BetaToBase != 2 || mtum.Category != "Factor" {
			t.Errorf("MTUM beta/category = %v/%s, want 2/Factor", mtum.BetaToBase, mtum.Category)
		}
		// MTUM grows by (1+g)^2 a year when VOO grows by 1+g, so it beats
		// VOO by g + g^2.
		dates, voo, _ := analysisAligned(src.series["VOO"], src.series["MTUM"], date(t, out.CommonRange.From), date(t, out.CommonRange.To))
		g, ok := analysisAnnualized(voo, dates[0], dates[len(dates)-1])
		if !ok {
			t.Fatalf("VOO is not annualized over %s..%s", out.CommonRange.From, out.CommonRange.To)
		}
		if mtum.TrackingDifference3YPct == nil || math.Abs(*mtum.TrackingDifference3YPct-pct(g+g*g)) > 0.011 {
			t.Errorf("MTUM tracking difference = %v, want %v", mtum.TrackingDifference3YPct, pct(g+g*g))
		}
		for _, w := range []string{"Factor fund", "beta 2.00 to VOO", "higher expense ratio by 0.12 pp", "pp vs VOO", "deeper drawdown"} {
			if !strings.Contains(mtum.Observation, w) {
				t.Errorf("MTUM observation %q lacks %q", mtum.Observation, w)
			}
		}
	})

	t.Run("short and stale histories are flagged", func(t *testing.T) {
		dfac := analysisCandidate(t, out.Candidates, "DFAC")
		if !dfac.ShortHistory || dfac.CommonRange.From != "2023-03-01" || dfac.ExpenseRatioPct != nil || dfac.ExpenseRatioDiffPctPoints != nil {
			t.Errorf("DFAC = %+v, want a short range from 2023-03-01 and no expense ratio", dfac)
		}
		if !strings.Contains(dfac.Observation, "only 0.8 years of common history") || !strings.Contains(dfac.Observation, "US Broad Market fund") {
			t.Errorf("DFAC observation = %q", dfac.Observation)
		}
		dia := analysisCandidate(t, out.Candidates, "DIA")
		if !dia.ShortHistory || dia.CommonRange.To != "2023-06-30" || dia.Return1YPct == nil {
			t.Errorf("DIA = %+v, want a range ending 2023-06-30", dia)
		}
		rsp := analysisCandidate(t, out.Candidates, "RSP")
		if rsp.Return3YAnnualizedPct != nil || rsp.TrackingDifference3YPct != nil || rsp.Return1YPct != nil {
			t.Errorf("RSP annualized/tracking/1y = %v/%v/%v, want null under a year", rsp.Return3YAnnualizedPct, rsp.TrackingDifference3YPct, rsp.Return1YPct)
		}
	})

	t.Run("different exposure lists the least correlated", func(t *testing.T) {
		got := analysisCandidateSymbols(out.DifferentExposure)
		if len(got) != 2 || !slices.Contains(got, "SPY") || !slices.Contains(got, "SCHD") {
			t.Fatalf("different_exposure = %v, want SPY and SCHD", got)
		}
		if out.DifferentExposure[0].CorrelationToBase > out.DifferentExposure[1].CorrelationToBase {
			t.Errorf("different_exposure not ascending: %v", out.DifferentExposure)
		}
		for _, c := range out.DifferentExposure {
			if c.CorrelationToBase >= 0.9 || !strings.HasPrefix(c.Observation, "different exposure") || c.ExpenseRatioPct == nil {
				t.Errorf("%s = %+v", c.Symbol, c)
			}
		}
	})

	t.Run("unknown and unmeasurable funds are skipped", func(t *testing.T) {
		if r := analysisSkippedReason(out.Skipped, "COWZ"); !strings.Contains(r, "only 10 trading days in common with VOO") {
			t.Errorf("COWZ reason = %q", r)
		}
		if r := analysisSkippedReason(out.Skipped, "VTI"); !strings.Contains(r, "not found") {
			t.Errorf("VTI reason = %q", r)
		}
		pool := 0
		for _, c := range out.CategoriesSearched {
			pool += len(universe.Filter(universe.Query{Category: c}))
		}
		if pool-1 != out.Considered+len(out.Skipped) { // VOO itself is not a candidate
			t.Errorf("considered %d + skipped %d != pool %d", out.Considered, len(out.Skipped), pool-1)
		}
		if slices.Contains(analysisCandidateSymbols(out.Candidates), "SSO") || analysisSkippedReason(out.Skipped, "SSO") != "" {
			t.Errorf("leveraged SSO was considered")
		}
		if len(out.Warnings) != 0 {
			t.Errorf("warnings = %v, want none", out.Warnings)
		}
	})

	t.Run("expense ratios are looked up only for listed funds", func(t *testing.T) {
		if fund.lookups("VOO") != 1 || fund.lookups("QQQ") != 1 || fund.lookups("SCHD") != 1 {
			t.Errorf("lookups VOO/QQQ/SCHD = %d/%d/%d, want 1 each", fund.lookups("VOO"), fund.lookups("QQQ"), fund.lookups("SCHD"))
		}
		if fund.lookups("COWZ") != 0 || fund.lookups("VTI") != 0 {
			t.Errorf("skipped funds were looked up")
		}
	})

	t.Run("options narrow the answer", func(t *testing.T) {
		var narrow findAlternativesOutput
		callOK(t, sess, "find_alternatives", map[string]any{"symbol": "VOO", "include_other_categories": false, "limit": 1, "min_correlation": 0.99}, &narrow)
		if !slices.Equal(narrow.CategoriesSearched, []string{"US Large Cap"}) || !slices.Equal(analysisCandidateSymbols(narrow.Candidates), []string{"SPYM"}) {
			t.Errorf("categories/candidates = %v/%v", narrow.CategoriesSearched, analysisCandidateSymbols(narrow.Candidates))
		}
		if narrow.Matched != 4 || slices.Contains(analysisCandidateSymbols(narrow.DifferentExposure), "MTUM") {
			t.Errorf("matched = %d, want SPYM, IVV, DIA and RSP", narrow.Matched)
		}
		var strict findAlternativesOutput
		callOK(t, sess, "find_alternatives", map[string]any{"symbol": "VOO", "min_correlation": 0.999}, &strict)
		// QQQ (corr about 0.95) misses the threshold but is too similar for
		// different_exposure, which keeps SPY and SCHD as in the default call.
		if slices.Contains(analysisCandidateSymbols(strict.Candidates), "QQQ") || !slices.Equal(analysisCandidateSymbols(strict.DifferentExposure), []string{"SPY", "SCHD"}) {
			t.Errorf("min_correlation 0.999: candidates %v, different_exposure %v, want no QQQ and SPY, SCHD", analysisCandidateSymbols(strict.Candidates), analysisCandidateSymbols(strict.DifferentExposure))
		}
	})

	t.Run("bond base searches bond categories", func(t *testing.T) {
		var bond findAlternativesOutput
		callOK(t, sess, "find_alternatives", map[string]any{"symbol": "BND"}, &bond)
		if bond.CategoriesSearched[0] != "US Aggregate Bond" || slices.Contains(bond.CategoriesSearched, "US Large Cap") {
			t.Errorf("categories = %v", bond.CategoriesSearched)
		}
		if !slices.Equal(analysisCandidateSymbols(bond.Candidates), []string{"AGG"}) || bond.Candidates[0].CorrelationToBase != 1 {
			t.Errorf("candidates = %+v, want AGG with correlation 1", bond.Candidates)
		}
		if !slices.Equal(analysisCandidateSymbols(bond.DifferentExposure), []string{"TLT"}) {
			t.Errorf("different_exposure = %v, want TLT", analysisCandidateSymbols(bond.DifferentExposure))
		}
	})

	t.Run("symbol outside the universe searches stocks and bonds", func(t *testing.T) {
		var outside findAlternativesOutput
		callOK(t, sess, "find_alternatives", map[string]any{"symbol": "KRW=X"}, &outside)
		if outside.Base.Category != "" || !slices.Contains(outside.CategoriesSearched, "Factor") || !slices.Contains(outside.CategoriesSearched, "US Treasury") {
			t.Errorf("base/categories = %+v/%v", outside.Base, outside.CategoriesSearched)
		}
		if !slices.ContainsFunc(outside.Notes, func(n string) bool { return strings.Contains(n, "not in the built-in universe") }) {
			t.Errorf("notes = %v", outside.Notes)
		}
		callErr(t, sess, "find_alternatives", map[string]any{"symbol": "KRW=X", "include_other_categories": false}, "set include_other_categories to true")
	})

	t.Run("a leveraged base is compared with the broad categories", func(t *testing.T) {
		var lev findAlternativesOutput
		callOK(t, sess, "find_alternatives", map[string]any{"symbol": "SSO"}, &lev)
		spy := analysisCandidate(t, lev.Candidates, "SPY")
		if spy.CorrelationToBase != 1 || spy.BetaToBase != 0.5 || !strings.Contains(spy.Observation, "beta 0.50 to SSO") {
			t.Errorf("SPY against SSO = %+v, want correlation 1 and beta 0.5", spy)
		}
		if slices.Contains(lev.CategoriesSearched, leveragedCategory) {
			t.Errorf("categories = %v, want no leveraged category", lev.CategoriesSearched)
		}
	})

	t.Run("a young base compares over its whole history", func(t *testing.T) {
		var young findAlternativesOutput
		callOK(t, sess, "find_alternatives", map[string]any{"symbol": "SCHD"}, &young)
		if young.CommonRange.From != "2022-01-03" || !slices.ContainsFunc(young.Notes, func(n string) bool { return strings.Contains(n, "SCHD has only 2.0 years of history") }) {
			t.Errorf("range/notes = %+v/%v", young.CommonRange, young.Notes)
		}
	})

	t.Run("expense ratio failures are warnings", func(t *testing.T) {
		failing := newAnalysisFakeFund()
		failing.fail["QQQ"] = true
		var partial findAlternativesOutput
		callOK(t, newSession(t, analysisDeps(src, failing)), "find_alternatives", map[string]any{"symbol": "VOO"}, &partial)
		if qqq := analysisCandidate(t, partial.Candidates, "QQQ"); qqq.ExpenseRatioPct != nil {
			t.Errorf("QQQ expense = %v, want null", *qqq.ExpenseRatioPct)
		}
		if len(partial.Warnings) != 1 || !strings.Contains(partial.Warnings[0], "failed for QQQ") {
			t.Errorf("warnings = %v", partial.Warnings)
		}
		var none findAlternativesOutput
		callOK(t, newSession(t, analysisDeps(src, nil)), "find_alternatives", map[string]any{"symbol": "VOO"}, &none)
		if none.Base.ExpenseRatioPct != nil || len(none.Warnings) != 1 || !strings.Contains(none.Warnings[0], "no fund data source") {
			t.Errorf("without a fund source: expense %v, warnings %v", none.Base.ExpenseRatioPct, none.Warnings)
		}
		// Without expense ratios the tie at correlation 1 falls back to the
		// symbol order.
		if got := analysisCandidateSymbols(none.Candidates)[:6]; !slices.Equal(got, []string{"DFAC", "DIA", "IVV", "MTUM", "RSP", "SPYM"}) {
			t.Errorf("tie order without expense ratios = %v", got)
		}
	})

	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown symbol", map[string]any{"symbol": "XYZ"}, "unknown symbol XYZ"},
		{"leveraged base in its own category", map[string]any{"symbol": "SSO", "include_other_categories": false}, "SSO's category Leveraged / Inverse holds no other non-leveraged fund to compare with"},
		{"limit too large", map[string]any{"symbol": "VOO", "limit": 21}, "limit"},
		{"correlation out of range", map[string]any{"symbol": "VOO", "min_correlation": 1.5}, "min_correlation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "find_alternatives", tt.args, tt.want)
		})
	}

	t.Run("category lists match the universe", func(t *testing.T) {
		for _, c := range slices.Concat(altEquityCategories, altBondCategories) {
			if !slices.Contains(universe.Categories(), c) {
				t.Errorf("category %q is not in the universe", c)
			}
		}
	})

	t.Run("handler validates without the schema", func(t *testing.T) {
		d := analysisDeps(src, nil)
		if _, err := d.findAlternatives(t.Context(), findAlternativesInput{Symbol: "VOO", Limit: -2}); err == nil || !strings.Contains(err.Error(), "between 1 and 20") {
			t.Errorf("limit error = %v", err)
		}
		if _, err := d.findAlternatives(t.Context(), findAlternativesInput{Symbol: "VOO", MinCorrelation: ptr(math.NaN())}); err == nil || !strings.Contains(err.Error(), "between -1 and 1") {
			t.Errorf("min_correlation error = %v", err)
		}
	})
}
