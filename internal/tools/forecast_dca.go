package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type forecastDCAInput struct {
	Symbol                  string            `json:"symbol,omitempty" jsonschema:"single ticker to project, e.g. VOO; give either symbol or allocations, not both"`
	Allocations             []allocationInput `json:"allocations,omitempty" jsonschema:"portfolio to project as a list of {symbol, weight} with weights summing to 1 or 100; alternative to symbol"`
	Amount                  float64           `json:"amount" jsonschema:"size of one contribution in currency before fees, e.g. 100 (USD) or 10000 (KRW); must be > 0"`
	Currency                string            `json:"currency,omitempty" jsonschema:"USD or KRW (default USD); with KRW the exchange rate path is resampled jointly from KRW=X"`
	Cadence                 string            `json:"cadence,omitempty" jsonschema:"daily, weekly or monthly (default daily); approximated as a purchase every 1, 4.85 (252/52, so 52 a year) or 21 simulated trading days"`
	HorizonYears            float64           `json:"horizon_years" jsonschema:"length of the plan in years, > 0 and at most 40, e.g. 5"`
	FeeRate                 float64           `json:"fee_rate,omitempty" jsonschema:"fraction of each contribution lost to commissions, e.g. 0.001 for 0.1% (default 0)"`
	Simulations             int               `json:"simulations,omitempty" jsonschema:"number of bootstrap paths, 1 to 20000 (default 2000); more paths smooth the percentiles but take longer"`
	Seed                    uint64            `json:"seed,omitempty" jsonschema:"random seed (default 42); the same seed and inputs always give the same result"`
	BlockLength             int               `json:"block_length,omitempty" jsonschema:"bootstrap block length in trading days (default 21, about one month); longer blocks keep more of the historical autocorrelation"`
	LookbackYears           float64           `json:"lookback_years,omitempty" jsonschema:"resample only the last N years of history, at most 200 (default 0 = all history every symbol shares)"`
	ExpectedAnnualReturnPct *float64          `json:"expected_annual_return_pct,omitempty" jsonschema:"optional external assumption such as a broker's long-run forecast, as a PERCENTAGE: 7 means 7% compound annual growth (0.07 would mean 0.07%). Must be above -100 and at most 1000. Shifts every resampled daily return so the portfolio's expected growth matches it instead of the historical average (volatility and correlations are kept)"`
}

// symbolTrendOutput is the trend of one symbol in a forecast.
type symbolTrendOutput struct {
	Symbol string `json:"symbol"`
	trendOutput
}

type forecastDCAOutput struct {
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

func registerForecastDCA(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forecast_dca",
		Description: "Projects the range of outcomes of a recurring-purchase plan over horizon_years by resampling the symbols' own daily history with a block bootstrap: blocks of consecutive past days are drawn at random (the same days for every symbol and for the KRW=X rate, so correlations survive) and the plan is run on thousands of such paths. This is NOT a price prediction: it only shows what the plan would have produced under reshuffled history, nothing that did not happen in the lookback can happen in a path, and the historical average return is assumed to persist unless expected_annual_return_pct overrides it. Returns percentiles (p5 to p95) of the final value and of the return, the probability of ending below the amount invested, the mean, the historical return and volatility the bootstrap is built on, the lookback range, the modelling assumptions in words and each symbol's current trend. Deterministic for a given seed.",
		Title:       "Forecast DCA",
		Annotations: readOnly("Forecast DCA", true),
		InputSchema: inputSchema[forecastDCAInput](schemaTweaks{
			defaults: map[string]any{"currency": "USD", "cadence": "daily", "simulations": analytics.DefaultSimulations, "seed": analytics.DefaultSeed, "block_length": analytics.DefaultBlockLength, "lookback_years": 0, "fee_rate": 0},
			minItems: map[string]int{"allocations": 1},
		}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in forecastDCAInput) (*mcp.CallToolResult, forecastDCAOutput, error) {
		out, err := d.forecastDCA(ctx, in)
		return nil, out, err
	})
}

// forecastDCA validates the plan, fetches the histories and runs the
// Monte Carlo.
func (d Deps) forecastDCA(ctx context.Context, in forecastDCAInput) (forecastDCAOutput, error) {
	allocs, err := resolveAllocations(in.Symbol, in.Allocations)
	if err != nil {
		return forecastDCAOutput{}, err
	}
	currency, err := parseCurrency(in.Currency)
	if err != nil {
		return forecastDCAOutput{}, err
	}
	cadence, err := parseCadence(in.Cadence)
	if err != nil {
		return forecastDCAOutput{}, err
	}
	if err := checkAmount(in.Amount); err != nil {
		return forecastDCAOutput{}, err
	}
	if in.HorizonYears <= 0 || in.HorizonYears > analytics.MaxHorizonYears {
		return forecastDCAOutput{}, fmt.Errorf("horizon_years must be > 0 and at most %d, got %v", analytics.MaxHorizonYears, in.HorizonYears)
	}
	if err := checkFeeRate(in.FeeRate); err != nil {
		return forecastDCAOutput{}, err
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
			return forecastDCAOutput{}, fmt.Errorf("expected_annual_return_pct must be above -100 and at most %v, got %v", maxExpectedReturnPct, r)
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
			return forecastDCAOutput{}, err
		}
	}
	if err := errs[fxSymbol]; krw && err != nil {
		return forecastDCAOutput{}, fmt.Errorf("exchange rate: %w", err)
	}

	plan := analytics.MCPlan{
		Symbols:      symbols,
		Weights:      weights,
		Amount:       in.Amount,
		Currency:     currency,
		Cadence:      analytics.Cadence(cadence),
		HorizonYears: in.HorizonYears,
		FeeRate:      in.FeeRate,
	}
	res, err := analytics.MonteCarlo(plan, cfg, analytics.MCInput{Series: series, FX: series[fxSymbol]})
	if err != nil {
		return forecastDCAOutput{}, userError(err)
	}
	if err := checkFinite("forecast", append([]float64{res.Invested, res.MeanFinal}, mapValues(res.Percentiles)...)...); err != nil {
		return forecastDCAOutput{}, err
	}

	trends := make([]symbolTrendOutput, 0, len(symbols))
	for _, sym := range symbols {
		t, err := analytics.AnalyzeTrend(series[sym], time.Time{})
		if err != nil {
			return forecastDCAOutput{}, userError(err)
		}
		trends = append(trends, symbolTrendOutput{Symbol: sym, trendOutput: toTrendOutput(t)})
	}

	out := forecastDCAOutput{
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
	for _, sym := range fetch {
		out.Warnings = append(out.Warnings, d.staleWarnings(sym)...)
	}
	return out, nil
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
