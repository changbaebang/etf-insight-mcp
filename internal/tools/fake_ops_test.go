package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// opsFlakySource is the shared fake source with a switch that makes every
// fetch fail, so a test can drive the cache into stale-on-error.
type opsFlakySource struct {
	*fakeSource
	down atomic.Bool
}

func newOpsFlakySource() *opsFlakySource {
	return &opsFlakySource{fakeSource: newFakeSource()}
}

func (f *opsFlakySource) Series(ctx context.Context, symbol string) (*market.Series, error) {
	if f.down.Load() {
		return nil, errors.New("fake: upstream down")
	}
	return f.fakeSource.Series(ctx, symbol)
}

// opsCache wraps src in a real cache.Store under a fresh temp dir, on the
// tests' fixed clock, and returns Deps that use it as Source and Cache.
func opsCache(t *testing.T, src market.Source) (Deps, *cache.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store := cache.New(src, dir, time.Hour, cache.WithClock(func() time.Time { return now }))
	deps := testDeps(store)
	deps.Cache = store
	return deps, store, dir
}

// opsWarm loads symbols through store so their price files exist.
func opsWarm(t *testing.T, store *cache.Store, symbols ...string) {
	t.Helper()
	for _, sym := range symbols {
		if _, err := store.Series(context.Background(), sym); err != nil {
			t.Fatalf("warm %s: %v", sym, err)
		}
	}
}

// opsWriteFundDoc writes a fund document where cache.FundStore keeps it
// and returns its size.
func opsWriteFundDoc(t *testing.T, dir, sym, doc string) int64 {
	t.Helper()
	path := filepath.Join(dir, "fund", sym+"."+doc+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":1,"fetched_at":%q,"data":{}}`, now.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return int64(len(body))
}

// opsWritePriceHeader writes a price file that holds only a format-2
// header, enough for Status, which stops reading before the series.
func opsWritePriceHeader(t *testing.T, dir, sym string) {
	t.Helper()
	at := now.Format(time.RFC3339)
	body := fmt.Sprintf(`{"version":2,"fetched_at":%q,"full_fetched_at":%q,"bars":1,"first_date":"2024-01-02T00:00:00Z","last_date":"2024-01-02T00:00:00Z","series":null}`, at, at)
	if err := os.WriteFile(filepath.Join(dir, sym+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// opsFileSize returns the size of path or fails the test.
func opsFileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// opsExists reports whether path exists.
func opsExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// opsProgressSession is newSession with a client that forwards every
// progress notification to the returned channel.
func opsProgressSession(t *testing.T, deps Deps) (*mcp.ClientSession, <-chan *mcp.ProgressNotificationParams) {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	s := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0"}, &mcp.ServerOptions{Instructions: Instructions})
	Register(s, deps)
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	notes := make(chan *mcp.ProgressNotificationParams, 64)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			notes <- req.Params
		},
	})
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, notes
}

// opsBlockingSource counts the fetches it starts and holds each one until
// release is closed, then fails it, so nothing reaches the cache files.
type opsBlockingSource struct {
	started atomic.Int64
	running atomic.Int64
	release chan struct{}
}

func newOpsBlockingSource(t *testing.T) *opsBlockingSource {
	t.Helper()
	src := &opsBlockingSource{release: make(chan struct{})}
	// The cache runs fetches detached from the caller, so they may outlive
	// the call under test. Release them and wait, so none is still running
	// when t.TempDir is removed.
	t.Cleanup(func() {
		close(src.release)
		deadline := time.Now().Add(5 * time.Second)
		for src.running.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	})
	return src
}

func (b *opsBlockingSource) Series(context.Context, string) (*market.Series, error) {
	b.started.Add(1)
	b.running.Add(1)
	defer b.running.Add(-1)
	<-b.release
	return nil, errors.New("fake: released")
}
