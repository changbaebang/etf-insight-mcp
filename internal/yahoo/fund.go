package yahoo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

const (
	// quoteChunk is the most symbols sent in one v7 quote request.
	quoteChunk = 50

	// summaryModules are the quoteSummary modules fetched in one request.
	// One response feeds FundProfile, Holdings and Performance alike.
	summaryModules = "fundProfile,topHoldings,fundPerformance,defaultKeyStatistics,summaryDetail,price"

	// defaultSearchLimit replaces a non-positive Search or News limit;
	// maxSearchLimit caps a larger one.
	defaultSearchLimit = 10
	maxSearchLimit     = 50
)

// Quote fetches current quotes for symbols from the v7 quote endpoint, 50
// symbols per request. It implements market.FundSource.
//
// Symbols are trimmed, upper-cased and de-duplicated; blank ones are
// dropped, and no symbols means no request and a nil result. Symbols Yahoo
// does not know are absent from the result rather than an error, so the
// caller detects them by comparing with its request. Quotes come back in
// Yahoo's order.
func (c *Client) Quote(ctx context.Context, symbols []string) ([]market.Quote, error) {
	wanted := normalizeSymbols(symbols)
	if len(wanted) == 0 {
		return nil, nil
	}
	quotes := make([]market.Quote, 0, len(wanted))
	for chunk := range slices.Chunk(wanted, quoteChunk) {
		body, err := c.get(ctx, c.quoteURL(chunk), true)
		if err != nil {
			return nil, fmt.Errorf("yahoo: quote %s: %w", symbolsLabel(chunk), err)
		}
		part, err := parseQuotes(body)
		if err != nil {
			return nil, fmt.Errorf("yahoo: quote %s: %w", symbolsLabel(chunk), err)
		}
		quotes = append(quotes, part...)
	}
	return quotes, nil
}

// FundProfile fetches descriptive data for one fund. It implements
// market.FundSource. The symbol is trimmed and upper-cased; an unknown one
// returns an error wrapping market.ErrNotFound. FundProfile, Holdings and
// Performance share one quoteSummary response per symbol for the duration
// of WithSummaryTTL.
func (c *Client) FundProfile(ctx context.Context, symbol string) (*market.FundProfile, error) {
	s, symbol, err := c.summary(ctx, symbol)
	if err != nil {
		return nil, err
	}
	return s.fundProfile(symbol), nil
}

// Holdings fetches the portfolio composition of one fund. It implements
// market.FundSource; see FundProfile for symbol handling and caching.
func (c *Client) Holdings(ctx context.Context, symbol string) (*market.Holdings, error) {
	s, symbol, err := c.summary(ctx, symbol)
	if err != nil {
		return nil, err
	}
	return s.holdings(symbol), nil
}

// Performance fetches Yahoo's trailing, calendar-year and risk statistics
// for one fund. It implements market.FundSource; see FundProfile for
// symbol handling and caching.
func (c *Client) Performance(ctx context.Context, symbol string) (*market.Performance, error) {
	s, symbol, err := c.summary(ctx, symbol)
	if err != nil {
		return nil, err
	}
	return s.performance(symbol), nil
}

// Search looks symbols up by ticker or name fragment and returns at most
// limit hits. It implements market.FundSource. A non-positive limit means
// 10 and anything above 50 is capped; an empty query is an error.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]market.SearchHit, error) {
	query, limit, err := searchArgs(query, limit)
	if err != nil {
		return nil, err
	}
	resp, err := c.search(ctx, query, limit, 0)
	if err != nil {
		return nil, err
	}
	return resp.hits(limit), nil
}

// News returns at most limit recent headlines matching query, newest
// first as Yahoo ranks them. It implements market.FundSource; limits are
// handled as in Search.
func (c *Client) News(ctx context.Context, query string, limit int) ([]market.NewsItem, error) {
	query, limit, err := searchArgs(query, limit)
	if err != nil {
		return nil, err
	}
	resp, err := c.search(ctx, query, 0, limit)
	if err != nil {
		return nil, err
	}
	return resp.items(limit), nil
}

// summary normalizes symbol and returns its quoteSummary, wrapping every
// error with the symbol.
func (c *Client) summary(ctx context.Context, symbol string) (*fundSummary, string, error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, "", err
	}
	s, err := c.fetchQuoteSummary(ctx, symbol)
	if err != nil {
		return nil, "", fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	return s, symbol, nil
}

