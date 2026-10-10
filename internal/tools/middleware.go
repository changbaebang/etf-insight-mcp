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

// emptyArguments normalizes the "arguments" of a tools/call request before
// the SDK validates them against the input schema: JSON null becomes an
// empty object, and top-level properties whose value is null are dropped.
//
// The go-sdk (v1.8.0) decodes a typed tool's arguments into a map and then
// writes the input schema's defaults into it. JSON null decodes to a nil
// map, so the first default assignment panics and takes the whole server
// down. Some clients send null for a tool without required inputs, and
// the MCP spec lets them omit arguments entirely, so null is treated as
// "no arguments".
//
// Some clients also send null for every input they leave unset. The schema
// only allows null for pointer fields, so {"end": null} would fail with a
// type error even though it means "end not given". Dropping such keys
// gives every optional input its default; a required one is reported as
// missing.
func emptyArguments(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
			if bytes.Equal(bytes.TrimSpace(call.Params.Arguments), []byte("null")) {
				call.Params.Arguments = json.RawMessage("{}")
			} else {
				call.Params.Arguments = dropNullProperties(call.Params.Arguments)
			}
		}
		return next(ctx, method, req)
	}
}

// dropNullProperties removes the top-level keys of a JSON object whose
// value is null. Anything that is not an object, or has no null value, is
// returned unchanged so the SDK reports malformed input itself.
func dropNullProperties(args json.RawMessage) json.RawMessage {
	var props map[string]json.RawMessage
	if err := json.Unmarshal(args, &props); err != nil || props == nil {
		return args
	}
	dropped := false
	for key, value := range props {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(props, key)
			dropped = true
		}
	}
	if !dropped {
		return args
	}
	out, err := json.Marshal(props)
	if err != nil {
		return args
	}
	return out
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
