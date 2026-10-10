package tools

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// TestInceptionWarnings: a fund whose price history starts well before its
// inception date (a predecessor product, as with SMH) is flagged by
// get_etf_info and review_dca_plan; a matching date and a failing fund
// source stay silent.
func TestInceptionWarnings(t *testing.T) {
	src := newFakeSource() // VOO history starts 2021-01-04
	later := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		fund simFundSource
		want bool
	}{
		{"history long before inception", simFundSource{profiles: map[string]*market.FundProfile{"VOO": {Symbol: "VOO", InceptionDate: later}}}, true},
		{"inception matches the history", simFundSource{profiles: map[string]*market.FundProfile{"VOO": {Symbol: "VOO", InceptionDate: time.Date(2021, 1, 4, 0, 0, 0, 0, time.UTC)}}}, false},
		{"fund source fails", simFundSource{err: errors.New("boom")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := testDeps(src)
			deps.Fund = tc.fund
			sess := newSession(t, deps)

			var info getETFInfoOutput
			callOK(t, sess, "get_etf_info", map[string]any{"symbol": "VOO"}, &info)
			got := strings.Contains(strings.Join(info.Warnings, "\n"), "inception date")
			if got != tc.want {
				t.Errorf("get_etf_info warnings = %q, want inception note %v", info.Warnings, tc.want)
			}
			if tc.want && !strings.Contains(strings.Join(info.Warnings, "\n"), "start simulations on or after 2022-06-01") {
				t.Errorf("get_etf_info warnings = %q, want the suggested start date", info.Warnings)
			}

			var review reviewDCAPlanOutput
			callOK(t, sess, "review_dca_plan", map[string]any{"symbol": "VOO", "amount": 100, "cadence": "monthly", "horizon_years": 1, "short_horizon_months": 3}, &review)
			got = strings.Contains(strings.Join(review.Warnings, "\n"), "inception date")
			if got != tc.want {
				t.Errorf("review_dca_plan warnings = %q, want inception note %v", review.Warnings, tc.want)
			}
		})
	}
}
