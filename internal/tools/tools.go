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
const Instructions = `etf-insight-mcp simulates small recurring purchases (dollar-cost averaging, DCA) of US-listed ETFs and projects the range of outcomes such a plan could have.

Data: daily closes, adjusted closes and dividends come from Yahoo Finance's unofficial chart API. Quotes are delayed and each symbol is cached on disk (6 hours by default), so numbers can lag the market by up to a day. KRW plans use the KRW=X rate (KRW per 1 USD) from the same source. Symbols outside the built-in universe work too when Yahoo knows them.

Units: amount is the size of ONE contribution in the plan currency (USD or KRW) before fees, e.g. 100 USD every trading day or 300000 KRW every month. Money fields are rounded to 2 decimals and share counts to 4. Every field ending in _pct is a plain percentage (7.5 means 7.5%). Dates are YYYY-MM-DD.

Typical flow: list_etfs to find candidates; get_etf_info for a snapshot (trailing returns, volatility, drawdowns, dividends, trend); simulate_dca or simulate_portfolio_dca to see what the plan would have done, with SPY as the default baseline; forecast_dca for the outcome range a block bootstrap of history gives. get_price_history returns the bars when a chart or a custom calculation is needed. The etf://universe resource is the universe CSV and the dca_report prompt chains the three main tools into a short write-up.

Errors are returned as tool errors whose message says what to change (an unknown symbol points at list_etfs, a bad date shows the expected format).

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
	registerPing(s, deps)
	registerListETFs(s)
	registerGetETFInfo(s, deps)
	registerGetPriceHistory(s, deps)
	registerSimulateDCA(s, deps)
	registerSimulatePortfolioDCA(s, deps)
	registerForecastDCA(s, deps)
	registerUniverseResource(s)
	registerDCAReportPrompt(s, deps)
}

// prefetchConcurrency caps the parallel upstream fetches of one call.
const prefetchConcurrency = 4

// readOnly builds the annotations shared by every tool: none of them
// changes anything, they only read prices and compute.
func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, Title: title}
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
			return fmt.Errorf("symbol %s is in the universe but the data source returned not found; try again later: %w", sym, err)
		}
		return fmt.Errorf("unknown symbol %s: not in universe and the data source returned not found; use list_etfs", sym)
	case errors.Is(err, cache.ErrInvalidSymbol):
		return fmt.Errorf("invalid symbol %q: use letters, digits and . - = ^ only, or find one with list_etfs", sym)
	default:
		return fmt.Errorf("fetching %s failed: %w", sym, err)
	}
}

// fetchAll loads every symbol, warming the cache concurrently when there
// is one so a portfolio does not pay one network round trip per symbol.
// It returns the series that loaded and a descriptive error for each
// symbol that did not; callers decide which failures are fatal.
func (d Deps) fetchAll(ctx context.Context, symbols []string) (map[string]*market.Series, map[string]error) {
	syms := uniqueSymbols(symbols)
	var failed map[string]error
	if d.Cache != nil && len(syms) > 1 {
		failed = cache.Prefetch(ctx, d.Source, syms, prefetchConcurrency)
	}
	series := make(map[string]*market.Series, len(syms))
	errs := make(map[string]error)
	for _, sym := range syms {
		if err, ok := failed[sym]; ok {
			errs[sym] = describeFetchError(sym, err)
			continue
		}
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

// staleWarning reports when the cache served sym although its last
// upstream fetch did not fully succeed (stale file served, or the fresh
// series could not be written), so a reader knows the numbers may lag.
// It is empty without a cache or after a clean fetch.
func (d Deps) staleWarning(sym string) string {
	if d.Cache == nil {
		return ""
	}
	err := d.Cache.LastError(sym)
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s may be stale: the last fetch from the data source did not fully succeed (%v)", sym, err)
}
