package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/google/jsonschema-go/jsonschema"
)

// find_alternatives knobs.
const (
	altDefaultLimit          = 8
	altMaxLimit              = 20
	altDefaultMinCorrelation = 0.9
	altDifferentCount        = 3 // least-correlated funds listed for context
	// altDifferentMaxCorrelation bounds different_exposure from above
	// whatever min_correlation is, so a strict threshold cannot fill it
	// with near-identical funds.
	altDifferentMaxCorrelation = 0.9
	altWindowYears             = 3  // comparison window ending on the base's last bar
	altMinCommonBars           = 20 // fewer common days make a correlation meaningless
	altCorrDecimals            = 1000.0
)

// altEquityCategories are searched besides the base's own category for a
// stock (or any non-bond) fund.
var altEquityCategories = []string{"US Broad Market", "US Large Cap", "US Large Growth", "US Large Value", "Dividend", "Factor"}

// altBondCategories are searched for a bond fund.
var altBondCategories = []string{"US Treasury", "US Aggregate Bond", "Corporate Bond", "High Yield Bond", "Municipal Bond", "International Bond", "Inflation Protected", "Cash / Ultra Short"}

// findAlternativesDescription is written for the model choosing a tool.
const findAlternativesDescription = "Answers 'I buy this ETF regularly; is there something similar, and how does it differ?' with facts, not advice. Candidates are universe funds in the symbol's own category plus, with include_other_categories (default true), the broad US equity categories (US Broad Market, US Large Cap, US Large Growth, US Large Value, Dividend, Factor) for a stock fund or every bond category for a bond fund; leveraged and inverse funds are never candidates. The symbol must be quoted in USD. Each candidate is measured against the symbol over their common history in the last 3 years (shorter when either fund is younger; short_history flags it): correlation and beta of daily returns, expense ratio and its difference in percentage points, trailing-12-month dividend yield, 1-year return, annualized return (null when the common history is under 365 days), 1-year volatility, max drawdown, tracking difference (candidate annualized minus the symbol's annualized) and a one-line observation. candidates keeps correlation >= min_correlation (default 0.9), ranked by correlation to 3 decimals then lower expense ratio, at most limit (default 8, max 20). different_exposure lists, for diversification context, up to 3 of the least correlated funds measured whose correlation is below 0.9 and that are not already among candidates; it does not depend on min_correlation. Returns come from adjusted closes, which are NAV-based and already net of expense ratios, so do not subtract fees again. Percentages are plain numbers (0.03 = 0.03%). The first call loads up to about 70 funds (a few seconds to a minute on a cold cache, fast afterwards); funds that fail to load are listed in skipped."

type findAlternativesInput struct {
	Symbol                 string   `json:"symbol" jsonschema:"the ETF bought today, e.g. VOO; case-insensitive"`
	Limit                  int      `json:"limit,omitempty" jsonschema:"maximum similar candidates returned, 1 to 20 (default 8)"`
	MinCorrelation         *float64 `json:"min_correlation,omitempty" jsonschema:"lowest correlation of daily returns with symbol for a fund to count as similar, -1 to 1 (default 0.9; 0.99 keeps only funds that track practically the same index)"`
	IncludeOtherCategories *bool    `json:"include_other_categories,omitempty" jsonschema:"true (default): also search the broad US equity categories (bond categories for a bond fund); false: only the symbol's own category"`
}

