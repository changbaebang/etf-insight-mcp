package sim

import (
	"errors"
	"fmt"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// converter turns USD amounts into the plan currency and back.
//
// For a USD plan the rate is always 1. For a KRW plan the rate on a day is
// the Close of the FX bar on that date, or of the last FX bar before it
// when the FX series has no bar that day (market.Series.IndexOn), so a US
// trading day that is a Korean holiday still gets a rate. The calendar is
// bounded by the FX series' first and last bar (see buildCalendar), so the
// fallback never reaches back before the history or past its end. A
// fallback to a bar more than a week old is reported in Result.Notes
// (see staleRun).
type converter struct {
	fx *market.Series // nil for USD plans
}

// newConverter builds the converter for currency. For KRW it requires a
// valid fx series.
func newConverter(currency string, fx *market.Series) (*converter, error) {
	if currency != CurrencyKRW {
		return &converter{}, nil
	}
	if fx == nil {
		return nil, errors.New("sim: KRW plan needs an FX series (KRW per USD)")
	}
	if err := fx.Validate(); err != nil {
		return nil, fmt.Errorf("sim: fx series: %w", err)
	}
	if fx.Len() == 0 {
		return nil, errors.New("sim: fx series has no bars")
	}
	return &converter{fx: fx}, nil
}

// enabled reports whether a real exchange rate is applied.
func (c *converter) enabled() bool {
	return c.fx != nil
}

// series returns the FX series, nil for USD plans.
func (c *converter) series() *market.Series {
	return c.fx
}

// rate returns how many units of the plan currency one USD is worth on
// day, and the date of the FX bar that rate comes from (day itself for a
// USD plan).
func (c *converter) rate(day time.Time) (float64, time.Time, error) {
	if c.fx == nil {
		return 1, day, nil
	}
	i, ok := c.fx.IndexOn(day)
	if !ok {
		return 0, time.Time{}, fmt.Errorf("sim: fx series has no bar on or before %s", formatDate(day))
	}
	b := c.fx.Bars[i]
	return b.Close, market.Day(b.Date), nil
}

// staleRateAfter is how old the FX bar behind a rate may be before the
// result says so. A week covers weekends and the longest regular holidays;
// anything older is a gap in the exchange-rate data.
const staleRateAfter = 7 * 24 * time.Hour

// staleRun is a run of consecutive trading days that all fell back to the
// same FX bar more than staleRateAfter old.
type staleRun struct {
	asOf        time.Time // the date of the FX bar used
	rate        float64
	first, last time.Time // the trading days affected
	days        int
}

// note describes the run for Result.Notes.
func (r staleRun) note() string {
	return fmt.Sprintf("KRW=X has no exchange rate for %d trading day(s) from %s to %s: they use the last earlier rate, %.2f from %s, more than a week old",
		r.days, formatDate(r.first), formatDate(r.last), r.rate, formatDate(r.asOf))
}
