// Package yahoo fetches price history, quotes, fund descriptions, symbol
// search results and news from the unofficial Yahoo Finance API and
// exposes them as market.Source, market.RangeSource and market.FundSource.
//
// Price history comes from GET {base}/v8/finance/chart/{symbol} and search
// from GET {base}/v1/finance/search; neither needs credentials. Batch
// quotes (v7) and fund details (v10 quoteSummary) additionally require a
// session: a cookie set by GET https://fc.yahoo.com and a crumb returned by
// GET {base}/v1/test/getcrumb with that cookie. The Client bootstraps the
// session lazily, shares it between requests and refreshes it once when
// Yahoo rejects it.
//
// Yahoo answers 429 to requests without a browser-like User-Agent, so every
// request carries the same simple one, and 429 and 5xx responses are
// retried with exponential backoff.
package yahoo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

const (
	defaultBaseURL    = "https://query1.finance.yahoo.com"
	defaultCookieURL  = "https://fc.yahoo.com"
	defaultUserAgent  = "Mozilla/5.0 (etf-insight-mcp)"
	defaultTimeout    = 20 * time.Second
	defaultAttempts   = 3
	defaultBackoff    = 500 * time.Millisecond
	defaultSummaryTTL = time.Minute

	// maxBackoff caps the delay between two attempts.
	maxBackoff = 30 * time.Second

	// maxBodyBytes caps a response. The full SPY history is about 1 MB, so
	// anything near this limit is not a payload this package reads.
	maxBodyBytes = 32 << 20
)

// Compile-time checks that Client satisfies every market interface it
// claims to implement.
var (
	_ market.Source      = (*Client)(nil)
	_ market.RangeSource = (*Client)(nil)
	_ market.FundSource  = (*Client)(nil)
)

// Client fetches data from Yahoo Finance. The zero value is not usable;
// construct one with New. A Client is safe for concurrent use.
type Client struct {
	httpClient *http.Client
	baseURL    string
	cookieURL  string
	userAgent  string
	attempts   int
	backoff    time.Duration
	summaryTTL time.Duration
	now        func() time.Time

	session   *session
	memo      *summaryMemo
	summaries callGroup[*fundSummary] // quoteSummary requests in flight, by symbol
}

// Option configures a Client created by New.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client. The default has a
// 20 second timeout. The client's own cookie jar is not used: session
// cookies are managed by the Client. A nil client is ignored.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithBaseURL points every API request (chart, quote, quoteSummary,
// getcrumb, search) at another host, typically an httptest.Server. The
// cookie bootstrap keeps its own URL; see WithCookieURL. A trailing slash
// is removed; an empty value is ignored.
func WithBaseURL(base string) Option {
	return func(c *Client) {
		if base != "" {
			c.baseURL = strings.TrimRight(base, "/")
		}
	}
}

// WithCookieURL sets the URL whose response cookies start a session. The
// default is https://fc.yahoo.com; tests point it at the same
// httptest.Server as WithBaseURL. An empty value is ignored.
func WithCookieURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.cookieURL = u
		}
	}
}

// WithUserAgent sets the User-Agent header sent with every request. An
// empty value is ignored because Yahoo rejects requests without one.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithRetries sets the total number of attempts per request and the base
// delay of the exponential backoff between them (base, 2*base, 4*base,
// ... capped at 30 s). n below 1 means a single attempt; a negative base
// means no delay.
func WithRetries(n int, base time.Duration) Option {
	return func(c *Client) {
		c.attempts = max(n, 1)
		c.backoff = max(base, 0)
	}
}

// WithSummaryTTL sets how long a quoteSummary response is kept in memory
// so that FundProfile, Holdings and Performance for one symbol cost a
// single request. The default is one minute; zero or negative disables
// the memo.
func WithSummaryTTL(ttl time.Duration) Option {
	return func(c *Client) {
		c.summaryTTL = max(ttl, 0)
	}
}

