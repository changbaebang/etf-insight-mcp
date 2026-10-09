package yahoo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// ptr is shorthand for an optional number in expectations.
func ptr(v float64) *float64 { return &v }

// quoteSymbols lists the symbols of quotes in order.
func quoteSymbols(quotes []market.Quote) []string {
	out := make([]string, 0, len(quotes))
	for _, q := range quotes {
		out = append(out, q.Symbol)
	}
	return out
}

func TestQuote(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)

	quotes, err := c.Quote(context.Background(), []string{" spy", "qqq ", "ZZZZNOPE", "SPY", ""})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	reqs := f.requests(quotePath)
	if len(reqs) != 1 {
		t.Fatalf("quote requests = %d, want 1", len(reqs))
	}
	if got := reqs[0].query.Get("symbols"); got != "SPY,QQQ,ZZZZNOPE" {
		t.Errorf("symbols param = %q, want normalised, de-duplicated SPY,QQQ,ZZZZNOPE", got)
	}
	if got := quoteSymbols(quotes); !reflect.DeepEqual(got, []string{"SPY", "QQQ"}) {
		t.Fatalf("quotes = %v, want SPY and QQQ only (unknown symbol absent)", got)
	}

	want := market.Quote{
		Symbol: "SPY", Name: "State Street SPDR S&P 500 ETF Trust", Currency: "USD", Exchange: "NYSEArca",
		MarketState: "PRE", Price: 773.93, Change: -3.28998, ChangePct: -0.423301, PreviousClose: 777.22,
		Open: 774.86, DayLow: 770.435, DayHigh: 777.09, Volume: 40070358,
		FiftyTwoWeekLow: 629.28, FiftyTwoWeekHigh: 781.62, FiftyDayAverage: 766.8368, TwoHundredDayAverage: 722.4597,
		DividendYield: 0.0072849393, AsOf: time.Unix(1791489600, 0).UTC(),
	}
	if !reflect.DeepEqual(quotes[0], want) {
		t.Errorf("SPY quote = %+v\nwant %+v", quotes[0], want)
	}
	if q := quotes[1]; q.Symbol != "QQQ" || q.Exchange != "NasdaqGM" || q.Price != 747.58 {
		t.Errorf("QQQ quote = %+v", q)
	}
}

func TestQuoteChunksLargeRequests(t *testing.T) {
	f := newFake(t)
	f.quotes = nil // echo the requested symbols
	c := newFakeClient(t, f)

	symbols := make([]string, 0, 120)
	for i := 1; i <= 120; i++ {
		symbols = append(symbols, fmt.Sprintf("S%03d", i))
	}
	quotes, err := c.Quote(context.Background(), symbols)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if !reflect.DeepEqual(quoteSymbols(quotes), symbols) {
		t.Errorf("got %d quotes in order %v...; want all 120 in request order", len(quotes), quoteSymbols(quotes)[:3])
	}
	reqs := f.requests(quotePath)
	var sizes []int
	for _, r := range reqs {
		sizes = append(sizes, len(strings.Split(r.query.Get("symbols"), ",")))
	}
	if !reflect.DeepEqual(sizes, []int{50, 50, 20}) {
		t.Errorf("chunk sizes = %v, want [50 50 20]", sizes)
	}
	assertBootstraps(t, f, 1, 1)
}

