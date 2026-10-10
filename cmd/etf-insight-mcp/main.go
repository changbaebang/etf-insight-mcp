// Command etf-insight-mcp is an MCP server that simulates small recurring
// purchases of US ETFs and projects outcome ranges. It speaks MCP over
// stdin/stdout, so every diagnostic goes to stderr.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/tools"
	"github.com/changbaebang/etf-insight-mcp/internal/yahoo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set at build time by the Makefile and the release build
// (-ldflags "-X main.version=..."); "dev" means a plain go build.
var version = "dev"

// defaultCacheTTL is how long a cached symbol is reused before it is
// brought up to date.
const defaultCacheTTL = 6 * time.Hour

// defaultFullRefreshDays is how many days a cached history is extended
// with recent bars only before the whole history is fetched again.
const defaultFullRefreshDays = 30

// maxFullRefreshDays caps -full-refresh-days at a hundred years. The cap
// keeps fullRefreshInterval well inside time.Duration, which overflows
// past about 106,751 days and would silently turn a huge value into a
// short or negative interval.
const maxFullRefreshDays = 36500

// errReported marks a command-line error the flag package already
// printed together with the usage.
var errReported = errors.New("invalid command line")

// config is the parsed command line.
type config struct {
	cacheDir        string
	cacheTTL        time.Duration
	fullRefreshDays int
	clearCache      bool
	showVersion     bool
}

// fullRefreshInterval is fullRefreshDays as a duration.
func (c config) fullRefreshInterval() time.Duration {
	return time.Duration(c.fullRefreshDays) * 24 * time.Hour
}

// parseFlags parses the command line without the program name. The flag
// package writes usage and parse errors to stderr and parseFlags then
// returns errReported; -h returns flag.ErrHelp. Values that parse but make
// no sense are returned as plain errors for the caller to print.
func parseFlags(args []string, stderr io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet("etf-insight-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.cacheDir, "cache-dir", "", "directory for cached price history and fund data (default: $"+cache.EnvCacheDir+" or the user cache directory, e.g. ~/Library/Caches/etf-insight-mcp)")
	fs.DurationVar(&c.cacheTTL, "cache-ttl", defaultCacheTTL, "how long a cached symbol is reused before it is brought up to date")
	fs.IntVar(&c.fullRefreshDays, "full-refresh-days", defaultFullRefreshDays, "days a cached history is extended with recent bars only before the whole history is fetched again (1 to "+fmt.Sprint(maxFullRefreshDays)+")")
	fs.BoolVar(&c.clearCache, "clear-cache", false, "delete every file this server wrote in the cache directory, report what was removed on stderr and exit")
	fs.BoolVar(&c.showVersion, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return config{}, err
		}
		return config{}, errReported
	}
	switch {
	case fs.NArg() > 0:
		return config{}, fmt.Errorf("unexpected argument %q: every option is a flag, see -h", fs.Arg(0))
	case c.cacheTTL <= 0:
		return config{}, fmt.Errorf("-cache-ttl must be positive, got %s", c.cacheTTL)
	case c.fullRefreshDays < 1:
		return config{}, fmt.Errorf("-full-refresh-days must be at least 1, got %d", c.fullRefreshDays)
	case c.fullRefreshDays > maxFullRefreshDays:
		return config{}, fmt.Errorf("-full-refresh-days must be at most %d, got %d", maxFullRefreshDays, c.fullRefreshDays)
	}
	return c, nil
}

// realMain runs the command and returns the process exit code. stdout is
// the MCP transport while serving, so only -version writes to it.
func realMain(args []string, stdout, stderr io.Writer) int {
	c, err := parseFlags(args, stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errReported):
		return 2
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "etf-insight-mcp: %v\n", err)
		return 2
	case c.showVersion:
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	}

	dir, err := resolveCacheDir(c.cacheDir)
	if err == nil && c.clearCache {
		err = clearCache(c, dir, stderr)
	} else if err == nil {
		err = run(c, dir)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "etf-insight-mcp: %v\n", err)
		return 1
	}
	return 0
}

// resolveCacheDir returns dir, or cache.DefaultDir() when dir is empty.
func resolveCacheDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	return cache.DefaultDir()
}

