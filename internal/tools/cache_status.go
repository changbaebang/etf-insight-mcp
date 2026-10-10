package tools

import (
	"context"
	"errors"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxCacheRows caps the symbol lists of the ops tools (cache_status rows,
// clear_cache and refresh_prices symbols).
const maxCacheRows = 200

// cacheBytesPerMiB converts byte counts into the *_mb fields.
const cacheBytesPerMiB = 1 << 20

// errCacheNotConfigured answers every ops tool on a server built without
// the on-disk cache (Deps.Cache nil).
var errCacheNotConfigured = errors.New("cache not configured: this server runs without the on-disk price cache, so there is nothing to inspect, clear or refresh; every call already fetches fresh data")

// cacheStatusInput is empty: cache_status takes no arguments.
type cacheStatusInput struct{}

// cacheSymbolRow is one cached price file on the wire.
type cacheSymbolRow struct {
	Symbol        string `json:"symbol"`
	Bars          int    `json:"bars" jsonschema:"daily bars in the file; 0 when the file is unreadable (see warnings)"`
	FirstDate     string `json:"first_date" jsonschema:"first cached bar YYYY-MM-DD"`
	LastDate      string `json:"last_date" jsonschema:"last cached bar YYYY-MM-DD"`
	FetchedAt     string `json:"fetched_at" jsonschema:"when the file was last brought up to date, RFC 3339 UTC"`
	FullFetchedAt string `json:"full_fetched_at" jsonschema:"when the whole history was last fetched, RFC 3339 UTC; refreshes in between only append recent days"`
	Bytes         int64  `json:"bytes"`
	LastError     string `json:"last_error,omitempty" jsonschema:"most recent failed fetch of the symbol in this server process; the cached file is then served as it is"`
}

// cacheStatusOutput is cache.Status on the wire.
type cacheStatusOutput struct {
	Dir         string           `json:"dir"`
	Files       int              `json:"files" jsonschema:"price and fund files together"`
	FundFiles   int              `json:"fund_files" jsonschema:"fund profile, holdings and performance files (part of files)"`
	TotalBytes  int64            `json:"total_bytes"`
	TotalMB     float64          `json:"total_mb" jsonschema:"total_bytes in MiB, 2 decimals"`
	OldestFetch string           `json:"oldest_fetch" jsonschema:"oldest fetch time of any readable file, RFC 3339 UTC; empty when the cache is empty"`
	NewestFetch string           `json:"newest_fetch" jsonschema:"newest fetch time of any readable file, RFC 3339 UTC"`
	SymbolCount int              `json:"symbol_count" jsonschema:"cached price files; symbols lists at most 200 of them"`
	Truncated   bool             `json:"truncated" jsonschema:"true when symbols was cut at 200 rows"`
	Symbols     []cacheSymbolRow `json:"symbols" jsonschema:"one row per cached price file, sorted by symbol"`
	Warnings    []string         `json:"warnings" jsonschema:"unreadable files, failed fetches or writes, files not fetched for 90 days, more than 150 files or 300 MiB in total"`
}

func (d Deps) registerCacheStatus(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "cache_status",
		Title:       "Cache status",
		Description: "Shows what the local on-disk price cache holds: its directory, file count and total size (bytes and MiB), and per cached symbol the bar count, first/last bar date, when it was last brought up to date and last fetched in full, file size and the last failed fetch (at most 200 symbols, sorted; symbol_count gives the total). warnings flags unreadable files, failed fetches or writes, files not fetched for 90 days and a cache over 150 files or 300 MiB. Use it when data looks stale or calls are slow, and before clear_cache or refresh_prices. Prices are reused for 6 hours by default and then topped up with recent days. Reads the local disk only, no network. Takes no arguments.",
		Annotations: readOnly("Cache status", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ cacheStatusInput) (*mcp.CallToolResult, cacheStatusOutput, error) {
		out, err := d.cacheStatus(ctx)
		return nil, out, err
	})
}

// cacheStatus maps the cache's own Status report.
func (d Deps) cacheStatus(ctx context.Context) (cacheStatusOutput, error) {
	if d.Cache == nil {
		return cacheStatusOutput{}, errCacheNotConfigured
	}
	st, err := d.Cache.Status(ctx)
	if err != nil {
		return cacheStatusOutput{}, err
	}
	out := cacheStatusOutput{
		Dir:         st.Dir,
		Files:       st.Files,
		FundFiles:   st.FundFiles,
		TotalBytes:  st.TotalBytes,
		TotalMB:     cacheMiB(st.TotalBytes),
		OldestFetch: cacheTimestamp(st.OldestFetch),
		NewestFetch: cacheTimestamp(st.NewestFetch),
		SymbolCount: len(st.Symbols),
		Symbols:     make([]cacheSymbolRow, 0, min(len(st.Symbols), maxCacheRows)),
		Warnings:    append([]string{}, st.Warnings...),
	}
	for i, ss := range st.Symbols {
		if i == maxCacheRows {
			out.Truncated = true
			break
		}
		out.Symbols = append(out.Symbols, toCacheSymbolRow(ss))
	}
	return out, nil
}

// toCacheSymbolRow renders one cache.SymbolStatus.
func toCacheSymbolRow(ss cache.SymbolStatus) cacheSymbolRow {
	row := cacheSymbolRow{
		Symbol:        ss.Symbol,
		Bars:          ss.Bars,
		FirstDate:     formatDate(ss.FirstDate),
		LastDate:      formatDate(ss.LastDate),
		FetchedAt:     cacheTimestamp(ss.FetchedAt),
		FullFetchedAt: cacheTimestamp(ss.FullFetchedAt),
		Bytes:         ss.Bytes,
	}
	if ss.LastError != "" {
		row.LastError = fetchPrefix.ReplaceAllString(ss.LastError, "")
	}
	return row
}

// cacheTimestamp renders an instant as RFC 3339 in UTC; zero is "".
func cacheTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// cacheMiB converts a byte count to MiB rounded to two decimals.
func cacheMiB(n int64) float64 {
	return round2(float64(n) / cacheBytesPerMiB)
}
