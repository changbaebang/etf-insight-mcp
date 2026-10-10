package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeSource is the smallest market.Source: it knows one symbol with a
// few bars and reports every other symbol as not found.
type fakeSource struct{}

func (fakeSource) Series(_ context.Context, symbol string) (*market.Series, error) {
	if symbol != "SPY" {
		return nil, fmt.Errorf("fake: %s: %w", symbol, market.ErrNotFound)
	}
	s := &market.Series{Meta: market.Meta{Symbol: "SPY", Currency: "USD"}}
	day := time.Date(2024, time.January, 2, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		p := 470 + float64(i)
		s.Bars = append(s.Bars, market.Bar{Date: day.AddDate(0, 0, i), Close: p, AdjClose: p})
	}
	return s, nil
}

// TestServerRoundTrip drives the real server through an in-memory
// transport, the same code path a Claude client takes over stdio.
func TestServerRoundTrip(t *testing.T) {
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()

	srv := newServer(tools.Deps{Source: fakeSource{}, Version: "test"})
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ping",
		Arguments: map[string]any{"message": "hi"},
	})
	if err != nil {
		t.Fatalf("call ping: %v", err)
	}
	if res.IsError {
		t.Fatalf("ping returned tool error: %+v", res.Content)
	}
	got, ok := res.StructuredContent.(map[string]any)
	if !ok || got["reply"] != "hi" || got["version"] != "test" {
		t.Errorf("structuredContent = %#v, want reply=hi version=test", res.StructuredContent)
	}

	list, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"ping", "list_etfs", "get_etf_info", "get_price_history", "simulate_dca", "simulate_portfolio_dca", "forecast_dca", "cache_status", "clear_cache", "refresh_prices"} {
		if !slices.Contains(names, want) {
			t.Errorf("tool %s is not listed; got %v", want, names)
		}
	}

	info, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_etf_info",
		Arguments: map[string]any{"symbol": "spy"},
	})
	if err != nil {
		t.Fatalf("call get_etf_info: %v", err)
	}
	if info.IsError {
		t.Fatalf("get_etf_info returned tool error: %+v", info.Content)
	}

	// Without a cache (Deps.Cache nil) the ops tools answer with a tool
	// error, not a crash.
	status, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "cache_status"})
	if err != nil {
		t.Fatalf("call cache_status: %v", err)
	}
	if !status.IsError {
		t.Errorf("cache_status without a cache = %+v, want a tool error", status.StructuredContent)
	}
}

func TestCheckCacheDir(t *testing.T) {
	dir := t.TempDir()
	if err := checkCacheDir(dir + "/nested/cache"); err != nil {
		t.Fatalf("checkCacheDir should create a nested directory: %v", err)
	}
	entries, err := os.ReadDir(dir + "/nested/cache")
	if err != nil || len(entries) != 0 {
		t.Errorf("probe file left behind: entries=%v err=%v", entries, err)
	}
	blocked := dir + "/file"
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkCacheDir(blocked); err == nil {
		t.Error("a regular file must not pass as a cache directory")
	}
}

