package tools

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// longRunStart is the first bar of the twelve-year synthetic series the
// rolling tests slide many windows over (a Monday).
var longRunStart = time.Date(2012, time.January, 2, 0, 0, 0, 0, time.UTC)

// newSimulationsSource is newFakeSource plus two series the simulation
// tools need: RISE climbs every day without a wobble, so investing
// everything on the first day must beat spreading it out, and LONGRUN
// spans about twelve years (3120 weekdays from 2012-01-02) with a
// quarterly dividend, enough for more than 60 one-year windows started
// every month and for 10 or more started every year.
func newSimulationsSource() *fakeSource {
	f := newFakeSource()
	f.add(synthetic("RISE", "USD", "ETF", seriesStart, 780, func(i int) float64 {
		return 100 * math.Exp(0.001*float64(i))
	}, 0, 0))
	f.add(synthetic("LONGRUN", "USD", "ETF", longRunStart, 3120, func(i int) float64 {
		x := float64(i)
		return 50 * math.Exp(0.0003*x) * (1 + 0.08*math.Sin(x/90))
	}, 63, 0.3))
	return f
}

// simFundSource serves fund profiles from memory and answers every other
// request as the Yahoo client would for an unknown symbol.
type simFundSource struct {
	profiles map[string]*market.FundProfile
	// err, when set, is what FundProfile returns for every symbol: an
	// upstream failure rather than an unknown fund.
	err error
}

func (f simFundSource) FundProfile(_ context.Context, symbol string) (*market.FundProfile, error) {
	if f.err != nil {
		return nil, f.err
	}
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if p, ok := f.profiles[sym]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("fake fund: %s: %w", sym, market.ErrNotFound)
}

func (simFundSource) Quote(context.Context, []string) ([]market.Quote, error) { return nil, nil }

func (simFundSource) Holdings(_ context.Context, symbol string) (*market.Holdings, error) {
	return nil, fmt.Errorf("fake fund: %s: %w", symbol, market.ErrNotFound)
}

func (simFundSource) Performance(_ context.Context, symbol string) (*market.Performance, error) {
	return nil, fmt.Errorf("fake fund: %s: %w", symbol, market.ErrNotFound)
}

func (simFundSource) Search(context.Context, string, int) ([]market.SearchHit, error) {
	return nil, nil
}

func (simFundSource) News(context.Context, string, int) ([]market.NewsItem, error) {
	return nil, nil
}

// newSimFundSource knows VOO with a 0.03% expense ratio and LONGRUN with
// a name and category but no expense ratio.
func newSimFundSource() simFundSource {
	return simFundSource{profiles: map[string]*market.FundProfile{
		"VOO":     {Symbol: "VOO", Name: "Vanguard S&P 500 ETF", Category: "Large Blend", ExpenseRatio: ptr(0.0003)},
		"LONGRUN": {Symbol: "LONGRUN", Name: "Long Run Index Fund", Category: "Large Blend"},
	}}
}
