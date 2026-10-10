package tools

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAnalysisToolsAreListed(t *testing.T) {
	sess := newSession(t, analysisDeps(newAnalysisSource(), nil))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range analysisToolNames {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("tool %s is not listed", name)
			continue
		}
		if tool.Description == "" || tool.Title == "" {
			t.Errorf("tool %s lacks a description or title", name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
			t.Errorf("tool %s annotations = %+v, want read-only and open-world", name, tool.Annotations)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Errorf("tool %s input schema is %T, want object", name, tool.InputSchema)
			continue
		}
		checkDescribed(t, name, "", schema)
	}

	for name, want := range map[string][]string{
		"get_technical_indicators": {"not predictions", "RSI", "MACD", "Bollinger"},
		"compare_etfs":             {"default SPY", "common_range", "null when unavailable"},
		"screen_universe":          {"COLD START", "refresh_prices", "category", fmt.Sprintf("max %d", len(universe.All())), "not a recommendation"},
		"find_alternatives":        {"not advice", "min_correlation", "already net of expense ratios", "skipped"},
	} {
		for _, w := range want {
			if tool := byName[name]; tool != nil && !strings.Contains(tool.Description, w) {
				t.Errorf("%s description lacks %q", name, w)
			}
		}
	}

	props := func(name string) map[string]any {
		schema, _ := byName[name].InputSchema.(map[string]any)
		p, _ := schema["properties"].(map[string]any)
		return p
	}
	symbols, _ := props("compare_etfs")["symbols"].(map[string]any)
	if symbols["minItems"] != float64(2) || symbols["maxItems"] != float64(10) {
		t.Errorf("compare_etfs symbols bounds = %v..%v, want 2..10", symbols["minItems"], symbols["maxItems"])
	}
	sortBy, _ := props("screen_universe")["sort_by"].(map[string]any)
	enum, _ := sortBy["enum"].([]any)
	var keys []string
	for _, v := range enum {
		keys = append(keys, v.(string))
	}
	if !slices.Equal(keys, analytics.RankKeys) {
		t.Errorf("screen_universe sort_by enum = %v, want %v", keys, analytics.RankKeys)
	}
	if limit, _ := props("screen_universe")["limit"].(map[string]any); limit["maximum"] != float64(screenMaxLimit) || limit["default"] != float64(screenDefaultLimit) {
		t.Errorf("screen_universe limit = %v", limit)
	}
	if corr, _ := props("find_alternatives")["min_correlation"].(map[string]any); corr["default"] != 0.9 || corr["minimum"] != float64(-1) {
		t.Errorf("find_alternatives min_correlation = %v", corr)
	}
}

