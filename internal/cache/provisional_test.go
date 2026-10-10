package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// intradaySource returns sampleSeries marked as fetched mid-session until
// told otherwise.
type intradaySource struct {
	calls atomic.Int32
	until time.Time
}

func (s *intradaySource) Series(_ context.Context, symbol string) (*market.Series, error) {
	s.calls.Add(1)
	out := sampleSeries(symbol)
	out.Meta.ProvisionalUntil = s.until
	return out, nil
}

// TestStoreRefetchesIntradayBarAfterTheSessionEnds: a file whose last bar
// was an intraday price is fresh until the session ends, then stale even
// though the TTL has not run out.
func TestStoreRefetchesIntradayBarAfterTheSessionEnds(t *testing.T) {
	ctx := context.Background()
	clock := t0
	src := &intradaySource{until: t0.Add(2 * time.Hour)}
	s := New(src, t.TempDir(), 6*time.Hour, WithClock(func() time.Time { return clock }))

	if _, err := s.Series(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	clock = t0.Add(time.Hour) // session still open
	if _, err := s.Series(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if c := src.calls.Load(); c != 1 {
		t.Fatalf("calls during the session = %d, want 1 (file still fresh)", c)
	}
	clock = t0.Add(3 * time.Hour) // session over, TTL not yet
	src.until = time.Time{}       // the provider now reports a settled close
	got, err := s.Series(ctx, "SPY")
	if err != nil {
		t.Fatal(err)
	}
	if c := src.calls.Load(); c != 2 {
		t.Errorf("calls after the session = %d, want 2 (intraday bar must be refetched)", c)
	}
	if !got.Meta.ProvisionalUntil.IsZero() {
		t.Errorf("ProvisionalUntil after the refetch = %v, want zero", got.Meta.ProvisionalUntil)
	}
}

// TestMergeTailProvisionalState: the merged series is intraday exactly
// when its last bar is: the tail's state when the tail supplies the last
// bar, the cache's when an empty tail leaves the cached bar in place, and
// none when the newest cached bar was dropped.
func TestMergeTailProvisionalState(t *testing.T) {
	cachedUntil, tailUntil := t0.Add(time.Hour), t0.Add(26*time.Hour)
	intradayBase := func() *market.Series {
		b := cachedHistory()
		b.Meta.ProvisionalUntil = cachedUntil
		return b
	}

	// A tail that settles the newest cached day and adds an intraday one.
	tail := cut(providerHistory(), date("2026-09-25"), date("2026-10-08"))
	tail.Meta.ProvisionalUntil = tailUntil
	merged, err := mergeTail(intradayBase(), tail, windowFrom)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Meta.ProvisionalUntil.Equal(tailUntil) {
		t.Errorf("tail supplies the last bar: ProvisionalUntil = %v, want %v", merged.Meta.ProvisionalUntil, tailUntil)
	}

	// A settled tail that ends on the newest cached day.
	settled := cut(cachedHistory(), date("2026-09-25"), date("2026-10-02"))
	merged, err = mergeTail(intradayBase(), settled, windowFrom)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Meta.ProvisionalUntil.IsZero() {
		t.Errorf("settled tail: ProvisionalUntil = %v, want zero", merged.Meta.ProvisionalUntil)
	}

	// An empty tail keeps the cached intraday bar, and its flag.
	base := intradayBase()
	merged, err = mergeTail(base, &market.Series{Meta: market.Meta{Symbol: "SPY"}}, windowFrom)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Meta.ProvisionalUntil.Equal(cachedUntil) {
		t.Errorf("empty tail: ProvisionalUntil = %v, want the cached %v", merged.Meta.ProvisionalUntil, cachedUntil)
	}

	// A tail without the newest cached day and nothing newer drops that
	// intraday bar; the settled bar before it is last.
	short := cut(cachedHistory(), date("2026-09-25"), date("2026-10-01"))
	merged, err = mergeTail(intradayBase(), short, windowFrom)
	if err != nil {
		t.Fatal(err)
	}
	if last, _ := merged.Last(); !last.Date.Equal(date("2026-10-01")) || !merged.Meta.ProvisionalUntil.IsZero() {
		t.Errorf("dropped intraday bar: last %s, ProvisionalUntil %v; want 2026-10-01 and zero", last.Date.Format("2006-01-02"), merged.Meta.ProvisionalUntil)
	}
}
