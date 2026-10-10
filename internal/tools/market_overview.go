package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// overviewSeriesTimeout bounds the price-history part of market_overview
// (a cold cache fetches nine full histories); rows whose history is not
// in by then are reported without a trend.
const overviewSeriesTimeout = 30 * time.Second

// overviewInstrument is one row of the fixed market overview.
type overviewInstrument struct {
	symbol, label, assetClass string
	// trend is false for instruments whose price path is not an
	// investable trend (the VIX index).
	trend bool
}

// overviewInstruments are the market_overview rows in display order.
var overviewInstruments = []overviewInstrument{
	{symbol: "SPY", label: "S&P 500", assetClass: "equity", trend: true},
	{symbol: "QQQ", label: "Nasdaq-100", assetClass: "equity", trend: true},
	{symbol: "DIA", label: "Dow Jones Industrial Average", assetClass: "equity", trend: true},
	{symbol: "IWM", label: "Russell 2000 small caps", assetClass: "equity", trend: true},
	{symbol: "VEA", label: "Developed markets ex-US", assetClass: "equity", trend: true},
	{symbol: "VWO", label: "Emerging markets", assetClass: "equity", trend: true},
	{symbol: "TLT", label: "Long-term US Treasuries", assetClass: "bond", trend: true},
	{symbol: "BND", label: "US investment-grade bonds", assetClass: "bond", trend: true},
	{symbol: "GLD", label: "Gold", assetClass: "commodity", trend: true},
	{symbol: "^VIX", label: "VIX", assetClass: "volatility", trend: false},
}

type marketOverviewInput struct{}

// overviewRow is one instrument of the overview on the wire.
type overviewRow struct {
	Symbol        string   `json:"symbol"`
	Label         string   `json:"label"`
	AssetClass    string   `json:"asset_class" jsonschema:"equity, bond, commodity or volatility"`
	Price         float64  `json:"price" jsonschema:"ETF price in USD; for VIX the index level"`
	Change        float64  `json:"change"`
	ChangePct     float64  `json:"change_pct"`
	PreviousClose float64  `json:"previous_close"`
	MarketState   string   `json:"market_state"`
	QuoteTime     string   `json:"quote_time" jsonschema:"time of the price, UTC RFC 3339 timestamp"`
	Stale         bool     `json:"stale,omitempty" jsonschema:"true when refreshing the quote failed and the last one held in memory is shown; warnings give its age"`
	Trend         string   `json:"trend,omitempty" jsonschema:"uptrend, downtrend, sideways or insufficient-history from the daily history (50/200-day averages and their slope); absent for VIX or when the history could not be loaded"`
	TrendAsOf     string   `json:"trend_as_of,omitempty" jsonschema:"date of the last daily bar the trend uses"`
	PctVsSMA200   *float64 `json:"pct_vs_sma_200,omitempty" jsonschema:"percent above (+) or below (-) the 200-day average"`
}

type marketOverviewOutput struct {
	LatestQuoteTime string        `json:"latest_quote_time" jsonschema:"latest quote time among the rows, UTC RFC 3339 timestamp"`
	Rows            []overviewRow `json:"rows"`
	Missing         []string      `json:"missing" jsonschema:"symbols without a quote; warnings say whether the provider does not know them or fetching them failed"`
	Notes           []string      `json:"notes"`
	Warnings        []string      `json:"warnings,omitempty"`
	Disclaimer      string        `json:"disclaimer"`
}

func (d Deps) registerMarketOverview(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "market_overview",
		Title:       "Market overview",
		Description: "One-call snapshot of the broad market: delayed quotes with change_pct for SPY (S&P 500), QQQ (Nasdaq-100), DIA (Dow), IWM (small caps), VEA (developed ex-US), VWO (emerging), TLT (long Treasuries), BND (US bonds), GLD (gold) and the VIX index (label VIX), plus each ETF's rule-based trend state and distance from its 200-day average computed from the cached daily history. Use it when asked how markets are doing today or for context before discussing a plan. Takes no input. change_pct and pct_vs_sma_200 are percentages; trend describes the recent price path, not a forecast. Quotes are cached for up to 15 minutes; when a refresh fails, the last quotes held in memory are shown, marked stale, with a warning giving their age.",
		Annotations: readOnly("Market overview", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ marketOverviewInput) (*mcp.CallToolResult, marketOverviewOutput, error) {
		out, err := d.marketOverview(ctx)
		return nil, out, err
	})
}

