package tools

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Limits and rules of get_dividends.
const (
	// maxDividendRows caps the payment list; the most recent rows are kept.
	maxDividendRows = 120
	// dividendGrowthYears is the span of growth_5y_pct.
	dividendGrowthYears = 5
	// recentFrequencyYears is how many of the latest full years
	// payments_per_year looks at, so a fund that changed its schedule is
	// labelled by the current one.
	recentFrequencyYears = 3
	// A calendar year counts as full when the data starts on or before
	// January fullYearFirstDay and ends on or after December
	// fullYearLastDay: the first and last trading days move around
	// holidays and weekends.
	fullYearFirstDay = 7
	fullYearLastDay  = 28
)

type getDividendsInput struct {
	Symbol string `json:"symbol" jsonschema:"ticker symbol, e.g. SCHD; case-insensitive"`
	Start  string `json:"start,omitempty" jsonschema:"first date YYYY-MM-DD, inclusive (default: the first bar of the history)"`
	End    string `json:"end,omitempty" jsonschema:"last date YYYY-MM-DD, inclusive (default: the latest bar); the trailing-12-month figures are measured as of this date"`
}

// dividendPayment is one cash dividend on the wire.
type dividendPayment struct {
	Date         string   `json:"date" jsonschema:"ex-dividend date as recorded in the price history"`
	Amount       float64  `json:"amount" jsonschema:"cash per share in the symbol's currency, restated for later splits to today's share count like the prices"`
	AmountAsPaid *float64 `json:"amount_as_paid,omitempty" jsonschema:"cash per share actually paid on the date, before restating for later splits; absent when no split followed"`
}

// dividendYear is one calendar year of payments.
type dividendYear struct {
	Year     int     `json:"year"`
	Total    float64 `json:"total" jsonschema:"cash per share paid in the year, restated for later splits to today's share count"`
	Payments int     `json:"payments"`
	FullYear bool    `json:"full_year" jsonschema:"false when the selected range covers only part of the year; partial years are excluded from payments_per_year and growth_5y_pct"`
}

type getDividendsOutput struct {
	Symbol          string            `json:"symbol"`
	Currency        string            `json:"currency"`
	From            string            `json:"from" jsonschema:"first bar of the selected range"`
	To              string            `json:"to" jsonschema:"last bar of the selected range"`
	Count           int               `json:"count" jsonschema:"number of payments in the range, including any not listed because of the cap"`
	Truncated       bool              `json:"truncated" jsonschema:"true when only the most recent 120 payments are listed"`
	Dividends       []dividendPayment `json:"dividends"`
	TTMAsOf         string            `json:"ttm_as_of"`
	Close           float64           `json:"close" jsonschema:"close on ttm_as_of, restated for later splits like the amounts; the denominator of ttm_yield_pct"`
	TTMTotal        float64           `json:"ttm_total" jsonschema:"cash per share paid in the 52 weeks ending on ttm_as_of, restated for later splits"`
	TTMYieldPct     float64           `json:"ttm_yield_pct"`
	PaymentsPerYear float64           `json:"payments_per_year" jsonschema:"median number of payments per full calendar year over the most recent 3 full years in the range; 0 when the range holds no full year"`
	Frequency       string            `json:"frequency" jsonschema:"monthly, quarterly, semiannual, annual, irregular, none, or unknown when no full calendar year is in the range"`
	AnnualTotals    []dividendYear    `json:"annual_totals"`
	Growth5YPct     *float64          `json:"growth_5y_pct,omitempty" jsonschema:"compound annual growth of the yearly total over the last 5 full years; absent when the range lacks 6 full years or the base year paid nothing"`
	Notes           []string          `json:"notes,omitempty"`
	Warnings        []string          `json:"warnings,omitempty"`
	Disclaimer      string            `json:"disclaimer"`
}

func (d Deps) registerGetDividends(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_dividends",
		Title:       "Get dividends",
		Description: "Cash dividend history of one symbol from its cached daily price history, optionally limited to start..end: every payment (date and cash per share, the 120 most recent when there are more), the trailing 52-week total and yield against the close on the range's last bar, the typical number of payments per year over the last 3 full years with a frequency label, per-calendar-year totals, and the 5-year compound growth of the yearly total when 6 full years are available. Use it for income questions (how much, how often, is it growing). Amounts and the close are per share in the symbol's currency, restated for later splits to today's share count like every price in this server; amount_as_paid gives the cash actually paid per share before a split, and a note names the splits. _pct fields are percentages. Payments are dated on the ex-dividend date the price source records.",
		Annotations: readOnly("Get dividends", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getDividendsInput) (*mcp.CallToolResult, getDividendsOutput, error) {
		out, err := d.getDividends(ctx, in)
		return nil, out, err
	})
}

