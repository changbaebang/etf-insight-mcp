package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestHasTrailingYear(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	s := synthetic("YR", "USD", "ETF", start, 300, func(int) float64 { return 100 }, 0, 0) // weekdays to about mid-Feb 2025
	last, _ := s.Last()
	tests := []struct {
		name string
		asOf time.Time
		want bool
	}{
		{"latest bar is more than 52 weeks after the first", time.Time{}, true},
		{"exactly 364 days after the first bar", start.AddDate(0, 0, 364), true},
		{"one day short", start.AddDate(0, 0, 363), false},
		{"before the first bar", start.AddDate(0, 0, -1), false},
		{"after the last bar uses the last bar", last.Date.AddDate(1, 0, 0), true},
	}
	for _, tt := range tests {
		if got := hasTrailingYear(s, tt.asOf); got != tt.want {
			t.Errorf("%s: hasTrailingYear = %v, want %v", tt.name, got, tt.want)
		}
	}
	if hasTrailingYear(&market.Series{Meta: market.Meta{Symbol: "EMPTY"}}, time.Time{}) {
		t.Error("an empty series has no trailing year")
	}
}

func TestRequireUSD(t *testing.T) {
	for cur, wantErr := range map[string]bool{"USD": false, "usd": false, "": false, "KRW": true, "GBp": true} {
		err := requireUSD(&market.Series{Meta: market.Meta{Symbol: "X", Currency: cur}})
		if (err != nil) != wantErr {
			t.Errorf("currency %q: err = %v, want error %v", cur, err, wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "USD only") {
			t.Errorf("currency %q: error %q should say USD only", cur, err)
		}
	}
}

func TestInstrumentNote(t *testing.T) {
	for kind, want := range map[string]string{"INDEX": "is an index", "CURRENCY": "exchange rate", "ETF": "", "": "", "MUTUALFUND": ""} {
		got := instrumentNote(&market.Series{Meta: market.Meta{Symbol: "X", InstrumentType: kind}})
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("instrument %q: note %q, want it to contain %q", kind, got, want)
		}
	}
}

func TestProvisionalNote(t *testing.T) {
	src := newFakeSource()
	voo := src.series["VOO"]
	last, _ := voo.Last()
	if got := provisionalNote(voo, time.Time{}); got != "" {
		t.Fatalf("settled series: note %q, want none", got)
	}
	voo.Meta.ProvisionalUntil = last.Date.Add(20 * time.Hour)
	voo.Meta.FetchedAt = last.Date.Add(15 * time.Hour)
	if got := provisionalNote(voo, time.Time{}); !strings.Contains(got, "VOO's latest bar") || !strings.Contains(got, "intraday price") {
		t.Errorf("intraday last bar: note %q", got)
	}
	if got := provisionalNote(voo, last.Date.AddDate(0, 0, -1)); got != "" {
		t.Errorf("figures that end before the last bar: note %q, want none", got)
	}
	if got := provisionalSummary(src.series, []string{"SPY", "VOO"}); !strings.Contains(got, "latest bars of VOO") || strings.Contains(got, "SPY") {
		t.Errorf("summary = %q, want VOO only", got)
	}
	if got := nonEmpty("", "a", ""); len(got) != 1 || got[0] != "a" {
		t.Errorf("nonEmpty = %q", got)
	}
}

func TestToolsFlagIntradayLastBar(t *testing.T) {
	src := newFakeSource()
	voo := src.series["VOO"]
	last, _ := voo.Last()
	voo.Meta.ProvisionalUntil = last.Date.Add(20 * time.Hour)
	voo.Meta.FetchedAt = last.Date.Add(15 * time.Hour)
	sess := newSession(t, testDeps(src))

	var info getETFInfoOutput
	callOK(t, sess, "get_etf_info", map[string]any{"symbol": "VOO"}, &info)
	if !strings.Contains(strings.Join(info.Warnings, "\n"), "intraday price") {
		t.Errorf("get_etf_info warnings = %q, want the intraday note", info.Warnings)
	}
	var sim simulateDCAOutput
	callOK(t, sess, "simulate_dca", map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "start": "2022-01-03", "compare_with": ""}, &sim)
	if !strings.Contains(strings.Join(sim.Notes, "\n"), "intraday price") {
		t.Errorf("simulate_dca notes = %q, want the intraday note", sim.Notes)
	}
	var hist getPriceHistoryOutput
	callOK(t, sess, "get_price_history", map[string]any{"symbol": "VOO", "end": "2022-06-30"}, &hist)
	if strings.Contains(strings.Join(hist.Warnings, "\n"), "intraday") {
		t.Errorf("history ending before the last bar: warnings %q, want no intraday note", hist.Warnings)
	}
}