// New returns a Client with production defaults, modified by opts.
func New(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    defaultBaseURL,
		cookieURL:  defaultCookieURL,
		userAgent:  defaultUserAgent,
		attempts:   defaultAttempts,
		backoff:    defaultBackoff,
		summaryTTL: defaultSummaryTTL,
		now:        time.Now,
		session:    &session{},
		memo:       &summaryMemo{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Series fetches the full daily history of symbol up to now. It
// implements market.Source and is SeriesRange with an unbounded range.
//
// The symbol is trimmed and upper-cased before the request. Unknown
// symbols return an error wrapping market.ErrNotFound. Every error
// mentions the symbol.
func (c *Client) Series(ctx context.Context, symbol string) (*market.Series, error) {
	return c.SeriesRange(ctx, symbol, time.Time{}, time.Time{})
}

// SeriesRange fetches the daily bars of symbol dated from the calendar
// date from through to, together with the dividends and splits in that
// window. It implements market.RangeSource. A zero from means the
// beginning of history and a zero to means today. from after to is an
// error.
//
// Yahoo applies the bounds to the UTC time stamping each bar, the
// session open (see chartPeriod). That matches the exchange-local date
// wherever the session opens at or after UTC midnight, as in the US,
// Europe and Korea. Where it opens before (the ASX and NZX), a bar is
// stamped on the UTC date before its own, so the result can also hold the
// trading day after to.
//
// The symbol is trimmed and upper-cased before the request. Unknown
// symbols return an error wrapping market.ErrNotFound. Every error
// mentions the symbol.
func (c *Client) SeriesRange(ctx context.Context, symbol string, from, to time.Time) (*market.Series, error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	period1, period2, err := c.chartPeriod(from, to)
	if err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	body, err := c.get(ctx, c.chartURL(symbol, period1, period2), false)
	if err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	s, err := parseChart(body, c.now())
	if err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	return s, nil
}

// historyStart is the period1 sent for "all history": 1900-01-01 UTC.
// Yahoo accepts negative Unix times, and period1=0 would silently cut
// pre-1970 history (^GSPC starts in 1927).
const historyStart int64 = -2208988800

// chartPeriod converts an inclusive calendar-date range into the
// period1/period2 Unix pair the chart endpoint expects. A zero from means
// all history and a zero to means now. to is widened to the last second
// of its UTC day so the bar of that day, which Yahoo stamps at the
// exchange open, is included; see SeriesRange for exchanges that open
// before UTC midnight.
func (c *Client) chartPeriod(from, to time.Time) (period1, period2 int64, err error) {
	period1 = historyStart
	if !from.IsZero() {
		period1 = market.Day(from).Unix()
	}
	period2 = c.now().Unix()
	if !to.IsZero() {
		period2 = market.Day(to).Add(24*time.Hour).Unix() - 1
	}
	if period1 > period2 {
		return 0, 0, fmt.Errorf("range start %s is after end %s",
			market.Day(from).Format(market.DateLayout), market.Day(to).Format(market.DateLayout))
	}
	return period1, period2, nil
}

// chartURL builds the chart request for symbol: daily bars between the two
// Unix times with dividend and split events and adjusted closes. The span
// is given as an explicit period1/period2 pair rather than range=max
// because, as of October 2026, Yahoo answers range=max with monthly bars
// (meta dataGranularity "1mo") no matter what interval says, while an
// explicit period honours interval=1d.
func (c *Client) chartURL(symbol string, period1, period2 int64) string {
	q := url.Values{}
	q.Set("period1", strconv.FormatInt(period1, 10))
	q.Set("period2", strconv.FormatInt(period2, 10))
	q.Set("interval", "1d")
	q.Set("events", "div,splits")
	q.Set("includeAdjustedClose", "true")
	return c.baseURL + "/v8/finance/chart/" + url.PathEscape(symbol) + "?" + q.Encode()
}

// normalizeSymbol trims and upper-cases symbol and rejects an empty one.
func normalizeSymbol(symbol string) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return "", errors.New("yahoo: empty symbol")
	}
	return symbol, nil
}

// get performs one API request and returns the body of a 200 response.
// When auth is set the request carries the session cookies and crumb, and
// a session that Yahoo rejects is refreshed once before the request is
// repeated. Transient failures are retried per WithRetries.
func (c *Client) get(ctx context.Context, rawURL string, auth bool) ([]byte, error) {
	body, err := c.getWithRetry(ctx, rawURL, auth)
	var ce *crumbError
	if auth && errors.As(err, &ce) {
		c.session.invalidate(ce.crumb)
		body, err = c.getWithRetry(ctx, rawURL, auth)
	}
	return body, err
}