// alternativeBase describes the fund the alternatives are measured against.
type alternativeBase struct {
	Symbol                string   `json:"symbol"`
	Name                  string   `json:"name"`
	Category              string   `json:"category" jsonschema:"universe category; empty for a symbol outside the universe"`
	AsOf                  string   `json:"as_of" jsonschema:"last trading day of the base, the as-of date of every trailing figure"`
	ExpenseRatioPct       *float64 `json:"expense_ratio_pct" jsonschema:"annual expense ratio in percent with 4 decimals (0.03 = 0.03%); null when unavailable"`
	TTMDividendYieldPct   float64  `json:"ttm_dividend_yield_pct"`
	Return1YPct           *float64 `json:"return_1y_pct" jsonschema:"trailing 1-year total return; null with less than a year of history"`
	Return5YAnnualizedPct *float64 `json:"return_5y_annualized_pct" jsonschema:"trailing 5-year annualized total return; null with less than 5 years of history"`
	Volatility1YPct       float64  `json:"volatility_1y_pct"`
	MaxDrawdownAllPct     float64  `json:"max_drawdown_all_pct" jsonschema:"largest fall from a prior high over the whole history"`
}

// alternativeCandidate is one fund measured against the base.
type alternativeCandidate struct {
	Symbol                    string        `json:"symbol"`
	Name                      string        `json:"name"`
	Category                  string        `json:"category"`
	CorrelationToBase         float64       `json:"correlation_to_base" jsonschema:"correlation of daily log returns with the base over common_range, -1 to 1"`
	BetaToBase                float64       `json:"beta_to_base" jsonschema:"slope of the candidate's daily log returns on the base's: 1 moves with it, 2 twice as much"`
	ExpenseRatioPct           *float64      `json:"expense_ratio_pct" jsonschema:"annual expense ratio in percent with 4 decimals; null when unavailable"`
	ExpenseRatioDiffPctPoints *float64      `json:"expense_ratio_diff_pct_points" jsonschema:"candidate minus base expense ratio in percentage points, negative = cheaper; null when either is unknown"`
	TTMDividendYieldPct       float64       `json:"ttm_dividend_yield_pct"`
	Return1YPct               *float64      `json:"return_1y_pct" jsonschema:"trailing 1-year total return as of the base's as_of; null with less than a year of history"`
	Return3YAnnualizedPct     *float64      `json:"return_3y_annualized_pct" jsonschema:"annualized total return over common_range (3 years unless short_history); null when the range is under 365 days"`
	Volatility1YPct           float64       `json:"volatility_1y_pct"`
	MaxDrawdown3YPct          float64       `json:"max_drawdown_3y_pct" jsonschema:"largest fall from a prior high within common_range"`
	TrackingDifference3YPct   *float64      `json:"tracking_difference_3y_pct" jsonschema:"candidate annualized return minus base annualized return over common_range, percentage points; null when the range is under 365 days"`
	CommonRange               analysisRange `json:"common_range" jsonschema:"trading days the candidate and the base share in the comparison window"`
	ShortHistory              bool          `json:"short_history" jsonschema:"true when common_range is shorter than the window (3 years, or the base's whole history when that is shorter)"`
	Observation               string        `json:"observation" jsonschema:"one-line factual summary of how the candidate differs from the base"`
}

// altMeasured is a candidate together with the figures its ranking and
// observation need but the wire does not carry.
type altMeasured struct {
	out             alternativeCandidate
	baseMaxDD       float64  // the base's drawdown over the same common range
	correlationRank float64  // correlation rounded to three decimals
	expense         *float64 // candidate expense ratio in percent, nil when unknown
}

// altSpan is the comparison window in dates.
type altSpan struct {
	from, to time.Time
	bars     int
}

// wire renders the window for the output.
func (w altSpan) wire() analysisRange {
	return analysisRange{From: formatDate(w.from), To: formatDate(w.to), Bars: w.bars}
}

