package sim

import (
	"errors"
	"fmt"
)

// LumpSumDiff is the lump-sum leg minus the DCA leg for the headline
// metrics: positive means investing everything at once did better.
type LumpSumDiff struct {
	// FinalValue is the difference in final value, in the plan currency.
	FinalValue float64
	// ReturnPct is the difference in simple return, in percentage points.
	ReturnPct float64
	// AnnualizedReturn is the difference in money-weighted annual rate as a
	// fraction. It is 0 unless both legs have AnnualizedReturnComputed;
	// Notes says when that is why.
	AnnualizedReturn float64
}

// LumpSumResult puts the same total through two plans on the same days.
type LumpSumResult struct {
	// LumpSum is the whole TotalAmount invested on the first contribution
	// day of the range (cadence Once) and valued at its end.
	LumpSum *Result
	// DCA is the plan as given, with Amount set so that its contributions
	// over the range add up to TotalAmount.
	DCA *Result
	// TotalAmount is the gross amount each leg invests, in the plan
	// currency.
	TotalAmount float64
	// Diff is LumpSum minus DCA.
	Diff LumpSumDiff
	// Notes reports the per-contribution amount of the DCA leg, how the
	// fees differ between the legs and anything that could not be
	// computed. Each leg's own Result.Notes are left on that leg.
	Notes []string
}

// RunLumpSumVsDCA answers "should I invest it all now or spread it out?"
// for totalAmount in the plan currency. p.Amount is ignored: the DCA leg
// is p with Amount = totalAmount / (number of contribution days in p's
// range) and the lump-sum leg is p with cadence Once and Amount =
// totalAmount, so both legs invest totalAmount on the same trading
// calendar with the same currency, fees, dividend model and calendar
// symbols; the fixed commission is therefore paid once by the lump sum
// and once per contribution by the DCA leg. p.Cadence must be recurring:
// Once is rejected because the two legs would be the same plan.
func RunLumpSumVsDCA(p Plan, in Input, totalAmount float64) (*LumpSumResult, error) {
	if !positiveFinite(totalAmount) {
		return nil, fmt.Errorf("sim: total amount must be > 0, got %v", totalAmount)
	}
	cadence, err := ParseCadence(string(p.Cadence))
	if err != nil {
		return nil, err
	}
	if cadence == Once {
		return nil, errors.New("sim: lump sum versus DCA needs a recurring cadence (daily, weekly or monthly), not once")
	}

	// The probe validates everything but the amount and yields the calendar
	// the contribution count is read from; totalAmount stands in for the
	// not-yet-known per-contribution amount.
	probe := p
	probe.Amount = totalAmount
	pr, err := prepare(probe, in)
	if err != nil {
		return nil, err
	}
	n := countContributions(pr.plan.Cadence, pr.cal)

	dcaPlan := pr.plan
	dcaPlan.Amount = totalAmount / float64(n)
	dca, err := Run(dcaPlan, in)
	if err != nil {
		return nil, fmt.Errorf("sim: DCA leg: %w", err)
	}

	lumpPlan := pr.plan
	lumpPlan.Amount = totalAmount
	lumpPlan.Cadence = Once
	lump, err := Run(lumpPlan, in)
	if err != nil {
		return nil, fmt.Errorf("sim: lump-sum leg: %w", err)
	}

	res := &LumpSumResult{LumpSum: lump, DCA: dca, TotalAmount: totalAmount}
	res.Diff, res.Notes = compareLegs(lump, dca)
	res.Notes = append(legNotes(dcaPlan, dca, lump), res.Notes...)
	return res, nil
}

// countContributions is how many days of cal contribute under c.
func countContributions(c Cadence, cal *calendar) int {
	n := 0
	for _, contributes := range contributionDays(c, cal.days) {
		if contributes {
			n++
		}
	}
	return n
}

// compareLegs computes lump minus dca and explains a missing annualized
// difference.
func compareLegs(lump, dca *Result) (LumpSumDiff, []string) {
	diff := LumpSumDiff{
		FinalValue: lump.FinalValue - dca.FinalValue,
		ReturnPct:  lump.ReturnPct - dca.ReturnPct,
	}
	if !lump.AnnualizedReturnComputed || !dca.AnnualizedReturnComputed {
		return diff, []string{"Diff.AnnualizedReturn is 0 because at least one leg has no annualized return; see that leg's Notes"}
	}
	diff.AnnualizedReturn = lump.AnnualizedReturn - dca.AnnualizedReturn
	return diff, nil
}

// legNotes spells out what each leg did, in words the caller can show.
func legNotes(dcaPlan Plan, dca, lump *Result) []string {
	notes := []string{
		fmt.Sprintf("DCA leg: %d %s contributions of %.2f %s from %s to %s; lump-sum leg: %.2f %s on %s; both valued at %s",
			dca.Contributions, dcaPlan.Cadence, dcaPlan.Amount, dcaPlan.Currency, formatDate(dca.Start), formatDate(dca.End),
			lump.Invested, dcaPlan.Currency, formatDate(lump.Start), formatDate(lump.End)),
		"Diff is lump sum minus DCA: positive means investing everything on the first day did better",
	}
	if dcaPlan.FeeFixed > 0 {
		notes = append(notes, fmt.Sprintf("the fixed commission of %.2f %s is paid once by the lump sum (fees %.2f) and %d times by the DCA leg (fees %.2f)",
			dcaPlan.FeeFixed, dcaPlan.Currency, lump.Fees, dca.Contributions, dca.Fees))
	}
	return notes
}
