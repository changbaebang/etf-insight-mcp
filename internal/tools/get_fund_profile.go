package tools

import (
	"context"
	"fmt"
	"math"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// costReferenceHolding is the holding size annual_cost_per_10k prices.
const costReferenceHolding = 10000

type getFundProfileInput struct {
	Symbol string `json:"symbol" jsonschema:"fund ticker, e.g. VOO; case-insensitive"`
}

type getFundProfileOutput struct {
	Symbol string `json:"symbol"`
	// ExpenseRatioPct comes first: cost comparisons are the main reason to
	// call this tool.
	ExpenseRatioPct  *float64 `json:"expense_ratio_pct" jsonschema:"annual fund expense ratio in percent, 4 decimals (0.03 = 0.03% a year); null when the provider does not report it"`
	AnnualCostPer10K *float64 `json:"annual_cost_per_10k,omitempty" jsonschema:"yearly fund expense on a 10000 holding in the fund currency (expense ratio x 10000)"`
	Name             string   `json:"name"`
	Family           string   `json:"family" jsonschema:"fund family / issuer as reported by the provider"`
	Category         string   `json:"category" jsonschema:"provider category, e.g. Large Value"`
	LegalType        string   `json:"legal_type" jsonschema:"e.g. Exchange Traded Fund"`
	InceptionDate    string   `json:"inception_date"`
	TurnoverPct      *float64 `json:"turnover_pct,omitempty" jsonschema:"annual portfolio turnover in percent"`
	NetAssets        *float64 `json:"net_assets,omitempty" jsonschema:"total net assets in the fund currency"`
	YieldPct         *float64 `json:"yield_pct,omitempty" jsonschema:"provider-reported yield in percent"`
	InUniverse       bool     `json:"in_universe"`
	Universe         *etfRow  `json:"universe,omitempty"`
	FetchedAt        string   `json:"fetched_at" jsonschema:"when the provider was asked, UTC RFC 3339"`
	Notes            []string `json:"notes"`
	Warnings         []string `json:"warnings,omitempty"`
}

func (d Deps) registerGetFundProfile(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_fund_profile",
		Title:       "Get fund profile",
		Description: "Descriptive profile of one fund from Yahoo Finance: expense_ratio_pct (annual fee in percent, kept to 4 decimals, with the yearly cost on a 10000 holding), fund family, category, legal type, inception date, turnover, net assets and yield, plus the universe entry when list_etfs knows it. Use it to compare costs between similar ETFs or to check what a fund is. Fields the provider does not report are null or absent. Price-based returns in the other tools are already net of the expense ratio, so do not subtract it again. Fund data is cached for about a day.",
		Annotations: readOnly("Get fund profile", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getFundProfileInput) (*mcp.CallToolResult, getFundProfileOutput, error) {
		out, err := d.getFundProfile(ctx, in)
		return nil, out, err
	})
}

// getFundProfile fetches and converts one fund profile.
func (d Deps) getFundProfile(ctx context.Context, in getFundProfileInput) (getFundProfileOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return getFundProfileOutput{}, err
	}
	sym, err := requireFundSymbol(in.Symbol)
	if err != nil {
		return getFundProfileOutput{}, err
	}
	p, err := fund.FundProfile(ctx, sym)
	if err != nil {
		return getFundProfileOutput{}, describeFundError(sym, "the fund profile", err)
	}
	return d.toFundProfileOutput(sym, p), nil
}

// toFundProfileOutput converts fractions to percentages. The expense
// ratio keeps 4 decimals: expense ratios are quoted to thousandths of a
// percent (0.035%), which two decimals would round away.
func (d Deps) toFundProfileOutput(sym string, p *market.FundProfile) getFundProfileOutput {
	if p.Symbol != "" {
		sym = normalizeSymbol(p.Symbol)
	}
	out := getFundProfileOutput{
		Symbol:        sym,
		Name:          p.Name,
		Family:        p.Family,
		Category:      p.Category,
		LegalType:     p.LegalType,
		InceptionDate: formatDate(p.InceptionDate),
		TurnoverPct:   fundPctPtr(p.Turnover),
		NetAssets:     fundRound2Ptr(p.NetAssets),
		YieldPct:      fundPctPtr(p.Yield),
		FetchedAt:     dataTimestamp(p.FetchedAt),
		Notes:         []string{"returns from price history (get_etf_info, simulations) are already net of the expense ratio"},
		Warnings:      d.fundFetchedWarnings(sym, p.FetchedAt),
	}
	if er := p.ExpenseRatio; er != nil && dataFinite(*er) {
		out.ExpenseRatioPct = ptr(math.Round(*er*100*10000) / 10000)
		out.AnnualCostPer10K = ptr(round2(*er * costReferenceHolding))
	} else {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider reports no expense ratio for %s; check the issuer's site before comparing costs", sym))
	}
	if e, ok := universe.Get(sym); ok {
		row := toRow(e)
		out.InUniverse, out.Universe = true, &row
	}
	if p.Family == "" && p.Category == "" && p.LegalType == "" && p.ExpenseRatio == nil && p.NetAssets == nil {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider reports no fund fields for %s; it may not be a fund (search_symbols shows the instrument type)", sym))
	}
	return out
}