// newServer builds the MCP server with every tool registered. Tests call
// it with a fake Source.
func newServer(deps tools.Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "etf-insight-mcp",
		Title:   "ETF Insight",
		Version: deps.Version,
	}, &mcp.ServerOptions{Instructions: tools.Instructions})
	tools.Register(s, deps)
	return s
}

// newStores puts one Yahoo Finance client behind the price cache and the
// fund cache. Both live in dir, so cache_status counts and clear_cache
// removes the fund files too.
func newStores(c config, dir string) (*cache.Store, *cache.FundStore) {
	yc := yahoo.New()
	store := cache.New(yc, dir, c.cacheTTL, cache.WithFullRefetchInterval(c.fullRefreshInterval()))
	return store, cache.NewFundStore(yc, dir)
}

// run serves over stdio until stdin closes or the process is interrupted.
func run(c config, dir string) error {
	if err := checkCacheDir(dir); err != nil {
		// Serving without a cache would refetch every symbol on every call
		// and label fresh data as uncacheable; better to stop and say so.
		return fmt.Errorf("cache directory %s is not usable: %w (pass -cache-dir or set $%s)", dir, err, cache.EnvCacheDir)
	}
	store, fund := newStores(c, dir)
	log.Printf("etf-insight-mcp %s: cache dir %s, ttl %s, full refresh every %d days", version, dir, c.cacheTTL, c.fullRefreshDays)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	deps := tools.Deps{Source: store, Cache: store, Fund: fund, Version: version, Now: time.Now}
	if err := newServer(deps).Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}

// clearCache removes every file the server wrote in dir (price files,
// fund files and leftover temporary files; anything else stays) and
// reports what went to w. The report compares the cache before and after,
// so when some files cannot be removed it counts only what went and lists
// what is still there.
func clearCache(c config, dir string, w io.Writer) error {
	store, _ := newStores(c, dir)
	ctx := context.Background()
	before, err := store.Status(ctx)
	if err != nil {
		return err
	}
	n, clearErr := store.ClearAll()
	after, statusErr := store.Status(ctx)
	if statusErr != nil {
		// Without a second look we know only how many files went.
		_, _ = fmt.Fprintf(w, "etf-insight-mcp: removed %d %s from %s\n", n, plural(n, "file", "files"), dir)
		return errors.Join(partwayError(dir, clearErr), statusErr)
	}

	_, _ = fmt.Fprintf(w, "etf-insight-mcp: removed %d %s (%d bytes) from %s\n", n, plural(n, "file", "files"), before.TotalBytes-after.TotalBytes, dir)
	kept := cachedSymbols(after)
	var removed []string
	for _, sym := range cachedSymbols(before) {
		if !slices.Contains(kept, sym) {
			removed = append(removed, sym)
		}
	}
	printFileGroups(w, "  ", removed, before.FundFiles-after.FundFiles)
	printFileGroups(w, "  still there: ", kept, after.FundFiles)
	return partwayError(dir, clearErr)
}

// cachedSymbols lists the symbols that have a price file in st.
func cachedSymbols(st *cache.Status) []string {
	syms := make([]string, 0, len(st.Symbols))
	for _, ss := range st.Symbols {
		syms = append(syms, ss.Symbol)
	}
	return syms
}

// printFileGroups writes one line for the price-history symbols and one
// for the fund document count, each with prefix, skipping empty groups.
func printFileGroups(w io.Writer, prefix string, symbols []string, fundFiles int) {
	if len(symbols) > 0 {
		_, _ = fmt.Fprintf(w, "%sprice history: %s\n", prefix, strings.Join(symbols, ", "))
	}
	if fundFiles > 0 {
		_, _ = fmt.Fprintf(w, "%sfund documents: %d %s\n", prefix, fundFiles, plural(fundFiles, "file", "files"))
	}
}

// partwayError wraps a ClearAll error with the directory, or returns nil.
func partwayError(dir string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("clearing %s failed partway: %w", dir, err)
}

// plural picks the singular or plural noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// checkCacheDir creates dir if needed and proves it is writable by
// creating and removing a probe file.
func checkCacheDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}

func main() {
	// stdout is the MCP transport; all diagnostics must go to stderr.
	log.SetOutput(os.Stderr)
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}
