package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxHoldingRows caps every list of get_holdings.
const maxHoldingRows = 25

// Sector weights are scaled to the whole fund by its stock share, and left
// out below minSectorStockShare: the provider splits only the stock part
// by sector, so a bond fund's stray equity line can read 99% utilities.
// Below fullStockShare the scaling is worth a note.
const (
	minSectorStockShare = 0.05
	fullStockShare      = 0.95
)

// ratingUSGovernment is the provider's bond-rating key that overlaps the
// letter grades: US government bonds are also counted as AA or AAA.
const ratingUSGovernment = "us_government"

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
	WeightPct float64 `json:"weight_pct" jsonschema:"share of the whole fund in percent"`
}

// ratingRow is one bond credit-rating bucket on the wire.
type ratingRow struct {
	Rating    string  `json:"rating" jsonschema:"provider rating key: aaa, aa, a, bbb, bb, b, below_b or other"`
	WeightPct float64 `json:"weight_pct"`
}

// assetMixOutput is the portfolio split by asset class; nil means not
// reported.
type assetMixOutput struct {
	StocksPct *float64 `json:"stocks_pct"`
	BondsPct  *float64 `json:"bonds_pct"`
	CashPct   *float64 `json:"cash_pct"`
	OtherPct  *float64 `json:"other_pct" jsonschema:"everything else, including preferred stock and convertibles such as equity-linked notes"`
}

