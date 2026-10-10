package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// staleAfter is how old the latest bar may be before get_etf_info warns
// that the data may be lagging.
const staleAfter = 7 * 24 * time.Hour

type getETFInfoInput struct {
	Symbol string `json:"symbol" jsonschema:"ticker symbol such as VOO or SCHD, case-insensitive; a symbol outside the universe is still looked up in the data source"`
	AsOf   string `json:"as_of,omitempty" jsonschema:"snapshot date YYYY-MM-DD; the last trading day on or before it is used, so a later date gives the latest bar with a warning (default: the latest bar)"`
}

// metaOutput is what the data provider says about the symbol.
type metaOutput struct {
	Name           string `json:"name"`
	Exchange       string `json:"exchange"`
	Currency       string `json:"currency"`
	InstrumentType string `json:"instrument_type"`
	FirstTradeDate string `json:"first_trade_date"`
}

// dataRangeOutput describes the history the server holds for the symbol.
type dataRangeOutput struct {
	FirstDate string `json:"first_date"`
	LastDate  string `json:"last_date"`
	Bars      int    `json:"bars"`
}

// windowOutput is one trailing-return window of analytics.Summary.
type windowOutput struct {
	Label          string   `json:"label" jsonschema:"1m, 3m, 6m, 1y, 3y, 5y, 10y or max"`
	TotalReturnPct float64  `json:"total_return_pct" jsonschema:"total return over the window with dividends reinvested"`
	AnnualizedPct  *float64 `json:"annualized_pct,omitempty" jsonschema:"compound annual growth rate; only present for windows of at least one year, shorter windows are not extrapolated"`
	From           string   `json:"from"`
	Available      bool     `json:"available" jsonschema:"false when the history does not reach back far enough; the other fields are then zero"`
}

// summaryOutput is analytics.Summary on the wire.
type summaryOutput struct {
	AsOf                string         `json:"as_of"`
	Close               float64        `json:"close"`
	AdjClose            float64        `json:"adj_close"`
	Windows             []windowOutput `json:"windows"`
	Volatility1YPct     float64        `json:"volatility_1y_pct"`
	MaxDrawdown1YPct    float64        `json:"max_drawdown_1y_pct"`
	MaxDrawdownAllPct   float64        `json:"max_drawdown_all_pct"`
	TTMDividendPerShare float64        `json:"ttm_dividend_per_share"`
	TTMDividendYieldPct float64        `json:"ttm_dividend_yield_pct"`
	High52W             float64        `json:"high_52w"`
	Low52W              float64        `json:"low_52w"`
}

// trendOutput is analytics.Trend on the wire.
type trendOutput struct {
	AsOf             string   `json:"as_of"`
	Close            float64  `json:"close"`
	SMA50            float64  `json:"sma_50"`
	SMA200           float64  `json:"sma_200"`
	PctVsSMA50       float64  `json:"pct_vs_sma_50"`
	PctVsSMA200      float64  `json:"pct_vs_sma_200"`
	Momentum121Pct   float64  `json:"momentum_12_1_pct" jsonschema:"return from 12 months ago to 1 month ago; 0 when fewer than 253 bars exist (then named in reasons)"`
	Return6MPct      float64  `json:"return_6m_pct" jsonschema:"return over the last 126 bars; 0 when the history is shorter (then named in reasons)"`
	Volatility20DPct float64  `json:"volatility_20d_pct"`
	Volatility1YPct  float64  `json:"volatility_1y_pct"`
	SMA200SlopePct   float64  `json:"sma_200_slope_pct" jsonschema:"change of the 200-day average over the last 20 bars; 0 when fewer than 220 bars exist"`
	State            string   `json:"state" jsonschema:"uptrend, downtrend, sideways or insufficient-history; a description of the recent price path, not a prediction"`
	Reasons          []string `json:"reasons"`
}

