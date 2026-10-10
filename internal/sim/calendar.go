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
	// bars[i][k] is the bar of allocation i on days[k]. Its Dividend also
	// carries the dividends of allocation i's bars that fell on days
	// missing from the calendar since days[k-1]; see alignBars.
	bars [][]market.Bar
	// splits[i] is the split history of allocation i.
	splits [][]market.Split
	// notes describes every dividend that was moved to a later day.
	notes []string
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
// trading day, is an error. The notes end with the calendar's own notes
// on dividends moved off days that not every symbol traded.
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
	return cal, append(notes, cal.notes...), nil
}

// fxLabel names the exchange-rate series in notes.
const fxLabel = "KRW=X exchange-rate"

// intersect returns the calendar of days inside [start, end] on which
// every allocated and every extra series has a bar. Bars are kept for the
// allocated series only.
func intersect(allocated, extra []named, start, end time.Time) *calendar {
	cal := &calendar{
		days:   commonDays(allocated, extra, start, end),
		bars:   make([][]market.Bar, len(allocated)),
		splits: make([][]market.Split, len(allocated)),
	}
	for i, n := range allocated {
		var notes []string
		cal.bars[i], notes = alignBars(n, cal.days, start, end)
		cal.notes = append(cal.notes, notes...)
		cal.splits[i] = n.s.Splits
	}
	return cal
}

// commonDays lists, in ascending order, the days inside [start, end] on
// which every allocated and every extra series has a bar. The first
// allocated series drives the walk; the rest are looked up by date.
func commonDays(allocated, extra []named, start, end time.Time) []time.Time {
	others := make([]map[time.Time]bool, 0, len(allocated)-1+len(extra))
	for _, n := range allocated[1:] {
		others = append(others, daySet(n.s.Between(start, end)))
	}
	for _, n := range extra {
		others = append(others, daySet(n.s.Between(start, end)))
	}

	var days []time.Time
	for _, b := range allocated[0].s.Between(start, end) {
		day := market.Day(b.Date)
		if tradesOnAll(day, others) {
			days = append(days, day)
		}
	}
	return days
}

// daySet indexes the Day-normalised dates of bars.
func daySet(bars []market.Bar) map[time.Time]bool {
	m := make(map[time.Time]bool, len(bars))
	for _, b := range bars {
		m[market.Day(b.Date)] = true
	}
	return m
}

// tradesOnAll reports whether every set contains day.
func tradesOnAll(day time.Time, sets []map[time.Time]bool) bool {
	for _, set := range sets {
		if !set[day] {
			return false
		}
	}
	return true
}

// alignBars returns n's bar on each of days, which must all be trading
// days of n inside [start, end].
//
// A bar of n on a day missing from days (another symbol did not trade)
// is dropped, but its dividend is not: it is added to n's bar on the next
// calendar day, the way yahoo moves a dividend dated on a non-trading day
// to the next bar. The shares held then are the shares held on the
// ex-date, because nothing is bought on a dropped day, and the engine
// handles dividends before the day's contribution. Each move adds a note.
// Dividends dated before the first calendar day are dropped (nothing is
// held yet), and so are those after the last (the plan is valued before
// them).
func alignBars(n named, days []time.Time, start, end time.Time) ([]market.Bar, []string) {
	out := make([]market.Bar, 0, len(days))
	var notes []string
	var pending []market.Bar // dropped bars whose dividend is still owed
	for _, b := range n.s.Between(start, end) {
		if len(out) == len(days) {
			break
		}
		day := market.Day(b.Date)
		if !day.Equal(days[len(out)]) {
			if b.Dividend > 0 && len(out) > 0 {
				pending = append(pending, b)
			}
			continue
		}
		for _, p := range pending {
			b.Dividend += p.Dividend
			notes = append(notes, fmt.Sprintf("%s dividend of %.4f per share dated %s, a day not every symbol traded, is credited on %s, the next day they all did",
				n.sym, p.Dividend, formatDate(p.Date), formatDate(day)))
		}
		pending = pending[:0]
		out = append(out, b)
	}
	return out, notes
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
// week for Weekly, the first day of each calendar month for Monthly and
// only the first day for Once. The first day of the calendar always
// contributes.
func contributionDays(c Cadence, days []time.Time) []bool {
	out := make([]bool, len(days))
	for k, d := range days {
		out[k] = k == 0 || startsNewPeriod(c, days[k-1], d)
	}
	return out
}

// startsNewPeriod reports whether cur falls in a later contribution period
// than prev, the trading day before it. Once has a single period, so it
// never starts a new one.
func startsNewPeriod(c Cadence, prev, cur time.Time) bool {
	switch c {
	case Once:
		return false
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
// begins on the start day itself, opensPeriod judges by the calendar.
func midPeriodStartNote(c Cadence, base *market.Series, start time.Time) (string, bool) {
	if c == Daily || c == Once {
		return "", false
	}
	mid := false
	if i, ok := base.IndexOn(start); ok && i > 0 && base.Bars[i].Date.Equal(start) {
		mid = !startsNewPeriod(c, market.Day(base.Bars[i-1].Date), start)
	} else {
		mid = !opensPeriod(c, start)
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

// openingHolidays is how many weekdays of a week or month may precede a
// day that opensPeriod still accepts as the period's first trading day.
// One covers the exchange holidays that open a period: New Year's Day and
// the Monday holidays such as Labor Day.
const openingHolidays = 1

// opensPeriod reports whether d can be the first trading day of its ISO
// week (Weekly) or calendar month (Monthly), judged by the calendar alone
// for a series that has no earlier bar: at most openingHolidays weekdays
// of the period come before d. Without an earlier bar a weekday holiday
// cannot be told apart from a day the series did not exist yet, so a
// series that begins on the second weekday of a period counts as on time.
func opensPeriod(c Cadence, d time.Time) bool {
	if c != Weekly && c != Monthly {
		return true
	}
	before := 0
	for day := d.AddDate(0, 0, -1); !startsNewPeriod(c, day, d); day = day.AddDate(0, 0, -1) {
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			before++
		}
	}
	return before <= openingHolidays
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
