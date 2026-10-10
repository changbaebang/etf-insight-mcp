// Package tools exposes the domain packages (universe, sim, analytics and
// the price Source) as MCP tools, one resource and one prompt.
//
// Register wires everything onto an mcp.Server. The handlers are methods
// on Deps, so a test can inject a fake market.Source and a fixed clock and
// drive the server through an in-memory transport.
//
// Conventions every tool follows:
//
//   - JSON field names are snake_case and every input field carries a
//     description: the model reads the schema to decide how to call.
//   - Dates are strings in market.DateLayout (YYYY-MM-DD).
//   - Money is rounded to 2 decimals and share counts to 4. A field whose
//     name ends in _pct is a plain percentage: 7.5 means 7.5%.
//   - Bad input and unknown symbols come back as tool errors (IsError)
//     whose message says what to change, never as a panic.
package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Disclaimer is attached to every simulation and forecast output and
// closes the server instructions.
const Disclaimer = "Not investment advice. Every figure is computed from past prices (Yahoo Finance, unofficial and delayed) under stated assumptions; past performance does not predict future results, and the ETF universe is a current list, so historical results carry survivorship bias."

// Instructions is the server-level guidance sent to clients when they
// connect. Keep it in sync with the tool set.
const Instructions = `etf-insight-mcp answers questions about small recurring purchases (dollar-cost averaging, DCA) of US-listed ETFs: what a plan would have done, what range of outcomes it could have, what it costs, and which similar funds exist.

Data: daily prices, dividends and splits come from Yahoo Finance's unofficial chart API; fund descriptions (expense ratio, holdings, provider performance), quotes, search and news come from its quote endpoints. Prices are delayed. Price history is stored on disk once (~/Library/Caches/etf-insight-mcp) and afterwards only the newest bars are fetched; fund documents are cached for a day and quotes for 15 minutes. KRW plans use the KRW=X rate (KRW per 1 USD). Symbols outside the built-in universe work when Yahoo knows them.

Units: amount is ONE contribution in the plan currency (USD or KRW) before costs. fee_rate is a fraction of each contribution and commission_fixed a fixed amount per purchase; for small daily purchases the fixed commission is usually the largest cost. Money is rounded to 2 decimals and share counts to 4. Fields ending in _pct are plain percentages (7.5 means 7.5%). Dates are YYYY-MM-DD; timestamps are RFC 3339 UTC.

Which tool:
- Find funds: list_etfs (built-in universe), search_symbols (anything Yahoo knows), screen_universe (rank the universe by momentum, returns, volatility, drawdown, dividend yield or trend).
- Describe one fund: get_etf_info (returns, volatility, drawdowns, dividends, trend), get_fund_profile (expense ratio, assets, inception), get_holdings, get_fund_performance, get_dividends, get_splits, get_technical_indicators, get_price_history, get_quote, get_news.
- Compare: compare_etfs (side by side, with correlation and beta to a benchmark), find_alternatives (funds similar to one you hold, with cost and return differences), market_overview (major ETFs and the VIX today).
- Simulate the past: simulate_dca and simulate_portfolio_dca (with an SPY comparison on the same days), simulate_lump_sum_vs_dca, simulate_rolling_dca (the plan from every historical start date).
- Look ahead: forecast_dca, a block bootstrap of history that gives a range of outcomes, not a price prediction.
- "Is my plan reasonable?": review_dca_plan splits a plan into costs, history, a short-term and a long-term view; follow it with find_alternatives.
- Cache: cache_status (files, size, warnings), refresh_prices (refetch now), clear_cache (deletes files, needs confirm=true).
The etf://universe resource is the universe CSV, and the dca_report prompt chains get_etf_info, simulate_dca and forecast_dca into a short write-up.

Errors come back as tool errors whose message says what to change (an unknown symbol points at list_etfs, a bad date shows the expected format).

` + Disclaimer