type getETFInfoOutput struct {
	Symbol   string          `json:"symbol"`
	Known    bool            `json:"known"`
	Universe *etfRow         `json:"universe,omitempty"`
	Meta     metaOutput      `json:"meta"`
	Data     dataRangeOutput `json:"data"`
	Summary  summaryOutput   `json:"summary"`
	Trend    trendOutput     `json:"trend"`
	Warnings []string        `json:"warnings,omitempty"`
}

func registerGetETFInfo(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_etf_info",
		Description: "Snapshot of one ETF: its universe entry (known=false for symbols outside the universe that the data source still knows), provider metadata, the date range held, trailing total returns (1m to max, dividends reinvested), 1-year volatility, drawdowns, trailing-12-month dividends, 52-week range, and a rule-based trend reading (50/200-day averages, 12-1 momentum, state uptrend/downtrend/sideways with reasons). Percentages are plain numbers (7.5 = 7.5%). Warnings flag a fund with less than 52 weeks of history (its 1-year, trailing-12-month and 52-week figures then cover a shorter span), an index or exchange rate (prices only, not an investable fund), an as_of after the latest bar, and old data. It does not list holdings or costs: use get_holdings for what the fund holds and get_fund_profile for its expense ratio and category. Fetches the symbol's full daily history from Yahoo Finance on first use and caches it.",
		Title:       "Get ETF info",
		Annotations: readOnly("Get ETF info", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getETFInfoInput) (*mcp.CallToolResult, getETFInfoOutput, error) {
		out, err := d.getETFInfo(ctx, in)
		return nil, out, err
	})
}

// getETFInfo builds the snapshot as of in.AsOf (default: latest bar).
func (d Deps) getETFInfo(ctx context.Context, in getETFInfoInput) (getETFInfoOutput, error) {
	asOf, err := parseOptionalDate("as_of", in.AsOf)
	if err != nil {
		return getETFInfoOutput{}, err
	}
	s, err := d.fetchSeries(ctx, in.Symbol)
	if err != nil {
		return getETFInfoOutput{}, err
	}
	first, _ := s.First()
	last, _ := s.Last()
	summary, err := analytics.Summarize(s, asOf)
	if err != nil {
		return getETFInfoOutput{}, withDataRange(err, s)
	}
	trend, err := analytics.AnalyzeTrend(s, asOf)
	if err != nil {
		return getETFInfoOutput{}, withDataRange(err, s)
	}

	out := getETFInfoOutput{
		Symbol: s.Meta.Symbol,
		Meta: metaOutput{
			Name:           s.Meta.Name,
			Exchange:       s.Meta.Exchange,
			Currency:       s.Meta.Currency,
			InstrumentType: s.Meta.InstrumentType,
			FirstTradeDate: formatDate(s.Meta.FirstTradeDate),
		},
		Data: dataRangeOutput{
			FirstDate: formatDate(first.Date),
			LastDate:  formatDate(last.Date),
			Bars:      s.Len(),
		},
		Summary: toSummaryOutput(summary),
		Trend:   toTrendOutput(trend),
	}
	if e, ok := universe.Get(s.Meta.Symbol); ok {
		row := toRow(e)
		out.Known, out.Universe = true, &row
	}
	out.Warnings = append(out.Warnings, d.staleWarnings(s.Meta.Symbol)...)
	out.Warnings = append(out.Warnings, nonEmpty(provisionalNote(s, asOf, d.clock()))...)
	out.Warnings = append(out.Warnings, nonEmpty(d.inceptionWarning(ctx, s))...)
	if note := instrumentNote(s); note != "" {
		out.Warnings = append(out.Warnings, note)
	}
	if !hasTrailingYear(s, asOf) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s has less than 52 weeks of history up to %s (from %s): volatility_1y_pct, max_drawdown_1y_pct, ttm_dividend_per_share, ttm_dividend_yield_pct and the 52-week high and low cover only that span, not a full year, so they are not comparable with other funds' full-year figures",
			s.Meta.Symbol, out.Summary.AsOf, formatDate(first.Date)))
	}
	if asOf.After(last.Date) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("as_of %s is after the latest bar; the snapshot is as of %s", formatDate(asOf), formatDate(last.Date)))
	}
	// The data should be recent when the snapshot is meant to be current:
	// no as_of, or one past the latest bar. The age is then measured up to
	// as_of when that is earlier than today.
	if asOf.IsZero() || asOf.After(last.Date) {
		ref := d.clock()
		if !asOf.IsZero() && asOf.Before(ref) {
			ref = asOf
		}
		if age := ref.Sub(last.Date); age > staleAfter {
			out.Warnings = append(out.Warnings, fmt.Sprintf("latest bar %s is %d days old", formatDate(last.Date), int(age.Hours()/24)))
		}
	}
	return out, nil
}

