package cache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// fakeRangeSource is a fakeSource that can also serve date ranges, cut
// from the same history the full fetch returns unless tail overrides it.
type fakeRangeSource struct {
	fakeSource

	rangeMu    sync.Mutex
	rangeCalls int
	lastFrom   time.Time
	lastTo     time.Time
	rangeErr   error
	tail       func(symbol string, from, to time.Time) *market.Series
}

func (f *fakeRangeSource) SeriesRange(ctx context.Context, symbol string, from, to time.Time) (*market.Series, error) {
	f.rangeMu.Lock()
	f.rangeCalls++
	f.lastFrom, f.lastTo = from, to
	err := f.rangeErr
	tail := f.tail
	f.rangeMu.Unlock()

	if gateErr := f.waitGate(ctx); gateErr != nil {
		return nil, gateErr
	}
	if err != nil {
		return nil, err
	}
	if tail != nil {
		return tail(symbol, from, to), nil
	}
	f.mu.Lock()
	series := f.series
	f.mu.Unlock()
	return cut(series(symbol), from, to), nil
}

func (f *fakeRangeSource) ranges() int {
	f.rangeMu.Lock()
	defer f.rangeMu.Unlock()
	return f.rangeCalls
}

// newRangeStore returns a Store over a RangeSource whose history is
// providerHistory, with the clock pinned to t0 and a 6 hour TTL.
func newRangeStore(t *testing.T, opts ...Option) (*Store, *fakeRangeSource) {
	t.Helper()
	f := &fakeRangeSource{}
	f.series = func(string) *market.Series { return providerHistory() }
	opts = append([]Option{WithClock(func() time.Time { return t0 })}, opts...)
	return New(f, t.TempDir(), 6*time.Hour, opts...), f
}

// staleEntry is the file on disk before a top-up: fetched 7 hours ago
// (past the TTL), fetched in full 10 days ago (inside the 30 day window).
func staleEntry() *entry {
	return &entry{
		Version:       formatVersion,
		FetchedAt:     t0.Add(-7 * time.Hour),
		FullFetchedAt: t0.AddDate(0, 0, -10),
		Series:        cachedHistory(),
	}
}

func seed(t *testing.T, s *Store, e *entry) {
	t.Helper()
	if err := s.writeEntry("SPY", e); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestStoreTopUpAppends(t *testing.T) {
	s, f := newRangeStore(t)
	seed(t, s, staleEntry())

	got, err := s.Series(context.Background(), "spy")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if f.ranges() != 1 || f.count() != 0 {
		t.Fatalf("range calls = %d, full calls = %d; want 1 and 0", f.ranges(), f.count())
	}
	if want := date("2026-09-25"); !f.lastFrom.Equal(want) {
		t.Errorf("from = %v, want last bar - 7 days = %v", f.lastFrom, want)
	}
	if want := date("2026-10-08"); !f.lastTo.Equal(want) {
		t.Errorf("to = %v, want today %v", f.lastTo, want)
	}
	want := []float64{100, 101, 102, 103, 104, 105, 106, 107}
	if !equalFloats(closes(got), want) {
		t.Errorf("closes = %v, want %v", closes(got), want)
	}
	e := readFile(t, s, "SPY")
	if e.Version != 2 || !e.FetchedAt.Equal(t0) || !e.FullFetchedAt.Equal(t0.AddDate(0, 0, -10)) {
		t.Errorf("file version=%d fetched_at=%v full_fetched_at=%v; want 2, now, unchanged", e.Version, e.FetchedAt, e.FullFetchedAt)
	}
	if e.Bars != 8 || !e.LastDate.Equal(date("2026-10-07")) {
		t.Errorf("summary bars=%d last=%v, want 8 and 2026-10-07", e.Bars, e.LastDate)
	}
	if s.LastError("SPY") != nil {
		t.Errorf("LastError = %v", s.LastError("SPY"))
	}

	// The topped-up file is fresh now: no call of either kind.
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	if f.ranges() != 1 || f.count() != 0 {
		t.Errorf("fresh read hit the source: range=%d full=%d", f.ranges(), f.count())
	}
}

func TestStoreTopUpFallsBackToFullFetch(t *testing.T) {
	tests := []struct {
		name      string
		seed      func(e *entry)
		truth     func(s *market.Series)
		wantRange int
	}{
		{
			name:      "close changed in the overlap",
			truth:     func(s *market.Series) { s.Bars[3].Close = 999 },
			wantRange: 1,
		},
		{
			name:      "new dividend in the tail",
			truth:     func(s *market.Series) { s.Bars[6].Dividend = 0.5 },
			wantRange: 1,
		},
		{
			name: "new split in the tail",
			truth: func(s *market.Series) {
				s.Splits = []market.Split{{Date: date("2026-10-06"), Numerator: 2, Denominator: 1}}
			},
			wantRange: 1,
		},
		{
			name:      "full fetch older than 30 days",
			seed:      func(e *entry) { e.FullFetchedAt = t0.AddDate(0, 0, -31) },
			wantRange: 0,
		},
		{
			name:      "full fetch time in the future",
			seed:      func(e *entry) { e.FullFetchedAt = t0.Add(time.Hour) },
			wantRange: 0,
		},
		{
			name:      "cached history without bars",
			seed:      func(e *entry) { e.Series.Bars = nil },
			wantRange: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, f := newRangeStore(t)
			e := staleEntry()
			if tt.seed != nil {
				tt.seed(e)
			}
			seed(t, s, e)
			if tt.truth != nil {
				f.series = func(string) *market.Series {
					h := providerHistory()
					tt.truth(h)
					return h
				}
			}

			got, err := s.Series(context.Background(), "SPY")
			if err != nil {
				t.Fatalf("Series: %v", err)
			}
			if f.ranges() != tt.wantRange || f.count() != 1 {
				t.Errorf("range calls = %d, full calls = %d; want %d and 1", f.ranges(), f.count(), tt.wantRange)
			}
			if got.Len() != 8 {
				t.Errorf("bars = %d, want the provider's 8", got.Len())
			}
			file := readFile(t, s, "SPY")
			if !file.FetchedAt.Equal(t0) || !file.FullFetchedAt.Equal(t0) {
				t.Errorf("fetched_at=%v full_fetched_at=%v, want both now after a full fetch", file.FetchedAt, file.FullFetchedAt)
			}
		})
	}
}

