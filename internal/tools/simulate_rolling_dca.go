package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/sim"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// minRollingYears and maxRollingYears bound duration_years.
	minRollingYears = 0.25
	maxRollingYears = 30
	// maxRollingStep bounds step_months: ten years between window starts.
	maxRollingStep = 120
	// maxRollingWindowsShown caps the windows listed in an output; the
	// statistics always use every window.
	maxRollingWindowsShown = 60
)

type simulateRollingDCAInput struct {
	Symbol        string            `json:"symbol,omitempty" jsonschema:"single ticker to buy, e.g. VOO; give either symbol or allocations, not both"`
	Allocations   []allocationInput `json:"allocations,omitempty" jsonschema:"portfolio as a list of {symbol, weight} with weights summing to 1 or 100; alternative to symbol. Windows then lie inside the history every symbol shares"`
	Amount        float64           `json:"amount" jsonschema:"size of one contribution in currency before fees, e.g. 100 (USD) or 10000 (KRW); must be > 0"`
	Currency      string            `json:"currency,omitempty" jsonschema:"USD or KRW (default USD); with KRW each contribution is converted at that day's KRW=X rate and every money field is reported in KRW"`
	Cadence       string            `json:"cadence,omitempty" jsonschema:"daily (every trading day), weekly or monthly; default daily. Each window's first trading day contributes, later contributions follow the cadence"`
	DurationYears float64           `json:"duration_years" jsonschema:"length of every window in years, a whole number of months from 0.25 (3 months) to 30, e.g. 1, 2.5 or 10"`
	StepMonths    int               `json:"step_months,omitempty" jsonschema:"months between successive window starts, 1 to 120 (default 1: a window starting every month); 12 starts one window a year"`
	costInput
}

// rollingWindowOutput is sim.RollingWindow on the wire.
type rollingWindowOutput struct {
	Start               string   `json:"start"`
	End                 string   `json:"end"`
	Contributions       int      `json:"contributions"`
	Invested            float64  `json:"invested"`
	FinalValue          float64  `json:"final_value"`
	ReturnPct           float64  `json:"return_pct"`
	AnnualizedReturnPct *float64 `json:"annualized_return_pct,omitempty" jsonschema:"money-weighted annual rate (XIRR); omitted for windows shorter than a year and when no rate fits"`
	MaxDrawdownPct      float64  `json:"max_drawdown_pct"`
}

// rollingStatsOutput summarises a sim.RollingResult: the distribution of
// one plan's outcomes over every window of the same length.
type rollingStatsOutput struct {
	WindowsCount          int                 `json:"windows_count" jsonschema:"number of windows the statistics cover"`
	DurationMonths        int                 `json:"duration_months"`
	StepMonths            int                 `json:"step_months" jsonschema:"months between successive window starts"`
	FirstStart            string              `json:"first_start" jsonschema:"start of the earliest window"`
	LastEnd               string              `json:"last_end" jsonschema:"end of the latest window"`
	ReturnPctPercentiles  map[string]float64  `json:"return_pct_percentiles" jsonschema:"p5 to p95 of the windows' simple returns, percent"`
	AnnualizedPercentiles map[string]float64  `json:"annualized_percentiles,omitempty" jsonschema:"p5 to p95 of the windows' money-weighted annualized returns, percent; omitted for windows shorter than a year"`
	ProbLossPct           float64             `json:"prob_loss_pct" jsonschema:"share of windows whose final value ended below the amount invested, percent"`
	Best                  rollingWindowOutput `json:"best" jsonschema:"window with the highest return_pct (the earlier one on a tie)"`
	Worst                 rollingWindowOutput `json:"worst" jsonschema:"window with the lowest return_pct (the earlier one on a tie)"`
}

type simulateRollingDCAOutput struct {
	Allocations   []allocationOutput `json:"allocations"`
	Amount        float64            `json:"amount"`
	Currency      string             `json:"currency"`
	Cadence       string             `json:"cadence"`
	DurationYears float64            `json:"duration_years"`
	rollingStatsOutput
	Windows            []rollingWindowOutput `json:"windows" jsonschema:"the windows in start order, at most the 60 most recent"`
	WindowsDownsampled bool                  `json:"windows_downsampled" jsonschema:"true when windows lists only the 60 most recent of windows_count; every statistic still uses all of them"`
	Notes              []string              `json:"notes,omitempty"`
	Disclaimer         string                `json:"disclaimer"`
}

