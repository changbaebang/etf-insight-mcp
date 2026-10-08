package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrefetchRespectsConcurrencyLimit(t *testing.T) {
	const limit = 2
	f := &fakeSource{gate: make(chan struct{})}
	symbols := []string{"A", "B", "C", "D", "E"}

	result := make(chan map[string]error, 1)
	go func() { result <- Prefetch(context.Background(), f, symbols, limit) }()

	waitFor(t, "the limit to be reached", func() bool { return f.inflight.Load() == limit })
	if got := f.maxInflight.Load(); got != limit {
		t.Errorf("max in flight = %d before release, want %d", got, limit)
	}
	close(f.gate)

	select {
	case errs := <-result:
		if len(errs) != 0 {
			t.Errorf("errors = %v, want none", errs)
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

func TestPrefetchReportsPerSymbolErrors(t *testing.T) {
	errB := errors.New("b broke")
	errD := errors.New("d broke")
	f := &fakeSource{failFor: map[string]error{"B": errB, "D": errD}}

	errs := Prefetch(context.Background(), f, []string{"A", "B", "C", "D"}, 4)
	if len(errs) != 2 {
		t.Fatalf("errors = %v, want exactly B and D", errs)
	}
	if !errors.Is(errs["B"], errB) || !errors.Is(errs["D"], errD) {
		t.Errorf("errors = %v, want B->%v D->%v", errs, errB, errD)
	}
	if f.count() != 4 {
		t.Errorf("calls = %d, want 4", f.count())
	}
}

func TestPrefetchStopsWhenContextDone(t *testing.T) {
	f := &fakeSource{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	symbols := []string{"A", "B", "C"}
	errs := Prefetch(ctx, f, symbols, 2)
	if f.count() != 0 {
		t.Errorf("calls = %d, want 0 after cancellation", f.count())
	}
	for _, sym := range symbols {
		if !errors.Is(errs[sym], context.Canceled) {
			t.Errorf("errs[%s] = %v, want context.Canceled", sym, errs[sym])
		}
	}
}

func TestPrefetchEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		symbols     []string
		concurrency int
		wantCalls   int
	}{
		{"no symbols", nil, 2, 0},
		{"concurrency below one is clamped", []string{"A", "B"}, 0, 2},
		{"duplicates are fetched each time", []string{"A", "A"}, 1, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSource{}
			errs := Prefetch(context.Background(), f, tt.symbols, tt.concurrency)
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

func TestPrefetchWarmsStore(t *testing.T) {
	s, f := newStore(t, time.Hour)
	symbols := []string{"SPY", "QQQ", "KRW=X"}
	if errs := Prefetch(context.Background(), s, symbols, 3); len(errs) != 0 {
		t.Fatalf("Prefetch errors = %v", errs)
	}
	if f.count() != len(symbols) {
		t.Fatalf("calls = %d, want %d", f.count(), len(symbols))
	}
	for _, sym := range symbols {
		if _, err := s.Series(context.Background(), sym); err != nil {
			t.Errorf("Series(%s) after Prefetch: %v", sym, err)
		}
	}
	if f.count() != len(symbols) {
		t.Errorf("warm reads hit the source: calls = %d", f.count())
	}
}
