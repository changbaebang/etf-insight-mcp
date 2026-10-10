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
	Bars     int    `json:"bars" jsonschema:"daily bars in the history this call ended with: the new download, or the previous cached file when the download failed (0 when there was neither); see error when ok is false"`
	LastDate string `json:"last_date" jsonschema:"last bar YYYY-MM-DD"`
	Error    string `json:"error,omitempty"`
}

type refreshPricesOutput struct {
	Requested      int             `json:"requested"`
	Refreshed      int             `json:"refreshed" jsonschema:"symbols with ok true"`
	Failed         int             `json:"failed"`
	Results        []refreshResult `json:"results" jsonschema:"one row per symbol in request order (explicit symbols first, then the universe)"`
	ElapsedSeconds float64         `json:"elapsed_seconds"`
	Warnings       []string        `json:"warnings" jsonschema:"problems with the call as a whole, such as a cancellation that left symbols unrefreshed; per-symbol problems are in each row's error"`
}

// registerRefreshPrices adds refresh_prices. Like clear_cache it keeps
// defaults out of the input schema (go-sdk v1.8.0 panics applying them to
// null arguments); the descriptions state them instead.
func (d Deps) registerRefreshPrices(s *mcp.Server) {
	destructive, openWorld := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:  "refresh_prices",
		Title: "Refresh cached prices",
		Description: "Re-downloads the full daily price history (closes, adjusted closes, dividends, splits) of symbols from Yahoo Finance and replaces their cache files, ignoring the cache age. Normally unnecessary: prices are reused for 6 hours by default and then topped up automatically. Use it when a split, a dividend correction or a provider fix is missing from cached data, or to warm the cache before a large comparison. " +
			"symbols picks tickers; with no symbols and universe=false every symbol already in the cache is refreshed; universe=true adds every built-in universe symbol (" + fmt.Sprint(len(universe.Symbols())) + " requests, 4 at a time: usually well under a minute, longer when Yahoo throttles). At most 200 symbols per call. " +
			"Returns one row per symbol with ok, bars, last_date and error; ok is true only when the full history was downloaded and written to the cache. An unknown symbol, a failed download or a cache file that could not be written is reported in its row (the previous cached file, if any, is kept) and never stops the others. Once the request is cancelled no new download starts and the remaining rows say so.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Refresh cached prices",
			DestructiveHint: &destructive,
			IdempotentHint:  true,
			OpenWorldHint:   &openWorld,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in refreshPricesInput) (*mcp.CallToolResult, refreshPricesOutput, error) {
		out, err := d.refreshPrices(ctx, in, progressCallback(ctx, "done"))
		return nil, out, err
	})
}

// refreshPrices refetches the selected symbols, prefetchConcurrency at a
// time. progress is called after each symbol with the number done. Once
// ctx is done no new download starts: the cache runs every download
// detached from its caller, so one started now would run on with nobody
// waiting for it. The symbols not reached are reported as not refreshed.
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
		wg        sync.WaitGroup
		mu        sync.Mutex
		done      int
		cancelled int // symbols never started because ctx was done
		sem       = make(chan struct{}, prefetchConcurrency)
	)
	finish := func(i int, r refreshResult) {
		results[i] = r
		mu.Lock()
		defer mu.Unlock()
		done++
		progress(done, len(syms), r.Symbol)
	}
	notStarted := func(i int, sym string) {
		cancelled++
		finish(i, refreshResult{Symbol: sym, Error: "not refreshed: " + ctx.Err().Error()})
	}
	for i, sym := range syms {
		// Check before the select as well: when a slot is free and ctx is
		// done, select picks either case at random.
		if ctx.Err() != nil {
			notStarted(i, sym)
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			notStarted(i, sym)
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
		if r.OK {
			out.Refreshed++
		} else {
			out.Failed++
		}
	}
	if cancelled > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("request cancelled: %d of %d symbols were not refreshed", cancelled, len(syms)))
	}
	out.ElapsedSeconds = round2(d.clock().Sub(start).Seconds())
	return out, nil
}

// refreshSymbols resolves the selection: explicit symbols, then the
// universe when asked, or every cached symbol when neither is given. A
// symbols list that holds only blanks is an error rather than "no symbols",
// so a malformed request never turns into a refresh of the whole cache.
func (d Deps) refreshSymbols(ctx context.Context, in refreshPricesInput) ([]string, error) {
	syms := uniqueSymbols(in.Symbols)
	if len(in.Symbols) > 0 && len(syms) == 0 {
		return nil, errors.New(`no valid symbol in symbols: pass tickers such as ["SPY"], or leave symbols out to refresh every cached symbol`)
	}
	if in.Universe {
		syms = uniqueSymbols(append(syms, universe.Symbols()...))
	}
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

// refreshOne forces a full fetch of sym. The cache answers a failed
// download with the previous file and a failed write with the downloaded
// series, both without an error, so the per-call report decides whether
// the row is ok. It is used rather than LastError and LastWriteError,
// which another tool's fetch of the same symbol can overwrite before they
// are read.
func (d Deps) refreshOne(ctx context.Context, sym string) refreshResult {
	r := refreshResult{Symbol: sym}
	rep, err := d.Cache.RefreshReport(ctx, sym)
	if err != nil {
		r.Error = describeFetchError(sym, err).Error()
		return r
	}
	r.Bars = rep.Series.Len()
	if last, ok := rep.Series.Last(); ok {
		r.LastDate = formatDate(last.Date)
	}
	switch {
	case rep.FetchErr != nil:
		r.Error = fmt.Sprintf("download failed (%s); the previous cached file is kept and still served", rootCause(rep.FetchErr))
	case rep.WriteErr != nil:
		r.Error = fmt.Sprintf("downloaded but the cache file could not be written (%s); the previous cached file, if any, is unchanged and the next call downloads again", rootCause(rep.WriteErr))
	default:
		r.OK = true
	}
	return r
}
