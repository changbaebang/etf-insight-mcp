package universe

import (
	"slices"
	"testing"
)

// filterSymbols returns the symbols Filter finds for text.
func filterSymbols(text string) []string {
	var out []string
	for _, e := range Filter(Query{Text: text}) {
		out = append(out, e.Symbol)
	}
	return out
}

func TestFix2TextMatchesEveryWordInSymbolNameIssuerOrNote(t *testing.T) {
	tests := []struct {
		text        string
		wantContain []string
		wantExclude []string
	}{
		// QQQ's name is "Invesco QQQ Trust"; the index is named in its note.
		{text: "nasdaq 100", wantContain: []string{"QQQ", "QQQM", "JEPQ", "QYLD"}},
		{text: "Nasdaq-100", wantContain: []string{"QQQ", "QQQM", "JEPQ", "QYLD"}},
		// The words are not adjacent in "Schwab U.S. Dividend Equity ETF".
		{text: "schwab dividend", wantContain: []string{"SCHD"}, wantExclude: []string{"VIG", "SCHB"}},
		// The issuer is searched too: SPDR funds are State Street's.
		{text: "state street gold", wantContain: []string{"GLD"}, wantExclude: []string{"IAU"}},
		{text: "S&P 500", wantContain: []string{"SPY", "VOO", "RSP"}, wantExclude: []string{"IJH", "QQQ"}},
	}
	for _, tc := range tests {
		got := filterSymbols(tc.text)
		for _, sym := range tc.wantContain {
			if !slices.Contains(got, sym) {
				t.Errorf("Filter(%q) = %v, want it to contain %s", tc.text, got, sym)
			}
		}
		for _, sym := range tc.wantExclude {
			if slices.Contains(got, sym) {
				t.Errorf("Filter(%q) = %v, want it without %s", tc.text, got, sym)
			}
		}
	}
}

func TestFix2CompletionAndAllWorldExUSFunds(t *testing.T) {
	if vxf, ok := Get("VXF"); !ok || vxf.Category != "US Mid Cap" {
		t.Errorf("VXF = %+v, want category US Mid Cap: it excludes the S&P 500 and is no total-market fund", vxf)
	}
	veu, ok := Get("VEU")
	if !ok || veu.Issuer != "Vanguard" || veu.Category != "International ex-US" || veu.Leveraged {
		t.Errorf("VEU = %+v (found %v), want Vanguard, International ex-US", veu, ok)
	}
}
