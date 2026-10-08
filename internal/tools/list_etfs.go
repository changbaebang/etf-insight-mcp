package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// leveragedCategory is the universe category hidden unless asked for.
const leveragedCategory = "Leveraged / Inverse"

type listETFsInput struct {
	Category         string `json:"category,omitempty" jsonschema:"exact category name, case-insensitive; every valid name is returned in categories, e.g. Dividend, US Large Cap, US Treasury"`
	Issuer           string `json:"issuer,omitempty" jsonschema:"exact issuer name, case-insensitive: Vanguard, iShares, State Street (SPDR funds), Invesco, Schwab, JPMorgan, ARK, ProShares, Direxion, VanEck, WisdomTree, First Trust, Fidelity, Dimensional, Pacer, Global X or Avantis"`
	Query            string `json:"query,omitempty" jsonschema:"case-insensitive text matched against symbol and name, e.g. 'S&P 500' or 'treasury'"`
	IncludeLeveraged bool   `json:"include_leveraged,omitempty" jsonschema:"include leveraged and inverse funds (default false); set automatically when category is 'Leveraged / Inverse'"`
}

// etfRow is one universe entry on the wire.
type etfRow struct {
	Symbol    string `json:"symbol"`
	Name      string `json:"name"`
	Issuer    string `json:"issuer"`
	Category  string `json:"category"`
	Leveraged bool   `json:"leveraged"`
	Note      string `json:"note"`
}

type listETFsOutput struct {
	Count      int      `json:"count"`
	Categories []string `json:"categories"`
	ETFs       []etfRow `json:"etfs"`
}

func registerListETFs(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_etfs",
		Description: "Lists the built-in universe of about 100 widely held US-listed ETFs, optionally filtered by category, issuer or free text. Needs no network. Leveraged and inverse funds are hidden unless include_leveraged is true or category is 'Leveraged / Inverse'. Start here to find symbols for the other tools; the response also lists every category name.",
		Annotations: readOnly("List ETFs"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listETFsInput) (*mcp.CallToolResult, listETFsOutput, error) {
		out, err := listETFs(in)
		return nil, out, err
	})
}

// listETFs filters the universe. A category or issuer that does not exist
// is an error listing the valid names, so a typo is corrected on the next
// call instead of silently matching nothing.
func listETFs(in listETFsInput) (listETFsOutput, error) {
	categories := universe.Categories()
	category := strings.TrimSpace(in.Category)
	if category != "" && !containsFold(categories, category) {
		return listETFsOutput{}, fmt.Errorf("unknown category %q; valid categories: %s", in.Category, strings.Join(categories, ", "))
	}
	issuer := strings.TrimSpace(in.Issuer)
	if known := issuers(); issuer != "" && !containsFold(known, issuer) {
		return listETFsOutput{}, fmt.Errorf("unknown issuer %q; valid issuers: %s", in.Issuer, strings.Join(known, ", "))
	}

	etfs := universe.Filter(universe.Query{
		Category:         category,
		Issuer:           issuer,
		Text:             strings.TrimSpace(in.Query),
		IncludeLeveraged: in.IncludeLeveraged || strings.EqualFold(category, leveragedCategory),
	})
	rows := make([]etfRow, 0, len(etfs))
	for _, e := range etfs {
		rows = append(rows, toRow(e))
	}
	return listETFsOutput{Count: len(rows), Categories: categories, ETFs: rows}, nil
}

// issuers returns the distinct issuers in order of first appearance,
// leveraged funds included.
func issuers() []string {
	var out []string
	seen := make(map[string]bool)
	for _, e := range universe.All() {
		if !seen[e.Issuer] {
			seen[e.Issuer] = true
			out = append(out, e.Issuer)
		}
	}
	return out
}

// containsFold reports whether list holds s, ignoring case.
func containsFold(list []string, s string) bool {
	for _, item := range list {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

// toRow converts a universe entry to its wire form.
func toRow(e universe.ETF) etfRow {
	return etfRow{
		Symbol:    e.Symbol,
		Name:      e.Name,
		Issuer:    e.Issuer,
		Category:  e.Category,
		Leveraged: e.Leveraged,
		Note:      e.Note,
	}
}
