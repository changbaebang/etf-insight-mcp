package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// progressKey is the context key of a request's progress reporter.
type progressKey struct{}

// progress sends MCP progress notifications for one tools/call request.
// The spec requires progress to increase with every notification, so a
// tool that loads symbols in several batches adds each batch to the total
// instead of starting again from zero.
type progress struct {
	mu          sync.Mutex
	done, total int
	send        func(done, total int, msg string)
}

// add announces n more items of work.
func (p *progress) add(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total += n
}

// step reports one finished item, e.g. step("SPY", "loaded") sends
// "SPY loaded (3 of 126)". It holds the lock while sending, so
// notifications leave in the order of their progress values.
func (p *progress) step(item, verb string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done++
	p.send(p.done, p.total, fmt.Sprintf("%s %s (%d of %d)", item, verb, p.done, p.total))
}

// progressFrom returns the reporter attachProgress put in ctx, or nil when
// the client did not ask for progress; a nil reporter ignores every call.
func progressFrom(ctx context.Context) *progress {
	p, _ := ctx.Value(progressKey{}).(*progress)
	return p
}

// attachProgress is a receiving middleware: for a tools/call request that
// carries a progress token it puts a reporter in the context, so any tool
// that loads several symbols (through fetchAll) reports each one without
// handling the token itself.
func attachProgress(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok || call.Params == nil || call.Session == nil {
			return next(ctx, method, req)
		}
		token := call.Params.GetProgressToken()
		if token == nil {
			return next(ctx, method, req)
		}
		session := call.Session
		p := &progress{send: func(done, total int, msg string) {
			_ = session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      float64(done),
				Total:         float64(total),
				Message:       msg,
			})
		}}
		return next(context.WithValue(ctx, progressKey{}, p), method, req)
	}
}

// progressCallback adapts the request's reporter to a callback that is
// told the batch size on every call, as refresh_prices reports it.
func progressCallback(ctx context.Context, verb string) func(done, total int, item string) {
	p := progressFrom(ctx)
	if p == nil {
		return func(int, int, string) {}
	}
	var once sync.Once
	return func(_, total int, item string) {
		once.Do(func() { p.add(total) })
		p.step(item, verb)
	}
}
