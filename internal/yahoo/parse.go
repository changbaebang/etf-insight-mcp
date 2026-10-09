package yahoo

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// defaultLocation is used when the response names no usable time zone.
// Nearly every symbol this project cares about trades in New York.
const defaultLocation = "America/New_York"

// chartResponse mirrors the subset of the v8 chart payload that is read.
// Optional fields are zero-value tolerant, so a missing or null key never
// fails decoding.
type chartResponse struct {
	Chart struct {
		Result []chartResult `json:"result"`
		Error  *chartError   `json:"error"`
	} `json:"chart"`
}

// chartError is the error object Yahoo returns in place of a result.
type chartError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// err converts the API error object into a Go error, mapping the
// "Not Found" code onto market.ErrNotFound.
func (e *chartError) err() error {
	desc := e.Description
	if desc == "" {
		desc = "no data for symbol"
	}
	if e.Code == "Not Found" {
		return fmt.Errorf("%s: %w", desc, market.ErrNotFound)
	}
	return fmt.Errorf("chart error %q: %s", e.Code, desc)
}

type chartResult struct {
	Meta       chartMeta       `json:"meta"`
	Timestamp  []int64         `json:"timestamp"`
	Events     chartEvents     `json:"events"`
	Indicators chartIndicators `json:"indicators"`
}

type chartMeta struct {
	Currency             string  `json:"currency"`
	Symbol               string  `json:"symbol"`
	ExchangeName         string  `json:"exchangeName"`
	FullExchangeName     string  `json:"fullExchangeName"`
	InstrumentType       string  `json:"instrumentType"`
	FirstTradeDate       int64   `json:"firstTradeDate"`
	ExchangeTimezoneName string  `json:"exchangeTimezoneName"`
	RegularMarketPrice   float64 `json:"regularMarketPrice"`
	FiftyTwoWeekHigh     float64 `json:"fiftyTwoWeekHigh"`
	FiftyTwoWeekLow      float64 `json:"fiftyTwoWeekLow"`
	LongName             string  `json:"longName"`
	ShortName            string  `json:"shortName"`
}

type chartEvents struct {
	// Dividends is keyed by the event's unix timestamp as a string.
	Dividends map[string]chartDividend `json:"dividends"`
}

type chartDividend struct {
	Amount float64 `json:"amount"`
	Date   int64   `json:"date"`
}

type chartIndicators struct {
	Quote    []chartQuote    `json:"quote"`
	AdjClose []chartAdjClose `json:"adjclose"`
}

// chartQuote holds the raw price arrays. Only Close is used. Entries are
// pointers because the API emits null for days without a print.
type chartQuote struct {
	Close []*float64 `json:"close"`
}

type chartAdjClose struct {
	AdjClose []*float64 `json:"adjclose"`
}

