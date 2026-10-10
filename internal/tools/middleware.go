package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// emptyArguments rewrites a tools/call request whose "arguments" is JSON
// null to an empty object.
//
// The go-sdk (v1.8.0) decodes a typed tool's arguments into a map and then
// writes the input schema's defaults into it. JSON null decodes to a nil
// map, so the first default assignment panics and takes the whole server
// down. Some clients send null for a tool without required inputs, and
// the MCP spec lets them omit arguments entirely, so null is treated as
// "no arguments".
func emptyArguments(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
			if bytes.Equal(bytes.TrimSpace(call.Params.Arguments), []byte("null")) {
				call.Params.Arguments = json.RawMessage("{}")
			}
		}
		return next(ctx, method, req)
	}
}

// recoverPanics turns a panic while one request is handled into a JSON-RPC
// internal error for that request and logs the stack to stderr. Without
// it, a bug in a single tool, or in the SDK, would end the session for
// every other call as well.
func recoverPanics(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("etf-insight-mcp: panic while handling %s: %v\n%s", method, p, debug.Stack())
				res = nil
				err = &jsonrpc.Error{
					Code:    jsonrpc.CodeInternalError,
					Message: fmt.Sprintf("internal error while handling %s; the server is still running", method),
				}
			}
		}()
		return next(ctx, method, req)
	}
}
