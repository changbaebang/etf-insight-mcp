package sim

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"
)

// ErrInsufficientHistory is wrapped by the error RunRolling returns when
// fewer than three windows fit into the common history of the plan's
// symbols. Test for it with errors.Is.
var ErrInsufficientHistory = errors.New("sim: not enough history for rolling windows")

const (
	// minRollingWindows is the fewest windows a rolling result may be
	// built from; percentiles of one or two numbers say nothing.
	minRollingWindows = 3
	// monthsPerYear converts RollingConfig.DurationYears to whole months.
	monthsPerYear = 12
	// monthTolerance is how far DurationYears × 12 may be from a whole
	// number before the duration is rejected.
	monthTolerance = 1e-6
)

// RollingConfig shapes the windows RunRolling slides over the history.
type RollingConfig struct {
	// DurationYears is the length of every window in years and must be a
	// positive whole number of months: 1 = 12 months, 0.5 = 6 months,
	// 2.5 = 30 months. Windows cover whole calendar months: one that
	// starts on the first of a month S covers [S, S + that many months),
	// so a one-year window runs from 2015-01-01 through 2015-12-31 and a
	// monthly plan makes exactly 12 contributions in it, one on the first
	// trading day of each month. A weekly plan makes 52 or 53.
	DurationYears float64
	// StepMonths is how many months later each successive window starts:
	// 1 starts a new window every month, 12 every year. 0 means 1.
	StepMonths int
}

// RollingWindow is the outcome of the plan over one window. The money
// fields have the meaning of the same-named Result fields.
type RollingWindow struct {
	// Start is the window's first contribution day and End its valuation
	// day, both on the trading calendar.
	Start, End time.Time
	// Contributions is how many contributions the window made.
	Contributions int
	// Invested is the gross amount contributed and FinalValue the value at
	// End, in the plan currency.
	Invested, FinalValue float64
	// ReturnPct is (FinalValue − Invested) / Invested × 100.
	ReturnPct float64
	// AnnualizedReturn is the window's XIRR as a fraction; see
	// Result.AnnualizedReturn. It is 0 unless AnnualizedReturnComputed.
	AnnualizedReturn float64
	// AnnualizedReturnComputed reports whether AnnualizedReturn holds a
	// fitted rate.
	AnnualizedReturnComputed bool
	// MaxDrawdownPct is the largest NAV fall inside the window, as a
	// positive percentage; see Result.MaxDrawdownPct.
	MaxDrawdownPct float64
}

// RollingResult is the distribution of outcomes of one plan over every
// window of the same length that fits into the history: "what if I had
// started in year X?" answered for every X.
type RollingResult struct {
	// Windows lists every window in start-date order.
	Windows []RollingWindow
	// Count is len(Windows).
	Count int
	// ReturnPctPercentiles maps "p5", "p10", "p25", "p50", "p75", "p90"
	// and "p95" to that percentile of RollingWindow.ReturnPct over all
	// windows, by sorting with linear interpolation between ranks.
	ReturnPctPercentiles map[string]float64
	// AnnualizedPercentiles holds the same keys for
	// RollingWindow.AnnualizedReturn over the windows that computed one.
	// It is nil when no window did; Notes says so.
	AnnualizedPercentiles map[string]float64
	// ProbLoss is the share of windows whose FinalValue is below Invested,
	// as a fraction (0.2 = one window in five lost money).
	ProbLoss float64
	// Best and Worst are the windows with the highest and lowest
	// ReturnPct; on a tie the earlier window is kept.
	Best, Worst RollingWindow
	// Notes explains how the windows were placed and anything that
	// deviated from the request.
	Notes []string
}

