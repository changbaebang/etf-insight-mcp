package sim

import (
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestStartsNewPeriod(t *testing.T) {
	tests := []struct {
		name      string
		cadence   Cadence
		prev, cur string
		want      bool
	}{
		{"daily consecutive", Daily, "2024-01-02", "2024-01-03", true},
		{"weekly same week", Weekly, "2024-01-02", "2024-01-03", false},
		{"weekly next Monday", Weekly, "2024-01-05", "2024-01-08", true},
		{"weekly ISO week 53 spans the year", Weekly, "2020-12-31", "2021-01-01", false},
		{"weekly first Monday of 2021", Weekly, "2021-01-01", "2021-01-04", true},
		{"monthly same month", Monthly, "2024-01-30", "2024-01-31", false},
		{"monthly next month", Monthly, "2024-01-31", "2024-02-01", true},
		{"monthly same month number next year", Monthly, "2023-01-31", "2024-01-02", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := startsNewPeriod(tc.cadence, date(t, tc.prev), date(t, tc.cur))
			if got != tc.want {
				t.Errorf("startsNewPeriod(%s, %s, %s) = %v, want %v", tc.cadence, tc.prev, tc.cur, got, tc.want)
			}
		})
	}
}

func TestContributionDays(t *testing.T) {
	days := []time.Time{
		date(t, "2024-01-03"), // Wed, week 1
		date(t, "2024-01-04"),
		date(t, "2024-01-08"), // Mon, week 2
		date(t, "2024-01-31"), // week 5
		date(t, "2024-02-01"), // Thu, week 5, new month
	}
	tests := []struct {
		cadence Cadence
		want    []bool
	}{
		{Daily, []bool{true, true, true, true, true}},
		{Weekly, []bool{true, false, true, true, false}},
		{Monthly, []bool{true, false, false, false, true}},
	}
	for _, tc := range tests {
		t.Run(string(tc.cadence), func(t *testing.T) {
			got := contributionDays(tc.cadence, days)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k := range got {
				if got[k] != tc.want[k] {
					t.Errorf("day %d (%s) = %v, want %v", k, days[k].Format("2006-01-02"), got[k], tc.want[k])
				}
			}
		})
	}
	if got := contributionDays(Monthly, nil); len(got) != 0 {
		t.Errorf("contributionDays(nil) = %v, want empty", got)
	}
}

func TestIsLastOfMonth(t *testing.T) {
	days := []time.Time{
		date(t, "2024-01-30"),
		date(t, "2024-01-31"),
		date(t, "2024-02-01"),
		date(t, "2024-02-02"),
	}
	want := []bool{false, true, false, true}
	for k := range days {
		if got := isLastOfMonth(days, k); got != want[k] {
			t.Errorf("isLastOfMonth(%d) = %v, want %v", k, got, want[k])
		}
	}
}

func TestIntersectKeepsOnlySharedDays(t *testing.T) {
	a := newSeries(t, "A", "2024-01-01", 5, constant(1)) // Jan 1-5
	b := newSeries(t, "B", "2024-01-03", 5, constant(2)) // Jan 3-9
	cal := intersect([]*market.Series{a, b}, time.Time{}, time.Time{})
	wantDays := []string{"2024-01-03", "2024-01-04", "2024-01-05"}
	if len(cal.days) != len(wantDays) {
		t.Fatalf("days = %v, want %v", cal.days, wantDays)
	}
	for k, d := range wantDays {
		requireDate(t, "days[k]", cal.days[k], d)
		if cal.bars[0][k].Close != 1 || cal.bars[1][k].Close != 2 {
			t.Errorf("bars on %s = %v / %v, want closes 1 / 2", d, cal.bars[0][k], cal.bars[1][k])
		}
	}
}
