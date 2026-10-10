package tools

import (
	"strings"
	"testing"
)

func TestSearchSymbols(t *testing.T) {
	sess, _, fund := newDataSession(t)

	t.Run("etfs only by default", func(t *testing.T) {
		var out searchSymbolsOutput
		callOK(t, sess, "search_symbols", map[string]any{"query": " schwab "}, &out)
		if out.Query != "schwab" || !out.ETFOnly {
			t.Errorf("query/etf_only = %q/%v", out.Query, out.ETFOnly)
		}
		var syms []string
		for _, h := range out.Hits {
			syms = append(syms, h.Symbol)
			if h.Type != "ETF" {
				t.Errorf("hit %+v is not an ETF", h)
			}
		}
		if got := strings.Join(syms, ","); got != "SCHD,SCHB,SCHX" || out.Count != 3 {
			t.Errorf("hits = %s (count %d), want SCHD,SCHB,SCHX", got, out.Count)
		}
		if !out.Hits[0].InUniverse || out.Hits[0].Exchange != "NYSEArca" || out.Hits[0].Name != "Schwab US Dividend Equity ETF" {
			t.Errorf("first hit = %+v, want SCHD in the universe", out.Hits[0])
		}
		if search, _ := fund.limits(); search != searchFetchLimit {
			t.Errorf("upstream limit = %d, want %d so that filtering still fills the page", search, searchFetchLimit)
		}
	})

	t.Run("all instrument types", func(t *testing.T) {
		var out searchSymbolsOutput
		callOK(t, sess, "search_symbols", map[string]any{"query": "schwab", "etf_only": false}, &out)
		if out.ETFOnly || out.Count != 5 {
			t.Fatalf("etf_only/count = %v/%d, want false/5", out.ETFOnly, out.Count)
		}
		if h := out.Hits[1]; h.Symbol != "SCHW" || h.Type != "EQUITY" || h.InUniverse {
			t.Errorf("second hit = %+v, want SCHW equity outside the universe", h)
		}
		// us_only still filters, so the provider is asked for its maximum.
		if search, _ := fund.limits(); search != searchFetchLimit {
			t.Errorf("upstream limit = %d, want %d", search, searchFetchLimit)
		}
		callOK(t, sess, "search_symbols", map[string]any{"query": "schwab", "etf_only": false, "us_only": false}, &out)
		if search, _ := fund.limits(); search != defaultSearchLimit || out.Count != 5 {
			t.Errorf("unfiltered: upstream limit = %d, count %d; want %d and 5", search, out.Count, defaultSearchLimit)
		}
	})

	t.Run("limit caps the hits", func(t *testing.T) {
		var def, capped searchSymbolsOutput
		callOK(t, sess, "search_symbols", map[string]any{"query": "generic"}, &def)
		callOK(t, sess, "search_symbols", map[string]any{"query": "generic", "limit": 25}, &capped)
		if def.Count != defaultSearchLimit || len(def.Hits) != defaultSearchLimit {
			t.Errorf("default count = %d, want %d", def.Count, defaultSearchLimit)
		}
		if capped.Count != maxSearchLimit {
			t.Errorf("limit 25 count = %d, want 25", capped.Count)
		}
		for _, h := range capped.Hits {
			if h.Type != "ETF" {
				t.Errorf("hit %s is %s", h.Symbol, h.Type)
			}
		}
	})

	t.Run("only non-etfs match", func(t *testing.T) {
		var out searchSymbolsOutput
		callOK(t, sess, "search_symbols", map[string]any{"query": "corporation"}, &out)
		if out.Count != 0 || len(out.Hits) != 0 || len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "1 match that is not an ETF was hidden; set etf_only false") {
			t.Errorf("out = %+v, want no hits and an etf_only hint", out)
		}
	})

	t.Run("nothing matches", func(t *testing.T) {
		var out searchSymbolsOutput
		callOK(t, sess, "search_symbols", map[string]any{"query": "zzzz"}, &out)
		if out.Hits == nil || out.Count != 0 || len(out.Notes) != 1 || !strings.Contains(out.Notes[0], "no symbol matches") {
			t.Errorf("out = %+v, want an empty list and a hint", out)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "empty query", args: map[string]any{"query": "  "}, want: "query is required"},
		{name: "missing query", args: map[string]any{}, want: "query"},
		{name: "limit too high", args: map[string]any{"query": "x", "limit": 26}, want: "limit must be between 1 and 25, got 26"},
		{name: "negative limit", args: map[string]any{"query": "x", "limit": -1}, want: "between 1 and 25"},
		{name: "upstream failure", args: map[string]any{"query": "BOOM"}, want: "connection reset by peer"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "search_symbols", tt.args, tt.want)
		})
	}
}

func TestSearchSymbolsListings(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("us listings by default", func(t *testing.T) {
		out := callRaw(t, sess, "search_symbols", map[string]any{"query": "s&p 500"})
		hits, _ := out["hits"].([]any)
		if len(hits) != 1 || hits[0].(map[string]any)["symbol"] != "VOO" {
			t.Errorf("hits = %v, want only the US listing VOO", hits)
		}
		notes := rawStrings(out["notes"])
		if !hasDataNote(notes, "VFV.TO (Toronto)") || !hasDataNote(notes, "VUAA.L (London)") {
			t.Errorf("notes = %v, want the hidden foreign listings named", notes)
		}
	})

	t.Run("foreign listings on request", func(t *testing.T) {
		out := callRaw(t, sess, "search_symbols", map[string]any{"query": "s&p 500", "us_only": false})
		hits, _ := out["hits"].([]any)
		want := map[string]bool{"VOO": true, "VFV.TO": false, "VUAA.L": false}
		if len(hits) != len(want) {
			t.Fatalf("hits = %v, want %d", hits, len(want))
		}
		for _, h := range hits {
			row := h.(map[string]any)
			if us, ok := want[row["symbol"].(string)]; !ok || row["us_listing"] != us {
				t.Errorf("hit %v, want us_listing %v", row, us)
			}
		}
	})

	t.Run("non-latin query", func(t *testing.T) {
		callErr(t, sess, "search_symbols", map[string]any{"query": "미국 배당"}, "Latin")
	})
}

func TestForeignListing(t *testing.T) {
	for sym, want := range map[string]bool{"VOO": false, "BRK-B": false, "^GSPC": false, "KRW=X": false, "VFV.TO": true, "VUAA.L": true, "SCHD.MX": true, "X.": false} {
		if got := foreignListing(sym); got != want {
			t.Errorf("foreignListing(%q) = %v, want %v", sym, got, want)
		}
	}
}
