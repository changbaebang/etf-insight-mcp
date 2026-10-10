package tools

import (
	"context"
	"fmt"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
)

// getTechnicalIndicatorsDescription is written for the model choosing a tool.
const getTechnicalIndicatorsDescription = "Common chart indicators of one symbol on one day (default: the latest bar), all on the raw close: 14-day Wilder RSI (0 to 100), MACD 12/26/9 (line, signal line and histogram, in price units), 20-day Bollinger bands at 2 standard deviations with %B (bollinger_pct_b: 0 at the lower band, 1 at the upper band), 14-day average true range, 20/50/200-day simple and 12/26-day exponential moving averages, rule hits in plain words (RSI beyond 70/30, MACD and 50/200-day crossovers within the last 10 bars, close outside the bands or versus the 200-day average) and the same trend block as get_etf_info. These describe the recent price path; they are not predictions and not trading signals to act on. An indicator without enough history is 0 and named in signals. For returns, drawdowns and dividends use get_etf_info."

type getTechnicalIndicatorsInput struct {
	Symbol string `json:"symbol" jsonschema:"ticker symbol such as VOO or QQQ, case-insensitive; a symbol outside the universe is still looked up in the data source"`
	AsOf   string `json:"as_of,omitempty" jsonschema:"date YYYY-MM-DD; the indicators are computed on the last trading day on or before it from earlier bars only (default: the latest bar)"`
}

type getTechnicalIndicatorsOutput struct {
	Symbol          string      `json:"symbol"`
	Name            string      `json:"name"`
	Currency        string      `json:"currency" jsonschema:"currency of every price-level field"`
	AsOf            string      `json:"as_of" jsonschema:"the trading day the indicators describe"`
	Bars            int         `json:"bars" jsonschema:"bars of history up to as_of the indicators could use"`
	Close           float64     `json:"close"`
	RSI14           float64     `json:"rsi_14" jsonschema:"14-day Wilder RSI, 0 to 100; above 70 is conventionally called overbought and below 30 oversold"`
	MACD            float64     `json:"macd" jsonschema:"12-day minus 26-day exponential average of the close, in price units"`
	MACDSignal      float64     `json:"macd_signal" jsonschema:"9-day exponential average of macd"`
	MACDHist        float64     `json:"macd_hist" jsonschema:"macd minus macd_signal; positive while macd is above its signal line"`
	BollingerMiddle float64     `json:"bollinger_middle" jsonschema:"20-day simple average of the close"`
	BollingerUpper  float64     `json:"bollinger_upper" jsonschema:"bollinger_middle plus 2 standard deviations of the same 20 closes"`
	BollingerLower  float64     `json:"bollinger_lower" jsonschema:"bollinger_middle minus 2 standard deviations"`
	BollingerPctB   float64     `json:"bollinger_pct_b" jsonschema:"position of the close in the bands as a fraction: 0 at the lower band, 1 at the upper band, outside 0..1 beyond them"`
	ATR14           float64     `json:"atr_14" jsonschema:"14-day average true range in price units (the typical daily high-low range, gaps included)"`
	SMA20           float64     `json:"sma_20"`
	SMA50           float64     `json:"sma_50"`
	SMA200          float64     `json:"sma_200"`
	EMA12           float64     `json:"ema_12"`
	EMA26           float64     `json:"ema_26"`
	Signals         []string    `json:"signals" jsonschema:"rule hits on as_of in plain words, plus a note for each indicator that lacked history and is reported as 0"`
	Trend           trendOutput `json:"trend" jsonschema:"rule-based trend reading, the same as get_etf_info's"`
	Warnings        []string    `json:"warnings,omitempty"`
	Disclaimer      string      `json:"disclaimer"`
}

// getTechnicalIndicators computes the indicator snapshot of one symbol.
func (d Deps) getTechnicalIndicators(ctx context.Context, in getTechnicalIndicatorsInput) (getTechnicalIndicatorsOutput, error) {
	asOf, err := parseOptionalDate("as_of", in.AsOf)
	if err != nil {
		return getTechnicalIndicatorsOutput{}, err
	}
	s, err := d.fetchSeries(ctx, in.Symbol)
	if err != nil {
		return getTechnicalIndicatorsOutput{}, err
	}
	tech, err := analytics.ComputeTechnicals(s, asOf)
	if err != nil {
		return getTechnicalIndicatorsOutput{}, withDataRange(err, s)
	}
	trend, err := analytics.AnalyzeTrend(s, asOf)
	if err != nil {
		return getTechnicalIndicatorsOutput{}, withDataRange(err, s)
	}
	idx, _ := s.IndexOn(tech.AsOf)

	out := getTechnicalIndicatorsOutput{
		Symbol:          s.Meta.Symbol,
		Name:            s.Meta.Name,
		Currency:        s.Meta.Currency,
		AsOf:            formatDate(tech.AsOf),
		Bars:            idx + 1,
		Close:           round2(tech.Close),
		RSI14:           round2(tech.RSI14),
		MACD:            round4(tech.MACD),
		MACDSignal:      round4(tech.MACDSignal),
		MACDHist:        round4(tech.MACDHist),
		BollingerMiddle: round2(tech.BollingerMiddle),
		BollingerUpper:  round2(tech.BollingerUpper),
		BollingerLower:  round2(tech.BollingerLower),
		BollingerPctB:   round4(tech.BollingerPctB),
		ATR14:           round4(tech.ATR14),
		SMA20:           round2(tech.SMA20),
		SMA50:           round2(tech.SMA50),
		SMA200:          round2(tech.SMA200),
		EMA12:           round2(tech.EMA12),
		EMA26:           round2(tech.EMA26),
		Signals:         append([]string{}, tech.Signals...),
		Trend:           toTrendOutput(trend),
		Warnings:        d.staleWarnings(s.Meta.Symbol),
		Disclaimer:      Disclaimer,
	}
	if last, _ := s.Last(); asOf.IsZero() {
		if age := d.clock().Sub(last.Date); age > staleAfter {
			out.Warnings = append(out.Warnings, fmt.Sprintf("latest bar %s is %d days old", formatDate(last.Date), int(age.Hours()/24)))
		}
	}
	return out, nil
}
