package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// defaultReviewHorizonYears and defaultReviewShortMonths are the long
	// and short horizons of review_dca_plan when the caller gives none.
	defaultReviewHorizonYears = 5
	defaultReviewShortMonths  = 3
	// maxReviewShortMonths bounds short_horizon_months.
	maxReviewShortMonths = 60
	// reviewLongStep is the preferred step between long-term windows;
	// reviewMinLongWindows is the fewest windows it must give before the
	// review falls back to a window every month.
	reviewLongStep       = 12
	reviewMinLongWindows = 10
	// reviewSimulations and reviewSeed fix the Monte Carlo so a review is
	// reproducible.
	reviewSimulations        = 2000
	reviewSeed        uint64 = 42
	// lowExpenseRatio and highExpenseRatio are the two expense ratios the
	// cost drag arithmetic compares: a cheap index fund and a pricier one.
	lowExpenseRatio  = 0.0003
	highExpenseRatio = 0.0020
)

type reviewDCAPlanInput struct {
	Symbol             string  `json:"symbol" jsonschema:"ticker of the one ETF the plan buys, e.g. SCHD"`
	Amount             float64 `json:"amount" jsonschema:"USD per contribution before commissions, e.g. 5; must be > 0"`
	Cadence            string  `json:"cadence,omitempty" jsonschema:"daily (default; 252 contributions a year), weekly (52) or monthly (12)"`
	HorizonYears       float64 `json:"horizon_years,omitempty" jsonschema:"long-term horizon in years, a whole number of months from 0.25 to 40 (default 5)"`
	ShortHorizonMonths int     `json:"short_horizon_months,omitempty" jsonschema:"short-term horizon in months, 1 to 60 (default 3)"`
	costInput
}

type reviewPlanOutput struct {
	Symbol               string  `json:"symbol"`
	Name                 string  `json:"name"`
	Category             string  `json:"category" jsonschema:"universe category, else the fund data source's category; empty when neither knows"`
	Amount               float64 `json:"amount" jsonschema:"USD per contribution before commissions"`
	Currency             string  `json:"currency" jsonschema:"always USD"`
	Cadence              string  `json:"cadence"`
	ContributionsPerYear int     `json:"contributions_per_year" jsonschema:"252 for daily, 52 for weekly, 12 for monthly"`
	AnnualOutlay         float64 `json:"annual_outlay" jsonschema:"amount × contributions_per_year, USD, commissions included"`
	HorizonYears         float64 `json:"horizon_years"`
	FeeRate              float64 `json:"fee_rate"`
	CommissionFixed      float64 `json:"commission_fixed"`
	ReinvestDividends    bool    `json:"reinvest_dividends"`
}

type reviewCostsOutput struct {
	CommissionPerContribution             float64  `json:"commission_per_contribution" jsonschema:"commission_fixed + fee_rate × amount, USD"`
	CommissionPctOfContribution           float64  `json:"commission_pct_of_contribution" jsonschema:"commission_per_contribution as a percentage of amount"`
	CommissionPerYear                     float64  `json:"commission_per_year" jsonschema:"commission_per_contribution × contributions_per_year, USD"`
	ExpenseRatioPct                       *float64 `json:"expense_ratio_pct" jsonschema:"the fund's annual expense ratio from the fund data source, percent with 4 decimals (0.0945 = 0.0945%); null when unavailable, see notes"`
	ExpenseCostPerYearOnProjectedHoldings *float64 `json:"expense_cost_per_year_on_projected_holdings" jsonschema:"expense ratio × annual_outlay × horizon_years / 2, USD a year: the ratio charged on the average amount contributed over the horizon, ignoring price changes (a plain approximation); null without an expense ratio"`
	TotalCostPctOfOutlayYear1             float64  `json:"total_cost_pct_of_outlay_year1" jsonschema:"(commission_per_year + expense ratio × annual_outlay / 2) / annual_outlay × 100: first-year commissions plus the expense ratio on the first year's average holdings, as a percentage of the first year's outlay; commissions only when the expense ratio is unavailable"`
	Notes                                 []string `json:"notes" jsonschema:"the formulas behind these figures and why a figure is missing"`
}