// RunRolling runs p over every window of cfg.DurationYears that fits into
// the common history of its symbols and summarises the outcomes.
//
// p.Start and p.End are ignored. Windows start on the first of a month:
// the first window in the first full calendar month of the common history
// of every allocated and calendar symbol (and, for KRW, the exchange
// rate), each next window cfg.StepMonths later, and a window is run only
// when its last calendar day is on or before the last day of that common
// history. A history that begins on the first trading day of its month
// starts the first window in that month (see opensPeriod). Every window
// is a full Run with the plan's amount, cadence, currency, fees, dividend
// model and calendar symbols, so each window's first trading day
// contributes and RollingWindow equals a Run over the window's dates.
// Windows run concurrently on runtime.NumCPU() workers; the result does
// not depend on scheduling. Fewer than three windows is an error wrapping
// ErrInsufficientHistory.
func RunRolling(p Plan, in Input, cfg RollingConfig) (*RollingResult, error) {
	months, step, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	probe := p
	probe.Start, probe.End = time.Time{}, time.Time{}
	pr, err := prepare(probe, in)
	if err != nil {
		return nil, err
	}
	first, last := pr.cal.days[0], pr.cal.days[len(pr.cal.days)-1]
	ranges := windowRanges(firstWindowStart(first), last, months, step)
	if len(ranges) < minRollingWindows {
		return nil, fmt.Errorf("%w: %d window(s) of %d months fit between %s and %s, need at least %d",
			ErrInsufficientHistory, len(ranges), months, formatDate(first), formatDate(last), minRollingWindows)
	}
	windows, err := runWindows(pr.plan, in, ranges)
	if err != nil {
		return nil, err
	}
	res := summarise(windows)
	res.Notes = append(placementNotes(pr.plan.Cadence, ranges, months, step, first, last), res.Notes...)
	// The probe's other notes describe its own first day; only the notes
	// about the data itself hold for every window.
	res.Notes = append(res.Notes, pr.cal.notes...)
	return res, nil
}

// resolve validates the config and returns the window length and step in
// whole months.
func (cfg RollingConfig) resolve() (months, step int, err error) {
	if math.IsNaN(cfg.DurationYears) || math.IsInf(cfg.DurationYears, 0) || cfg.DurationYears <= 0 {
		return 0, 0, fmt.Errorf("sim: rolling duration must be > 0 years, got %v", cfg.DurationYears)
	}
	exact := cfg.DurationYears * monthsPerYear
	months = int(math.Round(exact))
	if months < 1 || math.Abs(exact-float64(months)) > monthTolerance {
		return 0, 0, fmt.Errorf("sim: rolling duration %v years is not a whole number of months", cfg.DurationYears)
	}
	step = cfg.StepMonths
	if step == 0 {
		step = 1
	}
	if step < 0 {
		return 0, 0, fmt.Errorf("sim: rolling step must be >= 1 month, got %d", cfg.StepMonths)
	}
	return months, step, nil
}

// dateRange is one window's inclusive calendar range before it is fitted
// to the trading calendar by Run.
type dateRange struct {
	start, end time.Time
}

// firstWindowStart returns the first of the first full calendar month of
// a history that begins on first: first's own month when first opens it
// (allowing for an opening holiday, see opensPeriod), the next month
// otherwise.
func firstWindowStart(first time.Time) time.Time {
	monthStart := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, time.UTC)
	if opensPeriod(Monthly, first) {
		return monthStart
	}
	return monthStart.AddDate(0, 1, 0)
}

// windowRanges lists the windows of months months that start on
// firstStart, the first of a month, or a multiple of step months after it
// and end on or before last. Each covers [start, start + months months).
func windowRanges(firstStart, last time.Time, months, step int) []dateRange {
	var out []dateRange
	for i := 0; ; i++ {
		start := firstStart.AddDate(0, i*step, 0)
		end := start.AddDate(0, months, -1)
		if end.After(last) {
			return out
		}
		out = append(out, dateRange{start: start, end: end})
	}
}

// runWindows runs p once per range on a bounded pool of workers and
// returns the outcomes in range order. The first failing window, in range
// order, is reported.
func runWindows(p Plan, in Input, ranges []dateRange) ([]RollingWindow, error) {
	windows := make([]RollingWindow, len(ranges))
	errs := make([]error, len(ranges))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), len(ranges)) {
		wg.Go(func() {
			for i := range jobs {
				windows[i], errs[i] = runWindow(p, in, ranges[i])
			}
		})
	}
	for i := range ranges {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("sim: rolling window %d (%s to %s): %w",
				i+1, formatDate(ranges[i].start), formatDate(ranges[i].end), err)
		}
	}
	return windows, nil
}

// runWindow runs p restricted to r. p is a copy, so setting its range
// does not leak to other windows.
func runWindow(p Plan, in Input, r dateRange) (RollingWindow, error) {
	p.Start, p.End = r.start, r.end
	res, err := Run(p, in)
	if err != nil {
		return RollingWindow{}, err
	}
	return RollingWindow{
		Start:                    res.Start,
		End:                      res.End,
		Contributions:            res.Contributions,
		Invested:                 res.Invested,
		FinalValue:               res.FinalValue,
		ReturnPct:                res.ReturnPct,
		AnnualizedReturn:         res.AnnualizedReturn,
		AnnualizedReturnComputed: res.AnnualizedReturnComputed,
		MaxDrawdownPct:           res.MaxDrawdownPct,
	}, nil
}

