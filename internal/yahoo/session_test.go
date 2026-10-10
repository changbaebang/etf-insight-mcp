package yahoo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestSessionBootstrapsOnceAcrossConcurrentRequests(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.Quote(context.Background(), []string{"SPY", "QQQ"})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Quote %d: %v", i, err)
		}
	}
	assertBootstraps(t, f, 1, 1)
	if got := len(f.requests(quotePath)); got != n {
		t.Errorf("quote requests = %d, want %d", got, n)
	}
	assertAllUserAgent(t, f, defaultUserAgent)
}

func TestSessionSharedAcrossEndpoints(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)
	ctx := context.Background()

	if _, err := c.Quote(ctx, []string{"SPY"}); err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if _, err := c.FundProfile(ctx, "SCHD"); err != nil {
		t.Fatalf("FundProfile: %v", err)
	}
	if _, err := c.Search(ctx, "dividend", 3); err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertBootstraps(t, f, 1, 1)

	for _, r := range f.requests(summaryPath) {
		if r.query.Get("crumb") != "crumb-1" || !strings.Contains(r.cookie, "A3=sess-1") {
			t.Errorf("quoteSummary sent crumb %q cookie %q, want crumb-1 / sess-1", r.query.Get("crumb"), r.cookie)
		}
	}
	for _, r := range f.requests(searchPath) {
		if r.query.Has("crumb") || r.cookie != "" {
			t.Errorf("search should not carry a session, got crumb %q cookie %q", r.query.Get("crumb"), r.cookie)
		}
	}
}

func TestSessionRefreshedOnceWhenRejected(t *testing.T) {
	tests := []struct {
		name         string
		rejectStatus int
	}{
		{name: "401", rejectStatus: http.StatusUnauthorized},
		{name: "403", rejectStatus: http.StatusForbidden},
		{name: "200 with Invalid Crumb body", rejectStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.expireNext = true
			f.rejectStatus = tt.rejectStatus
			c := newFakeClient(t, f)

			p, err := c.FundProfile(context.Background(), "SCHD")
			if err != nil {
				t.Fatalf("FundProfile: %v", err)
			}
			if p.Name != "Schwab U.S. Dividend Equity ETF" {
				t.Errorf("profile = %+v", p)
			}
			assertBootstraps(t, f, 2, 2)

			reqs := f.requests(summaryPath)
			if len(reqs) != 2 {
				t.Fatalf("quoteSummary requests = %d, want 2 (rejected, then retried)", len(reqs))
			}
			first, second := reqs[0], reqs[1]
			if first.query.Get("crumb") != "crumb-1" || !strings.Contains(first.cookie, "A3=sess-1") {
				t.Errorf("first request: crumb %q cookie %q, want crumb-1 / sess-1", first.query.Get("crumb"), first.cookie)
			}
			if second.query.Get("crumb") != "crumb-2" || !strings.Contains(second.cookie, "A3=sess-2") {
				t.Errorf("retry: crumb %q cookie %q, want crumb-2 / sess-2", second.query.Get("crumb"), second.cookie)
			}
			if second.path != summaryPath+"SCHD" || second.query.Get("modules") != summaryModules {
				t.Errorf("retry path/modules = %s %q", second.path, second.query.Get("modules"))
			}
			assertAllUserAgent(t, f, defaultUserAgent)
		})
	}
}

func TestSessionRejectedTwiceIsAnError(t *testing.T) {
	f := newFake(t)
	f.rejectAll = true
	c := newFakeClient(t, f)

	_, err := c.Quote(context.Background(), []string{"SPY"})
	if err == nil {
		t.Fatal("Quote succeeded against a server that rejects every session")
	}
	var ce *crumbError
	if !errors.As(err, &ce) || ce.status != http.StatusUnauthorized {
		t.Errorf("error %q does not carry the rejection", err)
	}
	if !strings.Contains(err.Error(), "Invalid Crumb") || !strings.Contains(err.Error(), "SPY") {
		t.Errorf("error %q should mention Yahoo's description and the symbol", err)
	}
	assertBootstraps(t, f, 2, 2)
	if got := len(f.requests(quotePath)); got != 2 {
		t.Errorf("quote requests = %d, want 2 (one refresh, no more)", got)
	}
}

func TestSessionWithoutCookie(t *testing.T) {
	f := newFake(t)
	f.noCookie = true
	c := newFakeClient(t, f)

	_, err := c.Holdings(context.Background(), "SCHD")
	if err == nil || !strings.Contains(err.Error(), "no session cookie") {
		t.Fatalf("error = %v, want a missing-cookie error", err)
	}
	if got := len(f.requests("/v1/test/getcrumb")); got != 0 {
		t.Errorf("crumb requests = %d, want 0 without a cookie", got)
	}
	if got := len(f.requests(summaryPath)); got != 0 {
		t.Errorf("quoteSummary requests = %d, want 0", got)
	}
}

