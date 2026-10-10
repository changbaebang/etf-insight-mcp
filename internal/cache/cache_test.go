package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

var (
	errUpstream = errors.New("upstream down")
	t0          = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
)

// fakeSource is a market.Source that counts calls, can be told to fail
// and can block every call until a gate is opened.
type fakeSource struct {
	mu      sync.Mutex
	calls   int
	err     error                              // returned by every call when non-nil
	failFor map[string]error                   // per-symbol failures
	gate    chan struct{}                      // when non-nil, Series blocks until closed or ctx is done
	series  func(symbol string) *market.Series // what a successful call returns; nil means sampleSeries

	inflight    atomic.Int32
	maxInflight atomic.Int32
}

func (f *fakeSource) Series(ctx context.Context, symbol string) (*market.Series, error) {
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		cur := f.maxInflight.Load()
		if n <= cur || f.maxInflight.CompareAndSwap(cur, n) {
			break
		}
	}

	f.mu.Lock()
	f.calls++
	err := f.err
	if err == nil {
		err = f.failFor[symbol]
	}
	series := f.series
	f.mu.Unlock()

	if gateErr := f.waitGate(ctx); gateErr != nil {
		return nil, gateErr
	}
	if err != nil {
		return nil, err
	}
	if series != nil {
		return series(symbol), nil
	}
	return sampleSeries(symbol), nil
}