// fetchQuoteSummary returns the quoteSummary modules for symbol, serving a
// response fetched within summaryTTL from memory so that the three fund
// methods for one symbol cost a single request. Errors are not memoized,
// and two misses for the same symbol at the same instant both fetch.
func (c *Client) fetchQuoteSummary(ctx context.Context, symbol string) (*fundSummary, error) {
	now := c.now()
	if s, ok := c.memo.get(symbol, now); ok {
		return s, nil
	}
	body, err := c.get(ctx, c.summaryURL(symbol), true)
	if err != nil {
		return nil, err
	}
	result, err := parseQuoteSummary(body)
	if err != nil {
		return nil, err
	}
	s := &fundSummary{result: result, fetchedAt: now}
	if c.summaryTTL > 0 {
		c.memo.put(symbol, s, now, now.Add(c.summaryTTL))
	}
	return s, nil
}

// search performs one search request. quotes and news are the counts
// requested for each block; 0 turns a block off.
func (c *Client) search(ctx context.Context, query string, quotes, news int) (*searchResponse, error) {
	body, err := c.get(ctx, c.searchURL(query, quotes, news), false)
	if err != nil {
		return nil, fmt.Errorf("yahoo: search %q: %w", query, err)
	}
	resp, err := parseSearch(body)
	if err != nil {
		return nil, fmt.Errorf("yahoo: search %q: %w", query, err)
	}
	return resp, nil
}

// searchArgs trims the query, rejects an empty one and clamps limit:
// non-positive becomes defaultSearchLimit, larger than maxSearchLimit is
// capped.
func searchArgs(query string, limit int) (string, int, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", 0, errors.New("yahoo: empty search query")
	}
	switch {
	case limit <= 0:
		limit = defaultSearchLimit
	case limit > maxSearchLimit:
		limit = maxSearchLimit
	}
	return query, limit, nil
}

// quoteURL builds the v7 batch quote request; the crumb is appended at
// send time.
func (c *Client) quoteURL(symbols []string) string {
	q := url.Values{}
	q.Set("symbols", strings.Join(symbols, ","))
	return c.baseURL + "/v7/finance/quote?" + q.Encode()
}

// summaryURL builds the v10 quoteSummary request for symbol with every
// module this package reads; the crumb is appended at send time.
func (c *Client) summaryURL(symbol string) string {
	q := url.Values{}
	q.Set("modules", summaryModules)
	return c.baseURL + "/v10/finance/quoteSummary/" + url.PathEscape(symbol) + "?" + q.Encode()
}

// searchURL builds the search request asking for the given number of
// symbol hits and news items.
func (c *Client) searchURL(query string, quotes, news int) string {
	q := url.Values{}
	q.Set("q", query)
	q.Set("quotesCount", strconv.Itoa(quotes))
	q.Set("newsCount", strconv.Itoa(news))
	return c.baseURL + "/v1/finance/search?" + q.Encode()
}

// normalizeSymbols trims, upper-cases and de-duplicates symbols, keeping
// the first occurrence's position and dropping blanks.
func normalizeSymbols(symbols []string) []string {
	out := make([]string, 0, len(symbols))
	seen := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// symbolsLabel names a chunk of symbols in an error message without
// listing fifty of them.
func symbolsLabel(symbols []string) string {
	const show = 3
	if len(symbols) <= show {
		return strings.Join(symbols, ",")
	}
	return fmt.Sprintf("%s,... (%d symbols)", strings.Join(symbols[:show], ","), len(symbols))
}

// summaryMemo keeps recent quoteSummary responses per symbol.
type summaryMemo struct {
	mu      sync.Mutex
	entries map[string]memoEntry
}

type memoEntry struct {
	summary *fundSummary
	expires time.Time
}

// get returns the memoized summary for symbol unless it has expired at
// now, in which case it is dropped.
func (m *summaryMemo) get(symbol string, now time.Time) (*fundSummary, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[symbol]
	if !ok {
		return nil, false
	}
	if !now.Before(e.expires) {
		delete(m.entries, symbol)
		return nil, false
	}
	return e.summary, true
}

// put stores s until expires and evicts every entry already expired at
// now, so the memo never grows beyond the symbols of one TTL window.
func (m *summaryMemo) put(symbol string, s *fundSummary, now, expires time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]memoEntry)
	}
	for k, e := range m.entries {
		if !now.Before(e.expires) {
			delete(m.entries, k)
		}
	}
	m.entries[symbol] = memoEntry{summary: s, expires: expires}
}