// Deps is what the tools need from the rest of the program.
type Deps struct {
	// Source supplies price history. Required.
	Source market.Source
	// Cache is the on-disk store behind Source, when there is one. It lets
	// a multi-symbol call prefetch concurrently and lets outputs warn when
	// a symbol was served from a stale file. nil is fine (tests, or a
	// Source without a cache).
	Cache *cache.Store
	// Fund supplies fund descriptions, quotes, search and news. nil means
	// the fund tools answer with a "not configured" tool error.
	Fund market.FundSource
	// Version is reported by ping.
	Version string
	// Now returns the current time; nil means time.Now. Tests pin it.
	Now func() time.Time
}

// Register adds every tool, the universe resource and the dca_report
// prompt to s. Deps.Source must be set; Deps.Now defaults to time.Now.
func Register(s *mcp.Server, deps Deps) {
	if deps.Source == nil {
		panic("tools: Deps.Source must not be nil")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	// recoverPanics runs first so it also catches a panic in emptyArguments.
	s.AddReceivingMiddleware(recoverPanics, emptyArguments)
	registerPing(s, deps)
	registerListETFs(s)
	registerGetETFInfo(s, deps)
	registerGetPriceHistory(s, deps)
	registerSimulateDCA(s, deps)
	registerSimulatePortfolioDCA(s, deps)
	registerForecastDCA(s, deps)
	registerUniverseResource(s)
	registerDCAReportPrompt(s, deps)
	// Round-2 tool groups, each in its own file.
	deps.registerData(s)
	deps.registerAnalysis(s)
	deps.registerSimulations(s)
	deps.registerOps(s)
}

// prefetchConcurrency caps the parallel upstream fetches of one call.
const prefetchConcurrency = 4

// readOnly builds the annotations shared by every tool: none of them
// changes anything, they only read prices and compute. openWorld says
// whether the tool talks to an external system (the price source); ping
// and list_etfs do not.
func readOnly(title string, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, Title: title, OpenWorldHint: &openWorld}
}

// checkFinite rejects results that overflowed to ±Inf or NaN before the
// SDK tries to marshal them, which would otherwise surface as a protocol
// error instead of a tool error.
func checkFinite(what string, values ...float64) error {
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%s is not a finite number; reduce amount, horizon or expected_annual_return_pct", what)
		}
	}
	return nil
}

// clock returns the current time, tolerating a Deps built without Now.
func (d Deps) clock() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// round2 rounds money to two decimals.
func round2(x float64) float64 { return math.Round(x*100) / 100 }

// round4 keeps four decimals for share counts and per-share dividends,
// which are too small for cents to be meaningful.
func round4(x float64) float64 { return math.Round(x*10000) / 10000 }

// pct turns a fraction (0.075) into a percentage rounded to two decimals
// (7.5).
func pct(fraction float64) float64 { return round2(fraction * 100) }

// formatDate renders t in the wire format; a zero time renders as "".
func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(market.DateLayout)
}

// parseDate parses a required YYYY-MM-DD input field.
func parseDate(field, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required (YYYY-MM-DD)", field)
	}
	t, err := market.ParseDate(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not a date; use YYYY-MM-DD", field, value)
	}
	return t, nil
}

// parseOptionalDate is parseDate for a field that may be left empty, in
// which case the zero time is returned.
func parseOptionalDate(field, value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return parseDate(field, value)
}

