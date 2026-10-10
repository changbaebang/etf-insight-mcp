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
	Query   string `json:"query" jsonschema:"ticker, fund name or words to look up, e.g. 'schwab dividend', 'nasdaq 100' or 'VOO'"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of hits, 1 to 25 (default 10)"`
	ETFOnly *bool  `json:"etf_only,omitempty" jsonschema:"true (default): keep only exchange-traded funds; false: also return stocks, indexes, mutual funds and other instrument types"`
}

// searchHit is one search result on the wire.
type searchHit struct {
	Symbol     string `json:"symbol"`
	Name       string `json:"name"`
	Type       string `json:"type" jsonschema:"provider instrument type: ETF, EQUITY, INDEX, MUTUALFUND, CRYPTOCURRENCY, ..."`
	Exchange   string `json:"exchange"`
	InUniverse bool   `json:"in_universe" jsonschema:"true when the symbol is in the built-in universe that list_etfs returns"`
}

type searchSymbolsOutput struct {
	Query   string      `json:"query"`
	ETFOnly bool        `json:"etf_only"`
	Count   int         `json:"count"`
	Hits    []searchHit `json:"hits"`
	Notes   []string    `json:"notes,omitempty"`
}

func (d Deps) registerSearchSymbols(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_symbols",
		Title:       "Search symbols",
		Description: "Looks up tickers by symbol, fund name or words through Yahoo Finance search. Use it when a symbol is not in list_etfs, when the user names a fund instead of a ticker, or to check that a ticker exists before calling the price and fund tools. By default only ETFs are returned (etf_only true); in_universe marks symbols that list_etfs also knows. Returns at most limit hits (default 10, max 25), in the provider's relevance order.",
		Annotations: readOnly("Search symbols", true),
		InputSchema: inputSchema[searchSymbolsInput](schemaTweaks{
			defaults: map[string]any{"limit": defaultSearchLimit, "etf_only": true},
		}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchSymbolsInput) (*mcp.CallToolResult, searchSymbolsOutput, error) {
		out, err := d.searchSymbols(ctx, in)
		return nil, out, err
	})
}

// searchSymbols queries the fund source and marks universe members.
func (d Deps) searchSymbols(ctx context.Context, in searchSymbolsInput) (searchSymbolsOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return searchSymbolsOutput{}, err
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return searchSymbolsOutput{}, errors.New("query is required: a ticker, fund name or words such as 'dividend growth'")
	}
	limit, err := parseDataLimit("limit", in.Limit, defaultSearchLimit, maxSearchLimit)
	if err != nil {
		return searchSymbolsOutput{}, err
	}
	etfOnly := in.ETFOnly == nil || *in.ETFOnly

	fetch := limit
	if etfOnly {
		fetch = searchFetchLimit
	}
	found, err := fund.Search(ctx, query, fetch)
	if err != nil {
		return searchSymbolsOutput{}, fmt.Errorf("searching %q failed: %s", query, strings.TrimPrefix(rootCause(err), "cache: "))
	}

	out := searchSymbolsOutput{Query: query, ETFOnly: etfOnly, Hits: []searchHit{}}
	hidden := 0
	for _, h := range found {
		if etfOnly && !strings.EqualFold(h.Type, searchTypeETF) {
			hidden++
			continue
		}
		if len(out.Hits) == limit {
			break
		}
		sym := normalizeSymbol(h.Symbol)
		_, known := universe.Get(sym)
		out.Hits = append(out.Hits, searchHit{
			Symbol:     sym,
			Name:       h.Name,
			Type:       h.Type,
			Exchange:   h.Exchange,
			InUniverse: known,
		})
	}
	out.Count = len(out.Hits)
	switch {
	case len(found) == 0:
		out.Notes = append(out.Notes, fmt.Sprintf("no symbol matches %q; try a shorter query, the issuer name, or list_etfs", query))
	case out.Count == 0 && hidden > 0:
		noun := "matches that are not ETFs were"
		if hidden == 1 {
			noun = "match that is not an ETF was"
		}
		out.Notes = append(out.Notes, fmt.Sprintf("%d %s hidden; set etf_only false to see them", hidden, noun))
	}
	return out, nil
}
