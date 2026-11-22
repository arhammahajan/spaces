// Package resurrect implements native tmux session persistence and AI-agent
// conversation resurrection for tmux-spaces.
//
// Modeled on tmux-resurrect's core primitives (window layouts, per-pane CWDs,
// active window/pane selection, process inspection) and integrated directly
// with spaceresume / spacecore so restored AI agent panes reopen their exact
// conversation instead of a blank shell.
package resurrect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
)

// SnapshotVersion is the on-disk schema version for SpaceSnapshot files.
const SnapshotVersion = 1

// AutoSaveInterval matches Zellij's serialization_interval (30s).
const AutoSaveInterval = 30 * time.Second

// SpaceSnapshot captures one tmux session (Space) in full fidelity.
type SpaceSnapshot struct {
	Version           int              `json:"version"`
	Name              string           `json:"name"`
	SavedAtMs         int64            `json:"saved_at_ms"`
	CWD               string           `json:"cwd,omitempty"`
	WorktreePath      string           `json:"worktree_path,omitempty"`
	WorktreeName      string           `json:"worktree_name,omitempty"`
	WorktreeBase      string           `json:"worktree_base,omitempty"`
	MainRepoRoot      string           `json:"main_repo_root,omitempty"`
	ActiveWindowIndex int              `json:"active_window_index"`
	Windows           []WindowSnapshot `json:"windows"`
}

// WindowSnapshot captures one tmux window and its split layout string.
type WindowSnapshot struct {
	Index        int            `json:"index"`
	Name         string         `json:"name"`
	Layout       string         `json:"layout,omitempty"`
	Active       bool           `json:"active"`
	Zoomed       bool           `json:"zoomed,omitempty"`
	Width        int            `json:"width,omitempty"`
	Height       int            `json:"height,omitempty"`
	WorktreePath string         `json:"worktree_path,omitempty"`
	WorktreeName string         `json:"worktree_name,omitempty"`
	WorktreeBase string         `json:"worktree_base,omitempty"`
	Panes        []PaneSnapshot `json:"panes"`
}

// OpenState records the ordered list of spaces that were actively open (not
// exited) on the tmux server, along with the last focused space. Persisted to
// a dotfile inside the snapshot directory so Store.ListAll() ignores it.
type OpenState struct {
	Version       int      `json:"version"`
	SavedAtMs     int64    `json:"saved_at_ms"`
	ActiveSession string   `json:"active_session,omitempty"`
	Spaces        []string `json:"spaces"`
}