// normalizeSymbol maps user input onto the upper-case ticker every
// package uses as a key.
func normalizeSymbol(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// userError strips the package prefixes from a domain error so the model
// reads "amount must be > 0" rather than "sim: amount must be > 0".
func userError(err error) error {
	msg := err.Error()
	for _, prefix := range []string{"sim: ", "analytics: ", "market: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return errors.New(msg)
}

// fetchSeries loads one symbol and turns provider errors into messages
// the model can act on. The returned series has at least one bar.
func (d Deps) fetchSeries(ctx context.Context, symbol string) (*market.Series, error) {
	sym := normalizeSymbol(symbol)
	if sym == "" {
		return nil, errors.New("symbol is required; use list_etfs to find one")
	}
	s, err := d.Source.Series(ctx, sym)
	if err != nil {
		return nil, describeFetchError(sym, err)
	}
	if s.Len() == 0 {
		return nil, fmt.Errorf("%s has no price history in the data source", sym)
	}
	return s, nil
}

// describeFetchError rewrites a Source error for the model: an unknown
// symbol points at list_etfs, a symbol that is in the universe but missing
// upstream says so, and anything else keeps its cause.
func describeFetchError(sym string, err error) error {
	switch {
	case errors.Is(err, market.ErrNotFound):
		if _, known := universe.Get(sym); known {
			return fmt.Errorf("symbol %s is in the universe but the data source returned not found; the ticker may have changed or been delisted, try search_symbols or another symbol", sym)
		}
		return fmt.Errorf("unknown symbol %s: not in universe and the data source returned not found; use list_etfs", sym)
	case errors.Is(err, cache.ErrInvalidSymbol):
		return fmt.Errorf("invalid symbol %q: use letters, digits and . - = ^ only, or find one with list_etfs", sym)
	default:
		return fmt.Errorf("fetching %s failed: %s", sym, rootCause(err))
	}
}

// rootCause renders an upstream error for a reader: the request URL and
// the repeated symbol prefixes are dropped, the cause is kept.
func rootCause(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	msg := err.Error()
	msg = fetchPrefix.ReplaceAllString(msg, "")
	return msg
}

// fetchPrefix matches the "cache: fetch X: yahoo: X: request: " chain the
// data layers prepend.
var fetchPrefix = regexp.MustCompile(`^(cache: fetch \S+: )?(yahoo: \S+: )?(request: )?`)

// fetchAll loads every symbol, warming the cache concurrently when there
// is one so a portfolio does not pay one network round trip per symbol.
// It returns the series that loaded and a descriptive error for each
// symbol that did not; callers decide which failures are fatal.
func (d Deps) fetchAll(ctx context.Context, symbols []string) (map[string]*market.Series, map[string]error) {
	syms := uniqueSymbols(symbols)
	series := make(map[string]*market.Series, len(syms))
	errs := make(map[string]error)
	if d.Cache != nil && len(syms) > 1 {
		// One concurrent pass; the series come back from the same call so a
		// symbol that could not be written to disk is not fetched twice.
		got, failed := d.Cache.Prefetch(ctx, syms, prefetchConcurrency)
		for _, sym := range syms {
			switch {
			case failed[sym] != nil:
				errs[sym] = describeFetchError(sym, failed[sym])
			case got[sym] == nil || got[sym].Len() == 0:
				errs[sym] = fmt.Errorf("%s has no price history in the data source", sym)
			default:
				series[sym] = got[sym]
			}
		}
		return series, errs
	}
	for _, sym := range syms {
		s, err := d.fetchSeries(ctx, sym)
		if err != nil {
			errs[sym] = err
			continue
		}
		series[sym] = s
	}
	return series, errs
}

// uniqueSymbols normalises symbols and drops blanks and duplicates,
// keeping first occurrences in order.
func uniqueSymbols(symbols []string) []string {
	out := make([]string, 0, len(symbols))
	seen := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		sym := normalizeSymbol(s)
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		out = append(out, sym)
	}
	return out
}

// staleWarnings reports, for sym, whether the cache served a stale file
// after an upstream failure and whether fresh data could not be cached.
// Both are empty without a cache or after a clean, persisted fetch.
//
// Call it only for symbols whose series reached the caller: a failed cold
// fetch records an upstream error too, and the wording would then claim
// that a cached file was served. Report a failed symbol's fetch error
// instead.
func (d Deps) staleWarnings(sym string) []string {
	if d.Cache == nil {
		return nil
	}
	var out []string
	if err := d.Cache.LastError(sym); err != nil {
		out = append(out, fmt.Sprintf("%s may be stale: served from the cached file because the last fetch failed (%s)", sym, rootCause(err)))
	}
	if err := d.Cache.LastWriteError(sym); err != nil {
		out = append(out, fmt.Sprintf("%s is current but could not be cached (%s); every call refetches it until the cache directory is writable", sym, rootCause(err)))
	}
	return out
}
