// Package universe holds the fixed list of ETFs the server knows about.
//
// The list lives in etfs.csv, is embedded into the binary at build time and
// is parsed once on first use. Because the file is a build artefact rather
// than user input, a malformed file is a programming error: the package
// panics with a descriptive message instead of returning an error.
//
// All exported functions are safe for concurrent use and return copies, so
// callers may modify the slices they get back without affecting the package.
package universe

import (
	"bytes"
	_ "embed" // enables the //go:embed directive on etfsCSV below
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

// etfsCSV is the raw embedded universe file. See etfs.csv for the column
// layout and the curation rules.
//
//go:embed etfs.csv
var etfsCSV []byte

// ETF is one fund in the universe.
type ETF struct {
	// Symbol is the upper-case ticker as listed on the exchange, e.g. "VOO".
	Symbol string
	// Name is the fund's full name, e.g. "Vanguard S&P 500 ETF".
	Name string
	// Issuer is the brand that sponsors the fund, e.g. "Vanguard" or "iShares".
	Issuer string
	// Category is one of the fixed category strings reported by Categories.
	Category string
	// Note is a short plain-English description of what the fund holds.
	Note string
	// Leveraged is true for leveraged and inverse funds. They reset daily and
	// are listed only so they can be analyzed, never as a recommendation.
	Leveraged bool
}

// Query narrows the universe. Every non-empty field must match for a fund to
// be included. The zero Query matches every non-leveraged fund.
type Query struct {
	// Category must equal the fund's Category, ignoring case.
	Category string
	// Issuer must equal the fund's Issuer, ignoring case.
	Issuer string
	// Text must appear in the fund's Symbol or Name, ignoring case.
	Text string
	// IncludeLeveraged keeps leveraged and inverse funds in the result. The
	// default (false) drops them even when Category names their category.
	IncludeLeveraged bool
}

// maxNoteLen caps the note column so tool output stays scannable.
const maxNoteLen = 80

// wantHeader is the exact column order etfs.csv must use.
var wantHeader = []string{"symbol", "name", "issuer", "category", "leveraged", "note"}

// knownCategories is the closed set of Category values etfs.csv may use. A
// new category is added here first so the data and the tool schema built on
// top of it cannot drift apart.
var knownCategories = map[string]bool{
	"US Broad Market":         true,
	"US Large Cap":            true,
	"US Large Growth":         true,
	"US Large Value":          true,
	"US Mid Cap":              true,
	"US Small Cap":            true,
	"Dividend":                true,
	"Sector":                  true,
	"Thematic":                true,
	"Factor":                  true,
	"International Developed": true,
	"Emerging Markets":        true,
	"Global":                  true,
	"US Treasury":             true,
	"US Aggregate Bond":       true,
	"Corporate Bond":          true,
	"High Yield Bond":         true,
	"Municipal Bond":          true,
	"International Bond":      true,
	"Inflation Protected":     true,
	"Cash / Ultra Short":      true,
	"Commodity":               true,
	"Covered Call":            true,
	"Leveraged / Inverse":     true,
}

// data is the parsed universe. It is built once by load and never mutated
// afterwards, so reads need no locking.
type data struct {
	etfs       []ETF
	bySymbol   map[string]int // upper-case symbol -> index into etfs
	categories []string       // distinct, in order of first appearance
}

var (
	loadOnce sync.Once
	loaded   *data
)

// load parses the embedded file on first call and returns the shared,
// read-only result afterwards.
func load() *data {
	loadOnce.Do(func() {
		loaded = mustBuild(etfsCSV)
	})
	return loaded
}

// mustBuild wraps build in a panic so a broken embedded file fails at the
// first use with a clear message instead of yielding an empty universe.
func mustBuild(raw []byte) *data {
	d, err := build(raw)
	if err != nil {
		panic(fmt.Sprintf("universe: embedded etfs.csv is malformed: %v", err))
	}
	return d
}

// build parses raw CSV bytes and indexes the result for lookups.
func build(raw []byte) (*data, error) {
	etfs, err := parse(raw)
	if err != nil {
		return nil, err
	}
	d := &data{
		etfs:     etfs,
		bySymbol: make(map[string]int, len(etfs)),
	}
	for i, e := range etfs {
		d.bySymbol[e.Symbol] = i
		if !slices.Contains(d.categories, e.Category) {
			d.categories = append(d.categories, e.Category)
		}
	}
	return d, nil
}

// parse reads the CSV, skipping # comment lines, and validates every row.
func parse(raw []byte) ([]ETF, error) {
	r := csv.NewReader(bytes.NewReader(raw))
	r.Comment = '#'
	r.FieldsPerRecord = len(wantHeader)
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	if !slices.Equal(header, wantHeader) {
		return nil, fmt.Errorf("header is %q, want %q", header, wantHeader)
	}

	var etfs []ETF
	seen := make(map[string]bool)
	for {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading record: %w", err)
		}
		line, _ := r.FieldPos(0)
		e, err := parseRecord(record)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if seen[e.Symbol] {
			return nil, fmt.Errorf("line %d: duplicate symbol %s", line, e.Symbol)
		}
		seen[e.Symbol] = true
		etfs = append(etfs, e)
	}
	if len(etfs) == 0 {
		return nil, errors.New("no ETF rows after the header")
	}
	return etfs, nil
}

