package cache

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// date parses a YYYY-MM-DD test literal.
func date(s string) time.Time {
	t, err := market.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return t
}

// mkSeries builds a series with AdjClose equal to Close from
// "date:close" or "date:close:dividend" specs.
func mkSeries(symbol string, specs ...string) *market.Series {
	s := &market.Series{Meta: market.Meta{Symbol: symbol, Name: "Test Fund", Currency: "USD"}}
	for _, spec := range specs {
		parts := strings.Split(spec, ":")
		closePx, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			panic(err)
		}
		b := market.Bar{Date: date(parts[0]), Close: closePx, AdjClose: closePx}
		if len(parts) > 2 {
			if b.Dividend, err = strconv.ParseFloat(parts[2], 64); err != nil {
				panic(err)
			}
		}
		s.Bars = append(s.Bars, b)
	}
	return s
}

// cachedHistory is the history on disk before a top-up: one trading week.
func cachedHistory() *market.Series {
	return mkSeries("SPY", "2026-09-28:100", "2026-09-29:101", "2026-09-30:102", "2026-10-01:103", "2026-10-02:104")
}

// providerHistory is what the provider reports at t0: the cached week plus
// three new days and a newer quote.
func providerHistory() *market.Series {
	s := cachedHistory()
	s.Bars = append(s.Bars, mkSeries("SPY", "2026-10-05:105", "2026-10-06:106", "2026-10-07:107").Bars...)
	s.Meta.RegularMarketPrice = 107
	s.Meta.FetchedAt = t0
	return s
}

// cut returns the bars and splits of s dated within [from, to], as a
// RangeSource would.
func cut(s *market.Series, from, to time.Time) *market.Series {
	out := &market.Series{Meta: s.Meta}
	out.Bars = append(out.Bars, s.Between(from, to)...)
	for _, sp := range s.Splits {
		if !sp.Date.Before(from) && !sp.Date.After(to) {
			out.Splits = append(out.Splits, sp)
		}
	}
	return out
}

// closes lists the Close of every bar, for compact comparisons.
func closes(s *market.Series) []float64 {
	out := make([]float64, 0, s.Len())
	for _, b := range s.Bars {
		out = append(out, b.Close)
	}
	return out
}

func TestMergeTailAppends(t *testing.T) {
	base := cachedHistory()
	tail := cut(providerHistory(), date("2026-09-25"), date("2026-10-08"))

	merged, err := mergeTail(base, tail)
	if err != nil {
		t.Fatalf("mergeTail: %v", err)
	}
	want := []float64{100, 101, 102, 103, 104, 105, 106, 107}
	if got := closes(merged); !equalFloats(got, want) {
		t.Errorf("closes = %v, want %v", got, want)
	}
	if merged.Meta.RegularMarketPrice != 107 || !merged.Meta.FetchedAt.Equal(t0) {
		t.Errorf("meta quote fields not refreshed: %+v", merged.Meta)
	}
	if merged.Meta.Name != "Test Fund" {
		t.Errorf("meta name lost: %+v", merged.Meta)
	}
	if base.Len() != 5 || tail.Len() != 8 {
		t.Errorf("inputs mutated: base %d bars, tail %d bars", base.Len(), tail.Len())
	}
}

func TestMergeTailReplacesNewestCachedBar(t *testing.T) {
	// The cached 10-02 bar was captured intraday at 104; the settled close
	// is 104.5. That alone must not force a full fetch.
	truth := providerHistory()
	truth.Bars[4].Close, truth.Bars[4].AdjClose = 104.5, 104.5
	merged, err := mergeTail(cachedHistory(), cut(truth, date("2026-09-25"), date("2026-10-08")))
	if err != nil {
		t.Fatalf("mergeTail: %v", err)
	}
	if got := merged.Bars[4].Close; got != 104.5 {
		t.Errorf("newest cached bar close = %v, want the tail's 104.5", got)
	}
}

func TestMergeTailEmptyTail(t *testing.T) {
	base := cachedHistory()
	merged, err := mergeTail(base, &market.Series{Meta: base.Meta})
	if err != nil {
		t.Fatalf("mergeTail: %v", err)
	}
	if !equalFloats(closes(merged), closes(base)) {
		t.Errorf("closes = %v, want unchanged %v", closes(merged), closes(base))
	}
}