// waitGate blocks until the gate is opened or ctx is done; a nil gate
// never blocks.
func (f *fakeSource) waitGate(ctx context.Context) error {
	if f.gate == nil {
		return nil
	}
	select {
	case <-f.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSource) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func sampleSeries(symbol string) *market.Series {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	return &market.Series{
		Meta: market.Meta{Symbol: symbol, Currency: "USD", FetchedAt: t0},
		Bars: []market.Bar{
			{Date: day(2), Close: 10, AdjClose: 10},
			{Date: day(5), Close: 11, AdjClose: 11, Dividend: 0.5},
			{Date: day(6), Close: 12, AdjClose: 12},
		},
	}
}

// newStore returns a Store over a fresh fake with a fixed clock.
func newStore(t *testing.T, ttl time.Duration) (*Store, *fakeSource) {
	t.Helper()
	f := &fakeSource{}
	s := New(f, t.TempDir(), ttl, WithClock(func() time.Time { return t0 }))
	return s, f
}

func assertSeries(t *testing.T, got *market.Series, symbol string) {
	t.Helper()
	if got == nil {
		t.Fatal("got nil series")
	}
	want := sampleSeries(symbol)
	if got.Meta.Symbol != want.Meta.Symbol || len(got.Bars) != len(want.Bars) {
		t.Fatalf("series = %+v, want %+v", got, want)
	}
	for i := range want.Bars {
		g, w := got.Bars[i], want.Bars[i]
		if !g.Date.Equal(w.Date) || g.Close != w.Close || g.AdjClose != w.AdjClose || g.Dividend != w.Dividend {
			t.Errorf("bar %d = %+v, want %+v", i, g, w)
		}
	}
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func readFile(t *testing.T, s *Store, sym string) entry {
	t.Helper()
	data, err := os.ReadFile(s.path(sym))
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("decode cache file: %v", err)
	}
	return e
}

func TestStoreFreshHitSkipsSource(t *testing.T) {
	s, f := newStore(t, time.Hour)
	ctx := context.Background()

	first, err := s.Series(ctx, "spy")
	if err != nil {
		t.Fatalf("cold Series: %v", err)
	}
	assertSeries(t, first, "SPY")
	if f.count() != 1 {
		t.Fatalf("calls after cold read = %d, want 1", f.count())
	}

	second, err := s.Series(ctx, " SPY ")
	if err != nil {
		t.Fatalf("warm Series: %v", err)
	}
	assertSeries(t, second, "SPY")
	if f.count() != 1 {
		t.Errorf("calls after warm read = %d, want 1", f.count())
	}
	if e := readFile(t, s, "SPY"); !e.FetchedAt.Equal(t0) {
		t.Errorf("file FetchedAt = %v, want %v", e.FetchedAt, t0)
	}
}

func TestStoreExpiredTTLRefetches(t *testing.T) {
	s, f := newStore(t, time.Hour)
	ctx := context.Background()
	if _, err := s.Series(ctx, "SPY"); err != nil {
		t.Fatalf("cold Series: %v", err)
	}

	later := t0.Add(time.Hour + time.Second)
	s.now = func() time.Time { return later }
	got, err := s.Series(ctx, "SPY")
	if err != nil {
		t.Fatalf("expired Series: %v", err)
	}
	assertSeries(t, got, "SPY")
	if f.count() != 2 {
		t.Errorf("calls = %d, want 2", f.count())
	}
	if e := readFile(t, s, "SPY"); !e.FetchedAt.Equal(later) {
		t.Errorf("file FetchedAt = %v, want rewritten to %v", e.FetchedAt, later)
	}
}

func TestStoreStaleOnError(t *testing.T) {
	s, f := newStore(t, time.Hour)
	ctx := context.Background()
	if _, err := s.Series(ctx, "SPY"); err != nil {
		t.Fatalf("cold Series: %v", err)
	}
	if s.LastError("SPY") != nil {
		t.Fatalf("LastError after success = %v", s.LastError("SPY"))
	}

	f.setErr(errUpstream)
	s.now = func() time.Time { return t0.Add(2 * time.Hour) }
	got, err := s.Series(ctx, "SPY")
	if err != nil {
		t.Fatalf("stale Series returned error %v, want stale data", err)
	}
	assertSeries(t, got, "SPY")
	if last := s.LastError("spy"); !errors.Is(last, errUpstream) {
		t.Errorf("LastError = %v, want wrapped upstream error", last)
	} else if !strings.Contains(last.Error(), "SPY") {
		t.Errorf("LastError %q does not mention the symbol", last)
	}
	if f.count() != 2 {
		t.Errorf("calls = %d, want 2", f.count())
	}

	f.setErr(nil)
	if _, err := s.Refresh(ctx, "SPY"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if s.LastError("SPY") != nil {
		t.Errorf("LastError after recovery = %v, want nil", s.LastError("SPY"))
	}
}

func TestStoreNoFileOnError(t *testing.T) {
	s, f := newStore(t, time.Hour)
	f.setErr(errUpstream)

	got, err := s.Series(context.Background(), "SPY")
	if got != nil || !errors.Is(err, errUpstream) {
		t.Fatalf("Series = %v, %v; want nil and wrapped upstream error", got, err)
	}
	if !strings.Contains(err.Error(), "SPY") {
		t.Errorf("error %q does not mention the symbol", err)
	}
	if !errors.Is(s.LastError("SPY"), errUpstream) {
		t.Errorf("LastError = %v, want upstream error", s.LastError("SPY"))
	}
	if _, statErr := os.Stat(s.path("SPY")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a cache file was written despite the failure: %v", statErr)
	}
}

func TestStoreSingleFlight(t *testing.T) {
	s, f := newStore(t, time.Hour)
	f.gate = make(chan struct{})

	const n = 20
	var (
		started sync.WaitGroup
		done    sync.WaitGroup
		results = make([]*market.Series, n)
		errs    = make([]error, n)
	)
	started.Add(n)
	done.Add(n)
	for i := range n {
		go func() {
			defer done.Done()
			started.Done()
			results[i], errs[i] = s.Series(context.Background(), "COLD")
		}()
	}
	started.Wait()
	waitFor(t, "the leader to reach the source", func() bool { return f.count() == 1 })
	close(f.gate)
	done.Wait()

	if f.count() != 1 {
		t.Errorf("calls = %d, want exactly 1 for %d concurrent cold reads", f.count(), n)
	}
	for i := range n {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
			continue
		}
		assertSeries(t, results[i], "COLD")
	}
	if len(s.inflight) != 0 {
		t.Errorf("inflight map still has %d entries", len(s.inflight))
	}
}

func TestStoreWaiterHonorsContext(t *testing.T) {
	s, f := newStore(t, time.Hour)
	f.gate = make(chan struct{})

	leaderDone := make(chan error, 1)
	go func() {
		_, err := s.Series(context.Background(), "SLOW")
		leaderDone <- err
	}()
	waitFor(t, "the leader to reach the source", func() bool { return f.count() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Series(ctx, "SLOW")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waiter took %v, want a prompt return", elapsed)
	}

	close(f.gate)
	if err := <-leaderDone; err != nil {
		t.Errorf("leader: %v", err)
	}
	if f.count() != 1 {
		t.Errorf("calls = %d, want 1", f.count())
	}
}

func TestStoreRefreshBypassesTTL(t *testing.T) {
	s, f := newStore(t, time.Hour)
	ctx := context.Background()
	if _, err := s.Series(ctx, "SPY"); err != nil {
		t.Fatalf("Series: %v", err)
	}
	got, err := s.Refresh(ctx, "spy")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	assertSeries(t, got, "SPY")
	if f.count() != 2 {
		t.Errorf("calls = %d, want 2 (Refresh must ignore the fresh file)", f.count())
	}
}

func TestStoreRejectsBadSymbols(t *testing.T) {
	s, f := newStore(t, time.Hour)
	ctx := context.Background()
	bad := []string{"", "   ", "sp y", "../etc", "spy/", "SPY;rm", "ｓｐｙ", "sp\ny"}
	for _, sym := range bad {
		t.Run(sym, func(t *testing.T) {
			if _, err := s.Series(ctx, sym); !errors.Is(err, ErrInvalidSymbol) {
				t.Errorf("Series(%q) error = %v, want ErrInvalidSymbol", sym, err)
			}
			if _, err := s.Refresh(ctx, sym); !errors.Is(err, ErrInvalidSymbol) {
				t.Errorf("Refresh(%q) error = %v, want ErrInvalidSymbol", sym, err)
			}
		})
	}
	if f.count() != 0 {
		t.Errorf("rejected symbols reached the source %d times", f.count())
	}

	good := []struct{ in, want string }{
		{"brk-b", "BRK-B"},
		{"krw=x", "KRW=X"},
		{"^gspc", "^GSPC"},
		{"bf.b", "BF.B"},
		{" spy ", "SPY"}, // surrounding whitespace is trimmed
		{"qqq\n", "QQQ"}, // including a trailing newline
	}
	for _, tt := range good {
		got, err := s.Series(ctx, tt.in)
		if err != nil {
			t.Errorf("Series(%q): %v", tt.in, err)
			continue
		}
		if got.Meta.Symbol != tt.want {
			t.Errorf("Series(%q) fetched %q, want %q", tt.in, got.Meta.Symbol, tt.want)
		}
		if _, err := os.Stat(s.path(tt.want)); err != nil {
			t.Errorf("Series(%q) left no %s.json: %v", tt.in, tt.want, err)
		}
	}
}

func TestStoreAtomicWriteLeavesNoTempFiles(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	ctx := context.Background()
	for _, sym := range []string{"SPY", "QQQ", "SPY"} {
		if _, err := s.Refresh(ctx, sym); err != nil {
			t.Fatalf("Refresh(%s): %v", sym, err)
		}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "QQQ.json" || names[1] != "SPY.json" {
		t.Errorf("cache dir holds %v, want exactly QQQ.json and SPY.json", names)
	}
}

func TestStoreWriteFailureStillReturnsSeries(t *testing.T) {
	f := &fakeSource{}
	// A file where the directory should be makes every write fail.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(f, blocked, time.Hour)

	got, err := s.Series(context.Background(), "SPY")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	assertSeries(t, got, "SPY")
	if s.LastError("SPY") != nil {
		t.Errorf("LastError = %v, want nil: the fetch itself succeeded", s.LastError("SPY"))
	}
	if s.LastWriteError("SPY") == nil {
		t.Error("LastWriteError = nil, want the write failure")
	}
}

// TestStoreLeaderCancelDoesNotFailWaiters: the caller that started a fetch
// gives up, but another caller waiting on the same symbol still gets the
// series from the single upstream call.
func TestStoreLeaderCancelDoesNotFailWaiters(t *testing.T) {
	s, f := newStore(t, time.Hour)
	f.gate = make(chan struct{})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := s.Series(leaderCtx, "SLOW")
		leaderDone <- err
	}()
	waitFor(t, "the leader to reach the source", func() bool { return f.count() == 1 })

	waiterDone := make(chan error, 1)
	var waiterSeries *market.Series
	go func() {
		got, err := s.Series(context.Background(), "SLOW")
		waiterSeries = got
		waiterDone <- err
	}()
	// Give the waiter time to register, then cancel only the leader.
	time.Sleep(20 * time.Millisecond)
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}

	close(f.gate)
	if err := <-waiterDone; err != nil {
		t.Fatalf("waiter error = %v, want nil: the fetch must outlive the leader", err)
	}
	assertSeries(t, waiterSeries, "SLOW")
	if f.count() != 1 {
		t.Errorf("calls = %d, want 1", f.count())
	}
	if err := s.LastError("SLOW"); err != nil {
		t.Errorf("LastError = %v, want nil after a successful shared fetch", err)
	}
}

func TestStorePrefetchReturnsSeries(t *testing.T) {
	s, f := newStore(t, time.Hour)
	f.failFor = map[string]error{"BAD": errUpstream}

	series, errs := s.Prefetch(context.Background(), []string{"SPY", "QQQ", "BAD"}, 2)
	if len(series) != 2 || series["SPY"] == nil || series["QQQ"] == nil {
		t.Fatalf("series = %v, want SPY and QQQ", series)
	}
	if len(errs) != 1 || !errors.Is(errs["BAD"], errUpstream) {
		t.Fatalf("errs = %v, want BAD -> upstream error", errs)
	}
	if f.count() != 3 {
		t.Errorf("calls = %d, want 3", f.count())
	}
	// A second prefetch is served from disk.
	s.Prefetch(context.Background(), []string{"SPY", "QQQ"}, 2)
	if f.count() != 3 {
		t.Errorf("calls after warm prefetch = %d, want 3", f.count())
	}
}

func TestStoreFutureFetchTimeIsStale(t *testing.T) {
	s, f := newStore(t, time.Hour)
	// Write a file stamped far in the future, then move the clock back.
	s.now = func() time.Time { return t0.AddDate(100, 0, 0) }
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return t0 }
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Errorf("calls = %d, want 2: a future FetchedAt must not count as fresh", f.count())
	}
}

func TestNewSweepsOldTempFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "SPY.json.123.tmp")
	fresh := filepath.Join(dir, "QQQ.json.456.tmp")
	keep := filepath.Join(dir, "SPY.json")
	for _, p := range []string{old, fresh, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	New(&fakeSource{}, dir, time.Hour)

	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old temp file still exists (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp file was removed: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("cache file was removed: %v", err)
	}
}

func TestStoreCorruptFileIsMiss(t *testing.T) {
	s, f := newStore(t, time.Hour)
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
	}{
		{"garbage", "not json"},
		{"array", "[1,2,3]"},
		{"v1 missing series", `{"FetchedAt":"2026-10-08T09:00:00Z"}`},
		{"v1 invalid series", `{"FetchedAt":"2026-10-08T09:00:00Z","Series":{"Meta":{"Symbol":"SPY"},"Bars":[{"Date":"2026-01-02T00:00:00Z","Close":0,"AdjClose":1}]}}`},
		{"v2 missing series", `{"version":2,"fetched_at":"2026-10-08T09:00:00Z","full_fetched_at":"2026-10-08T09:00:00Z"}`},
		{"v2 invalid series", `{"version":2,"fetched_at":"2026-10-08T09:00:00Z","full_fetched_at":"2026-10-08T09:00:00Z","series":{"Meta":{"Symbol":"SPY"},"Bars":[{"Date":"2026-01-02T00:00:00Z","Close":1,"AdjClose":0}]}}`},
		{"unknown version", `{"version":7,"fetched_at":"2026-10-08T09:00:00Z","series":{"Meta":{"Symbol":"SPY"},"Bars":[]}}`},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(s.path("SPY"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := s.Series(context.Background(), "SPY")
			if err != nil {
				t.Fatalf("Series: %v", err)
			}
			assertSeries(t, got, "SPY")
			if f.count() != i+1 {
				t.Errorf("calls = %d, want %d (corrupt file must be refetched)", f.count(), i+1)
			}
			if e := readFile(t, s, "SPY"); e.Series == nil || e.Series.Meta.Symbol != "SPY" {
				t.Errorf("file not rewritten with a valid series: %+v", e)
			}
		})
	}
}

