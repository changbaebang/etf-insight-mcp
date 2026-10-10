// Package market defines the price data contract shared by every other
// package: a daily bar series per symbol and the Source that supplies it.
//
// Keep this package dependency-free (standard library only). Data
// providers implement Source; simulations and analytics consume Series.
package market

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrNotFound is returned by a Source when the symbol is unknown.
var ErrNotFound = errors.New("market: symbol not found")

// Bar is one trading day of a symbol.
type Bar struct {
	// Date is the exchange-local trading date, normalised to UTC midnight.
	Date time.Time
	// Open, High and Low are the day's prices in the series currency; 0
	// when the provider did not report them (older cache files).
	Open, High, Low float64
	// Volume is the number of shares traded; 0 when not reported.
	Volume int64
	// Close is the closing price in the series currency, restated for later
	// splits (the provider divides closes before a split by its ratio) but
	// not adjusted for dividends.
	Close float64
	// AdjClose is the dividend- and split-adjusted close. Buying and valuing
	// with AdjClose models dividends reinvested on the pay date.
	AdjClose float64
	// Dividend is the cash dividend per share paid on this date, restated
	// for later splits like Close; 0 if none.
	Dividend float64
}

// Meta is descriptive data about a symbol as reported by the provider.
type Meta struct {
	Symbol         string
	Name           string // long name, may be empty
	Currency       string // ISO code, e.g. "USD"
	Exchange       string
	InstrumentType string // "ETF", "EQUITY", "CURRENCY", ...
	FirstTradeDate time.Time
	// Latest quote fields; zero when the provider did not report them.
	RegularMarketPrice float64
	FiftyTwoWeekHigh   float64
	FiftyTwoWeekLow    float64
	FetchedAt          time.Time
	// ProvisionalUntil is set when the last bar belongs to a trading
	// session that was still open when the series was fetched: that bar's
	// Close is an intraday price, and the session ends (the close becomes
	// final) at this time. Zero when the last bar is a settled close.
	ProvisionalUntil time.Time
}

// Series is the full daily history of one symbol.
//
// Invariants, enforced by Validate: Bars are sorted ascending by Date with
// no duplicate dates, and every Close and AdjClose is > 0.
type Series struct {
	Meta Meta
	Bars []Bar
	// Splits lists share splits in ascending date order; nil when none are
	// known. Prices are already split-adjusted.
	Splits []Split
}

// Validate checks the Series invariants.
func (s *Series) Validate() error {
	if s == nil {
		return errors.New("market: nil series")
	}
	if s.Meta.Symbol == "" {
		return errors.New("market: series has empty symbol")
	}
	for i, b := range s.Bars {
		if b.Close <= 0 || b.AdjClose <= 0 {
			return fmt.Errorf("market: %s bar %d (%s) has non-positive price", s.Meta.Symbol, i, b.Date.Format(DateLayout))
		}
		if i > 0 && !s.Bars[i-1].Date.Before(b.Date) {
			return fmt.Errorf("market: %s bars not strictly ascending at %d (%s)", s.Meta.Symbol, i, b.Date.Format(DateLayout))
		}
	}
	return nil
}

// Len returns the number of bars.
func (s *Series) Len() int { return len(s.Bars) }

// First returns the earliest bar and false if the series is empty.
func (s *Series) First() (Bar, bool) {
	if len(s.Bars) == 0 {
		return Bar{}, false
	}
	return s.Bars[0], true
}

// Last returns the latest bar and false if the series is empty.
func (s *Series) Last() (Bar, bool) {
	if len(s.Bars) == 0 {
		return Bar{}, false
	}
	return s.Bars[len(s.Bars)-1], true
}

// IndexOn returns the index of the bar on date, or of the last bar before
// it when the date is not a trading day. ok is false when every bar is
// after date.
func (s *Series) IndexOn(date time.Time) (idx int, ok bool) {
	date = Day(date)
	i := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date.After(date) })
	if i == 0 {
		return 0, false
	}
	return i - 1, true
}

// Between returns the bars with start <= Date <= end as a sub-slice of the
// original backing array. Zero start or end means unbounded.
func (s *Series) Between(start, end time.Time) []Bar {
	bars := s.Bars
	if !start.IsZero() {
		start = Day(start)
		i := sort.Search(len(bars), func(i int) bool { return !bars[i].Date.Before(start) })
		bars = bars[i:]
	}
	if !end.IsZero() {
		end = Day(end)
		i := sort.Search(len(bars), func(i int) bool { return bars[i].Date.After(end) })
		bars = bars[:i]
	}
	return bars
}

// Source supplies price history. Implementations must be safe for
// concurrent use. Unknown symbols return an error wrapping ErrNotFound.
type Source interface {
	Series(ctx context.Context, symbol string) (*Series, error)
}

// DateLayout is the wire format for dates in tool inputs and outputs.
const DateLayout = "2006-01-02"

// Day normalises t to UTC midnight of its calendar date, dropping the
// clock and location.
func Day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ParseDate parses a DateLayout string into a Day-normalised time.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("market: bad date %q (want YYYY-MM-DD): %w", s, err)
	}
	return Day(t), nil
}
