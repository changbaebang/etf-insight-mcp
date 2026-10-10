package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxQuoteSymbols caps the symbols of one get_quote call (one upstream
// batch).
const maxQuoteSymbols = 50

type getQuoteInput struct {
	Symbols []string `json:"symbols" jsonschema:"1 to 50 ticker symbols, e.g. [\"VOO\", \"SCHD\", \"^VIX\"]; case-insensitive, duplicates are dropped"`
}

// quoteRow is one quote on the wire. Prices are in currency; 0 means the
// provider did not report the field.
type quoteRow struct {
	Symbol               string  `json:"symbol"`
	Name                 string  `json:"name"`
	Price                float64 `json:"price"`
	Change               float64 `json:"change" jsonschema:"price change against previous_close"`
	ChangePct            float64 `json:"change_pct"`
	PreviousClose        float64 `json:"previous_close"`
	Open                 float64 `json:"open"`
	DayLow               float64 `json:"day_low"`
	DayHigh              float64 `json:"day_high"`
	Volume               int64   `json:"volume"`
	FiftyTwoWeekLow      float64 `json:"fifty_two_week_low"`
	FiftyTwoWeekHigh     float64 `json:"fifty_two_week_high"`
	FiftyDayAverage      float64 `json:"fifty_day_average"`
	TwoHundredDayAverage float64 `json:"two_hundred_day_average"`
	DividendYieldPct     float64 `json:"dividend_yield_pct" jsonschema:"trailing annual dividend yield as the provider reports it; Yahoo leaves it at 0 for some ETFs, get_dividends computes the yield from the dividends actually paid"`
	MarketState          string  `json:"market_state" jsonschema:"REGULAR, PRE, POST, CLOSED, ... as reported by the provider"`
	Currency             string  `json:"currency"`
	Exchange             string  `json:"exchange"`
	AsOf                 string  `json:"as_of" jsonschema:"time of the price in UTC, RFC 3339"`
}

type getQuoteOutput struct {
	Count   int        `json:"count"`
	Quotes  []quoteRow `json:"quotes"`
	Missing []string   `json:"missing" jsonschema:"requested symbols the provider did not return"`
	Notes   []string   `json:"notes,omitempty"`
}

func (d Deps) registerGetQuote(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_quote",
		Title:       "Get quotes",
		Description: "Latest delayed quotes for 1 to 50 symbols in one call: price, change and change_pct against the previous close, the day's open/low/high and volume, the 52-week range, the 50- and 200-day averages, the provider's trailing dividend yield (0 for some ETFs; get_dividends computes one from paid dividends), the market state and the quote time. Use it for 'where is X trading now'; use get_etf_info or get_price_history for history and returns. Prices are in each symbol's currency, change_pct and dividend_yield_pct are percentages (1.2 = 1.2%), and 0 means the provider did not report a field. Symbols the provider does not know are listed in missing instead of failing the call. Quotes are delayed and may be cached for up to 15 minutes.",
		Annotations: readOnly("Get quotes", true),
		InputSchema: inputSchema[getQuoteInput](schemaTweaks{minItems: map[string]int{"symbols": 1}}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getQuoteInput) (*mcp.CallToolResult, getQuoteOutput, error) {
		out, err := d.getQuote(ctx, in)
		return nil, out, err
	})
}

// getQuote fetches the quotes in request order and lists what is missing.
func (d Deps) getQuote(ctx context.Context, in getQuoteInput) (getQuoteOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return getQuoteOutput{}, err
	}
	syms := uniqueSymbols(in.Symbols)
	switch {
	case len(syms) == 0:
		return getQuoteOutput{}, errors.New("symbols is required: pass 1 to 50 tickers such as [\"VOO\"]")
	case len(syms) > maxQuoteSymbols:
		return getQuoteOutput{}, fmt.Errorf("symbols has %d distinct tickers, at most %d per call; split the request", len(syms), maxQuoteSymbols)
	}
	quotes, missing, err := d.fetchQuotes(ctx, fund, syms)
	if err != nil {
		return getQuoteOutput{}, err
	}
	if len(quotes) == 0 {
		return getQuoteOutput{}, fmt.Errorf("no quote found for %s: unknown to the data source; check the tickers with search_symbols or list_etfs", strings.Join(missing, ", "))
	}
	out := getQuoteOutput{Count: len(quotes), Quotes: make([]quoteRow, 0, len(quotes)), Missing: missing}
	for _, q := range quotes {
		out.Quotes = append(out.Quotes, toQuoteRow(q))
	}
	if len(missing) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("no quote for %s; check the tickers with search_symbols", strings.Join(missing, ", ")))
	}
	return out, nil
}

// fetchQuotes asks the fund source for syms (already normalised and
// unique) and returns the quotes in request order plus the symbols that
// came back empty. missing is never nil.
func (d Deps) fetchQuotes(ctx context.Context, fund market.FundSource, syms []string) ([]market.Quote, []string, error) {
	got, err := fund.Quote(ctx, syms)
	if err != nil {
		if errors.Is(err, cache.ErrInvalidSymbol) {
			return nil, nil, fmt.Errorf("%s; tickers may only use letters, digits and . - = ^", strings.TrimPrefix(err.Error(), "cache: "))
		}
		return nil, nil, fmt.Errorf("fetching quotes failed: %s", strings.TrimPrefix(rootCause(err), "cache: "))
	}
	bySymbol := make(map[string]market.Quote, len(got))
	for _, q := range got {
		sym := normalizeSymbol(q.Symbol)
		if _, dup := bySymbol[sym]; !dup {
			bySymbol[sym] = q
		}
	}
	quotes := make([]market.Quote, 0, len(syms))
	missing := []string{}
	for _, sym := range syms {
		q, ok := bySymbol[sym]
		if !ok {
			missing = append(missing, sym)
			continue
		}
		quotes = append(quotes, q)
	}
	return quotes, missing, nil
}

// toQuoteRow rounds a quote for the wire. Volume and percentages keep
// their units; prices get two decimals.
func toQuoteRow(q market.Quote) quoteRow {
	return quoteRow{
		Symbol:               normalizeSymbol(q.Symbol),
		Name:                 q.Name,
		Price:                round2(q.Price),
		Change:               round2(q.Change),
		ChangePct:            round2(q.ChangePct),
		PreviousClose:        round2(q.PreviousClose),
		Open:                 round2(q.Open),
		DayLow:               round2(q.DayLow),
		DayHigh:              round2(q.DayHigh),
		Volume:               q.Volume,
		FiftyTwoWeekLow:      round2(q.FiftyTwoWeekLow),
		FiftyTwoWeekHigh:     round2(q.FiftyTwoWeekHigh),
		FiftyDayAverage:      round2(q.FiftyDayAverage),
		TwoHundredDayAverage: round2(q.TwoHundredDayAverage),
		DividendYieldPct:     pct(q.DividendYield),
		MarketState:          q.MarketState,
		Currency:             q.Currency,
		Exchange:             q.Exchange,
		AsOf:                 dataTimestamp(q.AsOf),
	}
}
