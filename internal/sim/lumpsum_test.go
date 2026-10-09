package sim

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestOnceCadence(t *testing.T) {
	t.Run("ParseCadence", func(t *testing.T) {
		for _, in := range []string{"once", " ONCE ", "Once"} {
			got, err := ParseCadence(in)
			if err != nil || got != Once {
				t.Errorf("ParseCadence(%q) = %q, %v; want Once", in, got, err)
			}
		}
		if _, err := ParseCadence("twice"); err == nil || !strings.Contains(err.Error(), "or once") {
			t.Errorf("ParseCadence(twice) error = %v, want the cadence list to mention once", err)
		}
	})

	t.Run("contributes exactly once", func(t *testing.T) {
		// 260 weekdays rising 100 → 359 from Wednesday 2023-01-17, a
		// mid-month start that would make Weekly or Monthly add a note.
		spy := newSeries(t, "SPY", "2023-01-17", 260, func(k int) float64 { return 100 + float64(k) })
		res := mustRun(t, singlePlan("SPY", Once, 1000), seriesInput(spy))

		if res.Contributions != 1 {
			t.Errorf("Contributions = %d, want 1", res.Contributions)
		}
		requireDate(t, "Start", res.Start, "2023-01-17")
		requireFloat(t, "Invested", res.Invested, 1000, tight)
		requireFloat(t, "Holdings[0].Shares", res.Holdings[0].Shares, 10, tight)
		requireFloat(t, "FinalValue", res.FinalValue, 10*359, tight)
		requireFloat(t, "MaxDrawdownPct", res.MaxDrawdownPct, 0, tight)
		if !res.AnnualizedReturnComputed || res.AnnualizedReturn <= 0 {
			t.Errorf("AnnualizedReturn = %v computed=%v, want a positive fitted rate for one buy and one valuation", res.AnnualizedReturn, res.AnnualizedReturnComputed)
		}
		if len(res.Notes) != 0 {
			t.Errorf("Notes = %q, want none: a single contribution has no schedule to explain", res.Notes)
		}
	})

	t.Run("calendar helpers", func(t *testing.T) {
		days := []time.Time{date(t, "2024-01-03"), date(t, "2024-01-08"), date(t, "2024-02-01")}
		got := contributionDays(Once, days)
		if len(got) != 3 || !got[0] || got[1] || got[2] {
			t.Errorf("contributionDays(Once) = %v, want only the first day", got)
		}
		if startsNewPeriod(Once, days[0], days[2]) {
			t.Error("startsNewPeriod(Once) = true, want false across a month change")
		}
		if _, ok := midPeriodStartNote(Once, newSeries(t, "X", "2024-01-01", 20, constant(1)), date(t, "2024-01-17")); ok {
			t.Error("midPeriodStartNote(Once) noted a mid-month start")
		}
	})
}

// requireSameTotal checks that both legs invested exactly totalAmount,
// to an absolute 1e-9.
func requireSameTotal(t *testing.T, res *LumpSumResult, totalAmount float64) {
	t.Helper()
	for name, leg := range map[string]*Result{"LumpSum": res.LumpSum, "DCA": res.DCA} {
		if math.Abs(leg.Invested-totalAmount) > 1e-9 {
			t.Errorf("%s.Invested = %.12f, want %v", name, leg.Invested, totalAmount)
		}
	}
	if res.TotalAmount != totalAmount {
		t.Errorf("TotalAmount = %v, want %v", res.TotalAmount, totalAmount)
	}
	if !res.LumpSum.Start.Equal(res.DCA.Start) || !res.LumpSum.End.Equal(res.DCA.End) {
		t.Errorf("legs run on different ranges: lump %s..%s, DCA %s..%s",
			formatDate(res.LumpSum.Start), formatDate(res.LumpSum.End), formatDate(res.DCA.Start), formatDate(res.DCA.End))
	}
}

