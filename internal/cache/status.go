package cache

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Thresholds past which Status adds a warning. They are generous for a
// cache of daily bars: thirty years of one symbol is about 2 MiB, so a
// cache that trips them holds far more than a few plans need.
const (
	// MaxFiles is the number of cache files (price and fund together)
	// above which Status warns that the cache should be cleared.
	MaxFiles = 150
	// MaxTotalBytes is the total size of the cache above which Status
	// warns.
	MaxTotalBytes int64 = 300 << 20
	// MaxFileBytes is the size of a single file above which Status warns;
	// a price file that large holds more than a daily history should.
	MaxFileBytes int64 = 10 << 20
	// MaxFileAge is how long a file may go without being fetched before
	// Status warns that it is probably no longer used.
	MaxFileAge = 90 * 24 * time.Hour
)

// limits holds the Status thresholds so tests can lower them.
type limits struct {
	files      int
	totalBytes int64
	fileBytes  int64
	fileAge    time.Duration
}

var defaultLimits = limits{files: MaxFiles, totalBytes: MaxTotalBytes, fileBytes: MaxFileBytes, fileAge: MaxFileAge}

// SymbolStatus describes one cached price file.
type SymbolStatus struct {
	Symbol string
	// Bars is the number of cached bars; FirstDate and LastDate bound them.
	Bars                int
	FirstDate, LastDate time.Time
	// FetchedAt is when the file was last brought up to date and
	// FullFetchedAt when the whole history was last fetched.
	FetchedAt, FullFetchedAt time.Time
	// Bytes is the file size.
	Bytes int64
	// Version is the on-disk format version; 0 when the file is unreadable.
	Version int
	// LastError is the most recent upstream failure for the symbol as
	// Store.LastError reports it, or "".
	LastError string
}

// Status describes the cache directory.
type Status struct {
	Dir string
	// Files counts price and fund files together; FundFiles is the fund
	// share of it. TotalBytes is their combined size.
	Files      int
	FundFiles  int
	TotalBytes int64
	// Symbols lists the price files in symbol order.
	Symbols []SymbolStatus
	// OldestFetch and NewestFetch bound the fetch times of every readable
	// file; zero when there is none.
	OldestFetch, NewestFetch time.Time
	// Warnings are human-readable problems: thresholds exceeded,
	// unreadable files and remembered fetch or write errors.
	Warnings []string
}

// Status reads the cache directory and reports what is in it. Price files
// are read only up to their header (and their last few bytes), so the
// cost is per file, not per bar. A missing directory is an empty cache,
// not an error. Files whose names this package never writes are ignored,
// and symlinks are never followed, a symlinked fund directory included.
func (s *Store) Status(ctx context.Context) (*Status, error) {
	st := &Status{Dir: s.dir}
	if err := s.scanPriceFiles(ctx, st); err != nil {
		return nil, err
	}
	slices.SortFunc(st.Symbols, func(a, b SymbolStatus) int { return cmp.Compare(a.Symbol, b.Symbol) })
	if err := s.scanFundFiles(ctx, st); err != nil {
		return nil, err
	}
	s.addErrorWarnings(st)
	s.addTotalWarnings(st)
	return st, nil
}

// scanPriceFiles adds every price file in the cache directory to st.
func (s *Store) scanPriceFiles(ctx context.Context, st *Status) error {
	return scanDir(ctx, s.dir, func(d fs.DirEntry, info fs.FileInfo) {
		sym, ok := priceSymbol(d.Name())
		if !ok {
			return
		}
		ss := SymbolStatus{Symbol: sym, Bytes: info.Size(), LastError: errString(s.lastErr.get(sym))}
		h, err := readHeader(filepath.Join(s.dir, d.Name()))
		if err != nil {
			st.Warnings = append(st.Warnings, fmt.Sprintf("%s: unreadable, will be refetched (%v)", d.Name(), err))
		} else {
			ss.Version, ss.Bars = h.Version, h.Bars
			ss.FirstDate, ss.LastDate = h.FirstDate, h.LastDate
			ss.FetchedAt, ss.FullFetchedAt = h.FetchedAt, h.FullFetchedAt
		}
		st.Symbols = append(st.Symbols, ss)
		s.countFile(st, d.Name(), info.Size(), ss.FetchedAt)
	})
}