// getDividends collects the payments of the selected range and derives
// the yearly statistics.
func (d Deps) getDividends(ctx context.Context, in getDividendsInput) (getDividendsOutput, error) {
	start, err := parseOptionalDate("start", in.Start)
	if err != nil {
		return getDividendsOutput{}, err
	}
	end, err := parseOptionalDate("end", in.End)
	if err != nil {
		return getDividendsOutput{}, err
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		return getDividendsOutput{}, fmt.Errorf("end %s is before start %s", formatDate(end), formatDate(start))
	}
	s, err := d.fetchSeries(ctx, in.Symbol)
	if err != nil {
		return getDividendsOutput{}, err
	}
	bars := s.Between(start, end)
	if len(bars) == 0 {
		first, _ := s.First()
		last, _ := s.Last()
		return getDividendsOutput{}, fmt.Errorf("no bars for %s between %s and %s; data covers %s to %s",
			s.Meta.Symbol, boundOr(start, "the first bar"), boundOr(end, "the last bar"), formatDate(first.Date), formatDate(last.Date))
	}
	from, to := bars[0], bars[len(bars)-1]
	// The trailing year may reach before start: it describes the payout
	// as of the range's last bar, like get_etf_info does.
	summary, err := analytics.Summarize(s, to.Date)
	if err != nil {
		return getDividendsOutput{}, withDataRange(err, s)
	}

	out := getDividendsOutput{
		Symbol:      s.Meta.Symbol,
		Currency:    s.Meta.Currency,
		From:        formatDate(from.Date),
		To:          formatDate(to.Date),
		Dividends:   []dividendPayment{},
		TTMAsOf:     formatDate(summary.AsOf),
		Close:       round2(summary.Close),
		TTMTotal:    round4(summary.TTMDividendPerShare),
		TTMYieldPct: pct(summary.TTMDividendYield),
		Disclaimer:  Disclaimer,
	}
	for _, b := range bars {
		if b.Dividend > 0 {
			out.Count++
			pay := dividendPayment{Date: formatDate(b.Date), Amount: round4(b.Dividend)}
			if f := splitFactorAfter(s.Splits, b.Date); f != 1 {
				pay.AmountAsPaid = ptr(round4(b.Dividend * f))
			}
			out.Dividends = append(out.Dividends, pay)
		}
	}
	if note := splitNote(s.Splits, from.Date); note != "" {
		out.Notes = append(out.Notes, note)
	}
	if !hasTrailingYear(s, to.Date) {
		first, _ := s.First()
		out.Notes = append(out.Notes, fmt.Sprintf("ttm_total and ttm_yield_pct cover only the %d days of history before ttm_as_of, not a full 52 weeks", int(to.Date.Sub(first.Date).Hours()/24)))
	}
	if len(out.Dividends) > maxDividendRows {
		out.Dividends = out.Dividends[len(out.Dividends)-maxDividendRows:]
		out.Truncated = true
		out.Notes = append(out.Notes, fmt.Sprintf("%d payments in range, only the most recent %d are listed; narrow start/end to see older ones", out.Count, maxDividendRows))
	}

	years := dividendYears(bars)
	out.AnnualTotals = make([]dividendYear, 0, len(years))
	for _, y := range years {
		out.AnnualTotals = append(out.AnnualTotals, dividendYear{Year: y.year, Total: round4(y.total), Payments: y.payments, FullYear: y.full})
	}
	full := fullDividendYears(years)
	out.PaymentsPerYear, out.Frequency = dividendFrequency(full)
	if len(full) == 0 {
		out.Notes = append(out.Notes, "the range holds no full calendar year, so payments_per_year is 0 and frequency is unknown")
	}
	if len(full) > recentFrequencyYears {
		if _, longRun := medianFrequency(full); longRun != out.Frequency {
			recent := full[len(full)-recentFrequencyYears:]
			out.Notes = append(out.Notes, fmt.Sprintf("the schedule changed: payments_per_year and frequency follow the last %d full years (%d-%d), while over all %d full years in the range the typical schedule was %s",
				recentFrequencyYears, recent[0].year, recent[len(recent)-1].year, len(full), longRun))
		}
	}
	growth, note := dividendGrowth(full)
	out.Growth5YPct = growth
	out.Notes = append(out.Notes, note)
	if out.Count == 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("no dividend was paid between %s and %s", out.From, out.To))
	}
	out.Warnings = d.staleWarnings(s.Meta.Symbol)
	return out, nil
}

