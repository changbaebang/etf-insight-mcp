package analytics

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// percentileKeys lists every key MCResult.Percentiles must carry.
var percentileKeys = []string{"p5", "p10", "p25", "p50", "p75", "p90", "p95"}

// basePlan is a one-symbol monthly USD plan over one year.
func basePlan(symbols ...string) MCPlan {
	return MCPlan{
		Symbols:      symbols,
		Amount:       100,
		Currency:     CurrencyUSD,
		Cadence:      CadenceMonthly,
		HorizonYears: 1,
		FeeRate:      0.01,
	}
}

// deterministicFinal is the final value of a plan whose price grows by a
// constant daily log return c and whose contributions are placed every
// interval steps starting at step 0: net * sum over k of exp(c * (steps -
// k*interval)).
func deterministicFinal(net, c float64, steps, interval int) float64 {
	var total float64
	for t := 0; t < steps; t += interval {
		total += net * math.Exp(c*float64(steps-t))
	}
	return total
}

func TestMonteCarloZeroVolatility(t *testing.T) {
	const c = 0.0004
	in := MCInput{Series: map[string]*market.Series{"A": growthSeries("A", 300, c)}}

	tests := []struct {
		cadence           Cadence
		wantContributions int
	}{
		{CadenceDaily, 252},
		{CadenceWeekly, 51},
		{CadenceMonthly, 12},
	}
	for _, tt := range tests {
		t.Run(string(tt.cadence), func(t *testing.T) {
			plan := basePlan("A")
			plan.Cadence = tt.cadence
			res, err := MonteCarlo(plan, MCConfig{Simulations: 50}, in)
			if err != nil {
				t.Fatalf("MonteCarlo: %v", err)
			}
			if res.Contributions != tt.wantContributions {
				t.Fatalf("Contributions = %d, want %d", res.Contributions, tt.wantContributions)
			}
			if want := float64(tt.wantContributions) * plan.Amount; res.Invested != want {
				t.Fatalf("Invested = %v, want %v", res.Invested, want)
			}
			interval, _ := tt.cadence.interval()
			want := deterministicFinal(plan.Amount*(1-plan.FeeRate), c, 252, interval)
			for _, key := range percentileKeys {
				got, ok := res.Percentiles[key]
				if !ok {
					t.Fatalf("percentile %s missing", key)
				}
				if !approxRel(got, want, 1e-9) {
					t.Fatalf("%s = %v, want %v", key, got, want)
				}
				if wantPct := (want/res.Invested - 1) * 100; !approxRel(res.ReturnPctPercentiles[key], wantPct, 1e-9) {
					t.Fatalf("return %s = %v, want %v", key, res.ReturnPctPercentiles[key], wantPct)
				}
			}
			if !approxRel(res.MeanFinal, want, 1e-9) {
				t.Fatalf("MeanFinal = %v, want %v", res.MeanFinal, want)
			}
			if res.ProbLoss != 0 {
				t.Fatalf("ProbLoss = %v, want 0 for a growing plan", res.ProbLoss)
			}
			if want := math.Exp(252*c) - 1; !approxRel(res.HistoricalAnnualReturn, want, 1e-9) {
				t.Fatalf("HistoricalAnnualReturn = %v, want %v", res.HistoricalAnnualReturn, want)
			}
			if res.HistoricalVolatility > 1e-9 {
				t.Fatalf("HistoricalVolatility = %v, want ~0", res.HistoricalVolatility)
			}
			if res.Simulations != 50 || res.BlockLength != DefaultBlockLength || res.Seed != DefaultSeed {
				t.Fatalf("effective config wrong: %+v", res)
			}
		})
	}
}

