package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestGetETFInfo(t *testing.T) {
	src := newFakeSource()
	sess := newSession(t, testDeps(src))
	spy := src.series["SPY"]
	last, _ := spy.Last()

	t.Run("known symbol", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": " spy "}, &out)
		if out.Symbol != "SPY" || !out.Known || out.Universe == nil || out.Universe.Category != "US Large Cap" {
			t.Errorf("symbol/known/universe = %q/%v/%+v", out.Symbol, out.Known, out.Universe)
		}
		if out.Meta.Currency != "USD" || out.Meta.InstrumentType != "ETF" || out.Meta.FirstTradeDate != "2021-01-04" {
			t.Errorf("meta = %+v", out.Meta)
		}
		if out.Data.Bars != spy.Len() || out.Data.FirstDate != "2021-01-04" || out.Data.LastDate != last.Date.Format(market.DateLayout) {
			t.Errorf("data = %+v, want %d bars 2021-01-04..%s", out.Data, spy.Len(), last.Date.Format(market.DateLayout))
		}
		if out.Summary.AsOf != out.Data.LastDate || out.Summary.Close != round2(last.Close) {
			t.Errorf("summary as_of/close = %s/%v, want %s/%v", out.Summary.AsOf, out.Summary.Close, out.Data.LastDate, round2(last.Close))
		}
		labels := make([]string, 0, len(out.Summary.Windows))
		for _, w := range out.Summary.Windows {
			labels = append(labels, w.Label)
		}
		if got, want := strings.Join(labels, ","), "1m,3m,6m,1y,3y,5y,10y,max"; got != want {
			t.Errorf("window labels = %s, want %s", got, want)
		}
		if w := out.Summary.Windows[4]; w.Label != "3y" || w.Available {
			t.Errorf("3y window = %+v, want unavailable (series is just under 3 years)", w)
		}
		if w := out.Summary.Windows[7]; !w.Available || w.From != "2021-01-04" {
			t.Errorf("max window = %+v, want available from 2021-01-04", w)
		}
		if out.Summary.TTMDividendPerShare != 6 { // four quarterly 1.5 dividends in the trailing year
			t.Errorf("ttm dividend per share = %v, want 6", out.Summary.TTMDividendPerShare)
		}
		if out.Summary.Volatility1YPct <= 0 || out.Summary.MaxDrawdownAllPct <= 0 || out.Summary.High52W <= out.Summary.Low52W {
			t.Errorf("summary statistics look wrong: %+v", out.Summary)
		}
		switch out.Trend.State {
		case analytics.StateUptrend, analytics.StateDowntrend, analytics.StateSideways:
		default:
			t.Errorf("trend state = %q with %d bars", out.Trend.State, out.Data.Bars)
		}
		if out.Trend.SMA200 <= 0 || len(out.Trend.Reasons) == 0 {
			t.Errorf("trend = %+v", out.Trend)
		}
		if len(out.Warnings) != 0 {
			t.Errorf("warnings = %v, want none (data is 5 days old)", out.Warnings)
		}
	})

	t.Run("symbol outside the universe", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "KRW=X"}, &out)
		if out.Known || out.Universe != nil || out.Meta.Currency != "KRW" {
			t.Errorf("known/universe/currency = %v/%+v/%s", out.Known, out.Universe, out.Meta.Currency)
		}
	})

	t.Run("as_of anchors the snapshot", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "SPY", "as_of": "2022-06-18"}, &out) // a Saturday
		if out.Summary.AsOf != "2022-06-17" || out.Trend.AsOf != "2022-06-17" {
			t.Errorf("as_of = %s/%s, want 2022-06-17", out.Summary.AsOf, out.Trend.AsOf)
		}
		if out.Data.Bars != spy.Len() {
			t.Errorf("data.bars = %d, want the whole series %d", out.Data.Bars, spy.Len())
		}
	})

	t.Run("old data is flagged", func(t *testing.T) {
		deps := testDeps(src)
		deps.Now = func() time.Time { return now.AddDate(0, 1, 0) }
		var out getETFInfoOutput
		callOK(t, newSession(t, deps), "get_etf_info", map[string]any{"symbol": "SPY"}, &out)
		if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "days old") {
			t.Errorf("warnings = %v, want one 'days old' warning", out.Warnings)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown symbol", args: map[string]any{"symbol": "XYZ"}, want: "unknown symbol XYZ: not in universe and the data source returned not found; use list_etfs"},
		{name: "universe symbol missing upstream", args: map[string]any{"symbol": "VTI"}, want: "in the universe but the data source returned not found"},
		{name: "empty symbol", args: map[string]any{"symbol": " "}, want: "symbol is required"},
		{name: "missing symbol", args: map[string]any{}, want: "symbol"},
		{name: "bad date", args: map[string]any{"symbol": "SPY", "as_of": "June 2022"}, want: "YYYY-MM-DD"},
		{name: "before history", args: map[string]any{"symbol": "SPY", "as_of": "2020-01-01"}, want: "no bar on or before 2020-01-01"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_etf_info", tt.args, tt.want)
		})
	}
}

