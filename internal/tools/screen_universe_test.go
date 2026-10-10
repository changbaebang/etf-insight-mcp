package tools

import (
	"cmp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
)

// analysisRankedSymbols lists the symbols of a screen in rank order.
func analysisRankedSymbols(out screenUniverseOutput) []string {
	syms := make([]string, 0, len(out.Ranked))
	for _, r := range out.Ranked {
		syms = append(syms, r.Symbol)
	}
	return syms
}

// analysisSkippedReason returns the reason sym was skipped, or "".
func analysisSkippedReason(skipped []analysisSkip, sym string) string {
	for _, s := range skipped {
		if s.Symbol == sym {
			return s.Reason
		}
	}
	return ""
}

func TestScreenUniverse(t *testing.T) {
	src := newAnalysisSource()
	sess := newSession(t, testDeps(src))
	largeCap := universe.Filter(universe.Query{Category: "US Large Cap"})

	t.Run("ranks one category by 1-year return", func(t *testing.T) {
		var out screenUniverseOutput
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "return_1y", "category": "us large cap"}, &out)
		if out.SortBy != "return_1y" || !out.Descending || out.Category != "US Large Cap" || out.Screened != len(largeCap) {
			t.Errorf("sort_by/descending/category/screened = %s/%v/%s/%d", out.SortBy, out.Descending, out.Category, out.Screened)
		}

		// The order is analytics.RankScores' on the funds the fake knows,
		// in universe order so ties keep that order.
		var scores []analytics.Score
		for _, e := range largeCap {
			if s, ok := src.series[e.Symbol]; ok && e.Symbol != "RSP" {
				scores = append(scores, analysisMust(analytics.ScoreSeries(s, time.Time{}))(t))
			}
		}
		ranked := analysisMust(analytics.RankScores(scores, analytics.RankByReturn1Y, true))(t)
		var want []string
		for _, sc := range ranked {
			want = append(want, sc.Symbol)
		}
		if got := analysisRankedSymbols(out); !slices.Equal(got, want) || len(got) != 5 {
			t.Fatalf("ranked = %v, want %v", got, want)
		}
		if out.Matched != 5 || out.Truncated {
			t.Errorf("matched/truncated = %d/%v, want 5/false", out.Matched, out.Truncated)
		}
		for i, r := range out.Ranked {
			if r.Rank != i+1 || r.Return1YPct == nil {
				t.Fatalf("row %d = %+v", i, r)
			}
			if i > 0 && *r.Return1YPct > *out.Ranked[i-1].Return1YPct {
				t.Errorf("row %d (%v) above row %d (%v) in a descending ranking", i, *r.Return1YPct, i-1, *out.Ranked[i-1].Return1YPct)
			}
			if r.Momentum121Pct == nil || r.Volatility1YPct == nil || r.MaxDrawdown1YPct == nil || r.DividendYieldPct == nil || r.PctVsSMA200 == nil || r.Return3MPct == nil || r.Trend == "" {
				t.Errorf("row %s has a missing metric: %+v", r.Symbol, r)
			}
			if r.Category != "US Large Cap" || r.Name == "" {
				t.Errorf("row %s name/category = %q/%q", r.Symbol, r.Name, r.Category)
			}
		}
		spy := out.Ranked[slices.IndexFunc(out.Ranked, func(r screenRow) bool { return r.Symbol == "SPY" })]
		score := analysisMust(analytics.ScoreSeries(src.series["SPY"], time.Time{}))(t)
		if *spy.Return1YPct != pct(score.Return1Y) || *spy.Momentum121Pct != pct(score.Momentum12_1) || *spy.PctVsSMA200 != round2(score.PctVsSMA200) || *spy.DividendYieldPct != pct(score.DividendYield) {
			t.Errorf("SPY row = %+v, want the analytics score %+v", spy, score)
		}

		if r := analysisSkippedReason(out.Skipped, "RSP"); !strings.Contains(r, "insufficient history for return_1y") {
			t.Errorf("RSP skip reason = %q", r)
		}
		for _, sym := range []string{"VV", "SCHX", "IWB"} {
			if r := analysisSkippedReason(out.Skipped, sym); !strings.Contains(r, "not found") {
				t.Errorf("%s skip reason = %q, want not found", sym, r)
			}
		}
		if len(out.Skipped) != 4 {
			t.Errorf("skipped = %+v, want RSP, VV, SCHX and IWB", out.Skipped)
		}
		if out.AsOf != "2023-12-29" || out.Disclaimer != Disclaimer {
			t.Errorf("as_of/disclaimer = %s/%q", out.AsOf, out.Disclaimer)
		}
		if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "DIA (2023-06-30)") {
			t.Errorf("warnings = %v, want DIA's old last bar", out.Warnings)
		}
	})

	t.Run("ascending puts the calmest first", func(t *testing.T) {
		var out screenUniverseOutput
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "volatility_1y", "descending": false, "category": "US Large Cap"}, &out)
		if out.Descending || len(out.Ranked) != 5 {
			t.Fatalf("descending/rows = %v/%d", out.Descending, len(out.Ranked))
		}
		if !slices.IsSortedFunc(out.Ranked, func(a, b screenRow) int { return cmp.Compare(*a.Volatility1YPct, *b.Volatility1YPct) }) {
			t.Errorf("volatility not ascending: %v", analysisRankedSymbols(out))
		}
	})

	t.Run("short metrics rank when they exist", func(t *testing.T) {
		var out screenUniverseOutput
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "return_3m", "category": "US Large Cap"}, &out)
		rsp := slices.IndexFunc(out.Ranked, func(r screenRow) bool { return r.Symbol == "RSP" })
		if rsp < 0 {
			t.Fatalf("RSP missing from a 3-month ranking: %v", analysisRankedSymbols(out))
		}
		if row := out.Ranked[rsp]; row.Return3MPct == nil || row.Return1YPct != nil || row.Momentum121Pct != nil || row.PctVsSMA200 != nil {
			t.Errorf("RSP row = %+v, want only the short metrics", row)
		}
	})

	t.Run("limit truncates", func(t *testing.T) {
		var out screenUniverseOutput
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "momentum_12_1", "category": "US Large Cap", "limit": 2}, &out)
		if len(out.Ranked) != 2 || out.Matched != 5 || !out.Truncated || out.Ranked[1].Rank != 2 {
			t.Errorf("rows/matched/truncated = %d/%d/%v", len(out.Ranked), out.Matched, out.Truncated)
		}
	})

	t.Run("whole universe without leveraged funds", func(t *testing.T) {
		var out screenUniverseOutput
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "dividend_yield", "limit": screenMaxLimit}, &out)
		if out.Screened != len(universe.Filter(universe.Query{})) || out.Category != "" {
			t.Errorf("screened/category = %d/%q", out.Screened, out.Category)
		}
		got := analysisRankedSymbols(out)
		for _, sym := range []string{"SPY", "VOO", "BND", "TLT", "MTUM", "QQQ"} {
			if !slices.Contains(got, sym) {
				t.Errorf("%s missing from %v", sym, got)
			}
		}
		if slices.Contains(got, "SSO") || analysisSkippedReason(out.Skipped, "SSO") != "" {
			t.Errorf("leveraged SSO was screened")
		}
		if out.Matched+out.SkippedCount != out.Screened || len(out.Skipped) != min(out.SkippedCount, screenMaxSkipped) {
			t.Errorf("matched %d + skipped_count %d != screened %d, or %d listed", out.Matched, out.SkippedCount, out.Screened, len(out.Skipped))
		}
		if r := analysisSkippedReason(out.Skipped, "VTI"); !strings.Contains(r, "VTI is in the universe but the data source returned not found") {
			t.Errorf("VTI skip reason = %q", r)
		}
	})

	t.Run("leveraged category includes leveraged funds", func(t *testing.T) {
		var out screenUniverseOutput
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "return_1y", "category": "leveraged / inverse"}, &out)
		if out.Category != leveragedCategory || !slices.Equal(analysisRankedSymbols(out), []string{"SSO"}) {
			t.Errorf("category/ranked = %s/%v, want only SSO", out.Category, analysisRankedSymbols(out))
		}
	})

	t.Run("as_of ranks the past", func(t *testing.T) {
		var out screenUniverseOutput
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "return_3m", "category": "Dividend", "as_of": "2022-06-18"}, &out)
		if out.AsOf != "2022-06-17" || !slices.Equal(analysisRankedSymbols(out), []string{"SCHD"}) {
			t.Errorf("as_of/ranked = %s/%v, want 2022-06-17/[SCHD]", out.AsOf, analysisRankedSymbols(out))
		}
		callOK(t, sess, "screen_universe", map[string]any{"sort_by": "return_3m", "category": "Dividend", "as_of": "2021-06-01"}, &out)
		if r := analysisSkippedReason(out.Skipped, "SCHD"); !strings.Contains(r, "no bar on or before 2021-06-01") {
			t.Errorf("SCHD skip reason before its history = %q", r)
		}
	})

	t.Run("prefetches through the cache once", func(t *testing.T) {
		counted := newAnalysisSource()
		store := cache.New(counted, t.TempDir(), time.Hour)
		deps := testDeps(store)
		deps.Cache = store
		cached := newSession(t, deps)
		var out screenUniverseOutput
		for range 2 {
			callOK(t, cached, "screen_universe", map[string]any{"sort_by": "return_1y", "category": "US Large Cap"}, &out)
		}
		for _, sym := range []string{"SPY", "VOO", "IVV", "SPYM", "RSP", "DIA"} {
			if n := counted.callCount(sym); n != 1 {
				t.Errorf("%s fetched %d times, want 1", sym, n)
			}
		}
		if len(out.Ranked) != 5 {
			t.Errorf("cached ranking has %d rows, want 5", len(out.Ranked))
		}
	})

	t.Run("stale cache warnings are capped", func(t *testing.T) {
		flaky := &analysisFlakySource{fakeSource: newAnalysisSource()}
		clock := time.Now()
		store := cache.New(flaky, t.TempDir(), time.Hour, cache.WithClock(func() time.Time { return clock }))
		deps := testDeps(store)
		deps.Cache = store
		stale := newSession(t, deps)
		var out screenUniverseOutput
		callOK(t, stale, "screen_universe", map[string]any{"sort_by": "return_1y", "category": "US Large Cap"}, &out)
		flaky.down.Store(true)
		clock = clock.Add(2 * time.Hour)
		callOK(t, stale, "screen_universe", map[string]any{"sort_by": "return_1y", "category": "US Large Cap"}, &out)
		if len(out.Ranked) != 5 {
			t.Errorf("stale ranking has %d rows, want 5 served from the cache", len(out.Ranked))
		}
		// DIA's old bar, five per-symbol cache warnings and the count of
		// the sixth.
		if len(out.Warnings) != 7 || !strings.Contains(out.Warnings[1], "may be stale") || out.Warnings[6] != "1 more cache warnings omitted; cache_status lists them all" {
			t.Errorf("warnings = %q", out.Warnings)
		}
	})

	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown sort key", map[string]any{"sort_by": "sharpe"}, "sort_by"},
		{"missing sort key", map[string]any{}, "sort_by"},
		{"unknown category", map[string]any{"sort_by": "return_1y", "category": "Crypto"}, "unknown category \"Crypto\"; valid categories: US Broad Market"},
		{"limit too large", map[string]any{"sort_by": "return_1y", "limit": screenMaxLimit + 1}, "limit"},
		{"bad as_of", map[string]any{"sort_by": "return_1y", "as_of": "yesterday"}, "YYYY-MM-DD"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "screen_universe", tt.args, tt.want)
		})
	}

	t.Run("handler validates without the schema", func(t *testing.T) {
		d := testDeps(src)
		if _, err := d.screenUniverse(t.Context(), screenUniverseInput{SortBy: "sharpe"}); err == nil || !strings.Contains(err.Error(), "use one of momentum_12_1") {
			t.Errorf("sort_by error = %v", err)
		}
		if _, err := d.screenUniverse(t.Context(), screenUniverseInput{SortBy: "return_1y", Limit: -1}); err == nil || !strings.Contains(err.Error(), "between 1 and") {
			t.Errorf("limit error = %v", err)
		}
	})
}