func TestSessionCrumbOutageIsRetried(t *testing.T) {
	f := newFake(t)
	f.crumbStatus = http.StatusServiceUnavailable
	c := newFakeClient(t, f)

	_, err := c.Performance(context.Background(), "BND")
	var se *statusError
	if !errors.As(err, &se) || se.status != http.StatusServiceUnavailable {
		t.Fatalf("error %v does not carry the crumb endpoint's 503", err)
	}
	if !strings.Contains(err.Error(), "giving up after 3 attempts") {
		t.Errorf("error %q should report the exhausted retries", err)
	}
	if got := len(f.requests("/v1/test/getcrumb")); got != 3 {
		t.Errorf("crumb requests = %d, want 3 (one per attempt)", got)
	}
	if got := len(f.requests(summaryPath)); got != 0 {
		t.Errorf("quoteSummary requests = %d, want 0", got)
	}
}

func TestAuthenticatedEndpointRetriesTransientStatuses(t *testing.T) {
	f := newFake(t)
	f.authStatuses = []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusOK}
	c := newFakeClient(t, f)

	quotes, err := c.Quote(context.Background(), []string{"SPY", "QQQ"})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(quotes) != 2 {
		t.Errorf("got %d quotes, want 2", len(quotes))
	}
	assertBootstraps(t, f, 1, 1)
	if got := len(f.requests(quotePath)); got != 3 {
		t.Errorf("quote requests = %d, want 3", got)
	}
}

func TestAuthenticatedNotFoundIsNotASessionProblem(t *testing.T) {
	f := newFake(t)
	c := newFakeClient(t, f)

	_, err := c.FundProfile(context.Background(), "ZZZZNOPE")
	if !errors.Is(err, market.ErrNotFound) {
		t.Fatalf("error %v does not wrap ErrNotFound", err)
	}
	assertBootstraps(t, f, 1, 1)
	if got := len(f.requests(summaryPath)); got != 1 {
		t.Errorf("quoteSummary requests = %d, want 1 (no refresh on 404)", got)
	}
}

// slowCookieServer serves quotes behind a cookie endpoint that holds every
// bootstrap until release is closed. It reports the first cookie request
// on arrived and counts them all in hits.
func slowCookieServer(t *testing.T) (c *Client, arrived <-chan struct{}, release chan<- struct{}, hits *atomic.Int32) {
	t.Helper()
	quotes := readFixture(t, "quote_v7.json")
	arrivedCh := make(chan struct{}, 1)
	releaseCh := make(chan struct{})
	hits = new(atomic.Int32)
	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if hits.Add(1) == 1 {
				arrivedCh <- struct{}{}
			}
			<-releaseCh
			http.SetCookie(w, &http.Cookie{Name: "A3", Value: "x", Path: "/"})
			respond(w, http.StatusNotFound, nil)
		case "/v1/test/getcrumb":
			respond(w, http.StatusOK, []byte("crumb"))
		default:
			respond(w, http.StatusOK, quotes)
		}
	}, WithRetries(1, 0))
	return c, arrivedCh, releaseCh, hits
}

// waitFor returns the value sent on ch, failing the test after 5 s.
func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func TestSessionBootstrapHonoursCallerContext(t *testing.T) {
	c, arrived, release, hits := slowCookieServer(t)

	first := make(chan error, 1)
	go func() { _, err := c.Quote(context.Background(), []string{"SPY"}); first <- err }()
	waitFor(t, arrived, "the first bootstrap")

	// A second caller with a short deadline must not wait for the first
	// caller's bootstrap to finish.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := c.Quote(ctx, []string{"QQQ"}); second <- err }()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("second caller: %v, want its own deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("a caller with a 50 ms deadline was still waiting after 3 s for another caller's bootstrap")
	}

	close(release)
	if err := waitFor(t, first, "the first caller"); err != nil {
		t.Errorf("first caller: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("cookie requests = %d, want 1", got)
	}
}

