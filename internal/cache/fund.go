package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// fundSubdir is the directory under the price cache that holds fund files.
const fundSubdir = "fund"

// Defaults for NewFundStore; FundOptions change them.
const (
	// DefaultFundTTL is how long a profile, holdings or performance file is
	// served without asking the source. Fund descriptions change rarely.
	DefaultFundTTL = 24 * time.Hour
	// DefaultQuoteTTL is how long a quote is served from memory.
	DefaultQuoteTTL = 15 * time.Minute
)

// fundFormatVersion is the on-disk format of fund files.
const fundFormatVersion = 1

// Fund document kinds. The first three are files
// <dir>/fund/<SYMBOL>.<kind>.json; quotes live in memory only and use the
// kind as their LastError key suffix.
const (
	kindProfile     = "profile"
	kindHoldings    = "holdings"
	kindPerformance = "performance"
	kindQuote       = "quote"
)

// fundKinds are the documents FundStore keeps on disk.
var fundKinds = []string{kindProfile, kindHoldings, kindPerformance}

// fundFile is the on-disk layout of one fund document.
type fundFile[T any] struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	Data      *T        `json:"data"`
}

// cachedQuote is one quote held in memory with the time it was fetched.
type cachedQuote struct {
	quote market.Quote
	at    time.Time
}

// FundStore is a market.FundSource that caches another FundSource: fund
// profiles, holdings and performance on disk under <dir>/fund with their
// own TTL, quotes in memory for a shorter one. Search and News pass
// through uncached. It is safe for concurrent use. Construct it with
// NewFundStore.
type FundStore struct {
	next     market.FundSource
	dir      string // <cache dir>/fund
	ttl      time.Duration
	quoteTTL time.Duration
	now      func() time.Time

	mu     sync.Mutex
	quotes map[string]cachedQuote

	lastErr      errTable // keyed by "<SYMBOL>.<kind>"
	lastWriteErr errTable
}

// FundOption configures a FundStore; pass FundOptions to NewFundStore.
type FundOption func(*FundStore)

// WithFundClock makes the FundStore read the current time from now
// instead of time.Now.
func WithFundClock(now func() time.Time) FundOption {
	return func(f *FundStore) {
		if now != nil {
			f.now = now
		}
	}
}

// WithFundTTL sets how long profile, holdings and performance files are
// served without asking the source. A value that is not positive keeps
// DefaultFundTTL.
func WithFundTTL(d time.Duration) FundOption {
	return func(f *FundStore) {
		if d > 0 {
			f.ttl = d
		}
	}
}

// WithQuoteTTL sets how long quotes are served from memory. A value that
// is not positive keeps DefaultQuoteTTL.
func WithQuoteTTL(d time.Duration) FundOption {
	return func(f *FundStore) {
		if d > 0 {
			f.quoteTTL = d
		}
	}
}

// NewFundStore returns a FundStore that serves next from <dir>/fund. Pass
// the same dir as to New so that Store.Status counts the fund files and
// Store.Clear removes them. The directory is created on the first write;
// temporary files left behind by an interrupted write (older than ten
// minutes) are removed.
func NewFundStore(next market.FundSource, dir string, opts ...FundOption) *FundStore {
	f := &FundStore{
		next:     next,
		dir:      filepath.Join(dir, fundSubdir),
		ttl:      DefaultFundTTL,
		quoteTTL: DefaultQuoteTTL,
		now:      time.Now,
		quotes:   make(map[string]cachedQuote),
	}
	for _, opt := range opts {
		opt(f)
	}
	sweepTempFiles(f.dir, f.now())
	return f
}

// QuoteReport is what FundStore.QuoteReport served for one request. A
// requested symbol that is neither in Quotes nor in Failed is unknown to
// the source.
type QuoteReport struct {
	// Quotes holds one quote per requested symbol that has one, in request
	// order, followed in symbol order by any quote the source returned
	// under a symbol that was not requested.
	Quotes []market.Quote
	// Stale lists the quotes in Quotes that were served from memory past
	// the quote TTL because refreshing them failed, in request order.
	Stale []StaleQuote
	// Failed lists the requested symbols without a quote because the fetch
	// failed and memory held none, in request order.
	Failed []string
	// Err is the fetch failure behind Stale and Failed; nil when the
	// source answered or did not need to be asked.
	Err error
}

// StaleQuote describes a quote served from memory after a failed refresh.
type StaleQuote struct {
	Symbol string
	// FetchedAt is when the source last returned the quote.
	FetchedAt time.Time
	// Age is how long before the request that was.
	Age time.Duration
}

