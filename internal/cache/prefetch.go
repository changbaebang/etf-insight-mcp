package cache

import (
	"context"
	"sync"

	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

// Prefetch warms src for every symbol with at most concurrency fetches in
// flight (a value below 1 is treated as 1). It returns the error for each
// symbol that failed; an empty map means every symbol succeeded. Once ctx
// is done no new fetch is started and each remaining symbol is reported
// with ctx.Err().
func Prefetch(ctx context.Context, src market.Source, symbols []string, concurrency int) map[string]error {
	concurrency = max(concurrency, 1)

	var (
		wg   sync.WaitGroup
		sem  = make(chan struct{}, concurrency) // one slot per allowed fetch
		mu   sync.Mutex
		errs = make(map[string]error)
	)
	record := func(sym string, err error) {
		mu.Lock()
		defer mu.Unlock()
		errs[sym] = err
	}

	for _, sym := range symbols {
		if err := ctx.Err(); err != nil {
			record(sym, err)
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			record(sym, ctx.Err())
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := src.Series(ctx, sym); err != nil {
				record(sym, err)
			}
		}()
	}
	wg.Wait()
	return errs
}