// rollingNoteNames renames the engine's field names in sim's rolling
// notes to the wire names the model sees.
var rollingNoteNames = strings.NewReplacer(
	"AnnualizedPercentiles", "annualized_percentiles",
	"ProbLoss", "prob_loss_pct",
	"Best/Worst rank by ReturnPct", "best/worst rank by return_pct",
)

func (d Deps) registerSimulateRollingDCA(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "simulate_rolling_dca",
		Title:       "Simulate rolling DCA",
		Description: "Answers \"what if I had started on every month since inception?\": runs the same recurring plan (amount per contribution, cadence, currency, fees, dividends) over every window of duration_years that fits into the symbol's history (a portfolio's common history), one window starting every step_months months from the first day of data, and summarises the outcomes: windows_count, percentiles p5 to p95 of the windows' simple returns (return_pct_percentiles) and money-weighted annualized returns (annualized_percentiles, windows of a year or longer only), prob_loss_pct = share of windows that ended below the amount invested, the best and worst window, and the windows themselves (only the 60 most recent when there are more; the statistics always use all). It is the historical counterpart of forecast_dca: real past paths instead of resampled ones, so it shows how much the start date mattered. Needs history of at least duration_years plus two steps (3 windows). Defaults: currency USD, cadence daily, step_months 1, fee_rate 0, commission_fixed 0, dividends reinvested. Percentages are plain numbers (7.5 = 7.5%). Historical, not a forecast.",
		Annotations: readOnly("Simulate rolling DCA", true),
		InputSchema: inputSchema[simulateRollingDCAInput](schemaTweaks{
			defaults: costDefaults(map[string]any{"currency": "USD", "cadence": "daily", "step_months": 1}),
			minItems: map[string]int{"allocations": 1},
		}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in simulateRollingDCAInput) (*mcp.CallToolResult, simulateRollingDCAOutput, error) {
		out, err := d.simulateRollingDCA(ctx, in)
		return nil, out, err
	})
}

// simulateRollingDCA validates the request, loads the histories and runs
// every window.
func (d Deps) simulateRollingDCA(ctx context.Context, in simulateRollingDCAInput) (simulateRollingDCAOutput, error) {
	allocs, err := resolveAllocations(in.Symbol, in.Allocations)
	if err != nil {
		return simulateRollingDCAOutput{}, err
	}
	currency, err := parseCurrency(in.Currency)
	if err != nil {
		return simulateRollingDCAOutput{}, err
	}
	cadence, err := parseCadence(in.Cadence)
	if err != nil {
		return simulateRollingDCAOutput{}, err
	}
	if err := checkAmount(in.Amount); err != nil {
		return simulateRollingDCAOutput{}, err
	}
	if err := in.validate(in.Amount); err != nil {
		return simulateRollingDCAOutput{}, err
	}
	months, err := wholeMonths("duration_years", in.DurationYears, minRollingYears, maxRollingYears)
	if err != nil {
		return simulateRollingDCAOutput{}, err
	}
	step := in.StepMonths
	if step == 0 {
		step = 1
	}
	if step < 1 || step > maxRollingStep {
		return simulateRollingDCAOutput{}, fmt.Errorf("step_months must be between 1 and %d, got %d", maxRollingStep, in.StepMonths)
	}
	plan := sim.Plan{
		Allocations: allocs,
		Amount:      in.Amount,
		Currency:    currency,
		Cadence:     cadence,
		FeeRate:     in.FeeRate,
		FeeFixed:    in.CommissionFixed,
		Reinvest:    in.reinvest(),
	}
	simIn, _, err := d.loadInput(ctx, plan)
	if err != nil {
		return simulateRollingDCAOutput{}, err
	}
	res, err := sim.RunRolling(plan, simIn, sim.RollingConfig{DurationYears: in.DurationYears, StepMonths: step})
	if err != nil {
		return simulateRollingDCAOutput{}, rollingError(err)
	}
	stats, notes, err := toRollingStats(res, months, step)
	if err != nil {
		return simulateRollingDCAOutput{}, err
	}

	shown := res.Windows
	if len(shown) > maxRollingWindowsShown {
		shown = shown[len(shown)-maxRollingWindowsShown:]
		notes = append(notes, fmt.Sprintf("windows lists the %d most recent of %d windows (downsampled); windows_count, the percentiles, prob_loss_pct, best and worst use all %d", maxRollingWindowsShown, res.Count, res.Count))
	}
	out := simulateRollingDCAOutput{
		Allocations:        toAllocationOutputs(allocs),
		Amount:             in.Amount,
		Currency:           currency,
		Cadence:            string(cadence),
		DurationYears:      in.DurationYears,
		rollingStatsOutput: stats,
		Windows:            make([]rollingWindowOutput, 0, len(shown)),
		WindowsDownsampled: len(shown) < res.Count,
		Notes:              append(notes, d.planStaleWarnings(plan)...),
		Disclaimer:         Disclaimer,
	}
	for _, w := range shown {
		out.Windows = append(out.Windows, toRollingWindow(w, months))
	}
	return out, nil
}

