package main

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
	"github.com/changbaebang/etf-insight-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeSource is the smallest market.Source: it knows one symbol with a
// few bars and reports every other symbol as not found.
type fakeSource struct{}

func (fakeSource) Series(_ context.Context, symbol string) (*market.Series, error) {
	if symbol != "SPY" {
		return nil, fmt.Errorf("fake: %s: %w", symbol, market.ErrNotFound)
	}
	s := &market.Series{Meta: market.Meta{Symbol: "SPY", Currency: "USD"}}
	day := time.Date(2024, time.January, 2, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		p := 470 + float64(i)
		s.Bars = append(s.Bars, market.Bar{Date: day.AddDate(0, 0, i), Close: p, AdjClose: p})
	}
	return s, nil
}

// TestServerRoundTrip drives the real server through an in-memory
// transport, the same code path a Claude client takes over stdio.
func TestServerRoundTrip(t *testing.T) {
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()

	srv := newServer(tools.Deps{Source: fakeSource{}, Version: "test"})
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ping",
		Arguments: map[string]any{"message": "hi"},
	})
	if err != nil {
		t.Fatalf("call ping: %v", err)
	}
	if res.IsError {
		t.Fatalf("ping returned tool error: %+v", res.Content)
	}
	got, ok := res.StructuredContent.(map[string]any)
	if !ok || got["reply"] != "hi" || got["version"] != "test" {
		t.Errorf("structuredContent = %#v, want reply=hi version=test", res.StructuredContent)
	}

	list, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"ping", "list_etfs", "get_etf_info", "get_price_history", "simulate_dca", "simulate_portfolio_dca", "forecast_dca"} {
		if !slices.Contains(names, want) {
			t.Errorf("tool %s is not listed; got %v", want, names)
		}
	}

	info, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_etf_info",
		Arguments: map[string]any{"symbol": "spy"},
	})
	if err != nil {
		t.Fatalf("call get_etf_info: %v", err)
	}
	if info.IsError {
		t.Fatalf("get_etf_info returned tool error: %+v", info.Content)
	}
}
