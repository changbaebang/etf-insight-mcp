package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/google/jsonschema-go/jsonschema"
)

// compare_etfs bounds: at least two symbols to compare, at most ten so the
// correlation matrix stays readable.
const (
	compareMinSymbols = 2
	compareMaxSymbols = 10
)

// compareETFsDescription is written for the model choosing a tool.
const compareETFsDescription = "Side-by-side comparison of 2 to 10 US-listed, USD-quoted ETFs and a benchmark (default SPY, listed first and added when missing); a symbol quoted in another currency is rejected. common_range is the trading days every symbol shares within start..end (default: their whole common history), so a young fund shortens it. Per symbol, measured over common_range so every row covers the same days: total return and annualized return (null under a year) with dividends reinvested, max drawdown, and beta and correlation to the benchmark from daily log returns of adjusted closes; plus the correlation matrix in the order of symbols. Also per symbol, as of common_range.to: expense ratio in percent (from fund data, null when unavailable), trailing total returns 1m to 10y (each window covers the same dates for every symbol and is unavailable for a symbol whose history is shorter), 1-year volatility, 1-year max drawdown, trailing-12-month dividend yield and trend state; notes flag a symbol with less than 52 weeks of history, whose 1-year figures cover less. Percentages are plain numbers (7.5 = 7.5%). To search the universe for funds similar to one ETF use find_alternatives; to rank many funds by one metric use screen_universe."

type compareETFsInput struct {
	Symbols   []string `json:"symbols" jsonschema:"2 to 10 ticker symbols to compare, e.g. [\"VOO\", \"QQQ\", \"SCHD\"]; case-insensitive, duplicates are dropped"`
	Start     string   `json:"start,omitempty" jsonschema:"first date YYYY-MM-DD of the range used for correlation and beta (default: the first day every symbol has)"`
	End       string   `json:"end,omitempty" jsonschema:"last date YYYY-MM-DD of that range, which is also the as-of date of returns, drawdowns, yield and trend (default: the last day every symbol has)"`
	Benchmark string   `json:"benchmark,omitempty" jsonschema:"symbol every ETF is measured against for beta and correlation, listed first (default SPY)"`
}

// compareETFRow is one symbol of a comparison.
type compareETFRow struct {
	Symbol                    string         `json:"symbol"`
	Name                      string         `json:"name"`
	Category                  string         `json:"category,omitempty" jsonschema:"universe category; empty for a symbol outside the universe"`
	IsBenchmark               bool           `json:"is_benchmark"`
	ExpenseRatioPct           *float64       `json:"expense_ratio_pct" jsonschema:"annual expense ratio in percent with 4 decimals (0.0945 = 0.0945%); null when the fund data source does not report it"`
	CommonRangeReturnPct      float64        `json:"common_range_return_pct" jsonschema:"total return with dividends reinvested from common_range.from to common_range.to, the same days for every symbol"`
	CommonRangeAnnualizedPct  *float64       `json:"common_range_annualized_pct" jsonschema:"common_range_return_pct as a compound annual rate; null when common_range spans less than 365 days"`
	MaxDrawdownCommonRangePct float64        `json:"max_drawdown_common_range_pct" jsonschema:"largest fall from a prior high within common_range, positive"`
	Returns                   []windowOutput `json:"returns" jsonschema:"trailing total returns 1m to 10y with dividends reinvested, ending at common_range.to; a window may start before common_range.from but covers the same dates for every symbol, and is unavailable for a symbol whose history is shorter"`
	Volatility1YPct           float64        `json:"volatility_1y_pct" jsonschema:"annualized volatility of the trailing 52 weeks of daily returns as of common_range.to; covers less for a symbol with a shorter history (see notes)"`
	MaxDrawdown1YPct          float64        `json:"max_drawdown_1y_pct" jsonschema:"largest fall from a prior high within the trailing 52 weeks as of common_range.to"`
	TTMDividendYieldPct       float64        `json:"ttm_dividend_yield_pct"`
	TrendState                string         `json:"trend_state" jsonschema:"uptrend, downtrend, sideways or insufficient-history as of common_range.to; a description, not a prediction"`
	BetaToBenchmark           float64        `json:"beta_to_benchmark" jsonschema:"slope of the symbol's daily log returns on the benchmark's over common_range: 1 moves with it, 2 twice as much; 0 when undefined"`
	CorrelationToBenchmark    float64        `json:"correlation_to_benchmark" jsonschema:"correlation of daily log returns with the benchmark over common_range, -1 to 1"`
}

