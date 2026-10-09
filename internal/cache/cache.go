// Package cache decorates a market.Source with an on-disk JSON cache.
//
// One file per symbol, <dir>/<SYMBOL>.json, holds the fetch time and the
// series. Reads inside the TTL never touch the wrapped source, concurrent
// misses for the same symbol share a single upstream call that outlives
// any one caller's context, and when the source fails but a stale file
// exists the stale series is served while the failure is kept for
// Store.LastError. A series that could not be written to disk is still
// served and the problem kept for Store.LastWriteError. Nothing is logged.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// ErrInvalidSymbol is returned for symbols that cannot name a cache file.
var ErrInvalidSymbol = errors.New("cache: invalid symbol")

// fetchTimeout bounds a shared upstream fetch, which runs detached from
// the context of the caller that started it so that one caller's
// cancellation cannot fail the others waiting on the same symbol.
const fetchTimeout = 90 * time.Second

// tempFileMaxAge is how old a leftover *.tmp file must be before New sweeps
// it; younger ones may belong to a write in progress.
const tempFileMaxAge = 10 * time.Minute

// symbolPattern limits symbols to characters that are safe in file names.
// Yahoo symbols use letters, digits and ".", "=", "^" and "-" (BRK-B,
// KRW=X, ^GSPC, BF.B).
var symbolPattern = regexp.MustCompile(`^[A-Z0-9.=^-]+$`)

// entry is the on-disk layout of one cached symbol.
type entry struct {
	FetchedAt time.Time
	Series    *market.Series
}

// call is one in-flight upstream fetch that concurrent callers wait on.
type call struct {
	done   chan struct{}
	series *market.Series
	err    error
}

// wait blocks until the fetch finishes or ctx is done.
func (c *call) wait(ctx context.Context) (*market.Series, error) {
	select {
	case <-c.done:
		return c.series, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Store is a market.Source that caches another Source on disk. It is safe
// for concurrent use. Construct it with New.
type Store struct {
	next market.Source
	dir  string
	ttl  time.Duration
	now  func() time.Time // injectable clock for tests

	mu           sync.Mutex
	inflight     map[string]*call
	lastErr      map[string]error // most recent upstream failure per symbol
	lastWriteErr map[string]error // most recent cache write failure per symbol
}

// New returns a Store that serves next from dir, treating a cached file
// younger than ttl as fresh. dir is created on the first write. Temporary
// files left behind by an interrupted write (older than ten minutes) are
// removed.
func New(next market.Source, dir string, ttl time.Duration) *Store {
	s := &Store{
		next:         next,
		dir:          dir,
		ttl:          ttl,
		now:          time.Now,
		inflight:     make(map[string]*call),
		lastErr:      make(map[string]error),
		lastWriteErr: make(map[string]error),
	}
	s.sweepTempFiles()
	return s
}

// sweepTempFiles deletes stale *.tmp files in the cache directory. Errors
// are ignored: the sweep is housekeeping, not a precondition.
func (s *Store) sweepTempFiles() {
	matches, err := filepath.Glob(filepath.Join(s.dir, "*.tmp"))
	if err != nil {
		return
	}
	for _, path := range matches {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if s.now().Sub(info.ModTime()) > tempFileMaxAge {
			_ = os.Remove(path)
		}
	}
}

// Series returns the cached series when its file is younger than the TTL
// and otherwise fetches from the wrapped source. It implements
// market.Source. The symbol is trimmed and upper-cased; one that is not
// safe as a file name returns an error wrapping ErrInvalidSymbol.
func (s *Store) Series(ctx context.Context, symbol string) (*market.Series, error) {
	sym, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	if e, ok := s.readEntry(sym); ok && s.isFresh(e) {
		return e.Series, nil
	}
	return s.fetch(ctx, sym)
}

// isFresh reports whether e was fetched less than the TTL ago. A fetch
// time in the future (clock skew, an edited or restored file) counts as
// stale rather than fresh forever.
func (s *Store) isFresh(e *entry) bool {
	age := s.now().Sub(e.FetchedAt)
	return age >= 0 && age < s.ttl
}

// Prefetch fetches every symbol through this Store with at most
// concurrency fetches in flight (below 1 is treated as 1) and returns the
// series that succeeded together with the error of each symbol that
// failed. Symbols already fresh on disk are served from the file. Once
// ctx is done no new fetch starts and the remaining symbols are reported
// with ctx.Err(). Callers should use the returned series directly rather
// than calling Series again: a series that could not be written to disk
// would otherwise be fetched a second time.
func (s *Store) Prefetch(ctx context.Context, symbols []string, concurrency int) (map[string]*market.Series, map[string]error) {
	concurrency = max(concurrency, 1)
	var (
		wg     sync.WaitGroup
		sem    = make(chan struct{}, concurrency)
		mu     sync.Mutex
		series = make(map[string]*market.Series, len(symbols))
		errs   = make(map[string]error)
	)
	for _, sym := range symbols {
		if err := ctx.Err(); err != nil {
			mu.Lock()
			errs[sym] = err
			mu.Unlock()
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			errs[sym] = ctx.Err()
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := s.Series(ctx, sym)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[sym] = err
				return
			}
			series[sym] = got
		}()
	}
	wg.Wait()
	return series, errs
}

// Refresh fetches symbol from the wrapped source regardless of the TTL
// and rewrites its cache file. It shares in-flight fetches and the
// stale-on-error behavior with Series.
func (s *Store) Refresh(ctx context.Context, symbol string) (*market.Series, error) {
	sym, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return s.fetch(ctx, sym)
}

// LastError returns the error of the most recent upstream fetch of symbol
// through this Store, or nil when that fetch succeeded or the symbol was
// never fetched. A non-nil value after a successful Series call means the
// series came from a stale file. Write problems are reported separately
// by LastWriteError.
func (s *Store) LastError(symbol string) error {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr[sym]
}

// LastWriteError returns the error of the most recent attempt to write
// symbol's cache file, or nil when it succeeded or was never attempted. A
// non-nil value means the data served is current but was not cached, so
// the next call will fetch again.
func (s *Store) LastWriteError(symbol string) error {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastWriteErr[sym]
}

// fetch runs one upstream fetch per symbol at a time. Callers that arrive
// while a fetch is in flight wait for its result instead of starting
// another, so a burst of cold reads costs a single request. The upstream
// call runs in its own goroutine under a context detached from every
// caller (context.WithoutCancel plus fetchTimeout), so a caller that gives
// up does not fail the others; every caller, the first included, returns
// as soon as its own ctx is done.
func (s *Store) fetch(ctx context.Context, sym string) (*market.Series, error) {
	s.mu.Lock()
	if c, ok := s.inflight[sym]; ok {
		s.mu.Unlock()
		return c.wait(ctx)
	}
	c := &call{done: make(chan struct{})}
	s.inflight[sym] = c
	s.mu.Unlock()

	go func() {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()
		c.series, c.err = s.fetchAndStore(fetchCtx, sym)

		s.mu.Lock()
		delete(s.inflight, sym)
		s.mu.Unlock()
		close(c.done)
	}()
	return c.wait(ctx)
}

// fetchAndStore calls the wrapped source and persists the result. When the
// source fails and a stale file exists, the stale series is returned with
// a nil error and the failure is recorded for LastError. A series that
// could not be written is still returned, with the problem recorded for
// LastWriteError.
func (s *Store) fetchAndStore(ctx context.Context, sym string) (*market.Series, error) {
	series, err := s.next.Series(ctx, sym)
	if err != nil {
		err = fmt.Errorf("cache: fetch %s: %w", sym, err)
		s.record(s.lastErr, sym, err)
		if stale, ok := s.readEntry(sym); ok {
			return stale.Series, nil
		}
		return nil, err
	}
	s.record(s.lastErr, sym, nil)
	s.record(s.lastWriteErr, sym, s.writeEntry(sym, series))
	return series, nil
}

// record stores err for sym in m, or clears the entry when err is nil.
func (s *Store) record(m map[string]error, sym string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(m, sym)
		return
	}
	m[sym] = err
}

