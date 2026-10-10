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