type compareETFsOutput struct {
	Benchmark         string          `json:"benchmark"`
	Symbols           []string        `json:"symbols" jsonschema:"order of etfs and of the correlation_matrix rows and columns; the benchmark is first"`
	CommonRange       analysisRange   `json:"common_range" jsonschema:"trading days every symbol shares within start..end"`
	ETFs              []compareETFRow `json:"etfs"`
	CorrelationMatrix [][]float64     `json:"correlation_matrix" jsonschema:"correlation of daily log returns for every pair, rows and columns in the order of symbols; 0 when undefined"`
	Notes             []string        `json:"notes"`
	Warnings          []string        `json:"warnings,omitempty"`
	Disclaimer        string          `json:"disclaimer"`
}

// compareETFsSchema publishes the symbol count bounds and the benchmark
// default so a client can validate a call before sending it.
func compareETFsSchema() *jsonschema.Schema {
	s := inputSchema[compareETFsInput](schemaTweaks{
		defaults: map[string]any{"benchmark": defaultBaseline},
		minItems: map[string]int{"symbols": compareMinSymbols},
	})
	property(s, "symbols").MaxItems = ptr(compareMaxSymbols)
	return s
}

// compareETFs fetches every symbol and the benchmark concurrently and puts
// them side by side.
func (d Deps) compareETFs(ctx context.Context, in compareETFsInput) (compareETFsOutput, error) {
	start, err := parseOptionalDate("start", in.Start)
	if err != nil {
		return compareETFsOutput{}, err
	}
	end, err := parseOptionalDate("end", in.End)
	if err != nil {
		return compareETFsOutput{}, err
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		return compareETFsOutput{}, fmt.Errorf("end %s is before start %s", formatDate(end), formatDate(start))
	}
	symbols, benchmark, err := compareSymbols(in.Symbols, in.Benchmark)
	if err != nil {
		return compareETFsOutput{}, err
	}

	loaded, errs := d.fetchAll(ctx, symbols)
	if err := ctx.Err(); err != nil {
		return compareETFsOutput{}, err
	}
	if err := analysisFetchErrors(symbols, errs); err != nil {
		return compareETFsOutput{}, err
	}
	series := make([]*market.Series, len(symbols))
	for i, sym := range symbols {
		series[i] = loaded[sym]
		// Daily returns are paired by calendar date, so a fund quoted in
		// another currency (and traded in another time zone) would be
		// mispriced and its correlation measured a day out of step.
		if err := analysisRequireUSD(series[i]); err != nil {
			return compareETFsOutput{}, err
		}
	}
	cmp, err := analytics.Compare(series, start, end)
	if err != nil {
		return compareETFsOutput{}, compareRangeError(err, series)
	}
	_, aligned := analytics.CommonRange(series, start, end)
	ratios, ratioWarnings := d.analysisExpenseRatios(ctx, symbols)

	out := compareETFsOutput{
		Benchmark:         benchmark,
		Symbols:           symbols,
		CommonRange:       analysisRange{From: formatDate(cmp.From), To: formatDate(cmp.To), Bars: cmp.Bars},
		ETFs:              make([]compareETFRow, 0, len(symbols)),
		CorrelationMatrix: make([][]float64, len(symbols)),
		Notes:             compareNotes(series, cmp, start),
		Warnings:          append(append(d.analysisCacheWarnings(symbols), ratioWarnings...), nonEmpty(provisionalSummary(loaded, symbols))...),
		Disclaimer:        Disclaimer,
	}
	for i, row := range cmp.Correlation {
		out.CorrelationMatrix[i] = make([]float64, len(row))
		for j, v := range row {
			out.CorrelationMatrix[i][j] = round4(v)
		}
	}
	for i, s := range series {
		trend, err := analytics.AnalyzeTrend(s, cmp.To)
		if err != nil {
			return compareETFsOutput{}, withDataRange(err, s)
		}
		sum := toSummaryOutput(cmp.Summaries[i])
		name, category := s.Meta.Name, ""
		if e, ok := universe.Get(s.Meta.Symbol); ok {
			name, category = e.Name, e.Category
		}
		prices := aligned[i]
		row := compareETFRow{
			Symbol:                    s.Meta.Symbol,
			Name:                      name,
			Category:                  category,
			IsBenchmark:               i == 0,
			ExpenseRatioPct:           analysisExpensePct(ratios, s.Meta.Symbol),
			CommonRangeReturnPct:      pct(prices[len(prices)-1]/prices[0] - 1),
			MaxDrawdownCommonRangePct: pct(analytics.MaxDrawdown(prices)),
			Returns:                   compareWindows(sum.Windows),
			Volatility1YPct:           sum.Volatility1YPct,
			MaxDrawdown1YPct:          sum.MaxDrawdown1YPct,
			TTMDividendYieldPct:       sum.TTMDividendYieldPct,
			TrendState:                trend.State,
			BetaToBenchmark:           round4(cmp.BetaToFirst[i]),
			CorrelationToBenchmark:    round4(cmp.Correlation[i][0]),
		}
		if cagr, ok := analysisAnnualized(prices, cmp.From, cmp.To); ok {
			row.CommonRangeAnnualizedPct = ptr(pct(cagr))
		}
		out.ETFs = append(out.ETFs, row)
	}
	return out, nil
}

