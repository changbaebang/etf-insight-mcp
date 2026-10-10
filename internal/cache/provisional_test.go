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

func TestRefreshMetaTakesTheTailsProvisionalState(t *testing.T) {
	have := market.Meta{Symbol: "SPY", ProvisionalUntil: t0}
	if got := refreshMeta(have, market.Meta{}); !got.ProvisionalUntil.IsZero() {
		t.Errorf("settled tail: ProvisionalUntil = %v, want zero", got.ProvisionalUntil)
	}
	later := t0.Add(24 * time.Hour)
	if got := refreshMeta(market.Meta{Symbol: "SPY"}, market.Meta{ProvisionalUntil: later}); !got.ProvisionalUntil.Equal(later) {
		t.Errorf("intraday tail: ProvisionalUntil = %v, want %v", got.ProvisionalUntil, later)
	}
}
