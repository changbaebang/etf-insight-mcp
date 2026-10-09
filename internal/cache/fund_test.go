package cache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// fakeFundSource counts calls per method, records the symbols passed to
// Quote, fails every call when err is set and omits quotes for symbols
// that start with "UNKNOWN".
type fakeFundSource struct {
	mu        sync.Mutex
	calls     map[string]int
	quoteSyms [][]string
	err       error
}

func (f *fakeFundSource) hit(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[method]++
	return f.err
}

func (f *fakeFundSource) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeFundSource) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeFundSource) Quote(_ context.Context, symbols []string) ([]market.Quote, error) {
	if err := f.hit("Quote"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.quoteSyms = append(f.quoteSyms, slices.Clone(symbols))
	f.mu.Unlock()
	var out []market.Quote
	for _, sym := range symbols {
		if strings.HasPrefix(sym, "UNKNOWN") {
			continue
		}
		out = append(out, market.Quote{Symbol: sym, Price: float64(len(sym)), AsOf: t0})
	}
	return out, nil
}

func (f *fakeFundSource) FundProfile(_ context.Context, symbol string) (*market.FundProfile, error) {
	if err := f.hit("FundProfile"); err != nil {
		return nil, err
	}
	ratio := 0.0003
	return &market.FundProfile{Symbol: symbol, Name: "Fund " + symbol, ExpenseRatio: &ratio, FetchedAt: t0}, nil
}

func (f *fakeFundSource) Holdings(_ context.Context, symbol string) (*market.Holdings, error) {
	if err := f.hit("Holdings"); err != nil {
		return nil, err
	}
	return &market.Holdings{Symbol: symbol, Top: []market.Holding{{Symbol: "AAPL", Name: "Apple", Weight: 0.07}}, EquityStats: map[string]float64{"priceToBook": 4.5}}, nil
}

func (f *fakeFundSource) Performance(_ context.Context, symbol string) (*market.Performance, error) {
	if err := f.hit("Performance"); err != nil {
		return nil, err
	}
	ret := 0.12
	return &market.Performance{Symbol: symbol, Trailing: []market.PeriodReturn{{Period: "1y", Fund: &ret}}}, nil
}

func (f *fakeFundSource) Search(_ context.Context, query string, _ int) ([]market.SearchHit, error) {
	if err := f.hit("Search"); err != nil {
		return nil, err
	}
	return []market.SearchHit{{Symbol: strings.ToUpper(query), Type: "ETF"}}, nil
}

func (f *fakeFundSource) News(_ context.Context, query string, _ int) ([]market.NewsItem, error) {
	if err := f.hit("News"); err != nil {
		return nil, err
	}
	return []market.NewsItem{{Title: query}}, nil
}

// newFundStore returns a FundStore over a fresh fake with the clock pinned
// to t0 and the default TTLs.
func newFundStore(t *testing.T, opts ...FundOption) (*FundStore, *fakeFundSource) {
	t.Helper()
	f := &fakeFundSource{}
	opts = append([]FundOption{WithFundClock(func() time.Time { return t0 })}, opts...)
	return NewFundStore(f, t.TempDir(), opts...), f
}

// docKinds maps each on-disk kind to a call through the FundStore that
// returns the document's symbol.
var docKinds = []struct {
	kind string
	call func(ctx context.Context, fs *FundStore, sym string) (string, error)
}{
	{kindProfile, func(ctx context.Context, fs *FundStore, sym string) (string, error) {
		p, err := fs.FundProfile(ctx, sym)
		if err != nil {
			return "", err
		}
		return p.Symbol, nil
	}},
	{kindHoldings, func(ctx context.Context, fs *FundStore, sym string) (string, error) {
		h, err := fs.Holdings(ctx, sym)
		if err != nil {
			return "", err
		}
		return h.Symbol, nil
	}},
	{kindPerformance, func(ctx context.Context, fs *FundStore, sym string) (string, error) {
		p, err := fs.Performance(ctx, sym)
		if err != nil {
			return "", err
		}
		return p.Symbol, nil
	}},
}

// method is the fake's method name for a kind.
func method(kind string) string {
	if kind == kindProfile {
		return "FundProfile"
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func TestFundStoreDocumentTTL(t *testing.T) {
	for _, dk := range docKinds {
		t.Run(dk.kind, func(t *testing.T) {
			fs, f := newFundStore(t)
			ctx := context.Background()

			got, err := dk.call(ctx, fs, " spy ")
			if err != nil || got != "SPY" {
				t.Fatalf("cold call = %q, %v", got, err)
			}
			path := filepath.Join(fs.dir, fundFileName("SPY", dk.kind))
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("no file written: %v", err)
			}
			if _, err := dk.call(ctx, fs, "SPY"); err != nil {
				t.Fatal(err)
			}
			if f.count(method(dk.kind)) != 1 {
				t.Errorf("calls after warm read = %d, want 1", f.count(method(dk.kind)))
			}

			fs.now = func() time.Time { return t0.Add(DefaultFundTTL + time.Second) }
			if _, err := dk.call(ctx, fs, "SPY"); err != nil {
				t.Fatal(err)
			}
			if f.count(method(dk.kind)) != 2 {
				t.Errorf("calls after TTL expiry = %d, want 2", f.count(method(dk.kind)))
			}
			if at, err := readFetchedAt(path); err != nil || !at.Equal(t0.Add(DefaultFundTTL+time.Second)) {
				t.Errorf("file fetched_at = %v, %v; want rewritten with the new time", at, err)
			}

			// Another FundStore on the same directory serves the file.
			again := NewFundStore(f, filepath.Dir(fs.dir), WithFundClock(fs.now))
			if got, err := dk.call(ctx, again, "SPY"); err != nil || got != "SPY" {
				t.Errorf("read through a second store = %q, %v", got, err)
			}
			if f.count(method(dk.kind)) != 2 {
				t.Errorf("second store hit the source: calls = %d", f.count(method(dk.kind)))
			}
		})
	}
}

func TestFundStoreRoundTripsDocuments(t *testing.T) {
	fs, f := newFundStore(t)
	ctx := context.Background()
	if _, err := fs.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Holdings(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Performance(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	again := NewFundStore(f, filepath.Dir(fs.dir), WithFundClock(fs.now))
	profile, err := again.FundProfile(ctx, "SPY")
	if err != nil || profile.Name != "Fund SPY" || profile.ExpenseRatio == nil || *profile.ExpenseRatio != 0.0003 || !profile.FetchedAt.Equal(t0) {
		t.Errorf("profile from file = %+v, %v", profile, err)
	}
	holdings, err := again.Holdings(ctx, "SPY")
	if err != nil || len(holdings.Top) != 1 || holdings.Top[0].Weight != 0.07 || holdings.EquityStats["priceToBook"] != 4.5 {
		t.Errorf("holdings from file = %+v, %v", holdings, err)
	}
	perf, err := again.Performance(ctx, "SPY")
	if err != nil || len(perf.Trailing) != 1 || perf.Trailing[0].Fund == nil || *perf.Trailing[0].Fund != 0.12 {
		t.Errorf("performance from file = %+v, %v", perf, err)
	}
	for _, m := range []string{"FundProfile", "Holdings", "Performance"} {
		if f.count(m) != 1 {
			t.Errorf("%s called %d times, want 1", m, f.count(m))
		}
	}
}

func TestFundStoreStaleOnError(t *testing.T) {
	fs, f := newFundStore(t)
	ctx := context.Background()
	if _, err := fs.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	f.setErr(errUpstream)
	fs.now = func() time.Time { return t0.Add(25 * time.Hour) }

	got, err := fs.FundProfile(ctx, "SPY")
	if err != nil || got == nil || got.Name != "Fund SPY" {
		t.Fatalf("stale read = %+v, %v; want the cached profile", got, err)
	}
	last := fs.LastError("spy")
	if !errors.Is(last, errUpstream) || !strings.Contains(last.Error(), "profile SPY") {
		t.Errorf("LastError = %v, want wrapped upstream error naming the document", last)
	}
	if fs.LastError("QQQ") != nil {
		t.Errorf("LastError(QQQ) = %v, want nil", fs.LastError("QQQ"))
	}

	// Without a file the error surfaces and nothing is written.
	if _, err := fs.Holdings(ctx, "SPY"); !errors.Is(err, errUpstream) {
		t.Errorf("Holdings without a file = %v, want upstream error", err)
	}
	if _, err := os.Stat(filepath.Join(fs.dir, fundFileName("SPY", kindHoldings))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a holdings file was written despite the failure: %v", err)
	}

	f.setErr(nil)
	if _, err := fs.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Holdings(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if fs.LastError("SPY") != nil {
		t.Errorf("LastError after recovery = %v, want nil", fs.LastError("SPY"))
	}
}

func TestFundStoreCorruptFileIsMiss(t *testing.T) {
	fs, f := newFundStore(t)
	ctx := context.Background()
	if _, err := fs.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fs.dir, fundFileName("SPY", kindProfile))
	for _, body := range []string{"garbage", `{"version":1,"fetched_at":"2026-10-08T09:00:00Z"}`, `{"version":9,"fetched_at":"2026-10-08T09:00:00Z","data":{}}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		before := f.count("FundProfile")
		if _, err := fs.FundProfile(ctx, "SPY"); err != nil {
			t.Fatal(err)
		}
		if f.count("FundProfile") != before+1 {
			t.Errorf("body %q was not treated as a miss", body)
		}
	}
}

func TestFundStoreQuoteMemo(t *testing.T) {
	fs, f := newFundStore(t)
	ctx := context.Background()

	got, err := fs.Quote(ctx, []string{"spy", "qqq", "SPY"})
	if err != nil {
		t.Fatal(err)
	}
	if syms := quoteSymbols(got); !slices.Equal(syms, []string{"SPY", "QQQ"}) {
		t.Errorf("quotes = %v, want SPY, QQQ once each in request order", syms)
	}
	if f.count("Quote") != 1 || !slices.Equal(f.quoteSyms[0], []string{"SPY", "QQQ"}) {
		t.Errorf("source asked %d times with %v, want once with [SPY QQQ]", f.count("Quote"), f.quoteSyms)
	}

	got, err = fs.Quote(ctx, []string{"QQQ", "SPY"})
	if err != nil {
		t.Fatal(err)
	}
	if syms := quoteSymbols(got); !slices.Equal(syms, []string{"QQQ", "SPY"}) || f.count("Quote") != 1 {
		t.Errorf("warm quotes = %v (calls %d), want QQQ, SPY from memory", syms, f.count("Quote"))
	}

	// Only the symbols not in memory are fetched.
	fs.now = func() time.Time { return t0.Add(time.Minute) }
	got, err = fs.Quote(ctx, []string{"SPY", "VOO"})
	if err != nil {
		t.Fatal(err)
	}
	if syms := quoteSymbols(got); !slices.Equal(syms, []string{"SPY", "VOO"}) {
		t.Errorf("mixed quotes = %v, want SPY, VOO", syms)
	}
	if f.count("Quote") != 2 || !slices.Equal(f.quoteSyms[1], []string{"VOO"}) {
		t.Errorf("source asked %d times, last with %v; want twice, last [VOO]", f.count("Quote"), f.quoteSyms[len(f.quoteSyms)-1])
	}

	// Past the quote TTL everything is fetched again.
	fs.now = func() time.Time { return t0.Add(DefaultQuoteTTL + time.Second) }
	if _, err := fs.Quote(ctx, []string{"SPY", "QQQ"}); err != nil {
		t.Fatal(err)
	}
	if f.count("Quote") != 3 || !slices.Equal(f.quoteSyms[2], []string{"SPY", "QQQ"}) {
		t.Errorf("expired quotes: source asked %d times, last with %v", f.count("Quote"), f.quoteSyms[len(f.quoteSyms)-1])
	}
	if entries, err := os.ReadDir(fs.dir); err == nil && len(entries) != 0 {
		t.Errorf("quotes wrote files: %v", entries)
	}
}

func TestFundStoreQuoteOmitsUnknown(t *testing.T) {
	fs, f := newFundStore(t)
	got, err := fs.Quote(context.Background(), []string{"SPY", "UNKNOWN1"})
	if err != nil {
		t.Fatal(err)
	}
	if syms := quoteSymbols(got); !slices.Equal(syms, []string{"SPY"}) {
		t.Errorf("quotes = %v, want only SPY", syms)
	}
	if fs.LastError("UNKNOWN1") != nil {
		t.Errorf("an omitted symbol is not an error: %v", fs.LastError("UNKNOWN1"))
	}
	// The unknown symbol is asked for again next time: nothing was memoized.
	if _, err := fs.Quote(context.Background(), []string{"UNKNOWN1"}); err != nil {
		t.Fatal(err)
	}
	if f.count("Quote") != 2 {
		t.Errorf("Quote calls = %d, want 2", f.count("Quote"))
	}
}

func TestFundStoreQuoteStaleOnError(t *testing.T) {
	fs, f := newFundStore(t)
	ctx := context.Background()
	if _, err := fs.Quote(ctx, []string{"SPY"}); err != nil {
		t.Fatal(err)
	}
	f.setErr(errUpstream)
	fs.now = func() time.Time { return t0.Add(time.Hour) }

	got, err := fs.Quote(ctx, []string{"SPY", "QQQ"})
	if err != nil {
		t.Fatalf("Quote with a stale memo = %v, want the stale SPY quote", err)
	}
	if syms := quoteSymbols(got); !slices.Equal(syms, []string{"SPY"}) {
		t.Errorf("quotes = %v, want the stale SPY only", syms)
	}
	for _, sym := range []string{"SPY", "QQQ"} {
		if !errors.Is(fs.LastError(sym), errUpstream) {
			t.Errorf("LastError(%s) = %v, want upstream error", sym, fs.LastError(sym))
		}
	}

	if _, err := fs.Quote(ctx, []string{"QQQ"}); !errors.Is(err, errUpstream) {
		t.Errorf("Quote with nothing in memory = %v, want upstream error", err)
	}

	f.setErr(nil)
	if _, err := fs.Quote(ctx, []string{"SPY", "QQQ"}); err != nil {
		t.Fatal(err)
	}
	if fs.LastError("SPY") != nil || fs.LastError("QQQ") != nil {
		t.Errorf("LastError after recovery = %v / %v", fs.LastError("SPY"), fs.LastError("QQQ"))
	}
}

func TestFundStorePassThrough(t *testing.T) {
	fs, f := newFundStore(t)
	ctx := context.Background()
	for range 2 {
		hits, err := fs.Search(ctx, "spy", 5)
		if err != nil || len(hits) != 1 || hits[0].Symbol != "SPY" {
			t.Fatalf("Search = %v, %v", hits, err)
		}
		news, err := fs.News(ctx, "etf", 5)
		if err != nil || len(news) != 1 || news[0].Title != "etf" {
			t.Fatalf("News = %v, %v", news, err)
		}
	}
	if f.count("Search") != 2 || f.count("News") != 2 {
		t.Errorf("Search/News calls = %d/%d, want 2/2: pass through uncached", f.count("Search"), f.count("News"))
	}
	if _, err := os.Stat(fs.dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pass-through calls created the fund dir: %v", err)
	}
}

func TestFundStoreRejectsBadSymbols(t *testing.T) {
	fs, f := newFundStore(t)
	ctx := context.Background()
	if _, err := fs.Quote(ctx, []string{"SPY", "sp y"}); !errors.Is(err, ErrInvalidSymbol) {
		t.Errorf("Quote error = %v, want ErrInvalidSymbol", err)
	}
	for _, dk := range docKinds {
		if _, err := dk.call(ctx, fs, "../SPY"); !errors.Is(err, ErrInvalidSymbol) {
			t.Errorf("%s error = %v, want ErrInvalidSymbol", dk.kind, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("rejected symbols reached the source: %v", f.calls)
	}
}

func TestFundStoreWriteFailureStillReturnsDocument(t *testing.T) {
	dir := t.TempDir()
	// A file where the fund directory should be makes every write fail.
	if err := os.WriteFile(filepath.Join(dir, fundSubdir), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := NewFundStore(&fakeFundSource{}, dir, WithFundClock(func() time.Time { return t0 }))

	got, err := fs.FundProfile(context.Background(), "SPY")
	if err != nil || got.Name != "Fund SPY" {
		t.Fatalf("FundProfile = %+v, %v", got, err)
	}
	if fs.LastError("SPY") != nil {
		t.Errorf("LastError = %v, want nil: the fetch succeeded", fs.LastError("SPY"))
	}
	if fs.LastWriteError("SPY") == nil {
		t.Error("LastWriteError = nil, want the write failure")
	}
	if fs.LastWriteError("QQQ") != nil {
		t.Errorf("LastWriteError(QQQ) = %v, want nil", fs.LastWriteError("QQQ"))
	}
}

func TestFundStoreOptions(t *testing.T) {
	fs, f := newFundStore(t, WithFundTTL(time.Minute), WithQuoteTTL(time.Second))
	ctx := context.Background()
	if _, err := fs.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Quote(ctx, []string{"SPY"}); err != nil {
		t.Fatal(err)
	}
	fs.now = func() time.Time { return t0.Add(2 * time.Second) }
	if _, err := fs.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Quote(ctx, []string{"SPY"}); err != nil {
		t.Fatal(err)
	}
	if f.count("FundProfile") != 1 || f.count("Quote") != 2 {
		t.Errorf("profile/quote calls = %d/%d, want 1/2 with a 1 minute fund TTL and 1 second quote TTL", f.count("FundProfile"), f.count("Quote"))
	}

	defaults := NewFundStore(f, t.TempDir(), WithFundClock(nil), WithFundTTL(0), WithQuoteTTL(-1))
	if defaults.ttl != DefaultFundTTL || defaults.quoteTTL != DefaultQuoteTTL || defaults.now == nil {
		t.Errorf("non-positive settings did not fall back to the defaults: ttl=%v quote=%v", defaults.ttl, defaults.quoteTTL)
	}
}

func TestFundFileParts(t *testing.T) {
	tests := []struct {
		name     string
		sym, kin string
		ok       bool
	}{
		{"SPY.profile.json", "SPY", "profile", true},
		{"BF.B.holdings.json", "BF.B", "holdings", true},
		{"KRW=X.performance.json", "KRW=X", "performance", true},
		{"SPY.quote.json", "", "", false},
		{"SPY.json", "", "", false},
		{"spy.profile.json", "", "", false},
		{"SPY.profile.json.123.tmp", "", "", false},
		{"readme.md", "", "", false},
	}
	for _, tt := range tests {
		sym, kind, ok := fundFileParts(tt.name)
		if sym != tt.sym || kind != tt.kin || ok != tt.ok {
			t.Errorf("fundFileParts(%q) = %q, %q, %v; want %q, %q, %v", tt.name, sym, kind, ok, tt.sym, tt.kin, tt.ok)
		}
	}
}

func quoteSymbols(quotes []market.Quote) []string {
	out := make([]string, 0, len(quotes))
	for _, q := range quotes {
		out = append(out, q.Symbol)
	}
	return out
}
