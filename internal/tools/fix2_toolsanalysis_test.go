package tools

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// The tests in this file pin the second review round's fixes to the
// analysis tools. Where a field is new or became nullable they decode
// into a generic JSON object, so each assertion reads like the JSON a
// client receives.

// fix2Rows returns the objects of the array field key of out.
func fix2Rows(t *testing.T, out map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := out[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, want an array", key, out[key])
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, r.(map[string]any))
	}
	return rows
}

func TestFix2CompareETFsRejectsNonUSDFunds(t *testing.T) {
	src := newAnalysisSource()
	krw := analysisDerived("360750.KS", src.series["VOO"], time.Time{}, time.Time{}, func(_ int, p float64) float64 { return 40 * p })
	krw.Meta.Currency = "KRW"
	src.add(krw)
	sess := newSession(t, analysisDeps(src, newAnalysisFakeFund()))

	callErr(t, sess, "compare_etfs", map[string]any{"symbols": []string{"360750.KS", "VOO"}}, "360750.KS is quoted in KRW")
	callErr(t, sess, "compare_etfs", map[string]any{"symbols": []string{"VOO", "IVV"}, "benchmark": "360750.KS"}, "quoted in KRW")
	callErr(t, sess, "find_alternatives", map[string]any{"symbol": "360750.KS"}, "360750.KS is quoted in KRW")
}

func TestFix2CompareETFsNotesAnIndexBenchmark(t *testing.T) {
	src := newAnalysisSource()
	index := analysisDerived("^GSPC", src.series["SPY"], time.Time{}, time.Time{}, func(_ int, p float64) float64 { return 10 * p })
	index.Meta.InstrumentType = "INDEX"
	src.add(index)
	sess := newSession(t, analysisDeps(src, newAnalysisFakeFund()))

	var out compareETFsOutput
	callOK(t, sess, "compare_etfs", map[string]any{"symbols": []string{"VOO", "IVV"}, "benchmark": "^GSPC"}, &out)
	if !slices.ContainsFunc(out.Notes, func(n string) bool { return strings.Contains(n, "^GSPC is an index") }) {
		t.Errorf("notes = %v, want the index caution", out.Notes)
	}
}

func TestFix2CompareETFsMeasuresEveryRowOverTheCommonRange(t *testing.T) {
	src := newAnalysisSource()
	sess := newSession(t, analysisDeps(src, newAnalysisFakeFund()))

	// RSP is VOO scaled and starts on 2023-06-01, so over the common range
	// both rows must show the same return and drawdown even though VOO's
	// own history reaches back to 2021.
	var out map[string]any
	callOK(t, sess, "compare_etfs", map[string]any{"symbols": []string{"VOO", "RSP"}}, &out)
	rows := fix2Rows(t, out, "etfs")
	if len(rows) != 3 {
		t.Fatalf("%d rows, want SPY, VOO and RSP", len(rows))
	}
	voo, rsp := rows[1], rows[2]
	for _, row := range rows {
		for _, w := range row["returns"].([]any) {
			if label := w.(map[string]any)["label"]; label == "max" {
				t.Errorf("%s still reports the 'max' window, which covers its own history rather than the common range", row["symbol"])
			}
		}
		if _, ok := row["max_drawdown_all_pct"]; ok {
			t.Errorf("%s still reports max_drawdown_all_pct", row["symbol"])
		}
		if ann, ok := row["common_range_annualized_pct"]; !ok || ann != nil {
			t.Errorf("%s common_range_annualized_pct = %v (present %v), want null for a range under a year", row["symbol"], ann, ok)
		}
	}
	for _, key := range []string{"common_range_return_pct", "max_drawdown_common_range_pct"} {
		v, r := voo[key], rsp[key]
		if v == nil || v != r {
			t.Errorf("%s VOO/RSP = %v/%v, want equal values (identical returns over the same days)", key, v, r)
		}
	}
	_, aligned := analytics.CommonRange([]*market.Series{src.series["SPY"], src.series["VOO"], src.series["RSP"]}, time.Time{}, time.Time{})
	if got, want := voo["max_drawdown_common_range_pct"], pct(analytics.MaxDrawdown(aligned[1])); got != want {
		t.Errorf("VOO max_drawdown_common_range_pct = %v, want %v", got, want)
	}
	if got, want := voo["common_range_return_pct"], pct(aligned[1][len(aligned[1])-1]/aligned[1][0]-1); got != want {
		t.Errorf("VOO common_range_return_pct = %v, want %v", got, want)
	}
}

