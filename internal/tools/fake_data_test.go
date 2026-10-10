package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dataToolNames is every tool of the data group.
var dataToolNames = []string{"search_symbols", "get_quote", "get_dividends", "get_splits", "get_fund_profile", "get_holdings", "get_fund_performance", "get_news", "market_overview"}

// dataQuoteTime is when the fake quotes were taken; the VIX quote is a
// quarter of an hour later so market_overview's as_of has a clear maximum.
var dataQuoteTime = time.Date(2024, time.January, 2, 21, 0, 0, 0, time.UTC)

// dataFakeFund implements market.FundSource from memory. Unknown symbols
// return market.ErrNotFound (Quote omits them), BOOM fails like a network
// error, and the limits Search and News were asked for are recorded.
type dataFakeFund struct {
	mu          sync.Mutex
	quotes      map[string]market.Quote
	profiles    map[string]*market.FundProfile
	holdings    map[string]*market.Holdings
	performance map[string]*market.Performance
	hits        []market.SearchHit
	newsCount   int
	searchLimit int
	newsLimit   int
	quoteCalls  int
}

// errDataFakeNetwork is what the fake answers for BOOM.
var errDataFakeNetwork = errors.New("yahoo: BOOM: request: connection reset by peer")

func (f *dataFakeFund) Quote(_ context.Context, symbols []string) ([]market.Quote, error) {
	f.mu.Lock()
	f.quoteCalls++
	f.mu.Unlock()
	var out []market.Quote
	for _, s := range symbols {
		sym := strings.ToUpper(strings.TrimSpace(s))
		if sym == "BOOM" {
			return nil, errDataFakeNetwork
		}
		if q, ok := f.quotes[sym]; ok {
			out = append(out, q)
		}
	}
	return out, nil
}

// dataLookup returns the document of sym from m, or the fake's errors.
func dataLookup[T any](m map[string]*T, symbol string) (*T, error) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if sym == "BOOM" {
		return nil, errDataFakeNetwork
	}
	v, ok := m[sym]
	if !ok {
		return nil, fmt.Errorf("fake fund: %s: %w", sym, market.ErrNotFound)
	}
	return v, nil
}

func (f *dataFakeFund) FundProfile(_ context.Context, symbol string) (*market.FundProfile, error) {
	return dataLookup(f.profiles, symbol)
}

func (f *dataFakeFund) Holdings(_ context.Context, symbol string) (*market.Holdings, error) {
	return dataLookup(f.holdings, symbol)
}

func (f *dataFakeFund) Performance(_ context.Context, symbol string) (*market.Performance, error) {
	return dataLookup(f.performance, symbol)
}

// Search returns the hits whose symbol or name contains query, ignoring
// case, in table order, at most limit of them.
func (f *dataFakeFund) Search(_ context.Context, query string, limit int) ([]market.SearchHit, error) {
	f.mu.Lock()
	f.searchLimit = limit
	f.mu.Unlock()
	if query == "BOOM" {
		return nil, errDataFakeNetwork
	}
	q := strings.ToLower(query)
	var out []market.SearchHit
	for _, h := range f.hits {
		if len(out) == limit {
			break
		}
		if strings.Contains(strings.ToLower(h.Symbol), q) || strings.Contains(strings.ToLower(h.Name), q) {
			out = append(out, h)
		}
	}
	return out, nil
}

// News returns newsCount numbered headlines about query (none for
// "nothing"), at most limit of them; every other one has no time.
func (f *dataFakeFund) News(_ context.Context, query string, limit int) ([]market.NewsItem, error) {
	f.mu.Lock()
	f.newsLimit = limit
	f.mu.Unlock()
	if query == "nothing" {
		return nil, nil
	}
	var out []market.NewsItem
	for i := 0; i < f.newsCount && len(out) < limit; i++ {
		item := market.NewsItem{
			Title:     fmt.Sprintf("Headline %d about %s", i+1, query),
			Publisher: "Fake Wire",
			Link:      fmt.Sprintf("https://example.com/news/%d", i+1),
		}
		if i%2 == 0 {
			item.PublishedAt = dataQuoteTime.Add(-time.Duration(i) * time.Hour)
		}
		out = append(out, item)
	}
	return out, nil
}

