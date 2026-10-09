// Package yahoo fetches daily price history from the unofficial Yahoo
// Finance chart API and exposes it as a market.Source.
//
// The endpoint is GET {base}/v8/finance/chart/{symbol}. It answers 429 to
// requests without a browser-like User-Agent, so the client always sends
// one, and it retries 429 and 5xx responses with exponential backoff.
package yahoo

import (
	"context"
	"encoding/json"
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
	defaultBaseURL   = "https://query1.finance.yahoo.com"
	defaultUserAgent = "Mozilla/5.0 (etf-insight-mcp)"
	defaultTimeout   = 20 * time.Second
	defaultAttempts  = 3
	defaultBackoff   = 500 * time.Millisecond

	// maxBackoff caps the delay between two attempts.
	maxBackoff = 30 * time.Second

	// maxBodyBytes caps a chart response. The full SPY history is about
	// 1 MB, so anything near this limit is not a chart payload.
	maxBodyBytes = 32 << 20
)

// Client fetches price history from Yahoo Finance. The zero value is not
// usable; construct one with New. A Client is safe for concurrent use.
type Client struct {
	httpClient *http.Client
	baseURL    string
	userAgent  string
	attempts   int
	backoff    time.Duration
}

// Option configures a Client created by New.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client. The default has a
// 20 second timeout. A nil client is ignored.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithBaseURL points the client at another host, typically an
// httptest.Server. A trailing slash is removed; an empty value is ignored.
func WithBaseURL(base string) Option {
	return func(c *Client) {
		if base != "" {
			c.baseURL = strings.TrimRight(base, "/")
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

// WithRetries sets the total number of attempts per fetch and the base
// delay of the exponential backoff between them (base, 2*base, 4*base,
// ... capped at 30 s). n below 1 means a single attempt; a negative base
// means no delay.
func WithRetries(n int, base time.Duration) Option {
	return func(c *Client) {
		c.attempts = max(n, 1)
		c.backoff = max(base, 0)
	}
}

// New returns a Client with production defaults, modified by opts.
func New(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    defaultBaseURL,
		userAgent:  defaultUserAgent,
		attempts:   defaultAttempts,
		backoff:    defaultBackoff,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Series fetches the full daily history of symbol. It implements
// market.Source.
//
// The symbol is trimmed and upper-cased before the request. Unknown
// symbols return an error wrapping market.ErrNotFound. Every error
// mentions the symbol.
func (c *Client) Series(ctx context.Context, symbol string) (*market.Series, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, errors.New("yahoo: empty symbol")
	}
	body, err := c.fetchWithRetry(ctx, symbol)
	if err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	s, err := parseChart(body, time.Now())
	if err != nil {
		return nil, fmt.Errorf("yahoo: %s: %w", symbol, err)
	}
	return s, nil
}

// fetchWithRetry performs the chart request up to c.attempts times,
// retrying only on 429 and 5xx responses. It sleeps with exponential
// backoff between attempts and gives up as soon as ctx is done.
func (c *Client) fetchWithRetry(ctx context.Context, symbol string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		if attempt > 1 {
			if err := sleep(ctx, c.backoffFor(attempt-1)); err != nil {
				return nil, fmt.Errorf("%w; last attempt: %w", err, lastErr)
			}
		}
		body, err := c.fetchOnce(ctx, symbol)
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

// fetchOnce issues a single chart request and returns the response body
// on 200. A 404 becomes an error wrapping market.ErrNotFound; any other
// status becomes a *statusError.
func (c *Client) fetchOnce(ctx context.Context, symbol string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.chartURL(symbol), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

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
	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return nil, notFoundError(body)
	default:
		return nil, &statusError{status: resp.StatusCode, body: excerpt(body)}
	}
}

// historyStart is the period1 sent for "all history": 1900-01-01 UTC.
// Yahoo accepts negative Unix times, and period1=0 would silently cut
// pre-1970 history (^GSPC starts in 1927).
const historyStart int64 = -2208988800

// chartURL builds the request URL for symbol: the whole daily history
// with dividend events and adjusted closes. The span is given as an
// explicit period1/period2 pair rather than range=max because, as of
// October 2026, Yahoo answers range=max with monthly bars (meta
// dataGranularity "1mo") no matter what interval says, while an explicit
// period honours interval=1d.
func (c *Client) chartURL(symbol string) string {
	q := url.Values{}
	q.Set("period1", strconv.FormatInt(historyStart, 10))
	q.Set("period2", strconv.FormatInt(time.Now().Unix(), 10))
	q.Set("interval", "1d")
	q.Set("events", "div")
	q.Set("includeAdjustedClose", "true")
	return c.baseURL + "/v8/finance/chart/" + url.PathEscape(symbol) + "?" + q.Encode()
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

// notFoundError turns a 404 body into an error wrapping market.ErrNotFound,
// keeping Yahoo's description when the body carries one.
func notFoundError(body []byte) error {
	var resp chartResponse
	if err := json.Unmarshal(body, &resp); err == nil && resp.Chart.Error != nil {
		return resp.Chart.Error.err()
	}
	return fmt.Errorf("no data for symbol: %w", market.ErrNotFound)
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
