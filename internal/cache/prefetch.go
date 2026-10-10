package cache

import (
	"context"
	"sync"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// Prefetch fetches every symbol through this Store with at most
// concurrency fetches in flight (below 1 is treated as 1) and returns the
// series that succeeded together with the error of each symbol that
// failed. Symbols already fresh on disk are served from the file. Once
// ctx is done no new fetch starts and the remaining symbols are reported
// with ctx.Err(). Callers should use the returned series directly rather
// than calling Series again: a series that could not be written to disk
// would otherwise be fetched a second time.
func (s *Store) Prefetch(ctx context.Context, symbols []string, concurrency int) (map[string]*market.Series, map[string]error) {
	return s.PrefetchNotify(ctx, symbols, concurrency, nil)
}

// PrefetchNotify is Prefetch that also calls done, when it is not nil,
// once for every symbol whose fetch finished, with that fetch's error (nil
// on success), from the goroutine that fetched it. Callers use it to
// report progress on long batches.
func (s *Store) PrefetchNotify(ctx context.Context, symbols []string, concurrency int, done func(symbol string, err error)) (map[string]*market.Series, map[string]error) {
	concurrency = max(concurrency, 1)
	var (
		wg     sync.WaitGroup
		sem    = make(chan struct{}, concurrency)
		mu     sync.Mutex
		series = make(map[string]*market.Series, len(symbols))
		errs   = make(map[string]error)
	)
	for _, sym := range symbols {
		if err := ctx.Err(); err != nil {
			mu.Lock()
			errs[sym] = err
			mu.Unlock()
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			errs[sym] = ctx.Err()
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := s.Series(ctx, sym)
			mu.Lock()
			if err != nil {
				errs[sym] = err
			} else {
				series[sym] = got
			}
			mu.Unlock()
			if done != nil {
				done(sym, err)
			}
		}()
	}
	wg.Wait()
	return series, errs
}