func TestStoreRoundTripsSeriesThroughFile(t *testing.T) {
	s, f := newStore(t, time.Hour)
	ctx := context.Background()
	if _, err := s.Series(ctx, "SPY"); err != nil {
		t.Fatalf("Series: %v", err)
	}
	// A second Store on the same directory must serve the file as-is.
	again := New(f, s.dir, time.Hour)
	again.now = s.now
	got, err := again.Series(ctx, "SPY")
	if err != nil {
		t.Fatalf("Series from file: %v", err)
	}
	assertSeries(t, got, "SPY")
	if got.Meta.Currency != "USD" || !got.Meta.FetchedAt.Equal(t0) {
		t.Errorf("meta lost in round trip: %+v", got.Meta)
	}
	if f.count() != 1 {
		t.Errorf("calls = %d, want 1", f.count())
	}
}

func TestStoreWritesVersion2(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatalf("Series: %v", err)
	}
	data, err := os.ReadFile(s.path("SPY"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"version", "fetched_at", "full_fetched_at", "bars", "first_date", "last_date", "series"} {
		if _, ok := top[key]; !ok {
			t.Errorf("file lacks %q key; has %v", key, slices.Sorted(maps.Keys(top)))
		}
	}
	if string(top["version"]) != "2" || string(top["bars"]) != "3" {
		t.Errorf("version = %s, bars = %s; want 2 and 3", top["version"], top["bars"])
	}
	if bytes.LastIndex(data, []byte(`"series"`)) < bytes.LastIndex(data, []byte(`"bars"`)) {
		t.Error("series is not encoded after the summary fields; Status would have to read the bars")
	}
	e := readFile(t, s, "SPY")
	if !e.FetchedAt.Equal(t0) || !e.FullFetchedAt.Equal(t0) {
		t.Errorf("fetched_at = %v, full_fetched_at = %v; want both %v", e.FetchedAt, e.FullFetchedAt, t0)
	}
	if !e.FirstDate.Equal(sampleSeries("SPY").Bars[0].Date) || !e.LastDate.Equal(sampleSeries("SPY").Bars[2].Date) {
		t.Errorf("first_date = %v, last_date = %v", e.FirstDate, e.LastDate)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		want := filepath.Join(t.TempDir(), "override", "nested")
		t.Setenv(EnvCacheDir, want)
		got, err := DefaultDir()
		if err != nil {
			t.Fatalf("DefaultDir: %v", err)
		}
		if got != want {
			t.Errorf("DefaultDir = %q, want %q", got, want)
		}
		if info, err := os.Stat(got); err != nil || !info.IsDir() {
			t.Errorf("DefaultDir did not create %q: %v", got, err)
		}
	})

	t.Run("user cache dir", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(EnvCacheDir, "")
		// Point every platform's cache root at the temp dir.
		t.Setenv("HOME", home)
		t.Setenv("XDG_CACHE_HOME", home)
		t.Setenv("LocalAppData", home)
		got, err := DefaultDir()
		if err != nil {
			t.Fatalf("DefaultDir: %v", err)
		}
		if !strings.HasPrefix(got, home) || filepath.Base(got) != "etf-insight-mcp" {
			t.Errorf("DefaultDir = %q, want <%s>/.../etf-insight-mcp", got, home)
		}
		if info, err := os.Stat(got); err != nil || !info.IsDir() {
			t.Errorf("DefaultDir did not create %q: %v", got, err)
		}
	})
}

