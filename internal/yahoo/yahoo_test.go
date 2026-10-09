package yahoo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// recorder captures what the fake server saw.
type recorder struct {
	mu        sync.Mutex
	hits      int
	userAgent string
	path      string
	uri       string // raw request URI, to check escaping
	query     string
}

func (r *recorder) observe(req *http.Request) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits++
	r.userAgent = req.Header.Get("User-Agent")
	r.path = req.URL.Path
	r.uri = req.RequestURI
	r.query = req.URL.RawQuery
	return r.hits
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits
}

func respond(w http.ResponseWriter, status int, body []byte) {
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// newTestServer starts a server driven by handler, closed with the test,
// and returns its URL.
func newTestServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// newTestClient starts a server driven by handler and returns a Client
// pointed at it with fast retries. Extra options are applied last.
func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base := []Option{WithBaseURL(srv.URL), WithCookieURL(srv.URL), WithHTTPClient(srv.Client()), WithRetries(3, time.Millisecond)}
	return New(append(base, opts...)...)
}

// statusSequence serves fixture bodies with the given statuses in order,
// repeating the last status once the sequence is exhausted.
func statusSequence(rec *recorder, fixture []byte, statuses ...int) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		n := rec.observe(req)
		status := statuses[min(n, len(statuses))-1]
		if status == http.StatusOK {
			respond(w, status, fixture)
			return
		}
		respond(w, status, []byte("upstream says "+http.StatusText(status)))
	}
}

func TestNewDefaults(t *testing.T) {
	c := New()
	if c.baseURL != defaultBaseURL || c.cookieURL != defaultCookieURL || c.userAgent != defaultUserAgent {
		t.Errorf("defaults = %q %q %q", c.baseURL, c.cookieURL, c.userAgent)
	}
	if c.attempts != defaultAttempts || c.backoff != defaultBackoff {
		t.Errorf("retry defaults = %d %v", c.attempts, c.backoff)
	}
	if c.httpClient.Timeout != defaultTimeout {
		t.Errorf("timeout = %v, want %v", c.httpClient.Timeout, defaultTimeout)
	}
	if c.summaryTTL != defaultSummaryTTL || c.session == nil || c.memo == nil || c.now == nil {
		t.Errorf("session defaults = ttl %v session %v memo %v", c.summaryTTL, c.session, c.memo)
	}

	custom := New(WithBaseURL("http://example.test/"), WithCookieURL("http://cookie.test"), WithUserAgent("ua"),
		WithRetries(0, -time.Second), WithSummaryTTL(-time.Second))
	if custom.baseURL != "http://example.test" {
		t.Errorf("trailing slash not trimmed: %q", custom.baseURL)
	}
	if custom.cookieURL != "http://cookie.test" {
		t.Errorf("cookieURL = %q", custom.cookieURL)
	}
	if custom.userAgent != "ua" {
		t.Errorf("userAgent = %q", custom.userAgent)
	}
	if custom.attempts != 1 || custom.backoff != 0 {
		t.Errorf("WithRetries(0,-1s) = %d %v, want 1 0", custom.attempts, custom.backoff)
	}
	if custom.summaryTTL != 0 {
		t.Errorf("WithSummaryTTL(-1s) = %v, want 0", custom.summaryTTL)
	}
	ignored := New(WithBaseURL(""), WithCookieURL(""), WithUserAgent(""), WithHTTPClient(nil))
	if ignored.baseURL != defaultBaseURL || ignored.cookieURL != defaultCookieURL || ignored.userAgent != defaultUserAgent || ignored.httpClient == nil {
		t.Error("empty option values should be ignored")
	}
}

func TestBackoffFor(t *testing.T) {
	c := New(WithRetries(10, time.Second))
	tests := []struct {
		retry int
		want  time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{5, 16 * time.Second},
		{6, maxBackoff},
		{40, maxBackoff},
	}
	for _, tt := range tests {
		if got := c.backoffFor(tt.retry); got != tt.want {
			t.Errorf("backoffFor(%d) = %v, want %v", tt.retry, got, tt.want)
		}
	}
}

