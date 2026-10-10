package tools

import (
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestGetDividends(t *testing.T) {
	sess, src, _ := newDataSession(t)

	t.Run("quarterly payer", func(t *testing.T) {
		var out getDividendsOutput
		callOK(t, sess, "get_dividends", map[string]any{"symbol": "spy"}, &out)
		spy := src.series["SPY"]
		last, _ := spy.Last()
		if out.Symbol != "SPY" || out.Currency != "USD" || out.From != "2021-01-04" || out.To != formatDate(last.Date) {
			t.Errorf("header = %+v", out)
		}
		if out.Count != 12 || len(out.Dividends) != 12 || out.Truncated {
			t.Fatalf("count/rows/truncated = %d/%d/%v, want 12/12/false", out.Count, len(out.Dividends), out.Truncated)
		}
		if d := out.Dividends[0]; d.Date != formatDate(spy.Bars[63].Date) || d.Amount != 1.5 {
			t.Errorf("first payment = %+v, want bar 63 paying 1.5", d)
		}
		if len(out.AnnualTotals) != 3 {
			t.Fatalf("annual totals = %+v, want 2021..2023", out.AnnualTotals)
		}
		for i, y := range out.AnnualTotals {
			if y.Year != 2021+i || y.Total != 6 || y.Payments != 4 || !y.FullYear {
				t.Errorf("year %d = %+v, want 4 payments totalling 6 in a full year", 2021+i, y)
			}
		}
		if out.PaymentsPerYear != 4 || out.Frequency != "quarterly" {
			t.Errorf("payments_per_year/frequency = %v/%s, want 4/quarterly", out.PaymentsPerYear, out.Frequency)
		}
		if out.TTMAsOf != out.To || out.TTMTotal != 6 || out.Close != round2(last.Close) || out.TTMYieldPct != pct(6/last.Close) {
			t.Errorf("ttm = %s/%v/%v/%v, want %s/6/%v/%v", out.TTMAsOf, out.TTMTotal, out.Close, out.TTMYieldPct, out.To, round2(last.Close), pct(6/last.Close))
		}
		if out.Growth5YPct != nil || !hasDataNote(out.Notes, "needs 6 full calendar years in the range, found 3") {
			t.Errorf("growth/notes = %v/%v, want absent with the reason", out.Growth5YPct, out.Notes)
		}
		if out.Disclaimer != Disclaimer {
			t.Error("disclaimer missing")
		}

		// The trailing-year figures agree with get_etf_info.
		var info getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "SPY"}, &info)
		if info.Summary.TTMDividendPerShare != out.TTMTotal || info.Summary.TTMDividendYieldPct != out.TTMYieldPct {
			t.Errorf("get_etf_info ttm %v/%v, get_dividends %v/%v", info.Summary.TTMDividendPerShare, info.Summary.TTMDividendYieldPct, out.TTMTotal, out.TTMYieldPct)
		}
	})

	t.Run("five-year growth", func(t *testing.T) {
		var out getDividendsOutput
		callOK(t, sess, "get_dividends", map[string]any{"symbol": "VIG"}, &out)
		if out.Count != 32 || len(out.AnnualTotals) != 8 {
			t.Fatalf("count/years = %d/%d, want 32 payments over 8 years", out.Count, len(out.AnnualTotals))
		}
		if y := out.AnnualTotals[2]; y.Year != 2018 || y.Total != 1.21 || !y.FullYear {
			t.Errorf("2018 = %+v, want a full year totalling 1.21", y)
		}
		if y := out.AnnualTotals[7]; y.Year != 2023 || y.Total != round4(1.9487171) {
			t.Errorf("2023 = %+v, want 1.9487", y)
		}
		if out.Growth5YPct == nil || *out.Growth5YPct != 10 {
			t.Errorf("growth_5y_pct = %v, want 10", out.Growth5YPct)
		}
		if !hasDataNote(out.Notes, "compares the 2018 total 1.2100 with the 2023 total 1.9487") {
			t.Errorf("notes = %v, want the compared years", out.Notes)
		}
		if out.PaymentsPerYear != 4 || out.Frequency != "quarterly" {
			t.Errorf("payments_per_year/frequency = %v/%s", out.PaymentsPerYear, out.Frequency)
		}
	})

	t.Run("growth needs the base year inside the range", func(t *testing.T) {
		var out getDividendsOutput
		callOK(t, sess, "get_dividends", map[string]any{"symbol": "VIG", "start": "2018-01-02"}, &out)
		if out.Growth5YPct == nil || *out.Growth5YPct != 10 {
			t.Errorf("growth from 2018 = %v, want 10", out.Growth5YPct)
		}
		var late getDividendsOutput
		callOK(t, sess, "get_dividends", map[string]any{"symbol": "VIG", "start": "2018-01-10"}, &late)
		if late.Growth5YPct != nil || late.AnnualTotals[0].FullYear || !hasDataNote(late.Notes, "found 5") {
			t.Errorf("growth/2018 full/notes = %v/%v/%v, want absent and partial when the range starts mid-January", late.Growth5YPct, late.AnnualTotals[0].FullYear, late.Notes)
		}
	})

	t.Run("partial years only", func(t *testing.T) {
		var out getDividendsOutput
		callOK(t, sess, "get_dividends", map[string]any{"symbol": "SPY", "start": "2021-03-01", "end": "2022-06-30"}, &out)
		bars := src.series["SPY"].Between(date(t, "2021-03-01"), date(t, "2022-06-30"))
		want := 0
		for _, b := range bars {
			if b.Dividend > 0 {
				want++
			}
		}
		if out.Count != want || out.From != "2021-03-01" || out.To != "2022-06-30" {
			t.Errorf("count/from/to = %d/%s/%s, want %d/2021-03-01/2022-06-30", out.Count, out.From, out.To, want)
		}
		if out.PaymentsPerYear != 0 || out.Frequency != "unknown" || !hasDataNote(out.Notes, "no full calendar year") {
			t.Errorf("payments/frequency/notes = %v/%s/%v", out.PaymentsPerYear, out.Frequency, out.Notes)
		}
		for _, y := range out.AnnualTotals {
			if y.FullYear {
				t.Errorf("%d marked full in a partial range", y.Year)
			}
		}
		if out.TTMAsOf != "2022-06-30" {
			t.Errorf("ttm_as_of = %s, want the range end", out.TTMAsOf)
		}
	})

	t.Run("long lists keep the most recent payments", func(t *testing.T) {
		var out getDividendsOutput
		callOK(t, sess, "get_dividends", map[string]any{"symbol": "DIVM"}, &out)
		s := src.series["DIVM"]
		var lastPay market.Bar
		for _, b := range s.Bars {
			if b.Dividend > 0 {
				lastPay = b
			}
		}
		if out.Count != 155 || len(out.Dividends) != maxDividendRows || !out.Truncated {
			t.Fatalf("count/rows/truncated = %d/%d/%v, want 155/%d/true", out.Count, len(out.Dividends), out.Truncated, maxDividendRows)
		}
		if got := out.Dividends[len(out.Dividends)-1].Date; got != formatDate(lastPay.Date) {
			t.Errorf("last listed payment = %s, want %s", got, formatDate(lastPay.Date))
		}
		if out.Frequency != "irregular" || !hasDataNote(out.Notes, "only the most recent 120") {
			t.Errorf("frequency/notes = %s/%v", out.Frequency, out.Notes)
		}
	})

	t.Run("no dividends", func(t *testing.T) {
		var out getDividendsOutput
		callOK(t, sess, "get_dividends", map[string]any{"symbol": "KRW=X"}, &out)
		if out.Count != 0 || out.Dividends == nil || out.TTMTotal != 0 || out.Frequency != "none" || !hasDataNote(out.Notes, "no dividend was paid") {
			t.Errorf("out = %+v", out)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown symbol", args: map[string]any{"symbol": "NOPE"}, want: "unknown symbol NOPE"},
		{name: "empty symbol", args: map[string]any{"symbol": ""}, want: "symbol is required"},
		{name: "bad date", args: map[string]any{"symbol": "SPY", "start": "2022/01/01"}, want: "YYYY-MM-DD"},
		{name: "end before start", args: map[string]any{"symbol": "SPY", "start": "2022-02-01", "end": "2022-01-01"}, want: "end 2022-01-01 is before start 2022-02-01"},
		{name: "outside history", args: map[string]any{"symbol": "SPY", "end": "2020-01-01"}, want: "data covers 2021-01-04"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_dividends", tt.args, tt.want)
		})
	}
}

