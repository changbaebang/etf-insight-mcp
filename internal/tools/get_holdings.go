package tools

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxHoldingRows caps every list of get_holdings.
const maxHoldingRows = 25

type getHoldingsInput struct {
	Symbol string `json:"symbol" jsonschema:"fund ticker, e.g. SCHD or BND; case-insensitive"`
}

// holdingRow is one top position on the wire.
type holdingRow struct {
	Symbol    string  `json:"symbol" jsonschema:"ticker of the position; empty for positions without one (bonds, cash)"`
	Name      string  `json:"name"`
	WeightPct float64 `json:"weight_pct"`
}

// sectorRow is one sector weight on the wire.
type sectorRow struct {
	Sector    string  `json:"sector" jsonschema:"provider sector key, e.g. technology, financial_services, realestate"`
	WeightPct float64 `json:"weight_pct"`
}

// ratingRow is one bond credit-rating bucket on the wire.
type ratingRow struct {
	Rating    string  `json:"rating" jsonschema:"provider rating key, e.g. us_government, aaa, bbb, below_b"`
	WeightPct float64 `json:"weight_pct"`
}

// assetMixOutput is the portfolio split by asset class; nil means not
// reported.
type assetMixOutput struct {
	StocksPct *float64 `json:"stocks_pct"`
	BondsPct  *float64 `json:"bonds_pct"`
	CashPct   *float64 `json:"cash_pct"`
	OtherPct  *float64 `json:"other_pct"`
}

type getHoldingsOutput struct {
	Symbol            string             `json:"symbol"`
	TopHoldings       []holdingRow       `json:"top_holdings" jsonschema:"largest positions as reported by the provider (usually the top 10)"`
	TopHoldingsWeight float64            `json:"top_holdings_weight_pct" jsonschema:"sum of the listed top holdings, a concentration measure"`
	SectorWeights     []sectorRow        `json:"sector_weights" jsonschema:"equity sector weights in provider order; empty for bond funds"`
	AssetMix          assetMixOutput     `json:"asset_mix"`
	BondRatings       []ratingRow        `json:"bond_ratings,omitempty" jsonschema:"credit quality buckets, present for funds holding bonds"`
	EquityStats       map[string]float64 `json:"equity_stats,omitempty" jsonschema:"portfolio equity statistics as the provider reports them, keys in snake_case; the price_to_* values are the reciprocals of the usual multiples (0.055 means P/E about 18)"`
	BondStats         map[string]float64 `json:"bond_stats,omitempty" jsonschema:"portfolio bond statistics such as duration and maturity in years, as the provider reports them"`
	FetchedAt         string             `json:"fetched_at" jsonschema:"when the provider was asked, UTC RFC 3339"`
	Notes             []string           `json:"notes,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
}

func (d Deps) registerGetHoldings(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_holdings",
		Title:       "Get fund holdings",
		Description: "Portfolio composition of one fund from Yahoo Finance: top holdings with weights (usually 10, at most 25 listed) and their combined weight, sector weights, the stock/bond/cash/other split, bond credit-rating buckets and equity or bond portfolio statistics when the fund reports them. Use it to see what a fund actually owns, how concentrated it is, or how two funds overlap. Weights are percentages (5.1 = 5.1% of the fund); null means not reported. Yahoo reports the equity price_to_* statistics as reciprocals (earnings, book, sales and cash-flow yields), not as multiples. Fund data is cached for about a day.",
		Annotations: readOnly("Get fund holdings", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getHoldingsInput) (*mcp.CallToolResult, getHoldingsOutput, error) {
		out, err := d.getHoldings(ctx, in)
		return nil, out, err
	})
}

// getHoldings fetches and converts the composition of one fund.
func (d Deps) getHoldings(ctx context.Context, in getHoldingsInput) (getHoldingsOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return getHoldingsOutput{}, err
	}
	sym, err := requireFundSymbol(in.Symbol)
	if err != nil {
		return getHoldingsOutput{}, err
	}
	h, err := fund.Holdings(ctx, sym)
	if err != nil {
		return getHoldingsOutput{}, describeFundError(sym, "holdings", err)
	}
	return d.toHoldingsOutput(sym, h), nil
}

// toHoldingsOutput converts fractions to percentages and caps the lists.
func (d Deps) toHoldingsOutput(sym string, h *market.Holdings) getHoldingsOutput {
	if h.Symbol != "" {
		sym = normalizeSymbol(h.Symbol)
	}
	out := getHoldingsOutput{
		Symbol:        sym,
		TopHoldings:   []holdingRow{},
		SectorWeights: []sectorRow{},
		AssetMix: assetMixOutput{
			StocksPct: fundPctPtr(h.StockPct),
			BondsPct:  fundPctPtr(h.BondPct),
			CashPct:   fundPctPtr(h.CashPct),
			OtherPct:  fundPctPtr(h.OtherPct),
		},
		EquityStats: holdingStats(h.EquityStats),
		BondStats:   holdingStats(h.BondStats),
		FetchedAt:   dataTimestamp(h.FetchedAt),
		Warnings:    d.fundFetchedWarnings(sym, h.FetchedAt),
	}
	top := 0.0
	for i, p := range h.Top {
		top += p.Weight
		if i < maxHoldingRows {
			out.TopHoldings = append(out.TopHoldings, holdingRow{Symbol: normalizeSymbol(p.Symbol), Name: p.Name, WeightPct: pct(p.Weight)})
		}
	}
	out.TopHoldingsWeight = pct(top)
	for i, w := range h.Sectors {
		if i < maxHoldingRows {
			out.SectorWeights = append(out.SectorWeights, sectorRow{Sector: w.Name, WeightPct: pct(w.Weight)})
		}
	}
	for i, w := range h.BondRatings {
		if i < maxHoldingRows {
			out.BondRatings = append(out.BondRatings, ratingRow{Rating: w.Name, WeightPct: pct(w.Weight)})
		}
	}
	if n := len(h.Top); n > maxHoldingRows {
		out.Notes = append(out.Notes, fmt.Sprintf("%d top holdings reported, the largest %d are listed; top_holdings_weight_pct covers all of them", n, maxHoldingRows))
	}
	if len(h.Top) == 0 && len(h.Sectors) == 0 && h.StockPct == nil && h.BondPct == nil {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider reports no holdings for %s; it may not be a fund (search_symbols shows the instrument type)", sym))
	}
	return out
}

// holdingStats renames provider keys to snake_case and rounds the values
// to 4 decimals; non-finite values are dropped. nil stays nil so the
// field is omitted.
func holdingStats(m map[string]float64) map[string]float64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]float64, len(m))
	for k, v := range m {
		if dataFinite(v) {
			out[snakeKey(k)] = round4(v)
		}
	}
	return out
}

// snakeKey turns a camelCase provider key into snake_case:
// priceToEarnings becomes price_to_earnings.
func snakeKey(k string) string {
	var b strings.Builder
	for i, r := range k {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
