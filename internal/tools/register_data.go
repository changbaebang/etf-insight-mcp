package tools

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"time"
	"unicode"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerData adds the data tool group: fund data, quotes, dividends,
// splits, search, news and the market overview.
func (d Deps) registerData(s *mcp.Server) {
	d.registerSearchSymbols(s)
	d.registerGetQuote(s)
	d.registerGetDividends(s)
	d.registerGetSplits(s)
	d.registerGetFundProfile(s)
	d.registerGetHoldings(s)
	d.registerGetFundPerformance(s)
	d.registerGetNews(s)
	d.registerMarketOverview(s)
}

// errFundSourceMissing is what every tool that needs Deps.Fund answers
// when the server was built without one.
var errFundSourceMissing = errors.New("fund data source not configured: this server has no quote/fund provider, so quotes, fund profiles, holdings, performance, search and news are unavailable; price-history tools (get_etf_info, get_price_history, get_dividends, get_splits) still work")

// fundStaleAfter is how old a fund document may be before the output
// warns that the last refresh probably failed. The fund cache refetches
// after a day, so anything well past that was served stale.
const fundStaleAfter = 48 * time.Hour

// requireFund returns the fund source or the not-configured tool error.
func (d Deps) requireFund() (market.FundSource, error) {
	if d.Fund == nil {
		return nil, errFundSourceMissing
	}
	return d.Fund, nil
}

// requireFundSymbol normalises a single-symbol input of a fund tool.
func requireFundSymbol(symbol string) (string, error) {
	sym := normalizeSymbol(symbol)
	if sym == "" {
		return "", errors.New("symbol is required, e.g. VOO; use list_etfs or search_symbols to find one")
	}
	return sym, nil
}

// parseDataLimit validates an optional list size: 0 means def, anything
// outside 1..maxN is an error naming the bounds.
func parseDataLimit(field string, n, def, maxN int) (int, error) {
	switch {
	case n == 0:
		return def, nil
	case n < 1 || n > maxN:
		return 0, fmt.Errorf("%s must be between 1 and %d, got %d", field, maxN, n)
	default:
		return n, nil
	}
}

// fundPctPtr turns an optional fraction into an optional percentage
// rounded to two decimals; nil and non-finite values stay unreported.
func fundPctPtr(fraction *float64) *float64 {
	if fraction == nil || !dataFinite(*fraction) {
		return nil
	}
	return ptr(pct(*fraction))
}

// fundRound2Ptr rounds an optional value that is already in its display
// unit (a ratio, or a percentage the provider reports as such).
func fundRound2Ptr(v *float64) *float64 {
	if v == nil || !dataFinite(*v) {
		return nil
	}
	return ptr(round2(*v))
}

// dataFinite reports whether v can be encoded as JSON.
func dataFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// dataTimestamp renders a point in time as RFC 3339 in UTC; zero renders
// as "".
func dataTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// describeFundError rewrites a FundSource error for the model: unknown
// symbols get the same hints as price lookups.
func describeFundError(sym, what string, err error) error {
	if errors.Is(err, market.ErrNotFound) {
		return describeFetchError(sym, err)
	}
	return fmt.Errorf("fetching %s for %s failed: %s", what, sym, fundCause(err))
}

// fundLayerPrefix matches the context the fund layers put in front of an
// upstream error: "cache: fetch quotes SPY,QQQ: ", then "yahoo: quote
// SPY,QQQ: " or "yahoo: search "x": " or "yahoo: SPY: ", then "request: ".
var fundLayerPrefix = regexp.MustCompile(`^(cache: fetch (quotes|profile|holdings|performance) \S+: )?(yahoo: (quote \S+|search "[^"]*"|\S+): )?(request: )?`)

// fundCause renders a FundSource error for a reader: the layer prefixes
// and request URLs are dropped, the cause is kept.
func fundCause(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return fundLayerPrefix.ReplaceAllString(err.Error(), "")
}

// requireLatinQuery rejects a search or news query without any Latin
// letter or digit: Yahoo's search answers such a query with "Invalid
// Search Query". The returned bool reports whether the query mixes in
// words in other scripts, which Yahoo ignores.
func requireLatinQuery(query string) (mixed bool, err error) {
	latin := false
	for _, r := range query {
		switch {
		case unicode.Is(unicode.Latin, r) || ('0' <= r && r <= '9'):
			latin = true
		case unicode.IsLetter(r):
			mixed = true
		}
	}
	if !latin {
		return false, fmt.Errorf("query %q has no Latin letters or digits: Yahoo Finance search only understands Latin text, so use the ticker or the English fund name (for example 'dividend' or 'SCHD')", query)
	}
	return mixed, nil
}

// fundFetchedWarnings warns when a fund document is older than
// fundStaleAfter, which means the cache served it after a failed refresh.
func (d Deps) fundFetchedWarnings(sym string, fetchedAt time.Time) []string {
	if fetchedAt.IsZero() {
		return nil
	}
	age := d.clock().Sub(fetchedAt)
	if age <= fundStaleAfter {
		return nil
	}
	return []string{fmt.Sprintf("%s fund data was fetched %d days ago (%s); the last refresh probably failed, so figures may be stale", sym, int(age.Hours()/24), dataTimestamp(fetchedAt))}
}