// path returns the cache file for sym.
func (s *Store) path(sym string) string {
	return filepath.Join(s.dir, sym+".json")
}

// readEntry loads the cache file for sym. ok is false when the file is
// missing, unreadable or does not hold a valid series; all three count as
// a cache miss.
func (s *Store) readEntry(sym string) (e *entry, ok bool) {
	data, err := os.ReadFile(s.path(sym))
	if err != nil {
		return nil, false
	}
	var loaded entry
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, false
	}
	if err := loaded.Series.Validate(); err != nil {
		return nil, false
	}
	return &loaded, true
}

// writeEntry persists series atomically: it writes a temporary file in the
// cache directory and renames it over the final path, so a reader never
// sees a partial file and a failed write leaves nothing behind.
func (s *Store) writeEntry(sym string, series *market.Series) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("cache: create %s: %w", s.dir, err)
	}
	tmp, err := os.CreateTemp(s.dir, sym+".*.tmp")
	if err != nil {
		return fmt.Errorf("cache: create temp file: %w", err)
	}
	e := entry{FetchedAt: s.now(), Series: series}
	if err := writeAndRename(tmp, s.path(sym), e); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// writeAndRename encodes e into tmp, closes it and moves it to dst.
func writeAndRename(tmp *os.File, dst string, e entry) error {
	if err := json.NewEncoder(tmp).Encode(e); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cache: encode %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cache: close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("cache: rename to %s: %w", dst, err)
	}
	return nil
}

// normalizeSymbol trims and upper-cases symbol and rejects anything that
// is not safe as a file name.
func normalizeSymbol(symbol string) (string, error) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if !symbolPattern.MatchString(sym) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSymbol, symbol)
	}
	return sym, nil
}
