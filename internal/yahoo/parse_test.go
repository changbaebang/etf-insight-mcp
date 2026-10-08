package yahoo

import (
	"encoding/json"
	"errors"
	"os"
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

// payload builds a chart body for table tests. closes and adjs use nil for
// JSON null; a nil adjs slice omits the adjclose block entirely; a nil
// dividends map omits the events block.
func payload(meta map[string]any, ts []int64, closes, adjs []any, dividends map[string]any) string {
	indicators := map[string]any{"quote": []any{map[string]any{"close": closes}}}
	if adjs != nil {
		indicators["adjclose"] = []any{map[string]any{"adjclose": adjs}}
	}
	result := map[string]any{"meta": meta, "timestamp": ts, "indicators": indicators}
	if dividends != nil {
		result["events"] = map[string]any{"dividends": dividends}
	}
	b, err := json.Marshal(map[string]any{
		"chart": map[string]any{"result": []any{result}, "error": nil},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
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
	for i, b := range s.Bars {
		want := 0.0
		if i == 4 { // 2026-09-18 pays the quarterly dividend
			want = 1.889
		}
		if b.Dividend != want {
			t.Errorf("bar %d (%s) dividend = %v, want %v", i, b.Date.Format(market.DateLayout), b.Dividend, want)
		}
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