// roundTripFunc lets a function stand in for the network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionBootstrapIsSharedByWaiters(t *testing.T) {
	quotes := readFixture(t, "quote_v7.json")
	for _, cookieStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(cookieStatus), func(t *testing.T) {
			// synctest.Wait below returns once every goroutine is blocked on
			// a channel, so all callers have joined the bootstrap in flight
			// before it is released. That needs an in-memory transport: a
			// real connection does not count as blocked. Neither does a
			// mutex, so code that made callers queue on one would hang here
			// until the test binary times out;
			// TestSessionBootstrapHonoursCallerContext reports that case
			// within seconds.
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var hits atomic.Int32
				transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
					status, header, body := http.StatusOK, http.Header{}, quotes
					switch r.URL.Path {
					case "/":
						hits.Add(1)
						<-release
						status, body = cookieStatus, []byte("cookie service down")
						if cookieStatus == http.StatusOK {
							status = http.StatusNotFound // as Yahoo answers, with the cookie
							header.Set("Set-Cookie", "A3=x; Path=/")
						}
					case "/v1/test/getcrumb":
						body = []byte("crumb")
					}
					return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
				})
				c := New(WithBaseURL("https://yahoo.test"), WithCookieURL("https://yahoo.test/"),
					WithHTTPClient(&http.Client{Transport: transport}), WithRetries(1, 0))

				const n = 4
				errs := make(chan error, n)
				for range n {
					go func() { _, err := c.Quote(context.Background(), []string{"SPY"}); errs <- err }()
				}
				synctest.Wait()
				close(release)

				for range n {
					err := <-errs
					var se *statusError
					switch {
					case cookieStatus == http.StatusOK && err != nil:
						t.Errorf("caller: %v", err)
					case cookieStatus != http.StatusOK && (!errors.As(err, &se) || se.status != cookieStatus):
						t.Errorf("caller: %v, want the shared bootstrap's HTTP %d", err, cookieStatus)
					}
				}
				if got := hits.Load(); got != 1 {
					t.Errorf("cookie requests = %d, want 1 shared by %d callers", got, n)
				}
			})
		})
	}
}

func TestCallGroup(t *testing.T) {
	// synctest runs the goroutines on a fake clock and lets the test wait
	// until every one of them is blocked, so the order below is exact.
	synctest.Test(t, func(t *testing.T) {
		var g callGroup[int]
		release := make(chan struct{})
		var runs atomic.Int32
		slow := func(ctx context.Context) (int, error) {
			runs.Add(1)
			select {
			case <-release:
				return 42, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}

		// The first caller starts the call, three more and a hurried one
		// join it.
		firstCtx, cancelFirst := context.WithCancel(context.Background())
		first := make(chan error, 1)
		go func() { _, err := g.do(firstCtx, "k", slow); first <- err }()
		synctest.Wait()
		results := make(chan int, 3)
		for range 3 {
			go func() {
				v, err := g.do(context.Background(), "k", slow)
				if err != nil {
					t.Errorf("waiter: %v", err)
				}
				results <- v
			}()
		}
		hurriedCtx, cancelHurried := context.WithTimeout(context.Background(), time.Second)
		defer cancelHurried()
		hurried := make(chan error, 1)
		go func() { _, err := g.do(hurriedCtx, "k", slow); hurried <- err }()
		synctest.Wait()

		// The first caller giving up ends only its own wait.
		cancelFirst()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Errorf("first caller: %v, want context.Canceled", err)
		}
		// The hurried caller leaves at its deadline while the call runs on.
		time.Sleep(time.Second)
		if err := <-hurried; !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("hurried caller: %v, want context.DeadlineExceeded", err)
		}

		close(release)
		for range 3 {
			if v := <-results; v != 42 {
				t.Errorf("waiter got %d, want the shared 42", v)
			}
		}
		if got := runs.Load(); got != 1 {
			t.Errorf("runs = %d, want 1", got)
		}

		// A finished call is forgotten: the next caller starts a new one.
		v, err := g.do(context.Background(), "k", func(context.Context) (int, error) { return 7, nil })
		if v != 7 || err != nil {
			t.Errorf("after the call: %d, %v; want a fresh call's 7", v, err)
		}

		// A caller whose context is already done starts nothing.
		dead, cancelDead := context.WithCancel(context.Background())
		cancelDead()
		_, err = g.do(dead, "k", func(context.Context) (int, error) {
			t.Error("a call was started for a caller whose context was done")
			return 0, nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("dead caller: %v, want context.Canceled", err)
		}
		synctest.Wait()
	})
}

func TestSessionInvalidateKeepsNewerCrumb(t *testing.T) {
	s := &session{crumb: "fresh"}
	s.invalidate("stale")
	if s.crumb != "fresh" {
		t.Errorf("invalidate(stale) cleared a fresh crumb")
	}
	s.invalidate("fresh")
	if s.crumb != "" {
		t.Errorf("invalidate(fresh) kept crumb %q", s.crumb)
	}
}

func TestFetchCrumbRejectsNonCrumbBodies(t *testing.T) {
	bodies := []string{"", "   ", "<html>blocked</html>", `{"finance":{"error":"nope"}}`, "two\nlines"}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			// Serve the odd body from the crumb endpoint via a one-off handler.
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					http.SetCookie(w, &http.Cookie{Name: "A3", Value: "x"})
					respond(w, http.StatusNotFound, nil)
					return
				}
				respond(w, http.StatusOK, []byte(body))
			})
			c := New(WithBaseURL(srv), WithCookieURL(srv), WithRetries(1, 0))
			_, err := c.Quote(context.Background(), []string{"SPY"})
			if err == nil || !strings.Contains(err.Error(), "crumb") {
				t.Fatalf("error = %v, want a crumb error", err)
			}
		})
	}
}
