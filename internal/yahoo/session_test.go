package yahoo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

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
