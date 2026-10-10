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

// apiError is the error object Yahoo returns in place of a result. Every
// endpoint nests it the same way under its own top-level key:
// {"chart":{"error":...}}, {"quoteSummary":{"error":...}},
// {"finance":{"error":...}}.
type apiError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// err converts the API error object into a Go error, mapping the
// "Not Found" code onto market.ErrNotFound.
func (e *apiError) err() error {
	desc := e.Description
	if desc == "" {
		desc = "no data for symbol"
	}
	if e.Code == "Not Found" {
		return fmt.Errorf("%s: %w", desc, market.ErrNotFound)
	}
	return fmt.Errorf("yahoo error %q: %s", e.Code, desc)
}

// decodeAPIError finds the error object in a Yahoo envelope of any
// endpoint, or returns nil when body is not such an envelope or carries no
// error.
func decodeAPIError(body []byte) *apiError {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	for _, raw := range env {
		var wrapper struct {
			Error *apiError `json:"error"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Error != nil {
			return wrapper.Error
		}
	}
	return nil
}

// chartResponse mirrors the subset of the v8 chart payload that is read.
// Optional fields are zero-value tolerant, so a missing or null key never
// fails decoding.
type chartResponse struct {
	Chart struct {
		Result []chartResult `json:"result"`
		Error  *apiError     `json:"error"`
	} `json:"chart"`
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
	// CurrentTradingPeriod is the current or most recent session; daily
	// bars are stamped with their session's start.
	CurrentTradingPeriod struct {
		Regular struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"regular"`
	} `json:"currentTradingPeriod"`
}

// chartEvents holds the corporate actions, each keyed by the event's unix
// timestamp as a string.
type chartEvents struct {
	Dividends map[string]chartDividend `json:"dividends"`
	Splits    map[string]chartSplit    `json:"splits"`
}

type chartDividend struct {
	Amount float64 `json:"amount"`
	Date   int64   `json:"date"`
}

type chartSplit struct {
	Date        int64   `json:"date"`
	Numerator   float64 `json:"numerator"`
	Denominator float64 `json:"denominator"`
}

type chartIndicators struct {
	Quote    []chartQuote    `json:"quote"`
	AdjClose []chartAdjClose `json:"adjclose"`
}

// chartQuote holds the raw OHLCV arrays, one entry per timestamp. Entries
// are pointers because the API emits null for days without a print.
type chartQuote struct {
	Open   []*float64 `json:"open"`
	High   []*float64 `json:"high"`
	Low    []*float64 `json:"low"`
	Close  []*float64 `json:"close"`
	Volume []*float64 `json:"volume"`
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
	bars = dropAfterSession(r.Meta, bars, loc)
	attachDividends(bars, r.Events.Dividends, loc)

	s := &market.Series{
		Meta:   parseMeta(r.Meta, loc, now),
		Bars:   bars,
		Splits: parseSplits(r.Events.Splits, loc),
	}
	s.Meta.ProvisionalUntil = provisionalUntil(r.Meta, bars, loc, now)
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// parseBars pairs each timestamp with its prices, dropping days where the
// close or adjusted close is null or non-positive. Open, high, low and
// volume are 0 when not reported. A timestamp that falls on the same
// local date as the previous bar replaces it, so the latest print for a
// day wins.
func parseBars(r chartResult, loc *time.Location) ([]market.Bar, error) {
	quote, adjs, err := priceArrays(r.Indicators)
	if err != nil {
		return nil, err
	}
	n := len(r.Timestamp)
	if len(quote.Close) != n || len(adjs) != n {
		return nil, fmt.Errorf("chart arrays disagree: %d timestamps, %d closes, %d adjcloses",
			n, len(quote.Close), len(adjs))
	}
	bars := make([]market.Bar, 0, n)
	for i, ts := range r.Timestamp {
		c, a := quote.Close[i], adjs[i]
		if c == nil || a == nil || *c <= 0 || *a <= 0 {
			continue
		}
		bar := market.Bar{
			Date:     localDay(ts, loc),
			Open:     at(quote.Open, i),
			High:     at(quote.High, i),
			Low:      at(quote.Low, i),
			Volume:   int64(at(quote.Volume, i)),
			Close:    *c,
			AdjClose: *a,
		}
		if k := len(bars); k > 0 && bars[k-1].Date.Equal(bar.Date) {
			bars[k-1] = bar
			continue
		}
		bars = append(bars, bar)
	}
	return bars, nil
}

// priceArrays extracts the OHLCV block and the adjclose array. When the
// payload omits the adjclose block, the raw close stands in for it.
func priceArrays(ind chartIndicators) (quote chartQuote, adjs []*float64, err error) {
	if len(ind.Quote) == 0 {
		return chartQuote{}, nil, errors.New("chart has no quote indicator")
	}
	quote = ind.Quote[0]
	adjs = quote.Close
	if len(ind.AdjClose) > 0 && ind.AdjClose[0].AdjClose != nil {
		adjs = ind.AdjClose[0].AdjClose
	}
	return quote, adjs, nil
}

// at returns the i-th entry of a nullable array, or 0 when the entry is
// null or the array is shorter than the timestamps.
func at(arr []*float64, i int) float64 {
	if i >= len(arr) || arr[i] == nil {
		return 0
	}
	return *arr[i]
}

// attachDividends adds each cash dividend to the bar on its exchange-local
// date, or to the next bar on or after it when that date had no bar.
// Dividends dated after the last bar are dropped.
func attachDividends(bars []market.Bar, dividends map[string]chartDividend, loc *time.Location) {
	for key, d := range dividends {
		if d.Amount <= 0 {
			continue
		}
		ts, ok := eventTime(key, d.Date)
		if !ok {
			continue
		}
		date := localDay(ts, loc)
		i := sort.Search(len(bars), func(i int) bool { return !bars[i].Date.Before(date) })
		if i < len(bars) {
			bars[i].Dividend += d.Amount
		}
	}
}

// parseSplits converts the split events into market.Splits in ascending
// date order, using the same exchange-local date conversion as dividends.
// Splits with a non-positive ratio are dropped; nil is returned when none
// remain.
func parseSplits(splits map[string]chartSplit, loc *time.Location) []market.Split {
	out := make([]market.Split, 0, len(splits))
	for key, sp := range splits {
		if sp.Numerator <= 0 || sp.Denominator <= 0 {
			continue
		}
		ts, ok := eventTime(key, sp.Date)
		if !ok {
			continue
		}
		out = append(out, market.Split{Date: localDay(ts, loc), Numerator: sp.Numerator, Denominator: sp.Denominator})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// eventTime returns the unix time of an event: its date field, or the map
// key when the field is missing, which some payloads do. ok is false when
// neither is usable.
func eventTime(key string, date int64) (ts int64, ok bool) {
	if date != 0 {
		return date, true
	}
	parsed, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
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

// dropAfterSession removes bars dated after the most recent regular
// session (currentTradingPeriod.regular). Yahoo appends such a bar for a
// market it keeps quoting outside its sessions, for example KRW=X on a
// Saturday: the bar holds a live quote rather than a session's close, and
// the next session's bar supersedes it. Without session data nothing is
// dropped.
func dropAfterSession(m chartMeta, bars []market.Bar, loc *time.Location) []market.Bar {
	start := m.CurrentTradingPeriod.Regular.Start
	if start == 0 {
		return bars
	}
	sessionDay := localDay(start, loc)
	n := len(bars)
	for n > 0 && bars[n-1].Date.After(sessionDay) {
		n--
	}
	return bars[:n]
}

// provisionalUntil returns the end of the regular session when the last
// bar belongs to a session that had not ended at now, so its close is an
// intraday price; otherwise zero. The session comes from the chart's
// currentTradingPeriod, whose regular start is the stamp of that day's bar.
func provisionalUntil(m chartMeta, bars []market.Bar, loc *time.Location, now time.Time) time.Time {
	session := m.CurrentTradingPeriod.Regular
	if len(bars) == 0 || session.End == 0 || session.Start == 0 {
		return time.Time{}
	}
	end := time.Unix(session.End, 0).UTC()
	if !now.Before(end) {
		return time.Time{}
	}
	if !bars[len(bars)-1].Date.Equal(localDay(session.Start, loc)) {
		return time.Time{}
	}
	return end
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