// scanFundFiles adds every fund file under <dir>/fund to st. A fund
// directory that is not a real directory is reported, not followed.
func (s *Store) scanFundFiles(ctx context.Context, st *Status) error {
	dir, err := s.fundDir()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, errNotRealDir):
		st.Warnings = append(st.Warnings, fmt.Sprintf("%s: %v; its files are neither counted nor cleared", fundSubdir, err))
		return nil
	case err != nil:
		return err
	}
	return scanDir(ctx, dir, func(d fs.DirEntry, info fs.FileInfo) {
		if _, _, ok := fundFileParts(d.Name()); !ok {
			return
		}
		name := fundSubdir + "/" + d.Name()
		at, err := readFetchedAt(filepath.Join(dir, d.Name()))
		if err != nil {
			st.Warnings = append(st.Warnings, fmt.Sprintf("%s: unreadable, will be refetched (%v)", name, err))
		}
		st.FundFiles++
		s.countFile(st, name, info.Size(), at)
	})
}

// scanDir calls visit for every regular file in dir, stopping when ctx is
// done. A missing dir is empty, not an error; symlinks are skipped, never
// followed.
func scanDir(ctx context.Context, dir string, visit func(d fs.DirEntry, info fs.FileInfo)) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cache: read %s: %w", dir, err)
	}
	for _, d := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue // removed since ReadDir
		}
		visit(d, info)
	}
	return nil
}

// countFile adds one file to the totals and warns when it is too large or
// has not been fetched for too long. A zero fetchedAt (unreadable file)
// takes no part in the age bookkeeping.
func (s *Store) countFile(st *Status, name string, size int64, fetchedAt time.Time) {
	st.Files++
	st.TotalBytes += size
	if size > s.limits.fileBytes {
		st.Warnings = append(st.Warnings, fmt.Sprintf("%s is %s (more than %s)", name, fmtBytes(size), fmtBytes(s.limits.fileBytes)))
	}
	if fetchedAt.IsZero() {
		return
	}
	if st.OldestFetch.IsZero() || fetchedAt.Before(st.OldestFetch) {
		st.OldestFetch = fetchedAt
	}
	if fetchedAt.After(st.NewestFetch) {
		st.NewestFetch = fetchedAt
	}
	if age := s.now().Sub(fetchedAt); age > s.limits.fileAge {
		st.Warnings = append(st.Warnings, fmt.Sprintf("%s was last fetched %d days ago (more than %d)", name, days(age), days(s.limits.fileAge)))
	}
}

// addErrorWarnings reports every symbol whose last fetch or write failed.
func (s *Store) addErrorWarnings(st *Status) {
	for _, e := range s.lastErr.snapshot() {
		st.Warnings = append(st.Warnings, fmt.Sprintf("%s: last fetch failed: %v", e.key, e.err))
	}
	for _, e := range s.lastWriteErr.snapshot() {
		st.Warnings = append(st.Warnings, fmt.Sprintf("%s: last write failed: %v", e.key, e.err))
	}
}

// addTotalWarnings reports a cache with too many files or too many bytes.
func (s *Store) addTotalWarnings(st *Status) {
	if st.Files > s.limits.files {
		st.Warnings = append(st.Warnings, fmt.Sprintf("%d cache files (more than %d): clear symbols you no longer use", st.Files, s.limits.files))
	}
	if st.TotalBytes > s.limits.totalBytes {
		st.Warnings = append(st.Warnings, fmt.Sprintf("cache holds %s (more than %s)", fmtBytes(st.TotalBytes), fmtBytes(s.limits.totalBytes)))
	}
}

// Clear removes the cached files of the given symbols: the price history
// and the fund documents a FundStore keeps in the same directory. It
// returns the symbols for which at least one file was removed; a symbol
// without files is simply absent from the result. A symbol that is not
// safe as a file name makes Clear return an error wrapping
// ErrInvalidSymbol before anything is removed. Files are chosen by name:
// <SYMBOL>.json in the cache directory and <SYMBOL>.<kind>.json in its
// fund directory. Only regular files are touched; symlinks are left
// alone, never followed, and a fund directory that is itself a symlink is
// skipped. The remembered LastError and LastWriteError of each symbol are
// forgotten. A fetch already in flight for a cleared symbol still writes
// its result afterwards, and a FundStore's in-memory quotes are
// unaffected.
func (s *Store) Clear(symbols []string) ([]string, error) {
	syms, err := normalizeSymbols(symbols)
	if err != nil {
		return nil, err
	}
	var (
		removed []string
		errs    []error
	)
	for _, sym := range syms {
		n, err := removeFiles(s.symbolFiles(sym))
		if err != nil {
			errs = append(errs, err)
		}
		if n > 0 {
			removed = append(removed, sym)
		}
		s.lastErr.forget(sym)
		s.lastWriteErr.forget(sym)
		s.forgetUnwritten(sym)
	}
	return removed, errors.Join(errs...)
}

