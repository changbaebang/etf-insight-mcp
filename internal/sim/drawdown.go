package sim

// MaxDrawdown returns the largest peak-to-trough fall in values as a
// positive fraction of the peak: 0.339 means the series at some point
// stood 33.9% below its earlier high. Values are expected to be positive
// (prices or NAVs); an empty series or one that never falls returns 0.
func MaxDrawdown(values []float64) float64 {
	var peak, worst float64
	for _, v := range values {
		if v > peak {
			peak = v
		}
		if peak <= 0 {
			continue
		}
		if dd := (peak - v) / peak; dd > worst {
			worst = dd
		}
	}
	return worst
}
