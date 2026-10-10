package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestProgressIncreasesAcrossBatches: a tool that loads symbols in two
// batches must keep progress increasing, as the MCP spec requires.
func TestProgressIncreasesAcrossBatches(t *testing.T) {
	type sent struct{ done, total int }
	var got []sent
	p := &progress{send: func(done, total int, _ string) { got = append(got, sent{done, total}) }}
	p.add(2)
	p.step("A", "loaded")
	p.step("B", "loaded")
	p.add(3)
	p.step("C", "loaded")
	want := []sent{{1, 2}, {2, 2}, {3, 5}}
	if len(got) != len(want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("notification %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	var none *progress // no token: every call is a no-op
	none.add(1)
	none.step("X", "loaded")
}

// TestMultiSymbolToolsReportProgress drives compare_etfs with a progress
// token: every symbol it loads must produce one notification, with the
// progress value rising to the total.
func TestMultiSymbolToolsReportProgress(t *testing.T) {
	sess, notes := opsProgressSession(t, testDeps(newFakeSource()))
	params := &mcp.CallToolParams{Name: "compare_etfs", Arguments: map[string]any{"symbols": []string{"VOO", "SCHD"}}}
	params.SetProgressToken("cmp-1")
	res, err := sess.CallTool(context.Background(), params)
	if err != nil || res.IsError {
		t.Fatalf("call: %v %s", err, textOf(res))
	}
	// compare_etfs loads the two symbols plus the SPY benchmark.
	last := 0.0
	timeout := time.After(5 * time.Second)
	for seen := 0; seen < 3; seen++ {
		select {
		case n := <-notes:
			if n.ProgressToken != "cmp-1" || n.Total != 3 || n.Progress <= last {
				t.Errorf("notification %d = %+v, want token cmp-1, total 3, progress above %v", seen, n, last)
			}
			last = n.Progress
		case <-timeout:
			t.Fatalf("got %d progress notifications, want 3", seen)
		}
	}

	// Without a token nothing is sent.
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "compare_etfs", Arguments: map[string]any{"symbols": []string{"VOO", "SCHD"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notes:
		t.Errorf("unexpected notification without a token: %+v", n)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestProgressNamesFailures(t *testing.T) {
	var msgs []string
	p := &progress{send: func(_, _ int, msg string) { msgs = append(msgs, msg) }}
	p.add(2)
	p.step("SPY", outcome(nil))
	p.step("NOPE", outcome(errors.New("not found")))
	if len(msgs) != 2 || msgs[0] != "SPY loaded (1 of 2)" || msgs[1] != "NOPE failed (2 of 2)" {
		t.Errorf("messages = %q", msgs)
	}
}
