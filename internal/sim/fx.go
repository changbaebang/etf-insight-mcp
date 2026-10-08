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
// trading day that is a Korean holiday still gets a rate.
type converter struct {
	fx *market.Series // nil for USD plans
}

// newConverter builds the converter for currency. For KRW it requires a
// valid fx series with a bar on or before firstDay, the first trading day
// of the simulation.
func newConverter(currency string, fx *market.Series, firstDay time.Time) (*converter, error) {
	if currency != CurrencyKRW {
		return &converter{}, nil
	}
	if fx == nil {
		return nil, errors.New("sim: KRW plan needs an FX series (KRW per USD)")
	}
	if err := fx.Validate(); err != nil {
		return nil, fmt.Errorf("sim: fx series: %w", err)
	}
	if _, ok := fx.IndexOn(firstDay); !ok {
		return nil, fmt.Errorf("sim: fx series has no bar on or before %s", formatDate(firstDay))
	}
	return &converter{fx: fx}, nil
}

// enabled reports whether a real exchange rate is applied.
func (c *converter) enabled() bool {
	return c.fx != nil
}

// rate returns how many units of the plan currency one USD is worth on
// day.
func (c *converter) rate(day time.Time) (float64, error) {
	if c.fx == nil {
		return 1, nil
	}
	i, ok := c.fx.IndexOn(day)
	if !ok {
		return 0, fmt.Errorf("sim: fx series has no bar on or before %s", formatDate(day))
	}
	return c.fx.Bars[i].Close, nil
}
