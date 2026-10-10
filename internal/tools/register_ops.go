package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// registerOps adds the ops tool group: cache status, cache clearing and
// forced refreshes. Every tool answers with a "cache not configured" tool
// error when Deps.Cache is nil.
func (d Deps) registerOps(s *mcp.Server) {
	d.registerCacheStatus(s)
	d.registerClearCache(s)
	d.registerRefreshPrices(s)
}
