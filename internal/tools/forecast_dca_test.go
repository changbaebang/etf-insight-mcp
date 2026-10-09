package tools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// percentileKeys is every key the percentile maps must carry.
var percentileKeys = []string{"p5", "p10", "p25", "p50", "p75", "p90", "p95"}

func TestForecastDCA(t *testing.T) {
	src := newFakeSource()
	sess := newSession(t, testDeps(src))
	base := map[string]any{"symbol": "voo", "amount": 100, "cadence": "monthly", "horizon_years": 5, "simulations": 200, "seed": 7}

	var first forecastDCAOutput
	callOK(t, sess, "forecast_dca", base, &first)

	t.Run("shape", func(t *testing.T) {
		out := first
		if len(out.Allocations) != 1 || out.Allocations[0].Symbol != "VOO" || out.Allocations[0].WeightPct != 100 {
			t.Errorf("allocations = %+v", out.Allocations)
		}
		if out.Currency != "USD" || out.Cadence != "monthly" || out.HorizonYears != 5 || out.Simulations != 200 || out.Seed != 7 || out.BlockLength != analytics.DefaultBlockLength {
			t.Errorf("echo = %+v", out)
		}
		if out.Contributions != 60 || out.Invested != 6000 {
			t.Errorf("contributions/invested = %d/%v, want 60/6000", out.Contributions, out.Invested)
		}
		for _, key := range percentileKeys {
			if _, ok := out.Percentiles[key]; !ok {
				t.Errorf("percentiles lack %s", key)
			}
			if _, ok := out.ReturnPctPercentiles[key]; !ok {
				t.Errorf("return_pct_percentiles lack %s", key)
			}
		}
		if !(out.Percentiles["p10"] <= out.Percentiles["p50"] && out.Percentiles["p50"] <= out.Percentiles["p90"]) {
			t.Errorf("percentiles not ordered: %v", out.Percentiles)
		}
		if out.ProbLossPct < 0 || out.ProbLossPct > 100 || out.MeanFinal <= 0 || out.HistoricalVolatilityPct <= 0 {
			t.Errorf("statistics look wrong: %+v", out)
		}
		first, _ := src.series["VOO"].First()
		last, _ := src.series["VOO"].Last()
		if out.LookbackFrom != first.Date.Format(market.DateLayout) || out.LookbackTo != last.Date.Format(market.DateLayout) {
			t.Errorf("lookback = %s..%s, want the whole VOO history", out.LookbackFrom, out.LookbackTo)
		}
		if len(out.Assumptions) < 5 || len(out.Trend) != 1 || out.Trend[0].Symbol != "VOO" || out.Trend[0].SMA200 <= 0 {
			t.Errorf("assumptions/trend = %d/%+v", len(out.Assumptions), out.Trend)
		}
		if out.Disclaimer != Disclaimer || len(out.Warnings) != 0 {
			t.Errorf("disclaimer ok %v, warnings %v", out.Disclaimer == Disclaimer, out.Warnings)
		}
	})

	t.Run("deterministic for a seed", func(t *testing.T) {
		var again forecastDCAOutput
		callOK(t, sess, "forecast_dca", base, &again)
		if !reflect.DeepEqual(again.Percentiles, first.Percentiles) || !reflect.DeepEqual(again.ReturnPctPercentiles, first.ReturnPctPercentiles) || again.MeanFinal != first.MeanFinal {
			t.Errorf("second run differs: %v vs %v", again.Percentiles, first.Percentiles)
		}
	})

	t.Run("matches analytics.MonteCarlo", func(t *testing.T) {
		res, err := analytics.MonteCarlo(analytics.MCPlan{
			Symbols: []string{"VOO"}, Weights: []float64{1}, Amount: 100, Currency: "USD", Cadence: analytics.CadenceMonthly, HorizonYears: 5,
		}, analytics.MCConfig{Simulations: 200, Seed: 7}, analytics.MCInput{Series: src.series})
		if err != nil {
			t.Fatalf("MonteCarlo: %v", err)
		}
		for _, key := range percentileKeys {
			if first.Percentiles[key] != round2(res.Percentiles[key]) {
				t.Errorf("%s = %v, want %v", key, first.Percentiles[key], round2(res.Percentiles[key]))
			}
		}
		if first.ProbLossPct != pct(res.ProbLoss) || first.HistoricalAnnualReturnPct != pct(res.HistoricalAnnualReturn) {
			t.Errorf("prob_loss/historical = %v/%v, want %v/%v", first.ProbLossPct, first.HistoricalAnnualReturnPct, pct(res.ProbLoss), pct(res.HistoricalAnnualReturn))
		}
	})

	t.Run("a different seed changes the result", func(t *testing.T) {
		args := map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "horizon_years": 5, "simulations": 200, "seed": 8}
		var other forecastDCAOutput
		callOK(t, sess, "forecast_dca", args, &other)
		if reflect.DeepEqual(other.Percentiles, first.Percentiles) {
			t.Error("seed 8 reproduced seed 7")
		}
	})

	t.Run("portfolio uses the common history", func(t *testing.T) {
		args := map[string]any{
			"allocations": []map[string]any{{"symbol": "VOO", "weight": 50}, {"symbol": "SCHD", "weight": 50}},
			"amount":      100, "horizon_years": 2, "simulations": 100,
		}
		var out forecastDCAOutput
		callOK(t, sess, "forecast_dca", args, &out)
		if len(out.Trend) != 2 || out.Trend[1].Symbol != "SCHD" || len(out.Allocations) != 2 || out.Allocations[0].WeightPct != 50 {
			t.Errorf("trend/allocations = %+v/%+v", out.Trend, out.Allocations)
		}
		if out.LookbackFrom != "2022-01-03" {
			t.Errorf("lookback_from = %s, want 2022-01-03 (SCHD start)", out.LookbackFrom)
		}
		if out.Cadence != "daily" || out.Seed != analytics.DefaultSeed || out.Simulations != 100 {
			t.Errorf("defaults = %+v", out)
		}
	})

	t.Run("KRW resamples the exchange rate too", func(t *testing.T) {
		args := map[string]any{"symbol": "VOO", "amount": 10000, "currency": "KRW", "cadence": "daily", "horizon_years": 1, "simulations": 100}
		var out forecastDCAOutput
		callOK(t, sess, "forecast_dca", args, &out)
		if out.Currency != "KRW" || out.Invested != 2520000 {
			t.Errorf("currency/invested = %s/%v, want KRW/2520000", out.Currency, out.Invested)
		}
		if !strings.Contains(strings.Join(out.Assumptions, " "), "KRW") {
			t.Errorf("assumptions do not mention KRW: %v", out.Assumptions)
		}
	})

	t.Run("expected return override", func(t *testing.T) {
		args := map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "horizon_years": 5, "simulations": 200, "seed": 7, "expected_annual_return_pct": 7}
		var out forecastDCAOutput
		callOK(t, sess, "forecast_dca", args, &out)
		if !strings.Contains(strings.Join(out.Assumptions, " "), "override") {
			t.Errorf("assumptions do not mention the override: %v", out.Assumptions)
		}
		if out.HistoricalAnnualReturnPct != first.HistoricalAnnualReturnPct {
			t.Errorf("historical return changed with the override: %v vs %v", out.HistoricalAnnualReturnPct, first.HistoricalAnnualReturnPct)
		}
		if reflect.DeepEqual(out.Percentiles, first.Percentiles) {
			t.Error("override did not change the percentiles")
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "both symbol and allocations", args: map[string]any{"symbol": "VOO", "allocations": []map[string]any{{"symbol": "SPY", "weight": 1}}, "amount": 100, "horizon_years": 1}, want: "not both"},
		{name: "neither", args: map[string]any{"amount": 100, "horizon_years": 1}, want: "give symbol"},
		{name: "unknown symbol", args: map[string]any{"symbol": "XYZ", "amount": 100, "horizon_years": 1}, want: "unknown symbol XYZ"},
		{name: "zero horizon", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 0}, want: "horizon_years must be > 0"},
		{name: "long horizon", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 50}, want: "at most 40"},
		{name: "too many simulations", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 1, "simulations": 30000}, want: "exceeds the maximum"},
		{name: "negative simulations", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 1, "simulations": -1}, want: "simulations must be positive"},
		{name: "block longer than history", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 1, "block_length": 1000}, want: "insufficient history"},
		{name: "bad cadence", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 1, "cadence": "yearly"}, want: "use daily, weekly or monthly"},
		{name: "zero amount", args: map[string]any{"symbol": "VOO", "amount": 0, "horizon_years": 1}, want: "amount must be > 0"},
		{name: "impossible expected return", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 1, "expected_annual_return_pct": -100}, want: "greater than -100"},
		{name: "negative seed", args: map[string]any{"symbol": "VOO", "amount": 100, "horizon_years": 1, "seed": -1}, want: "seed"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "forecast_dca", tt.args, tt.want)
		})
	}
}
