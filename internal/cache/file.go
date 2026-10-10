package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// formatVersion is the on-disk format of price files written by this
// package. A file without a "version" field is version 1.
const formatVersion = 2

// tempFileMaxAge is how old a leftover temporary file must be before the
// constructors sweep it or ClearAll removes it; younger ones may belong
// to a write in progress.
const tempFileMaxAge = 10 * time.Minute

// entry is one cached symbol as stored in <dir>/<SYMBOL>.json (format
// version 2). FetchedAt is when the file was last brought up to date, by a
// top-up or a full fetch; FullFetchedAt is when the whole history was last
// fetched. Bars, FirstDate and LastDate summarize the series so that
// Status can stop reading before it: writeEntry fills them and "series" is
// encoded last.
type entry struct {
	Version       int            `json:"version"`
	FetchedAt     time.Time      `json:"fetched_at"`
	FullFetchedAt time.Time      `json:"full_fetched_at"`
	Bars          int            `json:"bars"`
	FirstDate     time.Time      `json:"first_date"`
	LastDate      time.Time      `json:"last_date"`
	Series        *market.Series `json:"series"`
}

// entryV1 is the layout written before format version 2: Go field names
// and no version. It is still read so existing caches keep working; the
// next fetch rewrites the file as version 2.
type entryV1 struct {
	FetchedAt time.Time
	Series    *market.Series
}

// summarize fills Bars, FirstDate and LastDate from Series.
func (e *entry) summarize() {
	e.Bars, e.FirstDate, e.LastDate = summarize(e.Series)
}

// summarize returns the bar count and date range of s.
func summarize(s *market.Series) (bars int, first, last time.Time) {
	bars = s.Len()
	if b, ok := s.First(); ok {
		first = b.Date
	}
	if b, ok := s.Last(); ok {
		last = b.Date
	}
	return bars, first, last
}

// decodeEntry parses a price file of either format. The stored series
// must be valid; any problem is an error, which readers treat as a miss.
func decodeEntry(data []byte) (*entry, error) {
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("cache: decode header: %w", err)
	}
	var e entry
	switch probe.Version {
	case 0:
		var v1 entryV1
		if err := json.Unmarshal(data, &v1); err != nil {
			return nil, fmt.Errorf("cache: decode version 1 file: %w", err)
		}
		e = entry{Version: 1, FetchedAt: v1.FetchedAt, Series: v1.Series}
	case formatVersion:
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("cache: decode file: %w", err)
		}
	default:
		return nil, fmt.Errorf("cache: unsupported format version %d", probe.Version)
	}
	if err := e.Series.Validate(); err != nil {
		return nil, fmt.Errorf("cache: stored series: %w", err)
	}
	return &e, nil
}

// header is what Status needs from a price file, read without decoding
// the bars when the file is version 2.
type header struct {
	Version       int
	FetchedAt     time.Time
	FullFetchedAt time.Time
	Bars          int
	FirstDate     time.Time
	LastDate      time.Time
}

// readHeader reads the top-level fields of the price file at path. A
// version 2 file is read only up to its "series" key, plus its last few
// bytes to make sure it was not cut short; a version 1 file has no summary
// and is decoded in full.
func readHeader(path string) (header, error) {
	f, err := os.Open(path)
	if err != nil {
		return header{}, fmt.Errorf("cache: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var (
		h          header
		summarized bool
	)
	err = scanTopLevel(f, func(key string, dec *json.Decoder) error {
		switch key {
		case "version":
			return dec.Decode(&h.Version)
		case "fetched_at":
			return dec.Decode(&h.FetchedAt)
		case "full_fetched_at":
			return dec.Decode(&h.FullFetchedAt)
		case "bars":
			summarized = true
			return dec.Decode(&h.Bars)
		case "first_date":
			return dec.Decode(&h.FirstDate)
		case "last_date":
			return dec.Decode(&h.LastDate)
		case "series":
			if summarized {
				return errStopScan
			}
			return h.summarize(dec)
		case "FetchedAt": // version 1
			h.Version = 1
			return dec.Decode(&h.FetchedAt)
		case "Series": // version 1
			h.Version = 1
			return h.summarize(dec)
		default:
			return skipValue(dec)
		}
	})
	if err == nil && summarized {
		err = checkComplete(f)
	}
	return h, err
}

// checkComplete returns an error unless the price file f ends the way
// writeEntry leaves it: the series object closed, then the entry object,
// then a newline. "}}" appears nowhere else in a price file, so a file cut
// short (which readEntry would reject) fails this without the bars being
// read.
func checkComplete(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cache: stat %s: %w", f.Name(), err)
	}
	tail := make([]byte, min(info.Size(), 16))
	if _, err := f.ReadAt(tail, info.Size()-int64(len(tail))); err != nil {
		return fmt.Errorf("cache: read end of %s: %w", f.Name(), err)
	}
	if !bytes.HasSuffix(bytes.TrimRight(tail, " \t\r\n"), []byte("}}")) {
		return errors.New("cache: file is cut short")
	}
	return nil
}

// summarize decodes a series value and records its bar count and dates.
func (h *header) summarize(dec *json.Decoder) error {
	var s market.Series
	if err := dec.Decode(&s); err != nil {
		return err
	}
	h.Bars, h.FirstDate, h.LastDate = summarize(&s)
	return nil
}

