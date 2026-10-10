package sim

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// percentileKeys lists every key the percentile maps must carry.
var percentileKeys = []string{"p5", "p10", "p25", "p50", "p75", "p90", "p95"}

// weekdaySeries builds a Series with a bar on every weekday from first to
// last inclusive (YYYY-MM-DD), priced by price(day); AdjClose equals Close.
func weekdaySeries(t *testing.T, symbol, first, last string, price func(day time.Time) float64) *market.Series {
	t.Helper()
	s := &market.Series{Meta: market.Meta{Symbol: symbol, Currency: "USD"}}
	end := date(t, last)
	for day := date(t, first); !day.After(end); day = day.AddDate(0, 0, 1) {
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		p := price(day)
		s.Bars = append(s.Bars, market.Bar{Date: day, Close: p, AdjClose: p})
	}
	return s
}

// yearlyPrice prices each calendar year as a straight line from the
// year's first to its second value; years not listed run 100 → 150.
func yearlyPrice(lines map[int][2]float64) func(time.Time) float64 {
	return func(d time.Time) float64 {
		line, ok := lines[d.Year()]
		if !ok {
			line = [2]float64{100, 150}
		}
		frac := float64(d.YearDay()-1) / 365
		return line[0] + (line[1]-line[0])*frac
	}
}

// sixYears is a 2018–2023 weekday series in which every year rises
// 100 → 150 except 2020, which falls 150 → 100, and 2021, which rises
// 100 → 200. Its last bar is Friday 2023-12-29.
func sixYears(t *testing.T) *market.Series {
	t.Helper()
	return weekdaySeries(t, "SPY", "2018-01-01", "2023-12-29",
		yearlyPrice(map[int][2]float64{2020: {150, 100}, 2021: {100, 200}}))
}

// mustRunRolling runs the rolling simulation or fails the test.
func mustRunRolling(t *testing.T, p Plan, in Input, cfg RollingConfig) *RollingResult {
	t.Helper()
	res, err := RunRolling(p, in, cfg)
	if err != nil {
		t.Fatalf("RunRolling: %v", err)
	}
	return res
}

// requireWindowEqualsRun checks that w carries exactly the metrics of a
// direct Run of p over w's own range.
func requireWindowEqualsRun(t *testing.T, w RollingWindow, p Plan, in Input) {
	t.Helper()
	p.Start, p.End = w.Start, w.End
	direct := mustRun(t, p, in)
	if w.Contributions != direct.Contributions {
		t.Errorf("window %s: Contributions = %d, Run gives %d", formatDate(w.Start), w.Contributions, direct.Contributions)
	}
	requireFloat(t, "Invested", w.Invested, direct.Invested, tight)
	requireFloat(t, "FinalValue", w.FinalValue, direct.FinalValue, tight)
	requireFloat(t, "ReturnPct", w.ReturnPct, direct.ReturnPct, tight)
	requireFloat(t, "AnnualizedReturn", w.AnnualizedReturn, direct.AnnualizedReturn, tight)
	requireFloat(t, "MaxDrawdownPct", w.MaxDrawdownPct, direct.MaxDrawdownPct, tight)
	if w.AnnualizedReturnComputed != direct.AnnualizedReturnComputed {
		t.Errorf("window %s: AnnualizedReturnComputed = %v, Run gives %v", formatDate(w.Start), w.AnnualizedReturnComputed, direct.AnnualizedReturnComputed)
	}
}

// requirePercentileKeys checks that m has every key and that the values
// do not decrease from p5 to p95.
func requirePercentileKeys(t *testing.T, name string, m map[string]float64) {
	t.Helper()
	if len(m) != len(percentileKeys) {
		t.Fatalf("%s has %d keys, want %d: %v", name, len(m), len(percentileKeys), m)
	}
	prev := math.Inf(-1)
	for _, key := range percentileKeys {
		v, ok := m[key]
		if !ok {
			t.Fatalf("%s lacks %s", name, key)
		}
		if v < prev {
			t.Errorf("%s not monotone at %s: %v < %v", name, key, v, prev)
		}
		prev = v
	}
}