func TestQuoteEdgeCases(t *testing.T) {
	t.Run("no symbols makes no request", func(t *testing.T) {
		f := newFake(t)
		c := newFakeClient(t, f)
		quotes, err := c.Quote(context.Background(), []string{"", "  "})
		if err != nil || quotes != nil {
			t.Fatalf("Quote = %v, %v; want nil, nil", quotes, err)
		}
		if got := len(f.requests("/")); got != 0 {
			t.Errorf("requests = %d, want 0", got)
		}
	})
	t.Run("all unknown yields an empty slice", func(t *testing.T) {
		f := newFake(t)
		f.quotes = []byte(`{"quoteResponse":{"result":[],"error":null}}`)
		c := newFakeClient(t, f)
		quotes, err := c.Quote(context.Background(), []string{"ZZZZNOPE"})
		if err != nil || len(quotes) != 0 {
			t.Fatalf("Quote = %v, %v; want empty, nil", quotes, err)
		}
	})
	t.Run("error object is reported", func(t *testing.T) {
		f := newFake(t)
		f.quotes = []byte(`{"quoteResponse":{"result":null,"error":{"code":"Bad Request","description":"too many symbols"}}}`)
		c := newFakeClient(t, f)
		_, err := c.Quote(context.Background(), []string{"SPY"})
		if err == nil || !strings.Contains(err.Error(), "too many symbols") || errors.Is(err, market.ErrNotFound) {
			t.Fatalf("error = %v, want Yahoo's description without ErrNotFound", err)
		}
	})
	t.Run("bad body", func(t *testing.T) {
		f := newFake(t)
		f.quotes = []byte("<html>")
		c := newFakeClient(t, f)
		if _, err := c.Quote(context.Background(), []string{"SPY"}); err == nil {
			t.Fatal("Quote accepted a non-JSON body")
		}
	})
}

