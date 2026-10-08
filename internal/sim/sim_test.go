package sim

import (
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

const tight = 1e-9

func TestParseCadence(t *testing.T) {
	tests := []struct {
		in      string
		want    Cadence
		wantErr bool
	}{
		{in: "daily", want: Daily},
		{in: " Weekly ", want: Weekly},
		{in: "MONTHLY", want: Monthly},
		{in: "", wantErr: true},
		{in: "yearly", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseCadence(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseCadence(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("ParseCadence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRunConstantPrice(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 10, constant(100))
	res := mustRun(t, singlePlan("SPY", Daily, 50), seriesInput(spy))

	if res.Contributions != 10 {
		t.Errorf("Contributions = %d, want 10", res.Contributions)
	}
	requireDate(t, "Start", res.Start, "2024-01-01")
	requireDate(t, "End", res.End, "2024-01-12")
	requireFloat(t, "Invested", res.Invested, 500, tight)
	requireFloat(t, "FinalValue", res.FinalValue, 500, tight)
	requireFloat(t, "Profit", res.Profit, 0, tight)
	requireFloat(t, "ReturnPct", res.ReturnPct, 0, tight)
	if r := res.AnnualizedReturn; r < -1e-6 || r > 1e-6 {
		t.Errorf("AnnualizedReturn = %g, want |r| < 1e-6", r)
	}
	requireFloat(t, "MaxDrawdownPct", res.MaxDrawdownPct, 0, tight)
	requireFloat(t, "Fees", res.Fees, 0, tight)
	requireFloat(t, "CashDividends", res.CashDividends, 0, tight)
	if res.FX != nil {
		t.Errorf("FX = %+v, want nil for a USD plan", res.FX)
	}
	if len(res.Notes) != 0 {
		t.Errorf("Notes = %q, want none", res.Notes)
	}
	if len(res.Holdings) != 1 {
		t.Fatalf("Holdings = %+v, want one", res.Holdings)
	}
	h := res.Holdings[0]
	if h.Symbol != "SPY" {
		t.Errorf("Holding.Symbol = %q, want SPY", h.Symbol)
	}
	requireFloat(t, "Holding.Shares", h.Shares, 5, tight)
	requireFloat(t, "Holding.Invested", h.Invested, 500, tight)
	requireFloat(t, "Holding.Value", h.Value, 500, tight)
	requireFloat(t, "Holding.Weight", h.Weight, 1, tight)
}

// TestRunLinearPriceMonthly checks the money math against arithmetic done
// by hand on a price that rises by 1 per weekday from 100 to 200 over 101
// weekdays starting 2024-01-01. Monthly contributions fall on the first
// weekday of each month, whose bar index k gives the price 100 + k.
func TestRunLinearPriceMonthly(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 101, func(k int) float64 { return 100 + float64(k) })
	const amount = 1000.0
	buys := []struct {
		date  string
		price float64
	}{
		{"2024-01-01", 100},
		{"2024-02-01", 123},
		{"2024-03-01", 144},
		{"2024-04-01", 165},
		{"2024-05-01", 187},
	}
	const lastDate, lastPrice = "2024-05-20", 200.0

	// Cross-check the literals above against the synthetic series so the
	// hand arithmetic really is about the data the engine sees.
	for _, b := range buys {
		i, ok := spy.IndexOn(date(t, b.date))
		if !ok || !spy.Bars[i].Date.Equal(date(t, b.date)) || spy.Bars[i].Close != b.price {
			t.Fatalf("series has no bar %s at %v", b.date, b.price)
		}
	}
	last, _ := spy.Last()
	if !last.Date.Equal(date(t, lastDate)) || last.Close != lastPrice {
		t.Fatalf("series ends %s at %v, want %s at %v", last.Date, last.Close, lastDate, lastPrice)
	}

	// Independent arithmetic: shares bought each month, valued at the end.
	wantInvested := amount * float64(len(buys))
	wantShares := 0.0
	for _, b := range buys {
		wantShares += amount / b.price
	}
	wantFinal := wantShares * lastPrice

	res := mustRun(t, singlePlan("SPY", Monthly, amount), seriesInput(spy))

	if res.Contributions != len(buys) {
		t.Errorf("Contributions = %d, want %d", res.Contributions, len(buys))
	}
	requireDate(t, "Start", res.Start, buys[0].date)
	requireDate(t, "End", res.End, lastDate)
	requireFloat(t, "Invested", res.Invested, wantInvested, tight)
	requireFloat(t, "FinalValue", res.FinalValue, wantFinal, tight)
	requireFloat(t, "Profit", res.Profit, wantFinal-wantInvested, tight)
	requireFloat(t, "ReturnPct", res.ReturnPct, (wantFinal-wantInvested)/wantInvested*100, tight)
	requireFloat(t, "Holdings[0].Shares", res.Holdings[0].Shares, wantShares, tight)
	// A rising price never draws down.
	requireFloat(t, "MaxDrawdownPct", res.MaxDrawdownPct, 0, tight)

	// The annualised return is the XIRR of the same flows built by hand.
	flows := make([]CashFlow, 0, len(buys)+1)
	for _, b := range buys {
		flows = append(flows, CashFlow{Date: date(t, b.date), Amount: -amount})
	}
	flows = append(flows, CashFlow{Date: date(t, lastDate), Amount: wantFinal})
	wantRate, err := XIRR(flows)
	if err != nil {
		t.Fatalf("XIRR: %v", err)
	}
	requireFloat(t, "AnnualizedReturn", res.AnnualizedReturn, wantRate, tight)
	if wantRate <= 0 {
		t.Errorf("hand XIRR = %g, expected a positive rate for a rising price", wantRate)
	}
}

func TestRunContributionCounts(t *testing.T) {
	// Enough weekdays from 2020-12-28 to reach past 2024-03-31.
	spy := newSeries(t, "SPY", "2020-12-28", 900, constant(100))
	tests := []struct {
		name       string
		cadence    Cadence
		start, end string
		want       int
	}{
		{"daily Q1 2024", Daily, "2024-01-01", "2024-03-31", 65},
		{"weekly Q1 2024", Weekly, "2024-01-01", "2024-03-31", 13},
		{"monthly Q1 2024", Monthly, "2024-01-01", "2024-03-31", 3},
		// 2023-12-25 (Mon) is ISO week 52 of 2023; 2024-01-01 starts week 1.
		{"weekly across new year", Weekly, "2023-12-25", "2024-01-12", 3},
		// 2021-01-01 (Fri) still belongs to ISO week 53 of 2020, so the
		// year change alone must not trigger a contribution.
		{"weekly ISO week 53", Weekly, "2020-12-28", "2021-01-08", 2},
		// A start inside a week contributes on that first day, then on
		// each following Monday.
		{"weekly starting midweek", Weekly, "2024-01-03", "2024-01-16", 3},
		// A start inside a month contributes on that first day.
		{"monthly starting midmonth", Monthly, "2024-01-17", "2024-02-02", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := singlePlan("SPY", tc.cadence, 100)
			p.Start, p.End = date(t, tc.start), date(t, tc.end)
			res := mustRun(t, p, seriesInput(spy))
			if res.Contributions != tc.want {
				t.Errorf("Contributions = %d, want %d", res.Contributions, tc.want)
			}
			requireFloat(t, "Invested", res.Invested, 100*float64(tc.want), tight)
		})
	}
}

func TestRunFee(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 10, constant(100))
	p := singlePlan("SPY", Daily, 100)
	p.FeeRate = 0.01
	res := mustRun(t, p, seriesInput(spy))

	requireFloat(t, "Invested", res.Invested, 1000, tight)
	requireFloat(t, "Fees", res.Fees, 0.01*res.Invested, tight)
	requireFloat(t, "FinalValue", res.FinalValue, 0.99*1000, tight)
	requireFloat(t, "Profit", res.Profit, -10, tight)
	requireFloat(t, "ReturnPct", res.ReturnPct, -1, tight)
	requireFloat(t, "Holdings[0].Shares", res.Holdings[0].Shares, 9.9, tight)
	// Fees are a cost, not a market fall: the NAV path stays flat.
	requireFloat(t, "MaxDrawdownPct", res.MaxDrawdownPct, 0, tight)
}

// TestRunKRW uses a rate of 1000 for the first contribution and 1200 for
// the second and for the final valuation. The FX series has bars only on
// 2024-01-01 and 2024-01-31 so every later day falls back to the last bar
// before it.
func TestRunKRW(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 44, constant(100)) // through 2024-02-29
	fx := barsSeries(t, "KRW=X", map[string]float64{"2024-01-01": 1000, "2024-01-31": 1200},
		[]string{"2024-01-01", "2024-01-31"})

	p := singlePlan("SPY", Monthly, 1_200_000)
	p.Currency = "krw"
	in := seriesInput(spy)
	in.FX = fx
	res := mustRun(t, p, in)

	// Hand arithmetic: 1,200,000 KRW / 1000 = 1200 USD = 12 shares, then
	// 1,200,000 / 1200 = 1000 USD = 10 shares; 22 shares × 100 × 1200.
	const wantInvested = 2_400_000.0
	const wantFinal = 22 * 100 * 1200.0
	const wantAvg = wantInvested / 2200
	const wantFXEffect = wantFinal - 2200*wantAvg

	if res.Contributions != 2 {
		t.Fatalf("Contributions = %d, want 2", res.Contributions)
	}
	requireDate(t, "End", res.End, "2024-02-29")
	requireFloat(t, "Invested", res.Invested, wantInvested, tight)
	requireFloat(t, "FinalValue", res.FinalValue, wantFinal, tight)
	requireFloat(t, "Profit", res.Profit, 240_000, tight)
	requireFloat(t, "ReturnPct", res.ReturnPct, 10, tight)
	requireFloat(t, "Holdings[0].Shares", res.Holdings[0].Shares, 22, tight)
	requireFloat(t, "Holdings[0].Value", res.Holdings[0].Value, wantFinal, tight)
	if res.FX == nil {
		t.Fatal("FX = nil, want a summary for a KRW plan")
	}
	requireFloat(t, "FX.StartRate", res.FX.StartRate, 1000, tight)
	requireFloat(t, "FX.EndRate", res.FX.EndRate, 1200, tight)
	requireFloat(t, "FX.AvgPurchaseRate", res.FX.AvgPurchaseRate, wantAvg, tight)
	requireFloat(t, "FX.FXEffect", res.FX.FXEffect, wantFXEffect, tight)
	// All of the profit is explained by the rate move, the USD price is flat.
	requireFloat(t, "FXEffect equals Profit", res.FX.FXEffect, res.Profit, tight)
	// Timeline values are in KRW too: January closes at 12 shares × 100 × 1200.
	if len(res.Timeline) != 2 {
		t.Fatalf("Timeline = %+v, want two points", res.Timeline)
	}
	requireDate(t, "Timeline[0].Date", res.Timeline[0].Date, "2024-01-31")
	requireFloat(t, "Timeline[0].Value", res.Timeline[0].Value, 12*100*1200, tight)
	requireFloat(t, "Timeline[0].Invested", res.Timeline[0].Invested, 1_200_000, tight)
}

func TestRunCashDividends(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	for i := range spy.Bars {
		spy.Bars[i].AdjClose = 95 // must be ignored when !Reinvest
	}
	spy.Bars[3].Dividend = 1

	t.Run("USD", func(t *testing.T) {
		p := singlePlan("SPY", Daily, 100)
		p.Reinvest = false
		res := mustRun(t, p, seriesInput(spy))
		// One share per day; 3 shares are held when the dividend is paid.
		requireFloat(t, "CashDividends", res.CashDividends, 3, tight)
		requireFloat(t, "FinalValue", res.FinalValue, 5*100+3, tight)
		requireFloat(t, "Profit", res.Profit, 3, tight)
		requireFloat(t, "Holdings[0].Shares", res.Holdings[0].Shares, 5, tight)
		requireFloat(t, "Holdings[0].Value", res.Holdings[0].Value, 500, tight)
		requireFloat(t, "Holdings[0].Weight", res.Holdings[0].Weight, 500.0/503, tight)
	})

	t.Run("KRW", func(t *testing.T) {
		fx := newSeries(t, "KRW=X", "2024-01-01", 5, constant(1000))
		p := singlePlan("SPY", Daily, 100_000)
		p.Currency = CurrencyKRW
		in := seriesInput(spy)
		in.FX = fx
		res := mustRun(t, p, in)
		requireFloat(t, "CashDividends", res.CashDividends, 3*1000, tight)
		requireFloat(t, "FinalValue", res.FinalValue, 5*100*1000+3000, tight)
		if res.FX == nil {
			t.Fatal("FX = nil")
		}
		// A flat rate means nothing is attributable to FX, cash included.
		requireFloat(t, "FX.AvgPurchaseRate", res.FX.AvgPurchaseRate, 1000, tight)
		requireFloat(t, "FX.FXEffect", res.FX.FXEffect, 0, tight)
	})
}

func TestRunReinvestUsesAdjClose(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	for i := range spy.Bars {
		spy.Bars[i].AdjClose = 50
	}
	spy.Bars[3].Dividend = 1 // must be ignored when Reinvest

	p := singlePlan("SPY", Daily, 100)
	p.Reinvest = true
	res := mustRun(t, p, seriesInput(spy))

	requireFloat(t, "Holdings[0].Shares", res.Holdings[0].Shares, 10, tight)
	requireFloat(t, "FinalValue", res.FinalValue, 500, tight)
	requireFloat(t, "CashDividends", res.CashDividends, 0, tight)
}

func TestRunMaxDrawdownUsesNAV(t *testing.T) {
	path := []float64{100, 120, 60, 90, 130, 100}
	spy := newSeries(t, "SPY", "2024-01-01", len(path), func(k int) float64 { return path[k] })
	res := mustRun(t, singlePlan("SPY", Daily, 100), seriesInput(spy))
	// The NAV tracks the price regardless of the daily buys, so the fall
	// from 120 to 60 is a 50% drawdown. Total value would only have
	// dropped from 220 to 210 on that day.
	requireFloat(t, "MaxDrawdownPct", res.MaxDrawdownPct, 50, tight)
}

func TestRunTimeline(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 50, constant(100)) // through 2024-03-08
	res := mustRun(t, singlePlan("SPY", Daily, 10), seriesInput(spy))

	want := []struct {
		date     string
		invested float64
	}{
		{"2024-01-31", 230},
		{"2024-02-29", 440},
		{"2024-03-08", 500},
	}
	if len(res.Timeline) != len(want) {
		t.Fatalf("Timeline has %d points, want %d: %+v", len(res.Timeline), len(want), res.Timeline)
	}
	for i, w := range want {
		requireDate(t, "Timeline.Date", res.Timeline[i].Date, w.date)
		requireFloat(t, "Timeline.Invested", res.Timeline[i].Invested, w.invested, tight)
		requireFloat(t, "Timeline.Value", res.Timeline[i].Value, w.invested, tight)
	}
}

func TestRunTwoSymbols(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 10, constant(100))
	bnd := newSeries(t, "BND", "2024-01-01", 10, constant(50))
	p := Plan{
		Allocations: []Allocation{{Symbol: " spy ", Weight: 0.6}, {Symbol: "bnd", Weight: 0.4}},
		Amount:      100,
		Currency:    "usd",
		Cadence:     "Daily",
	}
	res := mustRun(t, p, seriesInput(spy, bnd))

	if len(res.Holdings) != 2 {
		t.Fatalf("Holdings = %+v, want two", res.Holdings)
	}
	want := []Holding{
		{Symbol: "SPY", Shares: 6, Invested: 600, Value: 600, Weight: 0.6},
		{Symbol: "BND", Shares: 8, Invested: 400, Value: 400, Weight: 0.4},
	}
	for i, w := range want {
		h := res.Holdings[i]
		if h.Symbol != w.Symbol {
			t.Errorf("Holdings[%d].Symbol = %q, want %q", i, h.Symbol, w.Symbol)
		}
		requireFloat(t, "Shares", h.Shares, w.Shares, tight)
		requireFloat(t, "Invested", h.Invested, w.Invested, tight)
		requireFloat(t, "Value", h.Value, w.Value, tight)
		requireFloat(t, "Weight", h.Weight, w.Weight, tight)
	}
	requireFloat(t, "FinalValue", res.FinalValue, 1000, tight)
}

