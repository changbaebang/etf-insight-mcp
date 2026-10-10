package analytics

import (
	"fmt"
	"math"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// Default periods of the technical indicators. Every indicator function
// substitutes its default when the caller passes a non-positive period.
const (
	DefaultRSIPeriod       = 14
	DefaultMACDFast        = 12
	DefaultMACDSlow        = 26
	DefaultMACDSignal      = 9
	DefaultBollingerPeriod = 20
	DefaultBollingerK      = 2.0
	DefaultATRPeriod       = 14
)

// Thresholds and windows behind Technicals.Signals.
const (
	rsiOverbought   = 70 // RSI above this reads "overbought"
	rsiOversold     = 30 // RSI below this reads "oversold"
	recentCrossBars = 10 // a crossover on one of the last 10 bars (0 to 9 bars ago) is reported
	sma20Bars       = 20
)

// defaultPeriod returns n, or def when n is not positive.
func defaultPeriod(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// wilder smooths values with Wilder's method: the output at index n is the
// mean of values[1..n] and every later output is (previous*(n-1) + v)/n.
// The first n outputs are NaN. values[0] is never read because the first
// bar has no previous close to difference against; callers leave it 0.
func wilder(values []float64, n int) []float64 {
	out := nanSlice(len(values))
	if len(values) <= n {
		return out
	}
	var avg float64
	for i := 1; i < len(values); i++ {
		switch {
		case i < n:
			avg += values[i]
		case i == n:
			avg = (avg + values[i]) / float64(n)
		default:
			avg = (avg*float64(n-1) + values[i]) / float64(n)
		}
		if i >= n {
			out[i] = avg
		}
	}
	return out
}

// RSI returns Wilder's relative strength index of closes over n periods
// (DefaultRSIPeriod when n <= 0). Average gain and loss are seeded with the
// simple mean of the first n changes and then smoothed as
// (previous*(n-1) + change)/n, which is the classic StockCharts recipe.
// The result has the same length as closes; the first n positions, which
// need more changes than exist, hold NaN. A window with no losses reads
// 100 and a window with no gains reads 0; a window with neither (a flat
// price) reads 50. Values are expected oldest first.
func RSI(closes []float64, n int) []float64 {
	n = defaultPeriod(n, DefaultRSIPeriod)
	gains := make([]float64, len(closes))
	losses := make([]float64, len(closes))
	for i := 1; i < len(closes); i++ {
		if change := closes[i] - closes[i-1]; change > 0 {
			gains[i] = change
		} else {
			losses[i] = -change
		}
	}
	avgGain, avgLoss := wilder(gains, n), wilder(losses, n)
	out := nanSlice(len(closes))
	for i := n; i < len(closes); i++ {
		out[i] = rsiValue(avgGain[i], avgLoss[i])
	}
	return out
}

// rsiValue maps smoothed average gain and loss to the 0..100 index.
func rsiValue(avgGain, avgLoss float64) float64 {
	switch {
	case avgGain == 0 && avgLoss == 0:
		return 50
	case avgLoss == 0:
		return 100
	default:
		return 100 - 100/(1+avgGain/avgLoss)
	}
}

// MACD returns the moving average convergence/divergence of closes:
// macd = EMA(fast) - EMA(slow), signalLine = EMA(signal) of macd and
// histogram = macd - signalLine. Non-positive periods take the defaults
// 12, 26 and 9. Every slice has the length of closes; macd is NaN for the
// first slow-1 positions and the signal line and histogram for the first
// slow+signal-2, because the signal EMA starts where macd becomes defined
// (see EMA for the seeding rule). Values are expected oldest first.
func MACD(closes []float64, fast, slow, signal int) (macd, signalLine, histogram []float64) {
	fast = defaultPeriod(fast, DefaultMACDFast)
	slow = defaultPeriod(slow, DefaultMACDSlow)
	signal = defaultPeriod(signal, DefaultMACDSignal)

	macd = diff(EMA(closes, fast), EMA(closes, slow))
	signalLine = emaFromFirstDefined(macd, signal)
	histogram = diff(macd, signalLine)
	return macd, signalLine, histogram
}

// diff returns a[i] - b[i] for every i; NaN propagates.
func diff(a, b []float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out
}

// emaFromFirstDefined returns EMA(values, n) computed over the suffix that
// starts at the first non-NaN element, so a leading NaN mask is skipped
// instead of poisoning the recursion. Positions before the suffix, and the
// first n-1 of the suffix, hold NaN.
func emaFromFirstDefined(values []float64, n int) []float64 {
	out := nanSlice(len(values))
	for start, v := range values {
		if !math.IsNaN(v) {
			copy(out[start:], EMA(values[start:], n))
			break
		}
	}
	return out
}

// Bollinger returns the Bollinger bands of closes: middle = SMA(n) and
// upper/lower = middle ± k times the population standard deviation of the
// same n values (the convention of the original indicator). A non-positive
// n means DefaultBollingerPeriod and a non-positive k means
// DefaultBollingerK. Every slice has the length of closes with NaN in the
// first n-1 positions. Values are expected oldest first.
func Bollinger(closes []float64, n int, k float64) (middle, upper, lower []float64) {
	n = defaultPeriod(n, DefaultBollingerPeriod)
	if k <= 0 {
		k = DefaultBollingerK
	}
	middle = SMA(closes, n)
	upper, lower = nanSlice(len(closes)), nanSlice(len(closes))
	for i := n - 1; i < len(closes); i++ {
		sd := populationStdev(closes[i-n+1:i+1], middle[i])
		upper[i] = middle[i] + k*sd
		lower[i] = middle[i] - k*sd
	}
	return middle, upper, lower
}

// populationStdev returns the population (divide by n) standard deviation
// of values around the given mean.
func populationStdev(values []float64, mean float64) float64 {
	var ss float64
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(values)))
}

