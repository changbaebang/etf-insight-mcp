package sim

import "testing"

func TestMaxDrawdown(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		want   float64
	}{
		{"spec example", []float64{100, 120, 60, 90, 130, 100}, 0.5},
		{"empty", nil, 0},
		{"single", []float64{100}, 0},
		{"rising", []float64{1, 2, 3, 4}, 0},
		{"flat", []float64{5, 5, 5}, 0},
		{"halves", []float64{100, 50}, 0.5},
		{"earlier fall beats later smaller fall", []float64{100, 50, 200, 150}, 0.5},
		{"later fall beats earlier smaller fall", []float64{100, 90, 200, 50}, 0.75},
		{"non-positive prefix is skipped", []float64{0, 0, 100, 75}, 0.25},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MaxDrawdown(tc.values)
			if !closeTo(got, tc.want, 1e-12) {
				t.Errorf("MaxDrawdown(%v) = %g, want %g", tc.values, got, tc.want)
			}
		})
	}
}
