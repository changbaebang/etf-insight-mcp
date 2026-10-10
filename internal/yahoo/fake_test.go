package yahoo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeYahoo mimics, on one httptest.Server, every endpoint the Client
// uses: the cookie bootstrap at "/", getcrumb, chart, quoteSummary, v7
// quote and search. It hands out numbered cookies ("sess-N") and crumbs
// ("crumb-N"), validates them on the authenticated endpoints and records
// every request. All fields are guarded by mu, except the two summary
// channels, which are only read; set the knobs before the first request.
type fakeYahoo struct {
	mu sync.Mutex

	// summaryGate, when set, holds every quoteSummary request until it is
	// closed, and summaryArrived (buffered) receives a token as each such
	// request arrives. Both are used before mu is taken, so a held request
	// does not block the other endpoints.
	summaryGate    chan struct{}
	summaryArrived chan struct{}

	cookies int    // bootstraps served
	crumbs  int    // crumbs served
	valid   string // crumb currently accepted; "" accepts none

	// Knobs.
	expireNext   bool  // reject the next authenticated request and retire its crumb
	rejectAll    bool  // reject every authenticated request
	rejectStatus int   // status of a rejection; 0 means 401
	noCookie     bool  // bootstrap sets no cookie
	crumbStatus  int   // status of getcrumb; 0 means 200
	authStatuses []int // statuses served before authenticated bodies; 200 serves the body

	// Bodies. A quoteSummary symbol absent from summaries is answered 404;
	// a nil quotes body echoes the requested symbols.
	summaries map[string][]byte
	quotes    []byte
	search    []byte
	chart     []byte

	reqs []fakeRequest
}

// fakeRequest is what the fake remembers about one request.
type fakeRequest struct {
	path   string
	uri    string // raw request URI, to check escaping
	query  url.Values
	cookie string // raw Cookie header
	ua     string
}

// invalidCrumbBody is Yahoo's answer to a stale crumb, verbatim.
const invalidCrumbBody = `{"finance":{"result":null,"error":{"code":"Unauthorized","description":"Invalid Crumb"}}}`

func (f *fakeYahoo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.summaryGate != nil && strings.HasPrefix(r.URL.Path, summaryPath) {
		select {
		case f.summaryArrived <- struct{}{}:
		default:
		}
		<-f.summaryGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, fakeRequest{
		path: r.URL.Path, uri: r.RequestURI, query: r.URL.Query(),
		cookie: r.Header.Get("Cookie"), ua: r.Header.Get("User-Agent"),
	})
	switch {
	case r.URL.Path == "/":
		f.serveCookie(w)
	case r.URL.Path == "/v1/test/getcrumb":
		f.serveCrumb(w, r)
	case strings.HasPrefix(r.URL.Path, "/v8/finance/chart/"):
		respond(w, http.StatusOK, f.chart)
	case r.URL.Path == "/v1/finance/search":
		respond(w, http.StatusOK, f.search)
	case strings.HasPrefix(r.URL.Path, "/v10/finance/quoteSummary/"):
		if f.authorize(w, r) {
			f.serveSummary(w, path.Base(r.URL.Path))
		}
	case r.URL.Path == "/v7/finance/quote":
		if f.authorize(w, r) {
			f.serveQuotes(w, r.URL.Query().Get("symbols"))
		}
	default:
		respond(w, http.StatusNotFound, []byte("unknown endpoint "+r.URL.Path))
	}
}

func (f *fakeYahoo) serveCookie(w http.ResponseWriter) {
	f.cookies++
	if !f.noCookie {
		http.SetCookie(w, &http.Cookie{Name: "A3", Value: fmt.Sprintf("sess-%d", f.cookies), Path: "/"})
	}
	respond(w, http.StatusNotFound, []byte("not found, but you have a cookie"))
}

func (f *fakeYahoo) serveCrumb(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("A3"); err != nil {
		respond(w, http.StatusUnauthorized, []byte("no cookie"))
		return
	}
	if f.crumbStatus != 0 && f.crumbStatus != http.StatusOK {
		respond(w, f.crumbStatus, []byte("crumb service down"))
		return
	}
	f.crumbs++
	f.valid = fmt.Sprintf("crumb-%d", f.crumbs)
	w.Header().Set("Content-Type", "text/plain")
	respond(w, http.StatusOK, []byte(f.valid))
}

