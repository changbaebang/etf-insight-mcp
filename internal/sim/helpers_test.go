package sim

import (
	"math"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// newSeries builds a synthetic Series of n consecutive weekdays starting at
// start (YYYY-MM-DD; a weekend start moves to the next Monday). price(k)
// gives the close of bar k; AdjClose equals Close and Dividend is 0 unless
// a test changes the bars afterwards.
func newSeries(t *testing.T, symbol, start string, n int, price func(k int) float64) *market.Series {
	t.Helper()
	day := date(t, start)
	s := &market.Series{Meta: market.Meta{Symbol: symbol, Currency: "USD"}}
	for k := 0; k < n; {
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			p := price(k)
			s.Bars = append(s.Bars, market.Bar{Date: day, Close: p, AdjClose: p})
			k++
		}
		day = day.AddDate(0, 0, 1)
	}
	return s
}

// barsSeries builds a Series from explicit (date, close) pairs; AdjClose
// equals Close.
func barsSeries(t *testing.T, symbol string, closes map[string]float64, order []string) *market.Series {
	t.Helper()
	s := &market.Series{Meta: market.Meta{Symbol: symbol}}
	for _, d := range order {
		s.Bars = append(s.Bars, market.Bar{Date: date(t, d), Close: closes[d], AdjClose: closes[d]})
	}
	return s
}

// constant returns a price function that always yields p.
func constant(p float64) func(int) float64 {
	return func(int) float64 { return p }
}

// date parses a YYYY-MM-DD string or fails the test.
func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := market.ParseDate(s)
	if err != nil {
		t.Fatalf("date %q: %v", s, err)
	}
	return d
}

// singlePlan is a USD plan fully allocated to symbol with daily cadence
// and no fee, covering the whole series unless Start/End are set.
func singlePlan(symbol string, cadence Cadence, amount float64) Plan {
	return Plan{
		Allocations: []Allocation{{Symbol: symbol, Weight: 1}},
		Amount:      amount,
		Currency:    CurrencyUSD,
		Cadence:     cadence,
	}
}

// seriesInput wraps series into an Input keyed by their symbols.
func seriesInput(series ...*market.Series) Input {
	in := Input{Series: make(map[string]*market.Series, len(series))}
	for _, s := range series {
		in.Series[s.Meta.Symbol] = s
	}
	return in
}

// closeTo reports whether got is within rel (relative) of want, with an
// absolute floor of 1e-9 so values near zero compare sensibly.
func closeTo(got, want, rel float64) bool {
	diff := math.Abs(got - want)
	return diff <= 1e-9 || diff <= rel*math.Abs(want)
}

// requireFloat fails the test when got is not within rel of want.
func requireFloat(t *testing.T, name string, got, want, rel float64) {
	t.Helper()
	if !closeTo(got, want, rel) {
		t.Errorf("%s = %.12g, want %.12g (rel %g)", name, got, want, rel)
	}
}

// requireDate fails the test when got is not the calendar day want.
func requireDate(t *testing.T, name string, got time.Time, want string) {
	t.Helper()
	if !got.Equal(date(t, want)) {
		t.Errorf("%s = %s, want %s", name, got.Format(market.DateLayout), want)
	}
}

// mustRun runs the plan or fails the test.
func mustRun(t *testing.T, p Plan, in Input) *Result {
	t.Helper()
	res, err := Run(p, in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}