type findAlternativesOutput struct {
	Base               alternativeBase        `json:"base"`
	MinCorrelation     float64                `json:"min_correlation"`
	CategoriesSearched []string               `json:"categories_searched"`
	CommonRange        analysisRange          `json:"common_range" jsonschema:"the comparison window: up to 3 years ending on the base's last bar; a candidate's own common_range is shorter when it is younger"`
	Considered         int                    `json:"considered" jsonschema:"candidate funds measured"`
	Matched            int                    `json:"matched" jsonschema:"candidates with correlation >= min_correlation, before limit"`
	Candidates         []alternativeCandidate `json:"candidates" jsonschema:"similar funds, ranked by correlation (3 decimals) then lower expense ratio"`
	DifferentExposure  []alternativeCandidate `json:"different_exposure" jsonschema:"up to 3 of the least correlated funds measured, below correlation 0.9 and not among candidates, whatever min_correlation is; for diversification context"`
	Skipped            []analysisSkip         `json:"skipped" jsonschema:"funds that could not be measured, with the reason"`
	Notes              []string               `json:"notes"`
	Warnings           []string               `json:"warnings,omitempty"`
	Disclaimer         string                 `json:"disclaimer"`
}

// findAlternativesSchema publishes the defaults and bounds.
func findAlternativesSchema() *jsonschema.Schema {
	s := inputSchema[findAlternativesInput](schemaTweaks{
		defaults: map[string]any{"limit": altDefaultLimit, "min_correlation": altDefaultMinCorrelation, "include_other_categories": true},
	})
	limit := property(s, "limit")
	limit.Minimum, limit.Maximum = ptr(1.0), ptr(float64(altMaxLimit))
	corr := property(s, "min_correlation")
	corr.Minimum, corr.Maximum = ptr(-1.0), ptr(1.0)
	return s
}

