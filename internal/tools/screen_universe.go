package tools

import (
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

// screenDefaultLimit is how many rows screen_universe returns by default.
const screenDefaultLimit = 20

// screenMaxSkipped caps the skipped list so a screen as of an early date,
// when most funds did not exist yet, cannot bury the ranking under a
// hundred skip reasons; skipped_count keeps the full number. The skipped
// field's schema text quotes this number.
const screenMaxSkipped = 20

// screenMaxLimit lets one call return the whole universe.
var screenMaxLimit = len(universe.All())

// screenUniverseDescription is written for the model choosing a tool; it
// quotes the universe size, so it is built at registration.
func screenUniverseDescription() string {
	screened := len(universe.Filter(universe.Query{}))
	return fmt.Sprintf("Ranks the built-in ETF universe by one metric. sort_by is momentum_12_1 (return from 12 months to 1 month ago), return_1y, return_3m, volatility_1y, max_drawdown_1y, dividend_yield (trailing 12 months over the close) or pct_vs_sma_200 (close versus its 200-day average); descending defaults to true (highest first; pass false to put the calmest or shallowest first). Every row carries all seven metrics and the trend state, as plain percentages (7.5 = 7.5%%); a metric is null when the fund's history is too short for it (most need a full year). Funds without enough history for sort_by, or that failed to load, are listed in skipped with the reason (at most %d; skipped_count has the total). Returns at most limit rows (default %d, max %d); leveraged and inverse funds are left out unless include_leveraged. COLD START: the first call loads the full daily history of every screened fund (about %d symbols; 10 to 90 seconds depending on the network); later calls read the on-disk cache and take well under a second. Narrow with category to load fewer funds, and use refresh_prices when the cache is stale. A ranking of past figures, not a recommendation.", screenMaxSkipped, screenDefaultLimit, screenMaxLimit, screened)
}

type screenUniverseInput struct {
	SortBy           string `json:"sort_by" jsonschema:"metric to rank by: momentum_12_1, return_1y, return_3m, volatility_1y, max_drawdown_1y, dividend_yield or pct_vs_sma_200"`
	Descending       *bool  `json:"descending,omitempty" jsonschema:"true (default) puts the highest value first; false the lowest, e.g. the calmest funds for volatility_1y or the shallowest drawdowns for max_drawdown_1y"`
	Category         string `json:"category,omitempty" jsonschema:"screen only one universe category, case-insensitive, e.g. Dividend or US Large Cap (list_etfs returns every name); loads far fewer funds on a cold cache"`
	IncludeLeveraged bool   `json:"include_leveraged,omitempty" jsonschema:"also screen leveraged and inverse funds (default false); set automatically when category is 'Leveraged / Inverse'"`
	Limit            int    `json:"limit,omitempty" jsonschema:"maximum rows returned (default 20)"`
	AsOf             string `json:"as_of,omitempty" jsonschema:"rank as of this date YYYY-MM-DD, each fund on its last trading day on or before it (default: each fund's latest bar)"`
}

// screenRow is one ranked fund. Metrics are null when the history is too
// short for them.
type screenRow struct {
	Rank             int      `json:"rank"`
	Symbol           string   `json:"symbol"`
	Name             string   `json:"name"`
	Category         string   `json:"category"`
	Momentum121Pct   *float64 `json:"momentum_12_1_pct" jsonschema:"return from 12 months to 1 month ago; null with fewer than 253 bars"`
	Return1YPct      *float64 `json:"return_1y_pct" jsonschema:"trailing 1-year total return with dividends reinvested"`
	Return3MPct      *float64 `json:"return_3m_pct" jsonschema:"trailing 3-month total return with dividends reinvested"`
	Volatility1YPct  *float64 `json:"volatility_1y_pct" jsonschema:"annualized volatility of the last year of daily returns"`
	MaxDrawdown1YPct *float64 `json:"max_drawdown_1y_pct" jsonschema:"largest fall from a prior high within the trailing year, positive"`
	DividendYieldPct *float64 `json:"dividend_yield_pct" jsonschema:"trailing-12-month dividends over the close"`
	PctVsSMA200      *float64 `json:"pct_vs_sma_200" jsonschema:"close versus its 200-day average in percent; null with fewer than 200 bars"`
	Trend            string   `json:"trend" jsonschema:"uptrend, downtrend, sideways or insufficient-history"`
}

type screenUniverseOutput struct {
	SortBy       string         `json:"sort_by"`
	Descending   bool           `json:"descending"`
	Category     string         `json:"category,omitempty"`
	AsOf         string         `json:"as_of" jsonschema:"latest trading day used; warnings name funds whose last bar is older"`
	Screened     int            `json:"screened" jsonschema:"funds in the screened set"`
	Matched      int            `json:"matched" jsonschema:"funds with a value for sort_by, before limit"`
	Truncated    bool           `json:"truncated" jsonschema:"true when matched exceeds limit and only the first rows are returned"`
	Ranked       []screenRow    `json:"ranked"`
	Skipped      []analysisSkip `json:"skipped" jsonschema:"funds without enough history for sort_by or that failed to load, the first 20 in universe order"`
	SkippedCount int            `json:"skipped_count" jsonschema:"number of funds skipped, including those beyond the listed 20"`
	Warnings     []string       `json:"warnings,omitempty"`
	Disclaimer   string         `json:"disclaimer"`
}

// screenUniverseSchema publishes the sort keys as an enum, the defaults
// and the limit bounds.
func screenUniverseSchema() *jsonschema.Schema {
	s := inputSchema[screenUniverseInput](schemaTweaks{
		enums:    map[string][]string{"sort_by": analytics.RankKeys},
		defaults: map[string]any{"descending": true, "include_leveraged": false, "limit": screenDefaultLimit},
	})
	limit := property(s, "limit")
	limit.Description = fmt.Sprintf("maximum rows returned, 1 to %d (default %d)", screenMaxLimit, screenDefaultLimit)
	limit.Minimum = ptr(1.0)
	limit.Maximum = ptr(float64(screenMaxLimit))
	return s
}

// screenUniverse scores every fund of the screened set and ranks them.
func (d Deps) screenUniverse(ctx context.Context, in screenUniverseInput) (screenUniverseOutput, error) {
	sortBy := strings.ToLower(strings.TrimSpace(in.SortBy))
	if !slices.Contains(analytics.RankKeys, sortBy) {
		return screenUniverseOutput{}, fmt.Errorf("sort_by %q is not supported; use one of %s", in.SortBy, strings.Join(analytics.RankKeys, ", "))
	}
	limit := in.Limit
	if limit == 0 {
		limit = screenDefaultLimit
	}
	if limit < 1 || limit > screenMaxLimit {
		return screenUniverseOutput{}, fmt.Errorf("limit must be between 1 and %d, got %d", screenMaxLimit, in.Limit)
	}
	category := strings.TrimSpace(in.Category)
	if categories := universe.Categories(); category != "" && !containsFold(categories, category) {
		return screenUniverseOutput{}, fmt.Errorf("unknown category %q; valid categories: %s", in.Category, strings.Join(categories, ", "))
	}
	asOf, err := parseOptionalDate("as_of", in.AsOf)
	if err != nil {
		return screenUniverseOutput{}, err
	}
	descending := in.Descending == nil || *in.Descending

	etfs := universe.Filter(universe.Query{
		Category:         category,
		IncludeLeveraged: in.IncludeLeveraged || strings.EqualFold(category, leveragedCategory),
	})
	symbols := make([]string, 0, len(etfs))
	for _, e := range etfs {
		symbols = append(symbols, e.Symbol)
	}
	series, errs := d.fetchAll(ctx, symbols)
	// A cancelled request leaves most funds unloaded; ranking the rest
	// would present a partial screen as complete.
	if err := ctx.Err(); err != nil {
		return screenUniverseOutput{}, err
	}

	var (
		scores  []analytics.Score
		skipped = []analysisSkip{}
		anchors = make(map[string]time.Time, len(series))
	)
	var fetched []string
	for _, sym := range symbols {
		if err := errs[sym]; err != nil {
			skipped = append(skipped, analysisSkip{Symbol: sym, Reason: err.Error()})
			continue
		}
		fetched = append(fetched, sym)
		s := series[sym]
		sc, err := analytics.ScoreSeries(s, asOf)
		if err != nil {
			skipped = append(skipped, analysisSkip{Symbol: sym, Reason: withDataRange(err, s).Error()})
			continue
		}
		if v, _ := sc.Value(sortBy); math.IsNaN(v) {
			skipped = append(skipped, analysisSkip{Symbol: sym, Reason: screenShortReason(sortBy, sc.Bars)})
			continue
		}
		scores = append(scores, sc)
		anchors[sym] = screenAnchor(s, asOf)
	}
	ranked, err := analytics.RankScores(scores, sortBy, descending)
	if err != nil {
		return screenUniverseOutput{}, userError(err)
	}

	out := screenUniverseOutput{
		SortBy:       sortBy,
		Descending:   descending,
		Category:     screenCategoryName(category),
		Screened:     len(symbols),
		Matched:      len(ranked),
		Truncated:    len(ranked) > limit,
		Ranked:       make([]screenRow, 0, min(limit, len(ranked))),
		Skipped:      skipped[:min(screenMaxSkipped, len(skipped))],
		SkippedCount: len(skipped),
		Disclaimer:   Disclaimer,
	}
	for i, sc := range ranked[:min(limit, len(ranked))] {
		e, _ := universe.Get(sc.Symbol)
		out.Ranked = append(out.Ranked, screenRow{
			Rank:             i + 1,
			Symbol:           sc.Symbol,
			Name:             e.Name,
			Category:         e.Category,
			Momentum121Pct:   analysisPctOrNil(sc.Momentum12_1),
			Return1YPct:      analysisPctOrNil(sc.Return1Y),
			Return3MPct:      analysisPctOrNil(sc.Return3M),
			Volatility1YPct:  analysisPctOrNil(sc.Volatility1Y),
			MaxDrawdown1YPct: analysisPctOrNil(sc.MaxDrawdown1Y),
			DividendYieldPct: analysisPctOrNil(sc.DividendYield),
			PctVsSMA200:      analysisRound2OrNil(sc.PctVsSMA200),
			Trend:            sc.Trend,
		})
	}
	latest, lagging := screenLagging(anchors)
	out.AsOf = formatDate(latest)
	if len(lagging) > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("last bar more than a week before %s, so their figures are older: %s", out.AsOf, strings.Join(lagging, ", ")))
	}
	out.Warnings = append(out.Warnings, d.analysisCacheWarnings(fetched)...)
	out.Warnings = append(out.Warnings, nonEmpty(provisionalSummary(series, fetched))...)
	return out, nil
}

