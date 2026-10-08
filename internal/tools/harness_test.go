package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// now is the fixed clock every test server runs on: a few days after the
// synthetic series end, so freshness warnings stay quiet unless a test
// moves the clock on purpose.
var now = time.Date(2024, time.January, 3, 9, 0, 0, 0, time.UTC)

// seriesStart is the first bar of the long synthetic series (a Monday).
var seriesStart = time.Date(2021, time.January, 4, 0, 0, 0, 0, time.UTC)

// schdStart is the first bar of the short synthetic series (a Monday).
var schdStart = time.Date(2022, time.January, 3, 0, 0, 0, 0, time.UTC)

// fakeSource serves synthetic series from memory and answers every other
// symbol with market.ErrNotFound, like the Yahoo client does. It counts
// calls per symbol so a test can check that the cache path fetches once.
type fakeSource struct {
	mu     sync.Mutex
	series map[string]*market.Series
	calls  map[string]int
}

func (f *fakeSource) Series(_ context.Context, symbol string) (*market.Series, error) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	f.mu.Lock()
	f.calls[sym]++
	f.mu.Unlock()
	s, ok := f.series[sym]
	if !ok {
		return nil, fmt.Errorf("fake: %s: %w", sym, market.ErrNotFound)
	}
	return s, nil
}

// callCount returns how often symbol was requested.
func (f *fakeSource) callCount(symbol string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[symbol]
}

// newFakeSource builds three ETFs and the KRW=X rate. SPY and VOO span
// 2021-01-04 to 2023-12-29 (780 weekdays); SCHD starts a year later so
// portfolio tests see a moved start date. Prices drift upward with a sine
// wobble so trends, drawdowns and volatility are all non-trivial.
func newFakeSource() *fakeSource {
	f := &fakeSource{series: map[string]*market.Series{}, calls: map[string]int{}}
	wobble := func(base, drift, amp, period, phase float64) func(int) float64 {
		return func(i int) float64 {
			x := float64(i)
			return base * math.Exp(drift*x) * (1 + amp*math.Sin(x/period+phase))
		}
	}
	f.add(synthetic("SPY", "USD", "ETF", seriesStart, 780, wobble(370, 0.00025, 0.03, 40, 0), 63, 1.5))
	f.add(synthetic("VOO", "USD", "ETF", seriesStart, 780, wobble(340, 0.0003, 0.04, 37, 1), 63, 1.4))
	f.add(synthetic("SCHD", "USD", "ETF", schdStart, 520, wobble(70, 0.0002, 0.03, 30, 2), 63, 0.6))
	f.add(synthetic("KRW=X", "KRW", "CURRENCY", seriesStart, 780, func(i int) float64 {
		return 1100 + 0.15*float64(i) + 80*math.Sin(float64(i)/120)
	}, 0, 0))
	return f
}

func (f *fakeSource) add(s *market.Series) {
	f.series[s.Meta.Symbol] = s
}

// synthetic builds n weekday bars from start with Close = AdjClose =
// price(i) and a dividend every dividendEvery bars (0 = none).
func synthetic(symbol, currency, kind string, start time.Time, n int, price func(i int) float64, dividendEvery int, dividend float64) *market.Series {
	s := &market.Series{Meta: market.Meta{
		Symbol:         symbol,
		Name:           symbol + " synthetic",
		Currency:       currency,
		Exchange:       "TEST",
		InstrumentType: kind,
		FirstTradeDate: start,
	}}
	for day, i := start, 0; i < n; day = day.AddDate(0, 0, 1) {
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		p := price(i)
		b := market.Bar{Date: day, Close: p, AdjClose: p}
		if dividendEvery > 0 && i > 0 && i%dividendEvery == 0 {
			b.Dividend = dividend
		}
		s.Bars = append(s.Bars, b)
		i++
	}
	return s
}

// testDeps wraps src with the fixed clock.
func testDeps(src market.Source) Deps {
	return Deps{Source: src, Version: "test", Now: func() time.Time { return now }}
}

// newSession registers the tools on a fresh server and connects a client
// through an in-memory transport, the same path a Claude client takes
// over stdio.
func newSession(t *testing.T, deps Deps) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()

	s := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0"}, &mcp.ServerOptions{Instructions: Instructions})
	Register(s, deps)
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// call invokes a tool and returns the raw result; a protocol error fails
// the test.
func call(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// callOK invokes a tool, requires success and decodes the structured
// content into out.
func callOK(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res := call(t, sess, name, args)
	if res.IsError {
		t.Fatalf("%s(%v) returned tool error: %s", name, args, textOf(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s output: %v\n%s", name, err, raw)
	}
}

// callErr invokes a tool, requires a tool error and checks that its
// message contains want.
func callErr(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, sess, name, args)
	if !res.IsError {
		t.Fatalf("%s(%v) succeeded, want tool error containing %q", name, args, want)
	}
	if got := textOf(res); !strings.Contains(got, want) {
		t.Errorf("%s(%v) error = %q, want it to contain %q", name, args, got, want)
	}
}

// textOf joins the text content blocks of a result.
func textOf(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// date parses a YYYY-MM-DD string or fails the test.
func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := market.ParseDate(s)
	if err != nil {
		t.Fatalf("date %q: %v", s, err)
	}
	return d
}
