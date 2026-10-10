package tools

import "testing"

func TestGetSplits(t *testing.T) {
	sess, _, _ := newDataSession(t)

	t.Run("forward splits oldest first", func(t *testing.T) {
		var out getSplitsOutput
		callOK(t, sess, "get_splits", map[string]any{"symbol": "tqqq"}, &out)
		if out.Symbol != "TQQQ" || out.Count != 2 || len(out.Splits) != 2 || out.HistoryFrom != "2021-01-04" {
			t.Fatalf("out = %+v", out)
		}
		want := []splitRow{
			{Date: "2021-01-21", Ratio: "2:1", Numerator: 2, Denominator: 1},
			{Date: "2022-01-13", Ratio: "2:1", Numerator: 2, Denominator: 1},
		}
		for i := range want {
			if out.Splits[i] != want[i] {
				t.Errorf("split %d = %+v, want %+v", i, out.Splits[i], want[i])
			}
		}
		if !hasDataNote(out.Notes, "already split-adjusted") {
			t.Errorf("notes = %v, want the adjustment note", out.Notes)
		}
	})

	t.Run("reverse split", func(t *testing.T) {
		var out getSplitsOutput
		callOK(t, sess, "get_splits", map[string]any{"symbol": "UVXY"}, &out)
		if out.Count != 1 || out.Splits[0].Ratio != "1:10" || !out.Splits[0].Reverse {
			t.Errorf("splits = %+v, want one 1:10 reverse split", out.Splits)
		}
	})

	t.Run("no splits", func(t *testing.T) {
		var out getSplitsOutput
		callOK(t, sess, "get_splits", map[string]any{"symbol": "SPY"}, &out)
		if out.Count != 0 || out.Splits == nil || !hasDataNote(out.Notes, "no split recorded between 2021-01-04 and") {
			t.Errorf("out = %+v, want an empty list and a note", out)
		}
	})

	t.Run("unknown symbol", func(t *testing.T) {
		callErr(t, sess, "get_splits", map[string]any{"symbol": "NOPE"}, "unknown symbol NOPE")
	})
}
