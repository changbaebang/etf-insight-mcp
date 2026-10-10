package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type projectDCAInput struct {
	Symbol                  string            `json:"symbol,omitempty" jsonschema:"single ticker to project, e.g. VOO; give either symbol or allocations, not both"`
	Allocations             []allocationInput `json:"allocations,omitempty" jsonschema:"portfolio to project as a list of {symbol, weight} with weights summing to 1 or 100; alternative to symbol"`
	Amount                  float64           `json:"amount" jsonschema:"size of one contribution in currency before fees, e.g. 100 (USD) or 10000 (KRW); must be > 0"`
	Currency                string            `json:"currency,omitempty" jsonschema:"USD or KRW (default USD); with KRW the exchange rate path is resampled jointly from KRW=X. Every symbol must be quoted in USD either way"`
	Cadence                 string            `json:"cadence,omitempty" jsonschema:"daily, weekly or monthly (default daily); approximated as a purchase every 1, 4.85 (252/52, so 52 a year) or 21 simulated trading days"`
	HorizonYears            float64           `json:"horizon_years" jsonschema:"length of the plan in years, > 0 and at most 40, e.g. 5"`
	FeeRate                 float64           `json:"fee_rate,omitempty" jsonschema:"fraction of each contribution lost to commissions, e.g. 0.001 for 0.1% (default 0)"`
	CommissionFixed         float64           `json:"commission_fixed,omitempty" jsonschema:"fixed commission per ETF purchased, in the plan currency, e.g. 0.99 (default 0): every contribution pays it once for each ETF it buys and it must leave something of the smallest ETF's share to invest. The projection charges it as part of the fee rate, fee_rate + (number of ETFs × commission_fixed) / amount of every contribution (see assumptions). For small daily purchases this is usually the dominant cost"`
	Simulations             int               `json:"simulations,omitempty" jsonschema:"number of bootstrap paths, 1 to 20000 (default 2000); more paths smooth the percentiles but take longer"`
	Seed                    uint64            `json:"seed,omitempty" jsonschema:"random seed (default 42); the same seed and inputs always give the same result"`
	BlockLength             int               `json:"block_length,omitempty" jsonschema:"bootstrap block length in trading days (default 21, about one month); longer blocks keep more of the historical autocorrelation"`
	LookbackYears           float64           `json:"lookback_years,omitempty" jsonschema:"resample only the last N years of history, counted as N × 252 trading days; at least 1, since one year of common daily returns is the minimum, and at most 200 (default 0 = all history every symbol shares)"`
	ExpectedAnnualReturnPct *float64          `json:"expected_annual_return_pct,omitempty" jsonschema:"optional external assumption such as a broker's long-run forecast, as a PERCENTAGE: 7 means 7% compound annual growth (0.07 would mean 0.07%). Must be above -100 and at most 1000. Sets every holding's expected compound annual growth to this rate instead of its historical average (each symbol's resampled daily log returns are shifted so their mean is log(1 + rate) / 252; volatility and correlations are kept), so a mix of holdings compounds at it too. In a KRW plan the exchange-rate path is not shifted, so the KRW result also carries the historical KRW/USD drift on top of the rate"`
}

// symbolTrendOutput is the trend of one symbol in a forecast.
type symbolTrendOutput struct {
	Symbol string `json:"symbol"`
	trendOutput
}

type projectDCAOutput struct {
	Allocations               []allocationOutput  `json:"allocations"`
	Amount                    float64             `json:"amount"`
	Currency                  string              `json:"currency"`
	Cadence                   string              `json:"cadence"`
	HorizonYears              float64             `json:"horizon_years"`
	Simulations               int                 `json:"simulations"`
	BlockLength               int                 `json:"block_length"`
	Seed                      uint64              `json:"seed"`
	Contributions             int                 `json:"contributions"`
	Invested                  float64             `json:"invested"`
	Percentiles               map[string]float64  `json:"percentiles"`
	ReturnPctPercentiles      map[string]float64  `json:"return_pct_percentiles"`
	ProbLossPct               float64             `json:"prob_loss_pct"`
	MeanFinal                 float64             `json:"mean_final"`
	HistoricalAnnualReturnPct float64             `json:"historical_annual_return_pct" jsonschema:"compound annual growth of the plan's daily-rebalanced basket over the lookback, in the plan currency (FX included for KRW)"`
	HistoricalVolatilityPct   float64             `json:"historical_volatility_pct" jsonschema:"annualized volatility of the same daily series"`
	LookbackFrom              string              `json:"lookback_from"`
	LookbackTo                string              `json:"lookback_to"`
	Assumptions               []string            `json:"assumptions"`
	Trend                     []symbolTrendOutput `json:"trend"`
	Warnings                  []string            `json:"warnings,omitempty"`
	Disclaimer                string              `json:"disclaimer"`
}