// ATR returns Wilder's average true range of bars over n periods
// (DefaultATRPeriod when n <= 0). The true range of a bar is the largest
// of High-Low, |High-previous Close| and |Low-previous Close|; a bar whose
// High or Low is 0 (older cache files carry only closes) uses its Close in
// place of the missing field, so the range degrades to the close-to-close
// move. The first bar has no previous close, so the first n outputs are
// NaN; the output at n is the mean of the first n true ranges and later
// ones are Wilder-smoothed like RSI. Bars are expected oldest first.
func ATR(bars []market.Bar, n int) []float64 {
	n = defaultPeriod(n, DefaultATRPeriod)
	ranges := make([]float64, len(bars))
	for i := 1; i < len(bars); i++ {
		ranges[i] = trueRange(bars[i], bars[i-1].Close)
	}
	return wilder(ranges, n)
}

// trueRange returns the true range of b given the previous bar's close.
func trueRange(b market.Bar, prevClose float64) float64 {
	high, low := b.High, b.Low
	if high == 0 {
		high = b.Close
	}
	if low == 0 {
		low = b.Close
	}
	return math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
}

// Technicals is a snapshot of the common technical indicators of one
// series as of a date. Price-level indicators (moving averages, Bollinger
// bands, ATR, RSI, MACD) are computed on Close, not AdjClose, because that
// is how charts draw them. An indicator whose history is too short is
// reported as 0 and named in Signals.
type Technicals struct {
	// AsOf is the date of the bar used: the last bar on or before the
	// requested date.
	AsOf time.Time
	// Close is the raw close on AsOf.
	Close float64
	// RSI14 is the 14-period Wilder RSI, 0..100.
	RSI14 float64
	// MACD, MACDSignal and MACDHist are the 12/26/9 MACD line, its signal
	// line and their difference, in price units.
	MACD, MACDSignal, MACDHist float64
	// BollingerMiddle, BollingerUpper and BollingerLower are the 20-period,
	// 2-sigma bands. BollingerPctB is (Close - lower) / (upper - lower):
	// 0 at the lower band, 1 at the upper band, outside [0, 1] beyond them,
	// and 0.5 when the bands collapse onto the middle band because the
	// last 20 closes are identical (the close then sits on the middle).
	BollingerMiddle, BollingerUpper, BollingerLower, BollingerPctB float64
	// ATR14 is the 14-period average true range in price units.
	ATR14 float64
	// SMA20, SMA50 and SMA200 are simple moving averages of Close.
	SMA20, SMA50, SMA200 float64
	// EMA12 and EMA26 are the exponential averages behind MACD.
	EMA12, EMA26 float64
	// Signals lists, in plain English, the rules that fire on AsOf: RSI
	// beyond 70/30, MACD above or below its signal line (or the crossover
	// when it happened within the last 10 bars), the close outside the
	// Bollinger bands, the close relative to the 200-day average and a
	// 50/200-day crossover within the last 10 bars. It also carries one
	// note per indicator that lacked history.
	Signals []string
}