// withDataRange appends the available date range to an insufficient-
// history error so the caller can fix as_of without a second call.
func withDataRange(err error, s *market.Series) error {
	if !errors.Is(err, analytics.ErrInsufficientHistory) {
		return userError(err)
	}
	first, _ := s.First()
	last, _ := s.Last()
	return fmt.Errorf("%w; data covers %s to %s", userError(err), formatDate(first.Date), formatDate(last.Date))
}

// toSummaryOutput converts fractions to percentages and rounds.
func toSummaryOutput(s analytics.Summary) summaryOutput {
	windows := make([]windowOutput, 0, len(s.Windows))
	for _, w := range s.Windows {
		wo := windowOutput{
			Label:          w.Label,
			TotalReturnPct: pct(w.TotalReturn),
			From:           formatDate(w.From),
			Available:      w.Available,
		}
		if w.AnnualizedAvailable {
			wo.AnnualizedPct = ptr(pct(w.Annualized))
		}
		windows = append(windows, wo)
	}
	return summaryOutput{
		AsOf:                formatDate(s.AsOf),
		Close:               round2(s.Close),
		AdjClose:            round2(s.AdjClose),
		Windows:             windows,
		Volatility1YPct:     pct(s.Volatility1Y),
		MaxDrawdown1YPct:    pct(s.MaxDrawdown1Y),
		MaxDrawdownAllPct:   pct(s.MaxDrawdownAll),
		TTMDividendPerShare: round4(s.TTMDividendPerShare),
		TTMDividendYieldPct: pct(s.TTMDividendYield),
		High52W:             round2(s.High52W),
		Low52W:              round2(s.Low52W),
	}
}

// toTrendOutput converts fractions to percentages and rounds. PctVsSMA*
// are already percentages.
func toTrendOutput(t analytics.Trend) trendOutput {
	return trendOutput{
		AsOf:             formatDate(t.AsOf),
		Close:            round2(t.Close),
		SMA50:            round2(t.SMA50),
		SMA200:           round2(t.SMA200),
		PctVsSMA50:       round2(t.PctVsSMA50),
		PctVsSMA200:      round2(t.PctVsSMA200),
		Momentum121Pct:   pct(t.Momentum12_1),
		Return6MPct:      pct(t.Return6M),
		Volatility20DPct: pct(t.Volatility20D),
		Volatility1YPct:  pct(t.Volatility1Y),
		SMA200SlopePct:   pct(t.SMA200Slope),
		State:            t.State,
		Reasons:          append([]string{}, t.Reasons...),
	}
}

// inceptionLookupTimeout bounds the optional fund-profile lookup of
// get_etf_info; the snapshot itself never waits longer for it.
const inceptionLookupTimeout = 5 * time.Second

// inceptionWarning compares the fund's inception date, when the fund data
// source reports one, with the first bar of s: a history that starts long
// before the fund (a predecessor product) or long after it is flagged.
// Any failure yields "": the snapshot does not depend on fund data.
func (d Deps) inceptionWarning(ctx context.Context, s *market.Series) string {
	if d.Fund == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, inceptionLookupTimeout)
	defer cancel()
	p, err := d.Fund.FundProfile(ctx, s.Meta.Symbol)
	if err != nil || p == nil {
		return ""
	}
	first, _ := s.First()
	last, _ := s.Last()
	return inceptionNote(p.InceptionDate, first.Date, last.Date)
}