func registerProjectDCA(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "project_dca_outcomes",
		Description: "Projects the range of outcomes of a recurring-purchase plan over horizon_years by resampling the symbols' own daily history with a block bootstrap: blocks of consecutive past days are drawn at random (the same days for every symbol and for the KRW=X rate, so correlations survive) and the plan is run on thousands of such paths. This is NOT a price prediction: it only shows what the plan would have produced under reshuffled history, nothing that did not happen in the lookback can happen in a path, and the historical average return is assumed to persist unless expected_annual_return_pct overrides it. It needs at least a year of common history, and warns when the history is shorter than the horizon or than 5 years. Symbols must be quoted in USD. Costs: fee_rate and commission_fixed (per ETF purchased), folded into one fee rate. Returns percentiles (p5 to p95) of the final value and of the return, the probability of ending below the amount invested, the mean, the historical return and volatility the bootstrap is built on, the lookback range, the modelling assumptions in words and each symbol's current trend. Deterministic for a given seed.",
		Title:       "Project DCA outcomes",
		Annotations: readOnly("Project DCA outcomes", true),
		InputSchema: projectDCASchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in projectDCAInput) (*mcp.CallToolResult, projectDCAOutput, error) {
		out, err := d.projectDCA(ctx, in)
		return nil, out, err
	})
}

// maxSeed is the largest seed a JSON number carries exactly (2^53 − 1);
// a larger one would be rounded before it reaches the tool, and the run
// could not be reproduced from the seed echoed back.
const maxSeed = 1<<53 - 1

// projectDCASchema is the input schema of project_dca_outcomes: the inferred one
// with its defaults and the bound on seed.
func projectDCASchema() *jsonschema.Schema {
	s := inputSchema[projectDCAInput](schemaTweaks{
		defaults: map[string]any{"currency": "USD", "cadence": "daily", "simulations": analytics.DefaultSimulations, "seed": analytics.DefaultSeed, "block_length": analytics.DefaultBlockLength, "lookback_years": 0, "fee_rate": 0, "commission_fixed": 0},
		minItems: map[string]int{"allocations": 1},
	})
	property(s, "seed").Maximum = ptr(float64(maxSeed))
	return s
}

