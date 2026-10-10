package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	DividendYieldPct     float64 `json:"dividend_yield_pct" jsonschema:"the provider's dividend yield: for a fund its trailing twelve-month distribution yield, for a stock the forward annual dividend over the price; 0 when it reports none. get_dividends computes a yield from the dividends actually paid"`
	MarketState          string  `json:"market_state" jsonschema:"REGULAR, PRE, POST, CLOSED, ... as reported by the provider"`
	Currency             string  `json:"currency"`
	Exchange             string  `json:"exchange"`
	QuoteTime            string  `json:"quote_time" jsonschema:"time of the price, UTC RFC 3339 timestamp"`
	Stale                bool    `json:"stale,omitempty" jsonschema:"true when refreshing the quote failed and the last one held in memory is shown; warnings give its age"`
}

type getQuoteOutput struct {
	Count    int        `json:"count"`
	Quotes   []quoteRow `json:"quotes"`
	Missing  []string   `json:"missing" jsonschema:"requested symbols the provider does not know"`
	Failed   []string   `json:"failed,omitempty" jsonschema:"requested symbols whose quote could not be fetched because the provider failed; not known to be wrong, retry later"`
	Notes    []string   `json:"notes,omitempty"`
	Warnings []string   `json:"warnings,omitempty"`
}

func (d Deps) registerGetQuote(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_quote",
		Title:       "Get quotes",
		Description: "Latest delayed quotes for 1 to 50 symbols in one call: price, change and change_pct against the previous close, the day's open/low/high and volume, the 52-week range, the 50- and 200-day averages, the provider's dividend yield (0 when it reports none; get_dividends computes one from paid dividends), the market state and the quote time. Use it for 'where is X trading now'; use get_etf_info or get_price_history for history and returns. Prices are in each symbol's currency, change_pct and dividend_yield_pct are percentages (1.2 = 1.2%), and 0 means the provider did not report a field. Symbols the provider does not know are listed in missing and symbols whose fetch failed in failed, instead of failing the call. Quotes are delayed and cached for up to 15 minutes; when a refresh fails, the last quote held in memory is shown, marked stale, with a warning giving its age.",
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
	b, err := fetchQuotes(ctx, fund, syms)
	if err != nil {
		return getQuoteOutput{}, err
	}
	if len(b.quotes) == 0 {
		return getQuoteOutput{}, fmt.Errorf("no quote found for %s: unknown to the data source; check the tickers with search_symbols or list_etfs", strings.Join(b.unknown, ", "))
	}
	out := getQuoteOutput{
		Count:    len(b.quotes),
		Quotes:   make([]quoteRow, 0, len(b.quotes)),
		Missing:  b.unknown,
		Failed:   b.failed,
		Warnings: b.warnings(),
	}
	for _, q := range b.quotes {
		row := toQuoteRow(q)
		row.Stale = b.isStale(row.Symbol)
		out.Quotes = append(out.Quotes, row)
	}
	if len(b.unknown) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("no quote for %s: the provider does not know it; check the tickers with search_symbols", strings.Join(b.unknown, ", ")))
	}
	return out, nil
}

// quoteReporter is a FundSource that can say which quotes it served from
// memory after a failed refresh and which symbols it could not fetch at
// all. cache.FundStore is one; a plain source is asked through Quote.
type quoteReporter interface {
	QuoteReport(ctx context.Context, symbols []string) (*cache.QuoteReport, error)
}

// quoteBatch is the outcome of one quote request. Every requested symbol
// ends up in exactly one of quotes, unknown or failed.
type quoteBatch struct {
	quotes  []market.Quote     // in request order
	unknown []string           // the provider does not know these; never nil
	failed  []string           // fetching these failed and nothing was in memory
	stale   []cache.StaleQuote // quotes served from memory after a failed refresh
	cause   error              // why failed and stale are not empty
}

