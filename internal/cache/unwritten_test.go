package cache

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// growingSource returns one more bar on every call, so a test can tell
// the downloads apart by length.
type growingSource struct{ calls atomic.Int32 }

func (g *growingSource) Series(_ context.Context, symbol string) (*market.Series, error) {
	n := int(g.calls.Add(1))
	s := sampleSeries(symbol)
	last := s.Bars[len(s.Bars)-1]
	for i := 1; i <= n; i++ {
		b := last
		b.Date = last.Date.AddDate(0, 0, i)
		s.Bars = append(s.Bars, b)
	}
	return s, nil
}

// TestStoreServesUnwritableRefreshFromMemory: a refresh whose download
// succeeds but whose write fails must not be undone by the next read,
// which would otherwise find the older file still inside its TTL.
func TestStoreServesUnwritableRefreshFromMemory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	dir := t.TempDir()
	src := &growingSource{}
	s := New(src, dir, time.Hour)

	first, err := s.Series(ctx, "SPY")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	refreshed, err := s.Refresh(ctx, "SPY")
	if err != nil {
		t.Fatal(err)
	}
	if s.LastWriteError("SPY") == nil {
		t.Fatal("the refresh should have failed to write into a read-only directory")
	}
	if refreshed.Len() <= first.Len() {
		t.Fatalf("refresh returned %d bars, want more than the first fetch's %d", refreshed.Len(), first.Len())
	}

	got, err := s.Series(ctx, "SPY")
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != refreshed.Len() {
		t.Errorf("read after the failed write returned %d bars, want the refreshed %d (not the older file's %d)", got.Len(), refreshed.Len(), first.Len())
	}
	if c := src.calls.Load(); c != 2 {
		t.Errorf("upstream calls = %d, want 2: the refreshed series is fresh and must come from memory", c)
	}

	// Clearing the symbol forgets the in-memory copy too.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clear([]string{"SPY"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Series(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if c := src.calls.Load(); c != 3 {
		t.Errorf("upstream calls after Clear = %d, want 3", c)
	}
}