func TestRunRollingYearlyWindows(t *testing.T) {
	spy := sixYears(t)
	in := seriesInput(spy)
	p := singlePlan("SPY", Daily, 10)
	p.Start, p.End = date(t, "2019-06-01"), date(t, "2019-07-01") // ignored for placement
	res := mustRunRolling(t, p, in, RollingConfig{DurationYears: 1, StepMonths: 12})

	// Hand-placed: each window starts on the anniversary of the first bar
	// (or the next trading day: 2022-01-01 is a Saturday) and ends on the
	// day before the next anniversary (or the trading day before it:
	// 2022-12-31 is a Saturday). The 2023 window would end 2023-12-31,
	// after the last bar on the 29th, so it is not run.
	want := []struct{ start, end string }{
		{"2018-01-01", "2018-12-31"},
		{"2019-01-01", "2019-12-31"},
		{"2020-01-01", "2020-12-31"},
		{"2021-01-01", "2021-12-31"},
		{"2022-01-03", "2022-12-30"},
	}
	if res.Count != len(want) || len(res.Windows) != len(want) {
		t.Fatalf("Count = %d, len(Windows) = %d, want %d", res.Count, len(res.Windows), len(want))
	}
	for i, w := range want {
		win := res.Windows[i]
		requireDate(t, "Start", win.Start, w.start)
		requireDate(t, "End", win.End, w.end)
		requireWindowEqualsRun(t, win, singlePlan("SPY", Daily, 10), in)
	}
	requirePercentileKeys(t, "ReturnPctPercentiles", res.ReturnPctPercentiles)
	requirePercentileKeys(t, "AnnualizedPercentiles", res.AnnualizedPercentiles)
	if len(res.Notes) < 2 || !strings.Contains(res.Notes[0], "5 windows of 12 months, each covering whole calendar months") {
		t.Errorf("Notes = %q, want a placement note first", res.Notes)
	}
}

func TestRunRollingProbLossBestWorst(t *testing.T) {
	spy := sixYears(t)
	res := mustRunRolling(t, singlePlan("SPY", Daily, 10), seriesInput(spy), RollingConfig{DurationYears: 1, StepMonths: 12})

	// Only 2020 falls, so one window in five loses money.
	requireFloat(t, "ProbLoss", res.ProbLoss, 0.2, tight)
	requireDate(t, "Worst.Start", res.Worst.Start, "2020-01-01")
	if res.Worst.ReturnPct >= 0 {
		t.Errorf("Worst.ReturnPct = %v, want < 0", res.Worst.ReturnPct)
	}
	// 2021 doubles while the other rising years gain 50%.
	requireDate(t, "Best.Start", res.Best.Start, "2021-01-01")
	for _, w := range res.Windows {
		if w.Start.Year() != 2020 && w.ReturnPct <= 0 {
			t.Errorf("window %d: ReturnPct = %v, want > 0", w.Start.Year(), w.ReturnPct)
		}
		if w.ReturnPct > res.Best.ReturnPct || w.ReturnPct < res.Worst.ReturnPct {
			t.Errorf("window %d: ReturnPct %v outside [Worst %v, Best %v]", w.Start.Year(), w.ReturnPct, res.Worst.ReturnPct, res.Best.ReturnPct)
		}
	}

	// Percentiles by sort + linear interpolation over the five returns:
	// p50 is the middle value and p5 sits 20% of the way from the lowest
	// to the second lowest (rank 0.05 × 4 = 0.2).
	returns := make([]float64, 0, len(res.Windows))
	for _, w := range res.Windows {
		returns = append(returns, w.ReturnPct)
	}
	sort.Float64s(returns)
	requireFloat(t, "p50", res.ReturnPctPercentiles["p50"], returns[2], tight)
	requireFloat(t, "p5", res.ReturnPctPercentiles["p5"], returns[0]+0.2*(returns[1]-returns[0]), tight)
	requireFloat(t, "p95", res.ReturnPctPercentiles["p95"], returns[3]+0.8*(returns[4]-returns[3]), tight)
}

