package spacelaunch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

// cacheVersion is bumped whenever File's JSON shape changes; older files
// are rebuilt on read.
const cacheVersion = 1

// cacheTTL bounds how stale the inventory may get between explicit
// refreshes. Profile mutations (create/edit/remove/etc.) rewrite the file
// synchronously; agent binary installs are picked up when the TTL lapses.
const cacheTTL = 15 * time.Minute

// CachedProfile is one launchable profile as the Spaces plugin sees it.
type CachedProfile struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Source      string `json:"source"`
	Agent       string `json:"agent"`
}

// CachedAgent is one harness with the profiles that launch through it.
type CachedAgent struct {
	Name        string          `json:"name"`
	Binary      string          `json:"binary"`
	BinaryState string          `json:"binaryState"`
	Note        string          `json:"note"`
	Profiles    []CachedProfile `json:"profiles"`
}

// CacheFile is the on-disk inventory at CachePath. Agents come from
// agents.Infos; profiles resolve to their agent via profiles.Resolve, which
// only reads local files (no subprocesses, no network), so a refresh is
// fast and safe to run on popup open.
type CacheFile struct {
	Version     int           `json:"version"`
	GeneratedAt int64         `json:"generatedAt"`
	Agents      []CachedAgent `json:"agents"`
}

// CachePath returns the inventory file location.
func CachePath(paths harnessenv.Paths) string {
	return filepath.Join(harnessenv.HarnessDir(paths), "harnesses.json")
}

// Refresh rebuilds the inventory from the current agents and profiles and
// writes it atomically (tmp+rename, 0600). Unresolvable profiles (disabled,
// unknown, or missing native credentials) are skipped; the launch path
// reports failures with a clear error instead.
func Refresh(paths harnessenv.Paths) error {
	infos := agents.Infos(paths)
	entries, err := profiles.ListAll(paths)
	if err != nil {
		return fmt.Errorf("zjspace: list profiles: %w", err)
	}
	byAgent := map[string][]CachedProfile{}
	for _, entry := range entries {
		// The Spaces picker supplies its own default row.
		if entry.Source == profiles.SourceDefault {
			continue
		}
		if _, err := profiles.ResolveForAgent(entry.Agent, entry.Name, paths); err != nil {
			continue
		}
		byAgent[entry.Agent] = append(byAgent[entry.Agent], CachedProfile(entry))
	}
	file := CacheFile{Version: cacheVersion, GeneratedAt: time.Now().Unix()}
	for _, info := range infos {
		// Emit [] rather than null for profile-less agents: consumers
		// distinguish missing from null.
		profs := byAgent[info.Name]
		if profs == nil {
			profs = []CachedProfile{}
		}
		sort.Slice(profs, func(i, j int) bool { return profs[i].Name < profs[j].Name })
		file.Agents = append(file.Agents, CachedAgent{
			Name:        info.Name,
			Binary:      info.Binary,
			BinaryState: info.BinaryState,
			Note:        info.Note,
			Profiles:    profs,
		})
	}
	path := CachePath(paths)
	data, err := marshalCache(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("zjspace: create cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "harnesses-*.json")
	if err != nil {
		return fmt.Errorf("zjspace: create cache tmp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("zjspace: write cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("zjspace: write cache: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("zjspace: chmod cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("zjspace: install cache: %w", err)
	}
	return nil
}

// marshalCache renders the inventory for disk and stdout.
func marshalCache(file CacheFile) ([]byte, error) {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("zjspace: encode harnesses: %w", err)
	}
	return append(data, '\n'), nil
}

// needsRefresh reports whether the cached file is missing, stale, from an
// older schema, or older than the harness binary itself (which may have
// added agents).
func needsRefresh(path string, self string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	if time.Since(fi.ModTime()) > cacheTTL {
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var file CacheFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != cacheVersion {
		return true
	}
	if selfBin, err := os.Stat(self); err == nil {
		if selfBin.ModTime().After(fi.ModTime()) {
			return true
		}
	}
	return false
}

// Load reads the inventory file as-is, without refreshing.
func Load(paths harnessenv.Paths) (CacheFile, error) {
	var file CacheFile
	data, err := os.ReadFile(CachePath(paths))
	if err != nil {
		return file, fmt.Errorf("zjspace: read harnesses cache: %w", err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("zjspace: parse harnesses cache: %w", err)
	}
	if file.Version != cacheVersion {
		return file, fmt.Errorf("zjspace: unsupported harnesses cache version %d", file.Version)
	}
	return file, nil
}

// EnsureFresh returns the inventory, rebuilding it first when it is
// missing or stale. A failed rebuild keeps serving the previous file when
// one exists; callers without any file get the error.
func EnsureFresh(paths harnessenv.Paths) (CacheFile, error) {
	path := CachePath(paths)
	self, _ := os.Executable()
	if needsRefresh(path, self) {
		if err := Refresh(paths); err != nil {
			if _, statErr := os.Stat(path); statErr != nil {
				return CacheFile{}, err
			}
		}
	}
	return Load(paths)
}

// RefreshCache rebuilds the inventory for the default paths. Profile
// mutations (create/edit/remove/etc.) call it synchronously so the Spaces
// plugin sees changes without waiting for the TTL.
func RefreshCache() error {
	return Refresh(harnessenv.DefaultPaths())
}

// ProfilesFor returns the cached profiles for one agent, or nil when the
// agent has none. The second result reports whether the agent exists.
func ProfilesFor(file CacheFile, agent string) ([]CachedProfile, bool) {
	for _, entry := range file.Agents {
		if entry.Name == agent {
			return entry.Profiles, true
		}
	}
	return nil, false
}

// profileNames joins profile names for error messages.
func profileNames(profs []CachedProfile) string {
	names := make([]string, 0, len(profs))
	for _, p := range profs {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
