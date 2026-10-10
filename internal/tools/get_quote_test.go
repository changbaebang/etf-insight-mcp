package tools

import (
	"fmt"
	"testing"
)

func TestGetQuote(t *testing.T) {
	sess, _, fund := newDataSession(t)

	t.Run("request order and missing symbols", func(t *testing.T) {
		var out getQuoteOutput
		callOK(t, sess, "get_quote", map[string]any{"symbols": []string{"schd", " voo", "NOPE", "VOO"}}, &out)
		if out.Count != 2 || len(out.Quotes) != 2 || out.Quotes[0].Symbol != "SCHD" || out.Quotes[1].Symbol != "VOO" {
			t.Fatalf("quotes = %+v, want SCHD then VOO", out.Quotes)
		}
		if len(out.Missing) != 1 || out.Missing[0] != "NOPE" || len(out.Notes) != 1 {
			t.Errorf("missing/notes = %v/%v, want [NOPE] with a hint", out.Missing, out.Notes)
		}
		want := quoteRow{
			Symbol: "VOO", Name: "Vanguard S&P 500 ETF", Price: 435.12, Change: 1.23, ChangePct: 0.28, PreviousClose: 433.89,
			Open: 434, DayLow: 432.5, DayHigh: 436.25, Volume: 4567890, FiftyTwoWeekLow: 340.1, FiftyTwoWeekHigh: 440.9,
			FiftyDayAverage: 420.56, TwoHundredDayAverage: 400.44, DividendYieldPct: 1.36, MarketState: "REGULAR",
			Currency: "USD", Exchange: "NYSEArca", AsOf: "2024-01-02T21:00:00Z",
		}
		if out.Quotes[1] != want {
			t.Errorf("VOO = %+v\nwant %+v", out.Quotes[1], want)
		}
	})

	t.Run("one call for the batch", func(t *testing.T) {
		before := fund.quoteCalls
		var out getQuoteOutput
		callOK(t, sess, "get_quote", map[string]any{"symbols": []string{"SPY", "QQQ", "^vix"}}, &out)
		if fund.quoteCalls != before+1 || out.Count != 3 || len(out.Missing) != 0 || out.Missing == nil {
			t.Errorf("calls %d -> %d, count %d, missing %v; want one call, 3 quotes, empty missing", before, fund.quoteCalls, out.Count, out.Missing)
		}
	})

	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("S%02d", i)
	}
	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "all unknown", args: map[string]any{"symbols": []string{"NOPE", "XYZ"}}, want: "no quote found for NOPE, XYZ"},
		{name: "too many", args: map[string]any{"symbols": tooMany}, want: "51 distinct tickers, at most 50"},
		{name: "empty list", args: map[string]any{"symbols": []string{}}, want: "symbols"},
		{name: "blank symbols", args: map[string]any{"symbols": []string{" ", ""}}, want: "symbols is required"},
		{name: "missing", args: map[string]any{}, want: "symbols"},
		{name: "upstream failure", args: map[string]any{"symbols": []string{"BOOM"}}, want: "fetching quotes failed: connection reset by peer"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_quote", tt.args, tt.want)
		})
	}
}
