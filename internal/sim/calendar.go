package sim

import (
	"fmt"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// calendar is the trading calendar of a plan: the days on which every
// allocated symbol has a bar, inside the requested range, in ascending
// order, together with each symbol's bar on each day.
type calendar struct {
	days []time.Time
	// bars[i][k] is the bar of allocation i on days[k].
	bars [][]market.Bar
}

// buildCalendar intersects the series' trading days inside [p.Start, p.End]
// and returns the notes describing how the range was adjusted.
//
// A zero p.Start means the latest first bar of the series and a zero p.End
// the earliest last bar. A p.Start earlier than some symbol's first bar
// moves forward to that bar and a p.End later than some symbol's last bar
// moves back to it; each move adds a note. A range with no history, or
// one on which the series share no trading day, is an error.
func buildCalendar(p Plan, series []*market.Series) (*calendar, []string, error) {
	start, end := p.Start, p.End
	var notes []string

	latestFirst, firstSym := latestFirstBar(p.Allocations, series)
	switch {
	case start.IsZero():
		start = latestFirst
	case start.Before(latestFirst):
		notes = append(notes, fmt.Sprintf("start moved from %s to %s: %s history begins there",
			formatDate(start), formatDate(latestFirst), firstSym))
		start = latestFirst
	}

	earliestLast, lastSym := earliestLastBar(p.Allocations, series)
	switch {
	case end.IsZero():
		end = earliestLast
	case end.After(earliestLast):
		notes = append(notes, fmt.Sprintf("end moved from %s to %s: %s history ends there",
			formatDate(end), formatDate(earliestLast), lastSym))
		end = earliestLast
	}

	if end.Before(start) {
		return nil, nil, fmt.Errorf("sim: no history in range: start %s is after end %s",
			formatDate(start), formatDate(end))
	}

	cal := intersect(series, start, end)
	if len(cal.days) == 0 {
		return nil, nil, fmt.Errorf("sim: no trading day shared by %s between %s and %s",
			symbolList(p.Allocations), formatDate(start), formatDate(end))
	}
	return cal, notes, nil
}

// intersect returns the calendar of days inside [start, end] on which
// every series has a bar.
func intersect(series []*market.Series, start, end time.Time) *calendar {
	// The first series drives the walk; the others are indexed by date.
	base := series[0].Between(start, end)
	others := make([]map[time.Time]market.Bar, 0, len(series)-1)
	for _, s := range series[1:] {
		others = append(others, barsByDate(s.Between(start, end)))
	}

	cal := &calendar{bars: make([][]market.Bar, len(series))}
	for _, b := range base {
		day := market.Day(b.Date)
		dayBars, ok := barsOn(day, b, others)
		if !ok {
			continue
		}
		cal.days = append(cal.days, day)
		for i, db := range dayBars {
			cal.bars[i] = append(cal.bars[i], db)
		}
	}
	return cal
}

// barsByDate indexes bars by their Day-normalised date.
func barsByDate(bars []market.Bar) map[time.Time]market.Bar {
	m := make(map[time.Time]market.Bar, len(bars))
	for _, b := range bars {
		m[market.Day(b.Date)] = b
	}
	return m
}

// barsOn collects the bar of every series on day: first for the base
// series, then one per entry of others. ok is false when some series has
// no bar that day.
func barsOn(day time.Time, first market.Bar, others []map[time.Time]market.Bar) ([]market.Bar, bool) {
	bars := make([]market.Bar, 0, len(others)+1)
	bars = append(bars, first)
	for _, m := range others {
		b, ok := m[day]
		if !ok {
			return nil, false
		}
		bars = append(bars, b)
	}
	return bars, true
}

// latestFirstBar returns the latest date on which a series begins and the
// symbol of that series.
func latestFirstBar(allocs []Allocation, series []*market.Series) (time.Time, string) {
	var latest time.Time
	var sym string
	for i, s := range series {
		first, _ := s.First() // lookupSeries guarantees at least one bar
		if sym == "" || first.Date.After(latest) {
			latest, sym = market.Day(first.Date), allocs[i].Symbol
		}
	}
	return latest, sym
}

// earliestLastBar returns the earliest date on which a series ends and the
// symbol of that series.
func earliestLastBar(allocs []Allocation, series []*market.Series) (time.Time, string) {
	var earliest time.Time
	var sym string
	for i, s := range series {
		last, _ := s.Last() // lookupSeries guarantees at least one bar
		if sym == "" || last.Date.Before(earliest) {
			earliest, sym = market.Day(last.Date), allocs[i].Symbol
		}
	}
	return earliest, sym
}

// symbolList renders the allocated symbols as "A, B, C".
func symbolList(allocs []Allocation) string {
	syms := make([]string, len(allocs))
	for i, a := range allocs {
		syms[i] = a.Symbol
	}
	return strings.Join(syms, ", ")
}

// contributionDays flags the days of the calendar on which a contribution
// is made under cadence c: every day for Daily, the first day of each ISO
// week for Weekly and the first day of each calendar month for Monthly.
// The first day of the calendar always contributes.
func contributionDays(c Cadence, days []time.Time) []bool {
	out := make([]bool, len(days))
	for k, d := range days {
		out[k] = k == 0 || startsNewPeriod(c, days[k-1], d)
	}
	return out
}

// startsNewPeriod reports whether cur falls in a later contribution period
// than prev, the trading day before it.
func startsNewPeriod(c Cadence, prev, cur time.Time) bool {
	switch c {
	case Weekly:
		py, pw := prev.ISOWeek()
		cy, cw := cur.ISOWeek()
		return py != cy || pw != cw
	case Monthly:
		return prev.Year() != cur.Year() || prev.Month() != cur.Month()
	default: // Daily
		return true
	}
}

// isLastOfMonth reports whether days[k] is the last trading day of its
// calendar month within the calendar. The final day always is.
func isLastOfMonth(days []time.Time, k int) bool {
	if k == len(days)-1 {
		return true
	}
	cur, next := days[k], days[k+1]
	return cur.Year() != next.Year() || cur.Month() != next.Month()
}
