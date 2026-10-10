package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cacheFileSymbol is the symbol pattern the cache accepts as a file name
// (cache.ErrInvalidSymbol otherwise). clear_cache checks it up front so a
// preview never builds a path from a symbol the cache would reject.
var cacheFileSymbol = regexp.MustCompile(`^[A-Z0-9.=^-]+$`)

// cacheFundDocs are the fund documents cache.FundStore keeps per symbol,
// as <dir>/fund/<SYMBOL>.<doc>.json. Store.Clear removes them with the
// price file; Status only counts them in total, so a per-symbol preview
// looks them up directly.
var cacheFundDocs = []string{"profile", "holdings", "performance"}

type clearCacheInput struct {
	Symbols []string `json:"symbols,omitempty" jsonschema:"tickers whose cached price history and fund documents to delete, e.g. [\"SPY\", \"KRW=X\"], case-insensitive, at most 200; leave empty when all is true"`
	All     bool     `json:"all,omitempty" jsonschema:"delete every file the server cached, for all symbols (default false); use instead of symbols"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"must be true to delete anything (default false); without it the call fails with a preview of what would be removed"`
}

type clearCacheOutput struct {
	Removed      []string `json:"removed" jsonschema:"symbols whose cached files were deleted, sorted, at most 200 listed; with all=true these are the symbols that had price history (fund-only files are counted in files_removed)"`
	RemovedCount int      `json:"removed_count" jsonschema:"number of symbols whose cached files were deleted"`
	FilesRemoved int      `json:"files_removed" jsonschema:"price and fund files deleted"`
	FreedBytes   int64    `json:"freed_bytes"`
	FreedMB      float64  `json:"freed_mb" jsonschema:"freed_bytes in MiB, 2 decimals"`
	NotCached    []string `json:"not_cached,omitempty" jsonschema:"requested symbols that had no cached file"`
	Note         string   `json:"note"`
}

// clearPlan is what one clear_cache call deletes, measured before
// anything is removed.
type clearPlan struct {
	symbols []string // symbols with at least one file: sorted for all, request order otherwise
	missing []string // requested symbols without any file
	files   int
	bytes   int64
}

// registerClearCache adds clear_cache. Its input schema carries no
// defaults on purpose: with defaults, go-sdk v1.8.0 panics on a call whose
// arguments are JSON null, and every argument of this tool is optional.
func (d Deps) registerClearCache(s *mcp.Server) {
	destructive, openWorld := true, false
	mcp.AddTool(s, &mcp.Tool{
		Name:  "clear_cache",
		Title: "Clear local cache",
		Description: "Deletes cached price history and fund documents from the local disk, for the given symbols or, with all=true, for every symbol. Use it when a symbol's cached data looks wrong (e.g. after a split or a ticker change), or to free disk space; refresh_prices re-downloads without deleting first and is usually the better fix for stale data. " +
			"Requires confirm=true: a call without it deletes nothing and fails with a preview (how many files and bytes would go), so call once, read the preview, then repeat the same arguments with confirm=true. Returns the symbols removed (at most 200 listed), files_removed and freed_bytes/freed_mb. " +
			"The next call that needs a removed symbol fetches its full history again (one request per symbol). Quotes held in memory are not affected. Local disk only, no network.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Clear local cache",
			DestructiveHint: &destructive,
			IdempotentHint:  true,
			OpenWorldHint:   &openWorld,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clearCacheInput) (*mcp.CallToolResult, clearCacheOutput, error) {
		out, err := d.clearCache(ctx, in)
		return nil, out, err
	})
}

// clearCache previews or performs the deletion.
func (d Deps) clearCache(ctx context.Context, in clearCacheInput) (clearCacheOutput, error) {
	if d.Cache == nil {
		return clearCacheOutput{}, errCacheNotConfigured
	}
	syms, err := clearSymbols(in)
	if err != nil {
		return clearCacheOutput{}, err
	}
	st, err := d.Cache.Status(ctx)
	if err != nil {
		return clearCacheOutput{}, err
	}
	plan := planClear(st, syms, in.All)
	if plan.files == 0 {
		return clearCacheOutput{Removed: []string{}, NotCached: capCacheList(plan.missing), Note: "nothing to delete: " + nothingCachedFor(syms, in.All)}, nil
	}
	if !in.Confirm {
		return clearCacheOutput{}, fmt.Errorf("not deleted: clear_cache needs confirm=true. This call would delete %s; repeat it with the same arguments and confirm=true to proceed", plan.describe())
	}

	removed, files := plan.symbols, plan.files
	if in.All {
		n, err := d.Cache.ClearAll()
		if err != nil {
			return clearCacheOutput{}, fmt.Errorf("clearing the cache failed after %d files were removed: %w; cache_status shows what is left", n, err)
		}
		files = n
	} else {
		removed, err = d.Cache.Clear(syms)
		if err != nil {
			return clearCacheOutput{}, fmt.Errorf("clearing %s failed partway (%w); cache_status shows what is left", strings.Join(syms, ", "), err)
		}
	}
	if removed == nil {
		removed = []string{}
	}
	slices.Sort(removed)
	return clearCacheOutput{
		Removed:      capCacheList(removed),
		RemovedCount: len(removed),
		FilesRemoved: files,
		FreedBytes:   plan.bytes,
		FreedMB:      cacheMiB(plan.bytes),
		NotCached:    capCacheList(plan.missing),
		Note:         "deleted; the next call that needs one of these symbols fetches its full history again",
	}, nil
}