// QuoteReport returns quotes for symbols in the order requested, serving
// each from memory when it was fetched less than the quote TTL ago and
// asking the wrapped source for the rest in one call. When that call
// fails, the quotes memory still holds for those symbols, however old, are
// served and listed in Stale, the symbols memory lacks are listed in
// Failed, and the failure is kept in Err and for LastError. Symbols the
// source answers without are unknown to it and are simply omitted, as
// market.FundSource specifies.
//
// The error is non-nil only for a symbol that is not safe as a cache key
// (it wraps ErrInvalidSymbol); upstream trouble is reported in the
// QuoteReport.
func (f *FundStore) QuoteReport(ctx context.Context, symbols []string) (*QuoteReport, error) {
	syms, err := normalizeSymbols(symbols)
	if err != nil {
		return nil, err
	}
	now := f.now()
	have, missing := f.freshQuotes(syms, now)
	r := &QuoteReport{}
	if len(missing) > 0 {
		got, err := f.next.Quote(ctx, missing)
		if err != nil {
			r.Err = fmt.Errorf("cache: fetch quotes %s: %w", strings.Join(missing, ","), err)
			f.setQuoteErr(missing, r.Err)
			r.Stale, r.Failed = f.staleQuotes(missing, have, now)
		} else {
			f.setQuoteErr(missing, nil)
			f.rememberQuotes(got, now, have)
		}
	}
	r.Quotes = orderQuotes(syms, have)
	return r, nil
}

// Quote is QuoteReport for callers that only need the quotes, and it
// implements market.FundSource: stale quotes are served like fresh ones,
// and the fetch failure is returned only when no requested quote could be
// served at all. Use QuoteReport to tell stale quotes and failed fetches
// apart from symbols the source does not know.
func (f *FundStore) Quote(ctx context.Context, symbols []string) ([]market.Quote, error) {
	r, err := f.QuoteReport(ctx, symbols)
	if err != nil {
		return nil, err
	}
	if len(r.Quotes) == 0 && r.Err != nil {
		return nil, r.Err
	}
	return r.Quotes, nil
}

// freshQuotes splits syms into quotes fresh in memory and symbols that
// must be fetched. Duplicates are fetched once.
func (f *FundStore) freshQuotes(syms []string, now time.Time) (have map[string]market.Quote, missing []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	have = make(map[string]market.Quote, len(syms))
	for _, sym := range syms {
		if _, seen := have[sym]; seen || slices.Contains(missing, sym) {
			continue
		}
		if q, ok := f.quotes[sym]; ok && isFresh(now, q.at, f.quoteTTL) {
			have[sym] = q.quote
			continue
		}
		missing = append(missing, sym)
	}
	return have, missing
}

// staleQuotes adds whatever memory holds for syms to have, however old,
// after a failed fetch. It returns those quotes as stale, with their age
// as of now, and the symbols memory has nothing for as failed.
func (f *FundStore) staleQuotes(syms []string, have map[string]market.Quote, now time.Time) (stale []StaleQuote, failed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sym := range syms {
		q, ok := f.quotes[sym]
		if !ok {
			failed = append(failed, sym)
			continue
		}
		have[sym] = q.quote
		stale = append(stale, StaleQuote{Symbol: sym, FetchedAt: q.at, Age: now.Sub(q.at)})
	}
	return stale, failed
}

// rememberQuotes stores got in memory as of now and adds them to have.
func (f *FundStore) rememberQuotes(got []market.Quote, now time.Time, have map[string]market.Quote) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, q := range got {
		sym := strings.ToUpper(q.Symbol)
		f.quotes[sym] = cachedQuote{quote: q, at: now}
		have[sym] = q
	}
}

// setQuoteErr records err (or clears it) for every symbol in syms.
func (f *FundStore) setQuoteErr(syms []string, err error) {
	for _, sym := range syms {
		f.lastErr.set(sym+"."+kindQuote, err)
	}
}

// orderQuotes returns the quotes in have in the order of syms, each symbol
// once, followed in symbol order by any quote the source returned under a
// symbol that was not requested.
func orderQuotes(syms []string, have map[string]market.Quote) []market.Quote {
	out := make([]market.Quote, 0, len(have))
	seen := make(map[string]bool, len(have))
	for _, sym := range syms {
		if q, ok := have[sym]; ok && !seen[sym] {
			out = append(out, q)
			seen[sym] = true
		}
	}
	extra := make([]string, 0)
	for sym := range have {
		if !seen[sym] {
			extra = append(extra, sym)
		}
	}
	slices.Sort(extra)
	for _, sym := range extra {
		out = append(out, have[sym])
	}
	return out
}

// FundProfile returns the profile of symbol, from its file when that is
// younger than the fund TTL. It implements market.FundSource.
func (f *FundStore) FundProfile(ctx context.Context, symbol string) (*market.FundProfile, error) {
	sym, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return cachedDoc(ctx, f, sym, kindProfile, f.next.FundProfile)
}

