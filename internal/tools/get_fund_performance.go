package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxAnnualReturnRows caps the calendar-year list; the most recent years
// are kept.
const maxAnnualReturnRows = 25

type getFundPerformanceInput struct {
	Symbol string `json:"symbol" jsonschema:"fund ticker, e.g. SCHD; case-insensitive"`
}

// trailingRow is one trailing-return period on the wire. The provider's
// category averages for these periods are left out: it measures them to an
// older date than the fund's own figures, so the two do not compare.
type trailingRow struct {
	Period  string   `json:"period" jsonschema:"ytd, 1m, 3m, 1y, 3y, 5y or 10y, ending on trailing_as_of; 3y and longer are annualized by the provider"`
	FundPct *float64 `json:"fund_pct"`
}

// annualRow is one calendar-year return on the wire.
type annualRow struct {
	Year        int      `json:"year"`
	FundPct     *float64 `json:"fund_pct"`
	CategoryPct *float64 `json:"category_pct" jsonschema:"average return of the fund's provider category in the same calendar year"`
}

// riskRow is one period of provider risk statistics on the wire. The
// provider reports these already in display units, so they are only
// rounded.
type riskRow struct {
	Period               string   `json:"period" jsonschema:"3y, 5y or 10y"`
	Alpha                *float64 `json:"alpha" jsonschema:"annualized alpha in percentage points against the provider's standard index (the S&P 500 for US equity funds, a broad bond index such as the Bloomberg US Aggregate for bond funds), not the benchmark of the fund's own style or category"`
	Beta                 *float64 `json:"beta" jsonschema:"sensitivity to the provider's standard index (the S&P 500 for US equity funds, a broad bond index for bond funds); a leveraged fund's beta to its own index differs"`
	Sharpe               *float64 `json:"sharpe"`
	StdDevPct            *float64 `json:"std_dev_pct" jsonschema:"annualized standard deviation of returns in percent"`
	MeanMonthlyReturnPct *float64 `json:"mean_monthly_return_pct" jsonschema:"arithmetic mean of the monthly total returns over the period, in percent per month (the provider labels it meanAnnualReturn); 0.85 a month compounds to roughly 10% a year"`
	RSquared             *float64 `json:"r_squared" jsonschema:"0 to 100: how much of the fund's movement the provider's standard index (the S&P 500 for US equity funds, a broad bond index for bond funds) explains"`
	Treynor              *float64 `json:"treynor"`
}

