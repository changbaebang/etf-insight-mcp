package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// registerAnalysis adds the analysis tool group: technical indicators, comparisons, screening and alternatives.
// The group is filled in by round 2; until then it registers nothing.
func (d Deps) registerAnalysis(s *mcp.Server) {
	_ = s
}