type reviewHistoryOutput struct {
	AsOf                              string         `json:"as_of"`
	FirstDate                         string         `json:"first_date"`
	Windows                           []windowOutput `json:"windows" jsonschema:"trailing total returns with dividends reinvested, 1m to 10y and max, as get_etf_info reports them"`
	TTMDividendYieldPct               float64        `json:"ttm_dividend_yield_pct"`
	ProjectedAnnualDividendsAtHorizon float64        `json:"projected_annual_dividends_at_horizon" jsonschema:"ttm dividend yield × annual_outlay × horizon_years, USD a year: what the amount contributed by the horizon would pay at today's trailing yield, ignoring price changes and dividend growth (an approximation)"`
	Volatility1YPct                   float64        `json:"volatility_1y_pct"`
	MaxDrawdownAllPct                 float64        `json:"max_drawdown_all_pct" jsonschema:"deepest peak-to-trough fall of the adjusted price over the whole history"`
}

// reviewRollingOutput is a rolling-window summary with the result in one
// sentence.
type reviewRollingOutput struct {
	rollingStatsOutput
	Summary string `json:"summary" jsonschema:"the loss frequency in one sentence"`
}

type reviewShortTermOutput struct {
	Months  int                  `json:"months"`
	Trend   trendOutput          `json:"trend" jsonschema:"rule-based reading of the recent price path (50/200-day averages, momentum); a description, not a prediction"`
	Rolling *reviewRollingOutput `json:"rolling" jsonschema:"the plan over every historical window of months months, one starting every month; null when the history is too short (see notes)"`
	Notes   []string             `json:"notes,omitempty"`
}

type reviewMonteCarloOutput struct {
	Simulations               int                `json:"simulations"`
	Seed                      uint64             `json:"seed"`
	BlockLength               int                `json:"block_length"`
	Contributions             int                `json:"contributions"`
	Invested                  float64            `json:"invested" jsonschema:"contributions × amount, USD, commissions included"`
	Percentiles               map[string]float64 `json:"percentiles" jsonschema:"p5 to p95 of the final value, USD"`
	ReturnPctPercentiles      map[string]float64 `json:"return_pct_percentiles"`
	ProbLossPct               float64            `json:"prob_loss_pct" jsonschema:"share of paths ending below the amount invested, percent"`
	MeanFinal                 float64            `json:"mean_final"`
	HistoricalAnnualReturnPct float64            `json:"historical_annual_return_pct" jsonschema:"compound annual growth of the symbol over the lookback, the drift the bootstrap carries"`
	HistoricalVolatilityPct   float64            `json:"historical_volatility_pct"`
	LookbackFrom              string             `json:"lookback_from"`
	LookbackTo                string             `json:"lookback_to"`
	Assumptions               []string           `json:"assumptions"`
}

type reviewCostDragOutput struct {
	AssumedGrowthPct    float64 `json:"assumed_growth_pct" jsonschema:"gross annual growth the arithmetic assumes: monte_carlo's historical_annual_return_pct, or 0 when monte_carlo is unavailable"`
	NetInvested         float64 `json:"net_invested" jsonschema:"(annual_outlay − commission_per_year) × horizon_years, USD"`
	LowExpenseRatioPct  float64 `json:"low_expense_ratio_pct"`
	HighExpenseRatioPct float64 `json:"high_expense_ratio_pct"`
	FinalValueLow       float64 `json:"final_value_low" jsonschema:"value at the horizon with the low expense ratio, USD"`
	FinalValueHigh      float64 `json:"final_value_high" jsonschema:"value at the horizon with the high expense ratio, USD"`
	Difference          float64 `json:"difference" jsonschema:"final_value_low − final_value_high, USD"`
}

type reviewLongTermOutput struct {
	Years        float64                 `json:"years"`
	Rolling      *reviewRollingOutput    `json:"rolling" jsonschema:"the plan over every historical window of years, one starting every 12 months (every month when that gives fewer than 10 windows); null when the history is too short (see notes)"`
	MonteCarlo   *reviewMonteCarloOutput `json:"monte_carlo" jsonschema:"block-bootstrap projection of the same plan over years, 2000 paths, seed 42 (as forecast_dca computes it); null when it cannot run (see notes)"`
	CostDrag     reviewCostDragOutput    `json:"cost_drag" jsonschema:"what a 0.20% instead of a 0.03% expense ratio costs over years on this outlay"`
	CostDragNote string                  `json:"cost_drag_note"`
	Notes        []string                `json:"notes,omitempty"`
}

