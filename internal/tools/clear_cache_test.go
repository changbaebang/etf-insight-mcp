package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClearCache(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		callErr(t, newSession(t, testDeps(newFakeSource())), "clear_cache", map[string]any{"all": true, "confirm": true}, "cache not configured")
	})

	t.Run("preview without confirm deletes nothing", func(t *testing.T) {
		deps, store, dir := opsCache(t, newFakeSource())
		opsWarm(t, store, "SPY", "VOO")
		opsWriteFundDoc(t, dir, "SPY", "holdings")
		callErr(t, newSession(t, deps), "clear_cache", map[string]any{"symbols": []string{"spy", "XYZ"}}, "would delete 2 files")
		res := call(t, newSession(t, deps), "clear_cache", map[string]any{"symbols": []string{"spy", "XYZ"}, "confirm": false})
		msg := textOf(res)
		for _, want := range []string{"confirm=true", "of 1 symbol: SPY", "not cached, nothing to delete: XYZ"} {
			if !res.IsError || !strings.Contains(msg, want) {
				t.Errorf("preview = %q (error %v), want it to contain %q", msg, res.IsError, want)
			}
		}
		if !opsExists(filepath.Join(dir, "SPY.json")) || !opsExists(filepath.Join(dir, "fund", "SPY.holdings.json")) {
			t.Error("a preview must not delete files")
		}
		callErr(t, newSession(t, deps), "clear_cache", map[string]any{"all": true}, "would delete 3 files")
	})

	t.Run("symbols with confirm", func(t *testing.T) {
		deps, store, dir := opsCache(t, newFakeSource())
		opsWarm(t, store, "SPY", "VOO")
		fund := opsWriteFundDoc(t, dir, "SPY", "profile")
		want := opsFileSize(t, filepath.Join(dir, "SPY.json")) + fund

		var out clearCacheOutput
		callOK(t, newSession(t, deps), "clear_cache", map[string]any{"symbols": []string{" spy ", "XYZ"}, "confirm": true}, &out)
		if strings.Join(out.Removed, ",") != "SPY" || out.RemovedCount != 1 || out.FilesRemoved != 2 || out.FreedBytes != want || out.FreedMB != cacheMiB(want) {
			t.Errorf("out = %+v, want SPY removed with 2 files and %d bytes", out, want)
		}
		if strings.Join(out.NotCached, ",") != "XYZ" {
			t.Errorf("not_cached = %v, want XYZ", out.NotCached)
		}
		if opsExists(filepath.Join(dir, "SPY.json")) || opsExists(filepath.Join(dir, "fund", "SPY.profile.json")) {
			t.Error("SPY files still on disk")
		}
		if !opsExists(filepath.Join(dir, "VOO.json")) {
			t.Error("VOO was not selected but is gone")
		}
	})

	t.Run("all with confirm", func(t *testing.T) {
		deps, store, dir := opsCache(t, newFakeSource())
		opsWarm(t, store, "SPY", "VOO", "SCHD")
		opsWriteFundDoc(t, dir, "QQQ", "performance")
		stray := filepath.Join(dir, "notes.txt")
		if err := os.WriteFile(stray, []byte("mine"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := store.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		sess := newSession(t, deps)
		var out clearCacheOutput
		callOK(t, sess, "clear_cache", map[string]any{"all": true, "confirm": true}, &out)
		if strings.Join(out.Removed, ",") != "SCHD,SPY,VOO" || out.RemovedCount != 3 || out.FilesRemoved != 4 || out.FreedBytes != st.TotalBytes {
			t.Errorf("out = %+v, want 3 symbols, 4 files, %d bytes", out, st.TotalBytes)
		}
		if !opsExists(stray) {
			t.Error("a file the cache did not write was deleted")
		}
		var after cacheStatusOutput
		callOK(t, sess, "cache_status", nil, &after)
		if after.Files != 0 {
			t.Errorf("status after clearing = %+v, want empty", after)
		}

		// Clearing again is a no-op, not an error, even without confirm.
		var again clearCacheOutput
		callOK(t, sess, "clear_cache", map[string]any{"all": true}, &again)
		if again.FilesRemoved != 0 || len(again.Removed) != 0 || !strings.Contains(again.Note, "the cache is empty") {
			t.Errorf("second clear = %+v, want nothing to delete", again)
		}
	})

	t.Run("nothing cached for the symbols", func(t *testing.T) {
		deps, store, _ := opsCache(t, newFakeSource())
		opsWarm(t, store, "SPY")
		var out clearCacheOutput
		callOK(t, newSession(t, deps), "clear_cache", map[string]any{"symbols": []string{"QQQ"}}, &out)
		if out.FilesRemoved != 0 || strings.Join(out.NotCached, ",") != "QQQ" || !strings.Contains(out.Note, "no cached files for QQQ") {
			t.Errorf("out = %+v, want nothing to delete for QQQ", out)
		}
	})

	deps, _, _ := opsCache(t, newFakeSource())
	sess := newSession(t, deps)
	many := make([]string, 201)
	for i := range many {
		many[i] = fmt.Sprintf("S%03d", i)
	}
	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "nothing selected", args: map[string]any{"confirm": true}, want: "nothing selected"},
		{name: "null arguments", args: nil, want: "nothing selected"},
		{name: "blank symbols", args: map[string]any{"symbols": []string{" "}, "confirm": true}, want: "nothing selected"},
		{name: "symbols and all", args: map[string]any{"symbols": []string{"SPY"}, "all": true, "confirm": true}, want: "not both"},
		{name: "invalid symbol", args: map[string]any{"symbols": []string{"../SPY"}, "confirm": true}, want: `invalid symbol "../SPY"`},
		{name: "too many symbols", args: map[string]any{"symbols": many, "confirm": true}, want: "201 symbols given, at most 200"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "clear_cache", tt.args, tt.want)
		})
	}
}
