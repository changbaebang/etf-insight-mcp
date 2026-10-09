package universe

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// allowedCategories mirrors the category contract independently of the
// package's own table, so a typo in either place fails the build.
var allowedCategories = []string{
	"US Broad Market",
	"US Large Cap",
	"US Large Growth",
	"US Large Value",
	"US Mid Cap",
	"US Small Cap",
	"Dividend",
	"Sector",
	"Thematic",
	"Factor",
	"International Developed",
	"International ex-US",
	"Emerging Markets",
	"Global",
	"US Treasury",
	"US Aggregate Bond",
	"Corporate Bond",
	"High Yield Bond",
	"Municipal Bond",
	"International Bond",
	"Inflation Protected",
	"Cash / Ultra Short",
	"Commodity",
	"Covered Call",
	"Leveraged / Inverse",
}

// leveragedCategory is the one category whose funds carry Leveraged=true.
const leveragedCategory = "Leveraged / Inverse"

// validCSV is a small well-formed file used by the parser tests.
const validCSV = `# comment line
symbol,name,issuer,category,leveraged,note
VOO,Vanguard S&P 500 ETF,Vanguard,US Large Cap,false,Tracks the S&P 500
  TQQQ , ProShares UltraPro QQQ , ProShares , Leveraged / Inverse , true , Seeks 3x the Nasdaq-100
`

func TestAllRowCountInRange(t *testing.T) {
	const minRows, maxRows = 95, 140
	if n := len(All()); n < minRows || n > maxRows {
		t.Fatalf("len(All()) = %d, want between %d and %d", n, minRows, maxRows)
	}
}