// findAlternatives measures the universe funds near the base against it.
func (d Deps) findAlternatives(ctx context.Context, in findAlternativesInput) (findAlternativesOutput, error) {
	limit := in.Limit
	if limit == 0 {
		limit = altDefaultLimit
	}
	if limit < 1 || limit > altMaxLimit {
		return findAlternativesOutput{}, fmt.Errorf("limit must be between 1 and %d, got %d", altMaxLimit, in.Limit)
	}
	minCorr := altDefaultMinCorrelation
	if in.MinCorrelation != nil {
		minCorr = *in.MinCorrelation
	}
	if math.IsNaN(minCorr) || minCorr < -1 || minCorr > 1 {
		return findAlternativesOutput{}, fmt.Errorf("min_correlation must be between -1 and 1, got %v", minCorr)
	}
	includeOther := in.IncludeOtherCategories == nil || *in.IncludeOtherCategories

	base, err := d.fetchSeries(ctx, in.Symbol)
	if err != nil {
		return findAlternativesOutput{}, err
	}
	// Candidates are US-listed USD funds whose daily returns are paired
	// with the base's by calendar date, so the base must be one too.
	if err := analysisRequireUSD(base); err != nil {
		return findAlternativesOutput{}, err
	}
	baseSym := base.Meta.Symbol
	entry, known := universe.Get(baseSym)
	if !known && !includeOther {
		return findAlternativesOutput{}, fmt.Errorf("%s is not in the built-in universe, so it has no category to search; set include_other_categories to true or pick a universe fund with list_etfs", baseSym)
	}
	sum, err := analytics.Summarize(base, time.Time{})
	if err != nil {
		return findAlternativesOutput{}, withDataRange(err, base)
	}
	categories, pool := altPool(baseSym, entry.Category, known, includeOther)
	if len(pool) == 0 {
		return findAlternativesOutput{}, fmt.Errorf("%s's category %s holds no other non-leveraged fund to compare with; set include_other_categories to true", baseSym, entry.Category)
	}
	window := altWindow(base)

	loaded, errs := d.fetchAll(ctx, pool)
	if err := ctx.Err(); err != nil {
		return findAlternativesOutput{}, err
	}
	skipped := []analysisSkip{}
	fetched := []string{baseSym}
	var measured []altMeasured
	for _, sym := range pool {
		if err := errs[sym]; err != nil {
			skipped = append(skipped, analysisSkip{Symbol: sym, Reason: err.Error()})
			continue
		}
		fetched = append(fetched, sym)
		m, reason := altMeasure(base, loaded[sym], window, sum.AsOf)
		if reason != "" {
			skipped = append(skipped, analysisSkip{Symbol: sym, Reason: reason})
			continue
		}
		measured = append(measured, m)
	}
	similar := slices.DeleteFunc(slices.Clone(measured), func(m altMeasured) bool {
		return m.out.CorrelationToBase < minCorr
	})
	unlike := altLeastCorrelated(measured)
	// At most limit of the unlike funds can also be shown as candidates,
	// so the first limit+altDifferentCount hold every different_exposure
	// pick.
	unlike = unlike[:min(limit+altDifferentCount, len(unlike))]

	// Expense ratios are looked up only for the funds that can appear in
	// the answer: every similar one (they rank by it) and the least
	// correlated ones different_exposure picks from.
	lookup := []string{baseSym}
	for _, m := range slices.Concat(similar, unlike) {
		lookup = append(lookup, m.out.Symbol)
	}
	ratios, ratioWarnings := d.analysisExpenseRatios(ctx, lookup)
	baseER := analysisExpensePct(ratios, baseSym)
	baseYield := pct(sum.TTMDividendYield)
	finish := func(list []altMeasured) []alternativeCandidate {
		out := make([]alternativeCandidate, 0, len(list))
		for _, m := range list {
			out = append(out, altFinish(m, baseSym, entry.Category, baseER, baseYield, window))
		}
		return out
	}
	for _, list := range [][]altMeasured{similar, unlike} {
		for i := range list {
			list[i].expense = analysisExpensePct(ratios, list[i].out.Symbol)
		}
	}
	slices.SortFunc(similar, altCompareSimilar)
	shown := similar[:min(limit, len(similar))]
	different := altDifferent(unlike, shown)

	notes := altNotes(baseSym, base, known, window)
	if len(different) == 0 {
		notes = append(notes, fmt.Sprintf("different_exposure is empty: no fund measured outside candidates has a correlation below %g with %s", altDifferentMaxCorrelation, baseSym))
	}
	if short := altShortHistory(base, loaded, slices.Concat(shown, different), sum.AsOf); len(short) > 0 {
		notes = append(notes, fmt.Sprintf("%s %s less than 52 weeks of history up to %s, so 1-year volatility and trailing-12-month dividend yield cover only that shorter span and are not full-year figures",
			strings.Join(short, ", "), compareHasOrHave(len(short)), formatDate(sum.AsOf)))
	}
	if note := instrumentNote(base); note != "" {
		notes = append(notes, note)
	}

	return findAlternativesOutput{
		Base:               altBaseOutput(base, entry, sum, baseER),
		MinCorrelation:     minCorr,
		CategoriesSearched: categories,
		CommonRange:        window.wire(),
		Considered:         len(measured),
		Matched:            len(similar),
		Candidates:         finish(shown),
		DifferentExposure:  finish(different),
		Skipped:            skipped,
		Notes:              notes,
		Warnings:           append(d.analysisCacheWarnings(fetched), ratioWarnings...),
		Disclaimer:         Disclaimer,
	}, nil
}

// altLeastCorrelated returns the measured funds whose correlation with the
// base is below altDifferentMaxCorrelation, least correlated first.
func altLeastCorrelated(measured []altMeasured) []altMeasured {
	unlike := slices.DeleteFunc(slices.Clone(measured), func(m altMeasured) bool {
		return m.out.CorrelationToBase >= altDifferentMaxCorrelation
	})
	slices.SortFunc(unlike, func(a, b altMeasured) int {
		return cmp.Or(cmp.Compare(a.out.CorrelationToBase, b.out.CorrelationToBase), strings.Compare(a.out.Symbol, b.out.Symbol))
	})
	return unlike
}