type reviewDCAPlanOutput struct {
	Plan         reviewPlanOutput      `json:"plan"`
	Costs        reviewCostsOutput     `json:"costs"`
	History      reviewHistoryOutput   `json:"history"`
	ShortTerm    reviewShortTermOutput `json:"short_term"`
	LongTerm     reviewLongTermOutput  `json:"long_term"`
	Observations []string              `json:"observations" jsonschema:"factual sentences drawn from the sections above, ending with the disclaimer; never a recommendation"`
	Warnings     []string              `json:"warnings,omitempty"`
	Disclaimer   string                `json:"disclaimer"`
}

func (d Deps) registerReviewDCAPlan(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "review_dca_plan",
		Title:       "Review DCA plan",
		Description: "Reviews a recurring purchase plan of ONE ETF in USD, typically a small one such as 5 USD every trading day with a 0.99 USD commission, and lays out the facts in one call. plan: contributions per year (252 daily, 52 weekly, 12 monthly) and annual outlay. costs: commission per purchase, as a percentage of each purchase and per year, the fund's expense ratio (from the fund data source; null when unavailable) with its approximate yearly cost, and the total first-year cost as a percentage of the outlay, with the formulas. history: trailing returns, trailing-12-month dividend yield and the yearly dividends the horizon's contributions would earn at it, 1-year volatility, deepest drawdown. short_term: the current trend reading and how often a plan of short_horizon_months ended below cost across every historical start month. long_term: the same over horizon_years from real history, a 2000-path block-bootstrap projection (seed 42) of final value and return, and the arithmetic cost of a 0.20% versus a 0.03% expense ratio. observations: plain factual sentences. It never recommends; to look at cheaper or similar funds call find_alternatives. USD only, no portfolios: for KRW or several ETFs use simulate_rolling_dca and forecast_dca. Defaults: cadence daily, horizon_years 5, short_horizon_months 3, commission_fixed 0, fee_rate 0, reinvest_dividends true. Percentages are plain numbers (7.5 = 7.5%).",
		Annotations: readOnly("Review DCA plan", true),
		InputSchema: inputSchema[reviewDCAPlanInput](schemaTweaks{
			defaults: costDefaults(map[string]any{"cadence": "daily", "horizon_years": defaultReviewHorizonYears, "short_horizon_months": defaultReviewShortMonths}),
		}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reviewDCAPlanInput) (*mcp.CallToolResult, reviewDCAPlanOutput, error) {
		out, err := d.reviewDCAPlan(ctx, in)
		return nil, out, err
	})
}

// contributionsPerYear is how many purchases a cadence makes in a year of
// 252 trading days.
func contributionsPerYear(c sim.Cadence) int {
	switch c {
	case sim.Weekly:
		return 52
	case sim.Monthly:
		return 12
	default:
		return 252
	}
}

// reviewFigures are the plan-level numbers every section and observation
// derives from.
type reviewFigures struct {
	sym             string
	amount          float64
	cadence         sim.Cadence
	perYear         int
	outlay          float64 // amount × perYear
	commission      float64 // per contribution
	commissionYear  float64
	horizonYears    float64
	horizonMonths   int
	shortMonths     int
	expenseRatio    *float64 // fraction; nil when unavailable
	expenseRatioWhy string   // why expenseRatio is nil
}

// reviewDCAPlan validates the plan, loads the history and assembles every
// section from the existing engines.
func (d Deps) reviewDCAPlan(ctx context.Context, in reviewDCAPlanInput) (reviewDCAPlanOutput, error) {
	f, plan, err := in.figures()
	if err != nil {
		return reviewDCAPlanOutput{}, err
	}
	simIn, _, err := d.loadInput(ctx, plan)
	if err != nil {
		return reviewDCAPlanOutput{}, err
	}
	series := simIn.Series[f.sym]
	summary, err := analytics.Summarize(series, time.Time{})
	if err != nil {
		return reviewDCAPlanOutput{}, userError(err)
	}
	trend, err := analytics.AnalyzeTrend(series, time.Time{})
	if err != nil {
		return reviewDCAPlanOutput{}, userError(err)
	}
	profile := d.reviewProfile(ctx, &f)

	out := reviewDCAPlanOutput{
		Plan:       reviewPlan(f, in, series, profile),
		Costs:      reviewCosts(f),
		History:    reviewHistory(f, summary),
		Disclaimer: Disclaimer,
	}
	out.ShortTerm = reviewShortTerm(f, plan, simIn, trend)
	out.LongTerm = reviewLongTerm(f, plan, simIn, in)
	out.Observations = reviewObservations(f, out, summary)
	out.Warnings = d.planStaleWarnings(plan)
	if c := series.Meta.Currency; c != "" && !strings.EqualFold(c, sim.CurrencyUSD) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s is quoted in %s; review_dca_plan treats its prices as USD", f.sym, c))
	}
	return out, nil
}

