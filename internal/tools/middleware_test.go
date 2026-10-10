package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNullArgumentsDoNotCrash sends "arguments": null to every tool. Before
// emptyArguments, the SDK panicked on any tool whose schema has defaults
// and the whole server died; now every call must come back as a result
// (a tool error is fine) and the session must keep working.
func TestNullArgumentsDoNotCrash(t *testing.T) {
	sess := newSession(t, testDeps(newFakeSource()))
	ctx := context.Background()
	for _, name := range toolNames {
		t.Run(name, func(t *testing.T) {
			_, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: json.RawMessage("null")})
			if err != nil {
				t.Fatalf("call %s with null arguments: protocol error %v", name, err)
			}
		})
	}
	var out pingOutput
	callOK(t, sess, "ping", map[string]any{"message": "still here"}, &out)
	if out.Reply != "still here" {
		t.Errorf("ping after null calls = %q", out.Reply)
	}
}

func TestEmptyArguments(t *testing.T) {
	var seen json.RawMessage
	next := func(_ context.Context, _ string, req mcp.Request) (mcp.Result, error) {
		seen = req.(*mcp.CallToolRequest).Params.Arguments
		return &mcp.CallToolResult{}, nil
	}
	for _, tc := range []struct{ in, want string }{
		{"null", "{}"},
		{" null\n", "{}"},
		{`{"a":1}`, `{"a":1}`},
		{"", ""},
	} {
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "x", Arguments: json.RawMessage(tc.in)}}
		if _, err := emptyArguments(next)(context.Background(), "tools/call", req); err != nil {
			t.Fatal(err)
		}
		if string(seen) != tc.want {
			t.Errorf("arguments %q became %q, want %q", tc.in, seen, tc.want)
		}
	}
	// Other requests pass through untouched.
	if _, err := emptyArguments(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.ListToolsResult{}, nil
	})(context.Background(), "tools/list", &mcp.ListToolsRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverPanics(t *testing.T) {
	panicking := func(context.Context, string, mcp.Request) (mcp.Result, error) { panic("boom") }
	res, err := recoverPanics(panicking)(context.Background(), "tools/call", nil)
	if res != nil {
		t.Errorf("result = %v, want nil", res)
	}
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInternalError {
		t.Fatalf("error = %v, want a JSON-RPC internal error", err)
	}

	ok := func(context.Context, string, mcp.Request) (mcp.Result, error) { return &mcp.CallToolResult{}, nil }
	if res, err := recoverPanics(ok)(context.Background(), "tools/call", nil); err != nil || res == nil {
		t.Errorf("non-panicking handler: res=%v err=%v", res, err)
	}
}