// ClearAll removes every file this package wrote under the cache
// directory: price and fund files, recognized by their name and by the
// header every cache file starts with, and temporary files of interrupted
// writes older than ten minutes (younger ones may belong to a write in
// progress; the next start sweeps them). Other files, subdirectories and
// symlinks are left alone, and a fund directory that is itself a symlink
// is skipped. It returns the number of files removed and forgets every
// remembered error.
func (s *Store) ClearAll() (int, error) {
	paths, err := s.ownFiles(s.dir, isPriceFile)
	errs := []error{err}
	dir, err := s.fundDir()
	switch {
	case err == nil:
		fund, err := s.ownFiles(dir, isFundFile)
		paths = append(paths, fund...)
		errs = append(errs, err)
	case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errNotRealDir):
		errs = append(errs, err)
	}
	removed, err := removeFiles(paths)
	errs = append(errs, err)
	s.lastErr.reset()
	s.lastWriteErr.reset()
	s.forgetUnwritten()
	return removed, errors.Join(errs...)
}

// errNotRealDir reports a fund directory that is a symlink or a file.
var errNotRealDir = errors.New("not a real directory (symlinks are not followed)")

// fundDir returns the fund subdirectory of the cache. The error wraps
// fs.ErrNotExist when it is missing and is errNotRealDir when it is
// anything but a real directory, a symlink to one included.
func (s *Store) fundDir() (string, error) {
	dir := filepath.Join(s.dir, fundSubdir)
	info, err := os.Lstat(dir)
	if err != nil {
		return dir, fmt.Errorf("cache: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return dir, errNotRealDir
	}
	return dir, nil
}

// symbolFiles lists every file Clear removes for sym. The fund documents
// are left out when the fund directory is not a real directory.
func (s *Store) symbolFiles(sym string) []string {
	paths := []string{s.path(sym)}
	if _, err := s.fundDir(); err != nil {
		return paths
	}
	for _, kind := range fundKinds {
		paths = append(paths, fundPath(s.dir, sym, kind))
	}
	return paths
}

// ownFiles returns the files in dir that this package wrote: regular
// files whose name isData accepts and whose content starts with a cache
// header, plus leftover temporary files (see isLeftoverTemp). A missing
// dir yields nothing.
func (s *Store) ownFiles(dir string, isData func(name string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache: read %s: %w", dir, err)
	}
	now := s.now()
	var paths []string
	for _, d := range entries {
		path := filepath.Join(dir, d.Name())
		data := d.Type().IsRegular() && isData(d.Name()) && hasCacheHeader(path)
		if data || isLeftoverTemp(d, now) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// removeFiles deletes the regular files among paths, skipping missing
// ones, directories and symlinks. It returns how many were removed and
// every failure joined.
func removeFiles(paths []string) (int, error) {
	var (
		removed int
		errs    []error
	)
	for _, p := range paths {
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("cache: stat %s: %w", p, err))
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(p); err != nil {
			errs = append(errs, fmt.Errorf("cache: %w", err)) // *fs.PathError already names the operation and path
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// priceSymbol returns the symbol a price file name belongs to, or false
// for a name this package did not write.
func priceSymbol(name string) (string, bool) {
	stem, ok := strings.CutSuffix(name, ".json")
	if !ok || !symbolPattern.MatchString(stem) {
		return "", false
	}
	return stem, true
}

// isPriceFile reports whether name is a price file.
func isPriceFile(name string) bool {
	_, ok := priceSymbol(name)
	return ok
}

// isFundFile reports whether name is a fund file.
func isFundFile(name string) bool {
	_, _, ok := fundFileParts(name)
	return ok
}

// errString renders err for a status field; nil is "".
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// days rounds d down to whole days.
func days(d time.Duration) int {
	return int(d / (24 * time.Hour))
}

// fmtBytes renders n in binary units with one decimal.
func fmtBytes(n int64) string {
	const unit = 1024
	switch f := float64(n); {
	case n >= unit*unit*unit:
		return fmt.Sprintf("%.1f GiB", f/(unit*unit*unit))
	case n >= unit*unit:
		return fmt.Sprintf("%.1f MiB", f/(unit*unit))
	case n >= unit:
		return fmt.Sprintf("%.1f KiB", f/unit)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