// limits returns the last limits Search and News were called with.
func (f *dataFakeFund) limits() (search, news int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searchLimit, f.newsLimit
}

// newDataFakeFund builds the canned fund data the data tool tests use.
func newDataFakeFund() *dataFakeFund {
	f := &dataFakeFund{
		quotes:      map[string]market.Quote{},
		profiles:    map[string]*market.FundProfile{},
		holdings:    map[string]*market.Holdings{},
		performance: map[string]*market.Performance{},
		newsCount:   25,
	}
	for i, inst := range overviewInstruments {
		q := market.Quote{
			Symbol:        inst.symbol,
			Name:          inst.label + " fund",
			Currency:      "USD",
			Exchange:      "NYSEArca",
			MarketState:   "CLOSED",
			Price:         100 + float64(i),
			Change:        -0.5 + 0.1*float64(i),
			ChangePct:     -0.5 + 0.1*float64(i),
			PreviousClose: 100 + float64(i) + 0.5 - 0.1*float64(i),
			AsOf:          dataQuoteTime,
		}
		if inst.symbol == "^VIX" {
			q.Name, q.Exchange, q.Price, q.AsOf = "CBOE Volatility Index", "Cboe Indices", 13.2, dataQuoteTime.Add(15*time.Minute)
		}
		f.quotes[inst.symbol] = q
	}
	f.quotes["VOO"] = market.Quote{
		Symbol: "VOO", Name: "Vanguard S&P 500 ETF", Currency: "USD", Exchange: "NYSEArca", MarketState: "REGULAR",
		Price: 435.123, Change: 1.234, ChangePct: 0.28456, PreviousClose: 433.889, Open: 434.0, DayLow: 432.5, DayHigh: 436.25,
		Volume: 4567890, FiftyTwoWeekLow: 340.1, FiftyTwoWeekHigh: 440.9, FiftyDayAverage: 420.555, TwoHundredDayAverage: 400.444,
		DividendYield: 0.01357, AsOf: dataQuoteTime,
	}
	f.quotes["SCHD"] = market.Quote{Symbol: "SCHD", Name: "Schwab US Dividend Equity ETF", Currency: "USD", Price: 76.5, DividendYield: 0.035, AsOf: dataQuoteTime}

	f.profiles["VOO"] = &market.FundProfile{
		Symbol: "VOO", Name: "Vanguard S&P 500 ETF", Family: "Vanguard", Category: "Large Blend", LegalType: "Exchange Traded Fund",
		InceptionDate: time.Date(2010, time.September, 7, 0, 0, 0, 0, time.UTC),
		ExpenseRatio:  ptr(0.0003), Turnover: ptr(0.02), NetAssets: ptr(1.0123456789e12), Yield: ptr(0.01357),
		FetchedAt: now.Add(-time.Hour),
	}
	f.profiles["SPY"] = &market.FundProfile{Symbol: "SPY", Name: "SPDR S&P 500 ETF Trust", Family: "SPDR State Street Global Advisors", ExpenseRatio: ptr(0.000945), FetchedAt: now.Add(-time.Hour)}
	f.profiles["BND"] = &market.FundProfile{Symbol: "BND", Name: "Vanguard Total Bond Market ETF", Family: "Vanguard", Category: "Intermediate Core Bond", ExpenseRatio: ptr(0.0003), FetchedAt: now.Add(-5 * 24 * time.Hour)}
	f.profiles["AAPL"] = &market.FundProfile{Symbol: "AAPL", Name: "Apple Inc.", FetchedAt: now.Add(-time.Hour)}

	f.holdings["SCHD"] = &market.Holdings{
		Symbol: "SCHD",
		Top: []market.Holding{
			{Symbol: "AVGO", Name: "Broadcom Inc", Weight: 0.04512},
			{Symbol: "HD", Name: "Home Depot Inc", Weight: 0.04101},
			{Symbol: "", Name: "Cash", Weight: 0.0102},
		},
		Sectors:  []market.Weight{{Name: "financial_services", Weight: 0.1923}, {Name: "realestate", Weight: 0}},
		StockPct: ptr(0.9899), BondPct: ptr(0.0), CashPct: ptr(0.0101), OtherPct: nil,
		EquityStats: map[string]float64{"priceToEarnings": 0.05503, "medianMarketCap": 123456.78912},
		FetchedAt:   now.Add(-time.Hour),
	}
	f.holdings["BND"] = &market.Holdings{
		Symbol:      "BND",
		BondRatings: []market.Weight{{Name: "us_government", Weight: 0.4812}, {Name: "aaa", Weight: 0.0311}},
		BondPct:     ptr(0.995), CashPct: ptr(0.005),
		BondStats: map[string]float64{"duration": 6.05, "maturity": 8.4},
		FetchedAt: now.Add(-time.Hour),
	}
	big := &market.Holdings{Symbol: "BIGF", StockPct: ptr(1.0), FetchedAt: now.Add(-time.Hour)}
	for i := 0; i < 30; i++ {
		big.Top = append(big.Top, market.Holding{Symbol: fmt.Sprintf("H%02d", i+1), Name: fmt.Sprintf("Holding %d", i+1), Weight: 0.01})
	}
	f.holdings["BIGF"] = big

	f.performance["SCHD"] = &market.Performance{
		Symbol: "SCHD",
		Trailing: []market.PeriodReturn{
			{Period: "ytd", Fund: ptr(0.2146398), Category: ptr(0.18)},
			{Period: "1y", Fund: ptr(0.23364), Category: nil},
			{Period: "10y", Fund: ptr(0.124372505), Category: ptr(0.1)},
		},
		Annual: []market.AnnualReturn{
			{Year: 2021, Fund: ptr(0.2986), Category: ptr(0.2571)},
			{Year: 2022, Fund: ptr(-0.0322), Category: ptr(-0.0595)},
			{Year: 2023, Fund: ptr(0.0457), Category: nil},
		},
		Risk: []market.RiskStats{
			{Period: "3y", Alpha: ptr(1.4), Beta: ptr(0.56), MeanAnnualReturn: ptr(1.28), RSquared: ptr(25.04), StdDev: ptr(13.754), Sharpe: ptr(0.79), Treynor: ptr(19.49)},
			{Period: "5y", Alpha: ptr(-0.74), Beta: nil},
		},
		FetchedAt: now.Add(-time.Hour),
	}
	bigPerf := &market.Performance{Symbol: "BIGF", FetchedAt: now.Add(-time.Hour)}
	for y := 1994; y <= 2023; y++ {
		bigPerf.Annual = append(bigPerf.Annual, market.AnnualReturn{Year: y, Fund: ptr(0.01 * float64(y-1990))})
	}
	f.performance["BIGF"] = bigPerf

	f.hits = []market.SearchHit{
		{Symbol: "SCHD", Name: "Schwab US Dividend Equity ETF", Type: "ETF", Exchange: "NYSEArca"},
		{Symbol: "SCHW", Name: "The Charles Schwab Corporation", Type: "EQUITY", Exchange: "NYSE"},
		{Symbol: "SCHB", Name: "Schwab U.S. Broad Market ETF", Type: "ETF", Exchange: "NYSEArca"},
		{Symbol: "SWPPX", Name: "Schwab S&P 500 Index Fund", Type: "MUTUALFUND", Exchange: "Nasdaq"},
		{Symbol: "schx", Name: "Schwab U.S. Large-Cap ETF", Type: "ETF", Exchange: "NYSEArca"},
	}
	for i := 1; i <= 40; i++ {
		kind := "ETF"
		if i%4 == 0 {
			kind = "EQUITY"
		}
		f.hits = append(f.hits, market.SearchHit{Symbol: fmt.Sprintf("FND%02d", i), Name: fmt.Sprintf("Generic Fund %d", i), Type: kind, Exchange: "NYSEArca"})
	}
	return f
}