// parseRecord converts one CSV record into an ETF, trimming each field and
// rejecting anything that would make the universe inconsistent.
func parseRecord(record []string) (ETF, error) {
	for i, field := range record {
		record[i] = strings.TrimSpace(field)
		if record[i] == "" {
			return ETF{}, fmt.Errorf("column %s is empty", wantHeader[i])
		}
	}

	leveraged, err := parseBool(record[4])
	if err != nil {
		return ETF{}, err
	}
	e := ETF{
		Symbol:    record[0],
		Name:      record[1],
		Issuer:    record[2],
		Category:  record[3],
		Leveraged: leveraged,
		Note:      record[5],
	}
	if !validSymbol(e.Symbol) {
		return ETF{}, fmt.Errorf("symbol %q must be upper-case letters or digits only", e.Symbol)
	}
	if !knownCategories[e.Category] {
		return ETF{}, fmt.Errorf("symbol %s has unknown category %q", e.Symbol, e.Category)
	}
	if len(e.Note) > maxNoteLen {
		return ETF{}, fmt.Errorf("symbol %s note is %d characters, max %d", e.Symbol, len(e.Note), maxNoteLen)
	}
	return e, nil
}

// parseBool accepts exactly "true" or "false". strconv.ParseBool is looser
// than we want for a hand-maintained file.
func parseBool(s string) (bool, error) {
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("leveraged is %q, want true or false", s)
}

// validSymbol reports whether s is a plausible exchange ticker: one or more
// upper-case ASCII letters or digits.
func validSymbol(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if !isUpper && !isDigit {
			return false
		}
	}
	return true
}

// normalizeSymbol maps user input onto the canonical upper-case form used as
// the lookup key.
func normalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

// All returns every ETF in file order. The slice is a copy.
func All() []ETF {
	return slices.Clone(load().etfs)
}

// Get looks up one ETF by symbol, ignoring case and surrounding whitespace.
// ok is false when the symbol is not in the universe.
func Get(symbol string) (ETF, bool) {
	d := load()
	i, ok := d.bySymbol[normalizeSymbol(symbol)]
	if !ok {
		return ETF{}, false
	}
	return d.etfs[i], true
}

// Filter returns the ETFs matching q in file order. The result is never nil,
// so an empty match encodes as a JSON array rather than null.
func Filter(q Query) []ETF {
	category := strings.TrimSpace(q.Category)
	issuer := strings.TrimSpace(q.Issuer)
	text := strings.ToLower(strings.TrimSpace(q.Text))

	out := []ETF{}
	for _, e := range load().etfs {
		if e.Leveraged && !q.IncludeLeveraged {
			continue
		}
		if category != "" && !strings.EqualFold(e.Category, category) {
			continue
		}
		if issuer != "" && !strings.EqualFold(e.Issuer, issuer) {
			continue
		}
		if text != "" && !matchesText(e, text) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// matchesText reports whether the lower-cased needle appears in the fund's
// symbol or name.
func matchesText(e ETF, needle string) bool {
	return strings.Contains(strings.ToLower(e.Symbol), needle) ||
		strings.Contains(strings.ToLower(e.Name), needle)
}

// Categories returns the distinct categories in order of first appearance in
// the file. The slice is a copy.
func Categories() []string {
	return slices.Clone(load().categories)
}

// Symbols returns every symbol in file order.
func Symbols() []string {
	etfs := load().etfs
	out := make([]string, len(etfs))
	for i, e := range etfs {
		out[i] = e.Symbol
	}
	return out
}