// figures validates the input, applies the defaults and returns the
// derived numbers with the sim.Plan they describe.
func (in reviewDCAPlanInput) figures() (reviewFigures, sim.Plan, error) {
	sym := normalizeSymbol(in.Symbol)
	if sym == "" {
		return reviewFigures{}, sim.Plan{}, errors.New("symbol is required; use list_etfs to find one")
	}
	if err := checkAmount(in.Amount); err != nil {
		return reviewFigures{}, sim.Plan{}, err
	}
	cadence, err := parseCadence(in.Cadence)
	if err != nil {
		return reviewFigures{}, sim.Plan{}, err
	}
	horizon := in.HorizonYears
	if horizon == 0 {
		horizon = defaultReviewHorizonYears
	}
	horizonMonths, err := wholeMonths("horizon_years", horizon, minRollingYears, analytics.MaxHorizonYears)
	if err != nil {
		return reviewFigures{}, sim.Plan{}, err
	}
	short := in.ShortHorizonMonths
	if short == 0 {
		short = defaultReviewShortMonths
	}
	if short < 1 || short > maxReviewShortMonths {
		return reviewFigures{}, sim.Plan{}, fmt.Errorf("short_horizon_months must be between 1 and %d, got %d", maxReviewShortMonths, in.ShortHorizonMonths)
	}
	if err := in.validate(in.Amount); err != nil {
		return reviewFigures{}, sim.Plan{}, err
	}
	perYear := contributionsPerYear(cadence)
	commission := in.CommissionFixed + in.FeeRate*in.Amount
	f := reviewFigures{
		sym:            sym,
		amount:         in.Amount,
		cadence:        cadence,
		perYear:        perYear,
		outlay:         in.Amount * float64(perYear),
		commission:     commission,
		commissionYear: commission * float64(perYear),
		horizonYears:   horizon,
		horizonMonths:  horizonMonths,
		shortMonths:    short,
	}
	plan := sim.Plan{
		Allocations: []sim.Allocation{{Symbol: sym, Weight: 1}},
		Amount:      in.Amount,
		Currency:    sim.CurrencyUSD,
		Cadence:     cadence,
		FeeRate:     in.FeeRate,
		FeeFixed:    in.CommissionFixed,
		Reinvest:    in.reinvest(),
	}
	return f, plan, nil
}

// reviewProfile fetches the fund profile for the expense ratio and
// records in f why it is unavailable when it is.
func (d Deps) reviewProfile(ctx context.Context, f *reviewFigures) *market.FundProfile {
	if d.Fund == nil {
		f.expenseRatioWhy = "the fund data source is not configured"
		return nil
	}
	p, err := d.Fund.FundProfile(ctx, f.sym)
	switch {
	case errors.Is(err, market.ErrNotFound):
		f.expenseRatioWhy = "the fund data source has no profile for " + f.sym
		return nil
	case err != nil:
		f.expenseRatioWhy = "the fund profile could not be fetched: " + rootCause(err)
		return nil
	case p == nil:
		f.expenseRatioWhy = "the fund data source returned no profile for " + f.sym
		return nil
	}
	if er := p.ExpenseRatio; er == nil || math.IsNaN(*er) || *er < 0 || *er >= 1 {
		f.expenseRatioWhy = "the fund data source does not report an expense ratio for " + f.sym
	} else {
		f.expenseRatio = ptr(*er)
	}
	return p
}