// getWithRetry runs getOnce up to c.attempts times, retrying only on 429
// and 5xx responses. It sleeps with exponential backoff between attempts
// and gives up as soon as ctx is done.
func (c *Client) getWithRetry(ctx context.Context, rawURL string, auth bool) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		if attempt > 1 {
			if err := sleep(ctx, c.backoffFor(attempt-1)); err != nil {
				return nil, fmt.Errorf("%w; last attempt: %w", err, lastErr)
			}
		}
		body, err := c.getOnce(ctx, rawURL, auth)
		if err == nil {
			return body, nil
		}
		if !isRetryable(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", c.attempts, lastErr)
}

// getOnce issues a single request. For an auth request it appends the
// crumb and sends the session cookies, bootstrapping the session first
// when needed, and reports a 401, a 403 or an "Invalid Crumb" body as a
// *crumbError. A 404 becomes an error wrapping market.ErrNotFound; any
// other non-200 status becomes a *statusError.
func (c *Client) getOnce(ctx context.Context, rawURL string, auth bool) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	var crumb string
	var cookies []*http.Cookie
	if auth {
		crumb, cookies, err = c.credentials(ctx, u)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("crumb", crumb)
		u.RawQuery = q.Encode()
	}
	resp, err := c.send(ctx, u.String(), cookies)
	if err != nil {
		return nil, err
	}
	if auth && resp.rejectsSession() {
		return nil, &crumbError{crumb: crumb, status: resp.status, body: excerpt(resp.body)}
	}
	switch resp.status {
	case http.StatusOK:
		return resp.body, nil
	case http.StatusNotFound:
		return nil, notFoundError(resp.body)
	default:
		return nil, &statusError{status: resp.status, body: excerpt(resp.body)}
	}
}

// response is what send returns: the status, the body (at most
// maxBodyBytes) and the cookies the server set.
type response struct {
	status  int
	body    []byte
	cookies []*http.Cookie
}

// rejectsSession reports whether Yahoo refused the cookie or crumb: an
// HTTP 401 or 403, or a JSON error naming an invalid crumb.
func (r *response) rejectsSession() bool {
	return r.status == http.StatusUnauthorized || r.status == http.StatusForbidden ||
		bytes.Contains(r.body, []byte("Invalid Crumb"))
}

// send performs a GET with the client User-Agent and the given cookies and
// reads the whole response regardless of status.
func (c *Client) send(ctx context.Context, rawURL string, cookies []*http.Cookie) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	body, err := readBody(resp.Body)
	if closeErr := resp.Body.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &response{status: resp.StatusCode, body: body, cookies: resp.Cookies()}, nil
}

// backoffFor returns the delay before the given retry (1-based): base,
// 2*base, 4*base, ... capped at maxBackoff.
func (c *Client) backoffFor(retry int) time.Duration {
	d := c.backoff
	for i := 1; i < retry && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// statusError is an HTTP response the client could not use.
type statusError struct {
	status int
	body   string // short excerpt for diagnostics
}

func (e *statusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("unexpected HTTP status %d", e.status)
	}
	return fmt.Sprintf("unexpected HTTP status %d: %s", e.status, e.body)
}

// retryable reports whether another attempt may succeed: rate limiting
// and server-side failures are transient, everything else is not.
func (e *statusError) retryable() bool {
	return e.status == http.StatusTooManyRequests || e.status >= 500
}

func isRetryable(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.retryable()
}

// crumbError is a response in which Yahoo rejected the session that was
// sent. It records the rejected crumb so that only that session is
// invalidated, never one a concurrent request has refreshed meanwhile.
type crumbError struct {
	crumb  string
	status int
	body   string
}

func (e *crumbError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("session rejected with HTTP status %d", e.status)
	}
	return fmt.Sprintf("session rejected with HTTP status %d: %s", e.status, e.body)
}

// notFoundError turns a 404 body into an error wrapping market.ErrNotFound,
// keeping Yahoo's description when the body carries one.
func notFoundError(body []byte) error {
	desc := "no data for symbol"
	if e := decodeAPIError(body); e != nil && e.Description != "" {
		desc = e.Description
	}
	return fmt.Errorf("%s: %w", desc, market.ErrNotFound)
}

// readBody reads at most maxBodyBytes from r and fails when more is
// available, so a runaway response cannot exhaust memory.
func readBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("response larger than %d bytes", maxBodyBytes)
	}
	return body, nil
}

// excerpt returns the first line of body, truncated, for error messages.
func excerpt(body []byte) string {
	const limit = 200
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return s
}

// sleep waits for d or until ctx is done, whichever comes first, and
// returns ctx.Err() in the latter case.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