func TestClientSeries(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(t, statusSequence(rec, loadFixture(t), http.StatusOK))

	s, err := c.Series(context.Background(), " spy ")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if rec.path != "/v8/finance/chart/SPY" {
		t.Errorf("path = %q, want upper-cased trimmed symbol", rec.path)
	}
	for _, want := range []string{"period1=-2208988800", "period2=", "interval=1d", "events=div%2Csplits", "includeAdjustedClose=true"} {
		if !strings.Contains(rec.query, want) {
			t.Errorf("query %q lacks %q", rec.query, want)
		}
	}
	if rec.userAgent != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", rec.userAgent, defaultUserAgent)
	}
	if rec.count() != 1 {
		t.Errorf("hits = %d, want 1", rec.count())
	}

	if s.Meta.Symbol != "SPY" || s.Meta.Name != "State Street SPDR S&P 500 ETF Trust" || s.Meta.Currency != "USD" {
		t.Errorf("meta = %+v", s.Meta)
	}
	if len(s.Bars) != 9 {
		t.Fatalf("got %d bars, want 9", len(s.Bars))
	}
	if got := s.Bars[4]; got.Dividend != 1.889 || !got.Date.Equal(date(t, "2026-09-18")) {
		t.Errorf("dividend bar = %+v", got)
	}
	if got := s.Bars[0]; got.Open != 764.719970703125 || got.Volume != 45512700 {
		t.Errorf("first bar OHLCV not filled: %+v", got)
	}
	if s.Meta.FetchedAt.IsZero() || time.Since(s.Meta.FetchedAt) > time.Minute {
		t.Errorf("FetchedAt = %v, want roughly now", s.Meta.FetchedAt)
	}
}

func TestClientSeriesSplits(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(t, statusSequence(rec, readFixture(t, "tqqq_splits.json"), http.StatusOK))
	s, err := c.Series(context.Background(), "TQQQ")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(s.Splits) != 1 || s.Splits[0].Ratio() != "2:1" || !s.Splits[0].Date.Equal(date(t, "2022-01-13")) {
		t.Errorf("Splits = %+v, want one 2:1 split on 2022-01-13", s.Splits)
	}
}

func TestSeriesRangePeriods(t *testing.T) {
	d := func(s string) time.Time { return date(t, s) }
	tests := []struct {
		name        string
		from, to    time.Time
		wantPeriod1 string
		wantPeriod2 string
	}{
		{
			name: "bounded range", from: d("2022-01-10"), to: d("2022-01-19"),
			wantPeriod1: "1641772800", // 2022-01-10 00:00 UTC
			wantPeriod2: "1642636799", // 2022-01-19 23:59:59 UTC
		},
		{
			name: "single day", from: d("2022-01-13"), to: d("2022-01-13"),
			wantPeriod1: "1642032000", wantPeriod2: "1642118399",
		},
		{
			name: "clock and zone are dropped", from: time.Date(2022, 1, 10, 23, 59, 0, 0, time.FixedZone("x", -5*3600)), to: d("2022-01-19"),
			wantPeriod1: "1641772800", wantPeriod2: "1642636799",
		},
		{
			name: "zero from is all history", to: d("2022-01-19"),
			wantPeriod1: "-2208988800", wantPeriod2: "1642636799",
		},
		{
			name: "zero to is now", from: d("2022-01-10"),
			wantPeriod1: "1641772800", wantPeriod2: "1791460800", // fixtureNow
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			c := newTestClient(t, statusSequence(rec, readFixture(t, "tqqq_splits.json"), http.StatusOK))
			c.now = func() time.Time { return fixtureNow }

			s, err := c.SeriesRange(context.Background(), "tqqq", tt.from, tt.to)
			if err != nil {
				t.Fatalf("SeriesRange: %v", err)
			}
			q, err := url.ParseQuery(rec.query)
			if err != nil {
				t.Fatalf("parse query %q: %v", rec.query, err)
			}
			if q.Get("period1") != tt.wantPeriod1 || q.Get("period2") != tt.wantPeriod2 {
				t.Errorf("period1/period2 = %s/%s, want %s/%s", q.Get("period1"), q.Get("period2"), tt.wantPeriod1, tt.wantPeriod2)
			}
			if q.Get("interval") != "1d" || q.Get("events") != "div,splits" || q.Get("includeAdjustedClose") != "true" {
				t.Errorf("query = %v", q)
			}
			if rec.path != "/v8/finance/chart/TQQQ" || len(s.Bars) != 7 || !s.Meta.FetchedAt.Equal(fixtureNow) {
				t.Errorf("path %s, %d bars, fetchedAt %v", rec.path, len(s.Bars), s.Meta.FetchedAt)
			}
		})
	}
}

