package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Limits of get_news.
const (
	defaultNewsLimit = 10
	maxNewsLimit     = 20
)

type getNewsInput struct {
	Query string `json:"query" jsonschema:"a ticker such as SCHD or words in Latin letters such as 'treasury yields'"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of headlines, 1 to 20 (default 10)"`
}

// newsRow is one headline on the wire.
type newsRow struct {
	Title       string `json:"title"`
	Publisher   string `json:"publisher"`
	Link        string `json:"link"`
	PublishedAt string `json:"published_at" jsonschema:"UTC RFC 3339; empty when the provider gives no time"`
}

type getNewsOutput struct {
	Query string    `json:"query"`
	Count int       `json:"count"`
	News  []newsRow `json:"news"`
	Notes []string  `json:"notes,omitempty"`
}

func (d Deps) registerGetNews(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_news",
		Title:       "Get news",
		Description: "Recent news headlines for a ticker or free text from Yahoo Finance search: title, publisher, link and publish time (UTC). Use it to explain a recent move or to give context; the tool returns headlines only, not article text, so cite the link rather than inventing details. Yahoo search understands Latin letters only, so query by ticker or English words. At most limit headlines (default 10, max 20), in the provider's order.",
		Annotations: readOnly("Get news", true),
		InputSchema: inputSchema[getNewsInput](schemaTweaks{
			defaults: map[string]any{"limit": defaultNewsLimit},
		}),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getNewsInput) (*mcp.CallToolResult, getNewsOutput, error) {
		out, err := d.getNews(ctx, in)
		return nil, out, err
	})
}

// getNews fetches headlines for the query.
func (d Deps) getNews(ctx context.Context, in getNewsInput) (getNewsOutput, error) {
	fund, err := d.requireFund()
	if err != nil {
		return getNewsOutput{}, err
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return getNewsOutput{}, errors.New("query is required: a ticker such as SCHD or words such as 'treasury yields'")
	}
	mixedScript, err := requireLatinQuery(query)
	if err != nil {
		return getNewsOutput{}, err
	}
	limit, err := parseDataLimit("limit", in.Limit, defaultNewsLimit, maxNewsLimit)
	if err != nil {
		return getNewsOutput{}, err
	}
	items, err := fund.News(ctx, query, limit)
	if err != nil {
		return getNewsOutput{}, fmt.Errorf("fetching news for %q failed: %s", query, fundCause(err))
	}
	out := getNewsOutput{Query: query, News: make([]newsRow, 0, min(len(items), limit))}
	for _, it := range items {
		if len(out.News) == limit {
			break
		}
		out.News = append(out.News, newsRow{
			Title:       it.Title,
			Publisher:   it.Publisher,
			Link:        it.Link,
			PublishedAt: dataTimestamp(it.PublishedAt),
		})
	}
	out.Count = len(out.News)
	if out.Count == 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("no headlines for %q; try the ticker or broader words", query))
	}
	if mixedScript {
		out.Notes = append(out.Notes, "Yahoo search ignores the words not written in Latin letters, so these headlines match the rest of the query only")
	}
	return out, nil
}
