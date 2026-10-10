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
		return fmt.Sprintf("%s is an index, not an investable fund: its prices carry no dividends, fees or tracking error, so results overstate what a fund holder would get", s.Meta.Symbol)
	case "CURRENCY":
		return fmt.Sprintf("%s is an exchange rate, not an investable fund", s.Meta.Symbol)
	default:
		return ""
	}
}
