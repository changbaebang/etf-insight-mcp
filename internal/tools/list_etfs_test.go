package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/universe"
)

func TestListETFs(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))

	t.Run("default hides leveraged and lists categories", func(t *testing.T) {
		var out listETFsOutput
		callOK(t, sess, "list_etfs", map[string]any{}, &out)
		if want := len(universe.Filter(universe.Query{})); out.Count != want || len(out.ETFs) != want {
			t.Errorf("count = %d (%d rows), want %d", out.Count, len(out.ETFs), want)
		}
		if !slices.Equal(out.Categories, universe.Categories()) {
			t.Errorf("categories = %v, want %v", out.Categories, universe.Categories())
		}
		for _, e := range out.ETFs {
			if e.Leveraged {
				t.Errorf("%s is leveraged but include_leveraged was false", e.Symbol)
			}
		}
	})

	t.Run("category filter is case-insensitive", func(t *testing.T) {
		var out listETFsOutput
		callOK(t, sess, "list_etfs", map[string]any{"category": "dividend"}, &out)
		if out.Count == 0 {
			t.Fatal("no dividend ETFs")
		}
		for _, e := range out.ETFs {
			if e.Category != "Dividend" {
				t.Errorf("%s has category %q, want Dividend", e.Symbol, e.Category)
			}
		}
	})

	t.Run("issuer filter", func(t *testing.T) {
		var out listETFsOutput
		callOK(t, sess, "list_etfs", map[string]any{"issuer": "vanguard"}, &out)
		if out.Count == 0 {
			t.Fatal("no Vanguard ETFs")
		}
		for _, e := range out.ETFs {
			if e.Issuer != "Vanguard" {
				t.Errorf("%s has issuer %q, want Vanguard", e.Symbol, e.Issuer)
			}
		}
	})

	t.Run("query matches symbol, name, issuer or note", func(t *testing.T) {
		var out listETFsOutput
		callOK(t, sess, "list_etfs", map[string]any{"query": "treasury"}, &out)
		if out.Count == 0 {
			t.Fatal("no treasury ETFs")
		}
		for _, e := range out.ETFs {
			if !strings.Contains(strings.ToLower(strings.Join([]string{e.Symbol, e.Name, e.Issuer, e.Note}, " ")), "treasury") {
				t.Errorf("%s %q (note %q) does not match treasury", e.Symbol, e.Name, e.Note)
			}
		}
	})

	t.Run("leveraged category implies include_leveraged", func(t *testing.T) {
		var out listETFsOutput
		callOK(t, sess, "list_etfs", map[string]any{"category": "Leveraged / Inverse"}, &out)
		if out.Count == 0 {
			t.Fatal("no leveraged ETFs")
		}
		for _, e := range out.ETFs {
			if !e.Leveraged {
				t.Errorf("%s is not leveraged", e.Symbol)
			}
		}
	})

	t.Run("include_leveraged adds them to an unfiltered list", func(t *testing.T) {
		var out listETFsOutput
		callOK(t, sess, "list_etfs", map[string]any{"include_leveraged": true}, &out)
		if want := len(universe.All()); out.Count != want {
			t.Errorf("count = %d, want %d", out.Count, want)
		}
	})

	t.Run("unknown category lists the valid ones", func(t *testing.T) {
		callErr(t, sess, "list_etfs", map[string]any{"category": "Crypto"}, "valid categories: US Broad Market")
	})

	t.Run("unknown issuer lists the valid ones", func(t *testing.T) {
		callErr(t, sess, "list_etfs", map[string]any{"issuer": "BlackRock"}, "valid issuers: Vanguard")
	})
}
