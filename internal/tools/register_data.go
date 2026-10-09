package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// registerData adds the data tool group: fund data, quotes, dividends, splits, search, news and the market overview.
// The group is filled in by round 2; until then it registers nothing.
func (d Deps) registerData(s *mcp.Server) {
	_ = s
}
