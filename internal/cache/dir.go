package cache

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvCacheDir is the environment variable that overrides the cache
// directory chosen by DefaultDir.
const EnvCacheDir = "ETF_INSIGHT_CACHE_DIR"

// DefaultDir returns the cache directory and creates it if needed: the
// value of $ETF_INSIGHT_CACHE_DIR when set, otherwise "etf-insight-mcp"
// under the user's cache directory (os.UserCacheDir).
func DefaultDir() (string, error) {
	dir := os.Getenv(EnvCacheDir)
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("cache: resolve user cache dir: %w", err)
		}
		dir = filepath.Join(base, "etf-insight-mcp")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cache: create %s: %w", dir, err)
	}
	return dir, nil
}
