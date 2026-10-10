package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerAnalysis adds the analysis tool group: technical indicators,
// comparisons, screening and alternatives.
func (d Deps) registerAnalysis(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_technical_indicators",
		Title:       "Get technical indicators",
		Description: getTechnicalIndicatorsDescription,
		Annotations: readOnly("Get technical indicators", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getTechnicalIndicatorsInput) (*mcp.CallToolResult, getTechnicalIndicatorsOutput, error) {
		out, err := d.getTechnicalIndicators(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "compare_etfs",
		Title:       "Compare ETFs",
		Description: compareETFsDescription,
		Annotations: readOnly("Compare ETFs", true),
		InputSchema: compareETFsSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in compareETFsInput) (*mcp.CallToolResult, compareETFsOutput, error) {
		out, err := d.compareETFs(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "screen_universe",
		Title:       "Screen the ETF universe",
		Description: screenUniverseDescription(),
		Annotations: readOnly("Screen the ETF universe", true),
		InputSchema: screenUniverseSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in screenUniverseInput) (*mcp.CallToolResult, screenUniverseOutput, error) {
		out, err := d.screenUniverse(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_alternatives",
		Title:       "Find alternative ETFs",
		Description: findAlternativesDescription,
		Annotations: readOnly("Find alternative ETFs", true),
		InputSchema: findAlternativesSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in findAlternativesInput) (*mcp.CallToolResult, findAlternativesOutput, error) {
		out, err := d.findAlternatives(ctx, in)
		return nil, out, err
	})
}

// analysisRange is a span of common trading days on the wire.
type analysisRange struct {
	From string `json:"from" jsonschema:"first trading day of the range"`
	To   string `json:"to" jsonschema:"last trading day of the range"`
	Bars int    `json:"bars" jsonschema:"number of trading days in the range"`
}

// analysisSkip names a symbol a multi-symbol tool left out and why.
type analysisSkip struct {
	Symbol string `json:"symbol"`
	Reason string `json:"reason"`
}

// analysisMaxWarnings caps how many per-symbol cache warnings one
// multi-symbol answer repeats; the rest are counted.
const analysisMaxWarnings = 5

// analysisExpenseRatios looks up the expense ratio of each symbol through
// Deps.Fund, at most prefetchConcurrency at a time. The map holds the
// ratios as fractions (0.0003 = 0.03%) for the symbols that reported one;
// a symbol the source does not know or that reports no ratio is simply
// absent. The warning, when not empty, says which lookups failed or that
// no fund source is configured.
func (d Deps) analysisExpenseRatios(ctx context.Context, symbols []string) (map[string]float64, []string) {
	ratios := make(map[string]float64, len(symbols))
	if len(symbols) == 0 {
		return ratios, nil
	}
	if d.Fund == nil {
		return ratios, []string{"expense ratios are unavailable: no fund data source is configured, so expense_ratio_pct is null"}
	}
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed []string
		sem    = make(chan struct{}, prefetchConcurrency)
	)
	for _, sym := range uniqueSymbols(symbols) {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			p, err := d.Fund.FundProfile(ctx, sym)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed = append(failed, sym)
			case p != nil && p.ExpenseRatio != nil && !math.IsNaN(*p.ExpenseRatio) && !math.IsInf(*p.ExpenseRatio, 0):
				ratios[sym] = *p.ExpenseRatio
			}
		})
	}
	wg.Wait()
	if len(failed) == 0 {
		return ratios, nil
	}
	sort.Strings(failed)
	return ratios, []string{fmt.Sprintf("expense ratio lookup failed for %s; their expense_ratio_pct is null", strings.Join(failed, ", "))}
}

// analysisExpensePct renders the expense ratio of sym as a percentage with
// four decimals (0.0945 for SPY: two would round it to 0.09), or nil when
// it is unknown.
func analysisExpensePct(ratios map[string]float64, sym string) *float64 {
	r, ok := ratios[sym]
	if !ok {
		return nil
	}
	return ptr(round4(r * 100))
}

// analysisPctOrNil turns a fraction into a rounded percentage, or nil when
// it is NaN or infinite (not computable).
func analysisPctOrNil(fraction float64) *float64 {
	if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		return nil
	}
	return ptr(pct(fraction))
}

// analysisRound2OrNil rounds a value that is already a percentage, or
// returns nil when it is NaN or infinite.
func analysisRound2OrNil(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return ptr(round2(v))
}

// analysisFinite maps NaN and ±Inf to 0 so a value can be marshalled.
func analysisFinite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// analysisCacheWarnings gathers the cache warnings of the symbols that
// were loaded (a symbol that failed is reported with its reason instead),
// keeping the first analysisMaxWarnings and counting the rest so a screen
// of the whole universe cannot flood the answer.
func (d Deps) analysisCacheWarnings(symbols []string) []string {
	var all []string
	for _, sym := range symbols {
		all = append(all, d.staleWarnings(sym)...)
	}
	if len(all) <= analysisMaxWarnings {
		return all
	}
	out := append([]string{}, all[:analysisMaxWarnings]...)
	return append(out, fmt.Sprintf("%d more cache warnings omitted; cache_status lists them all", len(all)-analysisMaxWarnings))
}

// analysisFetchErrors joins the fetch errors of symbols, in order, into
// one message for a tool error, or returns nil when none failed.
func analysisFetchErrors(symbols []string, errs map[string]error) error {
	var msgs []string
	for _, sym := range symbols {
		if err := errs[sym]; err != nil {
			msgs = append(msgs, err.Error())
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return errors.New(strings.Join(msgs, "; "))
}
