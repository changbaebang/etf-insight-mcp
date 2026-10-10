package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRefreshPrices(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		callErr(t, newSession(t, testDeps(newFakeSource())), "refresh_prices", map[string]any{"symbols": []string{"SPY"}}, "cache not configured")
	})

	t.Run("per-symbol results", func(t *testing.T) {
		src := newFakeSource()
		deps, store, _ := opsCache(t, src)
		opsWarm(t, store, "SPY") // fresh on disk; refresh must fetch anyway
		var out refreshPricesOutput
		callOK(t, newSession(t, deps), "refresh_prices", map[string]any{"symbols": []string{"spy", "XYZ", "SPY", "VTI"}}, &out)
		if out.Requested != 3 || out.Refreshed != 1 || out.Failed != 2 || len(out.Results) != 3 {
			t.Fatalf("requested/refreshed/failed/rows = %d/%d/%d/%d, want 3/1/2/3", out.Requested, out.Refreshed, out.Failed, len(out.Results))
		}
		spy, xyz, vti := out.Results[0], out.Results[1], out.Results[2]
		if spy.Symbol != "SPY" || !spy.OK || spy.Bars != 780 || spy.LastDate != "2023-12-29" || spy.Error != "" {
			t.Errorf("SPY = %+v", spy)
		}
		if xyz.Symbol != "XYZ" || xyz.OK || xyz.Bars != 0 || !strings.Contains(xyz.Error, "unknown symbol XYZ") {
			t.Errorf("XYZ = %+v, want an unknown-symbol error", xyz)
		}
		if vti.OK || !strings.Contains(vti.Error, "in the universe but the data source returned not found") {
			t.Errorf("VTI = %+v, want the universe-but-missing error", vti)
		}
		if n := src.callCount("SPY"); n != 2 {
			t.Errorf("SPY fetched %d times, want 2 (warm + forced refresh)", n)
		}
		if len(out.Warnings) != 0 {
			t.Errorf("warnings = %v, want none", out.Warnings)
		}
	})

	t.Run("default is every cached symbol", func(t *testing.T) {
		src := newFakeSource()
		deps, store, _ := opsCache(t, src)
		opsWarm(t, store, "VOO", "SPY")
		var out refreshPricesOutput
		callOK(t, newSession(t, deps), "refresh_prices", map[string]any{}, &out)
		if out.Requested != 2 || out.Refreshed != 2 || out.Results[0].Symbol != "SPY" || out.Results[1].Symbol != "VOO" {
			t.Errorf("out = %+v, want SPY and VOO refreshed", out)
		}
	})

	t.Run("empty cache and no symbols", func(t *testing.T) {
		deps, _, _ := opsCache(t, newFakeSource())
		callErr(t, newSession(t, deps), "refresh_prices", nil, "nothing to refresh: the cache is empty")
	})

	t.Run("failed download keeps the cached file", func(t *testing.T) {
		src := newOpsFlakySource()
		deps, store, _ := opsCache(t, src)
		opsWarm(t, store, "SPY")
		src.down.Store(true)
		var out refreshPricesOutput
		callOK(t, newSession(t, deps), "refresh_prices", map[string]any{"symbols": []string{"SPY"}}, &out)
		r := out.Results[0]
		if r.OK || r.Bars != 780 || !strings.Contains(r.Error, "download failed (fake: upstream down); the previous cached file is kept") {
			t.Errorf("SPY = %+v, want a failure that kept 780 cached bars", r)
		}
		if out.Failed != 1 || out.Refreshed != 0 {
			t.Errorf("refreshed/failed = %d/%d, want 0/1", out.Refreshed, out.Failed)
		}
	})

	t.Run("universe", func(t *testing.T) {
		src := newFakeSource()
		deps, _, _ := opsCache(t, src)
		var out refreshPricesOutput
		callOK(t, newSession(t, deps), "refresh_prices", map[string]any{"symbols": []string{"KRW=X", "SPY"}, "universe": true}, &out)
		syms := universe.Symbols()
		if out.Requested != len(syms)+1 || len(out.Results) != out.Requested {
			t.Fatalf("requested = %d rows %d, want %d (universe + KRW=X)", out.Requested, len(out.Results), len(syms)+1)
		}
		if out.Results[0].Symbol != "KRW=X" || out.Results[1].Symbol != "SPY" {
			t.Errorf("first rows = %s, %s; want the explicit symbols first", out.Results[0].Symbol, out.Results[1].Symbol)
		}
		if out.Refreshed != 4 { // KRW=X, SPY, VOO, SCHD are the symbols the fake knows
			t.Errorf("refreshed = %d, want 4", out.Refreshed)
		}
		if n := src.callCount("SPY"); n != 1 {
			t.Errorf("SPY fetched %d times, want 1 (listed twice, refreshed once)", n)
		}
	})

	t.Run("too many symbols", func(t *testing.T) {
		deps, _, _ := opsCache(t, newFakeSource())
		many := make([]string, 0, 80)
		for i := range 80 {
			many = append(many, "X"+string(rune('A'+i/26))+string(rune('A'+i%26)))
		}
		callErr(t, newSession(t, deps), "refresh_prices", map[string]any{"symbols": many, "universe": true}, "at most 200 per call")
	})

	t.Run("write failure is not ok", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		deps, _, dir := opsCache(t, newFakeSource())
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		var out refreshPricesOutput
		callOK(t, newSession(t, deps), "refresh_prices", map[string]any{"symbols": []string{"SPY"}}, &out)
		r := out.Results[0]
		if r.OK || r.Bars != 780 || !strings.Contains(r.Error, "downloaded but the cache file could not be written") {
			t.Errorf("SPY = %+v, want a failed row that says the download was not cached", r)
		}
		if out.Refreshed != 0 || out.Failed != 1 || len(out.Warnings) != 0 {
			t.Errorf("refreshed/failed/warnings = %d/%d/%v, want 0/1/none", out.Refreshed, out.Failed, out.Warnings)
		}
	})

	t.Run("progress notifications", func(t *testing.T) {
		deps, _, _ := opsCache(t, newFakeSource())
		sess, notes := opsProgressSession(t, deps)
		params := &mcp.CallToolParams{Name: "refresh_prices", Arguments: map[string]any{"symbols": []string{"SPY", "VOO", "XYZ"}}}
		params.SetProgressToken("refresh-1")
		res, err := sess.CallTool(context.Background(), params)
		if err != nil || res.IsError {
			t.Fatalf("call: %v %s", err, textOf(res))
		}
		seen := 0
		timeout := time.After(5 * time.Second)
		for seen < 3 {
			select {
			case n := <-notes:
				seen++
				if n.ProgressToken != "refresh-1" || n.Total != 3 || n.Progress < 1 || n.Progress > 3 {
					t.Errorf("notification = %+v", n)
				}
			case <-timeout:
				t.Fatalf("got %d progress notifications, want 3", seen)
			}
		}
	})
}

