package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// hasWarning reports whether one of st's warnings contains substr.
func hasWarning(st *Status, substr string) bool {
	return slices.ContainsFunc(st.Warnings, func(w string) bool { return strings.Contains(w, substr) })
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestStatusEmptyCache(t *testing.T) {
	s := New(&fakeSource{}, filepath.Join(t.TempDir(), "never-created"), time.Hour)
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Dir != s.dir || st.Files != 0 || st.FundFiles != 0 || st.TotalBytes != 0 || len(st.Symbols) != 0 || len(st.Warnings) != 0 {
		t.Errorf("Status of a missing dir = %+v, want empty", st)
	}
	if !st.OldestFetch.IsZero() || !st.NewestFetch.IsZero() {
		t.Errorf("fetch times = %v / %v, want zero", st.OldestFetch, st.NewestFetch)
	}
}

func TestStatusCountsFiles(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	fund := NewFundStore(&fakeFundSource{}, s.dir, WithFundClock(s.now))
	ctx := context.Background()
	for _, sym := range []string{"SPY", "QQQ"} {
		if _, err := s.Series(ctx, sym); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fund.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if _, err := fund.Holdings(ctx, "QQQ"); err != nil {
		t.Fatal(err)
	}
	// Files that are not ours are ignored, symlinks included.
	for _, name := range []string{"notes.txt", "lower.json", "SPY.123.tmp"} {
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(s.dir, "SPY.json"), filepath.Join(s.dir, "VOO.json")); err != nil {
			t.Fatal(err)
		}
	}

	st, err := s.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Files != 4 || st.FundFiles != 2 {
		t.Errorf("Files = %d, FundFiles = %d; want 4 and 2", st.Files, st.FundFiles)
	}
	wantBytes := fileSize(t, s.path("SPY")) + fileSize(t, s.path("QQQ")) +
		fileSize(t, fundPath(s.dir, "SPY", kindProfile)) + fileSize(t, fundPath(s.dir, "QQQ", kindHoldings))
	if st.TotalBytes != wantBytes {
		t.Errorf("TotalBytes = %d, want %d", st.TotalBytes, wantBytes)
	}
	if len(st.Symbols) != 2 || st.Symbols[0].Symbol != "QQQ" || st.Symbols[1].Symbol != "SPY" {
		t.Fatalf("Symbols = %+v, want QQQ then SPY", st.Symbols)
	}
	for _, ss := range st.Symbols {
		want := SymbolStatus{
			Symbol: ss.Symbol, Bars: 3,
			FirstDate: date("2026-01-02"), LastDate: date("2026-01-06"),
			FetchedAt: t0, FullFetchedAt: t0,
			Bytes: fileSize(t, s.path(ss.Symbol)), Version: 2,
		}
		if ss != want {
			t.Errorf("%s status = %+v, want %+v", ss.Symbol, ss, want)
		}
	}
	if !st.OldestFetch.Equal(t0) || !st.NewestFetch.Equal(t0) {
		t.Errorf("fetch times = %v / %v, want %v", st.OldestFetch, st.NewestFetch, t0)
	}
	if len(st.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", st.Warnings)
	}
}

func TestStatusWarnings(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, s *Store, f *fakeSource)
		want  string
		wantN int // number of warnings; 0 means 1
	}{
		{
			name:  "too many files",
			setup: func(_ *testing.T, s *Store, _ *fakeSource) { s.limits.files = 1 },
			want:  "2 cache files (more than 1)",
		},
		{
			name:  "too many bytes",
			setup: func(_ *testing.T, s *Store, _ *fakeSource) { s.limits.totalBytes = 100 },
			want:  "cache holds",
		},
		{
			name:  "one file too large",
			setup: func(_ *testing.T, s *Store, _ *fakeSource) { s.limits.fileBytes = 100 },
			want:  "QQQ.json is",
			wantN: 2, // both files are the same size
		},
		{
			name:  "file not fetched for 90 days",
			setup: func(_ *testing.T, s *Store, _ *fakeSource) { s.now = func() time.Time { return t0.AddDate(0, 0, 91) } },
			want:  "SPY.json was last fetched 91 days ago (more than 90)",
			wantN: 2, // both files are the same age
		},
		{
			name: "last fetch failed",
			setup: func(t *testing.T, s *Store, f *fakeSource) {
				f.failFor = map[string]error{"BAD": errUpstream}
				if _, err := s.Series(context.Background(), "BAD"); err == nil {
					t.Fatal("BAD fetched without error")
				}
			},
			want: "BAD: last fetch failed: cache: fetch BAD: upstream down",
		},
		{
			name:  "last write failed",
			setup: func(_ *testing.T, s *Store, _ *fakeSource) { s.lastWriteErr.set("SPY", errors.New("disk full")) },
			want:  "SPY: last write failed: disk full",
		},
		{
			name: "unreadable file",
			setup: func(t *testing.T, s *Store, _ *fakeSource) {
				if err := os.WriteFile(s.path("SPY"), []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "SPY.json: unreadable, will be refetched",
		},
		{
			name: "unreadable fund file",
			setup: func(t *testing.T, s *Store, _ *fakeSource) {
				path := fundPath(s.dir, "SPY", kindProfile)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("nope"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "fund/SPY.profile.json: unreadable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, f := newStore(t, time.Hour)
			for _, sym := range []string{"SPY", "QQQ"} {
				if _, err := s.Series(context.Background(), sym); err != nil {
					t.Fatal(err)
				}
			}
			tt.setup(t, s, f)
			st, err := s.Status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if !hasWarning(st, tt.want) {
				t.Errorf("warnings = %q, want one containing %q", st.Warnings, tt.want)
			}
			if wantN := max(tt.wantN, 1); len(st.Warnings) != wantN {
				t.Errorf("got %d warnings, want exactly %d: %q", len(st.Warnings), wantN, st.Warnings)
			}
		})
	}
}

func TestStatusUnreadableFileIsListed(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("SPY"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Symbols) != 1 || st.Symbols[0].Version != 0 || st.Symbols[0].Bytes != 7 || st.Files != 1 {
		t.Errorf("Status = %+v, want the broken file listed with version 0", st)
	}
	if !st.OldestFetch.IsZero() {
		t.Errorf("OldestFetch = %v, want zero: the broken file has no fetch time", st.OldestFetch)
	}
}

func TestStatusReadsV1File(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	series := cachedHistory()
	v1, err := json.Marshal(entryV1{FetchedAt: t0.Add(-time.Hour), Series: series})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("SPY"), v1, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Symbols) != 1 {
		t.Fatalf("Symbols = %+v", st.Symbols)
	}
	got := st.Symbols[0]
	if got.Version != 1 || got.Bars != 5 || !got.FirstDate.Equal(date("2026-09-28")) || !got.LastDate.Equal(date("2026-10-02")) {
		t.Errorf("v1 status = %+v, want version 1 with 5 bars from 09-28 to 10-02", got)
	}
	if !got.FetchedAt.Equal(t0.Add(-time.Hour)) || !got.FullFetchedAt.IsZero() {
		t.Errorf("fetched_at = %v, full_fetched_at = %v", got.FetchedAt, got.FullFetchedAt)
	}
}

func TestStatusWarnsAboveDefaultFileLimit(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	tiny := &market.Series{Meta: market.Meta{Symbol: "X"}, Bars: []market.Bar{{Date: date("2026-01-02"), Close: 1, AdjClose: 1}}}
	for i := range MaxFiles + 1 {
		sym := fmt.Sprintf("S%03d", i)
		tiny.Meta.Symbol = sym
		if err := s.writeEntry(sym, &entry{Version: formatVersion, FetchedAt: t0, FullFetchedAt: t0, Series: tiny}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Status over %d files took %v, want well under a second", MaxFiles+1, elapsed)
	}
	if st.Files != MaxFiles+1 || !hasWarning(st, fmt.Sprintf("%d cache files (more than %d)", MaxFiles+1, MaxFiles)) {
		t.Errorf("Files = %d, warnings = %q", st.Files, st.Warnings)
	}
}

func TestStatusHonorsContext(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Status with a cancelled context = %v, want context.Canceled", err)
	}
}

// populate fetches SPY and QQQ prices and SPY fund documents into s's dir.
func populate(t *testing.T, s *Store) {
	t.Helper()
	fund := NewFundStore(&fakeFundSource{}, s.dir, WithFundClock(s.now))
	ctx := context.Background()
	for _, sym := range []string{"SPY", "QQQ"} {
		if _, err := s.Series(ctx, sym); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fund.FundProfile(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
	if _, err := fund.Performance(ctx, "SPY"); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestClear(t *testing.T) {
	s, f := newStore(t, time.Hour)
	populate(t, s)
	f.failFor = map[string]error{"BAD": errUpstream}
	_, _ = s.Series(context.Background(), "BAD")
	if s.LastError("BAD") == nil {
		t.Fatal("BAD has no LastError to forget")
	}

	removed, err := s.Clear([]string{"spy", "NOPE", "bad", "SPY"})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if !slices.Equal(removed, []string{"SPY"}) {
		t.Errorf("removed = %v, want [SPY]: NOPE and BAD have no files, SPY counts once", removed)
	}
	for _, p := range []string{s.path("SPY"), fundPath(s.dir, "SPY", kindProfile), fundPath(s.dir, "SPY", kindPerformance)} {
		if exists(p) {
			t.Errorf("%s still exists", p)
		}
	}
	if !exists(s.path("QQQ")) {
		t.Error("QQQ.json was removed")
	}
	if s.LastError("BAD") != nil {
		t.Errorf("LastError(BAD) = %v after Clear, want nil", s.LastError("BAD"))
	}
	if _, err := s.Series(context.Background(), "SPY"); err != nil || f.count() != 4 {
		t.Errorf("SPY after Clear: err=%v calls=%d, want a fresh fetch (4 calls)", err, f.count())
	}
}

func TestClearRejectsBadSymbols(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	populate(t, s)
	for _, bad := range []string{"", "../SPY", "SPY/..", "sp y", "fund/SPY"} {
		removed, err := s.Clear([]string{"SPY", bad})
		if !errors.Is(err, ErrInvalidSymbol) || removed != nil {
			t.Errorf("Clear with %q = %v, %v; want ErrInvalidSymbol and nothing removed", bad, removed, err)
		}
	}
	if !exists(s.path("SPY")) {
		t.Error("SPY.json was removed although the call was rejected")
	}
}

func TestClearSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	s, _ := newStore(t, time.Hour)
	if err := os.MkdirAll(filepath.Join(s.dir, fundSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "precious.json")
	if err := os.WriteFile(outside, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := []string{s.path("SPY"), fundPath(s.dir, "SPY", kindProfile)}
	for _, link := range links {
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.Clear([]string{"SPY"})
	if err != nil || len(removed) != 0 {
		t.Errorf("Clear = %v, %v; want nothing removed", removed, err)
	}
	n, err := s.ClearAll()
	if err != nil || n != 0 {
		t.Errorf("ClearAll = %d, %v; want nothing removed", n, err)
	}
	if !exists(outside) {
		t.Fatal("the symlink target was deleted")
	}
	for _, link := range links {
		if !exists(link) {
			t.Errorf("symlink %s was removed", link)
		}
	}
}

func TestClearAll(t *testing.T) {
	s, f := newStore(t, time.Hour)
	populate(t, s)
	f.failFor = map[string]error{"BAD": errUpstream}
	_, _ = s.Series(context.Background(), "BAD")
	strays := []string{
		filepath.Join(s.dir, "notes.txt"),
		filepath.Join(s.dir, "lower.json"),
		filepath.Join(s.dir, fundSubdir, "readme.md"),
		filepath.Join(s.dir, fundSubdir, "SPY.unknown.json"),
	}
	temps := []string{
		filepath.Join(s.dir, "SPY.json.999.tmp"),
		filepath.Join(s.dir, fundSubdir, "SPY.profile.json.999.tmp"),
	}
	for _, p := range append(strays, temps...) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Temporary files count as leftovers only once they are older than
	// tempFileMaxAge on the Store's clock.
	for _, p := range temps {
		past := s.now().Add(-time.Hour)
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.ClearAll()
	if err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if n != 6 {
		t.Errorf("removed %d files, want 6 (2 price, 2 fund, 2 temp)", n)
	}
	for _, p := range strays {
		if !exists(p) {
			t.Errorf("stray %s was removed", p)
		}
	}
	for _, p := range append(temps, s.path("SPY"), s.path("QQQ"), fundPath(s.dir, "SPY", kindProfile)) {
		if exists(p) {
			t.Errorf("%s still exists", p)
		}
	}
	if s.LastError("BAD") != nil {
		t.Errorf("LastError(BAD) = %v after ClearAll", s.LastError("BAD"))
	}
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 0 || len(st.Warnings) != 0 {
		t.Errorf("Status after ClearAll = %+v, want empty", st)
	}

	// A second ClearAll on an empty cache is a no-op, as is one on a
	// directory that never existed.
	if n, err := s.ClearAll(); err != nil || n != 0 {
		t.Errorf("second ClearAll = %d, %v", n, err)
	}
	gone := New(f, filepath.Join(t.TempDir(), "missing"), time.Hour)
	if n, err := gone.ClearAll(); err != nil || n != 0 {
		t.Errorf("ClearAll on a missing dir = %d, %v", n, err)
	}
}

func TestFmtBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{10 << 20, "10.0 MiB"},
		{3 << 30, "3.0 GiB"},
	}
	for _, tt := range tests {
		if got := fmtBytes(tt.n); got != tt.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// BenchmarkStatus measures Status over MaxFiles price files of a long
// history each, the case the header-only read exists for.
func BenchmarkStatus(b *testing.B) {
	s := New(&fakeSource{}, b.TempDir(), time.Hour)
	bars := make([]market.Bar, 0, 10000)
	day := date("1986-01-02")
	for len(bars) < cap(bars) {
		px := 100 + float64(len(bars))*0.01
		bars = append(bars, market.Bar{Date: day, Open: px, High: px, Low: px, Volume: 1000, Close: px, AdjClose: px})
		day = day.AddDate(0, 0, 1)
	}
	for i := range MaxFiles {
		sym := fmt.Sprintf("S%03d", i)
		e := &entry{Version: formatVersion, FetchedAt: t0, FullFetchedAt: t0, Series: &market.Series{Meta: market.Meta{Symbol: sym}, Bars: bars}}
		if err := s.writeEntry(sym, e); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		st, err := s.Status(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if st.Files != MaxFiles || st.Symbols[0].Bars != len(bars) {
			b.Fatalf("Files = %d, Bars = %d", st.Files, st.Symbols[0].Bars)
		}
	}
}

// TestClearAllLeavesOtherProgramsFiles: in a cache directory shared with
// other programs, ClearAll removes only files with this package's names
// and header, and temporary files only once a write in progress can no
// longer own them.
func TestClearAllLeavesOtherProgramsFiles(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	populate(t, s)
	old, young := s.now().Add(-time.Hour), s.now()
	keep := map[string]time.Time{
		filepath.Join(s.dir, "2024-01-01.json"):            {}, // a valid symbol name, not a cache file
		filepath.Join(s.dir, "1.json"):                     {},
		filepath.Join(s.dir, "editor-swap.tmp"):            old,
		filepath.Join(s.dir, "QQQ.json.123.tmp"):           young, // a write in progress
		filepath.Join(s.dir, fundSubdir, "notes.json.tmp"): old,
	}
	for p, at := range keep {
		writeTestFile(t, p, `{"user":"data"}`)
		if !at.IsZero() {
			setModTime(t, p, at)
		}
	}
	stale := filepath.Join(s.dir, "SPY.json.456.tmp")
	writeTestFile(t, stale, "x")
	setModTime(t, stale, old)

	n, err := s.ClearAll()
	if err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if n != 5 {
		t.Errorf("removed %d files, want 5 (2 price, 2 fund, 1 stale temp)", n)
	}
	for p := range keep {
		if !exists(p) {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
	for _, p := range []string{stale, s.path("SPY"), s.path("QQQ"), fundPath(s.dir, "SPY", kindProfile)} {
		if exists(p) {
			t.Errorf("%s still exists", filepath.Base(p))
		}
	}
}

func TestHasCacheHeader(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	populate(t, s)
	v1, err := json.Marshal(entryV1{FetchedAt: t0, Series: cachedHistory()})
	if err != nil {
		t.Fatal(err)
	}
	v1Path := filepath.Join(t.TempDir(), "V1.json")
	writeTestFile(t, v1Path, string(v1))
	for _, p := range []string{s.path("SPY"), fundPath(s.dir, "SPY", kindProfile), v1Path} {
		if !hasCacheHeader(p) {
			t.Errorf("hasCacheHeader(%s) = false for a file this package wrote", filepath.Base(p))
		}
	}
	other := filepath.Join(t.TempDir(), "other.json")
	writeTestFile(t, other, `{"version":3}`)
	if hasCacheHeader(other) || hasCacheHeader(filepath.Join(t.TempDir(), "missing.json")) {
		t.Error("hasCacheHeader accepted a foreign or missing file")
	}
}

// TestStatusFlagsTruncatedFile: Status stops reading at "series", but a
// file cut short there is still reported, as readEntry would reject it.
func TestStatusFlagsTruncatedFile(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	if _, err := s.Series(context.Background(), "SPY"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.path("SPY"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, s.path("SPY"), string(data[:len(data)/2]))
	if _, ok := s.readEntry("SPY"); ok {
		t.Fatal("readEntry accepted the truncated file")
	}
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(st, "SPY.json: unreadable") || st.Symbols[0].Version != 0 {
		t.Errorf("Status = %+v, want SPY flagged unreadable", st)
	}
}

func TestStatusSortsSymbols(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	for _, sym := range []string{"BRK", "BRK-B", "BF", "BF.B"} {
		e := &entry{Version: formatVersion, FetchedAt: t0, FullFetchedAt: t0, Series: sampleSeries(sym)}
		if err := s.writeEntry(sym, e); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ss := range st.Symbols {
		got = append(got, ss.Symbol)
	}
	if want := []string{"BF", "BF.B", "BRK", "BRK-B"}; !slices.Equal(got, want) {
		t.Errorf("symbols = %v, want %v", got, want)
	}
}

// TestStatusErrorWarningsAreConsistent: a fetch that clears a symbol's
// error while Status runs must not produce "last fetch failed: <nil>".
func TestStatusErrorWarningsAreConsistent(t *testing.T) {
	s, _ := newStore(t, time.Hour)
	stop := make(chan struct{})
	toggled := make(chan struct{})
	go func() {
		defer close(toggled)
		for {
			select {
			case <-stop:
				return
			default:
				s.lastErr.set("SPY", errUpstream)
				s.lastErr.set("SPY", nil)
			}
		}
	}()
	defer func() {
		close(stop)
		<-toggled
	}()
	for range 2000 {
		st, err := s.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if hasWarning(st, "<nil>") {
			t.Fatalf("warnings = %q", st.Warnings)
		}
	}
}

// TestSymlinkedFundDirIsNotFollowed: Status, Clear and ClearAll never
// look through a fund directory that is a symlink.
func TestSymlinkedFundDirIsNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	s, _ := newStore(t, time.Hour)
	elsewhere := t.TempDir()
	draft := filepath.Join(elsewhere, "ABC.profile.json.1.tmp")
	doc := filepath.Join(elsewhere, "ABC.profile.json")
	writeTestFile(t, draft, "x")
	setModTime(t, draft, s.now().Add(-time.Hour))
	writeTestFile(t, doc, `{"version":1,"fetched_at":"2026-10-08T09:00:00Z","data":{}}`)
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(s.dir, fundSubdir)); err != nil {
		t.Fatal(err)
	}

	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.FundFiles != 0 || !hasWarning(st, "fund: not a real directory") {
		t.Errorf("Status = %+v, want no fund files and a warning", st)
	}
	if removed, err := s.Clear([]string{"ABC"}); err != nil || len(removed) != 0 {
		t.Errorf("Clear = %v, %v; want nothing removed", removed, err)
	}
	if n, err := s.ClearAll(); err != nil || n != 0 {
		t.Errorf("ClearAll = %d, %v; want nothing removed", n, err)
	}
	if !exists(draft) || !exists(doc) {
		t.Errorf("files behind the symlink were removed: temp %v, doc %v", exists(draft), exists(doc))
	}
}
