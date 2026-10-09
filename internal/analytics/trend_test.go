package analytics

import (
	"strings"
	"testing"
	"time"
)

func TestAnalyzeTrendStates(t *testing.T) {
	tests := []struct {
		name       string
		bars       int
		dailyLog   float64
		wantState  string
		wantReason string // substring that must appear in Reasons
	}{
		{name: "strictly rising", bars: 300, dailyLog: 0.001, wantState: StateUptrend, wantReason: "above 200-day average"},
		{name: "strictly falling", bars: 300, dailyLog: -0.001, wantState: StateDowntrend, wantReason: "below 200-day average"},
		{name: "flat", bars: 300, dailyLog: 0, wantState: StateSideways, wantReason: "equal to 200-day average"},
		{name: "rising but slope unavailable", bars: 210, dailyLog: 0.001, wantState: StateUptrend, wantReason: "slope needs 220 bars"},
		{name: "falling but slope unavailable", bars: 210, dailyLog: -0.001, wantState: StateDowntrend, wantReason: "judged on the averages alone"},
		{name: "too short", bars: 100, dailyLog: 0.001, wantState: StateInsufficientHistory, wantReason: "only 100 of the 200 bars"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := growthSeries("X", tt.bars, tt.dailyLog)
			tr, err := AnalyzeTrend(s, time.Time{})
			if err != nil {
				t.Fatalf("AnalyzeTrend: %v", err)
			}
			if tr.State != tt.wantState {
				t.Fatalf("State = %q, want %q (reasons %v)", tr.State, tt.wantState, tr.Reasons)
			}
			if !hasReason(tr.Reasons, tt.wantReason) {
				t.Fatalf("Reasons %v lack %q", tr.Reasons, tt.wantReason)
			}
			if tr.Close != s.Bars[tt.bars-1].Close || !tr.AsOf.Equal(s.Bars[tt.bars-1].Date) {
				t.Fatalf("anchor wrong: %+v", tr)
			}
		})
	}
}

func hasReason(reasons []string, substr string) bool {
	for _, r := range reasons {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

func TestAnalyzeTrendHandComputed(t *testing.T) {
	// Closes 1, 2, 3, ... so every average is a mean of consecutive integers.
	linear := func(i int, _ time.Time) float64 { return float64(i + 1) }

	t.Run("60 bars", func(t *testing.T) {
		s := seriesFrom("LIN", weekdays(day(2020, time.January, 1), 60), linear)
		tr, err := AnalyzeTrend(s, time.Time{})
		if err != nil {
			t.Fatalf("AnalyzeTrend: %v", err)
		}
		const sma50 = 35.5 // mean of 11..60
		if !approx(tr.SMA50, sma50, 1e-12) {
			t.Fatalf("SMA50 = %v, want %v", tr.SMA50, sma50)
		}
		if want := (60/sma50 - 1) * 100; !approx(tr.PctVsSMA50, want, 1e-9) {
			t.Fatalf("PctVsSMA50 = %v, want %v", tr.PctVsSMA50, want)
		}
		if tr.SMA200 != 0 || tr.PctVsSMA200 != 0 || tr.SMA200Slope != 0 {
			t.Fatalf("200-day fields must be zero without history: %+v", tr)
		}
		if tr.Momentum12_1 != 0 || tr.Return6M != 0 {
			t.Fatalf("long-lookback fields must be zero without history: %+v", tr)
		}
		if tr.State != StateInsufficientHistory {
			t.Fatalf("State = %q, want insufficient-history", tr.State)
		}
	})

	t.Run("300 bars", func(t *testing.T) {
		s := seriesFrom("LIN", weekdays(day(2020, time.January, 1), 300), linear)
		tr, err := AnalyzeTrend(s, time.Time{})
		if err != nil {
			t.Fatalf("AnalyzeTrend: %v", err)
		}
		checks := []struct {
			name string
			got  float64
			want float64
		}{
			{"SMA50", tr.SMA50, 275.5},   // mean of 251..300
			{"SMA200", tr.SMA200, 200.5}, // mean of 101..300
			{"PctVsSMA200", tr.PctVsSMA200, (300/200.5 - 1) * 100},
			{"Momentum12_1", tr.Momentum12_1, 279.0/48 - 1},  // index 278 over index 47
			{"Return6M", tr.Return6M, 300.0/174 - 1},         // index 299 over index 173
			{"SMA200Slope", tr.SMA200Slope, 200.5/180.5 - 1}, // SMA200 at 299 over at 279
		}
		for _, c := range checks {
			if !approx(c.got, c.want, 1e-9) {
				t.Fatalf("%s = %v, want %v", c.name, c.got, c.want)
			}
		}
		if tr.State != StateUptrend {
			t.Fatalf("State = %q, want uptrend (reasons %v)", tr.State, tr.Reasons)
		}
		if tr.Volatility20D <= 0 || tr.Volatility1Y <= 0 {
			t.Fatalf("volatilities must be positive for a linear ramp: %+v", tr)
		}
	})
}

func TestAnalyzeTrendAsOf(t *testing.T) {
	s := growthSeries("X", 400, 0.001)
	asOf := s.Bars[299].Date
	tr, err := AnalyzeTrend(s, asOf)
	if err != nil {
		t.Fatalf("AnalyzeTrend: %v", err)
	}
	full, err := AnalyzeTrend(growthSeries("X", 300, 0.001), time.Time{})
	if err != nil {
		t.Fatalf("AnalyzeTrend: %v", err)
	}
	if !tr.AsOf.Equal(asOf) || tr.Close != full.Close || tr.SMA200 != full.SMA200 || tr.State != full.State {
		t.Fatalf("asOf must ignore later bars: got %+v, want %+v", tr, full)
	}
}