func TestEveryRowSatisfiesDataInvariants(t *testing.T) {
	checks := []struct {
		name  string
		check func(e ETF) error
	}{
		{
			name: "symbol upper-case",
			check: func(e ETF) error {
				if e.Symbol != strings.ToUpper(e.Symbol) {
					return fmt.Errorf("symbol %q is not upper-case", e.Symbol)
				}
				return nil
			},
		},
		{
			name: "every field non-empty",
			check: func(e ETF) error {
				fields := map[string]string{
					"Symbol": e.Symbol, "Name": e.Name, "Issuer": e.Issuer,
					"Category": e.Category, "Note": e.Note,
				}
				for name, v := range fields {
					if strings.TrimSpace(v) == "" {
						return fmt.Errorf("%s: field %s is empty", e.Symbol, name)
					}
				}
				return nil
			},
		},
		{
			name: "category in allowed set",
			check: func(e ETF) error {
				if !slices.Contains(allowedCategories, e.Category) {
					return fmt.Errorf("%s: category %q not allowed", e.Symbol, e.Category)
				}
				return nil
			},
		},
		{
			name: "leveraged flag matches category",
			check: func(e ETF) error {
				if e.Leveraged != (e.Category == leveragedCategory) {
					return fmt.Errorf("%s: Leveraged=%v but Category=%q", e.Symbol, e.Leveraged, e.Category)
				}
				return nil
			},
		},
		{
			name: "note within length limit",
			check: func(e ETF) error {
				if len(e.Note) > maxNoteLen {
					return fmt.Errorf("%s: note is %d chars, max %d", e.Symbol, len(e.Note), maxNoteLen)
				}
				return nil
			},
		},
	}

	etfs := All()
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			for _, e := range etfs {
				if err := tc.check(e); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestSymbolsUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, s := range Symbols() {
		if seen[s] {
			t.Errorf("symbol %s appears more than once", s)
		}
		seen[s] = true
	}
}

func TestSymbolsMatchAllOrder(t *testing.T) {
	all := All()
	symbols := Symbols()
	if len(symbols) != len(all) {
		t.Fatalf("len(Symbols()) = %d, len(All()) = %d", len(symbols), len(all))
	}
	for i, e := range all {
		if symbols[i] != e.Symbol {
			t.Errorf("Symbols()[%d] = %s, All()[%d].Symbol = %s", i, symbols[i], i, e.Symbol)
		}
	}
}

func TestEveryAllowedCategoryIsUsed(t *testing.T) {
	got := Categories()
	for _, want := range allowedCategories {
		if !slices.Contains(got, want) {
			t.Errorf("category %q has no funds", want)
		}
	}
}

func TestContainsWellKnownSymbols(t *testing.T) {
	wellKnown := []string{
		"SPY", "IVV", "VOO", "VTI", "QQQ", "SCHD", "VIG", "VYM", "DGRO", "JEPI", "JEPQ",
		"VEA", "VXUS", "IEFA", "EFA", "VWO", "IEMG", "VT", "ACWI",
		"BND", "AGG", "BNDX", "TLT", "IEF", "SHY", "LQD", "HYG", "TIP", "MUB", "BIL", "SGOV",
		"GLD", "IAU", "SLV",
		"XLK", "XLF", "XLV", "XLE", "XLI", "XLY", "XLP", "XLU", "XLC", "XLRE", "SMH", "SOXX", "VNQ",
		"RSP", "SPYM", "QUAL", "USMV", "MTUM", "COWZ", "ARKK", "TQQQ", "SQQQ", "SOXL", "UPRO",
	}
	for _, s := range wellKnown {
		if _, ok := Get(s); !ok {
			t.Errorf("Get(%q) not found", s)
		}
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name       string
		symbol     string
		wantSymbol string
		wantOK     bool
	}{
		{name: "exact", symbol: "VOO", wantSymbol: "VOO", wantOK: true},
		{name: "lower-case", symbol: "voo", wantSymbol: "VOO", wantOK: true},
		{name: "mixed-case", symbol: "vOo", wantSymbol: "VOO", wantOK: true},
		{name: "surrounding spaces", symbol: "  voo\t", wantSymbol: "VOO", wantOK: true},
		{name: "leveraged is still found", symbol: "tqqq", wantSymbol: "TQQQ", wantOK: true},
		{name: "unknown", symbol: "NOPE", wantOK: false},
		{name: "empty", symbol: "", wantOK: false},
		{name: "spaces only", symbol: "   ", wantOK: false},
		{name: "inner space", symbol: "V OO", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Get(tc.symbol)
			if ok != tc.wantOK {
				t.Fatalf("Get(%q) ok = %v, want %v", tc.symbol, ok, tc.wantOK)
			}
			if !ok {
				if got != (ETF{}) {
					t.Errorf("Get(%q) returned %+v with ok=false, want zero ETF", tc.symbol, got)
				}
				return
			}
			if got.Symbol != tc.wantSymbol {
				t.Errorf("Get(%q).Symbol = %q, want %q", tc.symbol, got.Symbol, tc.wantSymbol)
			}
			if got.Name == "" || got.Issuer == "" || got.Category == "" || got.Note == "" {
				t.Errorf("Get(%q) returned incomplete ETF: %+v", tc.symbol, got)
			}
		})
	}
}

