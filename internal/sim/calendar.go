package sim

import (
	"fmt"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// calendar is the trading calendar of a plan: the days on which every
// allocated symbol (and every Plan.CalendarSymbols symbol) has a bar,
// inside the requested range, in ascending order, together with each
// allocated symbol's bar on each day.
type calendar struct {
	days []time.Time
	// bars[i][k] is the bar of allocation i on days[k].
	bars [][]market.Bar
}

// named pairs a series with the symbol it was requested under, for notes.
type named struct {
	sym string
	s   *market.Series
}

// buildCalendar intersects the trading days of the allocated series and
// the extra calendar series inside [p.Start, p.End] and returns the notes
// describing how the range was adjusted.
//
// A zero p.Start means the latest first bar of all bounding series and a
// zero p.End the earliest last bar. A p.Start earlier than some series'
// first bar moves forward to that bar and a p.End later than some series'
// last bar moves back to it; each move adds a note. fx, when non-nil, only
// bounds the range (its first and last bar); its trading days are not
// intersected because the converter falls back to the last rate before a
// day. A range with no history, or one on which the series share no
// trading day, is an error.
func buildCalendar(p Plan, allocated, extra []named, fx *market.Series) (*calendar, []string, error) {
	bounding := make([]named, 0, len(allocated)+len(extra)+1)
	bounding = append(bounding, allocated...)
	bounding = append(bounding, extra...)
	if fx != nil {
		bounding = append(bounding, named{sym: fxLabel, s: fx})
	}

	start, end := p.Start, p.End
	var notes []string

	latestFirst, firstSym := latestFirstBar(bounding)
	switch {
	case start.IsZero():
		start = latestFirst
	case start.Before(latestFirst):
		notes = append(notes, fmt.Sprintf("start moved from %s to %s: %s history begins there",
			formatDate(start), formatDate(latestFirst), firstSym))
		start = latestFirst
	}

	earliestLast, lastSym := earliestLastBar(bounding)
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

	cal := intersect(allocated, extra, start, end)
	if len(cal.days) == 0 {
		return nil, nil, fmt.Errorf("sim: no trading day shared by %s between %s and %s",
			symbolList(append(append([]named{}, allocated...), extra...)), formatDate(start), formatDate(end))
	}
	if note, ok := midPeriodStartNote(p.Cadence, allocated[0].s, cal.days[0]); ok {
		notes = append(notes, note)
	}
	return cal, notes, nil
}

// fxLabel names the exchange-rate series in notes.
const fxLabel = "KRW=X exchange-rate"

// intersect returns the calendar of days inside [start, end] on which
// every allocated and every extra series has a bar. Bars are kept for the
// allocated series only.
func intersect(allocated, extra []named, start, end time.Time) *calendar {
	// The first allocated series drives the walk; the rest are indexed by
	// date.
	base := allocated[0].s.Between(start, end)
	others := make([]map[time.Time]market.Bar, 0, len(allocated)-1+len(extra))
	for _, n := range allocated[1:] {
		others = append(others, barsByDate(n.s.Between(start, end)))
	}
	for _, n := range extra {
		others = append(others, barsByDate(n.s.Between(start, end)))
	}

	cal := &calendar{bars: make([][]market.Bar, len(allocated))}
	for _, b := range base {
		day := market.Day(b.Date)
		dayBars, ok := barsOn(day, b, others)
		if !ok {
			continue
		}
		cal.days = append(cal.days, day)
		for i := range allocated {
			cal.bars[i] = append(cal.bars[i], dayBars[i])
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
func latestFirstBar(series []named) (time.Time, string) {
	var latest time.Time
	var sym string
	for _, n := range series {
		first, _ := n.s.First() // validated to have at least one bar
		if sym == "" || first.Date.After(latest) {
			latest, sym = market.Day(first.Date), n.sym
		}
	}
	return latest, sym
}

// earliestLastBar returns the earliest date on which a series ends and the
// symbol of that series.
func earliestLastBar(series []named) (time.Time, string) {
	var earliest time.Time
	var sym string
	for _, n := range series {
		last, _ := n.s.Last() // validated to have at least one bar
		if sym == "" || last.Date.Before(earliest) {
			earliest, sym = market.Day(last.Date), n.sym
		}
	}
	return earliest, sym
}

// symbolList renders symbols as "A, B, C".
func symbolList(series []named) string {
	syms := make([]string, len(series))
	for i, n := range series {
		syms[i] = n.sym
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

// midPeriodStartNote explains, for weekly and monthly plans, that a first
// contribution falling in the middle of its week or month is followed by
// another on the first trading day of the next period. The previous bar of
// the base series decides whether the start is mid-period; when the series
// begins on the start day itself the calendar date is used instead (a
// Monday or the first weekday of the month counts as a period start).
func midPeriodStartNote(c Cadence, base *market.Series, start time.Time) (string, bool) {
	if c == Daily {
		return "", false
	}
	mid := false
	if i, ok := base.IndexOn(start); ok && i > 0 && base.Bars[i].Date.Equal(start) {
		mid = !startsNewPeriod(c, market.Day(base.Bars[i-1].Date), start)
	} else {
		mid = !firstWeekdayOfPeriod(c, start)
	}
	if !mid {
		return "", false
	}
	period := "month"
	if c == Weekly {
		period = "week"
	}
	return fmt.Sprintf("first contribution on %s falls mid-%s; later contributions are on the first trading day of each following %s",
		formatDate(start), period, period), true
}

// firstWeekdayOfPeriod reports whether d is the first weekday of its ISO
// week (Monday) or calendar month, judged by the calendar alone.
func firstWeekdayOfPeriod(c Cadence, d time.Time) bool {
	if c == Weekly {
		return d.Weekday() == time.Monday
	}
	for day := d.AddDate(0, 0, -1); day.Month() == d.Month(); day = day.AddDate(0, 0, -1) {
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			return false
		}
	}
	return true
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