type getHoldingsOutput struct {
	Symbol            string             `json:"symbol"`
	TopHoldings       []holdingRow       `json:"top_holdings" jsonschema:"largest positions as reported by the provider (usually the top 10)"`
	TopHoldingsWeight *float64           `json:"top_holdings_weight_pct" jsonschema:"sum of the listed top holdings, a concentration measure; null when the provider lists none"`
	SectorWeights     []sectorRow        `json:"sector_weights" jsonschema:"equity sector weights as shares of the whole fund (the provider's split of the stock part scaled by stocks_pct), in provider order; empty when stocks are under 5% of the fund"`
	AssetMix          assetMixOutput     `json:"asset_mix"`
	BondRatings       []ratingRow        `json:"bond_ratings,omitempty" jsonschema:"credit-quality letter grades of the bond portfolio as the provider reports them; absent when it reports none"`
	USGovernmentPct   *float64           `json:"us_government_pct,omitempty" jsonschema:"the provider's weight of US government bonds, kept apart from bond_ratings because those bonds are also counted in a letter grade; it can exceed 100"`
	EquityStats       map[string]float64 `json:"equity_stats,omitempty" jsonschema:"portfolio equity statistics, keys in snake_case; price_to_* are ordinary multiples (price_to_earnings 18 is a P/E of 18), converted from the yields the provider reports"`
	FetchedAt         string             `json:"fetched_at" jsonschema:"when the provider was asked, UTC RFC 3339; the portfolio itself is the provider's latest reported one, usually a month-end some weeks earlier"`
	Notes             []string           `json:"notes,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
}

func (d Deps) registerGetHoldings(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_holdings",
		Title:       "Get fund holdings",
		Description: "Portfolio composition of one fund from Yahoo Finance: top holdings with weights (usually 10, at most 25 listed) and their combined weight, equity sector weights as shares of the whole fund, the stock/bond/cash/other split, bond credit-rating letter grades with the US government share apart, and equity statistics such as P/E and P/B as ordinary multiples. Use it to see what a fund actually owns, how concentrated it is, or how two funds overlap. Weights are percentages (5.1 = 5.1% of the fund); null means not reported. The portfolio is the provider's latest reported one, usually a month-end some weeks before fetched_at. The provider's bond duration and maturity are not shown because they contradict the issuers' figures; leveraged and inverse funds get their exposure from derivatives these holdings do not show. Fund data is cached for about a day.",
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

// toHoldingsOutput converts fractions to percentages, caps the lists and
// corrects what the provider reports misleadingly: sector weights of a
// sliver of stocks, an overlapping government bucket, reciprocal price
// ratios and implausible bond statistics.
func (d Deps) toHoldingsOutput(sym string, h *market.Holdings) getHoldingsOutput {
	if h.Symbol != "" {
		sym = normalizeSymbol(h.Symbol)
	}
	out := getHoldingsOutput{
		Symbol:      sym,
		TopHoldings: []holdingRow{},
		AssetMix: assetMixOutput{
			StocksPct: fundPctPtr(h.StockPct),
			BondsPct:  fundPctPtr(h.BondPct),
			CashPct:   fundPctPtr(h.CashPct),
			OtherPct:  fundPctPtr(h.OtherPct),
		},
		FetchedAt: dataTimestamp(h.FetchedAt),
		Warnings:  d.fundFetchedWarnings(sym, h.FetchedAt),
	}
	reported := len(h.Top) > 0 || len(h.Sectors) > 0 || h.StockPct != nil || h.BondPct != nil
	if !reported {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider reports no holdings for %s; it may not be a fund (search_symbols shows the instrument type)", sym))
	}

	top := 0.0
	for i, p := range h.Top {
		top += p.Weight
		if i < maxHoldingRows {
			out.TopHoldings = append(out.TopHoldings, holdingRow{Symbol: normalizeSymbol(p.Symbol), Name: p.Name, WeightPct: pct(p.Weight)})
		}
	}
	switch n := len(h.Top); {
	case n > maxHoldingRows:
		out.TopHoldingsWeight = ptr(pct(top))
		out.Notes = append(out.Notes, fmt.Sprintf("%d top holdings reported, the largest %d are listed; top_holdings_weight_pct covers all of them", n, maxHoldingRows))
	case n > 0:
		out.TopHoldingsWeight = ptr(pct(top))
	case reported:
		out.Notes = append(out.Notes, fmt.Sprintf("the provider lists no top holdings for %s, so concentration is unknown (common for bond funds)", sym))
	}

	var notes []string
	out.SectorWeights, notes = sectorWeights(h.Sectors, h.StockPct)
	out.Notes = append(out.Notes, notes...)
	out.BondRatings, out.USGovernmentPct = bondRatings(h.BondRatings)
	out.EquityStats, notes = equityStats(h.EquityStats)
	out.Notes = append(out.Notes, notes...)
	if len(h.BondStats) > 0 {
		out.Notes = append(out.Notes, "the provider's bond duration and maturity are not shown: they contradict the issuers' published figures (a long-Treasury fund came out shorter than a short-Treasury one); see the issuer's fact sheet")
	}
	if e, ok := universe.Get(sym); ok && e.Leveraged {
		out.Notes = append(out.Notes, fmt.Sprintf("%s is a leveraged or inverse fund: its market exposure comes mostly from swaps and futures, which these holdings do not show (the cash and money-market positions are their collateral), so asset_mix and the weights understate the exposure", sym))
	}
	if reported {
		out.Notes = append(out.Notes, "weights are the provider's latest reported portfolio, usually a month-end some weeks before fetched_at, not today's")
	}
	return out
}

// sectorWeights turns the provider's sector split of the stock part into
// shares of the whole fund, at most maxHoldingRows of them. Without a
// stock share it keeps the split as reported, and below
// minSectorStockShare it returns none; the notes say which happened.
func sectorWeights(sectors []market.Weight, stockPct *float64) ([]sectorRow, []string) {
	rows := []sectorRow{}
	if len(sectors) == 0 {
		return rows, nil
	}
	scale := 1.0
	var notes []string
	switch {
	case stockPct == nil || !dataFinite(*stockPct):
		notes = append(notes, "sector weights describe the stock part of the fund only: the provider does not report the fund's stock share")
	case *stockPct < minSectorStockShare:
		return rows, []string{fmt.Sprintf("sector weights are omitted: stocks are %g%% of the fund, and the provider's sector split describes only that slice", pct(*stockPct))}
	default:
		scale = *stockPct
		if scale < fullStockShare {
			notes = append(notes, fmt.Sprintf("sector weights are scaled to the whole fund: the provider splits only the stock part (%g%% of the fund) by sector", pct(scale)))
		}
	}
	for i, w := range sectors {
		if i == maxHoldingRows {
			break
		}
		rows = append(rows, sectorRow{Sector: w.Name, WeightPct: pct(w.Weight * scale)})
	}
	return rows, notes
}

// bondRatings splits the provider's rating buckets into the letter grades
// and the US government weight, which overlaps them. Letter grades that
// are all zero are padding and are dropped.
func bondRatings(ratings []market.Weight) ([]ratingRow, *float64) {
	var rows []ratingRow
	var usGov *float64
	anyGrade := false
	for _, w := range ratings {
		if w.Name == ratingUSGovernment {
			usGov = ptr(pct(w.Weight))
			continue
		}
		if len(rows) < maxHoldingRows {
			rows = append(rows, ratingRow{Rating: w.Name, WeightPct: pct(w.Weight)})
		}
		anyGrade = anyGrade || w.Weight != 0
	}
	if !anyGrade {
		rows = nil
	}
	return rows, usGov
}

// equityStats renames provider keys to snake_case and drops non-finite
// values. The provider reports the price_to_* statistics as their
// reciprocals (earnings, book, sales and cash-flow yields), so those are
// inverted into ordinary multiples with two decimals; a value that is not
// positive has no meaningful multiple and is dropped with a note. Other
// statistics keep 4 decimals. nil stays nil so the field is omitted.
func equityStats(m map[string]float64) (map[string]float64, []string) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]float64, len(m))
	var dropped []string
	for k, v := range m {
		if !dataFinite(v) {
			continue
		}
		key := snakeKey(k)
		switch {
		case !strings.HasPrefix(key, "price_to_"):
			out[key] = round4(v)
		case v > 0:
			out[key] = round2(1 / v)
		default:
			dropped = append(dropped, key)
		}
	}
	if len(dropped) == 0 {
		return out, nil
	}
	slices.Sort(dropped)
	return out, []string{fmt.Sprintf("%s omitted: the provider's value is not positive, so it has no meaningful multiple", strings.Join(dropped, ", "))}
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
