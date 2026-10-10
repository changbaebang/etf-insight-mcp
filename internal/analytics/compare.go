package analytics

import (
	"fmt"
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// CommonRange aligns several series on the trading days they all share
// within [start, end] (a zero start or end is unbounded on that side). It
// returns those dates ascending and, for each input series in order, its
// AdjClose on each date, so aligned[i][d] is series i on dates[d]. A nil
// series has no dates, which empties the intersection. Series are assumed
// to satisfy market.Series.Validate (no duplicate dates). An empty input
// returns nil, nil.
func CommonRange(series []*market.Series, start, end time.Time) (dates []time.Time, aligned [][]float64) {
	if len(series) == 0 {
		return nil, nil
	}
	subs := make([]*market.Series, len(series))
	for i, s := range series {
		subs[i] = &market.Series{}
		if s != nil {
			subs[i].Bars = s.Between(start, end)
		}
	}
	dates = commonDates(subs)
	aligned = make([][]float64, len(subs))
	for i, s := range subs {
		aligned[i] = pricesOn(s, dates, adjClose)
	}
	return dates, aligned
}

// CorrelationMatrix returns the Pearson correlation of the daily log
// returns of every pair of rows in aligned, which must hold prices of
// equal length as produced by CommonRange. The matrix is symmetric with a
// unit diagonal; an off-diagonal entry is NaN when fewer than two returns
// exist or either row has zero variance.
func CorrelationMatrix(aligned [][]float64) [][]float64 {
	returns := make([][]float64, len(aligned))
	for i, prices := range aligned {
		returns[i] = LogReturns(prices)
	}
	out := make([][]float64, len(aligned))
	for i := range out {
		out[i] = make([]float64, len(aligned))
		for j := range out[i] {
			switch {
			case i == j:
				out[i][j] = 1
			case j < i:
				out[i][j] = out[j][i]
			default:
				out[i][j] = pearson(returns[i], returns[j])
			}
		}
	}
	return out
}

// pearson returns the Pearson correlation of x and y, NaN when undefined.
func pearson(x, y []float64) float64 {
	cov, varX, varY := covariance(x, y)
	return cov / math.Sqrt(varX*varY)
}

// covariance returns the sample covariance of x and y together with their
// sample variances, or NaN for all three when the slices differ in length
// or hold fewer than two elements.
func covariance(x, y []float64) (cov, varX, varY float64) {
	n := len(x)
	if n != len(y) || n < 2 {
		nan := math.NaN()
		return nan, nan, nan
	}
	mx, my := mean(x), mean(y)
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		cov += dx * dy
		varX += dx * dx
		varY += dy * dy
	}
	d := float64(n - 1)
	return cov / d, varX / d, varY / d
}

// Beta regresses the daily log returns of asset on those of benchmark,
// both given as aligned prices of equal length: beta is the slope and
// alpha the intercept annualised by 252 (a fraction of annual log return).
// Both are NaN when the slices differ in length, hold fewer than three
// prices (two returns) or the benchmark has zero variance.
func Beta(asset, benchmark []float64) (beta, alpha float64) {
	ra, rb := LogReturns(asset), LogReturns(benchmark)
	cov, _, varB := covariance(ra, rb)
	if varB == 0 {
		return math.NaN(), math.NaN()
	}
	beta = cov / varB
	alpha = (mean(ra) - beta*mean(rb)) * tradingDaysPerYear
	return beta, alpha
}

// Comparison sets several series side by side over their common history.
type Comparison struct {
	// Symbols are the input symbols in input order; every slice below is
	// indexed the same way.
	Symbols []string
	// From and To bound the common trading days used for Correlation,
	// BetaToFirst and AlphaToFirst, and Bars counts them.
	From, To time.Time
	Bars     int
	// Summaries are Summarize(series, To) for each input, so their
	// trailing windows may reach back before From.
	Summaries []Summary
	// Correlation is CorrelationMatrix over the common range with
	// undefined entries reported as 0. With two common days there is a
	// single return, so every off-diagonal entry is undefined and 0.
	Correlation [][]float64
	// BetaToFirst and AlphaToFirst are Beta of each series against the
	// first one (the caller's benchmark), 0 when undefined; the first
	// entries are always 1 and 0, the benchmark measured against itself.
	BetaToFirst  []float64
	AlphaToFirst []float64
}

// Compare aligns series on their common trading days within [start, end]
// (zero means unbounded) and computes summaries as of the last common day,
// the return correlation matrix and each series' beta to the first one,
// which the caller should make the benchmark (SPY). It returns an error
// wrapping ErrInvalidInput when no series is given or one fails
// market.Series.Validate, and one wrapping ErrInsufficientHistory when
// fewer than two common trading days exist.
func Compare(series []*market.Series, start, end time.Time) (*Comparison, error) {
	if len(series) == 0 {
		return nil, fmt.Errorf("%w: at least one series is required", ErrInvalidInput)
	}
	for _, s := range series {
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
	}
	dates, aligned := CommonRange(series, start, end)
	if len(dates) < 2 {
		return nil, fmt.Errorf("%w: %d common trading days%s, need at least 2",
			ErrInsufficientHistory, len(dates), describeRange(start, end))
	}

	c := &Comparison{
		Symbols:      make([]string, len(series)),
		From:         dates[0],
		To:           dates[len(dates)-1],
		Bars:         len(dates),
		Summaries:    make([]Summary, len(series)),
		Correlation:  CorrelationMatrix(aligned),
		BetaToFirst:  make([]float64, len(series)),
		AlphaToFirst: make([]float64, len(series)),
	}
	zeroUndefined(c.Correlation)
	for i, s := range series {
		sum, err := Summarize(s, c.To)
		if err != nil {
			return nil, err
		}
		c.Symbols[i] = s.Meta.Symbol
		c.Summaries[i] = sum
		if i == 0 {
			// The benchmark against itself: 1 and 0 by definition, also
			// on a range too short or too flat to regress.
			c.BetaToFirst[i], c.AlphaToFirst[i] = 1, 0
			continue
		}
		beta, alpha := Beta(aligned[i], aligned[0])
		c.BetaToFirst[i], c.AlphaToFirst[i] = orZero(beta), orZero(alpha)
	}
	return c, nil
}

// zeroUndefined replaces NaN and ±Inf entries of m with 0 in place.
func zeroUndefined(m [][]float64) {
	for _, row := range m {
		for j, v := range row {
			row[j] = orZero(v)
		}
	}
}

// describeRange words a possibly open date range for error messages.
func describeRange(start, end time.Time) string {
	switch {
	case start.IsZero() && end.IsZero():
		return ""
	case start.IsZero():
		return " up to " + end.Format(market.DateLayout)
	case end.IsZero():
		return " from " + start.Format(market.DateLayout)
	default:
		return " between " + start.Format(market.DateLayout) + " and " + end.Format(market.DateLayout)
	}
}
