package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// analysisToolNames are the tools the analysis group registers.
var analysisToolNames = []string{"get_technical_indicators", "compare_etfs", "screen_universe", "find_alternatives"}

// analysisDelisted is the last bar of the DIA fixture, which stops half a
// year before the other series like a fund that was closed.
var analysisDelisted = time.Date(2023, time.June, 30, 0, 0, 0, 0, time.UTC)

// newAnalysisSource extends the shared fake with funds whose relationship
// to VOO, SPY or BND is known exactly:
//
//   - IVV, SPYM, RSP (from 2023-06-01), DIA (to 2023-06-30) and DFAC (from
//     2023-03-01) are VOO scaled by a constant: identical daily returns,
//     so correlation and beta to VOO are exactly 1;
//   - MTUM is proportional to VOO squared and SSO to SPY squared: their
//     daily log returns are exactly twice the base's, so beta is 2 and
//     correlation 1;
//   - QQQ is VOO with a small fast wobble added: highly but not perfectly
//     correlated;
//   - COWZ holds only VOO's last 10 bars, too few to measure;
//   - XLK rises every day (RSI 100);
//   - BND and AGG (BND scaled) are bonds with correlation 1, TLT an
//     unrelated bond path.
//
// SPY and SCHD follow their own wobbles and are weakly correlated with VOO.
func newAnalysisSource() *fakeSource {
	f := newFakeSource()
	voo, spy := f.series["VOO"], f.series["SPY"]
	scaled := func(k float64) func(int, float64) float64 {
		return func(_ int, p float64) float64 { return k * p }
	}
	squared := func(k float64) func(int, float64) float64 {
		return func(_ int, p float64) float64 { return k * p * p }
	}
	f.add(analysisDerived("IVV", voo, time.Time{}, time.Time{}, scaled(1.1)))
	f.add(analysisDerived("SPYM", voo, time.Time{}, time.Time{}, scaled(0.2)))
	f.add(analysisDerived("RSP", voo, time.Date(2023, time.June, 1, 0, 0, 0, 0, time.UTC), time.Time{}, scaled(0.5)))
	f.add(analysisDerived("DIA", voo, time.Time{}, analysisDelisted, scaled(1.2)))
	f.add(analysisDerived("DFAC", voo, time.Date(2023, time.March, 1, 0, 0, 0, 0, time.UTC), time.Time{}, scaled(0.08)))
	f.add(analysisDerived("MTUM", voo, time.Time{}, time.Time{}, squared(0.001)))
	f.add(analysisDerived("SSO", spy, time.Time{}, time.Time{}, squared(0.002)))
	f.add(analysisDerived("QQQ", voo, time.Time{}, time.Time{}, func(i int, p float64) float64 {
		return 1.05 * p * (1 + 0.001*math.Sin(float64(i)/3))
	}))
	last, _ := voo.Last()
	f.add(analysisDerived("COWZ", voo, last.Date.AddDate(0, 0, -13), time.Time{}, scaled(0.6)))
	f.add(synthetic("XLK", "USD", "ETF", time.Date(2022, time.November, 1, 0, 0, 0, 0, time.UTC), 300, func(i int) float64 {
		return 100 * math.Pow(1.001, float64(i))
	}, 0, 0))
	bond := func(base, drift, amp, period, phase float64) func(int) float64 {
		return func(i int) float64 {
			x := float64(i)
			return base * math.Exp(drift*x) * (1 + amp*math.Sin(x/period+phase))
		}
	}
	f.add(synthetic("BND", "USD", "ETF", seriesStart, 780, bond(80, 0.00005, 0.01, 25, 0.5), 21, 0.2))
	f.add(analysisDerived("AGG", f.series["BND"], time.Time{}, time.Time{}, scaled(1.25)))
	f.add(synthetic("TLT", "USD", "ETF", seriesStart, 780, bond(140, -0.0002, 0.05, 19, 3), 21, 0.3))
	return f
}