func TestGetTechnicalIndicators(t *testing.T) {
	src := newAnalysisSource()
	sess := newSession(t, analysisDeps(src, nil))
	spy := src.series["SPY"]

	t.Run("maps every indicator", func(t *testing.T) {
		want, err := analytics.ComputeTechnicals(spy, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		trend, _ := analytics.AnalyzeTrend(spy, time.Time{})
		var out getTechnicalIndicatorsOutput
		callOK(t, sess, "get_technical_indicators", map[string]any{"symbol": " spy "}, &out)
		if out.Symbol != "SPY" || out.AsOf != formatDate(want.AsOf) || out.Bars != spy.Len() || out.Currency != "USD" {
			t.Errorf("symbol/as_of/bars/currency = %s/%s/%d/%s", out.Symbol, out.AsOf, out.Bars, out.Currency)
		}
		checks := []struct {
			name string
			got  *float64
			want float64
		}{
			{"close", &out.Close, round2(want.Close)},
			{"rsi_14", out.RSI14, round2(want.RSI14)},
			{"macd", out.MACD, round4(want.MACD)},
			{"macd_signal", out.MACDSignal, round4(want.MACDSignal)},
			{"macd_hist", out.MACDHist, round4(want.MACDHist)},
			{"bollinger_middle", out.BollingerMiddle, round2(want.BollingerMiddle)},
			{"bollinger_upper", out.BollingerUpper, round2(want.BollingerUpper)},
			{"bollinger_lower", out.BollingerLower, round2(want.BollingerLower)},
			{"bollinger_pct_b", out.BollingerPctB, round4(want.BollingerPctB)},
			{"atr_14", out.ATR14, round4(want.ATR14)},
			{"sma_20", out.SMA20, round2(want.SMA20)},
			{"sma_50", out.SMA50, round2(want.SMA50)},
			{"sma_200", out.SMA200, round2(want.SMA200)},
			{"ema_12", out.EMA12, round2(want.EMA12)},
			{"ema_26", out.EMA26, round2(want.EMA26)},
		}
		for _, c := range checks {
			if c.got == nil {
				t.Errorf("%s is null on a 780-bar series", c.name)
				continue
			}
			if *c.got != c.want {
				t.Errorf("%s = %v, want %v", c.name, *c.got, c.want)
			}
			if *c.got == 0 && c.name != "macd_hist" {
				t.Errorf("%s is 0 on a 780-bar series", c.name)
			}
		}
		if t.Failed() {
			return
		}
		if math.Abs(*out.MACDHist-(*out.MACD-*out.MACDSignal)) > 0.0002 {
			t.Errorf("macd_hist %v != macd %v - macd_signal %v", *out.MACDHist, *out.MACD, *out.MACDSignal)
		}
		if *out.BollingerLower >= *out.BollingerMiddle || *out.BollingerMiddle >= *out.BollingerUpper {
			t.Errorf("bands out of order: %v %v %v", *out.BollingerLower, *out.BollingerMiddle, *out.BollingerUpper)
		}
		if !slices.Equal(out.Signals, want.Signals) || len(out.Signals) == 0 {
			t.Errorf("signals = %v, want %v", out.Signals, want.Signals)
		}
		if out.Trend.State != trend.State || out.Trend.SMA200 != round2(trend.SMA200) || out.Trend.AsOf != out.AsOf {
			t.Errorf("trend = %+v, want state %s", out.Trend, trend.State)
		}
		if out.Disclaimer != Disclaimer || len(out.Warnings) != 0 {
			t.Errorf("disclaimer/warnings = %q/%v", out.Disclaimer, out.Warnings)
		}
	})

	t.Run("steady rise reads RSI 100 and overbought", func(t *testing.T) {
		var out getTechnicalIndicatorsOutput
		callOK(t, sess, "get_technical_indicators", map[string]any{"symbol": "XLK"}, &out)
		if out.RSI14 == nil || out.MACD == nil || out.MACDHist == nil {
			t.Fatalf("rsi/macd/hist = %v/%v/%v, want values on 300 bars", out.RSI14, out.MACD, out.MACDHist)
		}
		if *out.RSI14 != 100 || *out.MACD <= 0 || *out.MACDHist < 0 {
			t.Errorf("rsi/macd/hist = %v/%v/%v, want 100 and a positive MACD", *out.RSI14, *out.MACD, *out.MACDHist)
		}
		if !slices.ContainsFunc(out.Signals, func(s string) bool { return strings.Contains(s, "overbought") }) {
			t.Errorf("signals = %v, want an overbought reading", out.Signals)
		}
	})

	t.Run("short history reports null and says why", func(t *testing.T) {
		var out getTechnicalIndicatorsOutput
		callOK(t, sess, "get_technical_indicators", map[string]any{"symbol": "RSP"}, &out)
		if out.Bars >= 200 || out.SMA200 != nil || out.SMA50 == nil {
			t.Errorf("bars/sma_200/sma_50 = %d/%v/%v, want under 200 bars with only sma_200 missing", out.Bars, out.SMA200, out.SMA50)
		}
		if !slices.ContainsFunc(out.Signals, func(s string) bool { return strings.Contains(s, "200-day average needs 200 bars") }) {
			t.Errorf("signals = %v, want the 200-day history note", out.Signals)
		}
		if out.Trend.State != analytics.StateInsufficientHistory {
			t.Errorf("trend state = %s", out.Trend.State)
		}
	})

	t.Run("as_of anchors on the previous trading day", func(t *testing.T) {
		var out getTechnicalIndicatorsOutput
		callOK(t, sess, "get_technical_indicators", map[string]any{"symbol": "SPY", "as_of": "2022-06-18"}, &out) // a Saturday
		idx, _ := spy.IndexOn(date(t, "2022-06-17"))
		want, _ := analytics.ComputeTechnicals(spy, date(t, "2022-06-17"))
		if out.AsOf != "2022-06-17" || out.Bars != idx+1 || out.RSI14 == nil || *out.RSI14 != round2(want.RSI14) || out.Trend.AsOf != "2022-06-17" {
			t.Errorf("as_of/bars/rsi = %s/%d/%v, want 2022-06-17/%d/%v", out.AsOf, out.Bars, out.RSI14, idx+1, round2(want.RSI14))
		}
	})

	t.Run("old data is flagged", func(t *testing.T) {
		deps := analysisDeps(src, nil)
		deps.Now = func() time.Time { return now.AddDate(0, 1, 0) }
		var out getTechnicalIndicatorsOutput
		callOK(t, newSession(t, deps), "get_technical_indicators", map[string]any{"symbol": "SPY"}, &out)
		if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "days old") {
			t.Errorf("warnings = %v, want one 'days old' warning", out.Warnings)
		}
	})

	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown symbol", map[string]any{"symbol": "XYZ"}, "unknown symbol XYZ"},
		{"empty symbol", map[string]any{"symbol": ""}, "symbol is required"},
		{"bad date", map[string]any{"symbol": "SPY", "as_of": "18/06/2022"}, "YYYY-MM-DD"},
		{"before history", map[string]any{"symbol": "SPY", "as_of": "2020-01-01"}, "no bar on or before 2020-01-01; data covers 2021-01-04 to"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_technical_indicators", tt.args, tt.want)
		})
	}
}

// analysisMust returns v, failing the test passed to the returned
// function when err is not nil: analysisMust(f())(t).
func analysisMust[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// analysisAligned returns the adjusted closes of a and b on their common
// days within [from, to].
func analysisAligned(a, b *market.Series, from, to time.Time) (dates []time.Time, pa, pb []float64) {
	dates, aligned := analytics.CommonRange([]*market.Series{a, b}, from, to)
	return dates, aligned[0], aligned[1]
}
