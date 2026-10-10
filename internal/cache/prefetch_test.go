package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// prefetchResult is what one Store.Prefetch call returned.
type prefetchResult struct {
	series map[string]*market.Series
	errs   map[string]error
}

func TestStorePrefetchRespectsConcurrencyLimit(t *testing.T) {
	const limit = 2
	s, f := newStore(t, time.Hour)
	f.gate = make(chan struct{})
	symbols := []string{"A", "B", "C", "D", "E"}

	result := make(chan prefetchResult, 1)
	go func() {
		series, errs := s.Prefetch(context.Background(), symbols, limit)
		result <- prefetchResult{series, errs}
	}()

	waitFor(t, "the limit to be reached", func() bool { return f.inflight.Load() == limit })
	if got := f.maxInflight.Load(); got != limit {
		t.Errorf("max in flight = %d before release, want %d", got, limit)
	}
	close(f.gate)

	select {
	case r := <-result:
		if len(r.errs) != 0 || len(r.series) != len(symbols) {
			t.Errorf("series = %d, errors = %v; want %d and none", len(r.series), r.errs, len(symbols))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prefetch did not return")
	}
	if got := f.maxInflight.Load(); got != limit {
		t.Errorf("max in flight = %d, want exactly %d", got, limit)
	}
	if f.count() != len(symbols) {
		t.Errorf("calls = %d, want %d", f.count(), len(symbols))
	}
}

func TestStorePrefetchStopsWhenContextDone(t *testing.T) {
	s, f := newStore(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	symbols := []string{"A", "B", "C"}
	series, errs := s.Prefetch(ctx, symbols, 2)
	if f.count() != 0 || len(series) != 0 {
		t.Errorf("calls = %d, series = %d; want none after cancellation", f.count(), len(series))
	}
	for _, sym := range symbols {
		if !errors.Is(errs[sym], context.Canceled) {
			t.Errorf("errs[%s] = %v, want context.Canceled", sym, errs[sym])
		}
	}
}

func TestStorePrefetchEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		symbols     []string
		concurrency int
		wantCalls   int
	}{
		{"no symbols", nil, 2, 0},
		{"concurrency below one is clamped", []string{"A", "B"}, 0, 2},
		{"a duplicate is served from the file the first fetch wrote", []string{"A", "A"}, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, f := newStore(t, time.Hour)
			_, errs := s.Prefetch(context.Background(), tt.symbols, tt.concurrency)
			if len(errs) != 0 {
				t.Errorf("errors = %v, want none", errs)
			}
			if f.count() != tt.wantCalls {
				t.Errorf("calls = %d, want %d", f.count(), tt.wantCalls)
			}
			if f.maxInflight.Load() > int32(max(tt.concurrency, 1)) {
				t.Errorf("max in flight = %d exceeds limit", f.maxInflight.Load())
			}
		})
	}
}