func TestFundProfile(t *testing.T) {
	tests := []struct {
		symbol string
		want   market.FundProfile
	}{
		{
			symbol: "schd",
			want: market.FundProfile{
				Symbol: "SCHD", Name: "Schwab U.S. Dividend Equity ETF", Family: "Schwab ETFs",
				Category: "Large Value", LegalType: "Exchange Traded Fund",
				InceptionDate: time.Date(2011, 10, 20, 0, 0, 0, 0, time.UTC),
				ExpenseRatio:  ptr(0.00059999997), Turnover: ptr(0.3), NetAssets: ptr(107887099904), Yield: ptr(0.0324),
				FetchedAt: fixtureNow,
			},
		},
		{
			symbol: "BND",
			want: market.FundProfile{
				Symbol: "BND", Name: "Vanguard Total Bond Market Index Fund ETF Shares", Family: "Vanguard",
				Category: "Intermediate Core Bond", LegalType: "Exchange Traded Fund",
				InceptionDate: time.Date(2007, 4, 3, 0, 0, 0, 0, time.UTC),
				ExpenseRatio:  ptr(0.00029999999), Turnover: ptr(0.38), NetAssets: ptr(389050990592), Yield: ptr(0.0417),
				FetchedAt: fixtureNow,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			f := newFake(t)
			c := newFakeClient(t, f)
			got, err := c.FundProfile(context.Background(), tt.symbol)
			if err != nil {
				t.Fatalf("FundProfile: %v", err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("FundProfile = %+v\nwant %+v", *got, tt.want)
			}
			reqs := f.requests(summaryPath)
			if len(reqs) != 1 || reqs[0].path != summaryPath+tt.want.Symbol {
				t.Errorf("requests = %+v, want one for %s", reqs, tt.want.Symbol)
			}
			if got := reqs[0].query.Get("modules"); got != summaryModules {
				t.Errorf("modules = %q, want %q", got, summaryModules)
			}
		})
	}
}

func TestHoldings(t *testing.T) {
	tests := []struct {
		symbol string
		want   market.Holdings
	}{
		{
			symbol: "SCHD",
			want: market.Holdings{
				Symbol: "SCHD",
				Top: []market.Holding{
					{Symbol: "TXN", Name: "Texas Instruments Inc", Weight: 0.046566702},
					{Symbol: "QCOM", Name: "Qualcomm Inc", Weight: 0.0456261},
					{Symbol: "PG", Name: "Procter & Gamble Co", Weight: 0.0423155},
				},
				Sectors: []market.Weight{
					{Name: "realestate", Weight: 0}, {Name: "consumer_cyclical", Weight: 0.0673}, {Name: "basic_materials", Weight: 0},
				},
				BondRatings: nil, // only a zero us_government placeholder
				StockPct:    ptr(0.9991), BondPct: ptr(0), CashPct: ptr(0.00090000004), OtherPct: ptr(0),
				EquityStats: map[string]float64{"priceToEarnings": 0.05503, "priceToBook": 0.28174, "priceToSales": 0.55969, "priceToCashflow": 0.10055},
				BondStats:   nil,
				FetchedAt:   fixtureNow,
			},
		},
		{
			symbol: "BND",
			want: market.Holdings{
				Symbol: "BND",
				Top:    nil,
				BondRatings: []market.Weight{
					{Name: "bb", Weight: 0.0001}, {Name: "aa", Weight: 0.72540003}, {Name: "aaa", Weight: 0.0309},
					{Name: "a", Weight: 0.120799996}, {Name: "other", Weight: 0.00029999999}, {Name: "b", Weight: 0},
					{Name: "bbb", Weight: 0.1225}, {Name: "below_b", Weight: 0}, {Name: "us_government", Weight: 0.5179},
				},
				StockPct: ptr(0), BondPct: ptr(0.9864), CashPct: ptr(0.0135), OtherPct: ptr(0),
				EquityStats: nil, // all zero on a bond fund
				BondStats:   map[string]float64{"maturity": 9.328},
				FetchedAt:   fixtureNow,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			c := newFakeClient(t, newFake(t))
			got, err := c.Holdings(context.Background(), tt.symbol)
			if err != nil {
				t.Fatalf("Holdings: %v", err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("Holdings = %+v\nwant %+v", *got, tt.want)
			}
		})
	}
}

func TestPerformance(t *testing.T) {
	c := newFakeClient(t, newFake(t))
	got, err := c.Performance(context.Background(), "SCHD")
	if err != nil {
		t.Fatalf("Performance: %v", err)
	}
	if got.Symbol != "SCHD" || !got.FetchedAt.Equal(fixtureNow) {
		t.Errorf("symbol/fetchedAt = %s %v", got.Symbol, got.FetchedAt)
	}

	wantTrailing := []market.PeriodReturn{
		{Period: "ytd", Fund: ptr(0.2146398), Category: ptr(0.0782128)},
		{Period: "1m", Fund: ptr(-0.0599056), Category: ptr(0.0623014)},
		{Period: "3m", Fund: ptr(0.0340742), Category: ptr(0.038968697)},
		{Period: "1y", Fund: ptr(0.23364), Category: ptr(0.2615741)},
		{Period: "3y", Fund: ptr(0.1544442), Category: ptr(0.1587311)},
		{Period: "5y", Fund: ptr(0.0947983), Category: ptr(0.1012714)},
		{Period: "10y", Fund: ptr(0.124372505), Category: ptr(0.112622)},
	}
	if !reflect.DeepEqual(got.Trailing, wantTrailing) {
		t.Errorf("Trailing = %+v\nwant %+v", got.Trailing, wantTrailing)
	}

	wantAnnual := []market.AnnualReturn{
		{Year: 2023, Fund: ptr(0.045689702), Category: ptr(0.1163058)},
		{Year: 2024, Fund: ptr(0.11670861), Category: ptr(0.14283471)},
		{Year: 2025, Fund: ptr(0.0433039), Category: ptr(0.1496566)},
	}
	if !reflect.DeepEqual(got.Annual, wantAnnual) {
		t.Errorf("Annual = %+v\nwant %+v", got.Annual, wantAnnual)
	}

	wantRisk := []market.RiskStats{
		{Period: "3y", Alpha: ptr(1.4), Beta: ptr(0.56), MeanAnnualReturn: ptr(1.28), RSquared: ptr(25.04), StdDev: ptr(13.75), Sharpe: ptr(0.79), Treynor: ptr(19.49)},
		{Period: "5y", Alpha: ptr(-0.74), Beta: ptr(0.68), MeanAnnualReturn: ptr(0.85), RSquared: ptr(49.61), StdDev: ptr(15.13), Sharpe: ptr(0.42), Treynor: ptr(8.02)},
		{Period: "10y", Alpha: ptr(-0.3), Beta: ptr(0.82), MeanAnnualReturn: ptr(1.08), RSquared: ptr(68.04), StdDev: ptr(15.3), Sharpe: ptr(0.68), Treynor: ptr(11.99)},
	}
	if !reflect.DeepEqual(got.Risk, wantRisk) {
		t.Errorf("Risk = %+v\nwant %+v", got.Risk, wantRisk)
	}

	bnd, err := c.Performance(context.Background(), "BND")
	if err != nil {
		t.Fatalf("Performance(BND): %v", err)
	}
	if len(bnd.Trailing) != 7 || *bnd.Trailing[0].Fund != -0.027491901 || *bnd.Trailing[0].Category != 0.0016405 {
		t.Errorf("BND trailing = %+v", bnd.Trailing)
	}
	if len(bnd.Risk) != 3 || bnd.Risk[0].Period != "3y" || *bnd.Risk[0].Beta != 0.98 {
		t.Errorf("BND risk = %+v", bnd.Risk)
	}
}

func TestSummaryMemo(t *testing.T) {
	t.Run("three methods share one request within the TTL", func(t *testing.T) {
		f := newFake(t)
		c := newFakeClient(t, f)
		ctx := context.Background()
		if _, err := c.FundProfile(ctx, "SCHD"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Holdings(ctx, "schd"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Performance(ctx, " SCHD "); err != nil {
			t.Fatal(err)
		}
		if _, err := c.FundProfile(ctx, "BND"); err != nil {
			t.Fatal(err)
		}
		if got := len(f.requests(summaryPath)); got != 2 {
			t.Errorf("quoteSummary requests = %d, want 2 (one per symbol)", got)
		}
	})
	t.Run("expired entries are refetched", func(t *testing.T) {
		f := newFake(t)
		c := newFakeClient(t, f, WithSummaryTTL(time.Minute))
		ctx := context.Background()
		if _, err := c.FundProfile(ctx, "SCHD"); err != nil {
			t.Fatal(err)
		}
		c.now = func() time.Time { return fixtureNow.Add(59 * time.Second) }
		if _, err := c.Holdings(ctx, "SCHD"); err != nil {
			t.Fatal(err)
		}
		c.now = func() time.Time { return fixtureNow.Add(time.Minute) }
		p, err := c.FundProfile(ctx, "SCHD")
		if err != nil {
			t.Fatal(err)
		}
		if got := len(f.requests(summaryPath)); got != 2 {
			t.Errorf("quoteSummary requests = %d, want 2 (hit at 59 s, miss at 60 s)", got)
		}
		if !p.FetchedAt.Equal(fixtureNow.Add(time.Minute)) {
			t.Errorf("FetchedAt = %v, want the refetch time", p.FetchedAt)
		}
	})
	t.Run("zero TTL disables the memo", func(t *testing.T) {
		f := newFake(t)
		c := newFakeClient(t, f, WithSummaryTTL(0))
		ctx := context.Background()
		for range 3 {
			if _, err := c.FundProfile(ctx, "SCHD"); err != nil {
				t.Fatal(err)
			}
		}
		if got := len(f.requests(summaryPath)); got != 3 {
			t.Errorf("quoteSummary requests = %d, want 3", got)
		}
	})
	t.Run("errors are not memoized", func(t *testing.T) {
		f := newFake(t)
		c := newFakeClient(t, f)
		ctx := context.Background()
		for range 2 {
			if _, err := c.FundProfile(ctx, "GHOST"); !errors.Is(err, market.ErrNotFound) {
				t.Fatalf("error %v does not wrap ErrNotFound", err)
			}
		}
		if got := len(f.requests(summaryPath)); got != 2 {
			t.Errorf("quoteSummary requests = %d, want 2", got)
		}
	})
}

func TestSummaryNotFound(t *testing.T) {
	tests := []struct {
		name   string
		symbol string
		body   []byte // nil leaves the symbol unknown to the fake (404)
	}{
		{name: "404 with Not Found body", symbol: "ZZZZNOPE"},
		{
			name: "200 with Not Found error object", symbol: "GHOST",
			body: []byte(`{"quoteSummary":{"result":null,"error":{"code":"Not Found","description":"Quote not found for symbol: GHOST"}}}`),
		},
		{name: "200 with empty result", symbol: "EMPTY", body: []byte(`{"quoteSummary":{"result":[],"error":null}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			if tt.body != nil {
				f.summaries[tt.symbol] = tt.body
			}
			c := newFakeClient(t, f)
			for _, call := range []func(context.Context, string) error{
				func(ctx context.Context, s string) error { _, err := c.FundProfile(ctx, s); return err },
				func(ctx context.Context, s string) error { _, err := c.Holdings(ctx, s); return err },
				func(ctx context.Context, s string) error { _, err := c.Performance(ctx, s); return err },
			} {
				err := call(context.Background(), tt.symbol)
				if !errors.Is(err, market.ErrNotFound) {
					t.Errorf("error %v does not wrap ErrNotFound", err)
				}
				if !strings.Contains(err.Error(), tt.symbol) {
					t.Errorf("error %q does not mention the symbol", err)
				}
			}
		})
	}
}

func TestFundEmptySymbol(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)
	ctx := context.Background()
	if _, err := c.FundProfile(ctx, " "); err == nil {
		t.Error("FundProfile accepted an empty symbol")
	}
	if _, err := c.Holdings(ctx, ""); err == nil {
		t.Error("Holdings accepted an empty symbol")
	}
	if _, err := c.Performance(ctx, ""); err == nil {
		t.Error("Performance accepted an empty symbol")
	}
	if got := len(f.requests("/")); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

func TestSearch(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)

	hits, err := c.Search(context.Background(), " dividend etf ", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []market.SearchHit{
		{Symbol: "FDVV", Name: "Fidelity High Dividend ETF", Type: "ETF", Exchange: "NYSEArca"},
		{Symbol: "SPYD", Name: "State Street SPDR Portfolio S&P 500 High Dividend ETF", Type: "ETF", Exchange: "NYSEArca"},
	}
	if !reflect.DeepEqual(hits, want) {
		t.Errorf("hits = %+v\nwant %+v", hits, want)
	}
	reqs := f.requests(searchPath)
	if len(reqs) != 1 {
		t.Fatalf("search requests = %d, want 1", len(reqs))
	}
	q := reqs[0].query
	if q.Get("q") != "dividend etf" || q.Get("quotesCount") != "2" || q.Get("newsCount") != "0" {
		t.Errorf("query = %v, want q=dividend etf quotesCount=2 newsCount=0", q)
	}
	assertBootstraps(t, f, 0, 0)
}

func TestNews(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)

	items, err := c.News(context.Background(), "dividend etf", 2)
	if err != nil {
		t.Fatalf("News: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	want := market.NewsItem{
		Title:       "At 68 With $780,000, Do You Live on the Dividends or Sell Shares? These 3 ETFs Let You Do Both",
		Publisher:   "24/7 Wall St.",
		Link:        "https://finance.yahoo.com/markets/stocks/articles/68-780-000-live-dividends-203327791.html",
		PublishedAt: time.Unix(1791491607, 0).UTC(),
	}
	if !reflect.DeepEqual(items[0], want) {
		t.Errorf("item = %+v\nwant %+v", items[0], want)
	}
	q := f.requests(searchPath)[0].query
	if q.Get("quotesCount") != "0" || q.Get("newsCount") != "2" {
		t.Errorf("query = %v, want quotesCount=0 newsCount=2", q)
	}
}

func TestSearchLimits(t *testing.T) {
	tests := []struct {
		limit     int
		wantCount string
		wantLen   int // fixture holds 3 of each
	}{
		{limit: 0, wantCount: "10", wantLen: 3},
		{limit: -5, wantCount: "10", wantLen: 3},
		{limit: 1, wantCount: "1", wantLen: 1},
		{limit: 3, wantCount: "3", wantLen: 3},
		{limit: 100, wantCount: "50", wantLen: 3},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("limit %d", tt.limit), func(t *testing.T) {
			f := newFake(t)
			c := newFakeClient(t, f)
			ctx := context.Background()

			hits, err := c.Search(ctx, "etf", tt.limit)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			items, err := c.News(ctx, "etf", tt.limit)
			if err != nil {
				t.Fatalf("News: %v", err)
			}
			if len(hits) != tt.wantLen || len(items) != tt.wantLen {
				t.Errorf("got %d hits, %d items; want %d each", len(hits), len(items), tt.wantLen)
			}
			reqs := f.requests(searchPath)
			if got := reqs[0].query.Get("quotesCount"); got != tt.wantCount {
				t.Errorf("Search quotesCount = %q, want %q", got, tt.wantCount)
			}
			if got := reqs[1].query.Get("newsCount"); got != tt.wantCount {
				t.Errorf("News newsCount = %q, want %q", got, tt.wantCount)
			}
		})
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)
	ctx := context.Background()
	if _, err := c.Search(ctx, "  ", 5); err == nil {
		t.Error("Search accepted an empty query")
	}
	if _, err := c.News(ctx, "", 5); err == nil {
		t.Error("News accepted an empty query")
	}
	if got := len(f.requests("/")); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

func TestSearchSkipsIncompleteEntries(t *testing.T) {
	f := newFake(t)
	f.search = []byte(`{"quotes":[{"symbol":"","shortname":"nameless"},{"symbol":"VTI","shortname":"Vanguard Total Stock Market","quoteType":"ETF","exchange":"PCX"}],
		"news":[{"title":"","publisher":"x"},{"title":"Headline","publisher":"Pub","link":"https://x"}]}`)
	c := newFakeClient(t, f)
	ctx := context.Background()

	hits, err := c.Search(ctx, "vti", 5)
	if err != nil {
		t.Fatal(err)
	}
	wantHits := []market.SearchHit{{Symbol: "VTI", Name: "Vanguard Total Stock Market", Type: "ETF", Exchange: "PCX"}}
	if !reflect.DeepEqual(hits, wantHits) {
		t.Errorf("hits = %+v, want %+v (short name and raw exchange as fallbacks)", hits, wantHits)
	}
	items, err := c.News(ctx, "vti", 5)
	if err != nil {
		t.Fatal(err)
	}
	wantItems := []market.NewsItem{{Title: "Headline", Publisher: "Pub", Link: "https://x"}}
	if !reflect.DeepEqual(items, wantItems) {
		t.Errorf("items = %+v, want %+v (zero PublishedAt when missing)", items, wantItems)
	}
}

func TestParseQuoteSummaryErrors(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantNotFound bool
	}{
		{name: "not found error", body: `{"quoteSummary":{"result":null,"error":{"code":"Not Found","description":"nope"}}}`, wantNotFound: true},
		{name: "empty result", body: `{"quoteSummary":{"result":[],"error":null}}`, wantNotFound: true},
		{name: "null result", body: `{"quoteSummary":{"result":null,"error":null}}`, wantNotFound: true},
		{name: "other error", body: `{"quoteSummary":{"result":null,"error":{"code":"Bad Request","description":"bad modules"}}}`},
		{name: "invalid json", body: `{"quoteSummary":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseQuoteSummary([]byte(tt.body))
			if err == nil {
				t.Fatal("parseQuoteSummary succeeded, want error")
			}
			if errors.Is(err, market.ErrNotFound) != tt.wantNotFound {
				t.Errorf("errors.Is(%q, ErrNotFound) = %v, want %v", err, !tt.wantNotFound, tt.wantNotFound)
			}
		})
	}
}

func TestQuoteSummaryTolerantDecoding(t *testing.T) {
	// Modules missing, nulls, odd shapes inside number maps, unparsable
	// years and periods without a number must not fail or panic.
	body := []byte(`{"quoteSummary":{"result":[{
		"price":{"symbol":"X","shortName":"Short only"},
		"topHoldings":{"equityHoldings":null,"bondHoldings":{"duration":"n/a","maturity":{"raw":3.5},"junk":[1]},
			"sectorWeightings":[{"technology":{"raw":0.5}},{"energy":{}}],"holdings":[{"symbol":"","holdingName":""}]},
		"fundPerformance":{"trailingReturns":{"ytd":{"raw":0.1},"asOfDate":{"raw":1}},
			"annualTotalReturns":{"returns":[{"year":"abc","annualValue":{"raw":1}},{"year":"2020","annualValue":{}},{"year":"2021","annualValue":{"raw":0.2}}],
			"returnsCat":[{"year":"2019","annualValue":{"raw":0.3}}]},
			"riskOverviewStatistics":{"riskStatistics":[{"year":"","alpha":{"raw":1}},{"year":"all","beta":{"raw":2}},{"year":"5y","beta":{"raw":1.1}}]}}
	}],"error":null}}`)
	result, err := parseQuoteSummary(body)
	if err != nil {
		t.Fatalf("parseQuoteSummary: %v", err)
	}
	s := &fundSummary{result: result, fetchedAt: fixtureNow}

	p := s.fundProfile("X")
	if p.Name != "Short only" || p.ExpenseRatio != nil || p.NetAssets != nil || !p.InceptionDate.IsZero() {
		t.Errorf("profile = %+v, want short name and nil optionals", p)
	}

	h := s.holdings("X")
	if h.Top != nil || h.EquityStats != nil || h.StockPct != nil {
		t.Errorf("holdings = %+v, want no top holdings, equity stats or positions", h)
	}
	if !reflect.DeepEqual(h.BondStats, map[string]float64{"maturity": 3.5}) {
		t.Errorf("BondStats = %v, want only maturity", h.BondStats)
	}
	if !reflect.DeepEqual(h.Sectors, []market.Weight{{Name: "technology", Weight: 0.5}}) {
		t.Errorf("Sectors = %v, want technology only (empty entry skipped)", h.Sectors)
	}

	perf := s.performance("X")
	if len(perf.Trailing) != 1 || perf.Trailing[0].Period != "ytd" || perf.Trailing[0].Category != nil {
		t.Errorf("Trailing = %+v, want ytd with no category", perf.Trailing)
	}
	wantAnnual := []market.AnnualReturn{{Year: 2019, Category: ptr(0.3)}, {Year: 2021, Fund: ptr(0.2)}}
	if !reflect.DeepEqual(perf.Annual, wantAnnual) {
		t.Errorf("Annual = %+v, want %+v", perf.Annual, wantAnnual)
	}
	if len(perf.Risk) != 2 || perf.Risk[0].Period != "5y" || perf.Risk[1].Period != "all" {
		t.Errorf("Risk = %+v, want 5y before the unnumbered period, empty one dropped", perf.Risk)
	}
}

func TestDecodeAPIError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *apiError
	}{
		{name: "chart", body: `{"chart":{"result":null,"error":{"code":"Not Found","description":"gone"}}}`, want: &apiError{Code: "Not Found", Description: "gone"}},
		{name: "quoteSummary", body: `{"quoteSummary":{"result":null,"error":{"code":"Not Found","description":"x"}}}`, want: &apiError{Code: "Not Found", Description: "x"}},
		{name: "finance", body: invalidCrumbBody, want: &apiError{Code: "Unauthorized", Description: "Invalid Crumb"}},
		{name: "no error", body: `{"quoteSummary":{"result":[],"error":null}}`},
		{name: "search shape", body: `{"count":3,"quotes":[],"news":[]}`},
		{name: "not json", body: `<html>`},
		{name: "array", body: `[1,2]`},
		{name: "empty", body: ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeAPIError([]byte(tt.body))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeAPIError = %+v, want %+v", got, tt.want)
			}
		})
	}
	if err := notFoundError([]byte("<html>")); !errors.Is(err, market.ErrNotFound) || !strings.Contains(err.Error(), "no data for symbol") {
		t.Errorf("notFoundError(non-envelope) = %v", err)
	}
	if err := (&apiError{Code: "Bad", Description: ""}).err(); err == nil || !strings.Contains(err.Error(), "no data for symbol") {
		t.Errorf("apiError with empty description = %v", err)
	}
}

func TestNormalizeSymbols(t *testing.T) {
	got := normalizeSymbols([]string{" spy", "QQQ", "spy ", "", "  ", "qqq", "VTI"})
	if want := []string{"SPY", "QQQ", "VTI"}; !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeSymbols = %v, want %v", got, want)
	}
	if got := normalizeSymbols(nil); len(got) != 0 {
		t.Errorf("normalizeSymbols(nil) = %v, want empty", got)
	}
}