func TestFilter(t *testing.T) {
	all := All()
	nonLeveraged := 0
	for _, e := range all {
		if !e.Leveraged {
			nonLeveraged++
		}
	}

	tests := []struct {
		name        string
		q           Query
		wantLen     int            // -1 to skip the exact length check
		wantExact   []string       // non-nil: result symbols must equal this, in order
		wantContain []string       // symbols that must be present
		wantExclude []string       // symbols that must be absent
		wantEvery   func(ETF) bool // non-nil: every result must satisfy it
	}{
		{
			name:      "zero query returns every non-leveraged fund",
			q:         Query{},
			wantLen:   nonLeveraged,
			wantEvery: func(e ETF) bool { return !e.Leveraged },
		},
		{
			name:      "include leveraged returns the whole universe",
			q:         Query{IncludeLeveraged: true},
			wantLen:   len(all),
			wantExact: Symbols(),
		},
		{
			name:        "category exact",
			q:           Query{Category: "Dividend"},
			wantLen:     -1,
			wantContain: []string{"SCHD", "VIG", "VYM", "DGRO"},
			wantEvery:   func(e ETF) bool { return e.Category == "Dividend" },
		},
		{
			name:        "category ignores case and spaces",
			q:           Query{Category: "  dividend "},
			wantLen:     -1,
			wantContain: []string{"SCHD"},
			wantEvery:   func(e ETF) bool { return e.Category == "Dividend" },
		},
		{
			name:    "category unknown",
			q:       Query{Category: "Crypto"},
			wantLen: 0,
		},
		{
			name:    "leveraged category hidden by default",
			q:       Query{Category: leveragedCategory},
			wantLen: 0,
		},
		{
			name:        "leveraged category with flag",
			q:           Query{Category: leveragedCategory, IncludeLeveraged: true},
			wantLen:     -1,
			wantContain: []string{"TQQQ", "SQQQ", "SOXL", "UPRO"},
			wantEvery:   func(e ETF) bool { return e.Leveraged },
		},
		{
			name:        "issuer ignores case",
			q:           Query{Issuer: "vanguard"},
			wantLen:     -1,
			wantContain: []string{"VOO", "VTI", "BND"},
			wantEvery:   func(e ETF) bool { return e.Issuer == "Vanguard" },
		},
		{
			name:    "issuer unknown",
			q:       Query{Issuer: "Nobody"},
			wantLen: 0,
		},
		{
			name:        "text matches symbol substring",
			q:           Query{Text: "qqq"},
			wantLen:     -1,
			wantContain: []string{"QQQ", "QQQM"},
			wantExclude: []string{"TQQQ", "SQQQ"},
		},
		{
			name:        "text matches name substring ignoring case",
			q:           Query{Text: "GOLD"},
			wantLen:     -1,
			wantContain: []string{"GLD", "IAU"},
			wantEvery: func(e ETF) bool {
				return strings.Contains(strings.ToLower(e.Name), "gold") ||
					strings.Contains(strings.ToLower(e.Symbol), "gold")
			},
		},
		{
			name:        "text with leveraged included",
			q:           Query{Text: "qqq", IncludeLeveraged: true},
			wantLen:     -1,
			wantContain: []string{"QQQ", "TQQQ", "SQQQ"},
		},
		{
			name:    "text with no match",
			q:       Query{Text: "zzzz-not-a-fund"},
			wantLen: 0,
		},
		{
			name:      "category and issuer combined",
			q:         Query{Category: "US Large Cap", Issuer: "Vanguard"},
			wantLen:   2,
			wantExact: []string{"VOO", "VV"},
		},
		{
			name:      "all three fields combined",
			q:         Query{Category: "us treasury", Issuer: "ishares", Text: "20+"},
			wantLen:   1,
			wantExact: []string{"TLT"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Filter(tc.q)
			if got == nil {
				t.Fatal("Filter returned nil, want non-nil slice")
			}
			symbols := make([]string, len(got))
			for i, e := range got {
				symbols[i] = e.Symbol
			}
			if tc.wantLen >= 0 && len(got) != tc.wantLen {
				t.Errorf("len = %d, want %d (got %v)", len(got), tc.wantLen, symbols)
			}
			if tc.wantExact != nil && !slices.Equal(symbols, tc.wantExact) {
				t.Errorf("symbols = %v, want %v", symbols, tc.wantExact)
			}
			for _, s := range tc.wantContain {
				if !slices.Contains(symbols, s) {
					t.Errorf("result missing %s (got %v)", s, symbols)
				}
			}
			for _, s := range tc.wantExclude {
				if slices.Contains(symbols, s) {
					t.Errorf("result must not contain %s (got %v)", s, symbols)
				}
			}
			if tc.wantEvery != nil {
				for _, e := range got {
					if !tc.wantEvery(e) {
						t.Errorf("%s does not satisfy the predicate: %+v", e.Symbol, e)
					}
				}
			}
		})
	}
}

func TestFilterPreservesFileOrder(t *testing.T) {
	got := Filter(Query{IncludeLeveraged: true})
	all := All()
	if len(got) != len(all) {
		t.Fatalf("len = %d, want %d", len(got), len(all))
	}
	for i := range all {
		if got[i] != all[i] {
			t.Fatalf("index %d: Filter = %+v, All = %+v", i, got[i], all[i])
		}
	}
}

