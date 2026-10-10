package yahoo

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// qsNumber is Yahoo's {raw, fmt} number object as quoteSummary reports
// every figure. Raw is nil when the field is present but empty ({}), which
// means "not reported".
type qsNumber struct {
	Raw *float64 `json:"raw"`
}

// ptr returns a copy of the reported value, or nil when none was.
func (n qsNumber) ptr() *float64 {
	if n.Raw == nil {
		return nil
	}
	v := *n.Raw
	return &v
}

// value returns the reported value, or 0 when none was.
func (n qsNumber) value() float64 {
	if n.Raw == nil {
		return 0
	}
	return *n.Raw
}

// firstNumber returns the first of the numbers that was reported.
func firstNumber(numbers ...qsNumber) qsNumber {
	for _, n := range numbers {
		if n.Raw != nil {
			return n
		}
	}
	return qsNumber{}
}

// qsNumberMap decodes an object of qsNumbers into the reported values. It
// skips empty entries and entries of any other shape so one odd field
// cannot fail a whole module.
type qsNumberMap map[string]float64

// UnmarshalJSON implements json.Unmarshaler.
func (m *qsNumberMap) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("number map: %w", err)
	}
	out := make(qsNumberMap, len(raw))
	for k, v := range raw {
		var n qsNumber
		if err := json.Unmarshal(v, &n); err == nil && n.Raw != nil {
			out[k] = *n.Raw
		}
	}
	*m = out
	return nil
}

// quoteSummaryResponse mirrors the v10 quoteSummary envelope.
type quoteSummaryResponse struct {
	QuoteSummary struct {
		Result []quoteSummaryResult `json:"result"`
		Error  *apiError            `json:"error"`
	} `json:"quoteSummary"`
}

// quoteSummaryResult holds the modules this package requests. A module
// Yahoo does not report decodes to its zero value, which every accessor
// tolerates, so equity ETFs without bond data and bond ETFs without
// equity data both parse.
type quoteSummaryResult struct {
	FundProfile          qsFundProfile     `json:"fundProfile"`
	TopHoldings          qsTopHoldings     `json:"topHoldings"`
	FundPerformance      qsFundPerformance `json:"fundPerformance"`
	DefaultKeyStatistics qsKeyStatistics   `json:"defaultKeyStatistics"`
	SummaryDetail        qsSummaryDetail   `json:"summaryDetail"`
	Price                qsPrice           `json:"price"`
}

type qsFundProfile struct {
	Family       string `json:"family"`
	CategoryName string `json:"categoryName"`
	LegalType    string `json:"legalType"`
	Fees         struct {
		ExpenseRatio qsNumber `json:"annualReportExpenseRatio"`
		Turnover     qsNumber `json:"annualHoldingsTurnover"`
	} `json:"feesExpensesInvestment"`
}

type qsTopHoldings struct {
	Holdings            []qsHolding   `json:"holdings"`
	SectorWeightings    []qsNumberMap `json:"sectorWeightings"`
	BondRatings         []qsNumberMap `json:"bondRatings"`
	StockPosition       qsNumber      `json:"stockPosition"`
	BondPosition        qsNumber      `json:"bondPosition"`
	CashPosition        qsNumber      `json:"cashPosition"`
	OtherPosition       qsNumber      `json:"otherPosition"`
	PreferredPosition   qsNumber      `json:"preferredPosition"`
	ConvertiblePosition qsNumber      `json:"convertiblePosition"`
	EquityHoldings      qsNumberMap   `json:"equityHoldings"`
	BondHoldings        qsNumberMap   `json:"bondHoldings"`
}

type qsHolding struct {
	Symbol  string   `json:"symbol"`
	Name    string   `json:"holdingName"`
	Percent qsNumber `json:"holdingPercent"`
}

type qsFundPerformance struct {
	TrailingReturns    qsNumberMap `json:"trailingReturns"`
	TrailingReturnsCat qsNumberMap `json:"trailingReturnsCat"`
	AnnualTotalReturns struct {
		Returns    []qsAnnualReturn `json:"returns"`
		ReturnsCat []qsAnnualReturn `json:"returnsCat"`
	} `json:"annualTotalReturns"`
	RiskOverview struct {
		RiskStatistics []qsRiskStats `json:"riskStatistics"`
	} `json:"riskOverviewStatistics"`
}