// marketOverview fetches every quote in one call while the histories load
// through the cache, then joins them in display order.
func (d Deps) marketOverview(ctx context.Context) (marketOverviewOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return marketOverviewOutput{}, err
	}
	syms := make([]string, 0, len(overviewInstruments))
	var trendSyms []string
	for _, inst := range overviewInstruments {
		syms = append(syms, inst.symbol)
		if inst.trend {
			trendSyms = append(trendSyms, inst.symbol)
		}
	}

	type quoteResult struct {
		batch quoteBatch
		err   error
	}
	quoted := make(chan quoteResult, 1)
	go func() {
		b, err := fetchQuotes(ctx, fund, syms)
		quoted <- quoteResult{batch: b, err: err}
	}()
	seriesCtx, cancel := context.WithTimeout(ctx, overviewSeriesTimeout)
	series, failed := d.fetchAll(seriesCtx, trendSyms)
	cancel()
	qr := <-quoted
	if qr.err != nil {
		return marketOverviewOutput{}, qr.err
	}
	b := qr.batch
	if len(b.quotes) == 0 {
		return marketOverviewOutput{}, errors.New("the data source returned no quote for any overview symbol; try again later")
	}

	bySymbol := make(map[string]market.Quote, len(b.quotes))
	for _, q := range b.quotes {
		bySymbol[normalizeSymbol(q.Symbol)] = q
	}
	out := marketOverviewOutput{
		Rows:       make([]overviewRow, 0, len(overviewInstruments)),
		Missing:    []string{},
		Notes:      []string{"trend is a rule-based description of the recent daily price path (see get_etf_info for the reasons), not a prediction; quotes are delayed"},
		Warnings:   b.warnings(),
		Disclaimer: Disclaimer,
	}
	var latest time.Time
	for _, inst := range overviewInstruments {
		q, ok := bySymbol[inst.symbol]
		if !ok {
			out.Missing = append(out.Missing, inst.symbol)
			continue
		}
		if q.AsOf.After(latest) {
			latest = q.AsOf
		}
		row := overviewRow{
			Symbol:        inst.symbol,
			Label:         inst.label,
			AssetClass:    inst.assetClass,
			Price:         round2(q.Price),
			Change:        round2(q.Change),
			ChangePct:     round2(q.ChangePct),
			PreviousClose: round2(q.PreviousClose),
			MarketState:   q.MarketState,
			QuoteTime:     dataTimestamp(q.AsOf),
			Stale:         b.isStale(inst.symbol),
		}
		if inst.trend {
			out.Warnings = append(out.Warnings, overviewTrend(&row, series[inst.symbol], failed[inst.symbol])...)
			if failed[inst.symbol] == nil {
				out.Warnings = append(out.Warnings, d.staleWarnings(inst.symbol)...)
			}
		}
		out.Rows = append(out.Rows, row)
	}
	out.LatestQuoteTime = dataTimestamp(latest)
	if len(b.unknown) > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("no quote for %s: the provider does not know it", strings.Join(b.unknown, ", ")))
	}
	return out, nil
}

// overviewTrend fills the trend fields of row from s, or explains why it
// cannot.
func overviewTrend(row *overviewRow, s *market.Series, fetchErr error) []string {
	if fetchErr != nil || s == nil {
		cause := "history unavailable"
		if fetchErr != nil {
			cause = fetchErr.Error()
		}
		return []string{fmt.Sprintf("%s trend unavailable: %s", row.Symbol, cause)}
	}
	t, err := analytics.AnalyzeTrend(s, time.Time{})
	if err != nil {
		return []string{fmt.Sprintf("%s trend unavailable: %s", row.Symbol, userError(err))}
	}
	row.Trend = t.State
	row.TrendAsOf = formatDate(t.AsOf)
	if t.SMA200 > 0 {
		row.PctVsSMA200 = ptr(round2(t.PctVsSMA200))
	}
	return nil
}