func TestStoreTopUpReplacesNewestBar(t *testing.T) {
	s, f := newRangeStore(t)
	seed(t, s, staleEntry())
	f.series = func(string) *market.Series {
		h := providerHistory()
		h.Bars[4].Close, h.Bars[4].AdjClose = 104.5, 104.5 // settled close of the intraday bar
		return h
	}
	got, err := s.Series(context.Background(), "SPY")
	if err != nil {
		t.Fatal(err)
	}
	if f.count() != 0 {
		t.Errorf("full calls = %d, want 0: the newest cached bar may change", f.count())
	}
	if got.Bars[4].Close != 104.5 {
		t.Errorf("bar 2026-10-02 close = %v, want 104.5", got.Bars[4].Close)
	}
}

func TestStoreTopUpEmptyTail(t *testing.T) {
	s, f := newRangeStore(t)
	seed(t, s, staleEntry())
	f.tail = func(symbol string, _, _ time.Time) *market.Series {
		return &market.Series{Meta: market.Meta{Symbol: symbol}}
	}
	got, err := s.Series(context.Background(), "SPY")
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 5 || f.count() != 0 {
		t.Errorf("bars = %d, full calls = %d; want 5 unchanged and 0", got.Len(), f.count())
	}
	if e := readFile(t, s, "SPY"); !e.FetchedAt.Equal(t0) {
		t.Errorf("fetched_at = %v, want bumped to now", e.FetchedAt)
	}
}

func TestStoreTopUpErrorServesStale(t *testing.T) {
	s, f := newRangeStore(t)
	seed(t, s, staleEntry())
	f.rangeErr = errUpstream

	got, err := s.Series(context.Background(), "SPY")
	if err != nil {
		t.Fatalf("Series returned %v, want the stale file", err)
	}
	if got.Len() != 5 || f.count() != 0 {
		t.Errorf("bars = %d, full calls = %d; want the 5 cached bars and no full fetch", got.Len(), f.count())
	}
	if last := s.LastError("SPY"); !errors.Is(last, errUpstream) {
		t.Errorf("LastError = %v, want wrapped upstream error", last)
	}
	if e := readFile(t, s, "SPY"); !e.FetchedAt.Equal(t0.Add(-7 * time.Hour)) {
		t.Errorf("file rewritten after a failed top-up: fetched_at = %v", e.FetchedAt)
	}
}