// projectDCA validates the plan, fetches the histories and runs the
// Monte Carlo.
func (d Deps) projectDCA(ctx context.Context, in projectDCAInput) (projectDCAOutput, error) {
	allocs, err := resolveAllocations(in.Symbol, in.Allocations)
	if err != nil {
		return projectDCAOutput{}, err
	}
	currency, err := parseCurrency(in.Currency)
	if err != nil {
		return projectDCAOutput{}, err
	}
	cadence, err := parseCadence(in.Cadence)
	if err != nil {
		return projectDCAOutput{}, err
	}
	if err := checkAmount(in.Amount); err != nil {
		return projectDCAOutput{}, err
	}
	if in.HorizonYears <= 0 || in.HorizonYears > analytics.MaxHorizonYears {
		return projectDCAOutput{}, fmt.Errorf("horizon_years must be > 0 and at most %d, got %v", analytics.MaxHorizonYears, in.HorizonYears)
	}
	if err := checkFeeRate(in.FeeRate); err != nil {
		return projectDCAOutput{}, err
	}
	if err := checkFixedCommission(in.CommissionFixed, in.Amount, in.FeeRate, allocs); err != nil {
		return projectDCAOutput{}, err
	}
	cfg := analytics.MCConfig{
		Simulations:   in.Simulations,
		BlockLength:   in.BlockLength,
		LookbackYears: in.LookbackYears,
		Seed:          in.Seed,
	}
	var warnings []string
	if in.ExpectedAnnualReturnPct != nil {
		r := *in.ExpectedAnnualReturnPct
		if math.IsNaN(r) || r <= -100 || r > maxExpectedReturnPct {
			return projectDCAOutput{}, fmt.Errorf("expected_annual_return_pct must be above -100 and at most %v, got %v", maxExpectedReturnPct, r)
		}
		if r != 0 && math.Abs(r) < 1 {
			warnings = append(warnings, fmt.Sprintf("expected_annual_return_pct %v means %v%% a year; pass 7 for 7%%", r, r))
		}
		// The engine wants a mean annual log return; log(1 + r) is the
		// one whose compound growth equals the percentage given.
		logReturn := math.Log(1 + r/100)
		cfg.ExpectedAnnualReturn = &logReturn
	}

	symbols := make([]string, 0, len(allocs)+1)
	weights := make([]float64, 0, len(allocs))
	for _, a := range allocs {
		symbols = append(symbols, a.Symbol)
		weights = append(weights, a.Weight)
	}
	krw := currency == sim.CurrencyKRW
	fetch := symbols
	if krw {
		fetch = append(append([]string(nil), symbols...), fxSymbol)
	}
	series, errs := d.fetchAll(ctx, fetch)
	for _, sym := range symbols {
		if err := errs[sym]; err != nil {
			return projectDCAOutput{}, err
		}
		if err := requireUSD(series[sym]); err != nil {
			return projectDCAOutput{}, err
		}
		if note := instrumentNote(series[sym]); note != "" {
			warnings = append(warnings, note)
		}
	}
	if err := errs[fxSymbol]; krw && err != nil {
		return projectDCAOutput{}, fmt.Errorf("exchange rate: %w", err)
	}

	feeRate, feeNote := foldCommission(in.FeeRate, in.CommissionFixed, in.Amount, len(allocs))
	plan := analytics.MCPlan{
		Symbols:      symbols,
		Weights:      weights,
		Amount:       in.Amount,
		Currency:     currency,
		Cadence:      analytics.Cadence(cadence),
		HorizonYears: in.HorizonYears,
		FeeRate:      feeRate,
	}
	res, err := analytics.MonteCarloContext(ctx, plan, cfg, analytics.MCInput{Series: series, FX: series[fxSymbol]})
	if err != nil {
		return projectDCAOutput{}, userError(err)
	}
	if err := checkFinite("forecast", append([]float64{res.Invested, res.MeanFinal}, mapValues(res.Percentiles)...)...); err != nil {
		return projectDCAOutput{}, err
	}

	trends := make([]symbolTrendOutput, 0, len(symbols))
	for _, sym := range symbols {
		t, err := analytics.AnalyzeTrend(series[sym], time.Time{})
		if err != nil {
			return projectDCAOutput{}, userError(err)
		}
		trends = append(trends, symbolTrendOutput{Symbol: sym, trendOutput: toTrendOutput(t)})
	}

	out := projectDCAOutput{
		Allocations:               toAllocationOutputs(allocs),
		Amount:                    in.Amount,
		Currency:                  currency,
		Cadence:                   string(cadence),
		HorizonYears:              res.HorizonYears,
		Simulations:               res.Simulations,
		BlockLength:               res.BlockLength,
		Seed:                      res.Seed,
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
		Trend:                     trends,
		Warnings:                  warnings,
		Disclaimer:                Disclaimer,
	}
	if feeNote != "" {
		out.Assumptions = append(out.Assumptions, feeNote)
	}
	if w := shortHistoryWarning(res); w != "" {
		out.Warnings = append(out.Warnings, w)
	}
	for _, sym := range fetch {
		out.Warnings = append(out.Warnings, d.staleWarnings(sym)...)
		out.Warnings = append(out.Warnings, nonEmpty(provisionalNote(series[sym], time.Time{}))...)
	}
	return out, nil
}

