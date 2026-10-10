package cache

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// closeTolerance is the relative difference in Close or AdjClose that still
// counts as the same price when a top-up overlaps cached bars. A provider
// returns the same number for the same settled day, so anything beyond
// float noise means the history was rewritten.
const closeTolerance = 1e-6

// errNeedFull is returned, wrapped with the reason, by mergeTail when the
// cached history cannot be extended and must be fetched in full.
var errNeedFull = errors.New("cache: full fetch needed")

// mergeTail extends the cached history base with tail, a range fetch that
// starts at from, a few days before base's last bar, and ends today. It
// returns the merged series without mutating either input, or an error
// wrapping errNeedFull whenever appending would leave the cached bars and
// the provider's current view inconsistent. The rules, in order:
//
//  1. The overlap is every tail bar dated on or before base's last bar.
//     Its dates must match, one to one, the cached bars from from (or the
//     overlap's first date, if earlier) through the last cached bar; a
//     bar on either side without a counterpart means the provider's
//     trading calendar changed or its history was rewritten. Full fetch.
//     One exception: when nothing newer follows, the tail may lack the
//     newest cached day, because the provider withholds a session's bar
//     for a while after the close. That cached bar, captured during the
//     session, is dropped; the next top-up brings the settled one.
//  2. A dividend that differs between an overlapping tail bar and its
//     cached bar, or a dividend on a new bar, changes AdjClose for every
//     earlier bar (the adjustment is applied backwards). Full fetch.
//  3. Overlapping bars other than the newest cached bar must agree on
//     Close and AdjClose within closeTolerance. A difference means a split
//     or a back-adjustment was applied to the whole history, so the
//     cached bars before the overlap are stale too. Full fetch. The newest
//     cached bar is exempt because it may have been captured during the
//     trading session; the tail's print of that day replaces it. At least
//     one bar must be compared this way, or a rescaled history would go
//     unnoticed. Full fetch otherwise.
//  4. A split in the tail that the cache does not already hold (same date
//     and ratio) rescaled every earlier price. Full fetch.
//  5. Otherwise the bars dated after the last cached bar are appended, the
//     cached splits are kept, Meta keeps its descriptive fields and takes
//     the tail's latest quote fields, and the result must pass
//     market.Series.Validate.
//
// An empty tail returns a copy of base: the provider published nothing,
// so there is nothing to append and nothing to contradict the cache.
func mergeTail(base, tail *market.Series, from time.Time) (*market.Series, error) {
	if err := tail.Validate(); err != nil {
		return nil, fmt.Errorf("%w: tail: %w", errNeedFull, err)
	}
	last, ok := base.Last()
	if !ok {
		return nil, fmt.Errorf("%w: cached history is empty", errNeedFull)
	}
	n := sort.Search(len(tail.Bars), func(i int) bool { return tail.Bars[i].Date.After(last.Date) })
	overlap, fresh := tail.Bars[:n], tail.Bars[n:]

	keep := base.Bars // the cached bars that stay; checkOverlap may drop the newest
	if tail.Len() > 0 {
		var err error
		if keep, err = checkOverlap(base, overlap, from, len(fresh) > 0); err != nil {
			return nil, err
		}
	}
	if err := checkNewDividends(fresh); err != nil {
		return nil, err
	}
	if err := checkSplits(base.Splits, tail.Splits); err != nil {
		return nil, err
	}

	merged := &market.Series{
		Meta:   refreshMeta(base.Meta, tail.Meta),
		Bars:   make([]market.Bar, 0, len(keep)+len(fresh)),
		Splits: base.Splits,
	}
	merged.Bars = append(merged.Bars, keep...)
	if len(overlap) > 0 && len(keep) == len(base.Bars) {
		// checkOverlap proved the overlap ends on the newest cached bar's
		// date; the tail's print of that day is the settled one.
		merged.Bars[len(keep)-1] = overlap[len(overlap)-1]
	}
	merged.Bars = append(merged.Bars, fresh...)
	if err := merged.Validate(); err != nil {
		return nil, fmt.Errorf("%w: merged series: %w", errNeedFull, err)
	}
	return merged, nil
}