// newDataFakeSource extends the shared fake prices with the overview ETFs,
// a dividend grower (VIG), a frequent payer (DIVM) and split histories
// (TQQQ, UVXY).
func newDataFakeSource() *fakeSource {
	src := newFakeSource()
	rising := func(base, drift float64) func(int) float64 {
		return func(i int) float64 { return base * math.Exp(drift*float64(i)) * (1 + 0.02*math.Sin(float64(i)/25)) }
	}
	for _, sym := range []string{"QQQ", "DIA", "IWM", "VEA", "VWO", "BND", "GLD"} {
		src.add(synthetic(sym, "USD", "ETF", seriesStart, 780, rising(100, 0.0003), 63, 0.5))
	}
	src.add(synthetic("TLT", "USD", "ETF", seriesStart, 780, rising(150, -0.0008), 21, 0.3))
	src.add(synthetic("DIVM", "USD", "ETF", seriesStart, 780, rising(20, 0.0001), 5, 0.01))

	tqqq := synthetic("TQQQ", "USD", "ETF", seriesStart, 780, rising(40, 0.0005), 0, 0)
	tqqq.Splits = []market.Split{
		{Date: time.Date(2021, time.January, 21, 0, 0, 0, 0, time.UTC), Numerator: 2, Denominator: 1},
		{Date: time.Date(2022, time.January, 13, 0, 0, 0, 0, time.UTC), Numerator: 2, Denominator: 1},
	}
	src.add(tqqq)
	uvxy := synthetic("UVXY", "USD", "ETF", seriesStart, 780, rising(30, -0.001), 0, 0)
	uvxy.Splits = []market.Split{{Date: time.Date(2023, time.June, 23, 0, 0, 0, 0, time.UTC), Numerator: 1, Denominator: 10}}
	src.add(uvxy)

	src.add(dataDividendGrower())
	return src
}

