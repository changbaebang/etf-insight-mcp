package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/changbaebang/etf-insight-mcp/internal/universe"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type refreshPricesInput struct {
	Symbols  []string `json:"symbols,omitempty" jsonschema:"tickers to re-download in full, e.g. [\"SPY\", \"KRW=X\"], case-insensitive, at most 200 in total; default (empty and universe false): every symbol already in the cache"`
	Universe bool     `json:"universe,omitempty" jsonschema:"also re-download every symbol of the built-in universe (default false): one request per symbol, 4 at a time, usually well under a minute, longer when Yahoo throttles"`
}

// refreshResult is the outcome for one symbol.
type refreshResult struct {
	Symbol   string `json:"symbol"`
	OK       bool   `json:"ok" jsonschema:"true when the full history was downloaded and replaced the cached file"`
	Bars     int    `json:"bars" jsonschema:"daily bars now held; when ok is false but bars > 0 the previous cached file is still served"`
	LastDate string `json:"last_date" jsonschema:"last bar YYYY-MM-DD"`
	Error    string `json:"error,omitempty"`
}

type refreshPricesOutput struct {
	Requested      int             `json:"requested"`
	Refreshed      int             `json:"refreshed" jsonschema:"symbols with ok true"`
	Failed         int             `json:"failed"`
	Results        []refreshResult `json:"results" jsonschema:"one row per symbol in request order (explicit symbols first, then the universe)"`
	ElapsedSeconds float64         `json:"elapsed_seconds"`
	Warnings       []string        `json:"warnings" jsonschema:"symbols that were downloaded but could not be written to the cache"`
}

// registerRefreshPrices adds refresh_prices. Like clear_cache it keeps
// defaults out of the input schema (go-sdk v1.8.0 panics applying them to
// null arguments); the descriptions state them instead.
func (d Deps) registerRefreshPrices(s *mcp.Server) {
	destructive, openWorld := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:  "refresh_prices",
		Title: "Refresh cached prices",
		Description: "Re-downloads the full daily price history (closes, adjusted closes, dividends, splits) of symbols from Yahoo Finance and replaces their cache files, ignoring the cache age. Normally unnecessary: prices are reused for 6 hours and then topped up automatically. Use it when a split, a dividend correction or a provider fix is missing from cached data, or to warm the cache before a large comparison. " +
			"symbols picks tickers; with no symbols and universe=false every symbol already in the cache is refreshed; universe=true adds every built-in universe symbol (" + fmt.Sprint(len(universe.Symbols())) + " requests, 4 at a time: usually well under a minute, longer when Yahoo throttles). At most 200 symbols per call. " +
			"Returns one row per symbol with ok, bars, last_date and error; an unknown symbol or a failed download is reported in its row (the previous cached file, if any, is kept and still served) and never stops the others.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Refresh cached prices",
			DestructiveHint: &destructive,
			IdempotentHint:  true,
			OpenWorldHint:   &openWorld,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in refreshPricesInput) (*mcp.CallToolResult, refreshPricesOutput, error) {
		out, err := d.refreshPrices(ctx, in, refreshProgress(ctx, req))
		return nil, out, err
	})
}

// refreshPrices refetches the selected symbols, prefetchConcurrency at a
// time. progress is called after each symbol with the number done.
func (d Deps) refreshPrices(ctx context.Context, in refreshPricesInput, progress func(done, total int, sym string)) (refreshPricesOutput, error) {
	if d.Cache == nil {
		return refreshPricesOutput{}, errCacheNotConfigured
	}
	syms, err := d.refreshSymbols(ctx, in)
	if err != nil {
		return refreshPricesOutput{}, err
	}

	start := d.clock()
	results := make([]refreshResult, len(syms))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
		sem  = make(chan struct{}, prefetchConcurrency)
	)
	finish := func(i int, r refreshResult) {
		results[i] = r
		mu.Lock()
		defer mu.Unlock()
		done++
		progress(done, len(syms), r.Symbol)
	}
	for i, sym := range syms {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			finish(i, refreshResult{Symbol: sym, Error: "not refreshed: " + ctx.Err().Error()})
			continue
		}
		wg.Go(func() {
			defer func() { <-sem }()
			finish(i, d.refreshOne(ctx, sym))
		})
	}
	wg.Wait()

	out := refreshPricesOutput{Requested: len(syms), Results: results, Warnings: []string{}}
	for _, r := range results {
		if !r.OK {
			out.Failed++
			continue
		}
		out.Refreshed++
		out.Warnings = append(out.Warnings, d.staleWarnings(r.Symbol)...)
	}
	out.ElapsedSeconds = round2(d.clock().Sub(start).Seconds())
	return out, nil
}

// refreshSymbols resolves the selection: explicit symbols, then the
// universe when asked, or every cached symbol when neither is given.
func (d Deps) refreshSymbols(ctx context.Context, in refreshPricesInput) ([]string, error) {
	syms := in.Symbols
	if in.Universe {
		syms = append(append([]string{}, syms...), universe.Symbols()...)
	}
	syms = uniqueSymbols(syms)
	if len(syms) == 0 && !in.Universe {
		st, err := d.Cache.Status(ctx)
		if err != nil {
			return nil, err
		}
		for _, ss := range st.Symbols {
			syms = append(syms, ss.Symbol)
		}
		if len(syms) == 0 {
			return nil, errors.New(`nothing to refresh: the cache is empty; pass symbols (e.g. ["SPY"]) or universe=true`)
		}
	}
	if len(syms) > maxCacheRows {
		return nil, fmt.Errorf("%d symbols selected, at most %d per call; split the list", len(syms), maxCacheRows)
	}
	return syms, nil
}

// refreshOne forces a full fetch of sym. The cache serves the old file
// when the download fails, so a nil error with a remembered LastError
// still counts as a failure.
func (d Deps) refreshOne(ctx context.Context, sym string) refreshResult {
	r := refreshResult{Symbol: sym}
	s, err := d.Cache.Refresh(ctx, sym)
	if err != nil {
		r.Error = describeFetchError(sym, err).Error()
		return r
	}
	r.Bars = s.Len()
	if last, ok := s.Last(); ok {
		r.LastDate = formatDate(last.Date)
	}
	if lastErr := d.Cache.LastError(sym); lastErr != nil {
		r.Error = fmt.Sprintf("download failed (%s); the previous cached file is kept and still served", rootCause(lastErr))
		return r
	}
	r.OK = true
	return r
}

// refreshProgress sends MCP progress notifications when the client asked
// for them with a progress token, and does nothing otherwise.
func refreshProgress(ctx context.Context, req *mcp.CallToolRequest) func(done, total int, sym string) {
	if req == nil || req.Session == nil || req.Params == nil {
		return func(int, int, string) {}
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return func(int, int, string) {}
	}
	return func(done, total int, sym string) {
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      float64(done),
			Total:         float64(total),
			Message:       fmt.Sprintf("%s done (%d of %d)", sym, done, total),
		})
	}
}
