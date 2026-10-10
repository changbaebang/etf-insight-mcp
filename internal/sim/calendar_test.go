package sim

import (
	"testing"
	"time"
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
	cal := intersect([]named{{"A", a}, {"B", b}}, nil, time.Time{}, time.Time{})
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

func TestIntersectWithExtraCalendarSeries(t *testing.T) {
	a := newSeries(t, "A", "2024-01-01", 5, constant(1)) // Jan 1-5
	c := newSeries(t, "C", "2024-01-04", 3, constant(9)) // Jan 4, 5, 8
	cal := intersect([]named{{"A", a}}, []named{{"C", c}}, time.Time{}, time.Time{})
	wantDays := []string{"2024-01-04", "2024-01-05"}
	if len(cal.days) != len(wantDays) {
		t.Fatalf("days = %v, want %v", cal.days, wantDays)
	}
	if len(cal.bars) != 1 {
		t.Fatalf("bars kept for %d series, want 1 (extra series are calendar-only)", len(cal.bars))
	}
	for k, d := range wantDays {
		requireDate(t, "days[k]", cal.days[k], d)
		if cal.bars[0][k].Close != 1 {
			t.Errorf("bar on %s = %v, want close 1", d, cal.bars[0][k])
		}
	}
}

func TestMidPeriodStartNote(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 40, constant(100)) // Mon Jan 1 .. Feb 23
	tests := []struct {
		name  string
		c     Cadence
		start string
		want  bool
	}{
		{"daily never notes", Daily, "2024-01-17", false},
		{"monthly on the first trading day", Monthly, "2024-01-01", false},
		{"monthly mid-month", Monthly, "2024-01-17", true},
		{"monthly on Feb 1 with history before it", Monthly, "2024-02-01", false},
		{"weekly on a Monday", Weekly, "2024-01-08", false},
		{"weekly on a Wednesday", Weekly, "2024-01-10", true},
		{"series starting mid-month judged by the calendar", Monthly, "2024-01-01", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := midPeriodStartNote(tt.c, spy, date(t, tt.start))
			if got != tt.want {
				t.Errorf("midPeriodStartNote(%s, %s) = %v, want %v", tt.c, tt.start, got, tt.want)
			}
		})
	}
	// A series whose first bar is mid-month: no previous bar, so the
	// calendar decides (Jan 17 2024 is a Wednesday with weekdays before it).
	mid := newSeries(t, "MID", "2024-01-17", 10, constant(1))
	if _, got := midPeriodStartNote(Monthly, mid, date(t, "2024-01-17")); !got {
		t.Error("expected a mid-month note for a series starting on Jan 17")
	}
	// Mar 1 2024 is a Friday and the first weekday of March.
	first := newSeries(t, "FIRST", "2024-03-01", 10, constant(1))
	if _, got := midPeriodStartNote(Monthly, first, date(t, "2024-03-01")); got {
		t.Error("Mar 1 2024 is the first weekday of its month; no note expected")
	}
}

func TestMidPeriodStartNoteAfterOpeningHoliday(t *testing.T) {
	// With no earlier bar, a series that begins on the first trading day
	// after a weekday holiday starts its period on time.
	tests := []struct {
		name  string
		c     Cadence
		start string
		want  bool
	}{
		{"monthly after New Year's Day", Monthly, "2024-01-02", false},
		{"weekly after New Year's Day", Weekly, "2024-01-02", false},
		{"monthly after Labor Day", Monthly, "2024-09-03", false},
		{"weekly after Labor Day", Weekly, "2024-09-03", false},
		{"weekly two weekdays late", Weekly, "2024-09-04", true},
		{"monthly two weekdays late", Monthly, "2024-01-03", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSeries(t, "NEW", tt.start, 10, constant(1))
			_, got := midPeriodStartNote(tt.c, s, date(t, tt.start))
			if got != tt.want {
				t.Errorf("midPeriodStartNote(%s, %s) = %v, want %v", tt.c, tt.start, got, tt.want)
			}
		})
	}
}
