package analytics

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// stockChartsCloses is the 14-period RSI worked example from StockCharts
// ChartSchool (QQQQ, December 2009 to January 2010).
var stockChartsCloses = []float64{
	44.3389, 44.0902, 44.1497, 43.6124, 44.3278, 44.8264, 45.0955, 45.4245,
	45.8433, 46.0826, 45.8931, 46.0328, 45.6140, 46.2820, 46.2820, 46.0028,
	46.0328, 46.4116, 46.2222, 45.6439, 46.2122, 46.2521, 45.7137, 46.4515,
	45.7835, 45.3548, 44.0288, 44.1783, 44.2181, 44.5672, 43.4205, 42.6628,
	43.1314,
}

func TestRSIStockCharts(t *testing.T) {
	// Expected values were recomputed independently (Python, see the recipe
	// on RSI: seed with the mean of the first 14 gains and losses, then
	// Wilder smoothing). Rounded to two decimals they are StockCharts'
	// published column: 70.53, 66.32, 66.55, 69.41, 66.36, 57.97, 62.93,
	// 63.26, 56.06, 62.38, 54.71, 50.42, 39.99, 41.46, 41.87, 45.46, 37.30,
	// 33.08, 37.77.
	want := []float64{
		70.532789, 66.318562, 66.549830, 69.406305, 66.355169, 57.974856,
		62.929607, 63.257148, 56.059299, 62.377071, 54.707573, 50.422774,
		39.989823, 41.460482, 41.868916, 45.463212, 37.304042, 33.079523,
		37.772952,
	}
	got := RSI(stockChartsCloses, 14)
	if len(got) != len(stockChartsCloses) {
		t.Fatalf("len = %d, want %d", len(got), len(stockChartsCloses))
	}
	for i := range 14 {
		if !math.IsNaN(got[i]) {
			t.Fatalf("got[%d] = %v, want NaN before the first full period", i, got[i])
		}
	}
	for i, w := range want {
		if g := got[14+i]; !approx(g, w, 1e-6) {
			t.Fatalf("RSI[%d] = %.6f, want %.6f", 14+i, g, w)
		}
	}
}

func TestRSIEdgeCases(t *testing.T) {
	nan := math.NaN()
	rising := make([]float64, 16)
	for i := range rising {
		rising[i] = float64(i + 1)
	}
	tests := []struct {
		name   string
		closes []float64
		n      int
		want   []float64
	}{
		// Changes +1, -1, +2 with n=2: at index 2 the averages are 0.5 and
		// 0.5 (RSI 50); at index 3 they are 1.25 and 0.25 (RS 5, RSI 83.33).
		{name: "hand computed", closes: []float64{10, 11, 10, 12}, n: 2, want: []float64{nan, nan, 50, 100 - 100.0/6}},
		{name: "only gains read 100", closes: []float64{1, 2, 3, 4, 5}, n: 2, want: []float64{nan, nan, 100, 100, 100}},
		{name: "only losses read 0", closes: []float64{5, 4, 3, 2, 1}, n: 2, want: []float64{nan, nan, 0, 0, 0}},
		{name: "flat reads 50", closes: []float64{3, 3, 3, 3}, n: 2, want: []float64{nan, nan, 50, 50}},
		{name: "needs n+1 closes", closes: []float64{1, 2, 3}, n: 3, want: []float64{nan, nan, nan}},
		{name: "non-positive n means 14", closes: rising, n: 0, want: append(nanSlice(14), 100, 100)},
		{name: "empty", closes: nil, n: 14, want: []float64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RSI(tt.closes, tt.n)
			if !floatsMatch(got, tt.want, 1e-9) {
				t.Fatalf("RSI(%v, %d) = %v, want %v", tt.closes, tt.n, got, tt.want)
			}
		})
	}
}