func TestSymbolsLabel(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"SPY"}, "SPY"},
		{[]string{"A", "B", "C"}, "A,B,C"},
		{[]string{"A", "B", "C", "D", "E"}, "A,B,C,... (5 symbols)"},
	}
	for _, tt := range tests {
		if got := symbolsLabel(tt.in); got != tt.want {
			t.Errorf("symbolsLabel(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSearchArgs(t *testing.T) {
	tests := []struct {
		query     string
		limit     int
		wantQuery string
		wantLimit int
		wantErr   bool
	}{
		{" vti ", 5, "vti", 5, false},
		{"vti", 0, "vti", defaultSearchLimit, false},
		{"vti", -1, "vti", defaultSearchLimit, false},
		{"vti", 51, "vti", maxSearchLimit, false},
		{"   ", 5, "", 0, true},
	}
	for _, tt := range tests {
		query, limit, err := searchArgs(tt.query, tt.limit)
		if (err != nil) != tt.wantErr || query != tt.wantQuery || limit != tt.wantLimit {
			t.Errorf("searchArgs(%q, %d) = %q, %d, %v; want %q, %d, err=%v", tt.query, tt.limit, query, limit, err, tt.wantQuery, tt.wantLimit, tt.wantErr)
		}
	}
}

func TestSummaryMemoEviction(t *testing.T) {
	m := &summaryMemo{}
	t0 := fixtureNow
	a, b := &fundSummary{fetchedAt: t0}, &fundSummary{fetchedAt: t0}
	m.put("A", a, t0, t0.Add(time.Minute))
	m.put("B", b, t0.Add(30*time.Second), t0.Add(90*time.Second))
	if got, ok := m.get("A", t0.Add(59*time.Second)); !ok || got != a {
		t.Error("A should still be memoized at 59 s")
	}
	if _, ok := m.get("A", t0.Add(time.Minute)); ok {
		t.Error("A should have expired at 60 s")
	}
	m.put("C", &fundSummary{}, t0.Add(2*time.Minute), t0.Add(3*time.Minute))
	if _, ok := m.entries["B"]; ok {
		t.Error("put should evict B, expired before the put time")
	}
	if _, ok := m.entries["C"]; !ok {
		t.Error("put should keep the new entry")
	}
}
