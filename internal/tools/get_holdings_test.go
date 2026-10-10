package tools

import (
	"strings"
	"testing"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

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
		if out.TopHoldingsWeight == nil || *out.TopHoldingsWeight != 9.63 {
			t.Errorf("top_holdings_weight_pct = %v, want 9.63", out.TopHoldingsWeight)
		}
		// 19.23% of the 98.99% stock part is 19.04% of the fund.
		if len(out.SectorWeights) != 2 || out.SectorWeights[0] != (sectorRow{Sector: "financial_services", WeightPct: 19.04}) || out.SectorWeights[1].WeightPct != 0 {
			t.Errorf("sectors = %+v", out.SectorWeights)
		}
		mix := out.AssetMix
		if mix.StocksPct == nil || *mix.StocksPct != 98.99 || mix.BondsPct == nil || *mix.BondsPct != 0 || mix.CashPct == nil || *mix.CashPct != 1.01 || mix.OtherPct != nil {
			t.Errorf("asset mix = %+v", mix)
		}
		if len(out.EquityStats) != 2 || out.EquityStats["price_to_earnings"] != 18.17 || out.EquityStats["median_market_cap"] != 123456.7891 {
			t.Errorf("equity stats = %v", out.EquityStats)
		}
		if out.BondRatings != nil || out.USGovernmentPct != nil {
			t.Errorf("bond blocks = %v/%v, want absent", out.BondRatings, out.USGovernmentPct)
		}
	})

	t.Run("bond fund", func(t *testing.T) {
		var out getHoldingsOutput
		callOK(t, sess, "get_holdings", map[string]any{"symbol": "BND"}, &out)
		if len(out.BondRatings) != 1 || out.BondRatings[0] != (ratingRow{Rating: "aaa", WeightPct: 3.11}) || out.USGovernmentPct == nil || *out.USGovernmentPct != 48.12 {
			t.Errorf("bond ratings/us government = %+v/%v", out.BondRatings, out.USGovernmentPct)
		}
		if out.EquityStats != nil || out.SectorWeights == nil || len(out.SectorWeights) != 0 {
			t.Errorf("equity stats/sectors = %v/%v", out.EquityStats, out.SectorWeights)
		}
		if out.AssetMix.BondsPct == nil || *out.AssetMix.BondsPct != 99.5 || out.AssetMix.StocksPct != nil {
			t.Errorf("asset mix = %+v", out.AssetMix)
		}
	})

	t.Run("long lists are capped", func(t *testing.T) {
		var out getHoldingsOutput
		callOK(t, sess, "get_holdings", map[string]any{"symbol": "BIGF"}, &out)
		if len(out.TopHoldings) != maxHoldingRows || out.TopHoldingsWeight == nil || *out.TopHoldingsWeight != 30 || !hasDataNote(out.Notes, "30 top holdings reported, the largest 25") {
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

// TestGetHoldingsReviewFixes pins what the review asked for: no provider
// bond duration or maturity, the us_government bucket apart from the
// letter grades, sector weights that describe the whole fund, price_to_*
// as multiples, null concentration without holdings, and caveats for
// stale portfolios and leveraged funds.
func TestGetHoldingsReviewFixes(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("bond fund", func(t *testing.T) {
		out := callRaw(t, sess, "get_holdings", map[string]any{"symbol": "BND"})
		notes := rawStrings(out["notes"])
		if _, ok := out["bond_stats"]; ok || !hasDataNote(notes, "duration and maturity") {
			t.Errorf("bond_stats = %v, notes = %v; want the provider's duration and maturity left out with a note", out["bond_stats"], notes)
		}
		ratings, _ := out["bond_ratings"].([]any)
		for _, r := range ratings {
			if r.(map[string]any)["rating"] == "us_government" {
				t.Errorf("bond_ratings = %v, want us_government reported apart from the letter grades", ratings)
			}
		}
		if len(ratings) != 1 || out["us_government_pct"] != 48.12 {
			t.Errorf("bond_ratings/us_government_pct = %v/%v, want [aaa] and 48.12", ratings, out["us_government_pct"])
		}
		if out["top_holdings_weight_pct"] != nil || !hasDataNote(notes, "lists no top holdings") {
			t.Errorf("top_holdings_weight_pct = %v, notes = %v; want null and a note", out["top_holdings_weight_pct"], notes)
		}
		if !hasDataNote(notes, "month-end") {
			t.Errorf("notes = %v, want the portfolio date caution", notes)
		}
	})

	t.Run("bond fund with a stray equity sector split", func(t *testing.T) {
		out := callRaw(t, sess, "get_holdings", map[string]any{"symbol": "HYGF"})
		if sectors, _ := out["sector_weights"].([]any); len(sectors) != 0 {
			t.Errorf("sector_weights = %v, want none: stocks are 0%% of the fund", sectors)
		}
		if !hasDataNote(rawStrings(out["notes"]), "sector weights are omitted") {
			t.Errorf("notes = %v, want why the sector weights are left out", out["notes"])
		}
	})

	t.Run("balanced fund", func(t *testing.T) {
		out := callRaw(t, sess, "get_holdings", map[string]any{"symbol": "BALF"})
		sectors, _ := out["sector_weights"].([]any)
		if len(sectors) != 2 || sectors[0].(map[string]any)["weight_pct"] != float64(30) {
			t.Errorf("sector_weights = %v, want each half of the 60%% stock part as 30%% of the fund", sectors)
		}
		if !hasDataNote(rawStrings(out["notes"]), "scaled") {
			t.Errorf("notes = %v, want the scaling explained", out["notes"])
		}
	})

	t.Run("price ratios are multiples", func(t *testing.T) {
		out := callRaw(t, sess, "get_holdings", map[string]any{"symbol": "SCHD"})
		stats, _ := out["equity_stats"].(map[string]any)
		if stats["price_to_earnings"] != 18.17 || stats["median_market_cap"] != 123456.7891 {
			t.Errorf("equity_stats = %v, want P/E 18.17 (1 / 0.05503) and other statistics unchanged", stats)
		}
	})

	t.Run("leveraged fund", func(t *testing.T) {
		out := callRaw(t, sess, "get_holdings", map[string]any{"symbol": "TQQQ"})
		notes := rawStrings(out["notes"])
		if !hasDataNote(notes, "swaps") {
			t.Errorf("notes = %v, want the derivatives caveat for a leveraged fund", notes)
		}
		stats, _ := out["equity_stats"].(map[string]any)
		if _, ok := stats["price_to_earnings"]; ok || stats["price_to_book"] != 8.9 {
			t.Errorf("equity_stats = %v, want a zero P/E reciprocal dropped and P/B 8.9", stats)
		}
		if _, ok := out["bond_ratings"]; ok || out["us_government_pct"] != 35.02 {
			t.Errorf("bond_ratings/us_government_pct = %v/%v, want no all-zero letter grades and 35.02", out["bond_ratings"], out["us_government_pct"])
		}
	})

	t.Run("schema", func(t *testing.T) {
		if d := schemaDescription(t, sess, "get_holdings", "asset_mix", "other_pct"); !strings.Contains(d, "preferred") {
			t.Errorf("other_pct description = %q, want preferred and convertible positions named", d)
		}
	})
}

func TestSectorWeightsWithoutStockShare(t *testing.T) {
	rows, notes := sectorWeights([]market.Weight{{Name: "technology", Weight: 0.4}}, nil)
	if len(rows) != 1 || rows[0].WeightPct != 40 || len(notes) != 1 || !strings.Contains(notes[0], "stock part of the fund only") {
		t.Errorf("rows/notes = %v/%v, want the reported split kept and explained", rows, notes)
	}
	if rows, notes := sectorWeights(nil, ptr(0.6)); rows == nil || len(rows) != 0 || notes != nil {
		t.Errorf("no sectors = %v/%v, want an empty list and no note", rows, notes)
	}
}
