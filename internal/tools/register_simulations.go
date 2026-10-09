package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// registerSimulations adds the simulations tool group: lump-sum, rolling-window and plan-review simulations.
// The group is filled in by round 2; until then it registers nothing.
func (d Deps) registerSimulations(s *mcp.Server) {
	_ = s
}