// dataDividendGrower is VIG from 2016-01-04 to 2023-12-29 paying on the
// first trading day of March, June, September and December; each year's
// payments are 10% larger than the year before, so every calendar year
// totals 1.1^(year-2016).
func dataDividendGrower() *market.Series {
	start := time.Date(2016, time.January, 4, 0, 0, 0, 0, time.UTC)
	end := time.Date(2023, time.December, 29, 0, 0, 0, 0, time.UTC)
	s := &market.Series{Meta: market.Meta{Symbol: "VIG", Name: "VIG synthetic", Currency: "USD", Exchange: "TEST", InstrumentType: "ETF", FirstTradeDate: start}}
	paid := map[[2]int]bool{}
	for day, i := start, 0; !day.After(end); day = day.AddDate(0, 0, 1) {
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		p := 100 * math.Exp(0.0002*float64(i))
		b := market.Bar{Date: day, Close: p, AdjClose: p}
		key := [2]int{day.Year(), int(day.Month())}
		if m := day.Month(); (m == time.March || m == time.June || m == time.September || m == time.December) && !paid[key] {
			paid[key] = true
			b.Dividend = 0.25 * math.Pow(1.1, float64(day.Year()-2016))
		}
		s.Bars = append(s.Bars, b)
		i++
	}
	return s
}

// dataDeps wires the data fakes with the fixed clock.
func dataDeps(src *fakeSource, fund *dataFakeFund) Deps {
	d := testDeps(src)
	d.Fund = fund
	return d
}