// reviewPlan echoes the plan with its name and category.
func reviewPlan(f reviewFigures, in reviewDCAPlanInput, series *market.Series, profile *market.FundProfile) reviewPlanOutput {
	out := reviewPlanOutput{
		Symbol:               f.sym,
		Name:                 series.Meta.Name,
		Amount:               f.amount,
		Currency:             sim.CurrencyUSD,
		Cadence:              string(f.cadence),
		ContributionsPerYear: f.perYear,
		AnnualOutlay:         round2(f.outlay),
		HorizonYears:         f.horizonYears,
		FeeRate:              in.FeeRate,
		CommissionFixed:      in.CommissionFixed,
		ReinvestDividends:    in.reinvest(),
	}
	if profile != nil {
		if profile.Name != "" {
			out.Name = profile.Name
		}
		out.Category = profile.Category
	}
	if e, ok := universe.Get(f.sym); ok {
		out.Name, out.Category = e.Name, e.Category
	}
	return out
}

// reviewCosts computes the cost section.
func reviewCosts(f reviewFigures) reviewCostsOutput {
	out := reviewCostsOutput{
		CommissionPerContribution:   round2(f.commission),
		CommissionPctOfContribution: pct(f.commission / f.amount),
		CommissionPerYear:           round2(f.commissionYear),
		Notes: []string{
			"commission_per_contribution = commission_fixed + fee_rate × amount; commission_per_year = commission_per_contribution × contributions_per_year",
		},
	}
	yearOne := f.commissionYear
	if er := f.expenseRatio; er != nil {
		out.ExpenseRatioPct = ptr(round4(*er * 100))
		out.ExpenseCostPerYearOnProjectedHoldings = ptr(round2(*er * f.outlay * f.horizonYears / 2))
		yearOne += *er * f.outlay / 2
		out.Notes = append(out.Notes,
			"expense_cost_per_year_on_projected_holdings = expense_ratio × annual_outlay × horizon_years / 2: the ratio charged on the average amount contributed over the horizon, ignoring price changes (a plain approximation)",
			"total_cost_pct_of_outlay_year1 = (commission_per_year + expense_ratio × annual_outlay / 2) / annual_outlay × 100")
	} else {
		out.Notes = append(out.Notes,
			"expense_ratio_pct is null: "+f.expenseRatioWhy,
			"total_cost_pct_of_outlay_year1 = commission_per_year / annual_outlay × 100 (commissions only, the expense ratio is unavailable)")
	}
	out.TotalCostPctOfOutlayYear1 = pct(yearOne / f.outlay)
	return out
}

// reviewHistory maps the descriptive statistics of the symbol.
func reviewHistory(f reviewFigures, s analytics.Summary) reviewHistoryOutput {
	return reviewHistoryOutput{
		AsOf:                              formatDate(s.AsOf),
		FirstDate:                         formatDate(s.FirstDate),
		Windows:                           toSummaryOutput(s).Windows,
		TTMDividendYieldPct:               pct(s.TTMDividendYield),
		ProjectedAnnualDividendsAtHorizon: round2(s.TTMDividendYield * f.outlay * f.horizonYears),
		Volatility1YPct:                   pct(s.Volatility1Y),
		MaxDrawdownAllPct:                 pct(s.MaxDrawdownAll),
	}
}

// reviewShortTerm reads the trend and runs the short rolling windows.
func reviewShortTerm(f reviewFigures, plan sim.Plan, in sim.Input, trend analytics.Trend) reviewShortTermOutput {
	out := reviewShortTermOutput{Months: f.shortMonths, Trend: toTrendOutput(trend)}
	rolling, _, err := runReviewRolling(plan, in, f.shortMonths, 1)
	if err != nil {
		out.Notes = append(out.Notes, reviewRollingNote(err, "short_horizon_months"))
		return out
	}
	out.Rolling = rolling
	return out
}