func TestSeriesRangeRejectsInvertedRange(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(t, statusSequence(rec, readFixture(t, "tqqq_splits.json"), http.StatusOK))
	_, err := c.SeriesRange(context.Background(), "TQQQ", date(t, "2022-01-19"), date(t, "2022-01-10"))
	if err == nil || !strings.Contains(err.Error(), "2022-01-19") || !strings.Contains(err.Error(), "TQQQ") {
		t.Fatalf("error = %v, want one naming the symbol and the start date", err)
	}
	if rec.count() != 0 {
		t.Errorf("hits = %d, want 0", rec.count())
	}
}

func TestSeriesEscapesSymbols(t *testing.T) {
	tests := []struct {
		symbol  string
		wantURI string
	}{
		{"^vix", "/v8/finance/chart/%5EVIX?"},
		{"krw=x", "/v8/finance/chart/KRW=X?"},
		{"brk-b", "/v8/finance/chart/BRK-B?"},
	}
	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			rec := &recorder{}
			c := newTestClient(t, statusSequence(rec, loadFixture(t), http.StatusOK))
			if _, err := c.Series(context.Background(), tt.symbol); err != nil {
				t.Fatalf("Series: %v", err)
			}
			if !strings.HasPrefix(rec.uri, tt.wantURI) {
				t.Errorf("request URI = %q, want prefix %q", rec.uri, tt.wantURI)
			}
		})
	}
}

func TestClientSendsCustomUserAgent(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(t, statusSequence(rec, loadFixture(t), http.StatusOK), WithUserAgent("Mozilla/5.0 custom"))
	if _, err := c.Series(context.Background(), "SPY"); err != nil {
		t.Fatalf("Series: %v", err)
	}
	if rec.userAgent != "Mozilla/5.0 custom" {
		t.Errorf("User-Agent = %q", rec.userAgent)
	}
}

func TestClientRetries(t *testing.T) {
	fixture := loadFixture(t)
	tests := []struct {
		name       string
		statuses   []int
		wantHits   int
		wantErr    error // errors.Is target, nil for success
		wantStatus int   // status carried by the final *statusError, 0 when none
	}{
		{name: "429 then 200 succeeds", statuses: []int{429, 200}, wantHits: 2},
		{name: "503 then 200 succeeds", statuses: []int{503, 200}, wantHits: 2},
		{name: "persistent 500 gives up after 3", statuses: []int{500}, wantHits: 3, wantStatus: 500},
		{name: "404 wraps ErrNotFound without retry", statuses: []int{404}, wantHits: 1, wantErr: market.ErrNotFound},
		{name: "403 is not retried", statuses: []int{403}, wantHits: 1, wantStatus: 403},
		{name: "401 on the chart is a plain status, not a session problem", statuses: []int{401}, wantHits: 1, wantStatus: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			c := newTestClient(t, statusSequence(rec, fixture, tt.statuses...))
			s, err := c.Series(context.Background(), "SPY")
			if rec.count() != tt.wantHits {
				t.Errorf("hits = %d, want %d", rec.count(), tt.wantHits)
			}
			if tt.wantErr == nil && tt.wantStatus == 0 {
				if err != nil || s == nil {
					t.Fatalf("Series = %v, %v; want series", s, err)
				}
				return
			}
			if err == nil {
				t.Fatal("Series succeeded, want error")
			}
			if !strings.Contains(err.Error(), "SPY") {
				t.Errorf("error %q does not mention the symbol", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error %q does not wrap %v", err, tt.wantErr)
			}
			if tt.wantStatus != 0 {
				var se *statusError
				if !errors.As(err, &se) || se.status != tt.wantStatus {
					t.Errorf("error %q does not carry status %d", err, tt.wantStatus)
				}
			}
		})
	}
}