// techSeries holds the full indicator slices up to the anchor bar, which
// the signal rules read at and before the anchor index.
type techSeries struct {
	idx    int
	bars   []market.Bar
	rsi    []float64
	macd   []float64
	signal []float64
	hist   []float64
	middle []float64
	upper  []float64
	lower  []float64
	atr    []float64
	sma20  []float64
	sma50  []float64
	sma200 []float64
	ema12  []float64
	ema26  []float64
}

// ComputeTechnicals computes the Technicals of s as of asOf, using the
// last bar on or before asOf. A zero asOf means the last bar. Errors
// mirror Summarize: ErrInvalidInput for an invalid series and
// ErrInsufficientHistory when no bar exists on or before asOf. A short
// series is not an error: indicators without enough bars are 0 and
// explained in Signals.
func ComputeTechnicals(s *market.Series, asOf time.Time) (Technicals, error) {
	idx, err := anchorIndex(s, asOf)
	if err != nil {
		return Technicals{}, err
	}
	ts := newTechSeries(s.Bars[:idx+1])
	last := ts.bars[idx]

	t := Technicals{
		AsOf:            last.Date,
		Close:           last.Close,
		RSI14:           orZero(ts.rsi[idx]),
		MACD:            orZero(ts.macd[idx]),
		MACDSignal:      orZero(ts.signal[idx]),
		MACDHist:        orZero(ts.hist[idx]),
		BollingerMiddle: orZero(ts.middle[idx]),
		BollingerUpper:  orZero(ts.upper[idx]),
		BollingerLower:  orZero(ts.lower[idx]),
		BollingerPctB:   orZero(percentB(last.Close, ts.lower[idx], ts.upper[idx])),
		ATR14:           orZero(ts.atr[idx]),
		SMA20:           orZero(ts.sma20[idx]),
		SMA50:           orZero(ts.sma50[idx]),
		SMA200:          orZero(ts.sma200[idx]),
		EMA12:           orZero(ts.ema12[idx]),
		EMA26:           orZero(ts.ema26[idx]),
	}
	t.Signals = ts.signals()
	return t, nil
}

// percentB returns where price sits between the Bollinger bands as a
// fraction, 0 at lower and 1 at upper. Bands that coincide (up to
// rounding) mean every close in the window was the same, so the price is
// on the middle band: 0.5. NaN bands give NaN.
func percentB(price, lower, upper float64) float64 {
	if upper-lower <= 1e-12*math.Abs(upper) {
		return 0.5
	}
	return (price - lower) / (upper - lower)
}

// newTechSeries computes every indicator over bars, whose last element is
// the anchor.
func newTechSeries(bars []market.Bar) *techSeries {
	cl := closes(bars)
	ts := &techSeries{idx: len(bars) - 1, bars: bars}
	ts.rsi = RSI(cl, DefaultRSIPeriod)
	ts.macd, ts.signal, ts.hist = MACD(cl, DefaultMACDFast, DefaultMACDSlow, DefaultMACDSignal)
	ts.middle, ts.upper, ts.lower = Bollinger(cl, DefaultBollingerPeriod, DefaultBollingerK)
	ts.atr = ATR(bars, DefaultATRPeriod)
	ts.sma20 = SMA(cl, sma20Bars)
	ts.sma50 = SMA(cl, shortSMABars)
	ts.sma200 = SMA(cl, longSMABars)
	ts.ema12 = EMA(cl, DefaultMACDFast)
	ts.ema26 = EMA(cl, DefaultMACDSlow)
	return ts
}

// signals applies the rules documented on Technicals.Signals.
func (ts *techSeries) signals() []string {
	var out []string
	out = append(out, ts.rsiSignals()...)
	out = append(out, ts.macdSignals()...)
	out = append(out, ts.bollingerSignals()...)
	out = append(out, ts.averageSignals()...)
	out = append(out, ts.historyNotes()...)
	return out
}

// rsiSignals reports an overbought or oversold RSI.
func (ts *techSeries) rsiSignals() []string {
	rsi := ts.rsi[ts.idx]
	switch {
	// One decimal, so 70.2 does not print as the threshold 70 itself.
	case rsi > rsiOverbought:
		return []string{fmt.Sprintf("RSI %.1f overbought", rsi)}
	case rsi < rsiOversold: // false for NaN
		return []string{fmt.Sprintf("RSI %.1f oversold", rsi)}
	default:
		return nil
	}
}

// macdSignals reports a recent MACD/signal crossover, or otherwise which
// side of the signal line MACD sits on.
func (ts *techSeries) macdSignals() []string {
	return crossSignals(ts.hist, ts.idx, "MACD", "signal line")
}