// reviewLongTerm runs the long rolling windows, the Monte Carlo and the
// cost drag arithmetic.
func reviewLongTerm(f reviewFigures, plan sim.Plan, simIn sim.Input, in reviewDCAPlanInput) reviewLongTermOutput {
	out := reviewLongTermOutput{Years: f.horizonYears}

	rolling, res, err := runReviewRolling(plan, simIn, f.horizonMonths, reviewLongStep)
	if err != nil || res.Count < reviewMinLongWindows {
		var why string
		switch {
		case err == nil:
			why = fmt.Sprintf("gives only %d windows (fewer than %d)", res.Count, reviewMinLongWindows)
		case errors.Is(err, sim.ErrInsufficientHistory):
			why = "leaves fewer than 3 windows in the history"
		default:
			why = "fails (" + strings.ReplaceAll(err.Error(), "sim: ", "") + ")"
		}
		monthly, _, merr := runReviewRolling(plan, simIn, f.horizonMonths, 1)
		switch {
		case merr == nil:
			rolling, err = monthly, nil
			out.Notes = append(out.Notes, fmt.Sprintf("rolling starts a window every month because starting one every %d months %s; overlapping windows share most of their days, so they are not independent outcomes", reviewLongStep, why))
		case err != nil:
			err = merr
		}
	}
	if err != nil {
		out.Notes = append(out.Notes, reviewRollingNote(err, "horizon_years"))
	} else {
		out.Rolling = rolling
	}

	mc, notes := reviewMonteCarlo(f, simIn, in)
	out.MonteCarlo = mc
	out.Notes = append(out.Notes, notes...)

	growth, source := 0.0, "no growth, because monte_carlo is unavailable"
	if mc != nil {
		growth = mc.HistoricalAnnualReturnPct / 100
		source = "the historical annual return monte_carlo is built on"
	}
	out.CostDrag, out.CostDragNote = reviewCostDrag(f, growth, source)
	return out
}

// reviewRollingNote explains a rolling section left null; field names the
// input that sets the window length.
func reviewRollingNote(err error, field string) string {
	msg := "rolling is null: " + strings.ReplaceAll(err.Error(), "sim: ", "")
	if errors.Is(err, sim.ErrInsufficientHistory) {
		msg += "; a shorter " + field + " fits more windows"
	}
	return msg
}

// runReviewRolling runs the plan over every window of months months, one
// starting every step months, and maps it with a one-sentence summary.
func runReviewRolling(plan sim.Plan, in sim.Input, months, step int) (*reviewRollingOutput, *sim.RollingResult, error) {
	res, err := sim.RunRolling(plan, in, sim.RollingConfig{DurationYears: float64(months) / 12, StepMonths: step})
	if err != nil {
		return nil, nil, err
	}
	stats, _, err := toRollingStats(res, months, step)
	if err != nil {
		return nil, nil, err
	}
	spacing := ""
	if step != 1 {
		spacing = fmt.Sprintf(" (one starting every %d months)", step)
	}
	summary := fmt.Sprintf("over %d historical %d-month windows%s the plan ended below cost %s%% of the time",
		stats.WindowsCount, months, spacing, obsNum(stats.ProbLossPct))
	return &reviewRollingOutput{rollingStatsOutput: stats, Summary: summary}, res, nil
}

// reviewMonteCarlo projects the plan with the block bootstrap. The fixed
// commission folds into the fee rate exactly, because a USD plan's amount
// is constant: amount × (1 − fee_rate) − commission_fixed = amount × (1 −
// fee_rate − commission_fixed / amount).
func reviewMonteCarlo(f reviewFigures, simIn sim.Input, in reviewDCAPlanInput) (*reviewMonteCarloOutput, []string) {
	feeRate := in.FeeRate + in.CommissionFixed/in.Amount
	res, err := analytics.MonteCarlo(analytics.MCPlan{
		Symbols:      []string{f.sym},
		Weights:      []float64{1},
		Amount:       f.amount,
		Currency:     analytics.CurrencyUSD,
		Cadence:      analytics.Cadence(f.cadence),
		HorizonYears: f.horizonYears,
		FeeRate:      feeRate,
	}, analytics.MCConfig{Simulations: reviewSimulations, Seed: reviewSeed}, analytics.MCInput{Series: simIn.Series})
	if err != nil {
		return nil, []string{"monte_carlo is null: " + userError(err).Error()}
	}
	values := append([]float64{res.Invested, res.MeanFinal, res.HistoricalAnnualReturn}, mapValues(res.Percentiles)...)
	if err := checkFinite("monte_carlo", values...); err != nil {
		return nil, []string{"monte_carlo is null: the projection overflowed"}
	}
	var notes []string
	if in.CommissionFixed > 0 {
		notes = append(notes, fmt.Sprintf("monte_carlo charges commission_fixed as part of the fee rate: fee_rate + commission_fixed / amount = %s%% of every purchase, exact for a constant USD amount", obsNum(pct(feeRate))))
	}
	if !in.reinvest() {
		notes = append(notes, "monte_carlo resamples adjusted closes, so it assumes dividends are reinvested even though reinvest_dividends is false; the rolling windows honour the setting")
	}
	return &reviewMonteCarloOutput{
		Simulations:               res.Simulations,
		Seed:                      res.Seed,
		BlockLength:               res.BlockLength,
		Contributions:             res.Contributions,
		Invested:                  round2(res.Invested),
		Percentiles:               roundMap(res.Percentiles),
		ReturnPctPercentiles:      roundMap(res.ReturnPctPercentiles),
		ProbLossPct:               pct(res.ProbLoss),
		MeanFinal:                 round2(res.MeanFinal),
		HistoricalAnnualReturnPct: pct(res.HistoricalAnnualReturn),
		HistoricalVolatilityPct:   pct(res.HistoricalVolatility),
		LookbackFrom:              formatDate(res.LookbackFrom),
		LookbackTo:                formatDate(res.LookbackTo),
		Assumptions:               append([]string{}, res.Assumptions...),
	}, notes
}