func TestMACDHandCheck(t *testing.T) {
	nan := math.NaN()
	// Closes 1..6 with EMA(2) (alpha 2/3) and EMA(3) (alpha 1/2):
	//   EMA2: 1, 5/3, 23/9, 95/27, 365/81, 1337/243   (reported from index 1)
	//   EMA3: 1, 3/2, 9/4, 25/8, 65/16, 161/32         (reported from index 2)
	// macd = EMA2 - EMA3 from index 2: 11/36, 85/216, 575/1296, 3661/7776.
	// The signal EMA(2) seeds on 11/36 at index 2 and is reported from
	// index 3; the histogram is macd - signal.
	closes := []float64{1, 2, 3, 4, 5, 6}
	wantMACD := []float64{nan, nan, 0.305555555556, 0.393518518519, 0.443672839506, 0.470807613169}
	wantSignal := []float64{nan, nan, nan, 0.364197530864, 0.417181069959, 0.452932098765}
	wantHist := []float64{nan, nan, nan, 0.029320987654, 0.026491769547, 0.017875514403}

	macd, signal, hist := MACD(closes, 2, 3, 2)
	if !floatsMatch(macd, wantMACD, 1e-9) {
		t.Fatalf("macd = %v, want %v", macd, wantMACD)
	}
	if !floatsMatch(signal, wantSignal, 1e-9) {
		t.Fatalf("signal = %v, want %v", signal, wantSignal)
	}
	if !floatsMatch(hist, wantHist, 1e-9) {
		t.Fatalf("histogram = %v, want %v", hist, wantHist)
	}
}

func TestMACDDefaultsAndMask(t *testing.T) {
	closes := make([]float64, 40)
	for i := range closes {
		closes[i] = 100 + float64(i%7) - float64(i)/3
	}
	macd, signal, hist := MACD(closes, 0, 0, 0)
	wantMACD, wantSignal, wantHist := MACD(closes, 12, 26, 9)
	if !floatsMatch(macd, wantMACD, 0) || !floatsMatch(signal, wantSignal, 0) || !floatsMatch(hist, wantHist, 0) {
		t.Fatal("non-positive periods must select 12/26/9")
	}
	for i := range closes {
		if wantNaN := i < 25; math.IsNaN(macd[i]) != wantNaN {
			t.Fatalf("macd[%d] NaN = %v, want %v", i, !wantNaN, wantNaN)
		}
		if wantNaN := i < 33; math.IsNaN(signal[i]) != wantNaN || math.IsNaN(hist[i]) != wantNaN {
			t.Fatalf("signal/hist[%d] NaN mask wrong (signal %v, hist %v)", i, signal[i], hist[i])
		}
	}
	// Too short for the slow EMA: everything stays NaN.
	m, s, h := MACD(closes[:20], 0, 0, 0)
	for i := range m {
		if !math.IsNaN(m[i]) || !math.IsNaN(s[i]) || !math.IsNaN(h[i]) {
			t.Fatalf("index %d defined on a 20-bar input: %v %v %v", i, m[i], s[i], h[i])
		}
	}
}

func TestBollingerHandCheck(t *testing.T) {
	nan := math.NaN()
	// Windows of three consecutive integers have population stdev
	// sqrt(2/3) = 0.816497; with k = 2 the band half-width is 1.632993.
	closes := []float64{1, 2, 3, 4, 5}
	wantMiddle := []float64{nan, nan, 2, 3, 4}
	wantUpper := []float64{nan, nan, 3.632993161855, 4.632993161855, 5.632993161855}
	wantLower := []float64{nan, nan, 0.367006838145, 1.367006838145, 2.367006838145}

	middle, upper, lower := Bollinger(closes, 3, 2)
	if !floatsMatch(middle, wantMiddle, 1e-9) {
		t.Fatalf("middle = %v, want %v", middle, wantMiddle)
	}
	if !floatsMatch(upper, wantUpper, 1e-9) {
		t.Fatalf("upper = %v, want %v", upper, wantUpper)
	}
	if !floatsMatch(lower, wantLower, 1e-9) {
		t.Fatalf("lower = %v, want %v", lower, wantLower)
	}

	t.Run("defaults", func(t *testing.T) {
		long := make([]float64, 30)
		for i := range long {
			long[i] = 50 + float64(i*i%11)
		}
		m, u, l := Bollinger(long, 0, 0)
		wm, wu, wl := Bollinger(long, 20, 2)
		if !floatsMatch(m, wm, 0) || !floatsMatch(u, wu, 0) || !floatsMatch(l, wl, 0) {
			t.Fatal("non-positive n and k must select 20 and 2")
		}
		if !math.IsNaN(m[18]) || math.IsNaN(m[19]) {
			t.Fatalf("mask wrong around index 19: %v %v", m[18], m[19])
		}
	})
	t.Run("constant closes collapse the bands", func(t *testing.T) {
		m, u, l := Bollinger([]float64{7, 7, 7, 7}, 2, 2)
		for i := 1; i < 4; i++ {
			if m[i] != 7 || u[i] != 7 || l[i] != 7 {
				t.Fatalf("index %d: %v %v %v, want 7 7 7", i, m[i], u[i], l[i])
			}
		}
	})
}