// divYear accumulates one calendar year of a bar range.
type divYear struct {
	year     int
	total    float64
	payments int
	full     bool
}

// dividendYears sums the payments of bars (ascending, non-empty) per
// calendar year, every year the range touches included, and marks the
// years the range covers from first to last trading day.
func dividendYears(bars []market.Bar) []divYear {
	first, last := bars[0].Date, bars[len(bars)-1].Date
	years := make([]divYear, 0, last.Year()-first.Year()+1)
	for y := first.Year(); y <= last.Year(); y++ {
		years = append(years, divYear{
			year: y,
			full: !first.After(time.Date(y, time.January, fullYearFirstDay, 0, 0, 0, 0, time.UTC)) &&
				!last.Before(time.Date(y, time.December, fullYearLastDay, 0, 0, 0, 0, time.UTC)),
		})
	}
	for _, b := range bars {
		if b.Dividend > 0 {
			y := &years[b.Date.Year()-first.Year()]
			y.total += b.Dividend
			y.payments++
		}
	}
	return years
}

// fullDividendYears keeps the full years, ascending.
func fullDividendYears(years []divYear) []divYear {
	var out []divYear
	for _, y := range years {
		if y.full {
			out = append(out, y)
		}
	}
	return out
}

// dividendFrequency returns the median payment count of the most recent
// recentFrequencyYears full years and a label for it.
func dividendFrequency(full []divYear) (float64, string) {
	if len(full) > recentFrequencyYears {
		full = full[len(full)-recentFrequencyYears:]
	}
	return medianFrequency(full)
}

// medianFrequency returns the median payment count of years and a label
// for it.
func medianFrequency(years []divYear) (float64, string) {
	if len(years) == 0 {
		return 0, "unknown"
	}
	counts := make([]float64, 0, len(years))
	for _, y := range years {
		counts = append(counts, float64(y.payments))
	}
	sort.Float64s(counts)
	mid := len(counts) / 2
	median := counts[mid]
	if len(counts)%2 == 0 {
		median = (counts[mid-1] + counts[mid]) / 2
	}
	switch n := math.Round(median); {
	case median == 0:
		return 0, "none"
	case n >= 11 && n <= 13:
		return median, "monthly"
	case n >= 3 && n <= 5:
		return median, "quarterly"
	case n == 2:
		return median, "semiannual"
	case n == 1:
		return median, "annual"
	default:
		return median, "irregular"
	}
}

// dividendGrowth is the compound annual growth of the yearly total from
// the full year five years before the last full year to the last full
// year, with a note saying which years were compared or why it is absent.
func dividendGrowth(full []divYear) (*float64, string) {
	if len(full) < dividendGrowthYears+1 {
		return nil, fmt.Sprintf("growth_5y_pct needs %d full calendar years in the range, found %d", dividendGrowthYears+1, len(full))
	}
	last := full[len(full)-1]
	base := full[len(full)-1-dividendGrowthYears]
	if base.total <= 0 {
		return nil, fmt.Sprintf("growth_5y_pct is absent: %d paid no dividend", base.year)
	}
	g := math.Pow(last.total/base.total, 1/float64(dividendGrowthYears)) - 1
	return ptr(pct(g)), fmt.Sprintf("growth_5y_pct compares the %d total %.4f with the %d total %.4f", base.year, base.total, last.year, last.total)
}

// splitFactorAfter is how many of today's shares one share held on date
// became through the splits after it: the factor that turns a restated
// per-share amount back into the amount paid on date.
func splitFactorAfter(splits []market.Split, date time.Time) float64 {
	f := 1.0
	for _, sp := range splits {
		if sp.Date.After(date) && sp.Numerator > 0 && sp.Denominator > 0 {
			f *= sp.Numerator / sp.Denominator
		}
	}
	return f
}

// splitNote names the splits after from, which restated the amounts and
// the close of the range to today's share count, or returns "".
func splitNote(splits []market.Split, from time.Time) string {
	var named []string
	for _, sp := range splits {
		if sp.Date.After(from) {
			named = append(named, fmt.Sprintf("a %s split on %s", sp.Ratio(), formatDate(sp.Date)))
		}
	}
	if len(named) == 0 {
		return ""
	}
	return fmt.Sprintf("amounts, totals and close dated before %s are restated to today's share count like the prices; amount_as_paid shows the cash per share actually paid then", strings.Join(named, " and "))
}