func TestMergeTailToleratesFloatNoise(t *testing.T) {
	truth := providerHistory()
	truth.Bars[1].Close *= 1 + 1e-9
	truth.Bars[2].AdjClose *= 1 - 1e-9
	if _, err := mergeTail(cachedHistory(), cut(truth, date("2026-09-25"), date("2026-10-08"))); err != nil {
		t.Errorf("mergeTail rejected float noise: %v", err)
	}
}

func TestMergeTailNeedsFull(t *testing.T) {
	window := func(s *market.Series) *market.Series { return cut(s, date("2026-09-25"), date("2026-10-08")) }
	tests := []struct {
		name string
		tail func() *market.Series
		want string // substring of the reason
	}{
		{
			name: "close changed in the overlap",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars[3].Close = 999
				return window(s)
			},
			want: "prices on 2026-10-01 changed",
		},
		{
			name: "adjclose changed in the overlap",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars[2].AdjClose = 50
				return window(s)
			},
			want: "prices on 2026-09-30 changed",
		},
		{
			name: "new dividend on a new bar",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars[6].Dividend = 0.5
				return window(s)
			},
			want: "new dividend 0.5 on 2026-10-06",
		},
		{
			name: "dividend added to an overlapping bar",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars[3].Dividend = 0.3
				return window(s)
			},
			want: "dividend on 2026-10-01 changed from 0 to 0.3",
		},
		{
			name: "dividend added to the newest cached bar",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars[4].Dividend = 0.3
				return window(s)
			},
			want: "dividend on 2026-10-02 changed",
		},
		{
			name: "new split",
			tail: func() *market.Series {
				s := providerHistory()
				s.Splits = []market.Split{{Date: date("2026-10-06"), Numerator: 2, Denominator: 1}}
				return window(s)
			},
			want: "split 2:1 on 2026-10-06 is not cached",
		},
		{
			name: "overlap bar missing from the tail",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars = append(s.Bars[:3], s.Bars[4:]...) // drop 10-01
				return window(s)
			},
			want: "5 cached bars since 2026-09-28 but 4 in the tail",
		},
		{
			name: "extra bar in the overlap",
			tail: func() *market.Series {
				s := providerHistory()
				extra := market.Bar{Date: date("2026-09-27"), Close: 99.5, AdjClose: 99.5}
				s.Bars = append([]market.Bar{extra}, s.Bars...)
				return window(s)
			},
			want: "5 cached bars since 2026-09-27 but 6 in the tail",
		},
		{
			name: "tail date differs",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars[0].Date = date("2026-09-27")
				return window(s)
			},
			want: "cached bar on 2026-09-28, tail bar on 2026-09-27",
		},
		{
			name: "invalid tail",
			tail: func() *market.Series {
				s := providerHistory()
				s.Bars[6].Close = 0
				return window(s)
			},
			want: "tail:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged, err := mergeTail(cachedHistory(), tt.tail())
			if !errors.Is(err, errNeedFull) {
				t.Fatalf("err = %v, want errNeedFull (merged = %v)", err, merged)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("reason %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestMergeTailKeepsCachedSplit(t *testing.T) {
	base := cachedHistory()
	base.Splits = []market.Split{{Date: date("2026-09-30"), Numerator: 3, Denominator: 1}}
	truth := providerHistory()
	truth.Splits = base.Splits
	merged, err := mergeTail(base, cut(truth, date("2026-09-25"), date("2026-10-08")))
	if err != nil {
		t.Fatalf("a split already cached forced a full fetch: %v", err)
	}
	if len(merged.Splits) != 1 || merged.Splits[0].Ratio() != "3:1" {
		t.Errorf("splits = %v, want the cached 3:1", merged.Splits)
	}
}

func TestMergeTailEmptyBase(t *testing.T) {
	base := &market.Series{Meta: market.Meta{Symbol: "SPY"}}
	if _, err := mergeTail(base, providerHistory()); !errors.Is(err, errNeedFull) {
		t.Errorf("err = %v, want errNeedFull for an empty cached history", err)
	}
}

func TestNearlyEqual(t *testing.T) {
	tests := []struct {
		a, b float64
		want bool
	}{
		{0, 0, true},
		{100, 100, true},
		{100, 100 + 5e-5, true},
		{100, 100 + 2e-4, false},
		{0, 0.5, false},
		{0.5, 0, false},
		{-1, 1, false},
	}
	for _, tt := range tests {
		if got := nearlyEqual(tt.a, tt.b); got != tt.want {
			t.Errorf("nearlyEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