// hasDataNote reports whether any note contains substr.
func hasDataNote(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func TestDividendFrequencyAndGrowth(t *testing.T) {
	years := func(payments ...int) []divYear {
		out := make([]divYear, 0, len(payments))
		for i, n := range payments {
			out = append(out, divYear{year: 2010 + i, payments: n, total: float64(n), full: true})
		}
		return out
	}
	freq := []struct {
		payments []int
		want     float64
		label    string
	}{
		{payments: []int{12, 12, 11}, want: 12, label: "monthly"},
		{payments: []int{4, 5}, want: 4.5, label: "quarterly"},
		{payments: []int{2, 2, 3}, want: 2, label: "semiannual"},
		{payments: []int{1}, want: 1, label: "annual"},
		{payments: []int{0, 0, 1}, want: 0, label: "none"},
		{payments: []int{7}, want: 7, label: "irregular"},
		{payments: nil, want: 0, label: "unknown"},
	}
	for _, tt := range freq {
		got, label := dividendFrequency(years(tt.payments...))
		if got != tt.want || label != tt.label {
			t.Errorf("dividendFrequency(%v) = %v/%s, want %v/%s", tt.payments, got, label, tt.want, tt.label)
		}
	}

	if g, note := dividendGrowth(years(0, 4, 4, 4, 4, 4)); g != nil || !strings.Contains(note, "2010 paid no dividend") {
		t.Errorf("zero base year = %v/%q, want absent with the year named", g, note)
	}
	if g, _ := dividendGrowth(years(4, 4, 4, 4, 4, 0)); g == nil || *g != -100 {
		t.Errorf("stopped paying = %v, want -100", g)
	}
	if g, note := dividendGrowth(years(4, 4, 4, 4, 4)); g != nil || !strings.Contains(note, "found 5") {
		t.Errorf("five full years = %v/%q, want absent", g, note)
	}
}

// TestGetDividendsReviewFixes pins the split caveat, the recent-years
// frequency and the short-history flag on the trailing-year figures.
func TestGetDividendsReviewFixes(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("payments before a split", func(t *testing.T) {
		out := callRaw(t, sess, "get_dividends", map[string]any{"symbol": "SPLD"})
		divs, _ := out["dividends"].([]any)
		if len(divs) == 0 {
			t.Fatal("no dividends")
		}
		for _, d := range divs {
			row := d.(map[string]any)
			before := row["date"].(string) < "2022-07-01"
			if paid, ok := row["amount_as_paid"]; before && paid != 0.9 || !before && ok {
				t.Errorf("payment %v: amount_as_paid should be 0.9 before the 3:1 split and absent after", row)
			}
		}
		if !hasDataNote(rawStrings(out["notes"]), "3:1 split on 2022-07-01") {
			t.Errorf("notes = %v, want the split named", out["notes"])
		}
		if d := schemaDescription(t, sess, "get_dividends", "dividends", "amount"); !strings.Contains(d, "split") {
			t.Errorf("amount description = %q, want the split adjustment stated", d)
		}
	})

	t.Run("short history before ttm_as_of", func(t *testing.T) {
		out := callRaw(t, sess, "get_dividends", map[string]any{"symbol": "SPY", "end": "2021-06-30"})
		if !hasDataNote(rawStrings(out["notes"]), "52 weeks") {
			t.Errorf("notes = %v, want the trailing-year figures flagged as partial", out["notes"])
		}
	})
}

func TestDividendFrequencyFollowsRecentYears(t *testing.T) {
	// TQQQ's payment counts for its full years 2011-2025: the schedule
	// became quarterly in 2023, so the long-run median of 1 is misleading.
	counts := []int{0, 0, 0, 2, 1, 0, 0, 1, 2, 0, 0, 1, 4, 4, 4}
	years := make([]divYear, 0, len(counts))
	for i, n := range counts {
		years = append(years, divYear{year: 2011 + i, payments: n, total: float64(n), full: true})
	}
	if got, label := dividendFrequency(years); got != 4 || label != "quarterly" {
		t.Errorf("dividendFrequency = %v/%s, want 4/quarterly from the recent years", got, label)
	}
}
