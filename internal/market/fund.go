package market

import (
	"context"
	"fmt"
	"time"
)

// Split is a share split event. A 2-for-1 split has Numerator 2 and
// Denominator 1. Prices in a Series are already split-adjusted by the
// provider; splits are kept for display and for detecting when cached
// history must be refetched.
type Split struct {
	Date        time.Time
	Numerator   float64
	Denominator float64
}

// Ratio renders the split as "2:1".
func (s Split) Ratio() string {
	return fmt.Sprintf("%s:%s", trimFloat(s.Numerator), trimFloat(s.Denominator))
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

// Quote is a point-in-time quote for one symbol. Zero values mean the
// provider did not report the field.
type Quote struct {
	Symbol        string
	Name          string
	Currency      string
	Exchange      string
	MarketState   string // e.g. "REGULAR", "CLOSED", "PRE", "POST"
	Price         float64
	Change        float64
	ChangePct     float64 // percent, e.g. -0.24
	PreviousClose float64
	Open          float64
	DayLow        float64
	DayHigh       float64
	Volume        int64
	// Fifty-two-week range and moving averages as reported by the provider.
	FiftyTwoWeekLow      float64
	FiftyTwoWeekHigh     float64
	FiftyDayAverage      float64
	TwoHundredDayAverage float64
	// DividendYield is the trailing annual dividend yield as a fraction
	// (0.012 = 1.2%).
	DividendYield float64
	AsOf          time.Time
}

// FundProfile is descriptive fund data. Pointer fields are nil when the
// provider did not report them; fractions are plain fractions (0.0003 =
// 0.03%).
type FundProfile struct {
	Symbol        string
	Name          string
	Family        string // e.g. "Vanguard"
	Category      string // provider category, e.g. "Large Blend"
	LegalType     string // e.g. "Exchange Traded Fund"
	InceptionDate time.Time
	ExpenseRatio  *float64
	Turnover      *float64
	NetAssets     *float64 // in the fund currency
	Yield         *float64
	FetchedAt     time.Time
}

// Holding is one position of a fund with its portfolio weight as a
// fraction.
type Holding struct {
	Symbol string
	Name   string
	Weight float64
}

// Weight is a named weight (sector, bond rating, ...) as a fraction.
type Weight struct {
	Name   string
	Weight float64
}

// Holdings is the portfolio composition of a fund. Equity funds carry
// Sectors and EquityStats; bond funds carry BondRatings and BondStats.
type Holdings struct {
	Symbol      string
	Top         []Holding
	Sectors     []Weight
	BondRatings []Weight
	StockPct    *float64 // fractions of the portfolio
	BondPct     *float64
	CashPct     *float64
	OtherPct    *float64
	EquityStats map[string]float64 // e.g. "priceToEarnings", "priceToBook"
	BondStats   map[string]float64 // e.g. "duration", "maturity"
	FetchedAt   time.Time
}

// PeriodReturn is a trailing return of the fund and its category for a
// named period ("ytd", "1m", "3m", "1y", "3y", "5y", "10y") as fractions.
type PeriodReturn struct {
	Period   string
	Fund     *float64
	Category *float64
}

// AnnualReturn is a calendar-year return of the fund and its category.
type AnnualReturn struct {
	Year     int
	Fund     *float64
	Category *float64
}

// RiskStats are provider-computed risk statistics for a period ("3y",
// "5y", "10y").
type RiskStats struct {
	Period           string
	Alpha            *float64
	Beta             *float64
	MeanAnnualReturn *float64
	RSquared         *float64
	StdDev           *float64
	Sharpe           *float64
	Treynor          *float64
}

// Performance is provider-reported fund performance.
type Performance struct {
	Symbol    string
	Trailing  []PeriodReturn
	Annual    []AnnualReturn
	Risk      []RiskStats
	FetchedAt time.Time
}

// SearchHit is one symbol lookup result.
type SearchHit struct {
	Symbol   string
	Name     string
	Type     string // "ETF", "EQUITY", "INDEX", ...
	Exchange string
}

// NewsItem is one news headline.
type NewsItem struct {
	Title       string
	Publisher   string
	Link        string
	PublishedAt time.Time
}

// FundSource supplies fund descriptions, quotes, search and news.
// Implementations must be safe for concurrent use. Unknown symbols return
// an error wrapping ErrNotFound; Quote simply omits unknown symbols.
type FundSource interface {
	Quote(ctx context.Context, symbols []string) ([]Quote, error)
	FundProfile(ctx context.Context, symbol string) (*FundProfile, error)
	Holdings(ctx context.Context, symbol string) (*Holdings, error)
	Performance(ctx context.Context, symbol string) (*Performance, error)
	Search(ctx context.Context, query string, limit int) ([]SearchHit, error)
	News(ctx context.Context, query string, limit int) ([]NewsItem, error)
}

// RangeSource is a Source that can also fetch a bounded date range, which
// lets a cache top up stored history instead of refetching all of it.
// from and to are inclusive calendar dates.
type RangeSource interface {
	Source
	SeriesRange(ctx context.Context, symbol string, from, to time.Time) (*Series, error)
}