// clearSymbols validates the selection: symbols or all, not both and not
// neither, every symbol usable as a cache file name.
func clearSymbols(in clearCacheInput) ([]string, error) {
	syms := uniqueSymbols(in.Symbols)
	switch {
	case in.All && len(syms) > 0:
		return nil, errors.New("pass either symbols or all=true, not both")
	case !in.All && len(syms) == 0:
		return nil, errors.New(`nothing selected: pass symbols (e.g. ["SPY"]) or all=true; cache_status lists what is cached`)
	case len(syms) > maxCacheRows:
		return nil, fmt.Errorf("%d symbols given, at most %d per call; use all=true to clear everything", len(syms), maxCacheRows)
	}
	for _, sym := range syms {
		if !cacheFileSymbol.MatchString(sym) {
			return nil, fmt.Errorf("invalid symbol %q: use letters, digits and . - = ^ only; cache_status lists what is cached", sym)
		}
	}
	return syms, nil
}

// planClear measures what clearing syms (or everything, when all is set)
// removes, from the Status taken before deletion.
func planClear(st *cache.Status, syms []string, all bool) clearPlan {
	var plan clearPlan
	if all {
		for _, ss := range st.Symbols {
			plan.symbols = append(plan.symbols, ss.Symbol)
		}
		plan.files, plan.bytes = st.Files, st.TotalBytes
		return plan
	}
	price := make(map[string]int64, len(st.Symbols))
	for _, ss := range st.Symbols {
		price[ss.Symbol] = ss.Bytes
	}
	for _, sym := range syms {
		files, bytes := 0, int64(0)
		if b, ok := price[sym]; ok {
			files, bytes = 1, b
		}
		for _, doc := range cacheFundDocs {
			if b, ok := cacheFileSize(filepath.Join(st.Dir, "fund", sym+"."+doc+".json")); ok {
				files++
				bytes += b
			}
		}
		if files == 0 {
			plan.missing = append(plan.missing, sym)
			continue
		}
		plan.symbols = append(plan.symbols, sym)
		plan.files += files
		plan.bytes += bytes
	}
	return plan
}

// describe renders the plan for the confirmation error.
func (p clearPlan) describe() string {
	syms := p.symbols
	more := ""
	if len(syms) > 10 {
		more = fmt.Sprintf(" and %d more", len(syms)-10)
		syms = syms[:10]
	}
	msg := fmt.Sprintf("%d %s (%s) of %d %s: %s%s", p.files, cachePlural(p.files, "file", "files"), cacheHumanBytes(p.bytes), len(p.symbols), cachePlural(len(p.symbols), "symbol", "symbols"), strings.Join(syms, ", "), more)
	if len(p.missing) > 0 {
		msg += fmt.Sprintf("; not cached, nothing to delete: %s", strings.Join(capCacheList(p.missing), ", "))
	}
	return msg
}

// nothingCachedFor explains an empty plan.
func nothingCachedFor(syms []string, all bool) string {
	if all {
		return "the cache is empty"
	}
	return fmt.Sprintf("no cached files for %s", strings.Join(capCacheList(syms), ", "))
}

// cacheFileSize returns the size of path when it is a regular file
// (symlinks are not followed, as the cache never follows them either).
func cacheFileSize(path string) (int64, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Size(), true
}

// cacheHumanBytes renders n as bytes, KiB or MiB for a sentence.
func cacheHumanBytes(n int64) string {
	switch {
	case n >= cacheBytesPerMiB:
		return fmt.Sprintf("%.2f MiB", float64(n)/cacheBytesPerMiB)
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// cachePlural picks the singular or plural noun for n.
func cachePlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// capCacheList returns at most maxCacheRows entries of list.
func capCacheList(list []string) []string {
	if len(list) > maxCacheRows {
		return list[:maxCacheRows]
	}
	return list
}