// readFetchedAt reads the "fetched_at" field of the fund file at path
// without decoding its payload.
func readFetchedAt(path string) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("cache: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var at time.Time
	err = scanTopLevel(f, func(key string, dec *json.Decoder) error {
		switch key {
		case "fetched_at":
			return dec.Decode(&at)
		case "data":
			return errStopScan
		default:
			return skipValue(dec)
		}
	})
	return at, err
}

// errStopScan is returned by a scanTopLevel visitor to stop reading.
var errStopScan = errors.New("cache: stop scan")

// scanTopLevel walks the keys of the JSON object in r and calls visit for
// each with a decoder positioned on its value. visit must consume the
// value (decode it or skipValue it) or return errStopScan, after which
// nothing more is read. Cache files encode their large payload last, so a
// visitor that stops there reads only the header.
func scanTopLevel(r io.Reader, visit func(key string, dec *json.Decoder) error) error {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("cache: read object start: %w", err)
	}
	if tok != json.Delim('{') {
		return fmt.Errorf("cache: want a JSON object, got %v", tok)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("cache: read key: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("cache: want an object key, got %v", tok)
		}
		if err := visit(key, dec); err != nil {
			if errors.Is(err, errStopScan) {
				return nil
			}
			return fmt.Errorf("cache: read %q: %w", key, err)
		}
	}
	return nil
}

// skipValue consumes one JSON value without keeping it.
func skipValue(dec *json.Decoder) error {
	var raw json.RawMessage
	return dec.Decode(&raw)
}

// writeJSONAtomic encodes v into a temporary file in dir and renames it
// over <dir>/<name>, so a reader never sees a partial file and a failed
// write leaves nothing behind. dir is created when missing.
func writeJSONAtomic(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cache: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return fmt.Errorf("cache: create temp file: %w", err)
	}
	dst := filepath.Join(dir, name)
	if err := writeAndRename(tmp, dst, v); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// writeAndRename encodes v into tmp, closes it and moves it to dst.
func writeAndRename(tmp *os.File, dst string, v any) error {
	if err := json.NewEncoder(tmp).Encode(v); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cache: encode %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cache: close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("cache: rename to %s: %w", dst, err)
	}
	return nil
}

// sweepTempFiles deletes the temporary files writeJSONAtomic left in dir
// that are older than tempFileMaxAge. Other files, this package's or not,
// are never touched. Errors are ignored: the sweep is housekeeping, not a
// precondition.
func sweepTempFiles(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, d := range entries {
		if isLeftoverTemp(d, now) {
			_ = os.Remove(filepath.Join(dir, d.Name()))
		}
	}
}

// isLeftoverTemp reports whether d is a regular file that writeJSONAtomic
// created and that is older than tempFileMaxAge.
func isLeftoverTemp(d fs.DirEntry, now time.Time) bool {
	if !d.Type().IsRegular() || !isOwnTempFile(d.Name()) {
		return false
	}
	info, err := d.Info()
	return err == nil && now.Sub(info.ModTime()) > tempFileMaxAge
}

// isOwnTempFile reports whether name is a temporary file writeJSONAtomic
// creates: the name of a price or fund file, a dot, the random digits
// os.CreateTemp adds, and ".tmp" (SPY.json.123.tmp,
// SPY.profile.json.456.tmp).
func isOwnTempFile(name string) bool {
	stem, ok := strings.CutSuffix(name, ".tmp")
	if !ok {
		return false
	}
	i := strings.LastIndexByte(stem, '.')
	if i < 0 {
		return false
	}
	target, random := stem[:i], stem[i+1:]
	return isDigits(random) && (isPriceFile(target) || isFundFile(target))
}

// isDigits reports whether s is one or more ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cacheHeader matches the start of every file this package writes, as
// encoding/json lays it out: a version and a fetch time first (price
// files of version 2 and fund files), or the Go field name FetchedAt
// (price files of version 1).
var cacheHeader = regexp.MustCompile(`^\{("version":[0-9]+,"fetched_at"|"FetchedAt"):"`)

// hasCacheHeader reports whether the file at path starts like a file this
// package wrote. A name alone is not enough to delete a file: a cache
// directory may be shared with other programs' files such as 1.json.
func hasCacheHeader(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 64)
	n, _ := io.ReadFull(f, buf)
	return cacheHeader.Match(buf[:n])
}

// errTable remembers the most recent error per key. Its zero value is
// ready to use and it is safe for concurrent use.
type errTable struct {
	mu sync.Mutex
	m  map[string]error
}

// set remembers err for key, or forgets the key when err is nil.
func (t *errTable) set(key string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil {
		delete(t.m, key)
		return
	}
	if t.m == nil {
		t.m = make(map[string]error)
	}
	t.m[key] = err
}

// get returns the error remembered for key, or nil.
func (t *errTable) get(key string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[key]
}

// forget drops the given keys.
func (t *errTable) forget(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range keys {
		delete(t.m, key)
	}
}

// reset drops every key.
func (t *errTable) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m = nil
}

// keyedError is one entry of an errTable snapshot.
type keyedError struct {
	key string
	err error
}

// snapshot returns every remembered error sorted by key, read under one
// lock so that a key and its error always belong together.
func (t *errTable) snapshot() []keyedError {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]keyedError, 0, len(t.m))
	for _, key := range slices.Sorted(maps.Keys(t.m)) {
		out = append(out, keyedError{key: key, err: t.m[key]})
	}
	return out
}