func TestCategoriesOrderAndUniqueness(t *testing.T) {
	var want []string
	for _, e := range All() {
		if !slices.Contains(want, e.Category) {
			want = append(want, e.Category)
		}
	}
	got := Categories()
	if !slices.Equal(got, want) {
		t.Errorf("Categories() = %v, want first-appearance order %v", got, want)
	}
	seen := make(map[string]bool)
	for _, c := range got {
		if seen[c] {
			t.Errorf("category %q repeated", c)
		}
		seen[c] = true
	}
	if len(got) != len(allowedCategories) {
		t.Errorf("len(Categories()) = %d, want %d", len(got), len(allowedCategories))
	}
}

func TestReturnedSlicesAreCopies(t *testing.T) {
	t.Run("All", func(t *testing.T) {
		first := All()
		original := first[0]
		first[0].Symbol = "MUTATED"
		first[0].Leveraged = !first[0].Leveraged
		if again := All(); again[0] != original {
			t.Errorf("All()[0] = %+v after mutation, want %+v", again[0], original)
		}
		if got, ok := Get(original.Symbol); !ok || got != original {
			t.Errorf("Get(%q) = %+v, %v after mutation, want %+v, true", original.Symbol, got, ok, original)
		}
	})
	t.Run("Categories", func(t *testing.T) {
		first := Categories()
		original := first[0]
		first[0] = "MUTATED"
		if again := Categories(); again[0] != original {
			t.Errorf("Categories()[0] = %q after mutation, want %q", again[0], original)
		}
	})
	t.Run("Symbols", func(t *testing.T) {
		first := Symbols()
		original := first[0]
		first[0] = "MUTATED"
		if again := Symbols(); again[0] != original {
			t.Errorf("Symbols()[0] = %q after mutation, want %q", again[0], original)
		}
	})
	t.Run("Filter", func(t *testing.T) {
		first := Filter(Query{})
		original := first[0]
		first[0].Name = "MUTATED"
		if again := Filter(Query{}); again[0] != original {
			t.Errorf("Filter()[0] = %+v after mutation, want %+v", again[0], original)
		}
	})
}

func TestConcurrentReadsAreSafe(t *testing.T) {
	const goroutines = 16
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if len(All()) == 0 {
				t.Error("All() returned nothing")
			}
			if _, ok := Get("VOO"); !ok {
				t.Error("Get(VOO) not found")
			}
			if i%2 == 0 {
				_ = Filter(Query{Text: "s&p"})
			} else {
				_ = Categories()
			}
		}()
	}
	wg.Wait()
}

