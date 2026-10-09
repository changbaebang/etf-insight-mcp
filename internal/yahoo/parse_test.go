package yahoo

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// fixtureNow is the fetch time stamped on fixtures in these tests.
var fixtureNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/spy_sample.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := market.ParseDate(s)
	if err != nil {
		t.Fatalf("bad test date %q: %v", s, err)
	}
	return d
}

// utc returns the unix time of the given UTC wall clock.
func utc(year int, month time.Month, day, hour, minute int) int64 {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC).Unix()
}

// nyOpen returns the unix time of 09:30 New York daylight time on the given
// date, which is how Yahoo stamps US daily bars in summer.
func nyOpen(year int, month time.Month, day int) int64 {
	return utc(year, month, day, 13, 30)
}

func f(v float64) *float64 { return &v }

// chartSpec builds a chart body for table tests. Price arrays use nil for
// JSON null; a nil adjs slice omits the adjclose block, a nil OHLCV slice
// omits that array and a nil events map omits the events block.
type chartSpec struct {
	meta                    map[string]any
	ts                      []int64
	closes, adjs            []any
	open, high, low, volume []any
	events                  map[string]any
}

func (sp chartSpec) body() string {
	quote := map[string]any{"close": sp.closes}
	for name, arr := range map[string][]any{"open": sp.open, "high": sp.high, "low": sp.low, "volume": sp.volume} {
		if arr != nil {
			quote[name] = arr
		}
	}
	indicators := map[string]any{"quote": []any{quote}}
	if sp.adjs != nil {
		indicators["adjclose"] = []any{map[string]any{"adjclose": sp.adjs}}
	}
	result := map[string]any{"meta": sp.meta, "timestamp": sp.ts, "indicators": indicators}
	if sp.events != nil {
		result["events"] = sp.events
	}
	b, err := json.Marshal(map[string]any{
		"chart": map[string]any{"result": []any{result}, "error": nil},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// payload is the dividend-only shorthand for chartSpec: a nil dividends
// map omits the events block.
func payload(meta map[string]any, ts []int64, closes, adjs []any, dividends map[string]any) string {
	sp := chartSpec{meta: meta, ts: ts, closes: closes, adjs: adjs}
	if dividends != nil {
		sp.events = map[string]any{"dividends": dividends}
	}
	return sp.body()
}

// split builds a split event object.
func split(ts int64, num, den float64) map[string]any {
	return map[string]any{"date": ts, "numerator": num, "denominator": den, "splitRatio": "x"}
}

func nyMeta() map[string]any {
	return map[string]any{"symbol": "TST", "exchangeTimezoneName": "America/New_York"}
}

func TestParseChartFixture(t *testing.T) {
	s, err := parseChart(loadFixture(t), fixtureNow)
	if err != nil {
		t.Fatalf("parseChart: %v", err)
	}

	wantDates := []string{
		"2026-09-11", "2026-09-14", // 2026-09-15 has a null close and is skipped
		"2026-09-16", "2026-09-17", "2026-09-18", "2026-09-21",
		"2026-09-22", "2026-09-23", "2026-09-24",
	}
	if got := len(s.Bars); got != len(wantDates) {
		t.Fatalf("got %d bars, want %d", got, len(wantDates))
	}
	for i, want := range wantDates {
		if got := s.Bars[i].Date; !got.Equal(date(t, want)) {
			t.Errorf("bar %d date = %s, want %s", i, got.Format(market.DateLayout), want)
		}
	}

	first := s.Bars[0]
	if first.Close != 764.2899780273438 || first.AdjClose != 762.3967895507812 {
		t.Errorf("first bar = %+v, want close 764.2899780273438 adj 762.3967895507812", first)
	}
	if first.Open != 764.719970703125 || first.High != 766.3800048828125 || first.Low != 763.5999755859375 || first.Volume != 45512700 {
		t.Errorf("first bar OHLV = %v %v %v %v, want 764.719970703125 766.3800048828125 763.5999755859375 45512700",
			first.Open, first.High, first.Low, first.Volume)
	}
	// Bar 2 is 2026-09-16, the fourth timestamp: OHLCV must stay aligned
	// with the timestamps even though the third day was skipped.
	if b := s.Bars[2]; b.Open != 759.5 || b.High != 761.6699829101562 || b.Low != 749.5999755859375 || b.Volume != 59217700 {
		t.Errorf("bar 2 OHLV = %v %v %v %v, want 759.5 761.6699829101562 749.5999755859375 59217700", b.Open, b.High, b.Low, b.Volume)
	}
	for i, b := range s.Bars {
		want := 0.0
		if i == 4 { // 2026-09-18 pays the quarterly dividend
			want = 1.889
		}
		if b.Dividend != want {
			t.Errorf("bar %d (%s) dividend = %v, want %v", i, b.Date.Format(market.DateLayout), b.Dividend, want)
		}
	}
	if s.Splits != nil {
		t.Errorf("Splits = %+v, want nil when the payload has none", s.Splits)
	}

	meta := s.Meta
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Symbol", meta.Symbol, "SPY"},
		{"Name", meta.Name, "State Street SPDR S&P 500 ETF Trust"},
		{"Currency", meta.Currency, "USD"},
		{"Exchange", meta.Exchange, "NYSEArca"},
		{"InstrumentType", meta.InstrumentType, "ETF"},
		{"FirstTradeDate", meta.FirstTradeDate.Format(market.DateLayout), "1993-01-29"},
		{"RegularMarketPrice", meta.RegularMarketPrice, 777.22},
		{"FiftyTwoWeekHigh", meta.FiftyTwoWeekHigh, 781.62},
		{"FiftyTwoWeekLow", meta.FiftyTwoWeekLow, 629.28},
		{"FetchedAt", meta.FetchedAt.Equal(fixtureNow), true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("meta.%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseChartSplitsFixture(t *testing.T) {
	s, err := parseChart(readFixture(t, "tqqq_splits.json"), fixtureNow)
	if err != nil {
		t.Fatalf("parseChart: %v", err)
	}
	if s.Meta.Symbol != "TQQQ" || s.Meta.Exchange != "NasdaqGM" || s.Meta.Name != "ProShares UltraPro QQQ" {
		t.Errorf("meta = %+v", s.Meta)
	}
	wantDates := []string{"2022-01-10", "2022-01-11", "2022-01-12", "2022-01-13", "2022-01-14", "2022-01-18", "2022-01-19"}
	if got := len(s.Bars); got != len(wantDates) {
		t.Fatalf("got %d bars, want %d", got, len(wantDates))
	}
	for i, want := range wantDates {
		if got := s.Bars[i].Date; !got.Equal(date(t, want)) {
			t.Errorf("bar %d date = %s, want %s", i, got.Format(market.DateLayout), want)
		}
	}
	wantSplitDay := market.Bar{
		Date: date(t, "2022-01-13"), Open: 38.564998626708984, High: 38.775001525878906, Low: 35.0099983215332,
		Volume: 173531800, Close: 35.400001525878906, AdjClose: 33.66878890991211,
	}
	if got := s.Bars[3]; !reflect.DeepEqual(got, wantSplitDay) {
		t.Errorf("split-day bar = %+v\nwant %+v", got, wantSplitDay)
	}
	wantSplits := []market.Split{{Date: date(t, "2022-01-13"), Numerator: 2, Denominator: 1}}
	if !reflect.DeepEqual(s.Splits, wantSplits) {
		t.Errorf("Splits = %+v, want %+v", s.Splits, wantSplits)
	}
	if got := s.Splits[0].Ratio(); got != "2:1" {
		t.Errorf("Ratio = %q, want 2:1", got)
	}
}

func TestParseChartErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr error // when non-nil, errors.Is must hold
	}{
		{
			name:    "not found error object",
			body:    `{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`,
			wantErr: market.ErrNotFound,
		},
		{
			name: "other error object",
			body: `{"chart":{"result":null,"error":{"code":"Bad Request","description":"nope"}}}`,
		},
		{
			name: "empty result",
			body: `{"chart":{"result":[],"error":null}}`,
		},
		{
			name: "invalid json",
			body: `{"chart":`,
		},
		{
			name: "missing symbol",
			body: payload(map[string]any{}, []int64{nyOpen(2026, 9, 14)}, []any{f(1)}, []any{f(1)}, nil),
		},
		{
			name: "array length mismatch",
			body: payload(nyMeta(), []int64{nyOpen(2026, 9, 14), nyOpen(2026, 9, 15)}, []any{f(1)}, []any{f(1)}, nil),
		},
		{
			name: "no quote indicator",
			body: `{"chart":{"result":[{"meta":{"symbol":"TST"},"timestamp":[1],"indicators":{"quote":[]}}],"error":null}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := parseChart([]byte(tt.body), fixtureNow)
			if err == nil {
				t.Fatalf("parseChart returned %+v, want error", s)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error %q does not wrap %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && errors.Is(err, market.ErrNotFound) {
				t.Errorf("error %q wrongly wraps ErrNotFound", err)
			}
		})
	}
}

func TestParseChartShapes(t *testing.T) {
	mon, tue, wed := nyOpen(2026, 9, 14), nyOpen(2026, 9, 15), nyOpen(2026, 9, 16)
	sat := utc(2026, 9, 12, 4, 0) // Saturday 00:00 New York
	twoDays := chartSpec{meta: nyMeta(), ts: []int64{mon, tue}, closes: []any{f(1), f(1)}, adjs: []any{f(1), f(1)}}
	tests := []struct {
		name  string
		body  string
		check func(t *testing.T, s *market.Series)
	}{
		{
			name: "null and non-positive prices are skipped",
			body: payload(nyMeta(), []int64{mon, tue, wed, nyOpen(2026, 9, 17)},
				[]any{nil, f(2), f(3), f(4)}, []any{f(1), nil, f(0), f(4)}, nil),
			check: func(t *testing.T, s *market.Series) {
				if len(s.Bars) != 1 || !s.Bars[0].Date.Equal(date(t, "2026-09-17")) {
					t.Errorf("bars = %+v, want only 2026-09-17", s.Bars)
				}
			},
		},
		{
			name: "missing adjclose block falls back to close",
			body: payload(nyMeta(), []int64{mon}, []any{f(12.5)}, nil, nil),
			check: func(t *testing.T, s *market.Series) {
				if len(s.Bars) != 1 || s.Bars[0].AdjClose != 12.5 {
					t.Errorf("bars = %+v, want adjclose 12.5", s.Bars)
				}
			},
		},
		{
			name: "ohlcv arrays fill the bar",
			body: chartSpec{meta: nyMeta(), ts: []int64{mon, tue}, closes: []any{f(10), f(11)}, adjs: []any{f(10), f(11)},
				open: []any{f(9), f(10.5)}, high: []any{f(12), f(13)}, low: []any{f(8), f(9.5)}, volume: []any{f(1000), f(2000.0)}}.body(),
			check: func(t *testing.T, s *market.Series) {
				want := market.Bar{Date: date(t, "2026-09-15"), Open: 10.5, High: 13, Low: 9.5, Volume: 2000, Close: 11, AdjClose: 11}
				if !reflect.DeepEqual(s.Bars[1], want) {
					t.Errorf("bar = %+v, want %+v", s.Bars[1], want)
				}
			},
		},
		{
			name: "null ohlcv entries become zero",
			body: chartSpec{meta: nyMeta(), ts: []int64{mon}, closes: []any{f(10)}, adjs: []any{f(10)},
				open: []any{nil}, high: []any{nil}, low: []any{nil}, volume: []any{nil}}.body(),
			check: func(t *testing.T, s *market.Series) {
				if b := s.Bars[0]; b.Open != 0 || b.High != 0 || b.Low != 0 || b.Volume != 0 || b.Close != 10 {
					t.Errorf("bar = %+v, want zero OHLV with close 10", b)
				}
			},
		},
		{
			name: "missing or short ohlcv arrays are tolerated",
			body: chartSpec{meta: nyMeta(), ts: []int64{mon, tue}, closes: []any{f(10), f(11)}, adjs: []any{f(10), f(11)},
				open: []any{f(9)}, volume: []any{}}.body(),
			check: func(t *testing.T, s *market.Series) {
				if len(s.Bars) != 2 || s.Bars[0].Open != 9 || s.Bars[1].Open != 0 || s.Bars[1].High != 0 || s.Bars[1].Volume != 0 {
					t.Errorf("bars = %+v, want open 9 then zeros", s.Bars)
				}
			},
		},
		{
			name: "dividend on a trading day attaches to that bar",
			body: payload(nyMeta(), []int64{mon, tue}, []any{f(1), f(1)}, []any{f(1), f(1)},
				map[string]any{"1789479000": map[string]any{"amount": 0.5, "date": tue}}),
			check: func(t *testing.T, s *market.Series) {
				if s.Bars[0].Dividend != 0 || s.Bars[1].Dividend != 0.5 {
					t.Errorf("dividends = %v,%v want 0,0.5", s.Bars[0].Dividend, s.Bars[1].Dividend)
				}
			},
		},
		{
			name: "dividend on a non-trading day attaches to the next bar",
			body: payload(nyMeta(), []int64{mon, tue}, []any{f(1), f(1)}, []any{f(1), f(1)},
				map[string]any{"x": map[string]any{"amount": 0.25, "date": sat}}),
			check: func(t *testing.T, s *market.Series) {
				if s.Bars[0].Dividend != 0.25 || s.Bars[1].Dividend != 0 {
					t.Errorf("dividends = %v,%v want 0.25,0", s.Bars[0].Dividend, s.Bars[1].Dividend)
				}
			},
		},
		{
			name: "dividend after the last bar is dropped",
			body: payload(nyMeta(), []int64{mon}, []any{f(1)}, []any{f(1)},
				map[string]any{"x": map[string]any{"amount": 0.25, "date": wed}}),
			check: func(t *testing.T, s *market.Series) {
				if s.Bars[0].Dividend != 0 {
					t.Errorf("dividend = %v, want 0", s.Bars[0].Dividend)
				}
			},
		},
		{
			name: "dividend without date uses the map key",
			body: payload(nyMeta(), []int64{mon}, []any{f(1)}, []any{f(1)},
				map[string]any{"1789392600": map[string]any{"amount": 0.75}}),
			check: func(t *testing.T, s *market.Series) {
				if s.Bars[0].Dividend != 0.75 {
					t.Errorf("dividend = %v, want 0.75", s.Bars[0].Dividend)
				}
			},
		},
		{
			name: "splits are sorted by date and keep dividends",
			body: func() string {
				sp := twoDays
				sp.events = map[string]any{
					"dividends": map[string]any{"a": map[string]any{"amount": 0.5, "date": tue}},
					"splits":    map[string]any{"b": split(tue, 3, 1), "a": split(mon, 2, 1)},
				}
				return sp.body()
			}(),
			check: func(t *testing.T, s *market.Series) {
				want := []market.Split{
					{Date: date(t, "2026-09-14"), Numerator: 2, Denominator: 1},
					{Date: date(t, "2026-09-15"), Numerator: 3, Denominator: 1},
				}
				if !reflect.DeepEqual(s.Splits, want) {
					t.Errorf("Splits = %+v, want %+v", s.Splits, want)
				}
				if s.Bars[1].Dividend != 0.5 {
					t.Errorf("dividend = %v, want 0.5 alongside the splits", s.Bars[1].Dividend)
				}
			},
		},
		{
			name: "split without date uses the map key and keeps its own calendar date",
			body: func() string {
				sp := twoDays
				sp.events = map[string]any{"splits": map[string]any{
					strconv.FormatInt(sat, 10): map[string]any{"numerator": 1, "denominator": 4}, // Saturday; a reverse split
				}}
				return sp.body()
			}(),
			check: func(t *testing.T, s *market.Series) {
				want := []market.Split{{Date: date(t, "2026-09-12"), Numerator: 1, Denominator: 4}}
				if !reflect.DeepEqual(s.Splits, want) {
					t.Errorf("Splits = %+v, want %+v (not snapped to a bar)", s.Splits, want)
				}
				if s.Splits[0].Ratio() != "1:4" {
					t.Errorf("Ratio = %q, want 1:4", s.Splits[0].Ratio())
				}
			},
		},
		{
			name: "splits with a non-positive ratio or unusable date are dropped",
			body: func() string {
				sp := twoDays
				sp.events = map[string]any{"splits": map[string]any{
					"a": split(mon, 0, 1), "b": split(mon, 2, 0), "nokey": map[string]any{"numerator": 2, "denominator": 1},
				}}
				return sp.body()
			}(),
			check: func(t *testing.T, s *market.Series) {
				if s.Splits != nil {
					t.Errorf("Splits = %+v, want nil", s.Splits)
				}
			},
		},
		{
			name: "two timestamps on one local date keep the later print",
			body: payload(nyMeta(), []int64{mon, mon + 3600}, []any{f(1), f(2)}, []any{f(1), f(2)}, nil),
			check: func(t *testing.T, s *market.Series) {
				if len(s.Bars) != 1 || s.Bars[0].Close != 2 {
					t.Errorf("bars = %+v, want one bar with close 2", s.Bars)
				}
			},
		},
		{
			name: "london timestamps use the london date",
			body: payload(map[string]any{"symbol": "KRW=X", "exchangeTimezoneName": "Europe/London"},
				[]int64{utc(2026, 6, 1, 23, 0)}, []any{f(1300)}, []any{f(1300)}, nil),
			check: func(t *testing.T, s *market.Series) {
				if got := s.Bars[0].Date; !got.Equal(date(t, "2026-06-02")) {
					t.Errorf("date = %s, want 2026-06-02 (local), not the UTC date", got.Format(market.DateLayout))
				}
			},
		},
		{
			name: "london splits use the london date too",
			body: chartSpec{meta: map[string]any{"symbol": "KRW=X", "exchangeTimezoneName": "Europe/London"},
				ts: []int64{utc(2026, 6, 1, 23, 0)}, closes: []any{f(1300)}, adjs: []any{f(1300)},
				events: map[string]any{"splits": map[string]any{"x": split(utc(2026, 6, 1, 23, 0), 2, 1)}}}.body(),
			check: func(t *testing.T, s *market.Series) {
				if got := s.Splits[0].Date; !got.Equal(date(t, "2026-06-02")) {
					t.Errorf("split date = %s, want 2026-06-02 (local)", got.Format(market.DateLayout))
				}
			},
		},
		{
			name: "unknown timezone falls back to new york",
			body: payload(map[string]any{"symbol": "TST", "exchangeTimezoneName": "Mars/Olympus"},
				[]int64{utc(2026, 6, 2, 1, 0)}, []any{f(1)}, []any{f(1)}, nil),
			check: func(t *testing.T, s *market.Series) {
				if got := s.Bars[0].Date; !got.Equal(date(t, "2026-06-01")) {
					t.Errorf("date = %s, want 2026-06-01 (New York)", got.Format(market.DateLayout))
				}
			},
		},
		{
			name: "short name and exchange name are fallbacks",
			body: payload(map[string]any{"symbol": "TST", "shortName": "Short", "exchangeName": "PCX"},
				[]int64{mon}, []any{f(1)}, []any{f(1)}, nil),
			check: func(t *testing.T, s *market.Series) {
				if s.Meta.Name != "Short" || s.Meta.Exchange != "PCX" {
					t.Errorf("meta = %+v, want Name Short and Exchange PCX", s.Meta)
				}
				if !s.Meta.FirstTradeDate.IsZero() {
					t.Errorf("FirstTradeDate = %v, want zero when absent", s.Meta.FirstTradeDate)
				}
			},
		},
		{
			name: "a first trade date before 1970 is kept",
			body: payload(map[string]any{"symbol": "^GSPC", "exchangeTimezoneName": "America/New_York", "firstTradeDate": -1325583000},
				[]int64{mon}, []any{f(1)}, []any{f(1)}, nil),
			check: func(t *testing.T, s *market.Series) {
				if got := s.Meta.FirstTradeDate.Format(market.DateLayout); got != "1927-12-30" {
					t.Errorf("FirstTradeDate = %s, want 1927-12-30", got)
				}
			},
		},
		{
			name: "empty history is a valid empty series",
			body: payload(nyMeta(), nil, nil, nil, nil),
			check: func(t *testing.T, s *market.Series) {
				if len(s.Bars) != 0 {
					t.Errorf("bars = %+v, want none", s.Bars)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := parseChart([]byte(tt.body), fixtureNow)
			if err != nil {
				t.Fatalf("parseChart: %v", err)
			}
			tt.check(t, s)
		})
	}
}

func TestEventTime(t *testing.T) {
	tests := []struct {
		key    string
		date   int64
		wantTS int64
		wantOK bool
	}{
		{"1789392600", 1789479000, 1789479000, true}, // the date field wins
		{"1789392600", 0, 1789392600, true},          // key as fallback
		{"-86400", 0, -86400, true},                  // pre-1970 keys parse
		{"x", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		ts, ok := eventTime(tt.key, tt.date)
		if ts != tt.wantTS || ok != tt.wantOK {
			t.Errorf("eventTime(%q, %d) = %d, %v; want %d, %v", tt.key, tt.date, ts, ok, tt.wantTS, tt.wantOK)
		}
	}
}

func TestAt(t *testing.T) {
	arr := []*float64{f(1.5), nil}
	tests := []struct {
		i    int
		want float64
	}{{0, 1.5}, {1, 0}, {2, 0}, {-0, 1.5}}
	for _, tt := range tests {
		if got := at(arr, tt.i); got != tt.want {
			t.Errorf("at(arr, %d) = %v, want %v", tt.i, got, tt.want)
		}
	}
	if got := at(nil, 0); got != 0 {
		t.Errorf("at(nil, 0) = %v, want 0", got)
	}
}

func TestLoadLocation(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Europe/London", "Europe/London"},
		{"", defaultLocation},
		{"Mars/Olympus", defaultLocation},
	}
	for _, tt := range tests {
		if got := loadLocation(tt.name).String(); got != tt.want {
			t.Errorf("loadLocation(%q) = %s, want %s", tt.name, got, tt.want)
		}
	}
}