// summarise reduces the windows to percentiles, loss probability and the
// best and worst window. windows must not be empty.
func summarise(windows []RollingWindow) *RollingResult {
	res := &RollingResult{
		Windows: windows,
		Count:   len(windows),
		Best:    windows[0],
		Worst:   windows[0],
	}
	returns := make([]float64, 0, len(windows))
	annualized := make([]float64, 0, len(windows))
	losses := 0
	for _, w := range windows {
		returns = append(returns, w.ReturnPct)
		if w.AnnualizedReturnComputed {
			annualized = append(annualized, w.AnnualizedReturn)
		}
		if w.FinalValue < w.Invested {
			losses++
		}
		if w.ReturnPct > res.Best.ReturnPct {
			res.Best = w
		}
		if w.ReturnPct < res.Worst.ReturnPct {
			res.Worst = w
		}
	}
	res.ReturnPctPercentiles = percentiles(returns)
	res.ProbLoss = float64(losses) / float64(len(windows))
	switch missing := len(windows) - len(annualized); {
	case missing == 0:
		res.AnnualizedPercentiles = percentiles(annualized)
	case len(annualized) == 0:
		res.Notes = append(res.Notes, "no window has an annualized return ("+annualizedUnavailableNote+"); AnnualizedPercentiles is nil")
	default:
		res.AnnualizedPercentiles = percentiles(annualized)
		res.Notes = append(res.Notes, fmt.Sprintf("%d of %d windows have no annualized return (%s); AnnualizedPercentiles covers the other %d",
			missing, len(windows), annualizedUnavailableNote, len(annualized)))
	}
	return res
}

// placementNotes describes how the windows were laid over the history
// that runs from first to last.
func placementNotes(c Cadence, ranges []dateRange, months, step int, first, last time.Time) []string {
	head, final := ranges[0], ranges[len(ranges)-1]
	placement := fmt.Sprintf("%d windows of %d months, each covering whole calendar months from the first of a month and starting %d month(s) after the previous one, from %s to %s; the last window ends %s and a window ending after %s (the last day every series has data for) is not run",
		len(ranges), months, step, formatDate(head.start), formatDate(final.start), formatDate(final.end), formatDate(last))
	if !opensPeriod(Monthly, first) {
		placement += fmt.Sprintf("; the common history begins mid-month on %s, so the first window starts with the first full month", formatDate(first))
	}
	notes := []string{
		placement,
		"each window is the plan run on that range alone: its first trading day contributes and later contributions follow the cadence; percentiles are over windows (sorted, linearly interpolated between ranks), ProbLoss is the share of windows whose final value is below the amount invested, and Best/Worst rank by ReturnPct",
	}
	switch c {
	case Monthly:
		notes = append(notes, fmt.Sprintf("monthly cadence: every window makes %d contributions, on the first trading day of each of its months", months))
	case Weekly:
		notes = append(notes, "weekly cadence: a window's first contribution is on its first trading day, often mid-week, and later ones on the first trading day of each following ISO week, so a one-year window makes 52 or 53")
	}
	return notes
}

// percentileLevels are the reported percentiles, the same set analytics
// reports for Monte Carlo outcomes.
var percentileLevels = []struct {
	key string
	q   float64
}{
	{"p5", 0.05}, {"p10", 0.10}, {"p25", 0.25}, {"p50", 0.50},
	{"p75", 0.75}, {"p90", 0.90}, {"p95", 0.95},
}

// percentiles maps every percentileLevels key to that quantile of values,
// which it sorts in a copy. values must not be empty.
func percentiles(values []float64) map[string]float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	out := make(map[string]float64, len(percentileLevels))
	for _, lvl := range percentileLevels {
		out[lvl.key] = percentile(sorted, lvl.q)
	}
	return out
}

// percentile returns the q-th quantile (0 <= q <= 1) of sorted values with
// linear interpolation between the two nearest ranks, the convention the
// analytics package uses.
func percentile(sorted []float64, q float64) float64 {
	rank := q * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (rank-float64(lo))*(sorted[hi]-sorted[lo])
}
