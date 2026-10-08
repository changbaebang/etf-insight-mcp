package tools

import (
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestGetPriceHistory(t *testing.T) {
	src := newFakeSource()
	sess := newSession(t, testDeps(src))
	spy := src.series["SPY"]
	q1 := spy.Between(date(t, "2022-01-01"), date(t, "2022-03-31"))

	t.Run("daily range", func(t *testing.T) {
		var out getPriceHistoryOutput
		callOK(t, sess, "get_price_history", map[string]any{"symbol": "spy", "interval": "daily", "start": "2022-01-01", "end": "2022-03-31"}, &out)
		if out.Symbol != "SPY" || out.Currency != "USD" || out.Interval != "daily" || out.Downsampled {
			t.Errorf("header = %+v", out)
		}
		if out.Count != len(q1) || len(out.Points) != len(q1) {
			t.Fatalf("count = %d (%d points), want %d", out.Count, len(out.Points), len(q1))
		}
		if out.From != "2022-01-03" || out.To != "2022-03-31" {
			t.Errorf("from/to = %s/%s, want 2022-01-03/2022-03-31", out.From, out.To)
		}
		if p := out.Points[0]; p.Close != round2(q1[0].Close) || p.AdjClose != round2(q1[0].AdjClose) {
			t.Errorf("first point = %+v, want close %v", p, round2(q1[0].Close))
		}
	})

	t.Run("daily downsampled", func(t *testing.T) {
		var out getPriceHistoryOutput
		callOK(t, sess, "get_price_history", map[string]any{"symbol": "SPY", "interval": "daily", "start": "2022-01-01", "end": "2022-03-31", "max_points": 20}, &out)
		if !out.Downsampled || out.Count > 20 || out.Count < 10 {
			t.Errorf("downsampled/count = %v/%d, want true and about 20", out.Downsampled, out.Count)
		}
		if out.Points[0].Date != "2022-01-03" || out.Points[len(out.Points)-1].Date != "2022-03-31" {
			t.Errorf("first/last = %s/%s, want 2022-01-03/2022-03-31", out.Points[0].Date, out.Points[len(out.Points)-1].Date)
		}
		for i := 1; i < len(out.Points); i++ {
			if out.Points[i].Date <= out.Points[i-1].Date {
				t.Errorf("points not ascending at %d: %s then %s", i, out.Points[i-1].Date, out.Points[i].Date)
			}
		}
	})

	t.Run("monthly is the default and keeps month ends", func(t *testing.T) {
		var out getPriceHistoryOutput
		callOK(t, sess, "get_price_history", map[string]any{"symbol": "SPY"}, &out)
		if out.Interval != "monthly" || out.Downsampled {
			t.Errorf("interval/downsampled = %s/%v, want monthly/false", out.Interval, out.Downsampled)
		}
		if want := 36; out.Count != want { // 2021-01 .. 2023-12
			t.Errorf("count = %d, want %d", out.Count, want)
		}
		for _, p := range out.Points {
			d := date(t, p.Date)
			idx, _ := spy.IndexOn(d)
			if idx+1 < spy.Len() && spy.Bars[idx+1].Date.Month() == d.Month() {
				t.Errorf("%s is not the last bar of its month", p.Date)
			}
		}
	})

	t.Run("weekly keeps the last bar of each ISO week", func(t *testing.T) {
		var out getPriceHistoryOutput
		callOK(t, sess, "get_price_history", map[string]any{"symbol": "SPY", "interval": "weekly", "start": "2023-01-01", "end": "2023-03-31"}, &out)
		if out.Count != 13 {
			t.Errorf("count = %d, want 13 weeks", out.Count)
		}
		for _, p := range out.Points {
			if wd := date(t, p.Date).Weekday(); wd != time.Friday {
				t.Errorf("%s is a %s, want Friday", p.Date, wd)
			}
		}
	})

	t.Run("dividends are carried", func(t *testing.T) {
		var out getPriceHistoryOutput
		callOK(t, sess, "get_price_history", map[string]any{"symbol": "SPY", "interval": "daily", "max_points": 2000}, &out)
		paid := 0.0
		for _, p := range out.Points {
			paid += p.Dividend
		}
		want := 0.0
		for _, b := range spy.Bars {
			want += b.Dividend
		}
		if paid != round4(want) {
			t.Errorf("dividends sum to %v, want %v", paid, want)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "bad interval", args: map[string]any{"symbol": "SPY", "interval": "hourly"}, want: "use daily, weekly or monthly"},
		{name: "too many points", args: map[string]any{"symbol": "SPY", "max_points": 5000}, want: "between 1 and 2000"},
		{name: "negative points", args: map[string]any{"symbol": "SPY", "max_points": -1}, want: "between 1 and 2000"},
		{name: "end before start", args: map[string]any{"symbol": "SPY", "start": "2022-03-01", "end": "2022-01-01"}, want: "is before start"},
		{name: "bad start", args: map[string]any{"symbol": "SPY", "start": "01/02/2022"}, want: "YYYY-MM-DD"},
		{name: "empty range", args: map[string]any{"symbol": "SPY", "start": "2030-01-01"}, want: "no bars for SPY"},
		{name: "unknown symbol", args: map[string]any{"symbol": "NOPE"}, want: "use list_etfs"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_price_history", tt.args, tt.want)
		})
	}
}

func TestDownsample(t *testing.T) {
	bars := func(n int) []market.Bar {
		out := make([]market.Bar, n)
		for i := range out {
			out[i].Close = float64(i)
		}
		return out
	}
	tests := []struct {
		n, limit  int
		wantCount int
		wantDown  bool
	}{
		{n: 10, limit: 10, wantCount: 10},
		{n: 10, limit: 300, wantCount: 10},
		{n: 600, limit: 300, wantCount: 201, wantDown: true},
		{n: 301, limit: 300, wantCount: 151, wantDown: true},
		{n: 2001, limit: 2000, wantCount: 1001, wantDown: true},
		{n: 10, limit: 1, wantCount: 1, wantDown: true},
		{n: 10, limit: 2, wantCount: 2, wantDown: true},
	}
	for _, tt := range tests {
		got, down := downsample(bars(tt.n), tt.limit)
		if len(got) != tt.wantCount || down != tt.wantDown {
			t.Errorf("downsample(n=%d, limit=%d) = %d bars, %v; want %d, %v", tt.n, tt.limit, len(got), down, tt.wantCount, tt.wantDown)
		}
		if len(got) > tt.limit {
			t.Errorf("downsample(n=%d, limit=%d) returned %d > limit", tt.n, tt.limit, len(got))
		}
		if last := got[len(got)-1].Close; last != float64(tt.n-1) {
			t.Errorf("downsample(n=%d, limit=%d) last = %v, want %d", tt.n, tt.limit, last, tt.n-1)
		}
	}
}