func TestTrueRange(t *testing.T) {
	tests := []struct {
		name      string
		bar       market.Bar
		prevClose float64
		want      float64
	}{
		{name: "high-low dominates", bar: market.Bar{High: 15, Low: 12, Close: 14}, prevClose: 12.5, want: 3},
		{name: "gap up: high minus previous close", bar: market.Bar{High: 13, Low: 12, Close: 12.5}, prevClose: 11, want: 2},
		{name: "gap down: previous close minus low", bar: market.Bar{High: 15, Low: 12, Close: 13}, prevClose: 20, want: 8},
		{name: "close only falls back to close-to-close", bar: market.Bar{Close: 13}, prevClose: 10, want: 3},
		{name: "missing low uses close", bar: market.Bar{High: 12, Close: 11}, prevClose: 10, want: 2},
		{name: "missing high uses close", bar: market.Bar{Low: 9, Close: 11}, prevClose: 10, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trueRange(tt.bar, tt.prevClose); !approx(got, tt.want, 1e-12) {
				t.Fatalf("trueRange(%+v, %v) = %v, want %v", tt.bar, tt.prevClose, got, tt.want)
			}
		})
	}
}

func TestATR(t *testing.T) {
	nan := math.NaN()
	ohlc := []market.Bar{
		{High: 12, Low: 10, Close: 11},
		{High: 13, Low: 12, Close: 12.5}, // TR 2 (gap up)
		{High: 15, Low: 12, Close: 14},   // TR 3
		{High: 14, Low: 9, Close: 10},    // TR 5
		{High: 11, Low: 10, Close: 10.5}, // TR 1
	}
	closeOnly := []market.Bar{{Close: 10}, {Close: 11}, {Close: 13}, {Close: 12}, {Close: 12}} // TR 1, 2, 1, 0
	tests := []struct {
		name string
		bars []market.Bar
		n    int
		want []float64
	}{
		// n=2: seed (2+3)/2 = 2.5, then (2.5+5)/2 = 3.75, (3.75+1)/2 = 2.375.
		{name: "ohlc hand computed", bars: ohlc, n: 2, want: []float64{nan, nan, 2.5, 3.75, 2.375}},
		// n=2: seed (1+2)/2 = 1.5, then (1.5+1)/2 = 1.25, (1.25+0)/2 = 0.625.
		{name: "close-only cache falls back", bars: closeOnly, n: 2, want: []float64{nan, nan, 1.5, 1.25, 0.625}},
		{name: "needs n+1 bars", bars: ohlc[:3], n: 3, want: []float64{nan, nan, nan}},
		{name: "non-positive n means 14", bars: ohlc, n: 0, want: []float64{nan, nan, nan, nan, nan}},
		{name: "empty", bars: nil, n: 2, want: []float64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ATR(tt.bars, tt.n)
			if !floatsMatch(got, tt.want, 1e-12) {
				t.Fatalf("ATR(n=%d) = %v, want %v", tt.n, got, tt.want)
			}
		})
	}
}

