package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Limits of get_price_history.
const (
	defaultMaxPoints = 300
	maxMaxPoints     = 2000
)

// Intervals of get_price_history.
const (
	intervalDaily   = "daily"
	intervalWeekly  = "weekly"
	intervalMonthly = "monthly"
)

type getPriceHistoryInput struct {
	Symbol    string `json:"symbol" jsonschema:"ticker symbol, e.g. VOO"`
	Start     string `json:"start,omitempty" jsonschema:"first date YYYY-MM-DD, inclusive (default: the first bar)"`
	End       string `json:"end,omitempty" jsonschema:"last date YYYY-MM-DD, inclusive (default: the last bar)"`
	Interval  string `json:"interval,omitempty" jsonschema:"daily, weekly (last bar of each ISO week) or monthly (last bar of each calendar month); default monthly"`
	MaxPoints int    `json:"max_points,omitempty" jsonschema:"maximum number of points, 1 to 2000 (default 300); when the interval yields more, every k-th point plus the last is kept and downsampled is true"`
}

// pricePoint is one bar on the wire.
type pricePoint struct {
	Date     string  `json:"date"`
	Close    float64 `json:"close"`
	AdjClose float64 `json:"adj_close"`
	Dividend float64 `json:"dividend"`
}

type getPriceHistoryOutput struct {
	Symbol      string       `json:"symbol"`
	Currency    string       `json:"currency"`
	Interval    string       `json:"interval"`
	Count       int          `json:"count"`
	From        string       `json:"from"`
	To          string       `json:"to"`
	Downsampled bool         `json:"downsampled"`
	Points      []pricePoint `json:"points"`
}

func registerGetPriceHistory(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_price_history",
		Description: "Daily, weekly or monthly price points of one symbol (close, dividend-adjusted close and cash dividend per share), for charts or custom calculations. Weekly and monthly keep the last bar of each ISO week or calendar month. At most max_points are returned; when more would be needed, every k-th point plus the last is kept and downsampled is true. Prices are in the symbol's currency (USD for US ETFs, KRW per USD for KRW=X).",
		Annotations: readOnly("Get price history"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getPriceHistoryInput) (*mcp.CallToolResult, getPriceHistoryOutput, error) {
		out, err := d.getPriceHistory(ctx, in)
		return nil, out, err
	})
}

// getPriceHistory selects, buckets and thins the bars of one symbol.
func (d Deps) getPriceHistory(ctx context.Context, in getPriceHistoryInput) (getPriceHistoryOutput, error) {
	interval, err := parseInterval(in.Interval)
	if err != nil {
		return getPriceHistoryOutput{}, err
	}
	limit, err := parseMaxPoints(in.MaxPoints)
	if err != nil {
		return getPriceHistoryOutput{}, err
	}
	start, err := parseOptionalDate("start", in.Start)
	if err != nil {
		return getPriceHistoryOutput{}, err
	}
	end, err := parseOptionalDate("end", in.End)
	if err != nil {
		return getPriceHistoryOutput{}, err
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		return getPriceHistoryOutput{}, fmt.Errorf("end %s is before start %s", formatDate(end), formatDate(start))
	}
	s, err := d.fetchSeries(ctx, in.Symbol)
	if err != nil {
		return getPriceHistoryOutput{}, err
	}

	bars := s.Between(start, end)
	if len(bars) == 0 {
		first, _ := s.First()
		last, _ := s.Last()
		return getPriceHistoryOutput{}, fmt.Errorf("no bars for %s between %s and %s; data covers %s to %s",
			s.Meta.Symbol, in.Start, in.End, formatDate(first.Date), formatDate(last.Date))
	}
	bars = bucket(bars, interval)
	bars, downsampled := downsample(bars, limit)

	points := make([]pricePoint, 0, len(bars))
	for _, b := range bars {
		points = append(points, pricePoint{
			Date:     formatDate(b.Date),
			Close:    round2(b.Close),
			AdjClose: round2(b.AdjClose),
			Dividend: round4(b.Dividend),
		})
	}
	return getPriceHistoryOutput{
		Symbol:      s.Meta.Symbol,
		Currency:    s.Meta.Currency,
		Interval:    interval,
		Count:       len(points),
		From:        points[0].Date,
		To:          points[len(points)-1].Date,
		Downsampled: downsampled,
		Points:      points,
	}, nil
}

// parseInterval validates the interval, defaulting to monthly.
func parseInterval(s string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "":
		return intervalMonthly, nil
	case intervalDaily, intervalWeekly, intervalMonthly:
		return v, nil
	default:
		return "", fmt.Errorf("interval %q is not supported; use daily, weekly or monthly", s)
	}
}

// parseMaxPoints validates max_points, defaulting to 300.
func parseMaxPoints(n int) (int, error) {
	switch {
	case n == 0:
		return defaultMaxPoints, nil
	case n < 1 || n > maxMaxPoints:
		return 0, fmt.Errorf("max_points must be between 1 and %d, got %d", maxMaxPoints, n)
	default:
		return n, nil
	}
}

// bucket keeps the last bar of each period of the interval; daily keeps
// every bar.
func bucket(bars []market.Bar, interval string) []market.Bar {
	if interval == intervalDaily {
		return bars
	}
	period := func(t time.Time) [2]int {
		if interval == intervalWeekly {
			y, w := t.ISOWeek()
			return [2]int{y, w}
		}
		return [2]int{t.Year(), int(t.Month())}
	}
	out := make([]market.Bar, 0, len(bars))
	for i, b := range bars {
		if i == len(bars)-1 || period(b.Date) != period(bars[i+1].Date) {
			out = append(out, b)
		}
	}
	return out
}

// downsample keeps every k-th bar plus the last so that at most limit bars
// remain, and reports whether anything was dropped. k is the smallest
// stride that fits: ceil((n-1) / (limit-1)).
func downsample(bars []market.Bar, limit int) ([]market.Bar, bool) {
	n := len(bars)
	if n <= limit {
		return bars, false
	}
	if limit == 1 {
		return bars[n-1:], true
	}
	k := (n + limit - 3) / (limit - 1)
	out := make([]market.Bar, 0, limit)
	for i := 0; i < n; i += k {
		out = append(out, bars[i])
	}
	if (n-1)%k != 0 {
		out = append(out, bars[n-1])
	}
	return out, true
}
