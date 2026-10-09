package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pingInput struct {
	Message string `json:"message,omitempty" jsonschema:"optional text to echo back (default pong)"`
}

type pingOutput struct {
	Reply   string `json:"reply"`
	Version string `json:"version"`
}

func registerPing(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "ping",
		Title:       "Ping",
		Description: "Health check. Echoes the message back with the server version. Needs no network.",
		Annotations: readOnly("Ping", false),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in pingInput) (*mcp.CallToolResult, pingOutput, error) {
		return nil, d.ping(in), nil
	})
}

// ping answers the health check.
func (d Deps) ping(in pingInput) pingOutput {
	msg := in.Message
	if msg == "" {
		msg = "pong"
	}
	return pingOutput{Reply: msg, Version: d.Version}
}