// altDifferent picks the first altDifferentCount funds of unlike (least
// correlated first) that are not already shown as candidates.
func altDifferent(unlike, shown []altMeasured) []altMeasured {
	var out []altMeasured
	for _, m := range unlike {
		if len(out) == altDifferentCount {
			break
		}
		isShown := slices.ContainsFunc(shown, func(c altMeasured) bool { return c.out.Symbol == m.out.Symbol })
		if !isShown {
			out = append(out, m)
		}
	}
	return out
}

// altShortHistory lists the base and then each listed fund whose history
// up to asOf is shorter than 52 weeks.
func altShortHistory(base *market.Series, loaded map[string]*market.Series, listed []altMeasured, asOf time.Time) []string {
	var short []string
	if !hasTrailingYear(base, asOf) {
		short = append(short, base.Meta.Symbol)
	}
	for _, m := range listed {
		if s := loaded[m.out.Symbol]; s != nil && !hasTrailingYear(s, asOf) {
			short = append(short, m.out.Symbol)
		}
	}
	return short
}

// altPool lists the categories to search and their non-leveraged funds,
// the base's own category first and the base itself left out.
func altPool(baseSym, baseCategory string, known, includeOther bool) (categories, symbols []string) {
	if known && baseCategory != leveragedCategory {
		categories = append(categories, baseCategory)
	}
	if includeOther {
		switch {
		case !known:
			categories = append(categories, altEquityCategories...)
			categories = append(categories, altBondCategories...)
		case slices.Contains(altBondCategories, baseCategory):
			categories = append(categories, altBondCategories...)
		default:
			categories = append(categories, altEquityCategories...)
		}
	}
	var searched []string
	for _, c := range categories {
		if slices.Contains(searched, c) {
			continue
		}
		searched = append(searched, c)
		for _, e := range universe.Filter(universe.Query{Category: c}) {
			if e.Symbol != baseSym && !slices.Contains(symbols, e.Symbol) {
				symbols = append(symbols, e.Symbol)
			}
		}
	}
	return searched, symbols
}

// altWindow is the comparison window: the last altWindowYears of the
// base's history, or all of it when it is younger.
func altWindow(base *market.Series) altSpan {
	last, _ := base.Last()
	bars := base.Between(last.Date.AddDate(-altWindowYears, 0, 0), last.Date)
	return altSpan{from: bars[0].Date, to: last.Date, bars: len(bars)}
}

// altMeasure computes the pairwise statistics of one candidate against the
// base over their common days in window. A non-empty reason means the
// candidate cannot be measured.
func altMeasure(base, cand *market.Series, window altSpan, asOf time.Time) (altMeasured, string) {
	dates, aligned := analytics.CommonRange([]*market.Series{base, cand}, window.from, window.to)
	if len(dates) < altMinCommonBars {
		return altMeasured{}, fmt.Sprintf("only %d trading days in common with %s between %s and %s, need %d",
			len(dates), base.Meta.Symbol, formatDate(window.from), formatDate(window.to), altMinCommonBars)
	}
	corr := analytics.CorrelationMatrix(aligned)[0][1]
	if math.IsNaN(corr) {
		return altMeasured{}, "correlation is undefined: one of the price series is flat over the common range"
	}
	sum, err := analytics.Summarize(cand, asOf)
	if err != nil {
		return altMeasured{}, userError(err).Error()
	}
	beta, _ := analytics.Beta(aligned[1], aligned[0])
	first, last := dates[0], dates[len(dates)-1]
	c := alternativeCandidate{
		Symbol:              cand.Meta.Symbol,
		Name:                cand.Meta.Name,
		CorrelationToBase:   round4(corr),
		BetaToBase:          round4(analysisFinite(beta)),
		TTMDividendYieldPct: pct(sum.TTMDividendYield),
		Return1YPct:         altWindowReturn(sum, "1y"),
		Volatility1YPct:     pct(sum.Volatility1Y),
		MaxDrawdown3YPct:    pct(analytics.MaxDrawdown(aligned[1])),
		CommonRange:         analysisRange{From: formatDate(first), To: formatDate(last), Bars: len(dates)},
		ShortHistory:        first.Sub(window.from) > staleAfter || window.to.Sub(last) > staleAfter,
	}
	if e, ok := universe.Get(c.Symbol); ok {
		c.Name, c.Category = e.Name, e.Category
	}
	candAnn, candOK := analysisAnnualized(aligned[1], first, last)
	baseAnn, baseOK := analysisAnnualized(aligned[0], first, last)
	if candOK && baseOK {
		c.Return3YAnnualizedPct = ptr(pct(candAnn))
		c.TrackingDifference3YPct = ptr(pct(candAnn - baseAnn))
	}
	return altMeasured{
		out:             c,
		baseMaxDD:       analytics.MaxDrawdown(aligned[0]),
		correlationRank: math.Round(corr*altCorrDecimals) / altCorrDecimals,
	}, ""
}

