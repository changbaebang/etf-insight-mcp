package analytics

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// geometricSeries builds weekday bars from start through end whose price
// grows by exactly annualRate per calendar year, compounded continuously.
func geometricSeries(symbol string, start, end time.Time, annualRate float64) *market.Series {
	return seriesFrom(symbol, weekdaysUntil(start, end), func(_ int, d time.Time) float64 {
		years := d.Sub(start).Hours() / 24 / daysPerYear
		return 100 * math.Pow(1+annualRate, years)
	})
}

// windowByLabel finds one Window in a Summary.
func windowByLabel(t *testing.T, sum Summary, label string) Window {
	t.Helper()
	for _, w := range sum.Windows {
		if w.Label == label {
			return w
		}
	}
	t.Fatalf("window %q missing from %v", label, sum.Windows)
	return Window{}
}

func TestSummarizeGeometricGrowth(t *testing.T) {
	start, end := day(2018, time.January, 1), day(2024, time.January, 1)
	s := geometricSeries("GROW", start, end, 0.10)
	last, _ := s.Last()
	asOf := last.Date

	// Dividends: two inside the trailing year, one outside.
	setDividend := func(daysBack int, amount float64) {
		idx, ok := s.IndexOn(asOf.AddDate(0, 0, -daysBack))
		if !ok {
			t.Fatalf("no bar %d days back", daysBack)
		}
		s.Bars[idx].Dividend = amount
	}
	setDividend(30, 0.5)
	setDividend(200, 0.7)
	setDividend(500, 1.0)

	sum, err := Summarize(s, time.Time{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if sum.Symbol != "GROW" || !sum.AsOf.Equal(asOf) || sum.Bars != s.Len() || !sum.FirstDate.Equal(s.Bars[0].Date) {
		t.Fatalf("identity fields wrong: %+v", sum)
	}
	if sum.Close != last.Close || sum.AdjClose != last.AdjClose {
		t.Fatalf("prices wrong: close %v adj %v", sum.Close, sum.AdjClose)
	}

	windows := []struct {
		label          string
		available      bool
		wantTotal      float64 // checked only when > 0
		wantAnnualized float64 // checked only when > 0
	}{
		{label: "1m", available: true},
		{label: "1y", available: true, wantTotal: 0.10, wantAnnualized: 0.10},
		{label: "3y", available: true, wantAnnualized: 0.10},
		{label: "5y", available: true, wantAnnualized: 0.10},
		{label: "10y", available: false},
		{label: "max", available: true, wantAnnualized: 0.10},
	}
	for _, tt := range windows {
		w := windowByLabel(t, sum, tt.label)
		if w.Available != tt.available {
			t.Fatalf("window %s available = %v, want %v", tt.label, w.Available, tt.available)
		}
		if tt.wantTotal > 0 && !approx(w.TotalReturn, tt.wantTotal, 1e-3) {
			t.Fatalf("window %s total return = %v, want %v", tt.label, w.TotalReturn, tt.wantTotal)
		}
		if tt.wantAnnualized > 0 && !approx(w.Annualized, tt.wantAnnualized, 1e-3) {
			t.Fatalf("window %s annualized = %v, want %v", tt.label, w.Annualized, tt.wantAnnualized)
		}
	}
	if w := windowByLabel(t, sum, "1m"); w.Annualized != w.TotalReturn {
		t.Fatalf("1m window must not be annualized: %+v", w)
	}
	if w := windowByLabel(t, sum, "max"); !w.From.Equal(s.Bars[0].Date) {
		t.Fatalf("max window From = %v, want first bar %v", w.From, s.Bars[0].Date)
	}
	if len(sum.Windows) != len(windowSpecs) {
		t.Fatalf("got %d windows, want %d", len(sum.Windows), len(windowSpecs))
	}

	if sum.MaxDrawdownAll != 0 || sum.MaxDrawdown1Y != 0 {
		t.Fatalf("rising series must have zero drawdown, got all %v 1y %v", sum.MaxDrawdownAll, sum.MaxDrawdown1Y)
	}
	if !approx(sum.TTMDividendPerShare, 1.2, 1e-12) {
		t.Fatalf("TTM dividend = %v, want 1.2", sum.TTMDividendPerShare)
	}
	if !approx(sum.TTMDividendYield, 1.2/last.Close, 1e-12) {
		t.Fatalf("TTM yield = %v, want %v", sum.TTMDividendYield, 1.2/last.Close)
	}
	if sum.High52W != last.Close {
		t.Fatalf("52w high = %v, want last close %v", sum.High52W, last.Close)
	}
	year := trailingYear(s, asOf)
	if sum.Low52W != year[0].Close {
		t.Fatalf("52w low = %v, want first close of trailing year %v", sum.Low52W, year[0].Close)
	}
	// Weekend gaps make the daily log returns uneven, so volatility is
	// small but not zero.
	if sum.Volatility1Y <= 0 || sum.Volatility1Y > 0.05 {
		t.Fatalf("volatility 1y = %v, want small positive", sum.Volatility1Y)
	}
}

func TestSummarizeAsOf(t *testing.T) {
	s := geometricSeries("GROW", day(2020, time.January, 1), day(2021, time.December, 31), 0.10)
	tests := []struct {
		name     string
		asOf     time.Time
		wantAsOf time.Time
	}{
		{name: "trading day", asOf: day(2021, time.June, 15), wantAsOf: day(2021, time.June, 15)},
		{name: "weekend falls back to friday", asOf: day(2021, time.June, 13), wantAsOf: day(2021, time.June, 11)},
		{name: "after last bar uses last bar", asOf: day(2030, time.January, 1), wantAsOf: day(2021, time.December, 31)},
		{name: "zero means last bar", asOf: time.Time{}, wantAsOf: day(2021, time.December, 31)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum, err := Summarize(s, tt.asOf)
			if err != nil {
				t.Fatalf("Summarize: %v", err)
			}
			if !sum.AsOf.Equal(tt.wantAsOf) {
				t.Fatalf("AsOf = %v, want %v", sum.AsOf, tt.wantAsOf)
			}
			idx, _ := s.IndexOn(tt.wantAsOf)
			if sum.Bars != idx+1 {
				t.Fatalf("Bars = %d, want %d", sum.Bars, idx+1)
			}
		})
	}
}

func TestSummarizeErrors(t *testing.T) {
	valid := growthSeries("OK", 10, 0.001)
	tests := []struct {
		name    string
		series  *market.Series
		asOf    time.Time
		wantErr error
	}{
		{name: "nil series", series: nil, wantErr: ErrInvalidInput},
		{name: "empty symbol", series: &market.Series{Bars: valid.Bars}, wantErr: ErrInvalidInput},
		{name: "no bars", series: &market.Series{Meta: market.Meta{Symbol: "EMPTY"}}, wantErr: ErrInsufficientHistory},
		{name: "asOf before first bar", series: valid, asOf: day(2019, time.January, 1), wantErr: ErrInsufficientHistory},
		{
			name:    "non-positive price",
			series:  &market.Series{Meta: market.Meta{Symbol: "BAD"}, Bars: []market.Bar{{Date: day(2020, time.January, 2), Close: 0, AdjClose: 1}}},
			wantErr: ErrInvalidInput,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Summarize(tt.series, tt.asOf)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Summarize error = %v, want %v", err, tt.wantErr)
			}
			if _, err := AnalyzeTrend(tt.series, tt.asOf); !errors.Is(err, tt.wantErr) {
				t.Fatalf("AnalyzeTrend error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSummarizeShortSeries(t *testing.T) {
	s := growthSeries("SHORT", 10, 0.001)
	sum, err := Summarize(s, time.Time{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	for _, w := range sum.Windows {
		if want := w.Label == "max"; w.Available != want {
			t.Fatalf("window %s available = %v, want %v", w.Label, w.Available, want)
		}
	}
	if !approx(sum.Volatility1Y, 0, 1e-12) {
		t.Fatalf("constant growth has no volatility, got %v", sum.Volatility1Y)
	}
	if sum.TTMDividendPerShare != 0 || sum.TTMDividendYield != 0 {
		t.Fatalf("no dividends expected, got %v", sum.TTMDividendPerShare)
	}
}

func TestAnnualize(t *testing.T) {
	tests := []struct {
		name     string
		total    float64
		from, to time.Time
		want     float64
		tol      float64
		wantOK   bool
	}{
		{name: "two years of 21% is about 10% a year", total: 0.21, from: day(2020, time.January, 1), to: day(2022, time.January, 1), want: 0.10, tol: 1e-3, wantOK: true},
		{name: "exactly 365 days is annualized as is", total: 0.07, from: day(2021, time.January, 1), to: day(2022, time.January, 1), want: 0.07, tol: 1e-3, wantOK: true},
		{name: "six months are not extrapolated", total: 0.05, from: day(2021, time.January, 1), to: day(2021, time.July, 1), want: 0.05, tol: 0, wantOK: false},
		{name: "loss over four years", total: -0.344, from: day(2018, time.January, 1), to: day(2022, time.January, 1), want: -0.10, tol: 1e-3, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := annualize(tt.total, tt.from, tt.to)
			if !approx(got, tt.want, tt.tol) || ok != tt.wantOK {
				t.Fatalf("annualize(%v) = %v, %v; want %v, %v", tt.total, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestSummarizeTrailingYearIsFiftyTwoWeeks: a fund that pays every 13
// weeks has two ex-dates exactly 52 weeks apart. On the latest one the
// trailing year must hold four payments, not five, and the bar exactly 52
// weeks back must not count towards the 52-week range either.
func TestSummarizeTrailingYearIsFiftyTwoWeeks(t *testing.T) {
	s := growthSeries("QTR", 600, 0.0005)
	last, _ := s.Last()
	yearAgo := -1
	for weeks := 0; weeks <= 52; weeks += 13 {
		date := last.Date.AddDate(0, 0, -7*weeks)
		idx, ok := s.IndexOn(date)
		if !ok || !s.Bars[idx].Date.Equal(date) {
			t.Fatalf("no bar on %s", date.Format(market.DateLayout))
		}
		s.Bars[idx].Dividend = 1
		yearAgo = idx
	}

	sum, err := Summarize(s, time.Time{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if sum.TTMDividendPerShare != 4 {
		t.Errorf("TTM dividend = %v, want 4: the payment 52 weeks back belongs to the year before", sum.TTMDividendPerShare)
	}
	// The series rises every day, so the 52-week low is the first close
	// inside the window: the bar after the one exactly 52 weeks back.
	if want := s.Bars[yearAgo+1].Close; sum.Low52W != want {
		t.Errorf("52-week low = %v, want %v (first bar after %s)", sum.Low52W, want, s.Bars[yearAgo].Date.Format(market.DateLayout))
	}
}

// TestSummarizeYearWindowsAnnualizeOverNominalYears: a window of N whole
// years is annualized over N years, so the 1-year CAGR is its total
// return and one output never shows two different 1-year rates.
func TestSummarizeYearWindowsAnnualizeOverNominalYears(t *testing.T) {
	s := geometricSeries("GROW", day(2018, time.January, 1), day(2024, time.March, 15), 0.10)
	sum, err := Summarize(s, time.Time{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	oneYear := windowByLabel(t, sum, "1y")
	if !oneYear.AnnualizedAvailable || oneYear.Annualized != oneYear.TotalReturn {
		t.Errorf("1y annualized = %v (available %v), want the total return %v", oneYear.Annualized, oneYear.AnnualizedAvailable, oneYear.TotalReturn)
	}
	for label, years := range map[string]float64{"3y": 3, "5y": 5} {
		w := windowByLabel(t, sum, label)
		if want := math.Pow(1+w.TotalReturn, 1/years) - 1; !approx(w.Annualized, want, 1e-12) {
			t.Errorf("%s annualized = %v, want %v", label, w.Annualized, want)
		}
	}
}