// TestRefreshPricesCancelStopsNewDownloads cancels a large refresh once the
// first prefetchConcurrency downloads are running. No further download may
// start: the cache runs each one detached from the caller, so a download
// started after the cancellation would run on with nobody waiting for it.
func TestRefreshPricesCancelStopsNewDownloads(t *testing.T) {
	src := newOpsBlockingSource(t)
	deps, _, _ := opsCache(t, src)
	syms := universe.Symbols()[:60]

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for src.started.Load() < prefetchConcurrency {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	// A real progress callback sends an MCP notification; the pause stands
	// in for it and gives finished workers time to free their slots while
	// the loop is still handing out symbols.
	progress := func(int, int, string) { time.Sleep(200 * time.Microsecond) }
	out, err := deps.refreshPrices(ctx, refreshPricesInput{Symbols: syms}, progress)
	if err != nil {
		t.Fatal(err)
	}
	if n := src.started.Load(); n != prefetchConcurrency {
		t.Errorf("downloads started = %d, want %d (none after the cancellation)", n, prefetchConcurrency)
	}
	if out.Refreshed != 0 || out.Failed != len(syms) {
		t.Errorf("refreshed/failed = %d/%d, want 0/%d", out.Refreshed, out.Failed, len(syms))
	}
	last := out.Results[len(out.Results)-1]
	if last.OK || !strings.Contains(last.Error, "not refreshed: context canceled") {
		t.Errorf("last row = %+v, want a not-refreshed row", last)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "cancelled") {
		t.Errorf("warnings = %v, want one cancellation warning", out.Warnings)
	}
}

func TestRefreshPricesBlankSymbols(t *testing.T) {
	src := newFakeSource()
	deps, store, _ := opsCache(t, src)
	opsWarm(t, store, "SPY", "VOO")
	callErr(t, newSession(t, deps), "refresh_prices", map[string]any{"symbols": []string{" ", ""}}, "no valid symbol")
	if n := src.callCount("SPY"); n != 1 {
		t.Errorf("SPY fetched %d times, want 1 (the warm-up only)", n)
	}
}
