package analytics

import (
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// Cadence is how often a plan buys. The strings equal package sim's
// cadence values so a tool layer can pass them through unchanged; this
// package deliberately does not import sim.
type Cadence string

// Supported cadences. Each one is approximated by a fixed, possibly
// fractional, number of simulated trading days between contributions, see
// Cadence.period.
const (
	CadenceDaily   Cadence = "daily"
	CadenceWeekly  Cadence = "weekly"
	CadenceMonthly Cadence = "monthly"
)

// period returns the number of simulated trading days between two
// contributions: 1 (daily), 252/52 ≈ 4.85 (weekly, so a simulated year
// holds 52 contributions like a calendar year) or 21 (monthly, 252/12).
// A contribution happens on step t whenever floor(t/period) advances, see
// contributesAt. Calendar effects such as holidays are ignored.
func (c Cadence) period() (float64, error) {
	switch c {
	case CadenceDaily:
		return 1, nil
	case CadenceWeekly:
		return tradingDaysPerYear / 52.0, nil
	case CadenceMonthly:
		return tradingDaysPerYear / 12.0, nil
	default:
		return 0, fmt.Errorf("%w: unknown cadence %q", ErrInvalidInput, string(c))
	}
}

// contributesAt reports whether step t (0-based) is a contribution step
// for the given period: step 0 always is, and so is every step on which
// floor(t/period) is larger than it was on the previous step.
func contributesAt(t int, period float64) bool {
	if t == 0 {
		return true
	}
	return math.Floor(float64(t)/period) > math.Floor(float64(t-1)/period)
}

// contributionCount is the number of contribution steps in steps steps:
// one per distinct value of floor(t/period) for t in [0, steps), computed
// with the same expression as contributesAt so the two always agree.
func contributionCount(steps int, period float64) int {
	if steps <= 0 {
		return 0
	}
	return int(math.Floor(float64(steps-1)/period)) + 1
}

// Monte Carlo defaults and limits.
const (
	// DefaultSimulations is the number of paths when MCConfig.Simulations is 0.
	DefaultSimulations = 2000
	// MaxSimulations caps MCConfig.Simulations.
	MaxSimulations = 20000
	// DefaultBlockLength is the bootstrap block length in trading days when
	// MCConfig.BlockLength is 0.
	DefaultBlockLength = 21
	// DefaultSeed is used when MCConfig.Seed is 0, so results are
	// reproducible unless the caller asks otherwise.
	DefaultSeed uint64 = 42
	// MaxHorizonYears caps MCPlan.HorizonYears.
	MaxHorizonYears = 40
	// MaxLookbackYears caps MCConfig.LookbackYears; larger values would
	// only overflow the date arithmetic.
	MaxLookbackYears = 200
	// MaxBlockLength caps MCConfig.BlockLength (ten trading years).
	MaxBlockLength = 2520
	// weightTolerance is how far the weight sum may stray from 1.
	weightTolerance = 1e-6
)

// Plan currencies.
const (
	CurrencyUSD = "USD"
	CurrencyKRW = "KRW"
)

// MCPlan is the recurring-purchase plan whose outcome distribution
// MonteCarlo projects.
type MCPlan struct {
	// Symbols are the instruments bought; each needs a series in MCInput.
	Symbols []string
	// Weights split every contribution across Symbols and must sum to 1
	// (within 1e-6). Nil means equal weights.
	Weights []float64
	// Amount is the gross contribution per purchase in Currency.
	Amount float64
	// Currency is "USD" (default when empty) or "KRW". With KRW the
	// contribution is converted at a simulated FX rate bootstrapped
	// jointly from MCInput.FX, quoted as KRW per 1 USD.
	Currency string
	// Cadence is how often Amount is invested.
	Cadence Cadence
	// HorizonYears is the plan length, > 0 and <= MaxHorizonYears.
	HorizonYears float64
	// FeeRate is the fraction of each contribution lost to fees, in [0, 1).
	FeeRate float64
}

// MCConfig tunes the simulation. The zero value is valid and selects the
// documented defaults.
type MCConfig struct {
	// Simulations is the number of paths: 0 means DefaultSimulations,
	// more than MaxSimulations is rejected.
	Simulations int
	// BlockLength is the bootstrap block length in trading days: 0 means
	// DefaultBlockLength. Drawing blocks of consecutive days instead of
	// single days keeps autocorrelation and volatility clustering.
	BlockLength int
	// LookbackYears restricts the resampled history to the last N years of
	// the common date range; 0 means all common history.
	LookbackYears float64
	// Seed seeds the random number generators: 0 means DefaultSeed. Every
	// path uses its own generator seeded with (Seed, path index), so the
	// result is bit-for-bit reproducible regardless of scheduling.
	Seed uint64
	// ExpectedAnnualReturn optionally replaces the historical drift: every
	// symbol's resampled daily log returns are shifted by one constant so
	// that the weighted portfolio's mean annual log return equals this
	// value (a fraction, e.g. 0.07). It encodes an external assumption such
	// as a broker's long-run forecast; nil uses history as is. The FX path,
	// if any, is not shifted.
	ExpectedAnnualReturn *float64
}

// MCInput supplies the price history the bootstrap resamples.
type MCInput struct {
	// Series maps each MCPlan.Symbols entry to its history.
	Series map[string]*market.Series
	// FX is the KRW-per-USD history, required when the plan is in KRW
	// and ignored otherwise. Its Close is used.
	FX *market.Series
}

// MCResult is the projected outcome distribution of a plan. Money fields
// are in the plan currency.
type MCResult struct {
	// Simulations, BlockLength and Seed are the effective values after
	// defaults were applied.
	Simulations, BlockLength int
	Seed                     uint64
	// HorizonYears echoes the plan.
	HorizonYears float64
	// Contributions is the number of purchases over the horizon and
	// Invested is Contributions * Amount; both are identical in every path.
	Contributions int
	Invested      float64
	// Percentiles maps "p5", "p10", "p25", "p50", "p75", "p90" and "p95"
	// to that percentile of the final portfolio value.
	Percentiles map[string]float64
	// ReturnPctPercentiles holds the same keys as (final / Invested - 1) * 100.
	ReturnPctPercentiles map[string]float64
	// ProbLoss is the fraction of paths whose final value is below Invested.
	ProbLoss float64
	// MeanFinal is the average final value across paths.
	MeanFinal float64
	// HistoricalAnnualReturn is exp(252 * mean daily log return) - 1 and
	// HistoricalVolatility the annualized standard deviation, both of the
	// weighted portfolio's daily log returns over the lookback, as fractions.
	HistoricalAnnualReturn, HistoricalVolatility float64
	// LookbackFrom and LookbackTo bound the resampled history.
	LookbackFrom, LookbackTo time.Time
	// Assumptions spells out every modelling choice, including the
	// expected-return override when set.
	Assumptions []string
}

// MonteCarlo projects the distribution of final values of plan p by
// resampling history with a circular block bootstrap.
//
// Method: the common date range of all symbols (and FX for a KRW plan) is
// cut to the lookback, and daily log returns are computed per symbol on
// AdjClose (Close for FX). The horizon is round(252 * HorizonYears)
// steps. Each path draws blocks of BlockLength consecutive days with a
// uniformly random start (wrapping around the end of the lookback) until
// it has one day per step, and applies that same day sequence to every
// symbol and to FX, so cross-asset correlation is preserved. On the first
// step and then once per cadence period (see Cadence.period), the contribution net of
// FeeRate is split by weight and buys fractional shares at the simulated
// price, each symbol starting at its last real AdjClose; a KRW
// contribution is first converted at the simulated FX rate. The final
// value is the holdings at the final simulated prices, converted at the
// final simulated FX rate for KRW. Paths run on runtime.NumCPU() workers.
// Percentiles use sorting with linear interpolation.
//
// Errors wrap ErrInvalidInput for bad plan or config values and a missing
// or invalid series, and ErrInsufficientHistory when the common history
// is shorter than BlockLength + 1 days.
func MonteCarlo(p MCPlan, cfg MCConfig, in MCInput) (*MCResult, error) {
	plan, err := normalizePlan(p)
	if err != nil {
		return nil, err
	}
	cfg, err = normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	h, err := buildHistory(plan, cfg, in)
	if err != nil {
		return nil, err
	}
	finals := runPaths(plan, cfg, h)
	return collect(plan, cfg, h, finals), nil
}

// planSpec is MCPlan after validation, with derived step counts.
type planSpec struct {
	symbols       []string
	weights       []float64
	amount        float64
	currency      string
	krw           bool
	cadence       Cadence
	period        float64
	steps         int
	contributions int
	horizonYears  float64
	feeRate       float64
}

// normalizePlan validates p and fills in defaults.
func normalizePlan(p MCPlan) (planSpec, error) {
	if len(p.Symbols) == 0 {
		return planSpec{}, fmt.Errorf("%w: at least one symbol is required", ErrInvalidInput)
	}
	weights, err := normalizeWeights(p.Weights, len(p.Symbols))
	if err != nil {
		return planSpec{}, err
	}
	if !(p.Amount > 0) || math.IsInf(p.Amount, 0) {
		return planSpec{}, fmt.Errorf("%w: amount must be positive, got %v", ErrInvalidInput, p.Amount)
	}
	currency := strings.ToUpper(p.Currency)
	if currency == "" {
		currency = CurrencyUSD
	}
	if currency != CurrencyUSD && currency != CurrencyKRW {
		return planSpec{}, fmt.Errorf("%w: currency must be USD or KRW, got %q", ErrInvalidInput, p.Currency)
	}
	period, err := p.Cadence.period()
	if err != nil {
		return planSpec{}, err
	}
	if !(p.HorizonYears > 0) || p.HorizonYears > MaxHorizonYears {
		return planSpec{}, fmt.Errorf("%w: horizon must be in (0, %d] years, got %v",
			ErrInvalidInput, MaxHorizonYears, p.HorizonYears)
	}
	steps := int(math.Round(tradingDaysPerYear * p.HorizonYears))
	if steps < 1 {
		return planSpec{}, fmt.Errorf("%w: horizon %v years rounds to zero trading days", ErrInvalidInput, p.HorizonYears)
	}
	if !(p.FeeRate >= 0) || p.FeeRate >= 1 {
		return planSpec{}, fmt.Errorf("%w: fee rate must be in [0, 1), got %v", ErrInvalidInput, p.FeeRate)
	}
	return planSpec{
		symbols:       p.Symbols,
		weights:       weights,
		amount:        p.Amount,
		currency:      currency,
		krw:           currency == CurrencyKRW,
		cadence:       p.Cadence,
		period:        period,
		steps:         steps,
		contributions: contributionCount(steps, period),
		horizonYears:  p.HorizonYears,
		feeRate:       p.FeeRate,
	}, nil
}

// normalizeWeights returns equal weights for nil, and otherwise checks
// that weights has n non-negative entries summing to 1.
func normalizeWeights(weights []float64, n int) ([]float64, error) {
	if weights == nil {
		out := make([]float64, n)
		for i := range out {
			out[i] = 1 / float64(n)
		}
		return out, nil
	}
	if len(weights) != n {
		return nil, fmt.Errorf("%w: %d weights for %d symbols", ErrInvalidInput, len(weights), n)
	}
	var sum float64
	for i, w := range weights {
		if !(w >= 0) {
			return nil, fmt.Errorf("%w: weight %d is %v, must be >= 0", ErrInvalidInput, i, w)
		}
		sum += w
	}
	if math.Abs(sum-1) > weightTolerance {
		return nil, fmt.Errorf("%w: weights sum to %v, must sum to 1", ErrInvalidInput, sum)
	}
	return weights, nil
}

// normalizeConfig validates cfg and applies the documented defaults.
func normalizeConfig(cfg MCConfig) (MCConfig, error) {
	switch {
	case cfg.Simulations == 0:
		cfg.Simulations = DefaultSimulations
	case cfg.Simulations < 0:
		return cfg, fmt.Errorf("%w: simulations must be positive, got %d", ErrInvalidInput, cfg.Simulations)
	case cfg.Simulations > MaxSimulations:
		return cfg, fmt.Errorf("%w: simulations %d exceeds the maximum %d", ErrInvalidInput, cfg.Simulations, MaxSimulations)
	}
	switch {
	case cfg.BlockLength == 0:
		cfg.BlockLength = DefaultBlockLength
	case cfg.BlockLength < 0:
		return cfg, fmt.Errorf("%w: block length must be positive, got %d", ErrInvalidInput, cfg.BlockLength)
	case cfg.BlockLength > MaxBlockLength:
		return cfg, fmt.Errorf("%w: block length %d exceeds the maximum %d", ErrInvalidInput, cfg.BlockLength, MaxBlockLength)
	}
	if !(cfg.LookbackYears >= 0) || cfg.LookbackYears > MaxLookbackYears {
		return cfg, fmt.Errorf("%w: lookback years must be in [0, %d], got %v", ErrInvalidInput, MaxLookbackYears, cfg.LookbackYears)
	}
	if cfg.Seed == 0 {
		cfg.Seed = DefaultSeed
	}
	if r := cfg.ExpectedAnnualReturn; r != nil && (math.IsNaN(*r) || math.IsInf(*r, 0)) {
		return cfg, fmt.Errorf("%w: expected annual return must be finite, got %v", ErrInvalidInput, *r)
	}
	return cfg, nil
}

// history is the resampling universe: aligned daily log returns of every
// symbol (and FX) over the lookback, plus the prices every path starts at.
type history struct {
	from, to time.Time
	// returns[i][d] is symbol i's log return on common day d.
	returns [][]float64
	// fxReturns[d] is the FX log return on common day d; nil for USD plans.
	fxReturns []float64
	// startPrices[i] is symbol i's last real AdjClose; startFX the last
	// real FX close (1 for USD plans).
	startPrices []float64
	startFX     float64
	// shift is added to every symbol's daily log return (0 without the
	// expected-return override).
	shift float64
	// meanDaily and stdevDaily describe the plan's daily log return in the
	// plan currency before the shift: the daily-rebalanced basket
	// log(Σ wᵢ·exp(rᵢ)) plus, for KRW plans, the FX log return.
	meanDaily, stdevDaily float64
	// meanDailyUSD is the basket's mean daily log return without the FX
	// leg; the override shift is defined against it.
	meanDailyUSD float64
}

// buildHistory aligns the input series on their common dates, cuts them to
// the lookback and derives the return matrix and portfolio statistics.
func buildHistory(plan planSpec, cfg MCConfig, in MCInput) (*history, error) {
	series := make([]*market.Series, 0, len(plan.symbols)+1)
	for _, sym := range plan.symbols {
		s, ok := in.Series[sym]
		if !ok || s == nil {
			return nil, fmt.Errorf("%w: no series for symbol %q", ErrInvalidInput, sym)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		series = append(series, s)
	}
	if plan.krw {
		if in.FX == nil {
			return nil, fmt.Errorf("%w: a KRW plan needs the FX series", ErrInvalidInput)
		}
		if err := in.FX.Validate(); err != nil {
			return nil, fmt.Errorf("%w: fx: %w", ErrInvalidInput, err)
		}
		series = append(series, in.FX)
	}

	dates := commonDates(series)
	if cfg.LookbackYears > 0 && len(dates) > 0 {
		cutoff := dates[len(dates)-1].AddDate(0, 0, -int(math.Round(cfg.LookbackYears*daysPerYear)))
		dates = dates[sort.Search(len(dates), func(i int) bool { return !dates[i].Before(cutoff) }):]
	}
	if cfg.BlockLength > len(dates)-1 {
		return nil, fmt.Errorf("%w: %d common trading days, need at least %d (block length + 1)",
			ErrInsufficientHistory, len(dates), cfg.BlockLength+1)
	}

	h := &history{
		from:        dates[0],
		to:          dates[len(dates)-1],
		returns:     make([][]float64, len(plan.symbols)),
		startPrices: make([]float64, len(plan.symbols)),
		startFX:     1,
	}
	for i := range plan.symbols {
		h.returns[i] = LogReturns(pricesOn(series[i], dates, adjClose))
		last, _ := series[i].Last()
		h.startPrices[i] = last.AdjClose
	}
	if plan.krw {
		h.fxReturns = LogReturns(pricesOn(in.FX, dates, rawClose))
		last, _ := in.FX.Last()
		h.startFX = last.Close
	}

	// The basket's daily log return is log(Σ wᵢ·exp(rᵢ)): what a portfolio
	// rebalanced to the weights every day actually earns. The weighted sum
	// Σ wᵢ·rᵢ of log returns understates it by about half the
	// diversification variance, which would bias the historical figures
	// and the override. Because log(Σ wᵢ·exp(rᵢ+s)) = s + log(Σ wᵢ·exp(rᵢ)),
	// one additive shift s moves the basket's mean exactly.
	basket := make([]float64, len(dates)-1)
	portfolio := make([]float64, len(dates)-1)
	for d := range basket {
		var growth float64
		for i, w := range plan.weights {
			growth += w * math.Exp(h.returns[i][d])
		}
		basket[d] = math.Log(growth)
		portfolio[d] = basket[d]
		if h.fxReturns != nil {
			portfolio[d] += h.fxReturns[d]
		}
	}
	h.meanDailyUSD = mean(basket)
	h.meanDaily = mean(portfolio)
	h.stdevDaily = sampleStdev(portfolio)
	if cfg.ExpectedAnnualReturn != nil {
		h.shift = (*cfg.ExpectedAnnualReturn - tradingDaysPerYear*h.meanDailyUSD) / tradingDaysPerYear
	}
	return h, nil
}

// commonDates returns, sorted ascending, the dates on which every series
// has a bar.
func commonDates(series []*market.Series) []time.Time {
	counts := make(map[time.Time]int)
	for _, s := range series {
		for _, b := range s.Bars {
			counts[b.Date]++
		}
	}
	dates := make([]time.Time, 0, len(counts))
	for d, n := range counts {
		if n == len(series) {
			dates = append(dates, d)
		}
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	return dates
}

// Price selectors for pricesOn.
func adjClose(b market.Bar) float64 { return b.AdjClose }
func rawClose(b market.Bar) float64 { return b.Close }

// pricesOn returns the price of s on each of dates, which must all be
// trading days of s, in order.
func pricesOn(s *market.Series, dates []time.Time, price func(market.Bar) float64) []float64 {
	byDate := make(map[time.Time]float64, s.Len())
	for _, b := range s.Bars {
		byDate[b.Date] = price(b)
	}
	out := make([]float64, len(dates))
	for i, d := range dates {
		out[i] = byDate[d]
	}
	return out
}

// runPaths simulates every path on a worker pool and returns the final
// value of each, indexed by path.
func runPaths(plan planSpec, cfg MCConfig, h *history) []float64 {
	finals := make([]float64, cfg.Simulations)
	jobs := make(chan int)
	workers := min(runtime.NumCPU(), cfg.Simulations)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := newPathRunner(plan, cfg, h)
			for i := range jobs {
				finals[i] = r.run(cfg.Seed, uint64(i))
			}
		}()
	}
	for i := range cfg.Simulations {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return finals
}

// pathRunner holds one worker's scratch state so that paths allocate
// nothing per run.
type pathRunner struct {
	plan   planSpec
	block  int
	h      *history
	prices []float64
	shares []float64
}

func newPathRunner(plan planSpec, cfg MCConfig, h *history) *pathRunner {
	return &pathRunner{
		plan:   plan,
		block:  cfg.BlockLength,
		h:      h,
		prices: make([]float64, len(h.startPrices)),
		shares: make([]float64, len(h.startPrices)),
	}
}

// run simulates one path with its own generator seeded by (seed, index)
// and returns the final value in the plan currency.
func (r *pathRunner) run(seed, index uint64) float64 {
	rng := rand.New(rand.NewPCG(seed, index))
	copy(r.prices, r.h.startPrices)
	clear(r.shares)
	fx := r.h.startFX
	days := len(r.h.returns[0])

	blockStart, blockPos := 0, r.block // forces a draw on the first step
	for t := range r.plan.steps {
		if contributesAt(t, r.plan.period) {
			r.contribute(fx)
		}
		if blockPos == r.block {
			blockStart, blockPos = rng.IntN(days), 0
		}
		day := (blockStart + blockPos) % days
		blockPos++
		for i := range r.prices {
			r.prices[i] *= math.Exp(r.h.returns[i][day] + r.h.shift)
		}
		if r.h.fxReturns != nil {
			fx *= math.Exp(r.h.fxReturns[day])
		}
	}

	var usd float64
	for i, sh := range r.shares {
		usd += sh * r.prices[i]
	}
	if r.plan.krw {
		return usd * fx
	}
	return usd
}

// contribute buys fractional shares with one net contribution at the
// current simulated prices; fx is the current KRW-per-USD rate.
func (r *pathRunner) contribute(fx float64) {
	net := r.plan.amount * (1 - r.plan.feeRate)
	if r.plan.krw {
		net /= fx
	}
	for i := range r.shares {
		r.shares[i] += net * r.plan.weights[i] / r.prices[i]
	}
}

// percentileLevels are the reported percentiles.
var percentileLevels = []struct {
	key string
	q   float64
}{
	{"p5", 0.05}, {"p10", 0.10}, {"p25", 0.25}, {"p50", 0.50},
	{"p75", 0.75}, {"p90", 0.90}, {"p95", 0.95},
}

// collect turns the per-path finals into an MCResult.
func collect(plan planSpec, cfg MCConfig, h *history, finals []float64) *MCResult {
	invested := float64(plan.contributions) * plan.amount
	sorted := append([]float64(nil), finals...)
	sort.Float64s(sorted)

	res := &MCResult{
		Simulations:            cfg.Simulations,
		BlockLength:            cfg.BlockLength,
		Seed:                   cfg.Seed,
		HorizonYears:           plan.horizonYears,
		Contributions:          plan.contributions,
		Invested:               invested,
		Percentiles:            make(map[string]float64, len(percentileLevels)),
		ReturnPctPercentiles:   make(map[string]float64, len(percentileLevels)),
		MeanFinal:              mean(finals),
		HistoricalAnnualReturn: math.Exp(tradingDaysPerYear*h.meanDaily) - 1,
		HistoricalVolatility:   h.stdevDaily * math.Sqrt(tradingDaysPerYear),
		LookbackFrom:           h.from,
		LookbackTo:             h.to,
	}
	for _, lvl := range percentileLevels {
		v := percentile(sorted, lvl.q)
		res.Percentiles[lvl.key] = v
		res.ReturnPctPercentiles[lvl.key] = (v/invested - 1) * 100
	}
	losses := 0
	for _, v := range finals {
		if v < invested {
			losses++
		}
	}
	res.ProbLoss = float64(losses) / float64(len(finals))
	res.Assumptions = assumptions(plan, cfg, h)
	return res
}

// percentile returns the q-th quantile (0 <= q <= 1) of sorted values with
// linear interpolation between the two nearest ranks.
func percentile(sorted []float64, q float64) float64 {
	rank := q * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (rank-float64(lo))*(sorted[hi]-sorted[lo])
}

// assumptions spells out the modelling choices behind a result.
func assumptions(plan planSpec, cfg MCConfig, h *history) []string {
	days := len(h.returns[0]) + 1
	out := []string{
		"The result is the distribution of outcomes the plan would have had under resampled history; it is not a forecast of prices.",
		fmt.Sprintf("Daily log returns of each symbol's adjusted close from %s to %s (%d trading days) are resampled with a circular block bootstrap: blocks of %d consecutive days with random starts, wrapping at the end; %d paths, seed %d.",
			h.from.Format(market.DateLayout), h.to.Format(market.DateLayout), days, cfg.BlockLength, cfg.Simulations, cfg.Seed),
		"The same day sequence drives every symbol (and the FX rate), so historical cross-asset correlation and volatility clustering are kept; nothing that did not happen in the lookback can happen in a path.",
		fmt.Sprintf("Horizon %.2f years = %d trading days at 252 per year; %s contributions are approximated as one every %.3g trading days (holidays and month lengths ignored), placed at the start of the period, %d in total, the first one today at the last adjusted close.",
			plan.horizonYears, plan.steps, plan.cadence, plan.period, plan.contributions),
		fmt.Sprintf("Each contribution of %.2f %s loses %.2f%% to fees, is split by weight and buys fractional shares; dividends are reinvested through the adjusted close; no taxes, spreads or rebalancing.",
			plan.amount, plan.currency, plan.feeRate*100),
	}
	out = append(out, fmt.Sprintf("Historical return and volatility describe the plan's daily log return in %s: the daily-rebalanced basket log(sum of weight × exp(return))%s.",
		plan.currency, fxStatsNote(plan.krw)))
	if plan.krw {
		out = append(out, "KRW contributions are converted to USD at that day's simulated rate (KRW per USD) and the final USD value is converted back at the final simulated rate; the FX path is resampled jointly from the FX series' closes and is not shifted by the expected-return override.")
	}
	if cfg.ExpectedAnnualReturn != nil {
		out = append(out, fmt.Sprintf("Expected annual return override: every resampled daily log return is shifted by %+.6f so the basket's compound annual growth in USD is %.2f%% (mean annual log return %.4f) instead of the historical %.2f%%.",
			h.shift, (math.Exp(*cfg.ExpectedAnnualReturn)-1)*100, *cfg.ExpectedAnnualReturn,
			(math.Exp(tradingDaysPerYear*h.meanDailyUSD)-1)*100))
	}
	return out
}

// fxStatsNote completes the historical-statistics assumption for KRW plans.
func fxStatsNote(krw bool) string {
	if krw {
		return " plus the daily KRW/USD log return, so exchange-rate moves are included"
	}
	return ""
}