func TestRecentCross(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name        string
		series      []float64
		idx         int
		lookback    int
		wantBarsAgo int
		wantUp      bool
		wantOK      bool
	}{
		{name: "up one bar ago", series: []float64{-1, -1, 1, 1}, idx: 3, lookback: 10, wantBarsAgo: 1, wantUp: true, wantOK: true},
		{name: "down today", series: []float64{1, 1, -1}, idx: 2, lookback: 10, wantBarsAgo: 0, wantUp: false, wantOK: true},
		{name: "through an exact zero", series: []float64{-1, 0, 1}, idx: 2, lookback: 10, wantBarsAgo: 0, wantUp: true, wantOK: true},
		{name: "outside the lookback", series: []float64{-1, 1, 1, 1, 1}, idx: 4, lookback: 3, wantOK: false},
		{name: "undefined history stops the scan", series: []float64{nan, 1, 1}, idx: 2, lookback: 10, wantOK: false},
		{name: "no change", series: []float64{1, 2, 3}, idx: 2, lookback: 10, wantOK: false},
		{name: "first bar", series: []float64{1, 2}, idx: 0, lookback: 10, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			barsAgo, up, ok := recentCross(tt.series, tt.idx, tt.lookback)
			if ok != tt.wantOK || barsAgo != tt.wantBarsAgo || up != tt.wantUp {
				t.Fatalf("recentCross = (%d, %v, %v), want (%d, %v, %v)", barsAgo, up, ok, tt.wantBarsAgo, tt.wantUp, tt.wantOK)
			}
		})
	}
}

