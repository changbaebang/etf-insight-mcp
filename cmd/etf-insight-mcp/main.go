// Command etf-insight-mcp is an MCP server that simulates small recurring
// purchases of US ETFs and projects outcome ranges. It speaks MCP over
// stdin/stdout, so every diagnostic goes to stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/cache"
	"github.com/changbaebang/etf-insight-mcp/internal/tools"
	"github.com/changbaebang/etf-insight-mcp/internal/yahoo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridden at build time by goreleaser (-X main.version=...).
var version = "dev"

// defaultCacheTTL is how long a cached symbol is reused before refetching.
const defaultCacheTTL = 6 * time.Hour

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

// run wires Yahoo Finance behind the on-disk cache and serves over stdio
// until stdin closes or the process is interrupted. An empty cacheDir
// means cache.DefaultDir().
func run(cacheDir string, ttl time.Duration) error {
	if cacheDir == "" {
		dir, err := cache.DefaultDir()
		if err != nil {
			return err
		}
		cacheDir = dir
	}
	store := cache.New(yahoo.New(), cacheDir, ttl)
	log.Printf("etf-insight-mcp %s: cache dir %s, ttl %s", version, cacheDir, ttl)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	deps := tools.Deps{Source: store, Cache: store, Version: version, Now: time.Now}
	return newServer(deps).Run(ctx, &mcp.StdioTransport{})
}

func main() {
	// stdout is the MCP transport; all diagnostics must go to stderr.
	log.SetOutput(os.Stderr)

	cacheDir := flag.String("cache-dir", "", "directory for cached price history (default: $"+cache.EnvCacheDir+" or the user cache directory, e.g. ~/Library/Caches/etf-insight-mcp)")
	cacheTTL := flag.Duration("cache-ttl", defaultCacheTTL, "how long a cached symbol is reused before it is refetched")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*cacheDir, *cacheTTL); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