// parseChart converts a raw chart payload into a validated market.Series.
// now is recorded as Meta.FetchedAt. It is a pure function so tests can
// feed fixtures directly.
func parseChart(body []byte, now time.Time) (*market.Series, error) {
	var resp chartResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode chart: %w", err)
	}
	if resp.Chart.Error != nil {
		return nil, resp.Chart.Error.err()
	}
	if len(resp.Chart.Result) == 0 {
		return nil, errors.New("chart has no result")
	}
	r := resp.Chart.Result[0]
	if r.Meta.Symbol == "" {
		return nil, errors.New("chart meta has no symbol")
	}
	loc := loadLocation(r.Meta.ExchangeTimezoneName)

	bars, err := parseBars(r, loc)
	if err != nil {
		return nil, err
	}
	attachDividends(bars, r.Events.Dividends, loc)

	s := &market.Series{
		Meta: parseMeta(r.Meta, loc, now),
		Bars: bars,
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// parseBars pairs each timestamp with its close and adjusted close,
// dropping days where either is null or non-positive. A timestamp that
// falls on the same local date as the previous bar replaces it, so the
// latest print for a day wins.
func parseBars(r chartResult, loc *time.Location) ([]market.Bar, error) {
	closes, adjs, err := priceArrays(r.Indicators)
	if err != nil {
		return nil, err
	}
	n := len(r.Timestamp)
	if len(closes) != n || len(adjs) != n {
		return nil, fmt.Errorf("chart arrays disagree: %d timestamps, %d closes, %d adjcloses",
			n, len(closes), len(adjs))
	}
	bars := make([]market.Bar, 0, n)
	for i, ts := range r.Timestamp {
		c, a := closes[i], adjs[i]
		if c == nil || a == nil || *c <= 0 || *a <= 0 {
			continue
		}
		bar := market.Bar{Date: localDay(ts, loc), Close: *c, AdjClose: *a}
		if k := len(bars); k > 0 && bars[k-1].Date.Equal(bar.Date) {
			bars[k-1] = bar
			continue
		}
		bars = append(bars, bar)
	}
	return bars, nil
}

// priceArrays extracts the close and adjclose arrays. When the payload
// omits the adjclose block, the raw close stands in for it.
func priceArrays(ind chartIndicators) (closes, adjs []*float64, err error) {
	if len(ind.Quote) == 0 {
		return nil, nil, errors.New("chart has no quote indicator")
	}
	closes = ind.Quote[0].Close
	adjs = closes
	if len(ind.AdjClose) > 0 && ind.AdjClose[0].AdjClose != nil {
		adjs = ind.AdjClose[0].AdjClose
	}
	return closes, adjs, nil
}

// attachDividends adds each cash dividend to the bar on its exchange-local
// date, or to the next bar on or after it when that date had no bar.
// Dividends dated after the last bar are dropped.
func attachDividends(bars []market.Bar, dividends map[string]chartDividend, loc *time.Location) {
	for key, d := range dividends {
		if d.Amount <= 0 {
			continue
		}
		ts := d.Date
		if ts == 0 {
			// Some payloads carry the timestamp only in the map key.
			parsed, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				continue
			}
			ts = parsed
		}
		date := localDay(ts, loc)
		i := sort.Search(len(bars), func(i int) bool { return !bars[i].Date.Before(date) })
		if i < len(bars) {
			bars[i].Dividend += d.Amount
		}
	}
}

// parseMeta copies the descriptive fields, preferring the long name and
// the full exchange name when both forms are present.
func parseMeta(m chartMeta, loc *time.Location, now time.Time) market.Meta {
	meta := market.Meta{
		Symbol:             m.Symbol,
		Name:               m.LongName,
		Currency:           m.Currency,
		Exchange:           m.FullExchangeName,
		InstrumentType:     m.InstrumentType,
		RegularMarketPrice: m.RegularMarketPrice,
		FiftyTwoWeekHigh:   m.FiftyTwoWeekHigh,
		FiftyTwoWeekLow:    m.FiftyTwoWeekLow,
		FetchedAt:          now,
	}
	if meta.Name == "" {
		meta.Name = m.ShortName
	}
	if meta.Exchange == "" {
		meta.Exchange = m.ExchangeName
	}
	if m.FirstTradeDate != 0 { // negative = before 1970, still a real date
		meta.FirstTradeDate = localDay(m.FirstTradeDate, loc)
	}
	return meta
}

// localDay converts a unix timestamp to the calendar date it falls on in
// loc, normalized with market.Day.
func localDay(unix int64, loc *time.Location) time.Time {
	return market.Day(time.Unix(unix, 0).In(loc))
}

// loadLocation resolves an IANA zone name, falling back to New York and
// then UTC so a missing tz database never fails a fetch.
func loadLocation(name string) *time.Location {
	for _, candidate := range []string{name, defaultLocation} {
		if candidate == "" {
			continue
		}
		if loc, err := time.LoadLocation(candidate); err == nil {
			return loc
		}
	}
	return time.UTC
}