func TestFix2FindAlternativesDifferentExposureIgnoresMinCorrelation(t *testing.T) {
	src := newAnalysisSource()
	sess := newSession(t, analysisDeps(src, newAnalysisFakeFund()))

	var base findAlternativesOutput
	callOK(t, sess, "find_alternatives", map[string]any{"symbol": "VOO"}, &base)
	if len(base.DifferentExposure) == 0 {
		t.Fatal("the default call lists no different_exposure fund")
	}

	// A low threshold matches every fund; the least correlated ones are
	// cut by limit and must still appear as different exposure.
	var low findAlternativesOutput
	callOK(t, sess, "find_alternatives", map[string]any{"symbol": "VOO", "min_correlation": -1, "limit": 3}, &low)
	if got, want := analysisCandidateSymbols(low.DifferentExposure), analysisCandidateSymbols(base.DifferentExposure); !slices.Equal(got, want) {
		t.Errorf("different_exposure with min_correlation -1 = %v, want %v as in the default call", got, want)
	}

	// A high threshold must not fill the list with near-identical funds.
	var high findAlternativesOutput
	callOK(t, sess, "find_alternatives", map[string]any{"symbol": "VOO", "min_correlation": 0.9999}, &high)
	for _, c := range high.DifferentExposure {
		if c.CorrelationToBase >= altDifferentMaxCorrelation {
			t.Errorf("different_exposure holds %s at corr %v", c.Symbol, c.CorrelationToBase)
		}
	}
	for _, out := range []findAlternativesOutput{base, low, high} {
		for _, c := range out.DifferentExposure {
			if slices.Contains(analysisCandidateSymbols(out.Candidates), c.Symbol) {
				t.Errorf("%s is both a candidate and different exposure", c.Symbol)
			}
		}
	}
}

func TestFix2FindAlternativesAnnualizesFromAFullYear(t *testing.T) {
	src := newAnalysisSource()
	// 2023-01-02 to 2023-12-29 is 361 days: under a year.
	src.add(analysisDerived("SCHX", src.series["VOO"], time.Date(2023, time.January, 2, 0, 0, 0, 0, time.UTC), time.Time{}, func(_ int, p float64) float64 { return 0.15 * p }))
	sess := newSession(t, analysisDeps(src, newAnalysisFakeFund()))

	var out findAlternativesOutput
	callOK(t, sess, "find_alternatives", map[string]any{"symbol": "VOO", "limit": 20}, &out)
	schx := analysisCandidate(t, out.Candidates, "SCHX")
	if schx.Return3YAnnualizedPct != nil || schx.TrackingDifference3YPct != nil {
		t.Errorf("SCHX over %s..%s reports an annualized return or tracking difference, want null under 365 days",
			schx.CommonRange.From, schx.CommonRange.To)
	}
}

func TestFix2AltObservationDoesNotRoundToPerfectCorrelation(t *testing.T) {
	c := alternativeCandidate{Symbol: "SPYM", CorrelationToBase: 0.9996}
	got := altObservation(c, altMeasured{correlationRank: 1}, "VOO", "US Large Cap", 0, altSpan{})
	if !strings.Contains(got, "corr 0.9996") {
		t.Errorf("observation = %q, want corr 0.9996", got)
	}
}

func TestFix2TechnicalIndicatorsWithoutHistoryAreNull(t *testing.T) {
	src := newAnalysisSource()
	sess := newSession(t, analysisDeps(src, nil))
	voo := src.series["VOO"]

	var young map[string]any
	callOK(t, sess, "get_technical_indicators", map[string]any{"symbol": "VOO", "as_of": formatDate(voo.Bars[26].Date)}, &young)
	for _, key := range []string{"macd_signal", "macd_hist", "sma_50", "sma_200"} {
		if v, ok := young[key]; !ok || v != nil {
			t.Errorf("27 bars: %s = %v (present %v), want null", key, v, ok)
		}
	}
	for _, key := range []string{"rsi_14", "macd", "bollinger_pct_b", "atr_14", "sma_20", "ema_12", "ema_26"} {
		if young[key] == nil {
			t.Errorf("27 bars: %s is null, want a value", key)
		}
	}

	var first map[string]any
	callOK(t, sess, "get_technical_indicators", map[string]any{"symbol": "VOO", "as_of": formatDate(voo.Bars[0].Date)}, &first)
	if v, ok := first["rsi_14"]; !ok || v != nil {
		t.Errorf("first bar: rsi_14 = %v (present %v), want null rather than an 'oversold' 0", v, ok)
	}
}

func TestFix2ScreenUniverseStopsWhenCancelled(t *testing.T) {
	deps := analysisDeps(newAnalysisSource(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := deps.screenUniverse(ctx, screenUniverseInput{SortBy: "return_1y"}); err == nil {
		t.Errorf("cancelled screen returned a ranking of %d funds, want the context error", len(out.Ranked))
	}
	if _, err := deps.findAlternatives(ctx, findAlternativesInput{Symbol: "VOO"}); err == nil {
		t.Error("cancelled find_alternatives returned an answer, want the context error")
	}
}

func TestFix2ScreenUniverseCapsSkipped(t *testing.T) {
	src := newAnalysisSource()
	sess := newSession(t, analysisDeps(src, nil))

	// The fake knows only a handful of universe funds, so most are skipped.
	var out map[string]any
	callOK(t, sess, "screen_universe", map[string]any{"sort_by": "return_1y", "limit": 5}, &out)
	skipped := fix2Rows(t, out, "skipped")
	count, _ := out["skipped_count"].(float64)
	if len(skipped) > screenMaxSkipped || int(count) <= screenMaxSkipped || math.IsNaN(count) {
		t.Errorf("skipped lists %d entries with skipped_count %v, want at most %d listed and the full count", len(skipped), out["skipped_count"], screenMaxSkipped)
	}
}
