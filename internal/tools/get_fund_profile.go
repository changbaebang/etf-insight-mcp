package tools

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// costReferenceHolding is the holding size annual_cost_per_10k prices.
const costReferenceHolding = 10000

// inceptionGap is how far the provider's inception date and the first bar
// of the price history may differ before the profile says so. A fund
// normally starts trading within days of its inception; the largest
// ordinary gap across the universe is about six weeks.
const inceptionGap = 62 * 24 * time.Hour

type getFundProfileInput struct {
	Symbol string `json:"symbol" jsonschema:"fund ticker, e.g. VOO; case-insensitive"`
}

type getFundProfileOutput struct {
	Symbol string `json:"symbol"`
	// ExpenseRatioPct comes first: cost comparisons are the main reason to
	// call this tool.
	ExpenseRatioPct  *float64 `json:"expense_ratio_pct" jsonschema:"annual expense ratio in percent from the fund's latest annual report as the provider has it (0.03 = 0.03% a year), rounded to 4 decimals; the issuer's current prospectus figure can differ slightly, so check it before a close comparison; null when the provider does not report it"`
	AnnualCostPer10K *float64 `json:"annual_cost_per_10k,omitempty" jsonschema:"yearly fund expense on a 10000 holding in the fund currency (expense ratio x 10000)"`
	Name             string   `json:"name"`
	Family           string   `json:"family" jsonschema:"fund family / issuer as reported by the provider"`
	Category         string   `json:"category" jsonschema:"provider category, e.g. Large Value"`
	LegalType        string   `json:"legal_type" jsonschema:"e.g. Exchange Traded Fund"`
	InceptionDate    string   `json:"inception_date" jsonschema:"inception date as the provider reports it"`
	PriceHistoryFrom string   `json:"price_history_from" jsonschema:"first daily bar of the price history the other tools compute from; empty when it could not be loaded. A notes entry flags a large gap to inception_date"`
	TurnoverPct      *float64 `json:"turnover_pct,omitempty" jsonschema:"annual portfolio turnover in percent"`
	NetAssets        *float64 `json:"net_assets,omitempty" jsonschema:"total net assets of the whole fund across all share classes, in the fund currency; for an ETF that is one class of a larger fund (as at Vanguard) it includes the mutual-fund classes, so it can be several times the ETF's own assets"`
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
		Description: "Descriptive profile of one fund from Yahoo Finance: expense_ratio_pct (annual fee in percent from the latest annual report, with the yearly cost on a 10000 holding; the issuer's current figure can differ slightly), fund family, category, legal type, inception date next to the first day of the price history, turnover, net assets of the whole fund across all its share classes, and yield, plus the universe entry when list_etfs knows it. Use it to compare costs between similar ETFs or to check what a fund is. Fields the provider does not report are null or absent. Price-based returns in the other tools are already net of the expense ratio, so do not subtract it again. Fund data is cached for about a day.",
		Annotations: readOnly("Get fund profile", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getFundProfileInput) (*mcp.CallToolResult, getFundProfileOutput, error) {
		out, err := d.getFundProfile(ctx, in)
		return nil, out, err
	})
}

// getFundProfile fetches and converts one fund profile, and checks its
// inception date against the start of the price history.
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
	out := d.toFundProfileOutput(sym, p)
	// The history is optional here: without it the profile is still
	// complete, only the inception check is skipped.
	if s, err := d.Source.Series(ctx, sym); err == nil {
		first, okFirst := s.First()
		last, okLast := s.Last()
		if okFirst && okLast {
			out.PriceHistoryFrom = formatDate(first.Date)
			if note := inceptionNote(p.InceptionDate, first.Date, last.Date); note != "" {
				out.Notes = append(out.Notes, note)
			}
		}
	}
	return out, nil
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
	if out.NetAssets != nil && strings.Contains(strings.ToLower(p.Family), "vanguard") {
		out.Notes = append(out.Notes, "net_assets covers the whole Vanguard fund across all its share classes: the ETF is one class beside the mutual-fund classes, so its own assets can be a fraction of this figure; compare fund sizes with other issuers with care")
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

// inceptionNote explains a price history running from firstBar to
// lastBar that starts far from the provider's inception date, or returns
// "" when they agree or the inception date is unknown.
func inceptionNote(inception, firstBar, lastBar time.Time) string {
	if inception.IsZero() {
		return ""
	}
	gap := inception.Sub(firstBar)
	switch {
	case gap > inceptionGap:
		note := fmt.Sprintf("the price history starts %s, %s before the %s inception date: the earlier prices belong to a predecessor product or another share class, or the provider's inception date is off; history-based figures in get_etf_info and the simulations include them",
			formatDate(firstBar), describeSpan(gap), formatDate(inception))
		// Suggest the cut-off only when it leaves at least 52 weeks of history.
		if lastBar.Sub(inception) >= trailingYearSpan {
			note += fmt.Sprintf(", so start simulations on or after %s to cover only this fund", formatDate(inception))
		}
		return note
	case -gap > inceptionGap:
		return fmt.Sprintf("the price history starts %s, %s after the %s inception date, so history-based figures cover only the period since %s",
			formatDate(firstBar), describeSpan(-gap), formatDate(inception), formatDate(firstBar))
	default:
		return ""
	}
}

// describeSpan renders a long span for a reader: years with one decimal
// from a year on, days below that.
func describeSpan(d time.Duration) string {
	days := d.Hours() / 24
	if days >= 365 {
		return fmt.Sprintf("%.1f years", days/365.25)
	}
	return countOf(int(days), "day")
}