// wholeMonths validates a duration in years that must be a whole number
// of months within [lo, hi] and returns the months.
func wholeMonths(field string, years, lo, hi float64) (int, error) {
	if math.IsNaN(years) || years < lo || years > hi {
		return 0, fmt.Errorf("%s must be between %v and %v, got %v", field, lo, hi, years)
	}
	exact := years * 12
	months := int(math.Round(exact))
	if math.Abs(exact-float64(months)) > 1e-6 {
		return 0, fmt.Errorf("%s %v is not a whole number of months; use a multiple of 1/12 such as 0.25, 0.5, 1, 2.5 or 5", field, years)
	}
	return months, nil
}

// rollingError rewrites a RunRolling error for the model: package
// prefixes go and too short a history says what to change.
func rollingError(err error) error {
	msg := strings.ReplaceAll(err.Error(), "sim: ", "")
	if errors.Is(err, sim.ErrInsufficientHistory) {
		msg += "; shorten duration_years or step_months, or pick a symbol with a longer history"
	}
	return errors.New(msg)
}

// toRollingStats maps the summary of a rolling run to the wire shape and
// returns the engine's notes in wire names. months is the window length:
// annualized figures are dropped below a year, where an annual rate would
// be an extrapolation (the policy simulate_dca applies too).
func toRollingStats(res *sim.RollingResult, months, step int) (rollingStatsOutput, []string, error) {
	values := []float64{res.ProbLoss, res.Best.ReturnPct, res.Worst.ReturnPct}
	values = append(values, mapValues(res.ReturnPctPercentiles)...)
	values = append(values, mapValues(res.AnnualizedPercentiles)...)
	if err := checkFinite("rolling result", values...); err != nil {
		return rollingStatsOutput{}, nil, err
	}
	out := rollingStatsOutput{
		WindowsCount:         res.Count,
		DurationMonths:       months,
		StepMonths:           step,
		ReturnPctPercentiles: roundMap(res.ReturnPctPercentiles),
		ProbLossPct:          pct(res.ProbLoss),
		Best:                 toRollingWindow(res.Best, months),
		Worst:                toRollingWindow(res.Worst, months),
	}
	if n := len(res.Windows); n > 0 {
		out.FirstStart = formatDate(res.Windows[0].Start)
		out.LastEnd = formatDate(res.Windows[n-1].End)
	}
	notes := make([]string, 0, len(res.Notes)+1)
	short := months < 12
	for _, n := range res.Notes {
		if short && strings.Contains(n, "AnnualizedPercentiles") {
			continue // the wire omits annualized figures for short windows anyway
		}
		notes = append(notes, rollingNoteNames.Replace(n))
	}
	switch {
	case short:
		notes = append(notes, "annualized_percentiles omitted: windows shorter than a year would turn a few months into an annual rate")
	case res.AnnualizedPercentiles != nil:
		out.AnnualizedPercentiles = scaleMap(res.AnnualizedPercentiles, 100)
	}
	return out, notes, nil
}

// toRollingWindow maps one window, dropping its annualized return when
// windows are shorter than a year.
func toRollingWindow(w sim.RollingWindow, months int) rollingWindowOutput {
	out := rollingWindowOutput{
		Start:          formatDate(w.Start),
		End:            formatDate(w.End),
		Contributions:  w.Contributions,
		Invested:       round2(w.Invested),
		FinalValue:     round2(w.FinalValue),
		ReturnPct:      round2(w.ReturnPct),
		MaxDrawdownPct: round2(w.MaxDrawdownPct),
	}
	if months >= 12 && w.AnnualizedReturnComputed {
		out.AnnualizedReturnPct = ptr(pct(w.AnnualizedReturn))
	}
	return out
}

// scaleMap multiplies every value by factor and rounds to two decimals:
// fractions in, percentages out.
func scaleMap(m map[string]float64, factor float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = round2(v * factor)
	}
	return out
}