// compareWindows drops the "max" window: it starts at each symbol's own
// first bar, so side by side the rows would compare different periods.
// The common_range figures take its place.
func compareWindows(windows []windowOutput) []windowOutput {
	return slices.DeleteFunc(windows, func(w windowOutput) bool { return w.Label == "max" })
}

// compareSymbols normalises the requested symbols and puts the benchmark
// first, adding it when it is not among them.
func compareSymbols(requested []string, benchmark string) (symbols []string, bench string, err error) {
	syms := uniqueSymbols(requested)
	if len(syms) < compareMinSymbols {
		return nil, "", fmt.Errorf("symbols must list at least %d different tickers, got %d; to look at one ETF use get_etf_info", compareMinSymbols, len(syms))
	}
	if len(syms) > compareMaxSymbols {
		return nil, "", fmt.Errorf("symbols may list at most %d tickers, got %d; split the comparison", compareMaxSymbols, len(syms))
	}
	bench = normalizeSymbol(benchmark)
	if bench == "" {
		bench = defaultBaseline
	}
	symbols = []string{bench}
	for _, sym := range syms {
		if sym != bench {
			symbols = append(symbols, sym)
		}
	}
	return symbols, bench, nil
}

// compareRangeError explains a comparison that found too few common
// trading days by listing every symbol's own range.
func compareRangeError(err error, series []*market.Series) error {
	if !errors.Is(err, analytics.ErrInsufficientHistory) {
		return userError(err)
	}
	ranges := make([]string, 0, len(series))
	for _, s := range series {
		first, _ := s.First()
		last, _ := s.Last()
		ranges = append(ranges, fmt.Sprintf("%s %s to %s", s.Meta.Symbol, formatDate(first.Date), formatDate(last.Date)))
	}
	return fmt.Errorf("%w; data covers %s", userError(err), strings.Join(ranges, ", "))
}

// compareNotes explains how the figures were computed and which symbol
// limits the common range.
func compareNotes(series []*market.Series, cmp *analytics.Comparison, start time.Time) []string {
	notes := []string{
		fmt.Sprintf("common_range_return_pct, common_range_annualized_pct, max_drawdown_common_range_pct, correlation_matrix, correlation_to_benchmark and beta_to_benchmark cover common_range (%s to %s, %d trading days), the same days for every symbol; daily log returns of adjusted closes are paired by calendar date", formatDate(cmp.From), formatDate(cmp.To), cmp.Bars),
		fmt.Sprintf("returns, 1-year volatility and drawdown, dividend yield and trend are as of %s; a trailing window may start before common_range.from, but it covers the same dates for every symbol that has the history", formatDate(cmp.To)),
	}
	for _, s := range series {
		if note := instrumentNote(s); note != "" {
			notes = append(notes, note)
		}
	}
	if short := compareShortHistory(series, cmp.To); len(short) > 0 {
		notes = append(notes, fmt.Sprintf("%s %s less than 52 weeks of history up to %s, so 1-year volatility, 1-year drawdown and trailing-12-month dividend yield cover only that shorter span and are not full-year figures",
			strings.Join(short, ", "), compareHasOrHave(len(short)), formatDate(cmp.To)))
	}
	var youngest *market.Series
	earliest := time.Time{}
	for _, s := range series {
		first, _ := s.First()
		if youngest == nil || first.Date.After(compareFirstDate(youngest)) {
			youngest = s
		}
		if earliest.IsZero() || first.Date.Before(earliest) {
			earliest = first.Date
		}
	}
	limiting := compareFirstDate(youngest)
	if limiting.Equal(cmp.From) && (start.IsZero() && limiting.After(earliest) || !start.IsZero() && limiting.After(start)) {
		notes = append(notes, fmt.Sprintf("common_range starts on %s because %s's history starts there", formatDate(limiting), youngest.Meta.Symbol))
	}
	return notes
}

// compareShortHistory lists, in order, the symbols whose history up to
// asOf is shorter than 52 weeks.
func compareShortHistory(series []*market.Series, asOf time.Time) []string {
	var short []string
	for _, s := range series {
		if !hasTrailingYear(s, asOf) {
			short = append(short, s.Meta.Symbol)
		}
	}
	return short
}

// compareHasOrHave returns "has" for one subject and "have" for several.
func compareHasOrHave(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// compareFirstDate returns the date of the first bar of s.
func compareFirstDate(s *market.Series) time.Time {
	first, _ := s.First()
	return first.Date
}
