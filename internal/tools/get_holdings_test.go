package tools

import "testing"

func TestGetHoldings(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("equity fund", func(t *testing.T) {
		var out getHoldingsOutput
		callOK(t, sess, "get_holdings", map[string]any{"symbol": "schd"}, &out)
		if out.Symbol != "SCHD" || len(out.TopHoldings) != 3 {
			t.Fatalf("out = %+v", out)
		}
		if h := out.TopHoldings[0]; h != (holdingRow{Symbol: "AVGO", Name: "Broadcom Inc", WeightPct: 4.51}) {
			t.Errorf("first holding = %+v", h)
		}
		if out.TopHoldingsWeight != 9.63 {
			t.Errorf("top_holdings_weight_pct = %v, want 9.63", out.TopHoldingsWeight)
		}
		if len(out.SectorWeights) != 2 || out.SectorWeights[0] != (sectorRow{Sector: "financial_services", WeightPct: 19.23}) || out.SectorWeights[1].WeightPct != 0 {
			t.Errorf("sectors = %+v", out.SectorWeights)
		}
		mix := out.AssetMix
		if mix.StocksPct == nil || *mix.StocksPct != 98.99 || mix.BondsPct == nil || *mix.BondsPct != 0 || mix.CashPct == nil || *mix.CashPct != 1.01 || mix.OtherPct != nil {
			t.Errorf("asset mix = %+v", mix)
		}
		if len(out.EquityStats) != 2 || out.EquityStats["price_to_earnings"] != 0.055 || out.EquityStats["median_market_cap"] != 123456.7891 {
			t.Errorf("equity stats = %v", out.EquityStats)
		}
		if out.BondRatings != nil || out.BondStats != nil {
			t.Errorf("bond blocks = %v/%v, want absent", out.BondRatings, out.BondStats)
		}
	})

	t.Run("bond fund", func(t *testing.T) {
		var out getHoldingsOutput
		callOK(t, sess, "get_holdings", map[string]any{"symbol": "BND"}, &out)
		if len(out.BondRatings) != 2 || out.BondRatings[0] != (ratingRow{Rating: "us_government", WeightPct: 48.12}) {
			t.Errorf("bond ratings = %+v", out.BondRatings)
		}
		if out.BondStats["duration"] != 6.05 || out.EquityStats != nil || out.SectorWeights == nil || len(out.SectorWeights) != 0 {
			t.Errorf("bond stats/equity stats/sectors = %v/%v/%v", out.BondStats, out.EquityStats, out.SectorWeights)
		}
		if out.AssetMix.BondsPct == nil || *out.AssetMix.BondsPct != 99.5 || out.AssetMix.StocksPct != nil {
			t.Errorf("asset mix = %+v", out.AssetMix)
		}
	})

	t.Run("long lists are capped", func(t *testing.T) {
		var out getHoldingsOutput
		callOK(t, sess, "get_holdings", map[string]any{"symbol": "BIGF"}, &out)
		if len(out.TopHoldings) != maxHoldingRows || out.TopHoldingsWeight != 30 || !hasDataNote(out.Notes, "30 top holdings reported, the largest 25") {
			t.Errorf("rows/weight/notes = %d/%v/%v", len(out.TopHoldings), out.TopHoldingsWeight, out.Notes)
		}
	})

	t.Run("unknown symbol", func(t *testing.T) {
		callErr(t, sess, "get_holdings", map[string]any{"symbol": "NOPE"}, "unknown symbol NOPE")
	})
	t.Run("upstream failure", func(t *testing.T) {
		callErr(t, sess, "get_holdings", map[string]any{"symbol": "BOOM"}, "fetching holdings for BOOM failed")
	})
}