// newDataSession starts a session on the data fakes and returns them.
func newDataSession(t *testing.T) (*mcp.ClientSession, *fakeSource, *dataFakeFund) {
	t.Helper()
	src, fund := newDataFakeSource(), newDataFakeFund()
	return newSession(t, dataDeps(src, fund)), src, fund
}

func TestDataToolsAreListedWithDescriptions(t *testing.T) {
	sess, _, _ := newDataSession(t)
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range dataToolNames {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("tool %s is not listed", name)
			continue
		}
		if len(tool.Description) < 100 {
			t.Errorf("tool %s description is too thin: %q", name, tool.Description)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.Title == "" {
			t.Errorf("tool %s annotations = %+v, want read-only with a title", name, tool.Annotations)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Errorf("tool %s input schema is %T, want object", name, tool.InputSchema)
			continue
		}
		checkDescribed(t, name, "", schema)
	}
}

func TestDataFundToolsNeedAFundSource(t *testing.T) {
	sess := newSession(t, testDeps(newDataFakeSource())) // Deps.Fund is nil
	calls := map[string]map[string]any{
		"search_symbols":       {"query": "schwab"},
		"get_quote":            {"symbols": []string{"VOO"}},
		"get_fund_profile":     {"symbol": "VOO"},
		"get_holdings":         {"symbol": "SCHD"},
		"get_fund_performance": {"symbol": "SCHD"},
		"get_news":             {"query": "VOO"},
		"market_overview":      {},
	}
	for name, args := range calls {
		t.Run(name, func(t *testing.T) {
			callErr(t, sess, name, args, "fund data source not configured")
		})
	}
	// The price-history tools of the group keep working without it.
	var div getDividendsOutput
	callOK(t, sess, "get_dividends", map[string]any{"symbol": "SPY"}, &div)
	var splits getSplitsOutput
	callOK(t, sess, "get_splits", map[string]any{"symbol": "TQQQ"}, &splits)
}

func TestDataHelpers(t *testing.T) {
	if got, err := parseDataLimit("limit", 0, 10, 25); err != nil || got != 10 {
		t.Errorf("parseDataLimit(0) = %d, %v; want default 10", got, err)
	}
	for _, n := range []int{-1, 26} {
		if _, err := parseDataLimit("limit", n, 10, 25); err == nil || !strings.Contains(err.Error(), "between 1 and 25") {
			t.Errorf("parseDataLimit(%d) error = %v, want bounds", n, err)
		}
	}
	if fundPctPtr(nil) != nil || fundPctPtr(ptr(math.NaN())) != nil || fundRound2Ptr(ptr(math.Inf(1))) != nil {
		t.Error("nil and non-finite values must stay unreported")
	}
	if got := fundPctPtr(ptr(0.123456)); got == nil || *got != 12.35 {
		t.Errorf("fundPctPtr(0.123456) = %v, want 12.35", got)
	}
	if got := dataTimestamp(time.Time{}); got != "" {
		t.Errorf("dataTimestamp(zero) = %q, want empty", got)
	}
	if got := dataTimestamp(time.Date(2024, 1, 2, 16, 0, 0, 0, time.FixedZone("EST", -5*3600))); got != "2024-01-02T21:00:00Z" {
		t.Errorf("dataTimestamp = %q, want UTC", got)
	}
	if got := snakeKey("priceToEarnings"); got != "price_to_earnings" {
		t.Errorf("snakeKey = %q", got)
	}
	d := testDeps(newFakeSource())
	if w := d.fundFetchedWarnings("X", now.Add(-time.Hour)); w != nil {
		t.Errorf("fresh document warned: %v", w)
	}
	if w := d.fundFetchedWarnings("X", now.Add(-72*time.Hour)); len(w) != 1 || !strings.Contains(w[0], "3 days ago") {
		t.Errorf("old document warnings = %v, want one '3 days ago'", w)
	}
}