// PaneSnapshot captures one tmux pane, including AI-agent resume metadata.
type PaneSnapshot struct {
	Index       int      `json:"index"`
	ID          string   `json:"id,omitempty"`
	CWD         string   `json:"cwd,omitempty"`
	Command     string   `json:"command,omitempty"`
	App         string   `json:"app,omitempty"`
	Title       string   `json:"title,omitempty"`
	Active      bool     `json:"active"`
	Agent       string   `json:"agent,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	Profile     string   `json:"profile,omitempty"`
	FullCommand []string `json:"full_command,omitempty"`
}

// Store persists per-space snapshots as atomic JSON files.
type Store struct {
	Dir string
	Now func() time.Time
}

// DefaultDir resolves the snapshot storage directory:
// $TMUX_SPACES_SNAPSHOT_DIR, else ${XDG_DATA_HOME:-~/.local/share}/tmux-spaces/snapshots.
func DefaultDir() string {
	if d := strings.TrimSpace(os.Getenv("TMUX_SPACES_SNAPSHOT_DIR")); d != "" {
		return d
	}
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = os.TempDir()
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "tmux-spaces", "snapshots")
}

// DefaultStore returns a Store backed by DefaultDir().
func DefaultStore() *Store {
	return &Store{Dir: DefaultDir(), Now: time.Now}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) snapshotPath(name string) string {
	sum := sha256.Sum256([]byte(name))
	safe := sanitizeSlug(name)
	if safe == "" {
		safe = "space"
	}
	return filepath.Join(s.Dir, fmt.Sprintf("%s-%s.json", safe, hex.EncodeToString(sum[:6])))
}

func sanitizeSlug(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "_")
}

// Save atomically persists snap as 0600 JSON.
func (s *Store) Save(snap SpaceSnapshot) error {
	if strings.TrimSpace(snap.Name) == "" {
		return errors.New("resurrect: space name is empty")
	}
	if snap.Version == 0 {
		snap.Version = SnapshotVersion
	}
	if snap.SavedAtMs == 0 {
		snap.SavedAtMs = s.now().UnixMilli()
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("resurrect: mkdir %s: %w", s.Dir, err)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("resurrect: marshal %s: %w", snap.Name, err)
	}
	data = append(data, '\n')
	dst := s.snapshotPath(snap.Name)
	tmp, err := os.CreateTemp(s.Dir, ".snap-*.tmp")
	if err != nil {
		return fmt.Errorf("resurrect: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("resurrect: rename %s: %w", dst, err)
	}
	return nil
}

// Load reads the persisted snapshot for name.
func (s *Store) Load(name string) (SpaceSnapshot, error) {
	path := s.snapshotPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		return SpaceSnapshot{}, err
	}
	var snap SpaceSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return SpaceSnapshot{}, fmt.Errorf("resurrect: parse %s: %w", path, err)
	}
	return snap, nil
}

// Delete removes the persisted snapshot for name. Missing files return nil.
func (s *Store) Delete(name string) error {
	err := os.Remove(s.snapshotPath(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ListAll returns all valid snapshots sorted newest-first.
func (s *Store) ListAll() ([]SpaceSnapshot, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []SpaceSnapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			continue
		}
		var snap SpaceSnapshot
		if err := json.Unmarshal(data, &snap); err != nil || snap.Name == "" {
			continue
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SavedAtMs != out[j].SavedAtMs {
			return out[i].SavedAtMs > out[j].SavedAtMs
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ListExited returns snapshots whose session name is not in running.
func (s *Store) ListExited(running map[string]bool) ([]model.ExitedSpace, error) {
	all, err := s.ListAll()
	if err != nil {
		return nil, err
	}
	var out []model.ExitedSpace
	for _, snap := range all {
		if running[snap.Name] {
			continue
		}
		paneCount := 0
		agentSeen := map[string]bool{}
		var agents []string
		for _, w := range snap.Windows {
			paneCount += len(w.Panes)
			for _, p := range w.Panes {
				if p.Agent != "" && !agentSeen[p.Agent] {
					agentSeen[p.Agent] = true
					agents = append(agents, p.Agent)
				}
			}
		}
		out = append(out, model.ExitedSpace{
			Name:        snap.Name,
			SavedAt:     time.UnixMilli(snap.SavedAtMs),
			CWD:         snap.CWD,
			WindowCount: len(snap.Windows),
			PaneCount:   paneCount,
			Agents:      agents,
		})
	}
	return out, nil
}

func (s *Store) stampPath() string {
	return filepath.Join(s.Dir, ".last-autosave")
}

// ShouldAutoSave reports whether at least AutoSaveInterval has elapsed since
// the last autosave.
func (s *Store) ShouldAutoSave() bool {
	info, err := os.Stat(s.stampPath())
	if err != nil {
		return true
	}
	return s.now().Sub(info.ModTime()) >= AutoSaveInterval
}

// TouchAutoSave records the current autosave timestamp.
func (s *Store) TouchAutoSave() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	now := s.now()
	p := s.stampPath()
	if err := os.WriteFile(p, []byte(fmt.Sprintf("%d\n", now.UnixMilli())), 0o600); err != nil {
		return err
	}
	return os.Chtimes(p, now, now)
}

func (s *Store) openStatePath() string {
	return filepath.Join(s.Dir, ".open-spaces.json")
}

// SaveOpenState atomically persists the set of currently open (non-exited) spaces.
func (s *Store) SaveOpenState(state OpenState) error {
	if state.Version == 0 {
		state.Version = SnapshotVersion
	}
	if state.SavedAtMs == 0 {
		state.SavedAtMs = s.now().UnixMilli()
	}
	seen := make(map[string]bool, len(state.Spaces))
	dedup := make([]string, 0, len(state.Spaces))
	for _, name := range state.Spaces {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		dedup = append(dedup, name)
	}
	state.Spaces = dedup
	state.ActiveSession = strings.TrimSpace(state.ActiveSession)

	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("resurrect: mkdir %s: %w", s.Dir, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("resurrect: marshal open state: %w", err)
	}
	data = append(data, '\n')
	dst := s.openStatePath()
	tmp, err := os.CreateTemp(s.Dir, ".open-*.tmp")
	if err != nil {
		return fmt.Errorf("resurrect: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("resurrect: rename %s: %w", dst, err)
	}
	return nil
}

// LoadOpenState reads the persisted list of last-open spaces.
func (s *Store) LoadOpenState() (OpenState, error) {
	path := s.openStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return OpenState{}, err
	}
	var state OpenState
	if err := json.Unmarshal(data, &state); err != nil {
		return OpenState{}, fmt.Errorf("resurrect: parse %s: %w", path, err)
	}
	return state, nil
}

// AddOpenSpace records name as open in OpenState, optionally updating ActiveSession.
func (s *Store) AddOpenSpace(name string, makeActive bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	state, err := s.LoadOpenState()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		state = OpenState{Version: SnapshotVersion}
	}
	found := false
	for _, sp := range state.Spaces {
		if sp == name {
			found = true
			break
		}
	}
	if !found {
		state.Spaces = append(state.Spaces, name)
	}
	if makeActive || state.ActiveSession == "" {
		state.ActiveSession = name
	}
	state.SavedAtMs = s.now().UnixMilli()
	return s.SaveOpenState(state)
}

// RemoveOpenSpace removes name from OpenState so a closed space is not
// automatically reopened on reboot.
func (s *Store) RemoveOpenSpace(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	state, err := s.LoadOpenState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	kept := make([]string, 0, len(state.Spaces))
	changed := false
	for _, sp := range state.Spaces {
		if sp == name {
			changed = true
			continue
		}
		kept = append(kept, sp)
	}
	if state.ActiveSession == name {
		state.ActiveSession = ""
		if len(kept) > 0 {
			state.ActiveSession = kept[0]
		}
		changed = true
	}
	if !changed {
		return nil
	}
	state.Spaces = kept
	state.SavedAtMs = s.now().UnixMilli()
	return s.SaveOpenState(state)
}

// RenameOpenSpace updates OpenState (and any existing snapshot file) when a
// space is renamed.
func (s *Store) RenameOpenSpace(oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" || oldName == newName {
		return nil
	}
	if snap, err := s.Load(oldName); err == nil {
		snap.Name = newName
		if err := s.Save(snap); err == nil {
			_ = s.Delete(oldName)
		}
	}
	state, err := s.LoadOpenState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	changed := false
	for i, sp := range state.Spaces {
		if sp == oldName {
			state.Spaces[i] = newName
			changed = true
		}
	}
	if state.ActiveSession == oldName {
		state.ActiveSession = newName
		changed = true
	}
	if !changed {
		return nil
	}
	state.SavedAtMs = s.now().UnixMilli()
	return s.SaveOpenState(state)
}
