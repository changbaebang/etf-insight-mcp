// Command etf-insight-mcp is an MCP server that simulates small daily
// purchases of US ETFs and analyses trend-based allocation rules.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridden at build time by goreleaser (-X main.version=...).
var version = "dev"

type pingInput struct {
	Message string `json:"message,omitempty" jsonschema:"optional text to echo back"`
}

type pingOutput struct {
	Reply   string `json:"reply"`
	Version string `json:"version"`
}

func ping(_ context.Context, _ *mcp.CallToolRequest, in pingInput) (*mcp.CallToolResult, pingOutput, error) {
	msg := in.Message
	if msg == "" {
		msg = "pong"
	}
	return nil, pingOutput{Reply: msg, Version: version}, nil
}

func newServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "etf-insight-mcp",
		Title:   "ETF Insight",
		Version: version,
	}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "ping",
		Description: "Health check. Echoes the message back with the server version.",
	}, ping)
	return s
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return newServer().Run(ctx, &mcp.StdioTransport{})
}

func main() {
	// stdout is the MCP transport; all diagnostics must go to stderr.
	log.SetOutput(os.Stderr)

	if err := run(); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