func TestStoreRefreshIsAlwaysFull(t *testing.T) {
	s, f := newRangeStore(t)
	seed(t, s, staleEntry())
	if _, err := s.Refresh(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	if f.ranges() != 0 || f.count() != 1 {
		t.Errorf("range calls = %d, full calls = %d; want 0 and 1", f.ranges(), f.count())
	}
}

func TestStoreV1FileIsRefetchedAndRewritten(t *testing.T) {
	s, f := newRangeStore(t)
	v1, err := json.Marshal(entryV1{FetchedAt: t0.Add(-time.Minute), Series: cachedHistory()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("SPY"), v1, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Series(context.Background(), "SPY")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if f.ranges() != 0 || f.count() != 1 {
		t.Errorf("range calls = %d, full calls = %d; want a full fetch even though the v1 file is a minute old", f.ranges(), f.count())
	}
	if got.Len() != 8 {
		t.Errorf("bars = %d, want 8", got.Len())
	}
	data, err := os.ReadFile(s.path("SPY"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["version"]) != "2" || top["FetchedAt"] != nil {
		t.Errorf("file not rewritten as version 2: %s", data[:min(len(data), 120)])
	}

	// Stale-on-error still serves a v1 file.
	f.setErr(errUpstream)
	again := New(f, t.TempDir(), 6*time.Hour, WithClock(func() time.Time { return t0 }))
	if err := os.MkdirAll(again.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(again.path("SPY"), v1, 0o600); err != nil {
		t.Fatal(err)
	}
	stale, err := again.Series(context.Background(), "SPY")
	if err != nil || stale.Len() != 5 {
		t.Errorf("stale v1 read = %v bars, %v; want the 5 cached bars", stale.Len(), err)
	}
}

func TestStoreTopUpSharesInFlightFetch(t *testing.T) {
	s, f := newRangeStore(t)
	seed(t, s, staleEntry())
	f.gate = make(chan struct{})

	const n = 10
	var (
		done    sync.WaitGroup
		results = make([]*market.Series, n)
		errs    = make([]error, n)
	)
	done.Add(n)
	for i := range n {
		go func() {
			defer done.Done()
			results[i], errs[i] = s.Series(context.Background(), "SPY")
		}()
	}
	waitFor(t, "the leader to reach the range source", func() bool { return f.ranges() == 1 })
	close(f.gate)
	done.Wait()

	if f.ranges() != 1 || f.count() != 0 {
		t.Errorf("range calls = %d, full calls = %d; want 1 and 0 for %d concurrent stale reads", f.ranges(), f.count(), n)
	}
	for i := range n {
		if errs[i] != nil {
			t.Errorf("reader %d: %v", i, errs[i])
		} else if results[i].Len() != 8 {
			t.Errorf("reader %d got %d bars, want 8", i, results[i].Len())
		}
	}
}

func TestStoreOptions(t *testing.T) {
	s, f := newRangeStore(t, WithOverlap(2*24*time.Hour), WithFullRefetchInterval(5*24*time.Hour))
	seed(t, s, staleEntry()) // full fetch 10 days ago: past the 5 day interval
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	if f.ranges() != 0 || f.count() != 1 {
		t.Errorf("range calls = %d, full calls = %d; want the shorter interval to force a full fetch", f.ranges(), f.count())
	}

	s.now = func() time.Time { return t0.Add(7 * time.Hour) } // stale again, full fetch 7h ago
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	if f.ranges() != 1 {
		t.Fatalf("range calls = %d, want 1", f.ranges())
	}
	if want := date("2026-10-05"); !f.lastFrom.Equal(want) {
		t.Errorf("from = %v, want last bar - 2 days = %v", f.lastFrom, want)
	}

	zero := New(f, t.TempDir(), 0, WithClock(nil), WithOverlap(0), WithFullRefetchInterval(-1))
	if zero.ttl != DefaultTTL || zero.overlap != DefaultOverlap || zero.fullInterval != DefaultFullRefetchInterval || zero.now == nil {
		t.Errorf("non-positive settings did not fall back to the defaults: %+v", zero)
	}
}