type qsAnnualReturn struct {
	Year  string   `json:"year"`
	Value qsNumber `json:"annualValue"`
}

type qsRiskStats struct {
	Year             string   `json:"year"`
	Alpha            qsNumber `json:"alpha"`
	Beta             qsNumber `json:"beta"`
	MeanAnnualReturn qsNumber `json:"meanAnnualReturn"`
	RSquared         qsNumber `json:"rSquared"`
	StdDev           qsNumber `json:"stdDev"`
	Sharpe           qsNumber `json:"sharpeRatio"`
	Treynor          qsNumber `json:"treynorRatio"`
}

type qsKeyStatistics struct {
	Category      string   `json:"category"`
	FundFamily    string   `json:"fundFamily"`
	LegalType     string   `json:"legalType"`
	InceptionDate qsNumber `json:"fundInceptionDate"`
	TotalAssets   qsNumber `json:"totalAssets"`
	Yield         qsNumber `json:"yield"`
}

type qsSummaryDetail struct {
	TotalAssets qsNumber `json:"totalAssets"`
	Yield       qsNumber `json:"yield"`
}

type qsPrice struct {
	Symbol    string `json:"symbol"`
	LongName  string `json:"longName"`
	ShortName string `json:"shortName"`
}

// parseQuoteSummary decodes a quoteSummary body. An error object or an
// empty result means Yahoo does not know the symbol.
func parseQuoteSummary(body []byte) (*quoteSummaryResult, error) {
	var resp quoteSummaryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode quoteSummary: %w", err)
	}
	if resp.QuoteSummary.Error != nil {
		return nil, resp.QuoteSummary.Error.err()
	}
	if len(resp.QuoteSummary.Result) == 0 {
		return nil, fmt.Errorf("quoteSummary has no result: %w", market.ErrNotFound)
	}
	return &resp.QuoteSummary.Result[0], nil
}

// fundSummary is one decoded quoteSummary response with the time it was
// fetched, which the market types report as FetchedAt.
type fundSummary struct {
	result    *quoteSummaryResult
	fetchedAt time.Time
}

// fundProfile maps the descriptive modules onto market.FundProfile. The
// fundProfile module is preferred and defaultKeyStatistics or
// summaryDetail fill its gaps. Net assets are taken from totalAssets,
// which is in the fund currency, not from the fundProfile figure, which
// is in millions and often 0.
func (s *fundSummary) fundProfile(symbol string) *market.FundProfile {
	r := s.result
	p := &market.FundProfile{
		Symbol:        symbol,
		Name:          firstNonEmpty(r.Price.LongName, r.Price.ShortName),
		Family:        firstNonEmpty(r.FundProfile.Family, r.DefaultKeyStatistics.FundFamily),
		Category:      firstNonEmpty(r.FundProfile.CategoryName, r.DefaultKeyStatistics.Category),
		LegalType:     firstNonEmpty(r.FundProfile.LegalType, r.DefaultKeyStatistics.LegalType),
		ExpenseRatio:  r.FundProfile.Fees.ExpenseRatio.ptr(),
		Turnover:      r.FundProfile.Fees.Turnover.ptr(),
		NetAssets:     firstNumber(r.DefaultKeyStatistics.TotalAssets, r.SummaryDetail.TotalAssets).ptr(),
		Yield:         firstNumber(r.DefaultKeyStatistics.Yield, r.SummaryDetail.Yield).ptr(),
		InceptionDate: s.inceptionDate(),
		FetchedAt:     s.fetchedAt,
	}
	return p
}

// inceptionDate returns the fund's inception date, or the zero time when
// Yahoo does not report one.
func (s *fundSummary) inceptionDate() time.Time {
	raw := s.result.DefaultKeyStatistics.InceptionDate.Raw
	if raw == nil || *raw == 0 {
		return time.Time{}
	}
	return market.Day(time.Unix(int64(*raw), 0).UTC())
}

