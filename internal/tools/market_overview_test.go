package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/cache"
)

func TestMarketOverview(t *testing.T) {
	t.Run("every row with trends", func(t *testing.T) {
		sess, src, fund := newDataSession(t)
		var out marketOverviewOutput
		callOK(t, sess, "market_overview", map[string]any{}, &out)
		if len(out.Rows) != len(overviewInstruments) || len(out.Missing) != 0 || len(out.Warnings) != 0 {
			t.Fatalf("rows/missing/warnings = %d/%v/%v", len(out.Rows), out.Missing, out.Warnings)
		}
		if fund.quoteCalls != 1 {
			t.Errorf("quote calls = %d, want one batch", fund.quoteCalls)
		}
		for i, row := range out.Rows {
			if row.Symbol != overviewInstruments[i].symbol {
				t.Errorf("row %d = %s, want %s", i, row.Symbol, overviewInstruments[i].symbol)
			}
		}
		vix := out.Rows[len(out.Rows)-1]
		if vix.Symbol != "^VIX" || vix.Label != "VIX" || vix.AssetClass != "volatility" || vix.Trend != "" || vix.PctVsSMA200 != nil || vix.Price != 13.2 {
			t.Errorf("VIX row = %+v, want label VIX without a trend", vix)
		}
		spy := out.Rows[0]
		trend, err := analytics.AnalyzeTrend(src.series["SPY"], time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if spy.Label != "S&P 500" || spy.Trend != trend.State || spy.TrendAsOf != "2023-12-29" || spy.PctVsSMA200 == nil || *spy.PctVsSMA200 != round2(trend.PctVsSMA200) {
			t.Errorf("SPY row = %+v, want trend %s %v", spy, trend.State, round2(trend.PctVsSMA200))
		}
		if spy.ChangePct != -0.5 || spy.Price != 100 || spy.QuoteTime != "2024-01-02T21:00:00Z" {
			t.Errorf("SPY quote fields = %+v", spy)
		}
		for _, row := range out.Rows[:len(out.Rows)-1] {
			switch row.Trend {
			case analytics.StateUptrend, analytics.StateDowntrend, analytics.StateSideways:
			default:
				t.Errorf("%s trend = %q", row.Symbol, row.Trend)
			}
		}
		if tlt := out.Rows[6]; tlt.Symbol != "TLT" || tlt.AssetClass != "bond" || tlt.Trend != analytics.StateDowntrend {
			t.Errorf("TLT row = %+v, want a bond in a downtrend", tlt)
		}
		if out.LatestQuoteTime != "2024-01-02T21:15:00Z" || out.Disclaimer != Disclaimer || len(out.Notes) == 0 {
			t.Errorf("latest_quote_time/disclaimer/notes = %s/%q/%v", out.LatestQuoteTime, out.Disclaimer, out.Notes)
		}
	})

	t.Run("missing quote and missing history", func(t *testing.T) {
		fund := newDataFakeFund()
		delete(fund.quotes, "GLD")
		sess := newSession(t, dataDeps(newFakeSource(), fund)) // only SPY has history among the overview ETFs
		var out marketOverviewOutput
		callOK(t, sess, "market_overview", map[string]any{}, &out)
		if len(out.Rows) != len(overviewInstruments)-1 || len(out.Missing) != 1 || out.Missing[0] != "GLD" {
			t.Fatalf("rows/missing = %d/%v, want GLD missing", len(out.Rows), out.Missing)
		}
		if out.Rows[0].Trend == "" || out.Rows[1].Symbol != "QQQ" || out.Rows[1].Trend != "" {
			t.Errorf("SPY/QQQ trends = %q/%q, want only SPY", out.Rows[0].Trend, out.Rows[1].Trend)
		}
		joined := strings.Join(out.Warnings, "\n")
		for _, want := range []string{"QQQ trend unavailable: symbol QQQ is in the universe but the data source returned not found", "no quote for GLD"} {
			if !strings.Contains(joined, want) {
				t.Errorf("warnings lack %q:\n%s", want, joined)
			}
		}
	})

	t.Run("histories load once through the cache", func(t *testing.T) {
		src, fund := newDataFakeSource(), newDataFakeFund()
		deps := dataDeps(src, fund)
		store := cache.New(src, t.TempDir(), time.Hour)
		deps.Source, deps.Cache = store, store
		sess := newSession(t, deps)
		for range 2 {
			var out marketOverviewOutput
			callOK(t, sess, "market_overview", map[string]any{}, &out)
			if len(out.Rows) != len(overviewInstruments) || len(out.Warnings) != 0 {
				t.Fatalf("rows/warnings = %d/%v", len(out.Rows), out.Warnings)
			}
		}
		for _, inst := range overviewInstruments {
			want := 1
			if !inst.trend {
				want = 0
			}
			if got := src.callCount(inst.symbol); got != want {
				t.Errorf("%s fetched %d times, want %d", inst.symbol, got, want)
			}
		}
	})

	t.Run("quote failure is a tool error", func(t *testing.T) {
		fund := newDataFakeFund()
		fund.quotes = nil
		sess := newSession(t, dataDeps(newDataFakeSource(), fund))
		callErr(t, sess, "market_overview", map[string]any{}, "no quote for any overview symbol")
	})
}

func TestMarketOverviewStaleQuotes(t *testing.T) {
	clock := &testClock{t: now}
	sess, fund := newQuoteCacheSession(t, clock)
	callRaw(t, sess, "market_overview", map[string]any{})

	fund.failQuotes(errDataFakeNetwork)
	clock.advance(6 * time.Hour)
	out := callRaw(t, sess, "market_overview", map[string]any{})
	warnings := rawStrings(out["warnings"])
	if !hasDataNote(warnings, "6 hours old") || !hasDataNote(warnings, "SPY") || !hasDataNote(warnings, "connection reset by peer") {
		t.Errorf("warnings = %v, want the stale quotes named with their age and the cause", warnings)
	}
	rows, _ := out["rows"].([]any)
	if len(rows) != len(overviewInstruments) || rows[0].(map[string]any)["stale"] != true {
		t.Errorf("rows = %v, want every row served from memory and marked stale", rows)
	}
}
