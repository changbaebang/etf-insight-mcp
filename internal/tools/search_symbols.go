package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Limits of search_symbols.
const (
	defaultSearchLimit = 10
	maxSearchLimit     = 25
	// searchFetchLimit is how many hits are requested upstream when only
	// ETFs are wanted, so that filtering out stocks and indexes still
	// leaves up to limit ETFs. It is the provider's own maximum.
	searchFetchLimit = 50
)

// searchTypeETF is the provider's instrument type of exchange-traded funds.
const searchTypeETF = "ETF"

type searchSymbolsInput struct {
	Query   string `json:"query" jsonschema:"ticker, fund name or words to look up in Latin letters, e.g. 'schwab us dividend equity', 'nasdaq 100' or 'VOO'"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of hits, 1 to 25 (default 10)"`
	ETFOnly *bool  `json:"etf_only,omitempty" jsonschema:"true (default): keep only exchange-traded funds; false: also return stocks, indexes, mutual funds and other instrument types"`
	USOnly  *bool  `json:"us_only,omitempty" jsonschema:"true (default): hide listings outside the US (symbols with a market suffix such as .TO or .L), which are not quoted in USD and which the simulation and comparison tools reject; false: return them too"`
}

// searchHit is one search result on the wire.
type searchHit struct {
	Symbol     string `json:"symbol"`
	Name       string `json:"name"`
	Type       string `json:"type" jsonschema:"provider instrument type: ETF, EQUITY, INDEX, MUTUALFUND, CRYPTOCURRENCY, ..."`
	Exchange   string `json:"exchange"`
	USListing  bool   `json:"us_listing" jsonschema:"false when Yahoo marks the symbol as listed outside the US (a market suffix such as .TO or .L); such listings trade in their local currency"`
	InUniverse bool   `json:"in_universe" jsonschema:"true when the symbol is in the built-in universe that list_etfs returns"`
}

type searchSymbolsOutput struct {
	Query   string      `json:"query"`
	ETFOnly bool        `json:"etf_only"`
	USOnly  bool        `json:"us_only"`
	Count   int         `json:"count"`
	Hits    []searchHit `json:"hits"`
	Notes   []string    `json:"notes,omitempty"`
}

func (d Deps) registerSearchSymbols(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_symbols",
		Title:       "Search symbols",
		Description: "Looks up tickers by symbol, fund name or words through Yahoo Finance search. Use it when a symbol is not in list_etfs, when the user names a fund instead of a ticker, or to check that a ticker exists before calling the price and fund tools. By default only US-listed ETFs are returned (etf_only and us_only true); foreign listings of the same fund (such as VFV.TO in Toronto) are named in a note, and in_universe marks symbols that list_etfs also knows. Yahoo search understands Latin letters only, so search by ticker or English name. Returns at most limit hits (default 10, max 25), in the provider's relevance order.",
		Annotations: readOnly("Search symbols", true),
		InputSchema: inputSchema[searchSymbolsInput](schemaTweaks{
			defaults: map[string]any{"limit": defaultSearchLimit, "etf_only": true, "us_only": true},
		}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchSymbolsInput) (*mcp.CallToolResult, searchSymbolsOutput, error) {
		out, err := d.searchSymbols(ctx, in)
		return nil, out, err
	})
}

// searchSymbols queries the fund source, filters by instrument type and
// listing, and marks universe members.
func (d Deps) searchSymbols(ctx context.Context, in searchSymbolsInput) (searchSymbolsOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return searchSymbolsOutput{}, err
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return searchSymbolsOutput{}, errors.New("query is required: a ticker, fund name or words such as 'dividend growth'")
	}
	mixedScript, err := requireLatinQuery(query)
	if err != nil {
		return searchSymbolsOutput{}, err
	}
	limit, err := parseDataLimit("limit", in.Limit, defaultSearchLimit, maxSearchLimit)
	if err != nil {
		return searchSymbolsOutput{}, err
	}
	etfOnly := in.ETFOnly == nil || *in.ETFOnly
	usOnly := in.USOnly == nil || *in.USOnly

	fetch := limit
	if etfOnly || usOnly {
		fetch = searchFetchLimit
	}
	found, err := fund.Search(ctx, query, fetch)
	if err != nil {
		return searchSymbolsOutput{}, fmt.Errorf("searching %q failed: %s", query, fundCause(err))
	}

	out := searchSymbolsOutput{Query: query, ETFOnly: etfOnly, USOnly: usOnly, Hits: []searchHit{}}
	nonETF := 0
	var foreign []string
	for _, h := range found {
		if len(out.Hits) == limit {
			break
		}
		if etfOnly && !strings.EqualFold(h.Type, searchTypeETF) {
			nonETF++
			continue
		}
		sym := normalizeSymbol(h.Symbol)
		us := !foreignListing(sym)
		if usOnly && !us {
			foreign = append(foreign, fmt.Sprintf("%s (%s)", sym, h.Exchange))
			continue
		}
		_, known := universe.Get(sym)
		out.Hits = append(out.Hits, searchHit{
			Symbol:     sym,
			Name:       h.Name,
			Type:       h.Type,
			Exchange:   h.Exchange,
			USListing:  us,
			InUniverse: known,
		})
	}
	out.Count = len(out.Hits)
	switch {
	case len(found) == 0:
		out.Notes = append(out.Notes, fmt.Sprintf("no symbol matches %q; try a shorter query, the issuer name, or list_etfs", query))
	case out.Count == 0 && nonETF > 0:
		noun := "matches that are not ETFs were"
		if nonETF == 1 {
			noun = "match that is not an ETF was"
		}
		out.Notes = append(out.Notes, fmt.Sprintf("%d %s hidden; set etf_only false to see them", nonETF, noun))
	}
	if len(foreign) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("listings outside the US were hidden: %s; they trade in their local currency, which the simulation and comparison tools reject, so set us_only false only to look them up", strings.Join(foreign, ", ")))
	}
	if mixedScript {
		out.Notes = append(out.Notes, "Yahoo search ignores the words not written in Latin letters; the results match the rest of the query only")
	}
	return out, nil
}

// foreignListing reports whether a Yahoo symbol carries a market suffix
// such as .TO (Toronto) or .L (London). Yahoo writes US share classes with
// a hyphen (BRK-B), so a dot marks a listing outside the US.
func foreignListing(sym string) bool {
	i := strings.LastIndexByte(sym, '.')
	return i > 0 && i < len(sym)-1
}
