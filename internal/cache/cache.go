// Package cache decorates a market.Source with an on-disk JSON cache of
// price history and a market.FundSource with a smaller one for fund data.
//
// Price history lives in one file per symbol, <dir>/<SYMBOL>.json, holding
// the series plus when it was last brought up to date and when it was last
// fetched in full. Reads inside the TTL never touch the wrapped source.
// Past the TTL the Store asks a market.RangeSource only for the days since
// the last cached bar and appends them; mergeTail documents when that is
// unsafe and the whole history is fetched again instead. Concurrent misses
// for the same symbol share a single upstream call that outlives any one
// caller's context, and when the source fails but a file exists the stale
// series is served while the failure is kept for Store.LastError. A series
// that could not be written to disk is still served and the problem kept
// for Store.LastWriteError. Status reports what is on disk and warns when
// the cache grows past its thresholds; Clear and ClearAll remove files.
// Nothing is logged.
package cache

import (
	"context"
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

// Defaults for New. Options change the last two.
const (
	// DefaultTTL is how long a price file is served without asking the
	// source for anything. New uses it when ttl is not positive.
	DefaultTTL = 6 * time.Hour
	// DefaultFullRefetchInterval is how long top-ups may extend a history
	// before the next stale read fetches the whole history again, which
	// picks up corrections the top-up rules cannot see.
	DefaultFullRefetchInterval = 30 * 24 * time.Hour
	// DefaultOverlap is how far before the last cached bar a top-up request
	// starts, so the cached tail is checked against the provider before
	// new bars are appended.
	DefaultOverlap = 7 * 24 * time.Hour
)

// fetchTimeout bounds a shared upstream fetch, which runs detached from
// the context of the caller that started it so that one caller's
// cancellation cannot fail the others waiting on the same symbol.
const fetchTimeout = 90 * time.Second

// symbolPattern limits symbols to characters that are safe in file names.
// Yahoo symbols use letters, digits and ".", "=", "^" and "-" (BRK-B,
// KRW=X, ^GSPC, BF.B).
var symbolPattern = regexp.MustCompile(`^[A-Z0-9.=^-]+$`)

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

// Store is a market.Source that caches another Source on disk and tops
// the cached history up instead of refetching it. It is safe for
// concurrent use. Construct it with New.
type Store struct {
	next         market.Source
	ranged       market.RangeSource // next, when it can fetch date ranges; nil otherwise
	dir          string
	ttl          time.Duration
	fullInterval time.Duration
	overlap      time.Duration
	limits       limits
	now          func() time.Time

	mu       sync.Mutex
	inflight map[string]*call

	lastErr      errTable // most recent upstream failure per symbol
	lastWriteErr errTable // most recent cache write failure per symbol
}

// Option configures a Store; pass Options to New.
type Option func(*Store)

// WithClock makes the Store read the current time from now instead of
// time.Now. Tests use it to age files without waiting.
func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// WithFullRefetchInterval sets how old the last full fetch may be before a
// stale read fetches the whole history instead of topping it up. A value
// that is not positive keeps DefaultFullRefetchInterval.
func WithFullRefetchInterval(d time.Duration) Option {
	return func(s *Store) {
		if d > 0 {
			s.fullInterval = d
		}
	}
}

// WithOverlap sets how far before the last cached bar a top-up request
// starts. A value that is not positive keeps DefaultOverlap.
func WithOverlap(d time.Duration) Option {
	return func(s *Store) {
		if d > 0 {
			s.overlap = d
		}
	}
}

// New returns a Store that serves next from dir, treating a cached file
// younger than ttl as fresh (DefaultTTL when ttl is not positive). dir is
// created on the first write. Temporary files left behind by an
// interrupted write (older than ten minutes) are removed. When next also
// implements market.RangeSource, stale files are topped up rather than
// refetched.
func New(next market.Source, dir string, ttl time.Duration, opts ...Option) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	s := &Store{
		next:         next,
		dir:          dir,
		ttl:          ttl,
		fullInterval: DefaultFullRefetchInterval,
		overlap:      DefaultOverlap,
		limits:       defaultLimits,
		now:          time.Now,
		inflight:     make(map[string]*call),
	}
	if rs, ok := next.(market.RangeSource); ok {
		s.ranged = rs
	}
	for _, opt := range opts {
		opt(s)
	}
	sweepTempFiles(s.dir, s.now())
	return s
}

// Series returns the cached series when its file is younger than the TTL
// and otherwise brings it up to date through the wrapped source: a top-up
// of the days since the last cached bar when the source is a
// market.RangeSource and the history was fetched in full recently enough,
// a full fetch otherwise (see mergeTail for the rules). It implements
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
	return s.fetch(ctx, sym, false)
}