func TestRunLumpSumVsDCARisingPrice(t *testing.T) {
	// The TestRunLinearPriceMonthly series: 100 → 200 over 101 weekdays,
	// monthly buys at 100, 123, 144, 165 and 187.
	spy := newSeries(t, "SPY", "2024-01-01", 101, func(k int) float64 { return 100 + float64(k) })
	in := seriesInput(spy)
	p := singlePlan("SPY", Monthly, 999) // Amount is ignored
	const total = 5000.0

	res, err := RunLumpSumVsDCA(p, in, total)
	if err != nil {
		t.Fatalf("RunLumpSumVsDCA: %v", err)
	}
	requireSameTotal(t, res, total)
	if res.DCA.Contributions != 5 || res.LumpSum.Contributions != 1 {
		t.Errorf("Contributions: DCA %d, lump %d; want 5 and 1", res.DCA.Contributions, res.LumpSum.Contributions)
	}
	// 5000 buys 50 shares at 100, worth 10000 at 200.
	requireFloat(t, "LumpSum.FinalValue", res.LumpSum.FinalValue, 10000, tight)
	requireFloat(t, "LumpSum.ReturnPct", res.LumpSum.ReturnPct, 100, tight)

	// The DCA leg is exactly Run with Amount 1000.
	direct := mustRun(t, singlePlan("SPY", Monthly, 1000), in)
	requireFloat(t, "DCA.FinalValue", res.DCA.FinalValue, direct.FinalValue, tight)
	requireFloat(t, "DCA.ReturnPct", res.DCA.ReturnPct, direct.ReturnPct, tight)
	requireFloat(t, "DCA.AnnualizedReturn", res.DCA.AnnualizedReturn, direct.AnnualizedReturn, tight)

	// Rising price: the lump sum wins on every metric.
	requireFloat(t, "Diff.FinalValue", res.Diff.FinalValue, 10000-direct.FinalValue, tight)
	requireFloat(t, "Diff.ReturnPct", res.Diff.ReturnPct, 100-direct.ReturnPct, tight)
	requireFloat(t, "Diff.AnnualizedReturn", res.Diff.AnnualizedReturn, res.LumpSum.AnnualizedReturn-direct.AnnualizedReturn, tight)
	if res.Diff.FinalValue <= 0 || res.Diff.ReturnPct <= 0 || res.Diff.AnnualizedReturn <= 0 {
		t.Errorf("Diff = %+v, want every field > 0 on a rising price", res.Diff)
	}
	if len(res.Notes) < 2 || !strings.Contains(res.Notes[0], "5 monthly contributions of 1000.00 USD") {
		t.Errorf("Notes = %q, want the per-contribution amount first", res.Notes)
	}
	if p.Amount != 999 || p.Cadence != Monthly {
		t.Errorf("RunLumpSumVsDCA modified the plan: %+v", p)
	}
}

func TestRunLumpSumVsDCAFallingThenRecovering(t *testing.T) {
	// V shape: 100 → 50 → 100 over 101 weekdays. The lump sum ends where it
	// started; daily buys on the way down and up average in below 100.
	spy := newSeries(t, "SPY", "2024-01-01", 101, func(k int) float64 { return 50 + math.Abs(float64(k-50)) })
	const total = 1010.0

	res, err := RunLumpSumVsDCA(singlePlan("SPY", Daily, 1), seriesInput(spy), total)
	if err != nil {
		t.Fatalf("RunLumpSumVsDCA: %v", err)
	}
	requireSameTotal(t, res, total)
	if res.DCA.Contributions != 101 {
		t.Errorf("DCA.Contributions = %d, want 101", res.DCA.Contributions)
	}
	requireFloat(t, "LumpSum.FinalValue", res.LumpSum.FinalValue, total, tight)
	requireFloat(t, "LumpSum.ReturnPct", res.LumpSum.ReturnPct, 0, tight)
	requireFloat(t, "LumpSum.MaxDrawdownPct", res.LumpSum.MaxDrawdownPct, 50, tight)
	if res.DCA.FinalValue <= total {
		t.Errorf("DCA.FinalValue = %v, want > %v", res.DCA.FinalValue, total)
	}
	if res.Diff.FinalValue >= 0 || res.Diff.ReturnPct >= 0 || res.Diff.AnnualizedReturn >= 0 {
		t.Errorf("Diff = %+v, want every field < 0 when DCA wins", res.Diff)
	}
	if !strings.Contains(res.Notes[0], "101 daily contributions of 10.00 USD") {
		t.Errorf("Notes[0] = %q, want 10.00 per contribution", res.Notes[0])
	}
}