// reviewCostDrag compares the low and the high expense ratio on the same
// net outlay: monthly contributions of a twelfth of the yearly amount
// after commissions, each month's balance multiplied by ((1 + growth) ×
// (1 − expense ratio))^(1/12).
func reviewCostDrag(f reviewFigures, growth float64, source string) (reviewCostDragOutput, string) {
	monthly := (f.outlay - f.commissionYear) / 12
	grow := func(er float64) float64 {
		factor := math.Pow((1+growth)*(1-er), 1.0/12)
		balance := 0.0
		for range f.horizonMonths {
			balance = (balance + monthly) * factor
		}
		return balance
	}
	low, high := round2(grow(lowExpenseRatio)), round2(grow(highExpenseRatio))
	out := reviewCostDragOutput{
		AssumedGrowthPct:    pct(growth),
		NetInvested:         round2(monthly * float64(f.horizonMonths)),
		LowExpenseRatioPct:  pct(lowExpenseRatio),
		HighExpenseRatioPct: pct(highExpenseRatio),
		FinalValueLow:       low,
		FinalValueHigh:      high,
		Difference:          round2(low - high),
	}
	note := fmt.Sprintf("Putting %s USD a year after commissions into the fund for %s (%s USD in all) and assuming %s%% gross growth a year (%s), a 0.03%% expense ratio ends at about %s USD and a 0.20%% one at about %s USD, %s USD apart. Arithmetic: a twelfth of the yearly amount is added every month and each month's balance is multiplied by ((1 + growth) × (1 − expense ratio))^(1/12).",
		obsMoney(monthly*12), obsYears(f.horizonYears), obsMoney(out.NetInvested), obsNum(out.AssumedGrowthPct), source,
		obsMoney(low), obsMoney(high), obsMoney(out.Difference))
	return out, note
}