// analysisDerived builds symbol from src's bars within [from, to] (zero =
// unbounded), mapping each close through price(i, close) where i counts
// src's bars from its start. Close and AdjClose are both the mapped value
// and dividends scale with the price, so a scaled copy has the same yield.
func analysisDerived(symbol string, src *market.Series, from, to time.Time, price func(i int, p float64) float64) *market.Series {
	s := &market.Series{Meta: src.Meta}
	s.Meta.Symbol, s.Meta.Name = symbol, symbol+" synthetic"
	for i, b := range src.Bars {
		if (!from.IsZero() && b.Date.Before(from)) || (!to.IsZero() && b.Date.After(to)) {
			continue
		}
		p := price(i, b.AdjClose)
		s.Bars = append(s.Bars, market.Bar{Date: b.Date, Close: p, AdjClose: p, Dividend: b.Dividend * p / b.AdjClose})
	}
	s.Meta.FirstTradeDate = s.Bars[0].Date
	return s
}

// analysisFakeFund serves fund profiles from memory. Symbols in fail
// return a network-style error; unknown symbols wrap market.ErrNotFound.
// It counts FundProfile calls so a test can see which symbols were looked
// up.
type analysisFakeFund struct {
	mu       sync.Mutex
	expense  map[string]*float64
	fail     map[string]bool
	profiled map[string]int
}

// newAnalysisFakeFund knows the expense ratios of the analysis fixtures;
// DFAC has a profile without an expense ratio.
func newAnalysisFakeFund() *analysisFakeFund {
	return &analysisFakeFund{
		expense: map[string]*float64{
			"VOO": ptr(0.0003), "IVV": ptr(0.0003), "SPYM": ptr(0.0002), "SPY": ptr(0.000945),
			"MTUM": ptr(0.0015), "DIA": ptr(0.0016), "RSP": ptr(0.002), "QQQ": ptr(0.002), "SCHD": ptr(0.0006),
			"SSO": ptr(0.0089), "DFAC": nil, "BND": ptr(0.0003), "AGG": ptr(0.0003), "TLT": ptr(0.0015),
		},
		fail:     map[string]bool{},
		profiled: map[string]int{},
	}
}

var errAnalysisFakeNetwork = errors.New("fake: connection reset")

func (f *analysisFakeFund) FundProfile(_ context.Context, symbol string) (*market.FundProfile, error) {
	sym := strings.ToUpper(symbol)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiled[sym]++
	if f.fail[sym] {
		return nil, fmt.Errorf("fake: %s: %w", sym, errAnalysisFakeNetwork)
	}
	er, ok := f.expense[sym]
	if !ok {
		return nil, fmt.Errorf("fake: %s: %w", sym, market.ErrNotFound)
	}
	return &market.FundProfile{Symbol: sym, Name: sym + " fund", ExpenseRatio: er}, nil
}

// lookups returns how often symbol's profile was requested.
func (f *analysisFakeFund) lookups(symbol string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profiled[symbol]
}

func (f *analysisFakeFund) Quote(context.Context, []string) ([]market.Quote, error) {
	return nil, errors.New("fake: quotes are not used by the analysis tools")
}

func (f *analysisFakeFund) Holdings(context.Context, string) (*market.Holdings, error) {
	return nil, errors.New("fake: holdings are not used by the analysis tools")
}

func (f *analysisFakeFund) Performance(context.Context, string) (*market.Performance, error) {
	return nil, errors.New("fake: performance is not used by the analysis tools")
}

func (f *analysisFakeFund) Search(context.Context, string, int) ([]market.SearchHit, error) {
	return nil, errors.New("fake: search is not used by the analysis tools")
}

func (f *analysisFakeFund) News(context.Context, string, int) ([]market.NewsItem, error) {
	return nil, errors.New("fake: news is not used by the analysis tools")
}

// analysisDeps wires the analysis fixtures and, when fund is not nil, the
// fake fund source.
func analysisDeps(src *fakeSource, fund *analysisFakeFund) Deps {
	deps := testDeps(src)
	if fund != nil {
		deps.Fund = fund
	}
	return deps
}

// analysisFlakySource serves the wrapped fake until down is set, then
// fails every request like an unreachable provider.
type analysisFlakySource struct {
	*fakeSource
	down atomic.Bool
}

func (f *analysisFlakySource) Series(ctx context.Context, symbol string) (*market.Series, error) {
	if f.down.Load() {
		return nil, fmt.Errorf("fake: %s: %w", symbol, errAnalysisFakeNetwork)
	}
	return f.fakeSource.Series(ctx, symbol)
}
