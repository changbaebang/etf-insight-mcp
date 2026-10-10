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
// caller's context (a caller whose context is already done starts none),
// and when the source fails but a file exists the stale series is served
// while the failure is kept for Store.LastError. A series that could not
// be written to disk is still served and the problem kept for
// Store.LastWriteError. Store.Refresh always downloads the whole history,
// and Store.RefreshReport says whether that download replaced the file.
// Status reports what is on disk and warns when the cache grows past its
// thresholds; Clear and ClearAll remove files, ClearAll only those whose
// name and header show this package wrote them. Nothing is logged.
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
// full records whether it downloads the whole history, so that a Refresh
// can tell it apart from a top-up. out is set before done is closed.
type call struct {
	done chan struct{}
	full bool
	out  outcome
}

// outcome is what one upstream fetch produced. Every caller that waited
// on the fetch gets the same outcome, so it describes this fetch rather
// than whatever the per-symbol LastError and LastWriteError tables hold
// by the time a caller reads them.
type outcome struct {
	series   *market.Series // what is served; nil only when err is set
	err      error          // set when there is nothing to serve
	fetchErr error          // the upstream failure when series is the stale file
	writeErr error          // why series could not be written to disk
}

// wait blocks until the fetch finishes or ctx is done.
func (c *call) wait(ctx context.Context) (outcome, error) {
	select {
	case <-c.done:
		return c.out, c.out.err
	case <-ctx.Done():
		return outcome{}, ctx.Err()
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
	// unwritten holds, per symbol, the newest entry that was fetched but
	// could not be written to disk. Reads prefer it over an older file, so
	// a failed write never brings back data from before the download; it
	// is dropped as soon as a write succeeds or the symbol is cleared.
	unwritten map[string]*entry

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
		unwritten:    make(map[string]*entry),
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
	if e, ok := s.current(sym); ok && s.isFresh(e) {
		return e.Series, nil
	}
	out, err := s.fetch(ctx, sym, false)
	return out.series, err
}

// current returns the newest entry known for sym: one held in memory
// because its write failed, when it is newer than the file, otherwise the
// file's.
func (s *Store) current(sym string) (*entry, bool) {
	file, ok := s.readEntry(sym)
	s.mu.Lock()
	mem := s.unwritten[sym]
	s.mu.Unlock()
	if mem != nil && (!ok || mem.FetchedAt.After(file.FetchedAt)) {
		return mem, true
	}
	return file, ok
}

// rememberWrite keeps e in memory when writing it failed, so later reads
// still see it, and forgets it once a write succeeds.
func (s *Store) rememberWrite(sym string, e *entry, writeErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if writeErr != nil {
		s.unwritten[sym] = e
		return
	}
	delete(s.unwritten, sym)
}

// forgetUnwritten drops the in-memory entries of syms, or of every symbol
// when syms is empty.
func (s *Store) forgetUnwritten(syms ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(syms) == 0 {
		clear(s.unwritten)
		return
	}
	for _, sym := range syms {
		delete(s.unwritten, sym)
	}
}

// isFresh reports whether e is in the current format, was brought up to
// date less than the TTL ago and, when its last bar was an intraday price,
// that session has not ended yet. A fetch time in the future (clock skew,
// an edited or restored file) counts as stale rather than fresh forever.
func (s *Store) isFresh(e *entry) bool {
	if e.Version != formatVersion || !isFresh(s.now(), e.FetchedAt, s.ttl) {
		return false
	}
	// An intraday last bar goes stale when its session ends, however
	// young the file: the next read tops it up with the settled close.
	until := e.Series.Meta.ProvisionalUntil
	return until.IsZero() || s.now().Before(until)
}

// Refresh fetches the whole history of symbol from the wrapped source
// regardless of the TTL and rewrites its cache file. It shares the
// stale-on-error behavior with Series: when the download fails and a file
// exists, the old series is returned with a nil error. A Refresh that
// arrives while a full fetch of the symbol is in flight shares it; one
// that arrives during a top-up waits for the top-up to finish and then
// downloads the whole history, so a Refresh always means a full download.
// Use RefreshReport to learn whether the download succeeded.
func (s *Store) Refresh(ctx context.Context, symbol string) (*market.Series, error) {
	r, err := s.RefreshReport(ctx, symbol)
	return r.Series, err
}

// Refreshed is what one RefreshReport call did.
type Refreshed struct {
	// Series is the history served: the new download, or the previous
	// cached file when the download failed.
	Series *market.Series
	// FetchErr is why the download failed when Series is the previous
	// cached file; nil when the whole history was downloaded.
	FetchErr error
	// WriteErr is why the download could not replace the cached file; nil
	// when it did. Series is current either way.
	WriteErr error
}

// Replaced reports whether the whole history was downloaded and written
// over the cached file.
func (r Refreshed) Replaced() bool {
	return r.FetchErr == nil && r.WriteErr == nil
}

// RefreshReport is Refresh that also reports what happened to this call's
// download. Unlike LastError and LastWriteError, which another call's
// fetch of the same symbol can overwrite at any time, the report belongs
// to the fetch this call waited on. The error is non-nil only when there
// is no series to serve at all: an invalid symbol, ctx done, or a failed
// download with no cached file.
func (s *Store) RefreshReport(ctx context.Context, symbol string) (Refreshed, error) {
	sym, err := normalizeSymbol(symbol)
	if err != nil {
		return Refreshed{}, err
	}
	out, err := s.fetch(ctx, sym, true)
	if err != nil {
		return Refreshed{}, err
	}
	return Refreshed{Series: out.series, FetchErr: out.fetchErr, WriteErr: out.writeErr}, nil
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
// another, so a burst of cold reads costs a single request. The one
// exception is a full fetch (full set) arriving during a top-up: it waits
// for the top-up to finish and then starts, or joins, a full fetch. The
// upstream call runs in its own goroutine under a context detached from
// every caller (context.WithoutCancel plus fetchTimeout), so a caller that
// gives up does not fail the others; every caller, the first included,
// returns as soon as its own ctx is done. A caller whose ctx is already
// done starts nothing, since nobody would wait for the download.
func (s *Store) fetch(ctx context.Context, sym string, full bool) (outcome, error) {
	for {
		if err := ctx.Err(); err != nil {
			return outcome{}, err
		}
		s.mu.Lock()
		c, ok := s.inflight[sym]
		if !ok {
			c = &call{done: make(chan struct{}), full: full}
			s.inflight[sym] = c
			s.mu.Unlock()
			go s.run(ctx, sym, c)
			return c.wait(ctx)
		}
		s.mu.Unlock()
		if c.full || !full {
			return c.wait(ctx)
		}
		// A top-up is in flight but a full download was asked for.
		select {
		case <-c.done:
		case <-ctx.Done():
			return outcome{}, ctx.Err()
		}
	}
}

// run performs the fetch of c detached from ctx's cancellation, then
// releases the in-flight slot and wakes the waiters.
func (s *Store) run(ctx context.Context, sym string, c *call) {
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	c.out = s.fetchAndStore(fetchCtx, sym, c.full)

	s.mu.Lock()
	delete(s.inflight, sym)
	s.mu.Unlock()
	close(c.done)
}

// fetchAndStore brings sym up to date through the wrapped source and
// persists the result. When the file is already fresh and full is not set
// it is served as is: another fetch may have finished between the
// caller's own read and taking the in-flight slot. When the source fails
// and a file exists, the stale series is served and the failure recorded
// in the outcome and for LastError. A series that could not be written is
// still served, with the problem recorded in the outcome and for
// LastWriteError.
func (s *Store) fetchAndStore(ctx context.Context, sym string, full bool) outcome {
	old, hasOld := s.current(sym)
	if hasOld && !full && s.isFresh(old) {
		return outcome{series: old.Series}
	}
	e, err := s.fetchEntry(ctx, sym, old, full)
	if err != nil {
		err = fmt.Errorf("cache: fetch %s: %w", sym, err)
		s.lastErr.set(sym, err)
		if hasOld {
			return outcome{series: old.Series, fetchErr: err}
		}
		return outcome{err: err}
	}
	s.lastErr.set(sym, nil)
	writeErr := s.writeEntry(sym, e)
	s.lastWriteErr.set(sym, writeErr)
	s.rememberWrite(sym, e, writeErr)
	return outcome{series: e.Series, writeErr: writeErr}
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
	from := market.Day(last.Date.Add(-s.overlap))
	tail, err := s.ranged.SeriesRange(ctx, sym, from, market.Day(now))
	if err != nil {
		return nil, err
	}
	merged, err := mergeTail(old.Series, tail, from)
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