// writeTestFile creates path, and its directory, holding content.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setModTime sets the modification time of path to at.
func setModTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestNewSweepsOnlyItsOwnTempFiles: the sweep lists the cache directory
// instead of globbing a pattern built from its path, so glob characters
// in the path cannot reach another directory, and it deletes only the
// temporary files writeJSONAtomic creates.
func TestNewSweepsOnlyItsOwnTempFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cache[x]")
	own := filepath.Join(dir, "SPY.json.123.tmp")
	foreign := filepath.Join(dir, "browser-download.tmp")
	sibling := filepath.Join(root, "cachex", "SPY.json.456.tmp") // matched by the glob cache[x]/*.tmp
	past := time.Now().Add(-time.Hour)
	for _, p := range []string{own, foreign, sibling} {
		writeTestFile(t, p, "x")
		setModTime(t, p, past)
	}

	New(&fakeSource{}, dir, time.Hour)

	if exists(own) {
		t.Error("the cache's own stale temp file was not swept")
	}
	if !exists(foreign) {
		t.Error("another program's temp file in the cache dir was swept")
	}
	if !exists(sibling) {
		t.Error("a temp file in a sibling directory was swept")
	}
}

func TestIsOwnTempFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"SPY.json.123.tmp", true},
		{"BRK-B.json.4294967295.tmp", true},
		{"SPY.profile.json.42.tmp", true},
		{"SPY.123.tmp", false},
		{"SPY.json.tmp", false},
		{"SPY.json.12a.tmp", false},
		{"spy.json.123.tmp", false},
		{"SPY.unknown.json.1.tmp", false},
		{"editor-swap.tmp", false},
		{"SPY.json", false},
	}
	for _, tt := range tests {
		if got := isOwnTempFile(tt.name); got != tt.want {
			t.Errorf("isOwnTempFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestStoreCancelledCallerStartsNoFetch: a caller whose context is already
// done must not start a detached download that nobody waits for.
func TestStoreCancelledCallerStartsNoFetch(t *testing.T) {
	s, f := newStore(t, time.Hour)
	f.gate = make(chan struct{}) // a download, if one started, would stay in flight
	defer close(f.gate)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Series(ctx, "SPY"); !errors.Is(err, context.Canceled) {
		t.Errorf("Series error = %v, want context.Canceled", err)
	}
	if _, err := s.Refresh(ctx, "QQQ"); !errors.Is(err, context.Canceled) {
		t.Errorf("Refresh error = %v, want context.Canceled", err)
	}
	s.mu.Lock()
	inflight := len(s.inflight)
	s.mu.Unlock()
	if inflight != 0 || f.count() != 0 {
		t.Errorf("in flight = %d, source calls = %d; want no download started", inflight, f.count())
	}
}

// TestStoreFetchRechecksFreshFile: a caller that read a stale file just
// before another fetch rewrote it serves the new file instead of fetching
// again; a forced full fetch still goes upstream.
func TestStoreFetchRechecksFreshFile(t *testing.T) {
	s, f := newStore(t, time.Hour)
	fresh := &entry{Version: formatVersion, FetchedAt: t0, FullFetchedAt: t0, Series: sampleSeries("SPY")}
	if err := s.writeEntry("SPY", fresh); err != nil {
		t.Fatal(err)
	}
	out, err := s.fetch(context.Background(), "SPY", false)
	if err != nil {
		t.Fatal(err)
	}
	assertSeries(t, out.series, "SPY")
	if f.count() != 0 {
		t.Errorf("calls = %d, want 0: the file is fresh", f.count())
	}
	if _, err := s.fetch(context.Background(), "SPY", true); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Errorf("calls = %d, want 1 for a full fetch", f.count())
	}
}

// TestStoreRefreshReport: the report describes this call's download, not
// whatever LastError holds by the time the caller looks.
func TestStoreRefreshReport(t *testing.T) {
	ctx := context.Background()
	s, f := newStore(t, time.Hour)

	r, err := s.RefreshReport(ctx, "spy")
	if err != nil || !r.Replaced() {
		t.Fatalf("first refresh = %+v, %v; want a replaced file", r, err)
	}
	assertSeries(t, r.Series, "SPY")

	f.setErr(errUpstream)
	r, err = s.RefreshReport(ctx, "SPY")
	if err != nil {
		t.Fatalf("failed refresh with a cached file returned %v, want the stale series", err)
	}
	if r.Replaced() || !errors.Is(r.FetchErr, errUpstream) || r.WriteErr != nil {
		t.Errorf("failed refresh = %+v, want FetchErr only", r)
	}
	assertSeries(t, r.Series, "SPY")

	// Another call's successful fetch clears LastError; the report keeps
	// the failure of the refresh it describes.
	f.setErr(nil)
	if _, err := s.Refresh(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if s.LastError("SPY") != nil || !errors.Is(r.FetchErr, errUpstream) {
		t.Errorf("LastError = %v, report FetchErr = %v", s.LastError("SPY"), r.FetchErr)
	}

	f.setErr(errUpstream)
	if _, err := s.RefreshReport(ctx, "NEW"); !errors.Is(err, errUpstream) {
		t.Errorf("failed refresh without a file = %v, want the upstream error", err)
	}
	if _, err := s.RefreshReport(ctx, "bad/sym"); !errors.Is(err, ErrInvalidSymbol) {
		t.Errorf("invalid symbol = %v, want ErrInvalidSymbol", err)
	}

	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	writeTestFile(t, blocked, "x")
	r, err = New(&fakeSource{}, blocked, time.Hour).RefreshReport(ctx, "SPY")
	if err != nil || r.Replaced() || r.FetchErr != nil || r.WriteErr == nil {
		t.Errorf("refresh that cannot write = %+v, %v; want WriteErr only", r, err)
	}
}
