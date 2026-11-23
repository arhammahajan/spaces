package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	// LimitsCacheVersion is the on-disk schema version for usage-limits-cache.json.
	LimitsCacheVersion = 1

	limitsCacheEnvVar = "TMUX_SPACES_USAGE_LIMITS_CACHE"
)

// DefaultLimitsCachePath resolves the persistent usage-limits cache path.
// It checks TMUX_SPACES_USAGE_LIMITS_CACHE first, otherwise stores the cache
// directly in <realHome>/.harness/usage-limits-cache.json.
func DefaultLimitsCachePath(homeDir string) string {
	if override := strings.TrimSpace(os.Getenv(limitsCacheEnvVar)); override != "" {
		return override
	}
	base := agent.RealHomeDir(homeDir)
	if base == "" {
		return ""
	}
	return filepath.Join(base, ".harness", "usage-limits-cache.json")
}

// LoadLimitsCache reads and validates the versioned JSON LimitsSnapshot at path.
func LoadLimitsCache(path string) (LimitsSnapshot, bool) {
	p := strings.TrimSpace(path)
	if p == "" {
		return LimitsSnapshot{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return LimitsSnapshot{}, false
	}
	var snap LimitsSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return LimitsSnapshot{}, false
	}
	if snap.Version != LimitsCacheVersion {
		return LimitsSnapshot{}, false
	}
	return snap, true
}

// SaveLimitsCache atomically writes snap to path with 0600 permissions,
// creating parent directories (such as ~/.harness) if needed.
func SaveLimitsCache(path string, snap LimitsSnapshot) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return fmt.Errorf("limits cache path is empty")
	}
	snap.Version = LimitsCacheVersion

	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create limits cache dir: %w", err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal limits cache: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".usage-limits-cache-*.tmp")
	if err != nil {
		return fmt.Errorf("create limits cache temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod limits cache temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write limits cache temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync limits cache temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close limits cache temp file: %w", err)
	}
	if err := os.Rename(tmpPath, p); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename limits cache file: %w", err)
	}
	return nil
}