func TestRunLumpSumVsDCAFees(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	p := singlePlan("SPY", Daily, 0)
	p.FeeRate, p.FeeFixed = 0.01, 0.5
	const total = 500.0

	res, err := RunLumpSumVsDCA(p, seriesInput(spy), total)
	if err != nil {
		t.Fatalf("RunLumpSumVsDCA: %v", err)
	}
	requireSameTotal(t, res, total)
	// DCA: 5 × (100 × 1% + 0.50) = 7.50; lump sum: 500 × 1% + 0.50 = 5.50.
	requireFloat(t, "DCA.Fees", res.DCA.Fees, 7.5, tight)
	requireFloat(t, "LumpSum.Fees", res.LumpSum.Fees, 5.5, tight)
	requireFloat(t, "DCA.FinalValue", res.DCA.FinalValue, 492.5, tight)
	requireFloat(t, "LumpSum.FinalValue", res.LumpSum.FinalValue, 494.5, tight)
	requireFloat(t, "Diff.FinalValue", res.Diff.FinalValue, 2, tight)
	found := false
	for _, n := range res.Notes {
		found = found || strings.Contains(n, "paid once by the lump sum (fees 5.50) and 5 times by the DCA leg (fees 7.50)")
	}
	if !found {
		t.Errorf("Notes = %q, want the commission comparison", res.Notes)
	}
}

func TestRunLumpSumVsDCAKRW(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	fx := newSeries(t, "KRW=X", "2024-01-01", 5, constant(1000))
	p := singlePlan("SPY", Daily, 0)
	p.Currency = CurrencyKRW
	in := seriesInput(spy)
	in.FX = fx
	const total = 1_000_000.0

	res, err := RunLumpSumVsDCA(p, in, total)
	if err != nil {
		t.Fatalf("RunLumpSumVsDCA: %v", err)
	}
	requireSameTotal(t, res, total)
	if res.LumpSum.FX == nil || res.DCA.FX == nil {
		t.Fatal("FX summary missing on a KRW leg")
	}
	// Flat price and flat rate: both legs end exactly where they started.
	requireFloat(t, "LumpSum.FinalValue", res.LumpSum.FinalValue, total, tight)
	requireFloat(t, "DCA.FinalValue", res.DCA.FinalValue, total, tight)
	if math.Abs(res.Diff.FinalValue) > 1e-9 {
		t.Errorf("Diff.FinalValue = %v, want 0", res.Diff.FinalValue)
	}
	if !strings.Contains(res.Notes[0], "KRW") {
		t.Errorf("Notes[0] = %q, want the plan currency", res.Notes[0])
	}
}

func TestRunLumpSumVsDCAErrors(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	withCadence := func(c Cadence) Plan { return singlePlan("SPY", c, 0) }
	withFixedFee := func(fee float64) Plan {
		p := singlePlan("SPY", Daily, 0)
		p.FeeFixed = fee
		return p
	}
	tests := []struct {
		name    string
		plan    Plan
		total   float64
		wantErr string
	}{
		{"zero total", withCadence(Daily), 0, "total amount must be > 0"},
		{"negative total", withCadence(Daily), -5, "total amount must be > 0"},
		{"NaN total", withCadence(Daily), math.NaN(), "total amount must be > 0"},
		{"once cadence", withCadence(Once), 100, "needs a recurring cadence"},
		{"bad cadence", withCadence("yearly"), 100, "bad cadence"},
		{"unknown symbol", singlePlan("QQQ", Daily, 0), 100, "no price series for QQQ"},
		// 5 USD over 5 days is 1 USD a day, which a 1 USD commission eats.
		{"fixed fee eats a contribution", withFixedFee(1), 5, "DCA leg: sim: fixed fee 1 leaves nothing"},
		// The probe already fails when the commission exceeds the total.
		{"fixed fee exceeds the total", withFixedFee(1), 0.5, "leaves nothing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := RunLumpSumVsDCA(tc.plan, seriesInput(spy), tc.total)
			if err == nil {
				t.Fatalf("RunLumpSumVsDCA returned %+v, want error containing %q", res, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunLumpSumVsDCASingleDayHasNoAnnualizedDiff(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	p := singlePlan("SPY", Daily, 0)
	p.Start, p.End = date(t, "2024-01-03"), date(t, "2024-01-03")

	res, err := RunLumpSumVsDCA(p, seriesInput(spy), 100)
	if err != nil {
		t.Fatalf("RunLumpSumVsDCA: %v", err)
	}
	requireSameTotal(t, res, 100)
	if res.DCA.AnnualizedReturnComputed || res.LumpSum.AnnualizedReturnComputed {
		t.Fatal("a single-day range must not annualise")
	}
	requireFloat(t, "Diff.AnnualizedReturn", res.Diff.AnnualizedReturn, 0, tight)
	last := res.Notes[len(res.Notes)-1]
	if !strings.Contains(last, "Diff.AnnualizedReturn is 0") {
		t.Errorf("Notes = %q, want the missing annualized difference explained last", res.Notes)
	}
}