// warningWith returns the first warning containing want, or "".
func warningWith(warnings []string, want string) string {
	for _, w := range warnings {
		if strings.Contains(w, want) {
			return w
		}
	}
	return ""
}

func TestGetETFInfoFlagsWhatTheFiguresMean(t *testing.T) {
	src := newFakeSource()
	flat := func(int) float64 { return 50 }
	// About seven months of history with a dividend every month.
	src.add(synthetic("YNG", "USD", "ETF", time.Date(2023, time.June, 1, 0, 0, 0, 0, time.UTC), 150, flat, 21, 0.4))
	src.add(synthetic("^GSPC", "USD", "INDEX", seriesStart, 780, func(i int) float64 { return 3700 + float64(i) }, 0, 0))
	sess := newSession(t, testDeps(src))

	t.Run("young fund", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "YNG"}, &out)
		w := warningWith(out.Warnings, "less than 52 weeks")
		if w == "" {
			t.Fatalf("warnings = %q, want one saying the trailing-year figures cover less than 52 weeks", out.Warnings)
		}
		for _, field := range []string{"ttm_dividend_yield_pct", "volatility_1y_pct", "max_drawdown_1y_pct"} {
			if !strings.Contains(w, field) {
				t.Errorf("warning %q does not name %s", w, field)
			}
		}
	})

	t.Run("young as of an early date", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "SPY", "as_of": "2021-09-01"}, &out)
		if warningWith(out.Warnings, "less than 52 weeks") == "" {
			t.Errorf("warnings = %q, want the short-history warning", out.Warnings)
		}
	})

	t.Run("a full year is not flagged", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "SPY", "as_of": "2022-06-01"}, &out)
		if len(out.Warnings) != 0 {
			t.Errorf("warnings = %q, want none", out.Warnings)
		}
	})

	t.Run("index", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "^gspc"}, &out)
		if warningWith(out.Warnings, "is an index") == "" {
			t.Errorf("warnings = %q, want the index caution", out.Warnings)
		}
	})

	t.Run("as_of after the latest bar", func(t *testing.T) {
		var out getETFInfoOutput
		callOK(t, sess, "get_etf_info", map[string]any{"symbol": "SPY", "as_of": "2030-01-01"}, &out)
		if out.Summary.AsOf != out.Data.LastDate {
			t.Errorf("summary.as_of = %s, want the latest bar %s", out.Summary.AsOf, out.Data.LastDate)
		}
		if warningWith(out.Warnings, "after the latest bar") == "" {
			t.Errorf("warnings = %q, want one saying as_of is after the latest bar", out.Warnings)
		}
	})

	t.Run("old data is flagged when as_of is in the future", func(t *testing.T) {
		deps := testDeps(src)
		deps.Now = func() time.Time { return now.AddDate(0, 1, 0) }
		var out getETFInfoOutput
		callOK(t, newSession(t, deps), "get_etf_info", map[string]any{"symbol": "SPY", "as_of": "2030-01-01"}, &out)
		if warningWith(out.Warnings, "days old") == "" {
			t.Errorf("warnings = %q, want a 'days old' warning", out.Warnings)
		}
	})
}
