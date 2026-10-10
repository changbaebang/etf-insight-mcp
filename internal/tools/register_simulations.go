package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// registerSimulations adds the round-2 simulation tools: lump sum versus
// DCA, rolling start dates and the plan review. simulate_dca,
// simulate_portfolio_dca and forecast_dca are registered by Register.
func (d Deps) registerSimulations(s *mcp.Server) {
	d.registerSimulateLumpSumVsDCA(s)
	d.registerSimulateRollingDCA(s)
	d.registerReviewDCAPlan(s)
}
