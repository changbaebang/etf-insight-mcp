package yahoo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// session is the cookie and crumb pair that Yahoo's authenticated
// endpoints (v7 quote, v10 quoteSummary) require. It starts empty and is
// filled on first use. mu guards jar and crumb and is never held during a
// request; requests that find no session share one bootstrap through boot.
type session struct {
	mu    sync.Mutex
	jar   http.CookieJar
	crumb string

	boot callGroup[sessionKeys]
}

// sessionKeys is one cookie jar with the crumb Yahoo issued for it.
type sessionKeys struct {
	jar   http.CookieJar
	crumb string
}

// current returns the installed session, if there is one.
func (s *session) current() (sessionKeys, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sessionKeys{jar: s.jar, crumb: s.crumb}, s.crumb != ""
}

// install makes keys the session every request uses from now on.
func (s *session) install(keys sessionKeys) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jar, s.crumb = keys.jar, keys.crumb
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
// request to u. When there is no session it starts a bootstrap, or joins
// the one already running, and waits for it until ctx is done: a caller
// never waits longer than its own deadline, and every caller waiting on
// one bootstrap gets its result, success or failure.
func (c *Client) credentials(ctx context.Context, u *url.URL) (crumb string, cookies []*http.Cookie, err error) {
	keys, ok := c.session.current()
	if !ok {
		keys, err = c.session.boot.do(ctx, "", c.bootstrap)
		if err != nil {
			return "", nil, fmt.Errorf("session: %w", err)
		}
	}
	return keys.crumb, keys.jar.Cookies(u), nil
}

// bootstrap obtains a fresh cookie and crumb and installs them. It is the
// shared call behind credentials, so it first returns a session installed
// by a bootstrap that finished after its caller looked. Failures are not
// retried here; a transient one surfaces as a retryable error to every
// request that was waiting for it.
func (c *Client) bootstrap(ctx context.Context) (sessionKeys, error) {
	if keys, ok := c.session.current(); ok {
		return keys, nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return sessionKeys{}, fmt.Errorf("cookie jar: %w", err)
	}
	if err := c.fetchCookies(ctx, jar); err != nil {
		return sessionKeys{}, fmt.Errorf("cookie: %w", err)
	}
	crumb, err := c.fetchCrumb(ctx, jar)
	if err != nil {
		return sessionKeys{}, fmt.Errorf("crumb: %w", err)
	}
	keys := sessionKeys{jar: jar, crumb: crumb}
	c.session.install(keys)
	return keys, nil
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

// sharedCallTimeout bounds a call that several requests share. Such a
// call outlives the request that started it, so no request's context ends
// it; this is the backstop for an HTTP client without a timeout. With the
// default client every request gives up after 20 s anyway.
const sharedCallTimeout = 2 * time.Minute

// callGroup runs at most one call per key at a time and hands its result
// to every caller that asks for the key while it runs, like
// golang.org/x/sync/singleflight. Unlike singleflight it honours each
// caller's context: a caller stops waiting as soon as its own context is
// done, and the call runs under a context detached from every caller's
// cancellation (context.WithoutCancel plus sharedCallTimeout), so one
// caller giving up does not fail the others. Results are not kept once
// the call returns. The zero value is ready to use.
type callGroup[T any] struct {
	mu    sync.Mutex
	calls map[string]*groupCall[T]
}

// groupCall is one call in flight. val and err are set before done is
// closed and never written again.
type groupCall[T any] struct {
	done chan struct{}
	val  T
	err  error
}

// do returns the result of fn for key, starting fn unless a call for key is
// already running, and waits for it or for ctx, whichever comes first. A
// caller whose ctx is already done starts nothing, since nobody would wait
// for the result.
func (g *callGroup[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	g.mu.Lock()
	call, ok := g.calls[key]
	if !ok {
		call = &groupCall[T]{done: make(chan struct{})}
		if g.calls == nil {
			g.calls = make(map[string]*groupCall[T])
		}
		g.calls[key] = call
		go g.run(context.WithoutCancel(ctx), key, call, fn)
	}
	g.mu.Unlock()

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-call.done:
		return call.val, call.err
	}
}

// run performs call and removes it from the group before waking its
// waiters, so a caller that arrives afterwards starts a fresh call.
func (g *callGroup[T]) run(ctx context.Context, key string, call *groupCall[T], fn func(context.Context) (T, error)) {
	ctx, cancel := context.WithTimeout(ctx, sharedCallTimeout)
	defer cancel()
	call.val, call.err = fn(ctx)

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(call.done)
}
