package yahoo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
)

// session is the cookie and crumb pair that Yahoo's authenticated
// endpoints (v7 quote, v10 quoteSummary) require. It starts empty and is
// filled on first use. The mutex also serialises the bootstrap, so
// concurrent first requests perform it once.
type session struct {
	mu    sync.Mutex
	jar   http.CookieJar
	crumb string
}

// invalidate forgets the session when its crumb is still the rejected one.
// A session that a concurrent request has already refreshed is kept.
func (s *session) invalidate(rejected string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.crumb == rejected {
		s.crumb = ""
		s.jar = nil
	}
}

// credentials returns the current crumb and the cookies to send with a
// request to u, bootstrapping the session first when there is none.
func (c *Client) credentials(ctx context.Context, u *url.URL) (crumb string, cookies []*http.Cookie, err error) {
	s := c.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.crumb == "" {
		if err := c.bootstrap(ctx); err != nil {
			return "", nil, fmt.Errorf("session: %w", err)
		}
	}
	return s.crumb, s.jar.Cookies(u), nil
}

// bootstrap obtains a fresh cookie and crumb and installs them. The
// caller holds the session mutex. Failures are not retried here; a
// transient one surfaces as a retryable error to the request that
// triggered the bootstrap.
func (c *Client) bootstrap(ctx context.Context) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("cookie jar: %w", err)
	}
	if err := c.fetchCookies(ctx, jar); err != nil {
		return fmt.Errorf("cookie: %w", err)
	}
	crumb, err := c.fetchCrumb(ctx, jar)
	if err != nil {
		return fmt.Errorf("crumb: %w", err)
	}
	c.session.jar, c.session.crumb = jar, crumb
	return nil
}

// fetchCookies requests the cookie URL and stores the cookies it sets in
// jar. Yahoo answers 404 there, so the status only matters when no cookie
// arrives: 429 and 5xx then stay retryable, anything else is final.
func (c *Client) fetchCookies(ctx context.Context, jar http.CookieJar) error {
	u, err := url.Parse(c.cookieURL)
	if err != nil {
		return fmt.Errorf("bad cookie URL %q: %w", c.cookieURL, err)
	}
	resp, err := c.send(ctx, u.String(), nil)
	if err != nil {
		return err
	}
	if len(resp.cookies) == 0 {
		return &statusError{status: resp.status, body: "no session cookie in the response"}
	}
	jar.SetCookies(u, resp.cookies)
	return nil
}

// fetchCrumb asks the crumb endpoint with the jar's cookies. The body is
// the crumb itself as plain text.
func (c *Client) fetchCrumb(ctx context.Context, jar http.CookieJar) (string, error) {
	u, err := url.Parse(c.baseURL + "/v1/test/getcrumb")
	if err != nil {
		return "", fmt.Errorf("bad crumb URL: %w", err)
	}
	resp, err := c.send(ctx, u.String(), jar.Cookies(u))
	if err != nil {
		return "", err
	}
	if resp.status != http.StatusOK {
		return "", &statusError{status: resp.status, body: excerpt(resp.body)}
	}
	crumb := strings.TrimSpace(string(resp.body))
	if crumb == "" || strings.ContainsAny(crumb, "<>{}\"\n") {
		return "", fmt.Errorf("unexpected crumb body %q", excerpt(resp.body))
	}
	return crumb, nil
}