// altWindowReturn returns the labelled trailing return as a percentage, or
// nil when the window is unavailable.
func altWindowReturn(sum analytics.Summary, label string) *float64 {
	for _, w := range sum.Windows {
		if w.Label == label && w.Available {
			return ptr(pct(w.TotalReturn))
		}
	}
	return nil
}

// altFinish adds the expense ratios and the observation to a measured
// candidate.
func altFinish(m altMeasured, baseSym, baseCategory string, baseER *float64, baseYieldPct float64, window altSpan) alternativeCandidate {
	c := m.out
	c.ExpenseRatioPct = m.expense
	if m.expense != nil && baseER != nil {
		c.ExpenseRatioDiffPctPoints = ptr(round4(*m.expense - *baseER))
	}
	c.Observation = altObservation(c, m, baseSym, baseCategory, baseYieldPct, window)
	return c
}

// altObservation words the main differences of a candidate from the base,
// most important first.
func altObservation(c alternativeCandidate, m altMeasured, baseSym, baseCategory string, baseYieldPct float64, window altSpan) string {
	level := "different"
	switch corr := m.correlationRank; {
	case corr >= 0.995:
		level = "practically the same"
	case corr >= 0.97:
		level = "very similar"
	case corr >= 0.9:
		level = "similar"
	}
	// Four decimals, as in correlation_to_base: three would print 0.9996
	// as a perfect 1.000.
	exposure := fmt.Sprintf("%s exposure (corr %.4f)", level, c.CorrelationToBase)
	if c.Category != "" && c.Category != baseCategory {
		exposure += ", " + c.Category + " fund"
	}
	parts := []string{exposure}
	if math.Abs(c.BetaToBase-1) >= 0.2 {
		parts = append(parts, fmt.Sprintf("beta %.2f to %s", c.BetaToBase, baseSym))
	}
	if diff := c.ExpenseRatioDiffPctPoints; diff != nil {
		switch {
		case *diff == 0:
			parts = append(parts, "same expense ratio")
		case *diff < 0:
			parts = append(parts, fmt.Sprintf("lower expense ratio by %g pp", -*diff))
		default:
			parts = append(parts, fmt.Sprintf("higher expense ratio by %g pp", *diff))
		}
	}
	if td := c.TrackingDifference3YPct; td != nil && math.Abs(*td) >= 0.25 {
		parts = append(parts, fmt.Sprintf("annualized return %+.2f pp vs %s", *td, baseSym))
	}
	baseDD := pct(m.baseMaxDD)
	if dd := c.MaxDrawdown3YPct - baseDD; dd >= 2 {
		parts = append(parts, fmt.Sprintf("deeper drawdown (%.1f%% vs %.1f%%)", c.MaxDrawdown3YPct, baseDD))
	} else if dd <= -2 {
		parts = append(parts, fmt.Sprintf("shallower drawdown (%.1f%% vs %.1f%%)", c.MaxDrawdown3YPct, baseDD))
	}
	if dy := c.TTMDividendYieldPct - baseYieldPct; dy >= 0.5 {
		parts = append(parts, fmt.Sprintf("higher dividend yield (%.2f%% vs %.2f%%)", c.TTMDividendYieldPct, baseYieldPct))
	} else if dy <= -0.5 {
		parts = append(parts, fmt.Sprintf("lower dividend yield (%.2f%% vs %.2f%%)", c.TTMDividendYieldPct, baseYieldPct))
	}
	if c.ShortHistory {
		from, _ := market.ParseDate(c.CommonRange.From)
		to, _ := market.ParseDate(c.CommonRange.To)
		parts = append(parts, fmt.Sprintf("only %.1f years of common history (window %s to %s)",
			to.Sub(from).Hours()/24/365.25, formatDate(window.from), formatDate(window.to)))
	}
	return strings.Join(parts, ", ")
}