func TestClientNotFoundBody(t *testing.T) {
	rec := &recorder{}
	body := []byte(`{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`)
	c := newTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		rec.observe(req)
		respond(w, http.StatusNotFound, body)
	})
	_, err := c.Series(context.Background(), "ZZZZNOPE")
	if !errors.Is(err, market.ErrNotFound) {
		t.Fatalf("error %v does not wrap ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "ZZZZNOPE") || !strings.Contains(err.Error(), "delisted") {
		t.Errorf("error %q should mention the symbol and Yahoo's description", err)
	}
}

func TestClientBadBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, []byte("<html>not json</html>"))
	})
	if _, err := c.Series(context.Background(), "SPY"); err == nil {
		t.Fatal("Series accepted a non-JSON body")
	}
}

func TestClientEmptySymbol(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(t, statusSequence(rec, loadFixture(t), http.StatusOK))
	if _, err := c.Series(context.Background(), "   "); err == nil {
		t.Fatal("Series accepted an empty symbol")
	}
	if _, err := c.SeriesRange(context.Background(), "", time.Time{}, time.Time{}); err == nil {
		t.Fatal("SeriesRange accepted an empty symbol")
	}
	if rec.count() != 0 {
		t.Errorf("hits = %d, want 0", rec.count())
	}
}

func TestClientContextCancelled(t *testing.T) {
	t.Run("before the request", func(t *testing.T) {
		rec := &recorder{}
		c := newTestClient(t, statusSequence(rec, loadFixture(t), http.StatusOK))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Series(ctx, "SPY")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v does not wrap context.Canceled", err)
		}
	})

	t.Run("during backoff", func(t *testing.T) {
		rec := &recorder{}
		// A 10 s backoff would stall the test if the deadline were ignored.
		c := newTestClient(t, statusSequence(rec, nil, http.StatusTooManyRequests), WithRetries(3, 10*time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := c.Series(ctx, "SPY")
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Series took %v, want a prompt return", elapsed)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error %v does not wrap context.DeadlineExceeded", err)
		}
		var se *statusError
		if !errors.As(err, &se) || se.status != http.StatusTooManyRequests {
			t.Errorf("error %q should also carry the last 429", err)
		}
		if rec.count() != 1 {
			t.Errorf("hits = %d, want 1", rec.count())
		}
	})

	t.Run("before an authenticated request", func(t *testing.T) {
		f := newFake(t)
		c := newFakeClient(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.Quote(ctx, []string{"SPY"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v does not wrap context.Canceled", err)
		}
		assertBootstraps(t, f, 0, 0)
	})
}

func TestReadBodyLimit(t *testing.T) {
	big := strings.NewReader(strings.Repeat("x", maxBodyBytes+1))
	if _, err := readBody(big); err == nil {
		t.Fatal("readBody accepted an oversized body")
	}
	small, err := readBody(strings.NewReader("ok"))
	if err != nil || string(small) != "ok" {
		t.Fatalf("readBody(small) = %q, %v", small, err)
	}
}

func TestExcerpt(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  short  ", "short"},
		{"first line\nsecond", "first line"},
		{strings.Repeat("a", 250), strings.Repeat("a", 200) + "..."},
	}
	for _, tt := range tests {
		if got := excerpt([]byte(tt.in)); got != tt.want {
			t.Errorf("excerpt(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestErrorStrings(t *testing.T) {
	if got := (&statusError{status: 500}).Error(); got != "unexpected HTTP status 500" {
		t.Errorf("statusError without body = %q", got)
	}
	if got := (&crumbError{status: 401}).Error(); got != "session rejected with HTTP status 401" {
		t.Errorf("crumbError without body = %q", got)
	}
	if got := (&crumbError{status: 401, body: "Invalid Crumb"}).Error(); !strings.Contains(got, "Invalid Crumb") {
		t.Errorf("crumbError with body = %q", got)
	}
	if isRetryable(&crumbError{status: 401}) || isRetryable(errors.New("x")) || !isRetryable(&statusError{status: 503}) {
		t.Error("isRetryable should hold for 5xx statusErrors only")
	}
}
