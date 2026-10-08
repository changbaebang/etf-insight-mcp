package sim

import "time"

// Holding is the final position in one allocated symbol.
type Holding struct {
	// Symbol is the upper-case ticker.
	Symbol string
	// Shares is the fractional number of shares held at the end.
	Shares float64
	// Invested is the gross amount contributed to this symbol (its weight
	// of every contribution, fees included), in the plan currency, so the
	// holdings' Invested sum to Result.Invested.
	Invested float64
	// Value is Shares × the end-of-range price, in the plan currency.
	Value float64
	// Weight is Value / Result.FinalValue. When uninvested cash dividends
	// exist the weights sum to less than 1.
	Weight float64
}

// Point is one sample of the portfolio over time.
type Point struct {
	// Date is the trading day sampled.
	Date time.Time
	// Invested is the cumulative gross amount contributed through Date.
	Invested float64
	// Value is the total portfolio value at Date's close, after that day's
	// contribution, in the plan currency.
	Value float64
}

// FXSummary explains how the exchange rate affected a KRW plan.
type FXSummary struct {
	// StartRate is the KRW per USD rate on Result.Start.
	StartRate float64
	// EndRate is the KRW per USD rate on Result.End.
	EndRate float64
	// AvgPurchaseRate is the total KRW invested divided by the total USD it
	// bought: the rate at which the investor effectively acquired dollars.
	AvgPurchaseRate float64
	// FXEffect is FinalValue − (final value in USD) × AvgPurchaseRate: the
	// part of the KRW profit explained by the exchange rate moving after
	// the dollars were bought. Positive when the won weakened.
	FXEffect float64
}

// Result is what happened to a Plan. All money is in the plan currency.
type Result struct {
	// Start is the first contribution day actually used and End the last
	// valuation day; both lie on the trading calendar.
	Start, End time.Time
	// Contributions is how many contributions were made.
	Contributions int
	// Invested is the sum of all contributions before fees.
	Invested float64
	// Fees is the sum of Plan.Amount × Plan.FeeRate over all contributions.
	Fees float64
	// FinalValue is the value of all holdings at End's close plus, when
	// !Plan.Reinvest, the uninvested cash dividends.
	FinalValue float64
	// Profit is FinalValue − Invested; fees therefore reduce it.
	Profit float64
	// ReturnPct is Profit / Invested × 100. It is a simple return that
	// ignores when the money was contributed; see AnnualizedReturn.
	ReturnPct float64
	// AnnualizedReturn is the money-weighted annual rate as a fraction
	// (0.08 = 8%): the XIRR of −Amount on every contribution day and
	// +FinalValue on End. It is 0 with an explanatory note when no rate in
	// [-99.99%, 1000%] fits, for example when the range is a single day.
	AnnualizedReturn float64
	// MaxDrawdownPct is the largest peak-to-trough fall of the unitised net
	// asset value, as a positive percentage (33.9 = 33.9%). The NAV is a
	// time-weighted path that excludes contributions; see engine.nav for
	// why total value would understate drawdowns. For a KRW plan the NAV
	// is in KRW, so exchange-rate moves are part of the drawdown.
	MaxDrawdownPct float64
	// CashDividends is the uninvested cash accumulated from dividends when
	// !Plan.Reinvest, converted at each pay date's rate. It is 0 when
	// dividends are reinvested.
	CashDividends float64
	// Holdings lists the final position per symbol in allocation order.
	Holdings []Holding
	// Timeline samples the portfolio on the last trading day of each
	// calendar month inside the range plus the final day, never more than
	// once per month.
	Timeline []Point
	// FX is nil unless Plan.Currency is "KRW".
	FX *FXSummary
	// Notes records every way the simulation deviated from the request,
	// e.g. "start moved from 2005-01-01 to 2011-10-20: SCHD history begins
	// there". It is nil when nothing deviated.
	Notes []string
}