// bollingerSignals reports a close outside the bands.
func (ts *techSeries) bollingerSignals() []string {
	closePrice := ts.bars[ts.idx].Close
	switch {
	case closePrice > ts.upper[ts.idx]:
		return []string{"close above upper Bollinger band"}
	case closePrice < ts.lower[ts.idx]:
		return []string{"close below lower Bollinger band"}
	default:
		return nil
	}
}

// averageSignals reports the close against the 200-day average and a
// recent 50/200-day crossover.
func (ts *techSeries) averageSignals() []string {
	closePrice, sma200 := ts.bars[ts.idx].Close, ts.sma200[ts.idx]
	var out []string
	switch {
	case closePrice > sma200:
		out = append(out, "close above 200-day average")
	case closePrice < sma200:
		out = append(out, "close below 200-day average")
	}
	if barsAgo, up, ok := recentCross(diff(ts.sma50, ts.sma200), ts.idx, recentCrossBars); ok {
		out = append(out, fmt.Sprintf("50-day average crossed %s 200-day average %s", aboveBelow(up), barsAgoText(barsAgo)))
	}
	return out
}

// crossSignals words the sign of series[idx] and any sign change within
// the last recentCrossBars bars: "<name> crossed above <other> N bars ago"
// when there was one, else "<name> above <other>" or "<name> below <other>".
func crossSignals(series []float64, idx int, name, other string) []string {
	if barsAgo, up, ok := recentCross(series, idx, recentCrossBars); ok {
		return []string{fmt.Sprintf("%s crossed %s %s %s", name, aboveBelow(up), other, barsAgoText(barsAgo))}
	}
	switch v := series[idx]; {
	case v > 0:
		return []string{fmt.Sprintf("%s above %s", name, other)}
	case v < 0:
		return []string{fmt.Sprintf("%s below %s", name, other)}
	default:
		return nil
	}
}

// recentCross finds the most recent sign change of series on one of the
// last lookback bars up to idx: a change at bar k compares series[k] with
// series[k-1], both defined, and is found when idx - k < lookback. It
// returns how many bars before idx the change happened, whether the
// series went up through zero, and false when no change is found.
func recentCross(series []float64, idx, lookback int) (barsAgo int, up, ok bool) {
	for k := idx; k >= 1 && k > idx-lookback; k-- {
		cur, prev := series[k], series[k-1]
		if math.IsNaN(cur) || math.IsNaN(prev) {
			return 0, false, false
		}
		switch {
		case cur > 0 && prev <= 0:
			return idx - k, true, true
		case cur < 0 && prev >= 0:
			return idx - k, false, true
		}
	}
	return 0, false, false
}

// aboveBelow words the direction of a crossover.
func aboveBelow(up bool) string {
	if up {
		return "above"
	}
	return "below"
}

// barsAgoText words a bar count relative to the anchor.
func barsAgoText(n int) string {
	switch n {
	case 0:
		return "today"
	case 1:
		return "1 bar ago"
	default:
		return fmt.Sprintf("%d bars ago", n)
	}
}

// historyNotes names every indicator that is NaN on the anchor because the
// series is too short, with the bar count it needs.
func (ts *techSeries) historyNotes() []string {
	bars := ts.idx + 1
	checks := []struct {
		name  string
		value float64
		need  int
	}{
		{"RSI", ts.rsi[ts.idx], DefaultRSIPeriod + 1},
		{"MACD line", ts.macd[ts.idx], DefaultMACDSlow},
		{"MACD signal line", ts.signal[ts.idx], DefaultMACDSlow + DefaultMACDSignal - 1},
		{"12-day exponential average", ts.ema12[ts.idx], DefaultMACDFast},
		{"26-day exponential average", ts.ema26[ts.idx], DefaultMACDSlow},
		{"Bollinger band", ts.middle[ts.idx], DefaultBollingerPeriod},
		{"ATR", ts.atr[ts.idx], DefaultATRPeriod + 1},
		{"20-day average", ts.sma20[ts.idx], sma20Bars},
		{"50-day average", ts.sma50[ts.idx], shortSMABars},
		{"200-day average", ts.sma200[ts.idx], longSMABars},
	}
	var out []string
	for _, c := range checks {
		if math.IsNaN(c.value) {
			out = append(out, fmt.Sprintf("%s needs %d bars, only %d available; reported as 0", c.name, c.need, bars))
		}
	}
	return out
}