// isFresh reports whether e is in the current format and was brought up
// to date less than the TTL ago. A fetch time in the future (clock skew,
// an edited or restored file) counts as stale rather than fresh forever.
func (s *Store) isFresh(e *entry) bool {
	return e.Version == formatVersion && isFresh(s.now(), e.FetchedAt, s.ttl)
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

// Refresh fetches the whole history of symbol from the wrapped source
// regardless of the TTL and rewrites its cache file. It shares the
// stale-on-error behavior with Series, and a Refresh that arrives while a
// fetch of the symbol is already in flight shares that fetch, whether it
// is a top-up or a full one.
func (s *Store) Refresh(ctx context.Context, symbol string) (*market.Series, error) {
	sym, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return s.fetch(ctx, sym, true)
}

// LastError returns the error of the most recent upstream fetch of symbol
// through this Store, or nil when that fetch succeeded or the symbol was
// never fetched. A non-nil value after a successful Series call means the
// series came from a stale file. Write problems are reported separately
// by LastWriteError.
func (s *Store) LastError(symbol string) error {
	return s.lastErr.get(strings.ToUpper(strings.TrimSpace(symbol)))
}

// LastWriteError returns the error of the most recent attempt to write
// symbol's cache file, or nil when it succeeded or was never attempted. A
// non-nil value means the data served is current but was not cached, so
// the next call will fetch again.
func (s *Store) LastWriteError(symbol string) error {
	return s.lastWriteErr.get(strings.ToUpper(strings.TrimSpace(symbol)))
}

// fetch runs one upstream fetch per symbol at a time. Callers that arrive
// while a fetch is in flight wait for its result instead of starting
// another, so a burst of cold reads costs a single request. The upstream
// call runs in its own goroutine under a context detached from every
// caller (context.WithoutCancel plus fetchTimeout), so a caller that gives
// up does not fail the others; every caller, the first included, returns
// as soon as its own ctx is done. full forces a full fetch; otherwise the
// history is topped up when it can be.
func (s *Store) fetch(ctx context.Context, sym string, full bool) (*market.Series, error) {
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
		c.series, c.err = s.fetchAndStore(fetchCtx, sym, full)

		s.mu.Lock()
		delete(s.inflight, sym)
		s.mu.Unlock()
		close(c.done)
	}()
	return c.wait(ctx)
}

// fetchAndStore brings sym up to date through the wrapped source and
// persists the result. When the source fails and a file exists, the stale
// series is returned with a nil error and the failure is recorded for
// LastError. A series that could not be written is still returned, with
// the problem recorded for LastWriteError.
func (s *Store) fetchAndStore(ctx context.Context, sym string, full bool) (*market.Series, error) {
	old, hasOld := s.readEntry(sym)
	e, err := s.fetchEntry(ctx, sym, old, full)
	if err != nil {
		err = fmt.Errorf("cache: fetch %s: %w", sym, err)
		s.lastErr.set(sym, err)
		if hasOld {
			return old.Series, nil
		}
		return nil, err
	}
	s.lastErr.set(sym, nil)
	s.lastWriteErr.set(sym, s.writeEntry(sym, e))
	return e.Series, nil
}

// fetchEntry tops old up when allowed and falls back to a full fetch when
// full is set, when old cannot be topped up, or when mergeTail declines.
func (s *Store) fetchEntry(ctx context.Context, sym string, old *entry, full bool) (*entry, error) {
	if !full && s.canTopUp(old) {
		e, err := s.topUp(ctx, sym, old)
		if !errors.Is(err, errNeedFull) {
			return e, err
		}
	}
	return s.fetchFull(ctx, sym)
}

// canTopUp reports whether old may be extended by a range fetch: the
// source can fetch ranges, the file is in the current format, holds at
// least one bar and its last full fetch is recent enough.
func (s *Store) canTopUp(old *entry) bool {
	if s.ranged == nil || old == nil || old.Version != formatVersion || old.Series.Len() == 0 {
		return false
	}
	age := s.now().Sub(old.FullFetchedAt)
	return age >= 0 && age <= s.fullInterval
}

// topUp fetches the days from overlap before old's last bar up to today
// and merges them into old. It keeps FullFetchedAt and stamps FetchedAt
// with the current time. An error wrapping errNeedFull means the caller
// must fetch the whole history instead.
func (s *Store) topUp(ctx context.Context, sym string, old *entry) (*entry, error) {
	last, _ := old.Series.Last()
	now := s.now()
	tail, err := s.ranged.SeriesRange(ctx, sym, last.Date.Add(-s.overlap), market.Day(now))
	if err != nil {
		return nil, err
	}
	merged, err := mergeTail(old.Series, tail)
	if err != nil {
		return nil, err
	}
	return &entry{Version: formatVersion, FetchedAt: now, FullFetchedAt: old.FullFetchedAt, Series: merged}, nil
}

// fetchFull fetches the whole history of sym and stamps both times.
func (s *Store) fetchFull(ctx context.Context, sym string) (*entry, error) {
	series, err := s.next.Series(ctx, sym)
	if err != nil {
		return nil, err
	}
	now := s.now()
	return &entry{Version: formatVersion, FetchedAt: now, FullFetchedAt: now, Series: series}, nil
}

// path returns the price file for sym.
func (s *Store) path(sym string) string {
	return filepath.Join(s.dir, sym+".json")
}

// readEntry loads the price file for sym. ok is false when the file is
// missing, unreadable or does not hold a valid series; all three count as
// a cache miss.
func (s *Store) readEntry(sym string) (e *entry, ok bool) {
	data, err := os.ReadFile(s.path(sym))
	if err != nil {
		return nil, false
	}
	e, err = decodeEntry(data)
	if err != nil {
		return nil, false
	}
	return e, true
}

// writeEntry persists e as sym's price file, atomically.
func (s *Store) writeEntry(sym string, e *entry) error {
	e.summarize()
	return writeJSONAtomic(s.dir, sym+".json", e)
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

// normalizeSymbols applies normalizeSymbol to every symbol and stops at
// the first invalid one.
func normalizeSymbols(symbols []string) ([]string, error) {
	syms := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		sym, err := normalizeSymbol(symbol)
		if err != nil {
			return nil, err
		}
		syms = append(syms, sym)
	}
	return syms, nil
}