// Holdings returns the holdings of symbol, from its file when that is
// younger than the fund TTL. It implements market.FundSource.
func (f *FundStore) Holdings(ctx context.Context, symbol string) (*market.Holdings, error) {
	sym, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return cachedDoc(ctx, f, sym, kindHoldings, f.next.Holdings)
}

// Performance returns the performance of symbol, from its file when that
// is younger than the fund TTL. It implements market.FundSource.
func (f *FundStore) Performance(ctx context.Context, symbol string) (*market.Performance, error) {
	sym, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	return cachedDoc(ctx, f, sym, kindPerformance, f.next.Performance)
}

// Search passes through to the wrapped source; results are not cached.
func (f *FundStore) Search(ctx context.Context, query string, limit int) ([]market.SearchHit, error) {
	return f.next.Search(ctx, query, limit)
}

// News passes through to the wrapped source; results are not cached.
func (f *FundStore) News(ctx context.Context, query string, limit int) ([]market.NewsItem, error) {
	return f.next.News(ctx, query, limit)
}

// LastError returns the most recent upstream failures for symbol across
// quotes, profile, holdings and performance, joined, or nil when the last
// fetch of each succeeded or never happened. A non-nil value after a
// successful call means some of the data came from a stale file or a
// stale quote in memory.
func (f *FundStore) LastError(symbol string) error {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	errs := make([]error, 0, 1+len(fundKinds))
	for _, kind := range append([]string{kindQuote}, fundKinds...) {
		errs = append(errs, f.lastErr.get(sym+"."+kind))
	}
	return errors.Join(errs...)
}

// LastWriteError returns the most recent failures to write symbol's fund
// files, joined, or nil. A non-nil value means the data served is current
// but was not cached, so the next call fetches again.
func (f *FundStore) LastWriteError(symbol string) error {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	errs := make([]error, 0, len(fundKinds))
	for _, kind := range fundKinds {
		errs = append(errs, f.lastWriteErr.get(sym+"."+kind))
	}
	return errors.Join(errs...)
}

// cachedDoc serves the document of kind for sym from its file when that is
// younger than the TTL and otherwise through fetch, rewriting the file.
// When fetch fails and a file exists, the stale document is served and the
// failure kept for LastError; a document that could not be written is
// still served and the problem kept for LastWriteError.
func cachedDoc[T any](ctx context.Context, f *FundStore, sym, kind string, fetch func(context.Context, string) (*T, error)) (*T, error) {
	name := fundFileName(sym, kind)
	old, hasOld := readFundFile[T](filepath.Join(f.dir, name))
	if hasOld && isFresh(f.now(), old.FetchedAt, f.ttl) {
		return old.Data, nil
	}
	key := sym + "." + kind
	doc, err := fetch(ctx, sym)
	if err == nil && doc == nil {
		err = errors.New("source returned nothing")
	}
	if err != nil {
		err = fmt.Errorf("cache: fetch %s %s: %w", kind, sym, err)
		f.lastErr.set(key, err)
		if hasOld {
			return old.Data, nil
		}
		return nil, err
	}
	f.lastErr.set(key, nil)
	file := fundFile[T]{Version: fundFormatVersion, FetchedAt: f.now(), Data: doc}
	f.lastWriteErr.set(key, writeJSONAtomic(f.dir, name, file))
	return doc, nil
}

// readFundFile loads a fund file. ok is false when it is missing,
// unreadable or in another format; all count as a miss.
func readFundFile[T any](path string) (file *fundFile[T], ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var ff fundFile[T]
	if err := json.Unmarshal(data, &ff); err != nil || ff.Version != fundFormatVersion || ff.Data == nil {
		return nil, false
	}
	return &ff, true
}

// isFresh reports whether something fetched at fetchedAt is younger than
// ttl as of now. A fetch time in the future counts as stale.
func isFresh(now, fetchedAt time.Time, ttl time.Duration) bool {
	age := now.Sub(fetchedAt)
	return age >= 0 && age < ttl
}

// fundFileName is the file name of one fund document.
func fundFileName(sym, kind string) string {
	return sym + "." + kind + ".json"
}

// fundPath is the path of one fund document below the price cache dir.
func fundPath(dir, sym, kind string) string {
	return filepath.Join(dir, fundSubdir, fundFileName(sym, kind))
}

// fundFileParts splits <SYMBOL>.<kind>.json into its parts; ok is false
// for a name this package did not write.
func fundFileParts(name string) (sym, kind string, ok bool) {
	stem, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return "", "", false
	}
	i := strings.LastIndex(stem, ".")
	if i < 0 {
		return "", "", false
	}
	sym, kind = stem[:i], stem[i+1:]
	if !symbolPattern.MatchString(sym) || !slices.Contains(fundKinds, kind) {
		return "", "", false
	}
	return sym, kind, true
}