// checkOverlap applies rules 1 to 3 to overlap, the tail bars dated on or
// before the newest cached bar, and returns the cached bars to keep:
// all of base's, or all but the newest when the provider withheld that
// day (allowed only when hasFresh is false). It returns base.Bars itself,
// or a prefix of it, never a copy.
func checkOverlap(base *market.Series, overlap []market.Bar, from time.Time, hasFresh bool) ([]market.Bar, error) {
	start := market.Day(from)
	if len(overlap) > 0 && overlap[0].Date.Before(start) {
		start = overlap[0].Date
	}
	cached := base.Between(start, time.Time{})
	keep := base.Bars
	withheld := !hasFresh && len(overlap) == len(cached)-1
	if withheld {
		cached = cached[:len(overlap)]
		keep = keep[:len(keep)-1]
	}
	if len(cached) != len(overlap) {
		return nil, fmt.Errorf("%w: %d cached bars since %s but %d in the tail",
			errNeedFull, len(cached), day(start), len(overlap))
	}
	// exempt is the index of the bar spared the price check, the tail's
	// print of the newest cached day, and so also the number of bars that
	// are checked. A withheld day has no print: every overlap bar is
	// checked.
	exempt := len(overlap) - 1
	if withheld {
		exempt = len(overlap)
	}
	if exempt < 1 {
		return nil, fmt.Errorf("%w: no cached bar since %s to check the tail against", errNeedFull, day(start))
	}
	for i, got := range overlap {
		have := cached[i]
		switch {
		case !have.Date.Equal(got.Date):
			return nil, fmt.Errorf("%w: cached bar on %s, tail bar on %s", errNeedFull, day(have.Date), day(got.Date))
		case !nearlyEqual(have.Dividend, got.Dividend):
			return nil, fmt.Errorf("%w: dividend on %s changed from %g to %g", errNeedFull, day(got.Date), have.Dividend, got.Dividend)
		case i != exempt && !samePrices(have, got):
			return nil, fmt.Errorf("%w: prices on %s changed", errNeedFull, day(got.Date))
		}
	}
	return keep, nil
}

// checkNewDividends applies rule 2 to the bars that would be appended.
func checkNewDividends(fresh []market.Bar) error {
	for _, b := range fresh {
		if b.Dividend > 0 {
			return fmt.Errorf("%w: new dividend %g on %s", errNeedFull, b.Dividend, day(b.Date))
		}
	}
	return nil
}

// checkSplits applies rule 4: every split the tail reports must already
// be cached.
func checkSplits(have, got []market.Split) error {
	for _, s := range got {
		if !containsSplit(have, s) {
			return fmt.Errorf("%w: split %s on %s is not cached", errNeedFull, s.Ratio(), day(s.Date))
		}
	}
	return nil
}

// containsSplit reports whether have holds s (same date and ratio).
func containsSplit(have []market.Split, s market.Split) bool {
	for _, h := range have {
		if h.Date.Equal(s.Date) && nearlyEqual(h.Numerator, s.Numerator) && nearlyEqual(h.Denominator, s.Denominator) {
			return true
		}
	}
	return false
}

// samePrices reports whether two bars agree on Close and AdjClose.
func samePrices(a, b market.Bar) bool {
	return nearlyEqual(a.Close, b.Close) && nearlyEqual(a.AdjClose, b.AdjClose)
}

// nearlyEqual reports whether a and b differ by at most closeTolerance
// relative to the larger magnitude. Equal values, zero included, match.
func nearlyEqual(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= closeTolerance*math.Max(math.Abs(a), math.Abs(b))
}

// refreshMeta keeps the cached descriptive fields and takes the quote
// fields the tail reports, since those describe the latest day.
func refreshMeta(have, got market.Meta) market.Meta {
	if got.RegularMarketPrice != 0 {
		have.RegularMarketPrice = got.RegularMarketPrice
	}
	if got.FiftyTwoWeekHigh != 0 {
		have.FiftyTwoWeekHigh = got.FiftyTwoWeekHigh
	}
	if got.FiftyTwoWeekLow != 0 {
		have.FiftyTwoWeekLow = got.FiftyTwoWeekLow
	}
	if !got.FetchedAt.IsZero() {
		have.FetchedAt = got.FetchedAt
	}
	return have
}

// day formats t as a calendar date for error messages.
func day(t time.Time) string {
	return t.Format(market.DateLayout)
}