func TestRunClampsToHistory(t *testing.T) {
	a := newSeries(t, "A", "2024-01-01", 30, constant(100)) // through 2024-02-09
	b := newSeries(t, "B", "2024-01-15", 30, constant(100)) // through 2024-02-23
	plan := func() Plan {
		return Plan{
			Allocations: []Allocation{{Symbol: "A", Weight: 0.5}, {Symbol: "B", Weight: 0.5}},
			Amount:      100,
			Currency:    CurrencyUSD,
			Cadence:     Daily,
		}
	}

	t.Run("start moves to the later history", func(t *testing.T) {
		p := plan()
		p.Start = date(t, "2024-01-01")
		res := mustRun(t, p, seriesInput(a, b))
		requireDate(t, "Start", res.Start, "2024-01-15")
		requireDate(t, "End", res.End, "2024-02-09")
		if len(res.Notes) != 1 || res.Notes[0] != "start moved from 2024-01-01 to 2024-01-15: B history begins there" {
			t.Errorf("Notes = %q", res.Notes)
		}
		if res.Contributions != 20 {
			t.Errorf("Contributions = %d, want 20", res.Contributions)
		}
	})

	t.Run("zero start needs no note", func(t *testing.T) {
		res := mustRun(t, plan(), seriesInput(a, b))
		requireDate(t, "Start", res.Start, "2024-01-15")
		if len(res.Notes) != 0 {
			t.Errorf("Notes = %q, want none", res.Notes)
		}
	})

	t.Run("end moves to the shorter history", func(t *testing.T) {
		p := plan()
		p.Start, p.End = date(t, "2024-01-15"), date(t, "2024-12-31")
		res := mustRun(t, p, seriesInput(a, b))
		requireDate(t, "End", res.End, "2024-02-09")
		if len(res.Notes) != 1 || res.Notes[0] != "end moved from 2024-12-31 to 2024-02-09: A history ends there" {
			t.Errorf("Notes = %q", res.Notes)
		}
	})

	t.Run("weekend start lands on the next trading day without a note", func(t *testing.T) {
		p := plan()
		p.Start = date(t, "2024-01-20") // Saturday
		res := mustRun(t, p, seriesInput(a, b))
		requireDate(t, "Start", res.Start, "2024-01-22")
		if len(res.Notes) != 0 {
			t.Errorf("Notes = %q, want none", res.Notes)
		}
	})
}

