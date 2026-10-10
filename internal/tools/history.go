package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// trailingYearSpan is how far back the first bar must reach for a
// "1-year" or trailing-twelve-month figure to cover a full year: 52 weeks.
// analytics measures its trailing-year windows over the same span.
const trailingYearSpan = 364 * 24 * time.Hour

// hasTrailingYear reports whether s reaches back a full 52 weeks from the
// bar it is read at: the last bar on or before asOf, or the last bar when
// asOf is zero. When it does not, 1-year volatility, 1-year drawdown and
// TTM dividend figures cover only the available history and must not be
// presented as full-year values.
func hasTrailingYear(s *market.Series, asOf time.Time) bool {
	first, ok := s.First()
	if !ok {
		return false
	}
	anchor, ok := s.Last()
	if !ok {
		return false
	}
	if !asOf.IsZero() {
		i, found := s.IndexOn(asOf)
		if !found {
			return false
		}
		anchor = s.Bars[i]
	}
	return anchor.Date.Sub(first.Date) >= trailingYearSpan
}

// requireUSD rejects a series quoted in a currency other than USD. The
// simulation, forecast and comparison engines treat every price as US
// dollars (and convert KRW plans through KRW=X), so a KRW- or GBP-quoted
// listing would be silently mispriced. Only fund symbols are checked; the
// KRW=X exchange-rate series is quoted in KRW by design.
func requireUSD(s *market.Series) error {
	cur := strings.ToUpper(strings.TrimSpace(s.Meta.Currency))
	if cur == "" || cur == "USD" {
		return nil
	}
	return fmt.Errorf("%s is quoted in %s; this tool handles US-listed funds quoted in USD only (find the US listing with search_symbols or list_etfs)", s.Meta.Symbol, cur)
}

// instrumentNote returns a caution for a series that is not an investable
// fund, such as an index (^GSPC) or an exchange rate, and "" for ETFs,
// mutual funds and unknown types.
func instrumentNote(s *market.Series) string {
	switch strings.ToUpper(strings.TrimSpace(s.Meta.InstrumentType)) {
	case "INDEX":
		return fmt.Sprintf("%s is an index, not an investable fund: unless it is a total-return index its prices leave out dividends, so returns usually understate what a fund tracking it delivered, and it bears no fees or tracking error; use a fund that tracks it for investable figures", s.Meta.Symbol)
	case "CURRENCY":
		return fmt.Sprintf("%s is an exchange rate, not an investable fund", s.Meta.Symbol)
	default:
		return ""
	}
}

// provisionalNote says that s's last bar is an intraday price, when the
// figures the caller reports rely on it: used is the last date they read,
// zero meaning the last bar. It is "" when the last bar is a settled close
// or is not used.
func provisionalNote(s *market.Series, used time.Time) string {
	if s == nil {
		return ""
	}
	until := s.Meta.ProvisionalUntil
	last, ok := s.Last()
	if until.IsZero() || !ok || (!used.IsZero() && used.Before(last.Date)) {
		return ""
	}
	return fmt.Sprintf("%s's latest bar (%s) is an intraday price fetched at %s, while that session was still open, not the close; the cache fetches the settled close once the session ends at %s",
		s.Meta.Symbol, formatDate(last.Date), s.Meta.FetchedAt.UTC().Format("2006-01-02 15:04 UTC"), until.UTC().Format("15:04 UTC"))
}

// provisionalSummary is provisionalNote for a multi-symbol answer: one
// line naming every symbol of symbols whose latest bar is intraday, or ""
// when none is.
func provisionalSummary(series map[string]*market.Series, symbols []string) string {
	var intraday []string
	for _, sym := range symbols {
		if s := series[sym]; s != nil && !s.Meta.ProvisionalUntil.IsZero() {
			intraday = append(intraday, sym)
		}
	}
	if len(intraday) == 0 {
		return ""
	}
	return fmt.Sprintf("latest bars of %s are intraday prices fetched while the session was still open, not closes; the cache fetches the settled closes once the session ends", strings.Join(intraday, ", "))
}

// nonEmpty returns the non-empty strings of notes, for appending optional
// notes to a list.
func nonEmpty(notes ...string) []string {
	var out []string
	for _, n := range notes {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}