func TestRunRollingInsufficientHistory(t *testing.T) {
	spy := sixYears(t)
	tests := []struct {
		name    string
		cfg     RollingConfig
		windows int // 0 means an error is expected
	}{
		{"three 3-year windows fit", RollingConfig{DurationYears: 3, StepMonths: 12}, 3},
		{"two 4-year windows", RollingConfig{DurationYears: 4, StepMonths: 12}, 0},
		{"no 10-year window", RollingConfig{DurationYears: 10, StepMonths: 12}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := RunRolling(singlePlan("SPY", Monthly, 100), seriesInput(spy), tc.cfg)
			if tc.windows == 0 {
				if !errors.Is(err, ErrInsufficientHistory) {
					t.Fatalf("err = %v, want ErrInsufficientHistory", err)
				}
				if !strings.Contains(err.Error(), "need at least 3") {
					t.Errorf("err = %q, want the minimum spelled out", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RunRolling: %v", err)
			}
			if res.Count != tc.windows {
				t.Errorf("Count = %d, want %d", res.Count, tc.windows)
			}
		})
	}
}

func TestRunRollingDeterministic(t *testing.T) {
	spy := sixYears(t)
	in := seriesInput(spy)
	p := singlePlan("SPY", Weekly, 25)
	cfg := RollingConfig{DurationYears: 1} // StepMonths 0 means monthly

	first := mustRunRolling(t, p, in, cfg)
	// Monthly starts from 2018-01 through 2022-12 fit; 2023-01 would end
	// 2023-12-31, after the last bar.
	if first.Count != 60 {
		t.Fatalf("Count = %d, want 60", first.Count)
	}
	requireDate(t, "Windows[59].Start", first.Windows[59].Start, "2022-12-01")
	requireDate(t, "Windows[59].End", first.Windows[59].End, "2023-11-30")
	requireWindowEqualsRun(t, first.Windows[17], p, in)

	second := mustRunRolling(t, p, in, cfg)
	if !reflect.DeepEqual(first, second) {
		t.Error("two runs of the same rolling simulation differ")
	}
	explicit := mustRunRolling(t, p, in, RollingConfig{DurationYears: 1, StepMonths: 1})
	if !reflect.DeepEqual(first, explicit) {
		t.Error("StepMonths 0 and 1 differ")
	}
}

func TestRunRollingHonoursFeeFixedAndCalendarSymbols(t *testing.T) {
	flat := func(time.Time) float64 { return 100 }
	spy := weekdaySeries(t, "SPY", "2018-01-01", "2023-12-29", flat)
	schd := weekdaySeries(t, "SCHD", "2020-06-15", "2023-12-29", flat)
	p := singlePlan("SPY", Monthly, 100)
	p.FeeFixed = 1
	p.CalendarSymbols = []string{"schd"}
	in := seriesInput(spy, schd)
	res := mustRunRolling(t, p, in, RollingConfig{DurationYears: 1, StepMonths: 12})

	// The calendar symbol moves the first common bar to mid-month
	// 2020-06-15, so the first window starts with the first full month,
	// July 2020; the July 2023 window would end 2024-06-30.
	want := []struct{ start, end string }{
		{"2020-07-01", "2021-06-30"},
		{"2021-07-01", "2022-06-30"},
		{"2022-07-01", "2023-06-30"},
	}
	if res.Count != len(want) {
		t.Fatalf("Count = %d, want %d", res.Count, len(want))
	}
	for i, w := range want {
		win := res.Windows[i]
		requireDate(t, "Start", win.Start, w.start)
		requireDate(t, "End", win.End, w.end)
		// One contribution on the first trading day of each of the twelve
		// months.
		if win.Contributions != 12 {
			t.Errorf("window %d: Contributions = %d, want 12", i, win.Contributions)
		}
		// On a flat price the fixed fee is the only loss, once per contribution.
		requireFloat(t, "FinalValue", win.FinalValue, win.Invested-float64(win.Contributions), tight)
		requireWindowEqualsRun(t, win, p, in)
	}
	requireFloat(t, "ProbLoss", res.ProbLoss, 1, tight)
	for _, n := range res.Notes {
		if strings.Contains(n, "2020-06-15") && !strings.Contains(n, "first full month") || strings.Contains(n, "falls mid-month") {
			t.Errorf("note %q describes the history's first day as if it were every window's", n)
		}
	}
}

func TestRunRollingMonthlyWindowMakesOneContributionPerMonth(t *testing.T) {
	// SCHD-like history that begins mid-month on Thursday 2011-10-20.
	schd := weekdaySeries(t, "SCHD", "2011-10-20", "2016-12-30", func(time.Time) float64 { return 50 })
	in := seriesInput(schd)
	for _, tc := range []struct {
		months int
		want   int
	}{{12, 12}, {3, 3}, {36, 36}} {
		res := mustRunRolling(t, singlePlan("SCHD", Monthly, 100), in,
			RollingConfig{DurationYears: float64(tc.months) / 12, StepMonths: 1})
		for _, w := range res.Windows {
			if w.Contributions != tc.want {
				t.Fatalf("%d-month window from %s: Contributions = %d, want %d",
					tc.months, formatDate(w.Start), w.Contributions, tc.want)
			}
		}
	}

	// Weekly windows start on the first of a month, usually mid-week, and
	// the notes say so without naming a single window's date.
	res := mustRunRolling(t, singlePlan("SCHD", Weekly, 100), in, RollingConfig{DurationYears: 1, StepMonths: 12})
	for _, n := range res.Notes {
		if strings.HasPrefix(n, "every window: ") {
			t.Errorf("note %q applies one window's placement to all of them", n)
		}
	}
}

func TestRunRollingErrors(t *testing.T) {
	spy := sixYears(t)
	tests := []struct {
		name    string
		plan    Plan
		cfg     RollingConfig
		wantErr string
	}{
		{"zero duration", singlePlan("SPY", Daily, 10), RollingConfig{}, "duration must be > 0"},
		{"negative duration", singlePlan("SPY", Daily, 10), RollingConfig{DurationYears: -1}, "duration must be > 0"},
		{"NaN duration", singlePlan("SPY", Daily, 10), RollingConfig{DurationYears: math.NaN()}, "duration must be > 0"},
		{"fractional months", singlePlan("SPY", Daily, 10), RollingConfig{DurationYears: 0.3}, "not a whole number of months"},
		{"negative step", singlePlan("SPY", Daily, 10), RollingConfig{DurationYears: 1, StepMonths: -1}, "step must be >= 1"},
		{"unknown symbol", singlePlan("QQQ", Daily, 10), RollingConfig{DurationYears: 1}, "no price series for QQQ"},
		{"bad amount", singlePlan("SPY", Daily, 0), RollingConfig{DurationYears: 1}, "amount must be > 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := RunRolling(tc.plan, seriesInput(spy), tc.cfg)
			if err == nil {
				t.Fatalf("RunRolling returned %+v, want error containing %q", res, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestFirstWindowStart(t *testing.T) {
	tests := []struct{ first, want string }{
		{"2018-01-01", "2018-01-01"}, // the first weekday of January
		{"2024-01-02", "2024-01-01"}, // after New Year's Day
		{"2024-09-03", "2024-09-01"}, // after Labor Day
		{"2011-10-20", "2011-11-01"}, // mid-month: the next full month
		{"2024-12-31", "2025-01-01"},
	}
	for _, tc := range tests {
		t.Run(tc.first, func(t *testing.T) {
			requireDate(t, "firstWindowStart", firstWindowStart(date(t, tc.first)), tc.want)
		})
	}
}

func TestWindowRangesCoverWholeMonths(t *testing.T) {
	ranges := windowRanges(date(t, "2018-01-01"), date(t, "2019-12-31"), 12, 1)
	if len(ranges) != 13 {
		t.Fatalf("len(ranges) = %d, want 13 (Jan 2018 through Jan 2019 starts)", len(ranges))
	}
	requireDate(t, "ranges[0].end", ranges[0].end, "2018-12-31")
	requireDate(t, "ranges[1].start", ranges[1].start, "2018-02-01")
	requireDate(t, "ranges[1].end", ranges[1].end, "2019-01-31")
	requireDate(t, "ranges[12].start", ranges[12].start, "2019-01-01")
	requireDate(t, "ranges[12].end", ranges[12].end, "2019-12-31")

	short := windowRanges(date(t, "2024-01-01"), date(t, "2024-12-31"), 3, 3)
	if len(short) != 4 {
		t.Fatalf("len(short) = %d, want 4 quarters", len(short))
	}
	requireDate(t, "short[0].end", short[0].end, "2024-03-31")
	requireDate(t, "short[3].start", short[3].start, "2024-10-01")
}

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4}
	tests := []struct {
		q, want float64
	}{
		{0, 1}, {0.25, 1.75}, {0.5, 2.5}, {1, 4},
	}
	for _, tc := range tests {
		requireFloat(t, "percentile", percentile(sorted, tc.q), tc.want, tight)
	}
	requireFloat(t, "single value", percentile([]float64{7}, 0.9), 7, tight)

	m := percentiles([]float64{3, 1, 2}) // unsorted input is sorted in a copy
	requireFloat(t, "p50 of 3,1,2", m["p50"], 2, tight)
	requireFloat(t, "p5 of 3,1,2", m["p5"], 1.1, tight)
}

func TestRunRollingReportsFailingWindow(t *testing.T) {
	flat := func(time.Time) float64 { return 100 }
	a := weekdaySeries(t, "A", "2024-01-01", "2024-06-28", flat)
	b := weekdaySeries(t, "B", "2024-01-01", "2024-06-28", flat)
	// B has no bars in March, so the March window shares no trading day.
	kept := b.Bars[:0]
	for _, bar := range b.Bars {
		if bar.Date.Month() != time.March {
			kept = append(kept, bar)
		}
	}
	b.Bars = kept

	p := Plan{
		Allocations: []Allocation{{Symbol: "A", Weight: 0.5}, {Symbol: "B", Weight: 0.5}},
		Amount:      100,
		Currency:    CurrencyUSD,
		Cadence:     Daily,
	}
	_, err := RunRolling(p, seriesInput(a, b), RollingConfig{DurationYears: 1.0 / 12})
	if err == nil {
		t.Fatal("RunRolling succeeded, want the March window to fail")
	}
	for _, want := range []string{"rolling window 3 (2024-03-01 to 2024-03-31)", "no trading day shared by A, B"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestRunRollingAnnualizedUnavailable(t *testing.T) {
	// One-month windows, monthly step, Jan–May 2024 (the June window would
	// end 2024-06-30, after the last bar on the 28th).
	cfg := RollingConfig{DurationYears: 1.0 / 12}

	t.Run("one steep window", func(t *testing.T) {
		// Flat except March, which doubles: +40% or so on daily buys in a
		// month annualises far beyond 1000%, so XIRR finds no rate.
		price := func(d time.Time) float64 {
			if d.Month() == time.March {
				return 100 + 100*float64(d.Day()-1)/30
			}
			return 100
		}
		spy := weekdaySeries(t, "SPY", "2024-01-01", "2024-06-28", price)
		res := mustRunRolling(t, singlePlan("SPY", Daily, 10), seriesInput(spy), cfg)
		if res.Count != 5 {
			t.Fatalf("Count = %d, want 5", res.Count)
		}
		march := res.Windows[2]
		requireDate(t, "Windows[2].Start", march.Start, "2024-03-01")
		if march.AnnualizedReturnComputed {
			t.Errorf("March window annualised to %v, want no fitted rate", march.AnnualizedReturn)
		}
		requirePercentileKeys(t, "AnnualizedPercentiles", res.AnnualizedPercentiles)
		// The four flat windows all have a rate of 0.
		requireFloat(t, "p50 annualized", res.AnnualizedPercentiles["p50"], 0, 1e-6)
		if !containsNote(res.Notes, "1 of 5 windows have no annualized return") {
			t.Errorf("Notes = %q, want the excluded window counted", res.Notes)
		}
	})

	t.Run("every window steep", func(t *testing.T) {
		// +5% a day: every one-month window is far beyond the XIRR bracket.
		spy := newSeries(t, "SPY", "2024-01-01", 130, func(k int) float64 { return 100 * math.Pow(1.05, float64(k)) })
		res := mustRunRolling(t, singlePlan("SPY", Daily, 10), seriesInput(spy), cfg)
		for _, w := range res.Windows {
			if w.AnnualizedReturnComputed {
				t.Errorf("window %s annualised to %v, want none", formatDate(w.Start), w.AnnualizedReturn)
			}
		}
		if res.AnnualizedPercentiles != nil {
			t.Errorf("AnnualizedPercentiles = %v, want nil", res.AnnualizedPercentiles)
		}
		requirePercentileKeys(t, "ReturnPctPercentiles", res.ReturnPctPercentiles)
		if !containsNote(res.Notes, "no window has an annualized return") {
			t.Errorf("Notes = %q, want the nil map explained", res.Notes)
		}
	})
}

// containsNote reports whether any note contains want.
func containsNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

func TestRunRollingContextStopsWhenCancelled(t *testing.T) {
	spy := newSeries(t, "SPY", "2010-01-04", 3000, func(k int) float64 { return 100 + float64(k)/10 })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := RunRollingContext(ctx, singlePlan("SPY", Monthly, 100), seriesInput(spy), RollingConfig{DurationYears: 1, StepMonths: 1})
	if !errors.Is(err, context.Canceled) || res != nil {
		t.Fatalf("RunRollingContext on a cancelled context = %v, %v; want nil, context.Canceled", res, err)
	}
	// The same plan without cancellation still runs.
	if _, err := RunRolling(singlePlan("SPY", Monthly, 100), seriesInput(spy), RollingConfig{DurationYears: 1, StepMonths: 1}); err != nil {
		t.Fatalf("RunRolling: %v", err)
	}
}