func TestBarsAgoText(t *testing.T) {
	tests := map[int]string{0: "today", 1: "1 bar ago", 5: "5 bars ago"}
	for n, want := range tests {
		if got := barsAgoText(n); got != want {
			t.Fatalf("barsAgoText(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestComputeTechnicalsValues(t *testing.T) {
	// Closes 1..60 with no High/Low, so every true range is 1.
	linear := func(i int, _ time.Time) float64 { return float64(i + 1) }
	s := seriesFrom("LIN", weekdays(day(2020, time.January, 1), 60), linear)
	tech, err := ComputeTechnicals(s, time.Time{})
	if err != nil {
		t.Fatalf("ComputeTechnicals: %v", err)
	}
	cl := closes(s.Bars)
	macd, signal, hist := MACD(cl, 12, 26, 9)
	checks := []struct {
		name      string
		got, want float64
	}{
		{"Close", tech.Close, 60},
		{"RSI14", tech.RSI14, 100},
		{"SMA20", tech.SMA20, 50.5}, // mean of 41..60
		{"SMA50", tech.SMA50, 35.5}, // mean of 11..60
		{"SMA200", tech.SMA200, 0},  // not enough bars, reported as 0
		{"EMA12", tech.EMA12, EMA(cl, 12)[59]},
		{"EMA26", tech.EMA26, EMA(cl, 26)[59]},
		{"MACD", tech.MACD, macd[59]},
		{"MACDSignal", tech.MACDSignal, signal[59]},
		{"MACDHist", tech.MACDHist, hist[59]},
		// Population stdev of 41..60 is sqrt(399/12) = 5.766281.
		{"BollingerMiddle", tech.BollingerMiddle, 50.5},
		{"BollingerUpper", tech.BollingerUpper, 62.032562594671},
		{"BollingerLower", tech.BollingerLower, 38.967437405329},
		{"BollingerPctB", tech.BollingerPctB, 0.911877235524},
		{"ATR14", tech.ATR14, 1},
	}
	for _, c := range checks {
		if !approx(c.got, c.want, 1e-9) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !tech.AsOf.Equal(s.Bars[59].Date) {
		t.Fatalf("AsOf = %v, want %v", tech.AsOf, s.Bars[59].Date)
	}
	for _, want := range []string{
		"RSI 100 overbought",
		"MACD above signal line",
		"200-day average needs 200 bars, only 60 available; reported as 0",
	} {
		if !hasReason(tech.Signals, want) {
			t.Errorf("Signals %v lack %q", tech.Signals, want)
		}
	}
	for _, unwanted := range []string{"Bollinger band needs", "RSI needs", "crossed"} {
		if hasReason(tech.Signals, unwanted) {
			t.Errorf("Signals %v must not mention %q", tech.Signals, unwanted)
		}
	}
}

// stepSeries builds n weekday bars at base, with every bar from index
// jumpAt on at base+jump.
func stepSeries(symbol string, n, jumpAt int, base, jump float64) *market.Series {
	return seriesFrom(symbol, weekdays(day(2020, time.January, 1), n), func(i int, _ time.Time) float64 {
		if i >= jumpAt {
			return base + jump
		}
		return base
	})
}

func TestComputeTechnicalsSignals(t *testing.T) {
	// Closes 100 - 0.001*i*i: a decline that steepens every bar, so the
	// MACD line keeps falling away from its signal line.
	accelerating := seriesFrom("ACC", weekdays(day(2020, time.January, 1), 300), func(i int, _ time.Time) float64 {
		return 100 - 0.001*float64(i*i)
	})
	tests := []struct {
		name     string
		series   *market.Series
		macdSign int      // sign the MACD line must have, 0 to skip
		want     []string // substrings that must appear
		unwanted []string // substrings that must not appear
	}{
		{
			name:     "monotone rise",
			series:   growthSeries("UP", 300, 0.01),
			macdSign: 1,
			want:     []string{"RSI 100 overbought", "MACD above signal line", "close above 200-day average"},
			unwanted: []string{"crossed", "needs", "Bollinger"},
		},
		{
			// An exponential decline shrinks |MACD| with the price, so the
			// negative MACD line rises towards zero and sits above its
			// lagging signal line: no "below signal" here, by design.
			name:     "exponential fall",
			series:   growthSeries("DOWN", 300, -0.01),
			macdSign: -1,
			want:     []string{"RSI 0 oversold", "MACD above signal line", "close below 200-day average"},
			unwanted: []string{"crossed", "needs", "Bollinger"},
		},
		{
			name:     "accelerating fall",
			series:   accelerating,
			macdSign: -1,
			want:     []string{"RSI 0 oversold", "MACD below signal line", "close below 200-day average"},
			unwanted: []string{"crossed", "needs"},
		},
		{
			name:   "spike above the upper band",
			series: stepSeries("SPIKE", 30, 29, 100, 10),
			want:   []string{"close above upper Bollinger band", "RSI 100 overbought"},
		},
		{
			name:   "drop below the lower band",
			series: stepSeries("DROP", 30, 29, 100, -10),
			want:   []string{"close below lower Bollinger band", "RSI 0 oversold"},
		},
		{
			// 250 flat bars then 120: SMA50 - SMA200 turns positive on bar
			// 250 (100.4 vs 100.1), five bars before the anchor at 255.
			name:   "golden cross five bars ago",
			series: &market.Series{Meta: market.Meta{Symbol: "GOLD"}, Bars: stepSeries("GOLD", 300, 250, 100, 20).Bars[:256]},
			want:   []string{"50-day average crossed above 200-day average 5 bars ago", "close above 200-day average", "RSI 100 overbought"},
		},
		{
			// The mirror image: 250 bars at 120 then 100, anchored at 255.
			name:   "death cross five bars ago",
			series: &market.Series{Meta: market.Meta{Symbol: "DEATH"}, Bars: stepSeries("DEATH", 300, 250, 120, -20).Bars[:256]},
			want:   []string{"50-day average crossed below 200-day average 5 bars ago", "close below 200-day average", "RSI 0 oversold"},
		},
		{
			name:   "too short for everything",
			series: growthSeries("NEW", 10, 0.001),
			want: []string{
				"RSI needs 15 bars, only 10 available; reported as 0",
				"MACD signal line needs 34 bars, only 10 available",
				"Bollinger band needs 20 bars, only 10 available",
				"ATR needs 15 bars, only 10 available",
				"20-day average needs 20 bars",
				"50-day average needs 50 bars",
				"200-day average needs 200 bars",
			},
			unwanted: []string{"overbought", "oversold", "MACD above", "MACD below"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tech, err := ComputeTechnicals(tt.series, time.Time{})
			if err != nil {
				t.Fatalf("ComputeTechnicals: %v", err)
			}
			if tt.macdSign != 0 && (tech.MACD > 0) != (tt.macdSign > 0) {
				t.Errorf("MACD = %v, want sign %d", tech.MACD, tt.macdSign)
			}
			for _, w := range tt.want {
				if !hasReason(tech.Signals, w) {
					t.Errorf("Signals %v lack %q", tech.Signals, w)
				}
			}
			for _, u := range tt.unwanted {
				if hasReason(tech.Signals, u) {
					t.Errorf("Signals %v must not mention %q", tech.Signals, u)
				}
			}
		})
	}
}

func TestComputeTechnicalsShortSeriesIsZero(t *testing.T) {
	tech, err := ComputeTechnicals(growthSeries("NEW", 10, 0.001), time.Time{})
	if err != nil {
		t.Fatalf("ComputeTechnicals: %v", err)
	}
	zeros := map[string]float64{
		"RSI14": tech.RSI14, "MACD": tech.MACD, "MACDSignal": tech.MACDSignal, "MACDHist": tech.MACDHist,
		"BollingerMiddle": tech.BollingerMiddle, "BollingerUpper": tech.BollingerUpper,
		"BollingerLower": tech.BollingerLower, "BollingerPctB": tech.BollingerPctB,
		"ATR14": tech.ATR14, "SMA20": tech.SMA20, "SMA50": tech.SMA50, "SMA200": tech.SMA200,
		"EMA12": tech.EMA12, "EMA26": tech.EMA26,
	}
	for name, v := range zeros {
		if v != 0 {
			t.Errorf("%s = %v, want 0 on a 10-bar series", name, v)
		}
	}
	if tech.Close == 0 {
		t.Fatal("Close must still be reported")
	}
}

func TestComputeTechnicalsMACDCross(t *testing.T) {
	// Forty bars falling 1% a day, then eight bars rising 2% a day: the
	// MACD histogram turns positive a few bars after the turn. The test
	// locates that bar from the MACD output and expects the matching text.
	dates := weekdays(day(2021, time.March, 1), 48)
	s := seriesFrom("V", dates, func(i int, _ time.Time) float64 {
		if i < 40 {
			return 100 * math.Exp(-0.01*float64(i))
		}
		return 100 * math.Exp(-0.01*39) * math.Exp(0.02*float64(i-39))
	})
	_, _, hist := MACD(closes(s.Bars), 0, 0, 0)
	idx := len(s.Bars) - 1
	cross := -1
	for k := idx; k > 0; k-- {
		if hist[k] > 0 && hist[k-1] <= 0 {
			cross = k
			break
		}
	}
	if cross < 0 || idx-cross >= recentCrossBars {
		t.Fatalf("test series must cross within %d bars of the end, cross at %d of %d", recentCrossBars, cross, idx)
	}
	tech, err := ComputeTechnicals(s, time.Time{})
	if err != nil {
		t.Fatalf("ComputeTechnicals: %v", err)
	}
	want := "MACD crossed above signal line " + barsAgoText(idx-cross)
	if !hasReason(tech.Signals, want) {
		t.Fatalf("Signals %v lack %q", tech.Signals, want)
	}
	if hasReason(tech.Signals, "MACD above signal line") {
		t.Fatalf("Signals %v must report the crossover instead of the side", tech.Signals)
	}
}

func TestComputeTechnicalsAsOfAndErrors(t *testing.T) {
	s := growthSeries("X", 100, 0.001)

	t.Run("as-of picks the last bar on or before", func(t *testing.T) {
		target := s.Bars[70].Date
		tech, err := ComputeTechnicals(s, target.AddDate(0, 0, 1)) // a Saturday when bar 70 is a Friday, else the next bar's eve
		if err != nil {
			t.Fatalf("ComputeTechnicals: %v", err)
		}
		idx, _ := s.IndexOn(target.AddDate(0, 0, 1))
		if !tech.AsOf.Equal(s.Bars[idx].Date) || tech.Close != s.Bars[idx].Close {
			t.Fatalf("AsOf = %v Close = %v, want bar %d", tech.AsOf, tech.Close, idx)
		}
		if want := SMA(closes(s.Bars[:idx+1]), 20)[idx]; !approx(tech.SMA20, want, 1e-12) {
			t.Fatalf("SMA20 must ignore bars after asOf: %v vs %v", tech.SMA20, want)
		}
	})
	t.Run("nil series", func(t *testing.T) {
		if _, err := ComputeTechnicals(nil, time.Time{}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("before first bar", func(t *testing.T) {
		_, err := ComputeTechnicals(s, day(2019, time.December, 1))
		if !errors.Is(err, ErrInsufficientHistory) {
			t.Fatalf("err = %v, want ErrInsufficientHistory", err)
		}
		if !strings.Contains(err.Error(), "2019-12-01") {
			t.Fatalf("error must name the date: %v", err)
		}
	})
}