type getFundPerformanceOutput struct {
	Symbol       string        `json:"symbol"`
	TrailingAsOf string        `json:"trailing_as_of" jsonschema:"date the trailing returns end on (YYYY-MM-DD), typically the last month-end rather than today; empty when the provider does not say"`
	Trailing     []trailingRow `json:"trailing"`
	Annual       []annualRow   `json:"annual" jsonschema:"calendar-year returns, oldest first, from the fund's first reported year, at most the 25 most recent years"`
	Risk         []riskRow     `json:"risk"`
	FetchedAt    string        `json:"fetched_at" jsonschema:"when the provider was asked, UTC RFC 3339"`
	Notes        []string      `json:"notes,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
	Disclaimer   string        `json:"disclaimer"`
}

func (d Deps) registerGetFundPerformance(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_fund_performance",
		Title:       "Get fund performance",
		Description: "Provider-reported (Yahoo Finance / Morningstar) performance of one fund: trailing returns (ytd, 1m, 3m, 1y and annualized 3y, 5y, 10y) measured to trailing_as_of, typically the last month-end rather than today; calendar-year returns next to the category average (at most 25 years); and risk statistics for 3, 5 and 10 years (alpha, beta, Sharpe, standard deviation, R-squared, the mean monthly return and Treynor). Alpha, beta and R-squared are measured against the provider's standard index (the S&P 500 for US equity funds, a broad bond index for bond funds), not the category's benchmark. Use the calendar years to judge a fund against its peers; use get_etf_info for returns computed from the price history up to the latest close. _pct fields are percentages (12.4 = 12.4%); null means not reported. Past performance only, not a prediction.",
		Annotations: readOnly("Get fund performance", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getFundPerformanceInput) (*mcp.CallToolResult, getFundPerformanceOutput, error) {
		out, err := d.getFundPerformance(ctx, in)
		return nil, out, err
	})
}

// getFundPerformance fetches and converts the performance of one fund.
func (d Deps) getFundPerformance(ctx context.Context, in getFundPerformanceInput) (getFundPerformanceOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return getFundPerformanceOutput{}, err
	}
	sym, err := requireFundSymbol(in.Symbol)
	if err != nil {
		return getFundPerformanceOutput{}, err
	}
	p, err := fund.Performance(ctx, sym)
	if err != nil {
		return getFundPerformanceOutput{}, describeFundError(sym, "performance", err)
	}
	return d.toFundPerformanceOutput(sym, p), nil
}

// toFundPerformanceOutput converts the return fractions to percentages
// and rounds the risk statistics, which are already in display units.
func (d Deps) toFundPerformanceOutput(sym string, p *market.Performance) getFundPerformanceOutput {
	if p.Symbol != "" {
		sym = normalizeSymbol(p.Symbol)
	}
	out := getFundPerformanceOutput{
		Symbol:       sym,
		TrailingAsOf: formatDate(p.AsOf),
		Trailing:     make([]trailingRow, 0, len(p.Trailing)),
		Annual:       make([]annualRow, 0, min(len(p.Annual), maxAnnualReturnRows)),
		Risk:         make([]riskRow, 0, len(p.Risk)),
		FetchedAt:    dataTimestamp(p.FetchedAt),
		Warnings:     d.fundFetchedWarnings(sym, p.FetchedAt),
		Disclaimer:   Disclaimer,
	}
	categoryDropped := false
	var noFund []string
	for _, r := range p.Trailing {
		if r.Fund == nil {
			noFund = append(noFund, r.Period)
		}
		out.Trailing = append(out.Trailing, trailingRow{Period: r.Period, FundPct: fundPctPtr(r.Fund)})
		categoryDropped = categoryDropped || r.Category != nil
	}
	if len(noFund) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider reports no fund return for %s: the fund is younger than the period or the figure is not reported", strings.Join(noFund, ", ")))
	}
	if categoryDropped {
		out.Notes = append(out.Notes, "category averages are given for calendar years only: the provider measures its trailing category returns to an older date than the fund's, so they do not compare")
	}
	if len(p.Trailing) > 0 && p.AsOf.IsZero() {
		out.Notes = append(out.Notes, "the provider gives no as-of date for the trailing returns; they are typically measured to the last month-end, not to today")
	}

	annual := fundYears(p.Annual)
	if len(annual) > maxAnnualReturnRows {
		out.Notes = append(out.Notes, fmt.Sprintf("%d calendar years of fund returns reported, the most recent %d are listed", len(annual), maxAnnualReturnRows))
		annual = annual[len(annual)-maxAnnualReturnRows:]
	}
	for _, r := range annual {
		out.Annual = append(out.Annual, annualRow{Year: r.Year, FundPct: fundPctPtr(r.Fund), CategoryPct: fundPctPtr(r.Category)})
	}
	for _, r := range p.Risk {
		out.Risk = append(out.Risk, riskRow{
			Period:               r.Period,
			Alpha:                fundRound2Ptr(r.Alpha),
			Beta:                 fundRound2Ptr(r.Beta),
			Sharpe:               fundRound2Ptr(r.Sharpe),
			StdDevPct:            fundRound2Ptr(r.StdDev),
			MeanMonthlyReturnPct: fundRound2Ptr(r.MeanAnnualReturn),
			RSquared:             fundRound2Ptr(r.RSquared),
			Treynor:              fundRound2Ptr(r.Treynor),
		})
	}
	if len(p.Trailing) == 0 && len(p.Annual) == 0 && len(p.Risk) == 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider reports no fund performance for %s; it may not be a fund, use get_etf_info for returns from the price history", sym))
	}
	return out
}

// fundYears drops the calendar years before the fund's first reported
// return: the provider's category history reaches back further than the
// fund itself, and those rows would only show a category figure.
func fundYears(annual []market.AnnualReturn) []market.AnnualReturn {
	for i, r := range annual {
		if r.Fund != nil {
			return annual[i:]
		}
	}
	return nil
}