func TestMonteCarloReproducible(t *testing.T) {
	in := MCInput{Series: map[string]*market.Series{"A": randomWalkSeries("A", 500, 7, 0.0003, 0.012)}}
	plan := basePlan("A")

	first, err := MonteCarlo(plan, MCConfig{Simulations: 200, Seed: 99}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	second, err := MonteCarlo(plan, MCConfig{Simulations: 200, Seed: 99}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same seed gave different results:\n%+v\n%+v", first, second)
	}

	other, err := MonteCarlo(plan, MCConfig{Simulations: 200, Seed: 100}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	if other.Percentiles["p50"] == first.Percentiles["p50"] {
		t.Fatalf("different seeds gave the same p50 %v", first.Percentiles["p50"])
	}

	zero, err := MonteCarlo(plan, MCConfig{Simulations: 200}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	explicit, err := MonteCarlo(plan, MCConfig{Simulations: 200, Seed: DefaultSeed}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	if zero.Seed != DefaultSeed || !reflect.DeepEqual(zero, explicit) {
		t.Fatalf("zero seed must equal the default seed %d", DefaultSeed)
	}
}

func TestMonteCarloDistributionShape(t *testing.T) {
	in := MCInput{Series: map[string]*market.Series{"A": randomWalkSeries("A", 500, 3, -0.0002, 0.015)}}
	res, err := MonteCarlo(basePlan("A"), MCConfig{Simulations: 500}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	if res.ProbLoss < 0 || res.ProbLoss > 1 {
		t.Fatalf("ProbLoss = %v, want within [0, 1]", res.ProbLoss)
	}
	if res.ProbLoss == 0 {
		t.Fatalf("a negative-drift walk should lose money on some paths")
	}
	for i := 1; i < len(percentileKeys); i++ {
		lo, hi := res.Percentiles[percentileKeys[i-1]], res.Percentiles[percentileKeys[i]]
		if lo > hi {
			t.Fatalf("percentiles not monotone: %s=%v > %s=%v", percentileKeys[i-1], lo, percentileKeys[i], hi)
		}
	}
	if res.HistoricalVolatility <= 0 {
		t.Fatalf("HistoricalVolatility = %v, want positive", res.HistoricalVolatility)
	}
	if len(res.Assumptions) == 0 {
		t.Fatalf("Assumptions must not be empty")
	}
}

func TestMonteCarloExpectedReturnOverride(t *testing.T) {
	const c = 0.0004
	in := MCInput{Series: map[string]*market.Series{"A": growthSeries("A", 300, c)}}
	plan := basePlan("A")

	history, err := MonteCarlo(plan, MCConfig{Simulations: 20}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	zero := 0.0
	flat, err := MonteCarlo(plan, MCConfig{Simulations: 20, ExpectedAnnualReturn: &zero}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	if flat.Percentiles["p50"] >= history.Percentiles["p50"] {
		t.Fatalf("override 0 must lower the median: %v >= %v", flat.Percentiles["p50"], history.Percentiles["p50"])
	}
	// With zero drift and zero volatility the price never moves, so the
	// final value is exactly the net invested amount.
	if want := flat.Invested * (1 - plan.FeeRate); !approxRel(flat.Percentiles["p50"], want, 1e-9) {
		t.Fatalf("flat p50 = %v, want %v", flat.Percentiles["p50"], want)
	}
	if !approxRel(flat.HistoricalAnnualReturn, history.HistoricalAnnualReturn, 1e-12) {
		t.Fatalf("override must not change the historical figure: %v vs %v", flat.HistoricalAnnualReturn, history.HistoricalAnnualReturn)
	}
	if !hasReason(flat.Assumptions, "override") || hasReason(history.Assumptions, "override") {
		t.Fatalf("override must be stated exactly when set:\n%v\n%v", flat.Assumptions, history.Assumptions)
	}
}

func TestMonteCarloJointResampling(t *testing.T) {
	a := randomWalkSeries("A", 500, 11, 0.0003, 0.012)
	b := &market.Series{Meta: market.Meta{Symbol: "B", Currency: "USD"}, Bars: a.Bars}
	in := MCInput{Series: map[string]*market.Series{"A": a, "B": b}}

	single, err := MonteCarlo(basePlan("A"), MCConfig{Simulations: 300}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	pair := basePlan("A", "B")
	pair.Weights = []float64{0.5, 0.5}
	split, err := MonteCarlo(pair, MCConfig{Simulations: 300}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	for _, key := range percentileKeys {
		if !approxRel(single.Percentiles[key], split.Percentiles[key], 1e-9) {
			t.Fatalf("%s: single %v, split %v", key, single.Percentiles[key], split.Percentiles[key])
		}
	}
	if !approxRel(single.HistoricalAnnualReturn, split.HistoricalAnnualReturn, 1e-9) {
		t.Fatalf("historical return differs: %v vs %v", single.HistoricalAnnualReturn, split.HistoricalAnnualReturn)
	}
}

func TestMonteCarloKRW(t *testing.T) {
	const assetLog, fxLog = 0.0004, 0.0002
	dates := weekdays(day(2020, time.January, 1), 300)
	asset := growthSeries("A", 300, assetLog)
	flatAsset := growthSeries("A", 300, 0)
	flatFX := seriesFrom("KRW=X", dates, func(_ int, _ time.Time) float64 { return 1300 })
	movingFX := seriesFrom("KRW=X", dates, func(i int, _ time.Time) float64 {
		return 1300 * math.Exp(fxLog*float64(i))
	})

	t.Run("constant fx equals the usd plan", func(t *testing.T) {
		krw := basePlan("A")
		krw.Currency = "krw" // lower case is accepted
		krw.Amount = 1_300_000
		krwRes, err := MonteCarlo(krw, MCConfig{Simulations: 20}, MCInput{Series: map[string]*market.Series{"A": asset}, FX: flatFX})
		if err != nil {
			t.Fatalf("MonteCarlo KRW: %v", err)
		}
		usd := basePlan("A")
		usd.Amount = 1_300_000
		usdRes, err := MonteCarlo(usd, MCConfig{Simulations: 20}, MCInput{Series: map[string]*market.Series{"A": asset}})
		if err != nil {
			t.Fatalf("MonteCarlo USD: %v", err)
		}
		for _, key := range percentileKeys {
			if !approxRel(krwRes.Percentiles[key], usdRes.Percentiles[key], 1e-9) {
				t.Fatalf("%s: krw %v, usd %v", key, krwRes.Percentiles[key], usdRes.Percentiles[key])
			}
		}
		if !hasReason(krwRes.Assumptions, "KRW") || hasReason(usdRes.Assumptions, "KRW contributions") {
			t.Fatalf("KRW assumption must be stated exactly for KRW plans")
		}
	})

	t.Run("moving fx with a flat asset", func(t *testing.T) {
		plan := basePlan("A")
		plan.Currency = CurrencyKRW
		plan.Amount = 1_300_000
		res, err := MonteCarlo(plan, MCConfig{Simulations: 20}, MCInput{Series: map[string]*market.Series{"A": flatAsset}, FX: movingFX})
		if err != nil {
			t.Fatalf("MonteCarlo: %v", err)
		}
		// Each contribution buys USD at rate fx_t and is valued at fx_end,
		// so it behaves like a constant-growth asset with the FX drift.
		want := deterministicFinal(plan.Amount*(1-plan.FeeRate), fxLog, 252, 21)
		if !approxRel(res.Percentiles["p50"], want, 1e-9) {
			t.Fatalf("p50 = %v, want %v", res.Percentiles["p50"], want)
		}
	})
}

func TestMonteCarloLookback(t *testing.T) {
	s := randomWalkSeries("A", 3*261, 5, 0.0002, 0.01)
	in := MCInput{Series: map[string]*market.Series{"A": s}}
	last, _ := s.Last()

	full, err := MonteCarlo(basePlan("A"), MCConfig{Simulations: 10}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	if !full.LookbackFrom.Equal(s.Bars[0].Date) || !full.LookbackTo.Equal(last.Date) {
		t.Fatalf("full lookback = %v..%v, want %v..%v", full.LookbackFrom, full.LookbackTo, s.Bars[0].Date, last.Date)
	}

	oneYear, err := MonteCarlo(basePlan("A"), MCConfig{Simulations: 10, LookbackYears: 1}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	earliest, latest := last.Date.AddDate(0, 0, -366), last.Date.AddDate(0, 0, -362)
	if oneYear.LookbackFrom.Before(earliest) || oneYear.LookbackFrom.After(latest) {
		t.Fatalf("1y lookback from = %v, want within %v..%v", oneYear.LookbackFrom, earliest, latest)
	}
	if oneYear.Percentiles["p50"] == full.Percentiles["p50"] {
		t.Fatalf("lookback must change the resampled history")
	}
}

func TestMonteCarloValidation(t *testing.T) {
	ok := growthSeries("A", 300, 0.0003)
	series := map[string]*market.Series{"A": ok, "B": ok}
	tests := []struct {
		name    string
		plan    func(p *MCPlan)
		cfg     MCConfig
		in      MCInput
		wantErr error
	}{
		{name: "no symbols", plan: func(p *MCPlan) { p.Symbols = nil }, wantErr: ErrInvalidInput},
		{name: "weights length mismatch", plan: func(p *MCPlan) { p.Weights = []float64{0.5, 0.5} }, wantErr: ErrInvalidInput},
		{name: "weights do not sum to 1", plan: func(p *MCPlan) { p.Weights = []float64{0.9} }, wantErr: ErrInvalidInput},
		{name: "negative weight", plan: func(p *MCPlan) { p.Symbols = []string{"A", "B"}; p.Weights = []float64{1.5, -0.5} }, wantErr: ErrInvalidInput},
		{name: "zero amount", plan: func(p *MCPlan) { p.Amount = 0 }, wantErr: ErrInvalidInput},
		{name: "unknown currency", plan: func(p *MCPlan) { p.Currency = "EUR" }, wantErr: ErrInvalidInput},
		{name: "unknown cadence", plan: func(p *MCPlan) { p.Cadence = "yearly" }, wantErr: ErrInvalidInput},
		{name: "zero horizon", plan: func(p *MCPlan) { p.HorizonYears = 0 }, wantErr: ErrInvalidInput},
		{name: "horizon too long", plan: func(p *MCPlan) { p.HorizonYears = 41 }, wantErr: ErrInvalidInput},
		{name: "horizon rounds to no steps", plan: func(p *MCPlan) { p.HorizonYears = 0.001 }, wantErr: ErrInvalidInput},
		{name: "fee of 100%", plan: func(p *MCPlan) { p.FeeRate = 1 }, wantErr: ErrInvalidInput},
		{name: "negative fee", plan: func(p *MCPlan) { p.FeeRate = -0.1 }, wantErr: ErrInvalidInput},
		{name: "missing symbol", plan: func(p *MCPlan) { p.Symbols = []string{"ZZZ"} }, wantErr: ErrInvalidInput},
		{name: "krw without fx", plan: func(p *MCPlan) { p.Currency = CurrencyKRW }, wantErr: ErrInvalidInput},
		{name: "too many simulations", cfg: MCConfig{Simulations: MaxSimulations + 1}, wantErr: ErrInvalidInput},
		{name: "negative simulations", cfg: MCConfig{Simulations: -1}, wantErr: ErrInvalidInput},
		{name: "negative block length", cfg: MCConfig{BlockLength: -1}, wantErr: ErrInvalidInput},
		{name: "negative lookback", cfg: MCConfig{LookbackYears: -1}, wantErr: ErrInvalidInput},
		{name: "nan override", cfg: MCConfig{ExpectedAnnualReturn: ptr(math.NaN())}, wantErr: ErrInvalidInput},
		{
			name:    "invalid series",
			in:      MCInput{Series: map[string]*market.Series{"A": {Meta: market.Meta{Symbol: "A"}, Bars: []market.Bar{{Date: day(2020, time.January, 2), Close: -1, AdjClose: 1}}}}},
			wantErr: ErrInvalidInput,
		},
		{
			name:    "too little common history",
			in:      MCInput{Series: map[string]*market.Series{"A": growthSeries("A", 10, 0.001)}},
			wantErr: ErrInsufficientHistory,
		},
		{
			name: "no overlap between symbols",
			plan: func(p *MCPlan) { p.Symbols = []string{"A", "B"} },
			in: MCInput{Series: map[string]*market.Series{
				"A": ok,
				"B": seriesFrom("B", weekdays(day(2030, time.January, 1), 300), func(_ int, _ time.Time) float64 { return 1 }),
			}},
			wantErr: ErrInsufficientHistory,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := basePlan("A")
			if tt.plan != nil {
				tt.plan(&plan)
			}
			in := tt.in
			if in.Series == nil {
				in.Series = series
			}
			res, err := MonteCarlo(plan, tt.cfg, in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if res != nil {
				t.Fatalf("result must be nil on error")
			}
		})
	}
}

func ptr(v float64) *float64 { return &v }

func TestMonteCarloDefaults(t *testing.T) {
	in := MCInput{Series: map[string]*market.Series{"A": growthSeries("A", 300, 0.0003)}}
	plan := basePlan("A")
	plan.Currency = "" // defaults to USD
	plan.Weights = nil // defaults to equal weights
	res, err := MonteCarlo(plan, MCConfig{}, in)
	if err != nil {
		t.Fatalf("MonteCarlo: %v", err)
	}
	if res.Simulations != DefaultSimulations || res.BlockLength != DefaultBlockLength || res.Seed != DefaultSeed {
		t.Fatalf("defaults not applied: %+v", res)
	}
	if res.HorizonYears != 1 {
		t.Fatalf("HorizonYears = %v, want 1", res.HorizonYears)
	}
	if !hasReason(res.Assumptions, "not a forecast") {
		t.Fatalf("assumptions must state the result is not a forecast: %v", res.Assumptions)
	}
	if !hasReason(res.Assumptions, "USD") {
		t.Fatalf("assumptions must name the currency: %v", res.Assumptions)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4}
	tests := []struct {
		q    float64
		want float64
	}{
		{0, 1}, {0.25, 1.75}, {0.5, 2.5}, {0.75, 3.25}, {1, 4},
	}
	for _, tt := range tests {
		if got := percentile(sorted, tt.q); !approx(got, tt.want, 1e-12) {
			t.Errorf("percentile(%v, %v) = %v, want %v", sorted, tt.q, got, tt.want)
		}
	}
	if got := percentile([]float64{9}, 0.5); got != 9 {
		t.Errorf("single value percentile = %v, want 9", got)
	}
}

func TestCommonDates(t *testing.T) {
	a := seriesFrom("A", weekdays(day(2020, time.January, 1), 10), func(_ int, _ time.Time) float64 { return 1 })
	b := seriesFrom("B", weekdays(day(2020, time.January, 8), 10), func(_ int, _ time.Time) float64 { return 1 })
	got := commonDates([]*market.Series{a, b})
	want := weekdays(day(2020, time.January, 8), 5)
	if len(got) != len(want) {
		t.Fatalf("commonDates = %v, want %v", got, want)
	}
	for i := range got {
		if !got[i].Equal(want[i]) {
			t.Fatalf("commonDates[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if got := commonDates(nil); len(got) != 0 {
		t.Fatalf("commonDates(nil) = %v, want empty", got)
	}
}

func TestCadenceInterval(t *testing.T) {
	tests := []struct {
		cadence Cadence
		want    int
		wantErr bool
	}{
		{CadenceDaily, 1, false},
		{CadenceWeekly, 5, false},
		{CadenceMonthly, 21, false},
		{"", 0, true},
		{"Daily", 0, true},
	}
	for _, tt := range tests {
		got, err := tt.cadence.interval()
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Cadence(%q).interval() = %d, %v; want %d, err=%v", tt.cadence, got, err, tt.want, tt.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), string(tt.cadence)) {
			t.Errorf("error %v should name the cadence", err)
		}
	}
}