// screenShortReason explains why a fund has no value for the sort key.
func screenShortReason(sortBy string, bars int) string {
	need := "a full year of history"
	switch sortBy {
	case analytics.RankByMomentum12_1:
		need = "253 bars"
	case analytics.RankByReturn3M:
		need = "3 months of history"
	case analytics.RankByPctVsSMA200:
		need = "200 bars"
	}
	return fmt.Sprintf("insufficient history for %s: %d bars up to the as-of date, needs %s", sortBy, bars, need)
}

// screenAnchor is the date a fund was scored on: its last bar on or before
// asOf (zero: its last bar).
func screenAnchor(s *market.Series, asOf time.Time) time.Time {
	if asOf.IsZero() {
		last, _ := s.Last()
		return last.Date
	}
	idx, _ := s.IndexOn(asOf)
	return s.Bars[idx].Date
}

// screenLagging returns the latest anchor and, sorted, the symbols whose
// anchor is more than staleAfter older (a delisted fund or a stale
// cache), each with its date.
func screenLagging(anchors map[string]time.Time) (latest time.Time, lagging []string) {
	for _, t := range anchors {
		if t.After(latest) {
			latest = t
		}
	}
	for sym, t := range anchors {
		if latest.Sub(t) > staleAfter {
			lagging = append(lagging, fmt.Sprintf("%s (%s)", sym, formatDate(t)))
		}
	}
	slices.Sort(lagging)
	return latest, lagging
}

// screenCategoryName returns the canonical spelling of a category typed
// in any case, or "" for none.
func screenCategoryName(category string) string {
	for _, c := range universe.Categories() {
		if strings.EqualFold(c, category) {
			return c
		}
	}
	return ""
}
