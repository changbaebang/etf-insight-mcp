package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPing(t *testing.T) {
	tests := []struct {
		name string
		in   pingInput
		want string
	}{
		{name: "default", in: pingInput{}, want: "pong"},
		{name: "echo", in: pingInput{Message: "hello"}, want: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, out, err := ping(context.Background(), nil, tt.in)
			if err != nil {
				t.Fatalf("ping returned error: %v", err)
			}
			if out.Reply != tt.want {
				t.Errorf("Reply = %q, want %q", out.Reply, tt.want)
			}
			if out.Version != version {
				t.Errorf("Version = %q, want %q", out.Version, version)
			}
		})
	}
}

// TestServerRoundTrip drives the real server through an in-memory transport,
// the same code path a Claude client takes over stdio.
func TestServerRoundTrip(t *testing.T) {
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()

	srv := newServer()
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
	if !ok || got["reply"] != "hi" {
		t.Errorf("structuredContent = %#v, want reply=hi", res.StructuredContent)
	}
}
