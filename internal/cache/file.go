package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// formatVersion is the on-disk format of price files written by this
// package. A file without a "version" field is version 1.
const formatVersion = 2

// tempFileMaxAge is how old a leftover *.tmp file must be before the
// constructors sweep it; younger ones may belong to a write in progress.
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
// version 2 file is read only up to its "series" key; a version 1 file
// has no summary and is decoded in full.
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
	return h, err
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

// sweepTempFiles deletes *.tmp files in dir older than tempFileMaxAge.
// Errors are ignored: the sweep is housekeeping, not a precondition.
func sweepTempFiles(dir string, now time.Time) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		return
	}
	for _, path := range matches {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) > tempFileMaxAge {
			_ = os.Remove(path)
		}
	}
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

// keys returns the remembered keys in sorted order.
func (t *errTable) keys() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Sorted(maps.Keys(t.m))
}