// holdings maps the topHoldings module onto market.Holdings. A weight
// block whose every value is zero is dropped: that is how Yahoo pads the
// block that does not apply to a fund (bond ratings on an equity ETF).
// Preferred and convertible positions, which the contract has no field
// for, count as other, so the four mix figures still add up to the whole
// portfolio.
func (s *fundSummary) holdings(symbol string) *market.Holdings {
	th := s.result.TopHoldings
	h := &market.Holdings{
		Symbol:      symbol,
		Sectors:     weights(th.SectorWeightings),
		BondRatings: weights(th.BondRatings),
		StockPct:    th.StockPosition.ptr(),
		BondPct:     th.BondPosition.ptr(),
		CashPct:     th.CashPosition.ptr(),
		OtherPct:    sumReported(th.OtherPosition, th.PreferredPosition, th.ConvertiblePosition),
		EquityStats: stats(th.EquityHoldings),
		BondStats:   stats(th.BondHoldings),
		FetchedAt:   s.fetchedAt,
	}
	for _, x := range th.Holdings {
		if x.Symbol == "" && x.Name == "" {
			continue
		}
		h.Top = append(h.Top, market.Holding{Symbol: x.Symbol, Name: x.Name, Weight: x.Percent.value()})
	}
	return h
}

// weights flattens Yahoo's list of single-key objects into named weights
// in the order given. It returns nil when nothing is reported or every
// weight is zero.
func weights(list []qsNumberMap) []market.Weight {
	var out []market.Weight
	nonZero := false
	for _, entry := range list {
		for _, name := range sortedKeys(entry) {
			w := entry[name]
			out = append(out, market.Weight{Name: name, Weight: w})
			nonZero = nonZero || w != 0
		}
	}
	if !nonZero {
		return nil
	}
	return out
}

// sumReported adds the numbers that were reported, or returns nil when none
// was.
func sumReported(numbers ...qsNumber) *float64 {
	var sum float64
	found := false
	for _, n := range numbers {
		if n.Raw != nil {
			sum += *n.Raw
			found = true
		}
	}
	if !found {
		return nil
	}
	return &sum
}

