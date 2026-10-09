package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// registerOps adds the ops tool group: cache status, cache clearing and forced refreshes.
// The group is filled in by round 2; until then it registers nothing.
func (d Deps) registerOps(s *mcp.Server) {
	_ = s
}