func TestParseFlags(t *testing.T) {
	c, err := parseFlags(nil, io.Discard)
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if c.cacheDir != "" || c.cacheTTL != defaultCacheTTL || c.fullRefreshDays != 30 || c.fullRefreshInterval() != 30*24*time.Hour || c.clearCache || c.showVersion {
		t.Errorf("defaults = %+v", c)
	}

	c, err = parseFlags([]string{"-cache-dir", "/tmp/x", "-cache-ttl", "90m", "-full-refresh-days", "7", "-clear-cache"}, io.Discard)
	if err != nil {
		t.Fatalf("explicit flags: %v", err)
	}
	if c.cacheDir != "/tmp/x" || c.cacheTTL != 90*time.Minute || c.fullRefreshDays != 7 || !c.clearCache {
		t.Errorf("explicit flags = %+v", c)
	}

	bad := []struct {
		args []string
		want string
	}{
		{args: []string{"-full-refresh-days", "0"}, want: "-full-refresh-days must be at least 1"},
		// Larger values would overflow time.Duration in fullRefreshInterval.
		{args: []string{"-full-refresh-days", "106752"}, want: "-full-refresh-days must be at most 36500"},
		{args: []string{"-full-refresh-days", "36501"}, want: "-full-refresh-days must be at most 36500"},
		{args: []string{"-cache-ttl", "0s"}, want: "-cache-ttl must be positive"},
		{args: []string{"serve"}, want: `unexpected argument "serve"`},
	}
	for _, tt := range bad {
		if _, err := parseFlags(tt.args, io.Discard); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("parseFlags(%v) error = %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestRealMain(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		code       int
		stdout     string
		stderrHas  string
		stderrNone bool
	}{
		{name: "version", args: []string{"-version"}, code: 0, stdout: version + "\n", stderrNone: true},
		{name: "help", args: []string{"-h"}, code: 0, stderrHas: "-full-refresh-days"},
		{name: "unknown flag", args: []string{"-nope"}, code: 2, stderrHas: "flag provided but not defined: -nope"},
		{name: "bad value", args: []string{"-cache-ttl", "soon"}, code: 2, stderrHas: "invalid value"},
		{name: "nonsense value", args: []string{"-full-refresh-days", "-3"}, code: 2, stderrHas: "etf-insight-mcp: -full-refresh-days must be at least 1"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := realMain(tt.args, &stdout, &stderr)
			if code != tt.code || stdout.String() != tt.stdout {
				t.Errorf("exit %d stdout %q, want %d %q", code, stdout.String(), tt.code, tt.stdout)
			}
			if tt.stderrNone && stderr.Len() > 0 {
				t.Errorf("stderr = %q, want nothing", stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.stderrHas) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.stderrHas)
			}
		})
	}
}

func TestRealMainClearCache(t *testing.T) {
	dir := t.TempDir()
	store := cache.New(fakeSource{}, dir, time.Hour)
	if _, err := store.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	fundDoc := filepath.Join(dir, "fund", "SPY.profile.json")
	if err := os.MkdirAll(filepath.Dir(fundDoc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fundDoc, []byte(`{"version":1,"fetched_at":"2024-01-05T00:00:00Z","data":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "README.txt")
	if err := os.WriteFile(stray, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code := realMain([]string{"-clear-cache", "-cache-dir", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if stdout.Len() > 0 {
		t.Errorf("stdout = %q, want nothing (stdout is the MCP transport)", stdout.String())
	}
	for _, want := range []string{"removed 2 files", "from " + dir, "price history: SPY", "fund documents: 1 file"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
		}
	}
	for _, gone := range []string{filepath.Join(dir, "SPY.json"), fundDoc} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err %v)", gone, err)
		}
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("a file the server did not write was removed: %v", err)
	}
}

// TestRealMainClearCachePartial makes the fund directory read-only, so the
// price file goes but the fund document stays. The report must count only
// what was removed and name what is still there.
func TestRealMainClearCachePartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	store := cache.New(fakeSource{}, dir, time.Hour)
	if _, err := store.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	priceInfo, err := os.Stat(filepath.Join(dir, "SPY.json"))
	if err != nil {
		t.Fatal(err)
	}
	fundDir := filepath.Join(dir, "fund")
	if err := os.MkdirAll(fundDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fundDir, "SPY.profile.json"), []byte(`{"version":1,"fetched_at":"2024-01-05T00:00:00Z","data":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fundDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fundDir, 0o755) })

	var stdout, stderr strings.Builder
	if code := realMain([]string{"-clear-cache", "-cache-dir", dir}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1; stderr %q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	want := []string{
		fmt.Sprintf("etf-insight-mcp: removed 1 file (%d bytes) from %s", priceInfo.Size(), dir),
		"  price history: SPY",
		"  still there: fund documents: 1 file",
	}
	if len(lines) != len(want)+1 || !slices.Equal(lines[:len(want)], want) {
		t.Fatalf("stderr lines = %q, want %q then the error", lines, want)
	}
	if !strings.Contains(lines[len(want)], "failed partway") {
		t.Errorf("last line = %q, want the partial-failure error", lines[len(want)])
	}
}