// authorize checks the cookie and crumb, applies the rejection knobs and
// the pre-body status sequence, and reports whether the body may be
// served.
func (f *fakeYahoo) authorize(w http.ResponseWriter, r *http.Request) bool {
	_, cookieErr := r.Cookie("A3")
	crumb := r.URL.Query().Get("crumb")
	if f.expireNext {
		f.expireNext = false
		f.valid = ""
	}
	if f.rejectAll || cookieErr != nil || crumb == "" || crumb != f.valid {
		status := f.rejectStatus
		if status == 0 {
			status = http.StatusUnauthorized
		}
		respond(w, status, []byte(invalidCrumbBody))
		return false
	}
	if len(f.authStatuses) > 0 {
		status := f.authStatuses[0]
		f.authStatuses = f.authStatuses[1:]
		if status != http.StatusOK {
			respond(w, status, []byte("upstream says "+http.StatusText(status)))
			return false
		}
	}
	return true
}

func (f *fakeYahoo) serveSummary(w http.ResponseWriter, symbol string) {
	body, ok := f.summaries[symbol]
	if !ok {
		msg := fmt.Sprintf(`{"quoteSummary":{"result":null,"error":{"code":"Not Found","description":"Quote not found for symbol: %s"}}}`, symbol)
		respond(w, http.StatusNotFound, []byte(msg))
		return
	}
	respond(w, http.StatusOK, body)
}

func (f *fakeYahoo) serveQuotes(w http.ResponseWriter, symbols string) {
	if f.quotes != nil {
		respond(w, http.StatusOK, f.quotes)
		return
	}
	var result []map[string]any
	for i, s := range strings.Split(symbols, ",") {
		result = append(result, map[string]any{"symbol": s, "regularMarketPrice": float64(i + 1)})
	}
	body, err := json.Marshal(map[string]any{"quoteResponse": map[string]any{"result": result, "error": nil}})
	if err != nil {
		panic(err)
	}
	respond(w, http.StatusOK, body)
}

// requests returns the recorded requests whose path starts with prefix.
func (f *fakeYahoo) requests(prefix string) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRequest
	for _, r := range f.reqs {
		if strings.HasPrefix(r.path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// bootstraps returns how many cookie and crumb requests were served.
func (f *fakeYahoo) bootstraps() (cookies, crumbs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cookies, f.crumbs
}

// Endpoint path prefixes used with requests.
const (
	summaryPath = "/v10/finance/quoteSummary/"
	quotePath   = "/v7/finance/quote"
	searchPath  = "/v1/finance/search"
	chartPath   = "/v8/finance/chart/"
)

// readFixture loads a testdata file.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

// newFake returns a fakeYahoo loaded with every fixture.
func newFake(t *testing.T) *fakeYahoo {
	t.Helper()
	return &fakeYahoo{
		summaries: map[string][]byte{
			"SCHD": readFixture(t, "quotesummary_schd.json"),
			"BND":  readFixture(t, "quotesummary_bnd.json"),
		},
		quotes: readFixture(t, "quote_v7.json"),
		search: readFixture(t, "search.json"),
		chart:  readFixture(t, "tqqq_splits.json"),
	}
}

// newFakeClient serves f and returns a Client pointed at it with fast
// retries and a fixed clock. Extra options are applied last.
func newFakeClient(t *testing.T, f *fakeYahoo, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	base := []Option{
		WithBaseURL(srv.URL), WithCookieURL(srv.URL),
		WithHTTPClient(srv.Client()), WithRetries(3, time.Millisecond),
	}
	c := New(append(base, opts...)...)
	c.now = func() time.Time { return fixtureNow }
	return c
}

// assertBootstraps checks the number of cookie and crumb fetches.
func assertBootstraps(t *testing.T, f *fakeYahoo, wantCookies, wantCrumbs int) {
	t.Helper()
	cookies, crumbs := f.bootstraps()
	if cookies != wantCookies || crumbs != wantCrumbs {
		t.Errorf("bootstraps = %d cookies, %d crumbs; want %d, %d", cookies, crumbs, wantCookies, wantCrumbs)
	}
}

// assertAllUserAgent checks that every recorded request carried ua.
func assertAllUserAgent(t *testing.T, f *fakeYahoo, ua string) {
	t.Helper()
	for _, r := range f.requests("/") {
		if r.ua != ua {
			t.Errorf("%s sent User-Agent %q, want %q", r.path, r.ua, ua)
		}
	}
}
