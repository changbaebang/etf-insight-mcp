package yahoo

import (
	"testing"
	"time"
)

func TestParseChartMarksIntradayLastBar(t *testing.T) {
	start := utc(2026, 6, 2, 13, 30) // regular session 13:30-20:00 UTC (09:30-16:00 EDT)
	end := utc(2026, 6, 2, 20, 0)
	meta := func() map[string]any {
		m := nyMeta()
		m["currentTradingPeriod"] = map[string]any{"regular": map[string]any{"start": start, "end": end}}
		return m
	}
	prev := utc(2026, 6, 1, 13, 30)
	tests := []struct {
		name string
		ts   []int64
		now  time.Time
		want time.Time
	}{
		{"during the session", []int64{prev, start}, time.Unix(utc(2026, 6, 2, 15, 0), 0), time.Unix(end, 0).UTC()},
		{"after the close", []int64{prev, start}, time.Unix(utc(2026, 6, 2, 20, 30), 0), time.Time{}},
		{"before today's bar exists", []int64{prev}, time.Unix(utc(2026, 6, 2, 12, 0), 0), time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closes := make([]any, len(tt.ts))
			for i := range closes {
				closes[i] = f(100 + float64(i))
			}
			s, err := parseChart([]byte(payload(meta(), tt.ts, closes, closes, nil)), tt.now)
			if err != nil {
				t.Fatal(err)
			}
			if !s.Meta.ProvisionalUntil.Equal(tt.want) {
				t.Errorf("ProvisionalUntil = %v, want %v", s.Meta.ProvisionalUntil, tt.want)
			}
		})
	}
	// No currentTradingPeriod (older fixtures, other endpoints): never provisional.
	s, err := parseChart([]byte(payload(nyMeta(), []int64{start}, []any{f(1)}, []any{f(1)}, nil)), time.Unix(utc(2026, 6, 2, 15, 0), 0))
	if err != nil || !s.Meta.ProvisionalUntil.IsZero() {
		t.Errorf("without a trading period: ProvisionalUntil = %v, err %v; want zero", s.Meta.ProvisionalUntil, err)
	}
}

// TestParseChartDropsBarsAfterTheSession: KRW=X on a Saturday carries a bar
// stamped at the live quote's time, after Friday's session; it is not a
// close and is dropped.
func TestParseChartDropsBarsAfterTheSession(t *testing.T) {
	m := map[string]any{"symbol": "KRW=X", "exchangeTimezoneName": "Europe/London",
		"currentTradingPeriod": map[string]any{"regular": map[string]any{"start": utc(2026, 10, 8, 23, 0), "end": utc(2026, 10, 9, 22, 59)}}}
	ts := []int64{utc(2026, 10, 7, 23, 0), utc(2026, 10, 8, 23, 0), utc(2026, 10, 10, 3, 30)} // Thu, Fri (London), Saturday quote
	closes := []any{f(1400), f(1410), f(1412)}
	s, err := parseChart([]byte(payload(m, ts, closes, closes, nil)), time.Unix(utc(2026, 10, 10, 8, 0), 0))
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("bars = %d, want 2 (the Saturday quote dropped)", s.Len())
	}
	if last, _ := s.Last(); last.Close != 1410 || !s.Meta.ProvisionalUntil.IsZero() {
		t.Errorf("last bar = %+v, provisional until %v; want Friday's settled 1410", last, s.Meta.ProvisionalUntil)
	}
}