func TestRunErrors(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 20, constant(100))
	bad := newSeries(t, "BAD", "2024-01-01", 3, constant(100))
	bad.Bars[1].Close = 0
	march := newSeries(t, "MAR", "2024-03-01", 10, constant(100))
	lateFX := newSeries(t, "KRW=X", "2024-02-01", 10, constant(1000))
	// Saturdays only: inside SPY's range but never on one of its days.
	weekends := barsSeries(t, "SAT", map[string]float64{"2024-01-06": 1, "2024-01-13": 1, "2024-01-20": 1},
		[]string{"2024-01-06", "2024-01-13", "2024-01-20"})

	base := func() Plan { return singlePlan("SPY", Daily, 100) }
	tests := []struct {
		name    string
		plan    func(t *testing.T) Plan
		in      Input
		wantErr string
	}{
		{"unknown symbol", func(*testing.T) Plan {
			p := base()
			p.Allocations[0].Symbol = "QQQ"
			return p
		}, seriesInput(spy), "no price series for QQQ"},
		{"nil series", func(*testing.T) Plan { return base() },
			Input{Series: map[string]*market.Series{"SPY": nil}}, "no price series for SPY"},
		{"invalid series", func(*testing.T) Plan {
			p := base()
			p.Allocations[0].Symbol = "BAD"
			return p
		}, seriesInput(bad), "non-positive price"},
		{"no allocations", func(*testing.T) Plan {
			p := base()
			p.Allocations = nil
			return p
		}, seriesInput(spy), "no allocations"},
		{"empty symbol", func(*testing.T) Plan {
			p := base()
			p.Allocations[0].Symbol = "  "
			return p
		}, seriesInput(spy), "empty symbol"},
		{"duplicate symbol", func(*testing.T) Plan {
			p := base()
			p.Allocations = []Allocation{{Symbol: "SPY", Weight: 0.5}, {Symbol: "spy", Weight: 0.5}}
			return p
		}, seriesInput(spy), "allocated twice"},
		{"weights do not sum to 1", func(*testing.T) Plan {
			p := base()
			p.Allocations[0].Weight = 0.9
			return p
		}, seriesInput(spy), "weights sum to 0.9"},
		{"zero weight", func(*testing.T) Plan {
			p := base()
			p.Allocations = []Allocation{{Symbol: "SPY", Weight: 1}, {Symbol: "QQQ", Weight: 0}}
			return p
		}, seriesInput(spy), "weight of QQQ must be > 0"},
		{"negative weight", func(*testing.T) Plan {
			p := base()
			p.Allocations = []Allocation{{Symbol: "SPY", Weight: 1.5}, {Symbol: "QQQ", Weight: -0.5}}
			return p
		}, seriesInput(spy), "weight of QQQ must be > 0"},
		{"zero amount", func(*testing.T) Plan {
			p := base()
			p.Amount = 0
			return p
		}, seriesInput(spy), "amount must be > 0"},
		{"negative amount", func(*testing.T) Plan {
			p := base()
			p.Amount = -1
			return p
		}, seriesInput(spy), "amount must be > 0"},
		{"bad cadence", func(*testing.T) Plan {
			p := base()
			p.Cadence = "yearly"
			return p
		}, seriesInput(spy), "bad cadence"},
		{"bad currency", func(*testing.T) Plan {
			p := base()
			p.Currency = "EUR"
			return p
		}, seriesInput(spy), "bad currency"},
		{"end before start", func(t *testing.T) Plan {
			p := base()
			p.Start, p.End = date(t, "2024-01-10"), date(t, "2024-01-09")
			return p
		}, seriesInput(spy), "end 2024-01-09 is before start 2024-01-10"},
		{"negative fee", func(*testing.T) Plan {
			p := base()
			p.FeeRate = -0.01
			return p
		}, seriesInput(spy), "fee rate"},
		{"fee of 100%", func(*testing.T) Plan {
			p := base()
			p.FeeRate = 1
			return p
		}, seriesInput(spy), "fee rate"},
		{"KRW without FX", func(*testing.T) Plan {
			p := base()
			p.Currency = CurrencyKRW
			return p
		}, seriesInput(spy), "needs an FX series"},
		{"FX starts after the first day", func(*testing.T) Plan {
			p := base()
			p.Currency = CurrencyKRW
			return p
		}, Input{Series: seriesInput(spy).Series, FX: lateFX}, "no bar on or before 2024-01-01"},
		{"empty intersection", func(*testing.T) Plan {
			p := base()
			p.Allocations = []Allocation{{Symbol: "SPY", Weight: 0.5}, {Symbol: "MAR", Weight: 0.5}}
			return p
		}, seriesInput(spy, march), "no history in range"},
		{"overlapping range but no shared day", func(*testing.T) Plan {
			p := base()
			p.Allocations = []Allocation{{Symbol: "SPY", Weight: 0.5}, {Symbol: "SAT", Weight: 0.5}}
			return p
		}, seriesInput(spy, weekends), "no trading day shared by SPY, SAT"},
		{"start after all history", func(t *testing.T) Plan {
			p := base()
			p.Start = date(t, "2025-01-01")
			return p
		}, seriesInput(spy), "no history in range: start 2025-01-01 is after end 2024-01-26"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(tc.plan(t), tc.in)
			if err == nil {
				t.Fatalf("Run returned %+v, want error containing %q", res, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunSingleDay(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	p := singlePlan("SPY", Monthly, 100)
	p.Start, p.End = date(t, "2024-01-03"), date(t, "2024-01-03")
	res := mustRun(t, p, seriesInput(spy))

	if res.Contributions != 1 {
		t.Errorf("Contributions = %d, want 1", res.Contributions)
	}
	requireDate(t, "Start", res.Start, "2024-01-03")
	requireDate(t, "End", res.End, "2024-01-03")
	requireFloat(t, "FinalValue", res.FinalValue, 100, tight)
	// Buying and valuing on the same day gives no time span to annualise.
	requireFloat(t, "AnnualizedReturn", res.AnnualizedReturn, 0, tight)
	if len(res.Notes) != 1 || !strings.HasPrefix(res.Notes[0], "annualized return not computed: ") {
		t.Errorf("Notes = %q, want one note about the annualized return", res.Notes)
	}
	if len(res.Timeline) != 1 {
		t.Errorf("Timeline = %+v, want one point", res.Timeline)
	}
}

func TestRunDoesNotModifyInputs(t *testing.T) {
	spy := newSeries(t, "SPY", "2024-01-01", 5, constant(100))
	p := Plan{
		Allocations: []Allocation{{Symbol: "spy", Weight: 1}},
		Amount:      100,
		Currency:    "usd",
		Cadence:     "DAILY",
	}
	mustRun(t, p, seriesInput(spy))
	if p.Allocations[0].Symbol != "spy" || p.Currency != "usd" || p.Cadence != "DAILY" {
		t.Errorf("Run modified the plan: %+v", p)
	}
}
