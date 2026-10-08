package analytics

import "math"

// SMA returns the simple moving average of values over a window of n
// elements. The result has the same length as values; the first n-1
// positions, where fewer than n values are available, hold NaN. Values are
// expected oldest first. A non-positive n yields a slice of NaN.
func SMA(values []float64, n int) []float64 {
	out := nanSlice(len(values))
	if n <= 0 {
		return out
	}
	var sum float64
	for i, v := range values {
		sum += v
		if i >= n {
			sum -= values[i-n]
		}
		if i >= n-1 {
			out[i] = sum / float64(n)
		}
	}
	return out
}

// EMA returns the exponential moving average of values with the standard
// smoothing factor alpha = 2/(n+1).
//
// Choice made here: the recursion is seeded with the first value and runs
// from the second element on (ema[i] = alpha*v[i] + (1-alpha)*ema[i-1]),
// but the first n-1 outputs are reported as NaN so that EMA and SMA become
// defined at the same index. Values are expected oldest first. A
// non-positive n yields a slice of NaN.
func EMA(values []float64, n int) []float64 {
	out := nanSlice(len(values))
	if n <= 0 || len(values) == 0 {
		return out
	}
	alpha := 2 / float64(n+1)
	ema := values[0]
	for i, v := range values {
		if i > 0 {
			ema = alpha*v + (1-alpha)*ema
		}
		if i >= n-1 {
			out[i] = ema
		}
	}
	return out
}

// LogReturns returns ln(values[i] / values[i-1]) for every i >= 1, so the
// result is one element shorter than the input. Values must be positive;
// a non-positive value makes the affected positions NaN or infinite.
// Fewer than two values yield an empty slice.
func LogReturns(values []float64) []float64 {
	if len(values) < 2 {
		return []float64{}
	}
	out := make([]float64, len(values)-1)
	for i := 1; i < len(values); i++ {
		out[i-1] = math.Log(values[i] / values[i-1])
	}
	return out
}

// AnnualizedVolatility returns the sample standard deviation of daily log
// returns scaled by sqrt(252). It returns 0 when fewer than two returns
// are given, because a sample standard deviation is undefined there.
func AnnualizedVolatility(dailyLogReturns []float64) float64 {
	if len(dailyLogReturns) < 2 {
		return 0
	}
	return sampleStdev(dailyLogReturns) * math.Sqrt(tradingDaysPerYear)
}

// MaxDrawdown returns the largest peak-to-trough decline in values as a
// positive fraction of the peak: 0.25 means the series once fell 25% from
// an earlier high. Values are expected oldest first. A series that never
// drops below an earlier value, or an empty one, returns 0.
func MaxDrawdown(values []float64) float64 {
	var peak, worst float64
	for i, v := range values {
		if i == 0 || v > peak {
			peak = v
		}
		if peak <= 0 {
			continue
		}
		if dd := 1 - v/peak; dd > worst {
			worst = dd
		}
	}
	return worst
}
