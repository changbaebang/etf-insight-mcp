package tools

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGetQuote(t *testing.T) {
	sess, _, fund := newDataSession(t)

	t.Run("request order and missing symbols", func(t *testing.T) {
		var out getQuoteOutput
		callOK(t, sess, "get_quote", map[string]any{"symbols": []string{"schd", " voo", "NOPE", "VOO"}}, &out)
		if out.Count != 2 || len(out.Quotes) != 2 || out.Quotes[0].Symbol != "SCHD" || out.Quotes[1].Symbol != "VOO" {
			t.Fatalf("quotes = %+v, want SCHD then VOO", out.Quotes)
		}
		if len(out.Missing) != 1 || out.Missing[0] != "NOPE" || len(out.Notes) != 1 {
			t.Errorf("missing/notes = %v/%v, want [NOPE] with a hint", out.Missing, out.Notes)
		}
		want := quoteRow{
			Symbol: "VOO", Name: "Vanguard S&P 500 ETF", Price: 435.12, Change: 1.23, ChangePct: 0.28, PreviousClose: 433.89,
			Open: 434, DayLow: 432.5, DayHigh: 436.25, Volume: 4567890, FiftyTwoWeekLow: 340.1, FiftyTwoWeekHigh: 440.9,
			FiftyDayAverage: 420.56, TwoHundredDayAverage: 400.44, DividendYieldPct: 1.36, MarketState: "REGULAR",
			Currency: "USD", Exchange: "NYSEArca", QuoteTime: "2024-01-02T21:00:00Z",
		}
		if out.Quotes[1] != want {
			t.Errorf("VOO = %+v\nwant %+v", out.Quotes[1], want)
		}
	})

	t.Run("one call for the batch", func(t *testing.T) {
		before := fund.quoteCalls
		var out getQuoteOutput
		callOK(t, sess, "get_quote", map[string]any{"symbols": []string{"SPY", "QQQ", "^vix"}}, &out)
		if fund.quoteCalls != before+1 || out.Count != 3 || len(out.Missing) != 0 || out.Missing == nil {
			t.Errorf("calls %d -> %d, count %d, missing %v; want one call, 3 quotes, empty missing", before, fund.quoteCalls, out.Count, out.Missing)
		}
	})

	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("S%02d", i)
	}
	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "all unknown", args: map[string]any{"symbols": []string{"NOPE", "XYZ"}}, want: "no quote found for NOPE, XYZ"},
		{name: "too many", args: map[string]any{"symbols": tooMany}, want: "51 distinct tickers, at most 50"},
		{name: "empty list", args: map[string]any{"symbols": []string{}}, want: "symbols"},
		{name: "blank symbols", args: map[string]any{"symbols": []string{" ", ""}}, want: "symbols is required"},
		{name: "missing", args: map[string]any{}, want: "symbols"},
		{name: "upstream failure", args: map[string]any{"symbols": []string{"BOOM"}}, want: "fetching quotes failed: connection reset by peer"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_quote", tt.args, tt.want)
		})
	}
}

// testClock is a settable clock shared by a test and the server goroutines.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newQuoteCacheSession serves the data fakes with the fund source behind a
// real cache.FundStore on clock, so quotes are memoized and can go stale.
func newQuoteCacheSession(t *testing.T, clock *testClock) (*mcp.ClientSession, *dataFakeFund) {
	t.Helper()
	fund := newDataFakeFund()
	deps := dataDeps(newDataFakeSource(), fund)
	deps.Fund = cache.NewFundStore(fund, t.TempDir(), cache.WithFundClock(clock.now))
	return newSession(t, deps), fund
}

func TestGetQuoteStaleAndFailed(t *testing.T) {
	clock := &testClock{t: now}
	sess, fund := newQuoteCacheSession(t, clock)
	callRaw(t, sess, "get_quote", map[string]any{"symbols": []string{"SPY"}})

	// Six hours later the provider fails: SPY is served from memory and
	// must say so; QQQ was never quoted, so it failed rather than being
	// unknown.
	fund.failQuotes(errDataFakeNetwork)
	clock.advance(6 * time.Hour)
	out := callRaw(t, sess, "get_quote", map[string]any{"symbols": []string{"SPY", "QQQ"}})
	if missing := rawStrings(out["missing"]); len(missing) != 0 {
		t.Errorf("missing = %v, want empty: QQQ is not unknown, its fetch failed", missing)
	}
	if failed := rawStrings(out["failed"]); len(failed) != 1 || failed[0] != "QQQ" {
		t.Errorf("failed = %v, want [QQQ]", failed)
	}
	if hasDataNote(rawStrings(out["notes"]), "check the tickers") {
		t.Errorf("notes = %v: a failed fetch must not be presented as an unknown ticker", out["notes"])
	}
	warnings := rawStrings(out["warnings"])
	if !hasDataNote(warnings, "SPY") || !hasDataNote(warnings, "6 hours old") || !hasDataNote(warnings, "QQQ") {
		t.Errorf("warnings = %v, want SPY stale by 6 hours and QQQ failed", warnings)
	}
	quotes, _ := out["quotes"].([]any)
	if len(quotes) != 1 || quotes[0].(map[string]any)["stale"] != true {
		t.Errorf("quotes = %v, want the SPY quote marked stale", quotes)
	}

	// A stale quote alone still carries its age.
	only := callRaw(t, sess, "get_quote", map[string]any{"symbols": []string{"SPY"}})
	if w := rawStrings(only["warnings"]); !hasDataNote(w, "SPY") || !hasDataNote(w, "6 hours old") {
		t.Errorf("warnings for a lone stale quote = %v", w)
	}

	// Nothing in memory and the provider down: a tool error, not "unknown".
	callErr(t, sess, "get_quote", map[string]any{"symbols": []string{"QQQ"}}, "fetching quotes failed: connection reset by peer")

	// Once the provider answers again, nothing is stale.
	fund.failQuotes(nil)
	healed := callRaw(t, sess, "get_quote", map[string]any{"symbols": []string{"SPY", "QQQ"}})
	if w := rawStrings(healed["warnings"]); len(w) != 0 {
		t.Errorf("warnings after recovery = %v, want none", w)
	}
}

func TestDescribeAge(t *testing.T) {
	tests := []struct {
		age  time.Duration
		want string
	}{
		{age: 16 * time.Minute, want: "16 minutes"},
		{age: 61 * time.Second, want: "1 minute"},
		{age: 6*time.Hour + 20*time.Minute, want: "6 hours"},
		{age: 75 * time.Hour, want: "3 days"},
	}
	for _, tt := range tests {
		if got := describeAge(tt.age); got != tt.want {
			t.Errorf("describeAge(%v) = %q, want %q", tt.age, got, tt.want)
		}
	}
}