// altCompareSimilar orders similar candidates by correlation to three
// decimals (descending), then by expense ratio (ascending, unknown last),
// then by the exact correlation and the symbol.
func altCompareSimilar(a, b altMeasured) int {
	if c := cmp.Compare(b.correlationRank, a.correlationRank); c != 0 {
		return c
	}
	switch {
	case a.expense != nil && b.expense == nil:
		return -1
	case a.expense == nil && b.expense != nil:
		return 1
	case a.expense != nil && b.expense != nil:
		if c := cmp.Compare(*a.expense, *b.expense); c != 0 {
			return c
		}
	}
	return cmp.Or(cmp.Compare(b.out.CorrelationToBase, a.out.CorrelationToBase), strings.Compare(a.out.Symbol, b.out.Symbol))
}

// altBaseOutput describes the base fund.
func altBaseOutput(base *market.Series, entry universe.ETF, sum analytics.Summary, er *float64) alternativeBase {
	out := alternativeBase{
		Symbol:              base.Meta.Symbol,
		Name:                base.Meta.Name,
		Category:            entry.Category,
		AsOf:                formatDate(sum.AsOf),
		ExpenseRatioPct:     er,
		TTMDividendYieldPct: pct(sum.TTMDividendYield),
		Return1YPct:         altWindowReturn(sum, "1y"),
		Volatility1YPct:     pct(sum.Volatility1Y),
		MaxDrawdownAllPct:   pct(sum.MaxDrawdownAll),
	}
	if entry.Name != "" {
		out.Name = entry.Name
	}
	for _, w := range sum.Windows {
		if w.Label == "5y" && w.AnnualizedAvailable {
			out.Return5YAnnualizedPct = ptr(pct(w.Annualized))
		}
	}
	return out
}

// altNotes explains the method and any limitation of this answer.
func altNotes(baseSym string, base *market.Series, known bool, window altSpan) []string {
	notes := []string{
		"returns are total returns on adjusted closes, which are NAV-based and already net of each fund's expense ratio; do not subtract expense ratios again",
		fmt.Sprintf("correlation, beta, max_drawdown_3y_pct, return_3y_annualized_pct and tracking difference use each candidate's common trading days with %s between %s and %s; the 1-year figures are as of %s",
			baseSym, formatDate(window.from), formatDate(window.to), formatDate(window.to)),
		"a high correlation means the daily moves were alike, not that the funds hold the same securities; compare index and holdings before treating two funds as interchangeable",
	}
	first, _ := base.First()
	if first.Date.Sub(window.to.AddDate(-altWindowYears, 0, 0)) > staleAfter {
		notes = append(notes, fmt.Sprintf("%s has only %.1f years of history, so every comparison uses that shorter range", baseSym, window.to.Sub(first.Date).Hours()/24/365.25))
	}
	if !known {
		notes = append(notes, fmt.Sprintf("%s is not in the built-in universe, so it has no category; the broad US equity categories and every bond category were searched", baseSym))
	}
	return notes
}
