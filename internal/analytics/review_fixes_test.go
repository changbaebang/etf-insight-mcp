package analytics

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestMonthsBack(t *testing.T) {
	tests := []struct {
		from string
		n    int
		want string
	}{
		{"2023-05-31", 1, "2023-04-30"},  // April has 30 days
		{"2023-05-31", 3, "2023-02-28"},  // February
		{"2024-02-29", 12, "2023-02-28"}, // leap day minus a year
		{"2024-01-31", 1, "2023-12-31"},  // no overflow across the year
		{"2024-03-15", 1, "2024-02-15"},  // ordinary day
		{"2024-08-31", 6, "2024-02-29"},  // leap February
		{"2024-03-31", 1, "2024-02-29"},
	}
	for _, tt := range tests {
		from, _ := market.ParseDate(tt.from)
		got := monthsBack(from, tt.n)
		if got.Format(market.DateLayout) != tt.want {
			t.Errorf("monthsBack(%s, %d) = %s, want %s", tt.from, tt.n, got.Format(market.DateLayout), tt.want)
		}
	}
}

func TestWindowsAnnualizedAvailability(t *testing.T) {
	s := growthSeries("A", 300, 0.0004) // about 14 months of weekdays
	sum, err := Summarize(s, time.Time{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	for _, w := range sum.Windows {
		switch w.Label {
		case "1m", "3m", "6m":
			if !w.Available || w.AnnualizedAvailable || w.Annualized != w.TotalReturn {
				t.Errorf("%s: available=%v annualizedAvailable=%v annualized=%v total=%v; want available, not annualized",
					w.Label, w.Available, w.AnnualizedAvailable, w.Annualized, w.TotalReturn)
			}
		case "1y":
			if !w.Available || !w.AnnualizedAvailable {
				t.Errorf("1y: available=%v annualizedAvailable=%v, want both true", w.Available, w.AnnualizedAvailable)
			}
		}
	}
}

func TestTrendReasonsMentionMissingIndicators(t *testing.T) {
	tr, err := AnalyzeTrend(growthSeries("X", 240, 0.001), time.Time{})
	if err != nil {
		t.Fatalf("AnalyzeTrend: %v", err)
	}
	if tr.Momentum12_1 != 0 {
		t.Errorf("Momentum12_1 = %v, want 0 with 240 bars", tr.Momentum12_1)
	}
	joined := strings.Join(tr.Reasons, " | ")
	if !strings.Contains(joined, "12-1 momentum needs 253 bars, only 240 available") {
		t.Errorf("Reasons = %q, want a momentum availability reason", tr.Reasons)
	}
	if strings.Contains(joined, "6-month return needs") {
		t.Errorf("Reasons = %q, 6-month return is available with 240 bars", tr.Reasons)
	}
	if tr.State != StateUptrend {
		t.Errorf("State = %s, want uptrend for a rising series with the slope known", tr.State)
	}
}

// TestMonteCarloBasketStatistics: A grows +1% a day and B falls 1% a day in
// log terms. The weighted sum of log returns is exactly 0, but a 50/50
// basket rebalanced daily grows by log(cosh(0.01)) per day.
func TestMonteCarloBasketStatistics(t *testing.T) {
	in := MCInput{Series: map[string]*market.Series{
		"A": growthSeries("A", 300, 0.01),
		"B": growthSeries("B", 300, -0.01),
	}}
	plan := MCPlan{Symbols: []string{"A", "B"}, Weights: []float64{0.5, 0.5}, Amount: 100,
		Currency: CurrencyUSD, Cadence: CadenceMonthly, HorizonYears: 1}
	res, err := MonteCarlo(plan, MCConfig{Simulations: 20}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	want := math.Exp(tradingDaysPerYear*math.Log(math.Cosh(0.01))) - 1
	if !approxRel(res.HistoricalAnnualReturn, want, 1e-9) {
		t.Errorf("HistoricalAnnualReturn = %v, want %v (daily-rebalanced basket)", res.HistoricalAnnualReturn, want)
	}
	if res.HistoricalVolatility > 1e-9 {
		t.Errorf("HistoricalVolatility = %v, want 0 for constant returns", res.HistoricalVolatility)
	}
}

func TestMonteCarloKRWHistoricalIncludesFX(t *testing.T) {
	in := MCInput{
		Series: map[string]*market.Series{"A": growthSeries("A", 300, 0)},
		FX:     growthSeries("KRW=X", 300, 0.0004),
	}
	plan := MCPlan{Symbols: []string{"A"}, Weights: []float64{1}, Amount: 100000,
		Currency: CurrencyKRW, Cadence: CadenceMonthly, HorizonYears: 1}
	res, err := MonteCarlo(plan, MCConfig{Simulations: 20}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	want := math.Exp(tradingDaysPerYear*0.0004) - 1
	if !approxRel(res.HistoricalAnnualReturn, want, 1e-9) {
		t.Errorf("HistoricalAnnualReturn = %v, want %v (FX drift included)", res.HistoricalAnnualReturn, want)
	}
	if !hasReason(res.Assumptions, "exchange-rate moves are included") {
		t.Errorf("Assumptions = %q, want the FX statistics note", res.Assumptions)
	}
}

// TestMonteCarloOverrideExact: with zero volatility the override must make
// the basket compound at exactly the requested rate.
func TestMonteCarloOverrideExact(t *testing.T) {
	in := MCInput{Series: map[string]*market.Series{"A": growthSeries("A", 300, 0.0004)}}
	target := math.Log(1.07)
	plan := MCPlan{Symbols: []string{"A"}, Weights: []float64{1}, Amount: 100,
		Currency: CurrencyUSD, Cadence: CadenceMonthly, HorizonYears: 1}
	res, err := MonteCarlo(plan, MCConfig{Simulations: 20, ExpectedAnnualReturn: &target}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	want := deterministicFinal(100, target/tradingDaysPerYear, 252, 21)
	if !approxRel(res.Percentiles["p50"], want, 1e-9) {
		t.Errorf("p50 = %v, want %v (7%% compound growth)", res.Percentiles["p50"], want)
	}
	if !hasReason(res.Assumptions, "compound annual growth in USD is 7.00%") {
		t.Errorf("Assumptions = %q, want the override stated as 7.00%% compound growth", res.Assumptions)
	}
}

func TestMonteCarloConfigLimits(t *testing.T) {
	in := MCInput{Series: map[string]*market.Series{"A": growthSeries("A", 300, 0.0004)}}
	plan := MCPlan{Symbols: []string{"A"}, Weights: []float64{1}, Amount: 100,
		Currency: CurrencyUSD, Cadence: CadenceDaily, HorizonYears: 1}
	for name, cfg := range map[string]MCConfig{
		"lookback above the cap": {LookbackYears: MaxLookbackYears + 1},
		"huge lookback":          {LookbackYears: 1e300},
		"block length above cap": {BlockLength: MaxBlockLength + 1},
		"block length max int":   {BlockLength: math.MaxInt},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := MonteCarlo(plan, cfg, in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
	_, err := MonteCarlo(plan, MCConfig{BlockLength: 299}, in) // 300 bars → 299 returns: fits exactly
	if err != nil {
		t.Errorf("block length equal to the history should be accepted: %v", err)
	}
	_, err = MonteCarlo(plan, MCConfig{BlockLength: 300}, in)
	if !errors.Is(err, ErrInsufficientHistory) {
		t.Errorf("block length longer than the history: error = %v, want ErrInsufficientHistory", err)
	}
}

func TestContributesAt(t *testing.T) {
	count := func(period float64) int {
		n := 0
		for t := range 252 {
			if contributesAt(t, period) {
				n++
			}
		}
		return n
	}
	if got := count(1); got != 252 {
		t.Errorf("daily contributions in a year = %d, want 252", got)
	}
	if got := count(tradingDaysPerYear / 52.0); got != 52 {
		t.Errorf("weekly contributions in a year = %d, want 52", got)
	}
	if got := count(21); got != 12 {
		t.Errorf("monthly contributions in a year = %d, want 12", got)
	}
	if contributionCount(252, tradingDaysPerYear/52.0) != 52 || contributionCount(1, 21) != 1 {
		t.Error("contributionCount disagrees with contributesAt")
	}
}