// fetchQuotes asks fund for syms (already normalised and unique). It
// fails only when no quote can be served because the provider failed or a
// ticker is malformed; unknown tickers and partial failures are reported
// in the batch.
func fetchQuotes(ctx context.Context, fund market.FundSource, syms []string) (quoteBatch, error) {
	var report cache.QuoteReport
	if r, ok := fund.(quoteReporter); ok {
		got, err := r.QuoteReport(ctx, syms)
		if err != nil {
			return quoteBatch{}, describeQuoteError(err)
		}
		report = *got
	} else {
		got, err := fund.Quote(ctx, syms)
		if err != nil {
			return quoteBatch{}, describeQuoteError(err)
		}
		report.Quotes = got
	}
	if len(report.Quotes) == 0 && report.Err != nil {
		return quoteBatch{}, describeQuoteError(report.Err)
	}

	bySymbol := make(map[string]market.Quote, len(report.Quotes))
	for _, q := range report.Quotes {
		sym := normalizeSymbol(q.Symbol)
		if _, dup := bySymbol[sym]; !dup {
			bySymbol[sym] = q
		}
	}
	b := quoteBatch{quotes: make([]market.Quote, 0, len(syms)), unknown: []string{}, stale: report.Stale, cause: report.Err}
	for _, sym := range syms {
		switch q, ok := bySymbol[sym]; {
		case ok:
			b.quotes = append(b.quotes, q)
		case slices.Contains(report.Failed, sym):
			b.failed = append(b.failed, sym)
		default:
			b.unknown = append(b.unknown, sym)
		}
	}
	return b, nil
}

// describeQuoteError rewrites a failed quote request for the model.
func describeQuoteError(err error) error {
	if errors.Is(err, cache.ErrInvalidSymbol) {
		return fmt.Errorf("%s; tickers may only use letters, digits and . - = ^", strings.TrimPrefix(err.Error(), "cache: "))
	}
	return fmt.Errorf("fetching quotes failed: %s", fundCause(err))
}

// isStale reports whether the quote of sym was served from memory after a
// failed refresh.
func (b quoteBatch) isStale(sym string) bool {
	return slices.ContainsFunc(b.stale, func(s cache.StaleQuote) bool { return s.Symbol == sym })
}

// warnings explains stale quotes, grouped by age, and failed fetches.
func (b quoteBatch) warnings() []string {
	var out []string
	if len(b.stale) > 0 {
		var ages []string
		bySymbols := make(map[string][]string)
		for _, s := range b.stale {
			age := describeAge(s.Age)
			if _, seen := bySymbols[age]; !seen {
				ages = append(ages, age)
			}
			bySymbols[age] = append(bySymbols[age], s.Symbol)
		}
		for _, age := range ages {
			syms := bySymbols[age]
			subject := "quote for %s is"
			if len(syms) > 1 {
				subject = "quotes for %s are"
			}
			out = append(out, fmt.Sprintf(subject+" %s old: refreshing failed (%s), so the last quotes held in memory are shown",
				strings.Join(syms, ", "), age, fundCause(b.cause)))
		}
	}
	if len(b.failed) > 0 {
		out = append(out, fmt.Sprintf("no quote for %s: fetching failed (%s); the tickers are not known to be wrong, retry later",
			strings.Join(b.failed, ", "), fundCause(b.cause)))
	}
	return out
}

// describeAge renders a quote age for a reader: minutes under two hours,
// hours under two days, days beyond.
func describeAge(age time.Duration) string {
	switch {
	case age < 2*time.Hour:
		return countOf(int(age.Minutes()), "minute")
	case age < 48*time.Hour:
		return countOf(int(age.Hours()), "hour")
	default:
		return countOf(int(age.Hours()/24), "day")
	}
}

// countOf renders n with unit, adding an s unless n is 1: "1 hour",
// "6 hours".
func countOf(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
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
		QuoteTime:            dataTimestamp(q.AsOf),
	}
}
