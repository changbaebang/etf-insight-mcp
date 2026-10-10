package tools

import (
	"context"
	"fmt"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxAnnualReturnRows caps the calendar-year list; the most recent years
// are kept.
const maxAnnualReturnRows = 25

type getFundPerformanceInput struct {
	Symbol string `json:"symbol" jsonschema:"fund ticker, e.g. SCHD; case-insensitive"`
}

// trailingRow is one trailing-return period on the wire.
type trailingRow struct {
	Period      string   `json:"period" jsonschema:"ytd, 1m, 3m, 1y, 3y, 5y or 10y; 3y and longer are annualized by the provider"`
	FundPct     *float64 `json:"fund_pct"`
	CategoryPct *float64 `json:"category_pct" jsonschema:"average of the fund's provider category over the same period"`
}

// annualRow is one calendar-year return on the wire.
type annualRow struct {
	Year        int      `json:"year"`
	FundPct     *float64 `json:"fund_pct"`
	CategoryPct *float64 `json:"category_pct"`
}

// riskRow is one period of provider risk statistics on the wire. The
// provider reports these already in display units, so they are only
// rounded.
type riskRow struct {
	Period              string   `json:"period" jsonschema:"3y, 5y or 10y"`
	Alpha               *float64 `json:"alpha" jsonschema:"annualized alpha against the category benchmark, in percentage points"`
	Beta                *float64 `json:"beta"`
	Sharpe              *float64 `json:"sharpe"`
	StdDevPct           *float64 `json:"std_dev_pct" jsonschema:"annualized standard deviation of returns in percent"`
	MeanAnnualReturnPct *float64 `json:"mean_annual_return_pct" jsonschema:"as labeled by the provider; the value behaves like the average monthly return in percent (0.85 is roughly 10% a year)"`
	RSquared            *float64 `json:"r_squared" jsonschema:"0 to 100: how much of the fund's movement the benchmark explains"`
	Treynor             *float64 `json:"treynor"`
}

type getFundPerformanceOutput struct {
	Symbol     string        `json:"symbol"`
	Trailing   []trailingRow `json:"trailing"`
	Annual     []annualRow   `json:"annual" jsonschema:"calendar-year returns, oldest first, at most the 25 most recent years"`
	Risk       []riskRow     `json:"risk"`
	FetchedAt  string        `json:"fetched_at" jsonschema:"when the provider was asked, UTC RFC 3339"`
	Notes      []string      `json:"notes,omitempty"`
	Warnings   []string      `json:"warnings,omitempty"`
	Disclaimer string        `json:"disclaimer"`
}

func (d Deps) registerGetFundPerformance(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_fund_performance",
		Title:       "Get fund performance",
		Description: "Provider-reported (Yahoo Finance / Morningstar) performance of one fund next to its category average: trailing returns (ytd, 1m, 3m, 1y and annualized 3y, 5y, 10y), calendar-year returns (at most 25 years) and risk statistics for 3, 5 and 10 years (alpha, beta, Sharpe, standard deviation, R-squared, Treynor). Use it to judge a fund against its peers; use get_etf_info for returns computed from the price history on any date. _pct fields are percentages (12.4 = 12.4%); null means not reported. Past performance only, not a prediction.",
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
		Symbol:     sym,
		Trailing:   make([]trailingRow, 0, len(p.Trailing)),
		Annual:     make([]annualRow, 0, min(len(p.Annual), maxAnnualReturnRows)),
		Risk:       make([]riskRow, 0, len(p.Risk)),
		FetchedAt:  dataTimestamp(p.FetchedAt),
		Warnings:   d.fundFetchedWarnings(sym, p.FetchedAt),
		Disclaimer: Disclaimer,
	}
	for _, r := range p.Trailing {
		out.Trailing = append(out.Trailing, trailingRow{Period: r.Period, FundPct: fundPctPtr(r.Fund), CategoryPct: fundPctPtr(r.Category)})
	}
	annual := p.Annual
	if len(annual) > maxAnnualReturnRows {
		annual = annual[len(annual)-maxAnnualReturnRows:]
		out.Notes = append(out.Notes, fmt.Sprintf("%d calendar years reported, the most recent %d are listed", len(p.Annual), maxAnnualReturnRows))
	}
	for _, r := range annual {
		out.Annual = append(out.Annual, annualRow{Year: r.Year, FundPct: fundPctPtr(r.Fund), CategoryPct: fundPctPtr(r.Category)})
	}
	for _, r := range p.Risk {
		out.Risk = append(out.Risk, riskRow{
			Period:              r.Period,
			Alpha:               fundRound2Ptr(r.Alpha),
			Beta:                fundRound2Ptr(r.Beta),
			Sharpe:              fundRound2Ptr(r.Sharpe),
			StdDevPct:           fundRound2Ptr(r.StdDev),
			MeanAnnualReturnPct: fundRound2Ptr(r.MeanAnnualReturn),
			RSquared:            fundRound2Ptr(r.RSquared),
			Treynor:             fundRound2Ptr(r.Treynor),
		})
	}
	if len(p.Trailing) == 0 && len(p.Annual) == 0 && len(p.Risk) == 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider reports no fund performance for %s; it may not be a fund, use get_etf_info for returns from the price history", sym))
	}
	return out
}
