package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheStatus(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		callErr(t, newSession(t, testDeps(newFakeSource())), "cache_status", map[string]any{}, "cache not configured")
	})

	t.Run("empty cache", func(t *testing.T) {
		deps, _, dir := opsCache(t, newFakeSource())
		var out cacheStatusOutput
		callOK(t, newSession(t, deps), "cache_status", nil, &out)
		if out.Dir != dir || out.Files != 0 || out.TotalBytes != 0 || out.SymbolCount != 0 || out.OldestFetch != "" {
			t.Errorf("status = %+v, want an empty cache in %s", out, dir)
		}
		if out.Symbols == nil || out.Warnings == nil {
			t.Errorf("symbols/warnings = %v/%v, want empty arrays, not null", out.Symbols, out.Warnings)
		}
	})

	t.Run("files are mapped", func(t *testing.T) {
		deps, store, dir := opsCache(t, newFakeSource())
		opsWarm(t, store, "SPY", "SCHD")
		fund := opsWriteFundDoc(t, dir, "SPY", "profile")
		if err := os.WriteFile(filepath.Join(dir, "BAD.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		want := opsFileSize(t, filepath.Join(dir, "SPY.json")) + opsFileSize(t, filepath.Join(dir, "SCHD.json")) + fund + int64(len("{not json"))

		var out cacheStatusOutput
		callOK(t, newSession(t, deps), "cache_status", map[string]any{}, &out)
		if out.Files != 4 || out.FundFiles != 1 || out.TotalBytes != want || out.TotalMB != cacheMiB(want) {
			t.Errorf("files/fund/bytes/mb = %d/%d/%d/%v, want 4/1/%d/%v", out.Files, out.FundFiles, out.TotalBytes, out.TotalMB, want, cacheMiB(want))
		}
		if out.OldestFetch != "2024-01-03T09:00:00Z" || out.NewestFetch != out.OldestFetch {
			t.Errorf("oldest/newest = %s/%s, want the fixed clock", out.OldestFetch, out.NewestFetch)
		}
		if out.SymbolCount != 3 || out.Truncated || len(out.Symbols) != 3 {
			t.Fatalf("symbol_count/truncated/rows = %d/%v/%d, want 3/false/3", out.SymbolCount, out.Truncated, len(out.Symbols))
		}
		var got []string
		for _, r := range out.Symbols {
			got = append(got, r.Symbol)
		}
		if strings.Join(got, ",") != "BAD,SCHD,SPY" {
			t.Errorf("rows = %v, want sorted BAD,SCHD,SPY", got)
		}
		spy := out.Symbols[2]
		if spy.Bars != 780 || spy.FirstDate != "2021-01-04" || spy.LastDate != "2023-12-29" ||
			spy.FetchedAt != "2024-01-03T09:00:00Z" || spy.FullFetchedAt != spy.FetchedAt ||
			spy.Bytes != opsFileSize(t, filepath.Join(dir, "SPY.json")) || spy.LastError != "" {
			t.Errorf("SPY row = %+v", spy)
		}
		if bad := out.Symbols[0]; bad.Bars != 0 || bad.FetchedAt != "" {
			t.Errorf("unreadable row = %+v, want zero bars and no fetch time", bad)
		}
		if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "BAD.json: unreadable") {
			t.Errorf("warnings = %v, want the unreadable file", out.Warnings)
		}
	})

	t.Run("failed fetch is surfaced", func(t *testing.T) {
		src := newOpsFlakySource()
		deps, store, _ := opsCache(t, src)
		opsWarm(t, store, "SPY")
		src.down.Store(true)
		if _, err := store.Refresh(context.Background(), "SPY"); err != nil {
			t.Fatalf("refresh with a cached file should serve it: %v", err)
		}
		var out cacheStatusOutput
		callOK(t, newSession(t, deps), "cache_status", nil, &out)
		if len(out.Symbols) != 1 || out.Symbols[0].LastError != "fake: upstream down" {
			t.Errorf("rows = %+v, want SPY with last_error 'fake: upstream down'", out.Symbols)
		}
		if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "SPY: last fetch failed") {
			t.Errorf("warnings = %v, want the failed fetch", out.Warnings)
		}
	})

	t.Run("rows are capped at 200", func(t *testing.T) {
		deps, _, dir := opsCache(t, newFakeSource())
		for i := range 201 {
			opsWritePriceHeader(t, dir, fmt.Sprintf("S%03d", i))
		}
		var out cacheStatusOutput
		callOK(t, newSession(t, deps), "cache_status", nil, &out)
		if out.SymbolCount != 201 || !out.Truncated || len(out.Symbols) != maxCacheRows || out.Symbols[199].Symbol != "S199" {
			t.Errorf("symbol_count/truncated/rows = %d/%v/%d, want 201/true/200 ending at S199", out.SymbolCount, out.Truncated, len(out.Symbols))
		}
		found := false
		for _, w := range out.Warnings {
			found = found || strings.Contains(w, "201 cache files (more than 150)")
		}
		if !found {
			t.Errorf("warnings = %v, want the file-count threshold", out.Warnings)
		}
	})
}

// TestOpsAnnotations checks the hints a client uses to decide whether to
// ask before calling.
func TestOpsAnnotations(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	list, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type hints struct{ readOnly, destructive, idempotent, openWorld bool }
	want := map[string]hints{
		"cache_status":   {readOnly: true, openWorld: false},
		"clear_cache":    {destructive: true, idempotent: true, openWorld: false},
		"refresh_prices": {destructive: false, idempotent: true, openWorld: true},
	}
	for _, tool := range list.Tools {
		w, ok := want[tool.Name]
		if !ok {
			continue
		}
		delete(want, tool.Name)
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil && !w.readOnly || a.OpenWorldHint == nil {
			t.Errorf("%s annotations = %+v, want every hint set", tool.Name, a)
			continue
		}
		got := hints{readOnly: a.ReadOnlyHint, idempotent: a.IdempotentHint, openWorld: *a.OpenWorldHint}
		if a.DestructiveHint != nil {
			got.destructive = *a.DestructiveHint
		}
		if got != w {
			t.Errorf("%s hints = %+v, want %+v", tool.Name, got, w)
		}
		if tool.Description == "" || a.Title == "" {
			t.Errorf("%s has no description or title", tool.Name)
		}
		if schema, ok := tool.InputSchema.(map[string]any); ok {
			checkDescribed(t, tool.Name, "", schema)
		} else {
			t.Errorf("%s input schema is %T, want an object", tool.Name, tool.InputSchema)
		}
	}
	for name := range want {
		t.Errorf("tool %s is not registered", name)
	}
}
