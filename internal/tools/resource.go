package tools

import (
	"context"

	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// UniverseURI is the resource that serves the embedded universe CSV.
const UniverseURI = "etf://universe"

// universeMIMEType is the media type of the universe resource.
const universeMIMEType = "text/csv"

func registerUniverseResource(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI:         UniverseURI,
		Name:        "universe",
		Title:       "ETF universe (CSV)",
		Description: "The built-in list of US-listed ETFs the server knows about, as CSV with columns symbol, name, issuer, category, leveraged, note. Lines starting with # are comments. list_etfs returns the same data filtered and as JSON.",
		MIMEType:    universeMIMEType,
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: universeMIMEType,
				Text:     string(universe.CSV()),
			}},
		}, nil
	})
}
