package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const storeSchemaVersion = 1

type storePayload struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

// Store persists managed worktree metadata records atomically as JSON.
type Store struct {
	Path string
	Now  func() time.Time

	mu sync.Mutex
}

// DefaultStorePath resolves the JSON store path:
// $TMUX_SPACES_WORKTREE_STATE, else $TMUX_SPACES_DATA_DIR/worktrees.json,
// else ${XDG_DATA_HOME:-~/.local/share}/tmux-spaces/worktrees.json.
func DefaultStorePath() string {
	if p := strings.TrimSpace(os.Getenv("TMUX_SPACES_WORKTREE_STATE")); p != "" {
		return filepath.Clean(p)
	}
	if d := strings.TrimSpace(os.Getenv("TMUX_SPACES_DATA_DIR")); d != "" {
		return filepath.Join(filepath.Clean(d), "worktrees.json")
	}
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = os.TempDir()
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "tmux-spaces", "worktrees.json")
}

// DefaultStore returns a Store backed by DefaultStorePath().
func DefaultStore() *Store {
	return &Store{
		Path: DefaultStorePath(),
		Now:  time.Now,
	}
}

func (s *Store) storePath() string {
	if s != nil && strings.TrimSpace(s.Path) != "" {
		return filepath.Clean(s.Path)
	}
	return DefaultStorePath()
}

func (s *Store) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) lock(exclusive bool) (func(), error) {
	s.mu.Lock()
	p := s.storePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("worktree store: mkdir %s: %w", dir, err)
	}
	unlockFile, err := lockStoreFile(p+".lock", exclusive)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		s.mu.Unlock()
	}, nil
}

// Load returns all persisted worktree records. Returns nil, nil if the state
// file does not exist yet.
func (s *Store) Load() ([]Record, error) {
	unlock, err := s.lock(false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() ([]Record, error) {
	p := s.storePath()
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("worktree store: read %s: %w", p, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var recs []Record
		if err := json.Unmarshal(data, &recs); err != nil {
			return nil, fmt.Errorf("worktree store: parse %s: %w", p, err)
		}
		return recs, nil
	}
	var payload storePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("worktree store: parse %s: %w", p, err)
	}
	return payload.Records, nil
}

func (s *Store) saveLocked(records []Record) error {
	p := s.storePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("worktree store: mkdir %s: %w", dir, err)
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAtMs != records[j].CreatedAtMs {
			return records[i].CreatedAtMs > records[j].CreatedAtMs
		}
		return records[i].Path < records[j].Path
	})

	payload := storePayload{
		Version: storeSchemaVersion,
		Records: records,
	}
	if payload.Records == nil {
		payload.Records = []Record{}
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("worktree store: marshal: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".worktrees-*.tmp")
	if err != nil {
		return fmt.Errorf("worktree store: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("worktree store: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("worktree store: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("worktree store: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, p); err != nil {
		cleanup()
		return fmt.Errorf("worktree store: rename %s: %w", p, err)
	}
	return nil
}

// Upsert inserts or updates a worktree Record matched by canonical path.
func (s *Store) Upsert(rec Record) error {
	if strings.TrimSpace(rec.Path) == "" {
		return errors.New("worktree store: record path is empty")
	}
	unlock, err := s.lock(true)
	if err != nil {
		return err
	}
	defer unlock()

	records, err := s.loadLocked()
	if err != nil {
		return err
	}

	rec.Path = canonicalPath(rec.Path)
	if rec.RepoKey != "" {
		rec.RepoKey = canonicalPath(rec.RepoKey)
	}
	if rec.MainRepoRoot != "" {
		rec.MainRepoRoot = canonicalPath(rec.MainRepoRoot)
	}
	nowMs := s.now().UnixMilli()
	if rec.LastUsedAtMs == 0 {
		rec.LastUsedAtMs = nowMs
	}

	updated := false
	for i := range records {
		if canonicalPath(records[i].Path) == rec.Path {
			if rec.CreatedAtMs == 0 {
				if records[i].CreatedAtMs != 0 {
					rec.CreatedAtMs = records[i].CreatedAtMs
				} else {
					rec.CreatedAtMs = nowMs
				}
			}
			records[i] = rec
			updated = true
			break
		}
	}
	if !updated {
		if rec.CreatedAtMs == 0 {
			rec.CreatedAtMs = nowMs
		}
		records = append(records, rec)
	}

	return s.saveLocked(records)
}

// GetByPath retrieves a Record by canonical path matching.
func (s *Store) GetByPath(path string) (Record, bool, error) {
	if strings.TrimSpace(path) == "" {
		return Record{}, false, nil
	}
	unlock, err := s.lock(false)
	if err != nil {
		return Record{}, false, err
	}
	defer unlock()

	records, err := s.loadLocked()
	if err != nil {
		return Record{}, false, err
	}
	target := canonicalPath(path)
	for _, rec := range records {
		if canonicalPath(rec.Path) == target {
			return rec, true, nil
		}
	}
	return Record{}, false, nil
}

// DeleteByPath removes any Record matching path canonically. Missing paths
// are not an error.
func (s *Store) DeleteByPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	unlock, err := s.lock(true)
	if err != nil {
		return err
	}
	defer unlock()

	records, err := s.loadLocked()
	if err != nil {
		return err
	}
	target := canonicalPath(path)
	kept := make([]Record, 0, len(records))
	changed := false
	for _, rec := range records {
		if canonicalPath(rec.Path) == target {
			changed = true
			continue
		}
		kept = append(kept, rec)
	}
	if !changed {
		return nil
	}
	return s.saveLocked(kept)
}

// ListByRepo returns all Records belonging to repoKey (canonical match).
func (s *Store) ListByRepo(repoKey string) ([]Record, error) {
	if strings.TrimSpace(repoKey) == "" {
		return nil, nil
	}
	unlock, err := s.lock(false)
	if err != nil {
		return nil, err
	}
	defer unlock()

	records, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	target := canonicalPath(repoKey)
	var out []Record
	for _, rec := range records {
		if canonicalPath(rec.RepoKey) == target {
			out = append(out, rec)
		}
	}
	return out, nil
}

// PruneMissing deletes records whose Path no longer exists on disk and returns
// the number of pruned records.
func (s *Store) PruneMissing() (int, error) {
	unlock, err := s.lock(true)
	if err != nil {
		return 0, err
	}
	defer unlock()

	records, err := s.loadLocked()
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}
	kept := make([]Record, 0, len(records))
	pruned := 0
	for _, rec := range records {
		if _, err := os.Stat(rec.Path); err != nil && errors.Is(err, os.ErrNotExist) {
			pruned++
			continue
		}
		kept = append(kept, rec)
	}
	if pruned == 0 {
		return 0, nil
	}
	if err := s.saveLocked(kept); err != nil {
		return 0, err
	}
	return pruned, nil
}

// canonicalPath resolves symlinks when possible (including when only parent
// directories exist) and returns a cleaned absolute path.
func canonicalPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	// Resolve existing ancestor symlinks when the leaf does not exist yet.
	cur := abs
	var tail []string
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := make([]string, 0, len(tail)+1)
			parts = append(parts, resolved)
			for i := len(tail) - 1; i >= 0; i-- {
				parts = append(parts, tail[i])
			}
			return filepath.Clean(filepath.Join(parts...))
		}
	}
	return filepath.Clean(abs)
}