// foldCommission turns a fixed commission per ETF purchased into the part
// of the fee rate the Monte Carlo charges, and explains it. A contribution
// of amount in n ETFs loses amount × feeRate + n × fixed, which is the
// fraction feeRate + n × fixed / amount of it: exact in total, because the
// amount is constant in the plan currency. The engine then splits the net
// by weight, so in a portfolio each ETF bears the commissions in
// proportion to its weight rather than one commission each; the note says
// so. Without a fixed commission feeRate is returned unchanged and the
// note is "".
func foldCommission(feeRate, fixed, amount float64, n int) (float64, string) {
	if fixed == 0 {
		return feeRate, ""
	}
	folded := feeRate + float64(n)*fixed/amount
	if n == 1 {
		return folded, fmt.Sprintf("commission_fixed %s is charged as part of the fee rate: fee_rate + commission_fixed / amount = %s%% of every contribution, exact for a constant amount", obsNum(fixed), obsNum(folded*100))
	}
	return folded, fmt.Sprintf("commission_fixed %s per ETF is charged as part of the fee rate: fee_rate + %d × commission_fixed / amount = %s%% of every contribution. The total is exact; the projection spreads it over the ETFs by weight, whereas a broker takes the same commission from every order",
		obsNum(fixed), n, obsNum(folded*100))
}

// minResampledYears is how much history a projection should resample
// before its range is taken at face value: shorter histories hold one
// market period rather than a cycle, so project_dca_outcomes and review_dca_plan
// warn below it.
const minResampledYears = 5

// resampledYears is the length of the history res resampled, in calendar
// years.
func resampledYears(res *analytics.MCResult) float64 {
	return res.LookbackTo.Sub(res.LookbackFrom).Hours() / 24 / 365.25
}

// historySlack absorbs the few holidays by which a lookback of N trading
// years can fall short of N calendar years, so that lookback_years equal
// to horizon_years is not flagged as shorter than the horizon.
const historySlack = 0.99

// shortHistory reports whether years of resampled history are too few for
// a projection over horizon years: fewer than the horizon or than
// minResampledYears, within historySlack.
func shortHistory(years, horizon float64) bool {
	return years < historySlack*horizon || years < historySlack*minResampledYears
}

// shortHistoryWarning returns a caution when res resampled too little
// history for its horizon (see shortHistory), and "" otherwise.
func shortHistoryWarning(res *analytics.MCResult) string {
	years := resampledYears(res)
	if !shortHistory(years, res.HorizonYears) {
		return ""
	}
	from, to := formatDate(res.LookbackFrom), formatDate(res.LookbackTo)
	if years < historySlack*res.HorizonYears {
		return fmt.Sprintf("the projection resamples only %.1f years of history (%s to %s) to project %s: every path is stitched together from that one stretch, so the range shows what that period would compound to over the horizon, not the conditions a longer history holds",
			years, from, to, obsYears(res.HorizonYears))
	}
	return fmt.Sprintf("the projection resamples only %.1f years of history (%s to %s), less than %d years, so the range reflects one market period rather than a full cycle",
		years, from, to, minResampledYears)
}

// maxExpectedReturnPct bounds the override; beyond it prices overflow
// within a long horizon.
const maxExpectedReturnPct = 1000.0

// mapValues returns the values of m in no particular order.
func mapValues(m map[string]float64) []float64 {
	out := make([]float64, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// resolveAllocations accepts exactly one of symbol or allocations.
func resolveAllocations(symbol string, allocs []allocationInput) ([]sim.Allocation, error) {
	sym := normalizeSymbol(symbol)
	switch {
	case sym != "" && len(allocs) > 0:
		return nil, errors.New("give either symbol or allocations, not both")
	case sym != "":
		return []sim.Allocation{{Symbol: sym, Weight: 1}}, nil
	case len(allocs) > 0:
		return parseAllocations(allocs)
	default:
		return nil, errors.New("give symbol (one ETF) or allocations (a portfolio); use list_etfs to find symbols")
	}
}

// roundMap rounds every value of a percentile map to two decimals. The
// result is never nil, so it encodes as an object.
func roundMap(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = round2(v)
	}
	return out
}