func TestParseAcceptsCommentsAndWhitespace(t *testing.T) {
	got, err := parse([]byte(validCSV))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []ETF{
		{Symbol: "VOO", Name: "Vanguard S&P 500 ETF", Issuer: "Vanguard", Category: "US Large Cap", Note: "Tracks the S&P 500"},
		{Symbol: "TQQQ", Name: "ProShares UltraPro QQQ", Issuer: "ProShares", Category: "Leveraged / Inverse", Leveraged: true, Note: "Seeks 3x the Nasdaq-100"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parse = %+v, want %+v", got, want)
	}
}

func TestParseRejectsMalformedInput(t *testing.T) {
	const header = "symbol,name,issuer,category,leveraged,note\n"
	row := func(symbol, name, issuer, category, leveraged, note string) string {
		return strings.Join([]string{symbol, name, issuer, category, leveraged, note}, ",") + "\n"
	}
	good := row("VOO", "Vanguard S&P 500 ETF", "Vanguard", "US Large Cap", "false", "Tracks the S&P 500")

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "empty file", input: "", wantErr: "reading header"},
		{name: "comment only", input: "# nothing here\n", wantErr: "reading header"},
		{name: "wrong header", input: "ticker,name,issuer,category,leveraged,note\n" + good, wantErr: "header is"},
		{name: "header only", input: header, wantErr: "no ETF rows"},
		{name: "too few columns", input: header + "VOO,Vanguard S&P 500 ETF\n", wantErr: "reading record"},
		{name: "too many columns", input: header + good[:len(good)-1] + ",extra\n", wantErr: "reading record"},
		{name: "empty symbol", input: header + row("", "n", "i", "US Large Cap", "false", "x"), wantErr: "column symbol is empty"},
		{name: "blank name", input: header + row("VOO", "   ", "i", "US Large Cap", "false", "x"), wantErr: "column name is empty"},
		{name: "empty note", input: header + row("VOO", "n", "i", "US Large Cap", "false", ""), wantErr: "column note is empty"},
		{name: "lower-case symbol", input: header + row("voo", "n", "i", "US Large Cap", "false", "x"), wantErr: "upper-case"},
		{name: "symbol with punctuation", input: header + row("BRK.B", "n", "i", "US Large Cap", "false", "x"), wantErr: "upper-case"},
		{name: "bad leveraged value", input: header + row("VOO", "n", "i", "US Large Cap", "yes", "x"), wantErr: "want true or false"},
		{name: "leveraged in mixed case", input: header + row("VOO", "n", "i", "US Large Cap", "True", "x"), wantErr: "want true or false"},
		{name: "unknown category", input: header + row("VOO", "n", "i", "Crypto", "false", "x"), wantErr: "unknown category"},
		{name: "note too long", input: header + row("VOO", "n", "i", "US Large Cap", "false", strings.Repeat("x", maxNoteLen+1)), wantErr: "max 80"},
		{name: "duplicate symbol", input: header + good + good, wantErr: "duplicate symbol VOO"},
		{name: "error names the line", input: header + good + row("bad", "n", "i", "US Large Cap", "false", "x"), wantErr: "line 3:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parse([]byte(tc.input))
			if err == nil {
				t.Fatalf("parse returned %d rows and nil error, want error containing %q", len(got), tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
			if got != nil {
				t.Errorf("parse returned %v alongside an error, want nil", got)
			}
		})
	}
}

func TestParseWrapsCSVErrors(t *testing.T) {
	_, err := parse([]byte("symbol,name,issuer,category,leveraged,note\nVOO,only two\n"))
	if err == nil {
		t.Fatal("parse returned nil error for a short row")
	}
	var target interface{ Unwrap() error }
	if !errors.As(err, &target) {
		t.Errorf("error %q does not wrap the underlying csv error", err)
	}
}

func TestMustBuildPanicsWithClearMessage(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("mustBuild did not panic on malformed input")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is %T, want string", r)
		}
		for _, want := range []string{"universe:", "etfs.csv is malformed", "header is"} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic message %q does not contain %q", msg, want)
			}
		}
	}()
	mustBuild([]byte("ticker,name,issuer,category,leveraged,note\n"))
}

func TestBuildIndexesSymbolsAndCategories(t *testing.T) {
	d, err := build([]byte(validCSV))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got, want := d.categories, []string{"US Large Cap", "Leveraged / Inverse"}; !slices.Equal(got, want) {
		t.Errorf("categories = %v, want %v", got, want)
	}
	if i, ok := d.bySymbol["TQQQ"]; !ok || i != 1 {
		t.Errorf("bySymbol[TQQQ] = %d, %v, want 1, true", i, ok)
	}
	if _, err := build([]byte("")); err == nil {
		t.Error("build accepted an empty file")
	}
}

func TestValidSymbol(t *testing.T) {
	tests := []struct {
		symbol string
		want   bool
	}{
		{symbol: "VOO", want: true},
		{symbol: "QQQM", want: true},
		{symbol: "SPY1", want: true},
		{symbol: "", want: false},
		{symbol: "voo", want: false},
		{symbol: "BRK.B", want: false},
		{symbol: "V OO", want: false},
		{symbol: "VOO-", want: false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%q", tc.symbol), func(t *testing.T) {
			if got := validSymbol(tc.symbol); got != tc.want {
				t.Errorf("validSymbol(%q) = %v, want %v", tc.symbol, got, tc.want)
			}
		})
	}
}