// reviewObservations turns the sections into plain factual sentences and
// closes with the disclaimer. Nothing here recommends anything.
func reviewObservations(f reviewFigures, out reviewDCAPlanOutput, s analytics.Summary) []string {
	var obs []string
	if f.commission > 0 {
		obs = append(obs, fmt.Sprintf("A %s USD commission on a %s USD contribution is %s%% of each purchase; over a year that is %s USD on %s USD invested.",
			obsMoney(f.commission), obsMoney(f.amount), obsNum(out.Costs.CommissionPctOfContribution), obsMoney(f.commissionYear), obsMoney(f.outlay)))
	} else {
		obs = append(obs, fmt.Sprintf("No commission was given (commission_fixed and fee_rate are 0), so all %s USD a year buys shares.", obsMoney(f.outlay)))
	}
	if er := f.expenseRatio; er != nil {
		obs = append(obs, fmt.Sprintf("Expense ratio %s%%: %s USD per year per %s USD held.",
			obsNum(*out.Costs.ExpenseRatioPct), obsMoney(*er*f.outlay), obsMoney(f.outlay)))
	} else {
		obs = append(obs, fmt.Sprintf("The expense ratio is unavailable (%s), so the cost figures cover commissions only.", f.expenseRatioWhy))
	}
	obs = append(obs, fmt.Sprintf("First-year costs come to %s%% of the %s USD outlay.", obsNum(out.Costs.TotalCostPctOfOutlayYear1), obsMoney(f.outlay)))

	if s.TTMDividendYield > 0 {
		obs = append(obs, fmt.Sprintf("Trailing-12-month dividend yield %s%%: the %s USD contributed over %s would pay about %s USD a year at that yield (ignoring price changes and dividend growth).",
			obsNum(out.History.TTMDividendYieldPct), obsMoney(f.outlay*f.horizonYears), obsYears(f.horizonYears), obsMoney(out.History.ProjectedAnnualDividendsAtHorizon)))
	} else {
		obs = append(obs, "No dividends were paid in the trailing 12 months.")
	}
	obs = append(obs, fmt.Sprintf("1-year volatility %s%%; the deepest fall of the adjusted price since %s was %s%%.",
		obsNum(out.History.Volatility1YPct), out.History.FirstDate, obsNum(out.History.MaxDrawdownAllPct)))

	t := out.ShortTerm.Trend
	if t.SMA200 > 0 {
		side := "above"
		if t.PctVsSMA200 < 0 {
			side = "below"
		}
		obs = append(obs, fmt.Sprintf("Trend reading as of %s: %s; the close is %s%% %s its 200-day average.", t.AsOf, t.State, obsNum(math.Abs(t.PctVsSMA200)), side))
	} else {
		obs = append(obs, fmt.Sprintf("Trend reading as of %s: %s.", t.AsOf, t.State))
	}
	obs = append(obs, rollingObservation(out.ShortTerm.Rolling, fmt.Sprintf("%d-month", f.shortMonths)))
	obs = append(obs, rollingObservation(out.LongTerm.Rolling, fmt.Sprintf("%d-month", f.horizonMonths)))
	if mc := out.LongTerm.MonteCarlo; mc != nil {
		obs = append(obs, fmt.Sprintf("Across %d resampled %s-year paths (seed %d) the median final value is %s USD on %s USD invested (p5 %s USD, p95 %s USD); %s%% of paths end below cost.",
			mc.Simulations, obsNum(f.horizonYears), mc.Seed, obsMoney(mc.Percentiles["p50"]), obsMoney(mc.Invested),
			obsMoney(mc.Percentiles["p5"]), obsMoney(mc.Percentiles["p95"]), obsNum(mc.ProbLossPct)))
	}
	cd := out.LongTerm.CostDrag
	obs = append(obs, fmt.Sprintf("Over %s, a 0.20%% expense ratio instead of 0.03%% leaves about %s USD less on this outlay at %s%% gross growth a year.",
		obsYears(f.horizonYears), obsMoney(cd.Difference), obsNum(cd.AssumedGrowthPct)))
	return append(obs, Disclaimer)
}

// rollingObservation states a rolling result, or why there is none.
func rollingObservation(r *reviewRollingOutput, length string) string {
	if r == nil {
		return fmt.Sprintf("The history is too short for %s rolling windows.", length)
	}
	s := strings.ToUpper(r.Summary[:1]) + r.Summary[1:]
	return fmt.Sprintf("%s; the median window returned %s%% and the worst %s%% (%s to %s).",
		s, obsNum(r.ReturnPctPercentiles["p50"]), obsNum(r.Worst.ReturnPct), r.Worst.Start, r.Worst.End)
}

// obsNum renders a rounded number for a sentence without trailing zeros:
// 19.8, 0.03, 5.
func obsNum(x float64) string {
	v := round4(x)
	if v == 0 {
		v = 0 // no negative zero
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// obsYears renders a horizon for a sentence: "1 year", "2.5 years".
func obsYears(y float64) string {
	if y == 1 {
		return "1 year"
	}
	return obsNum(y) + " years"
}

// obsMoney renders money for a sentence: cents only when there are any,
// thousands separated by commas (1,260 and 249.48).
func obsMoney(x float64) string {
	v := round2(x)
	if v == 0 {
		v = 0
	}
	s := strings.TrimSuffix(strconv.FormatFloat(math.Abs(v), 'f', 2, 64), ".00")
	whole, cents, hasCents := strings.Cut(s, ".")
	var b strings.Builder
	if v < 0 {
		b.WriteByte('-')
	}
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if hasCents {
		b.WriteByte('.')
		b.WriteString(cents)
	}
	return b.String()
}
