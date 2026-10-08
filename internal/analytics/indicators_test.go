package analytics

import (
	"math"
	"testing"
)

func TestSMA(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name   string
		values []float64
		n      int
		want   []float64
	}{
		{name: "window of three", values: []float64{1, 2, 3, 4, 5}, n: 3, want: []float64{nan, nan, 2, 3, 4}},
		{name: "window of one is identity", values: []float64{1, 2, 3}, n: 1, want: []float64{1, 2, 3}},
		{name: "window longer than input", values: []float64{1, 2}, n: 3, want: []float64{nan, nan}},
		{name: "non-positive window", values: []float64{1, 2}, n: 0, want: []float64{nan, nan}},
		{name: "empty input", values: nil, n: 3, want: []float64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SMA(tt.values, tt.n)
			if !floatsMatch(got, tt.want, 1e-12) {
				t.Fatalf("SMA(%v, %d) = %v, want %v", tt.values, tt.n, got, tt.want)
			}
		})
	}
}

func TestEMA(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name   string
		values []float64
		n      int
		want   []float64
	}{
		// alpha = 0.5: 1, 1.5, 2.25, 3.125, 4.0625; first n-1 masked.
		{name: "window of three", values: []float64{1, 2, 3, 4, 5}, n: 3, want: []float64{nan, nan, 2.25, 3.125, 4.0625}},
		{name: "window of one is identity", values: []float64{1, 2, 3}, n: 1, want: []float64{1, 2, 3}},
		{name: "window longer than input", values: []float64{1, 2}, n: 3, want: []float64{nan, nan}},
		{name: "non-positive window", values: []float64{1, 2}, n: 0, want: []float64{nan, nan}},
		{name: "empty input", values: nil, n: 3, want: []float64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EMA(tt.values, tt.n)
			if !floatsMatch(got, tt.want, 1e-12) {
				t.Fatalf("EMA(%v, %d) = %v, want %v", tt.values, tt.n, got, tt.want)
			}
		})
	}
}

func TestLogReturns(t *testing.T) {
	ln2 := math.Log(2)
	tests := []struct {
		name   string
		values []float64
		want   []float64
	}{
		{name: "doubling and halving", values: []float64{1, 2, 4, 2}, want: []float64{ln2, ln2, -ln2}},
		{name: "single value", values: []float64{5}, want: []float64{}},
		{name: "empty", values: nil, want: []float64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LogReturns(tt.values)
			if !floatsMatch(got, tt.want, 1e-12) {
				t.Fatalf("LogReturns(%v) = %v, want %v", tt.values, got, tt.want)
			}
		})
	}
}

func TestAnnualizedVolatility(t *testing.T) {
	// Four returns of ±0.01: mean 0, sample variance 4e-4/3.
	alternating := math.Sqrt(4e-4/3) * math.Sqrt(252)
	tests := []struct {
		name    string
		returns []float64
		want    float64
	}{
		{name: "alternating", returns: []float64{0.01, -0.01, 0.01, -0.01}, want: alternating},
		{name: "constant returns have no volatility", returns: []float64{0.002, 0.002, 0.002}, want: 0},
		{name: "single return", returns: []float64{0.01}, want: 0},
		{name: "empty", returns: nil, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnnualizedVolatility(tt.returns); !approx(got, tt.want, 1e-12) {
				t.Fatalf("AnnualizedVolatility(%v) = %v, want %v", tt.returns, got, tt.want)
			}
		})
	}
}

func TestMaxDrawdown(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		want   float64
	}{
		{name: "worst fall is 120 to 60", values: []float64{100, 120, 90, 110, 60, 80}, want: 0.5},
		{name: "monotone rise", values: []float64{1, 2, 3, 4}, want: 0},
		{name: "monotone fall", values: []float64{4, 3, 2, 1}, want: 0.75},
		{name: "single value", values: []float64{7}, want: 0},
		{name: "empty", values: nil, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MaxDrawdown(tt.values); !approx(got, tt.want, 1e-12) {
				t.Fatalf("MaxDrawdown(%v) = %v, want %v", tt.values, got, tt.want)
			}
		})
	}
}