// stats copies the reported statistics, leaving out exact zeros: Yahoo
// sends 0.0 for a figure it does not have, such as every equity ratio of a
// bond fund or the price-to-earnings figure of TQQQ and ARKK. The equity
// ratios are reciprocals (earnings over price), so a zero would also turn
// into an infinite multiple. It returns nil when nothing is left.
func stats(m qsNumberMap) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		if v != 0 {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortedKeys returns the keys of m in ascending order, so output built
// from a map is deterministic.
func sortedKeys(m qsNumberMap) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// trailingPeriods maps quoteSummary's trailing-return keys onto the
// contract's period names, in display order, with the length of each
// period in months (0 for year to date).
var trailingPeriods = []struct {
	key, period string
	months      int
}{
	{"ytd", "ytd", 0}, {"oneMonth", "1m", 1}, {"threeMonth", "3m", 3}, {"oneYear", "1y", 12},
	{"threeYear", "3y", 36}, {"fiveYear", "5y", 60}, {"tenYear", "10y", 120},
}

// trailingAsOfKey is the entry of the trailing-return block that dates it.
const trailingAsOfKey = "asOfDate"

// performance maps the fundPerformance module onto market.Performance.
// Trailing returns keep the contract's period order, calendar years are
// ascending and risk statistics are ordered by period length.
//
// Yahoo pads a figure it does not have with an exact 0.0 instead of
// leaving it out: every trailing return of a fund it has no returns for
// (DFAC), the periods a young fund has not existed for (JEPQ's 5y and
// 10y), single periods (SCHF's 3m), the whole category block of a fund
// without a category average (TQQQ), and the risk row of a period it does
// not cover. A return reported to seven significant digits is never
// exactly zero, so such a trailing return is treated as not reported
// (nil), and so is a risk row whose statistics are all zero. A fund
// return or risk row for a period longer than the fund has existed is
// dropped too, zero or not. Trailing rows left with neither a fund nor a
// category figure are omitted.
func (s *fundSummary) performance(symbol string) *market.Performance {
	fp := s.result.FundPerformance
	p := &market.Performance{Symbol: symbol, AsOf: trailingAsOf(fp.TrailingReturns), FetchedAt: s.fetchedAt}
	life := lifespan{inception: s.inceptionDate(), end: p.AsOf}
	if life.end.IsZero() {
		life.end = market.Day(s.fetchedAt)
	}
	for _, tp := range trailingPeriods {
		fund := reported(fp.TrailingReturns, tp.key)
		if !life.covers(tp.months) {
			fund = nil
		}
		cat := reported(fp.TrailingReturnsCat, tp.key)
		if fund == nil && cat == nil {
			continue
		}
		p.Trailing = append(p.Trailing, market.PeriodReturn{Period: tp.period, Fund: fund, Category: cat})
	}
	p.Annual = annualReturns(fp.AnnualTotalReturns.Returns, fp.AnnualTotalReturns.ReturnsCat)
	p.Risk = riskStats(fp.RiskOverview.RiskStatistics, life)
	return p
}

// trailingAsOf dates the trailing returns, or returns the zero time when
// their block carries no date. The block's asOfDate is the latest trading
// day, but the returns are monthly figures measured to the end of the
// month before it (asOfDate 2026-10-08, returns to 2026-09-30, as checked
// against the price history), so that month-end is returned. Yahoo does
// not say when it rolls the figures forward, so early in a month they may
// be a month older still.
func trailingAsOf(block qsNumberMap) time.Time {
	raw, ok := block[trailingAsOfKey]
	if !ok || raw <= 0 {
		return time.Time{}
	}
	day := market.Day(time.Unix(int64(raw), 0).UTC())
	return day.AddDate(0, 0, -day.Day()) // the last day of the previous month
}

// reported returns the value of key in block, or nil when the key is
// missing or holds an exact zero, Yahoo's padding for a figure it does not
// have.
func reported(block qsNumberMap, key string) *float64 {
	v, ok := block[key]
	if !ok || v == 0 {
		return nil
	}
	return &v
}

// lifespan is how long a fund had existed when its figures were measured:
// from its inception to end. A zero time means the date is unknown.
type lifespan struct {
	inception, end time.Time
}

// covers reports whether the fund existed for the whole period of the
// given number of months ending at end, 0 months meaning year to date. It
// reports true when either date is unknown.
func (l lifespan) covers(months int) bool {
	if l.inception.IsZero() || l.end.IsZero() {
		return true
	}
	start := time.Date(l.end.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	if months > 0 {
		start = l.end.AddDate(0, -months, 0)
	}
	return !l.inception.After(start)
}

// annualReturns joins the fund's and the category's calendar-year returns
// by year in ascending order. Entries whose year does not parse or whose
// value is not reported are dropped.
func annualReturns(fund, cat []qsAnnualReturn) []market.AnnualReturn {
	byYear := make(map[int]*market.AnnualReturn)
	add := func(list []qsAnnualReturn, set func(*market.AnnualReturn, *float64)) {
		for _, r := range list {
			year, err := strconv.Atoi(strings.TrimSpace(r.Year))
			if err != nil || r.Value.Raw == nil {
				continue
			}
			entry, ok := byYear[year]
			if !ok {
				entry = &market.AnnualReturn{Year: year}
				byYear[year] = entry
			}
			set(entry, r.Value.ptr())
		}
	}
	add(fund, func(a *market.AnnualReturn, v *float64) { a.Fund = v })
	add(cat, func(a *market.AnnualReturn, v *float64) { a.Category = v })
	if len(byYear) == 0 {
		return nil
	}
	out := make([]market.AnnualReturn, 0, len(byYear))
	for _, a := range byYear {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Year < out[j].Year })
	return out
}

// riskStats converts the risk statistics, ordered by period length (3y,
// 5y, 10y). Rows without a period are dropped, and so are rows that are
// padding (see qsRiskStats.padding) and rows for a period longer than the
// fund has existed. A single zero among real statistics is kept: they come
// with two decimals, so a T-bill fund's beta of 0.00 is a real figure.
func riskStats(list []qsRiskStats, life lifespan) []market.RiskStats {
	var out []market.RiskStats
	for _, r := range list {
		period := strings.TrimSpace(r.Year)
		if period == "" || r.padding() {
			continue
		}
		if years := periodYears(period); years != math.MaxInt && !life.covers(12*years) {
			continue
		}
		out = append(out, market.RiskStats{
			Period:           period,
			Alpha:            r.Alpha.ptr(),
			Beta:             r.Beta.ptr(),
			MeanAnnualReturn: r.MeanAnnualReturn.ptr(),
			RSquared:         r.RSquared.ptr(),
			StdDev:           r.StdDev.ptr(),
			Sharpe:           r.Sharpe.ptr(),
			Treynor:          r.Treynor.ptr(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return periodYears(out[i].Period) < periodYears(out[j].Period) })
	return out
}

// padding reports whether every statistic of r is missing or exactly zero,
// which is how Yahoo fills the row of a period it has no statistics for;
// no fund that traded through the period has seven zero statistics.
func (r qsRiskStats) padding() bool {
	for _, n := range []qsNumber{r.Alpha, r.Beta, r.MeanAnnualReturn, r.RSquared, r.StdDev, r.Sharpe, r.Treynor} {
		if n.value() != 0 {
			return false
		}
	}
	return true
}

// periodYears parses the leading integer of a period such as "3y";
// periods without one sort last.
func periodYears(period string) int {
	n, err := strconv.Atoi(strings.TrimSuffix(period, "y"))
	if err != nil {
		return math.MaxInt
	}
	return n
}

// quoteResponse mirrors the v7 quote envelope. Unlike quoteSummary, its
// numbers are plain.
type quoteResponse struct {
	QuoteResponse struct {
		Result []v7Quote `json:"result"`
		Error  *apiError `json:"error"`
	} `json:"quoteResponse"`
}

type v7Quote struct {
	Symbol                     string  `json:"symbol"`
	LongName                   string  `json:"longName"`
	ShortName                  string  `json:"shortName"`
	Currency                   string  `json:"currency"`
	Exchange                   string  `json:"exchange"`
	FullExchangeName           string  `json:"fullExchangeName"`
	MarketState                string  `json:"marketState"`
	RegularMarketPrice         float64 `json:"regularMarketPrice"`
	RegularMarketChange        float64 `json:"regularMarketChange"`
	RegularMarketChangePercent float64 `json:"regularMarketChangePercent"`
	RegularMarketPreviousClose float64 `json:"regularMarketPreviousClose"`
	RegularMarketOpen          float64 `json:"regularMarketOpen"`
	RegularMarketDayLow        float64 `json:"regularMarketDayLow"`
	RegularMarketDayHigh       float64 `json:"regularMarketDayHigh"`
	RegularMarketVolume        float64 `json:"regularMarketVolume"`
	RegularMarketTime          int64   `json:"regularMarketTime"`
	FiftyTwoWeekLow            float64 `json:"fiftyTwoWeekLow"`
	FiftyTwoWeekHigh           float64 `json:"fiftyTwoWeekHigh"`
	FiftyDayAverage            float64 `json:"fiftyDayAverage"`
	TwoHundredDayAverage       float64 `json:"twoHundredDayAverage"`
	// DividendYield is in percent (3.24 means 3.24%). The record also has
	// trailingAnnualDividendYield, which is not read: for many ETFs it is
	// 0 (SCHD, JEPI) or years out of date (VOO's rate is its 2021 total).
	DividendYield float64 `json:"dividendYield"`
}

// parseQuotes decodes a v7 quote body into market.Quotes in Yahoo's order.
// Symbols Yahoo does not know are simply absent, and so are the stubs it
// returns for some symbols it cannot quote, such as BRK.B (Yahoo's ticker
// is BRK-B): a record without a price is not a quote.
func parseQuotes(body []byte) ([]market.Quote, error) {
	var resp quoteResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode quote: %w", err)
	}
	if resp.QuoteResponse.Error != nil {
		return nil, resp.QuoteResponse.Error.err()
	}
	out := make([]market.Quote, 0, len(resp.QuoteResponse.Result))
	for i := range resp.QuoteResponse.Result {
		q := &resp.QuoteResponse.Result[i]
		if q.Symbol == "" || q.RegularMarketPrice == 0 {
			continue
		}
		out = append(out, q.quote())
	}
	return out, nil
}

// quote maps a v7 record onto market.Quote.
func (q *v7Quote) quote() market.Quote {
	mq := market.Quote{
		Symbol:               q.Symbol,
		Name:                 firstNonEmpty(q.LongName, q.ShortName),
		Currency:             q.Currency,
		Exchange:             firstNonEmpty(q.FullExchangeName, q.Exchange),
		MarketState:          q.MarketState,
		Price:                q.RegularMarketPrice,
		Change:               q.RegularMarketChange,
		ChangePct:            q.RegularMarketChangePercent,
		PreviousClose:        q.RegularMarketPreviousClose,
		Open:                 q.RegularMarketOpen,
		DayLow:               q.RegularMarketDayLow,
		DayHigh:              q.RegularMarketDayHigh,
		Volume:               int64(q.RegularMarketVolume),
		FiftyTwoWeekLow:      q.FiftyTwoWeekLow,
		FiftyTwoWeekHigh:     q.FiftyTwoWeekHigh,
		FiftyDayAverage:      q.FiftyDayAverage,
		TwoHundredDayAverage: q.TwoHundredDayAverage,
		DividendYield:        q.DividendYield / 100, // percent to fraction
	}
	if q.RegularMarketTime != 0 {
		mq.AsOf = time.Unix(q.RegularMarketTime, 0).UTC()
	}
	return mq
}

// searchResponse mirrors the blocks of the search payload that are read.
type searchResponse struct {
	Quotes []searchQuote `json:"quotes"`
	News   []searchNews  `json:"news"`
}

type searchQuote struct {
	Symbol    string `json:"symbol"`
	ShortName string `json:"shortname"`
	LongName  string `json:"longname"`
	QuoteType string `json:"quoteType"`
	Exchange  string `json:"exchange"`
	ExchDisp  string `json:"exchDisp"`
}

type searchNews struct {
	Title       string `json:"title"`
	Publisher   string `json:"publisher"`
	Link        string `json:"link"`
	PublishTime int64  `json:"providerPublishTime"`
}

// parseSearch decodes a search body.
func parseSearch(body []byte) (*searchResponse, error) {
	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	return &resp, nil
}

// hits converts the quotes block into at most limit search hits, dropping
// entries without a symbol.
func (r *searchResponse) hits(limit int) []market.SearchHit {
	out := make([]market.SearchHit, 0, min(limit, len(r.Quotes)))
	for _, q := range r.Quotes {
		if len(out) == limit {
			break
		}
		if q.Symbol == "" {
			continue
		}
		out = append(out, market.SearchHit{
			Symbol:   q.Symbol,
			Name:     firstNonEmpty(q.LongName, q.ShortName),
			Type:     q.QuoteType,
			Exchange: firstNonEmpty(q.ExchDisp, q.Exchange),
		})
	}
	return out
}

// items converts the news block into at most limit news items, dropping
// entries without a title.
func (r *searchResponse) items(limit int) []market.NewsItem {
	out := make([]market.NewsItem, 0, min(limit, len(r.News)))
	for _, n := range r.News {
		if len(out) == limit {
			break
		}
		if n.Title == "" {
			continue
		}
		item := market.NewsItem{Title: n.Title, Publisher: n.Publisher, Link: n.Link}
		if n.PublishTime != 0 {
			item.PublishedAt = time.Unix(n.PublishTime, 0).UTC()
		}
		out = append(out, item)
	}
	return out
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
