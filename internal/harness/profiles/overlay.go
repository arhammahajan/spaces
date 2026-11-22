package profiles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// SharedLink describes a targeted shared entry (often nested inside a real
// per-profile directory such as .gemini/config, .gemini/antigravity-cli, or
// cline's settings/) to symlink from the shared home or dotfiles checkout.
type SharedLink struct {
	ShadowRel       string   // relative to overlay root
	SharedRels      []string // candidate relative paths under sharedDir (and default shared home)
	DotfilesRel     string   // optional fallback relative path under DotfilesRoot(paths)
	EnsureSharedDir bool     // when true and no candidate exists yet, pre-create SharedRels[0] in sharedDir
	DirOnly         bool     // when true, candidate must be a directory
}

// OverlayLayout describes how a shadow home shares conversation/chat/config
// state with an agent's default home while isolating its private auth store.
type OverlayLayout struct {
	Agent                string
	DefaultSharedBase    string          // "home" (default) or "data" (XDG_DATA_HOME)
	DefaultSharedRelHome string          // relative to base (e.g. ".codex", ".pi/agent", "opencode")
	ShadowSubdir         string          // optional subdir inside shadowDir where overlay links live (e.g. ".gemini", "opencode", "kilo")
	AuthFileOnly         bool            // true when env var points straight to <shadowDir>/<authFile> (e.g. MUSE_AUTH_PATH)
	ShareKnownDirsOnly   bool            // share only the explicitly listed directories
	KnownSharedDirs      []string        // always pre-created in sharedDir and symlinked into overlay root
	BaseLinks            []SharedLink    // always symlinked into shadow home as base config (even when --no-overlay)
	SharedLinks          []SharedLink    // nested/targeted shared entries (MCPs, skills, chats inside private dirs)
	AuthFiles            []string        // primary credential files/paths: must be real, never symlinks
	PrivateEntries       map[string]bool // never symlinked from sharedDir (auth files, locks, caches)
	ShadowLocalEntries   map[string]bool // local per shadow home, never symlinked
	ReplaceableRuntime   map[string]bool // runtime locks that may be replaced with a shared symlink
}

var overlayLayouts = map[string]OverlayLayout{
	"codex": {
		Agent:                "codex",
		DefaultSharedRelHome: ".codex",
		KnownSharedDirs: []string{
			"sessions",
			"archived_sessions",
			"sqlite",
			"shell_snapshots",
			"worktrees",
			"skills",
			"plugins",
			"cache",
			"mcp-oauth-locks",
		},
		BaseLinks: []SharedLink{
			{
				ShadowRel:   "hooks.json",
				SharedRels:  []string{"hooks.json"},
				DotfilesRel: filepath.Join("codex", ".codex", "hooks.json"),
			},
		},
		AuthFiles: []string{"auth.json"},
		PrivateEntries: map[string]bool{
			"auth.json":         true,
			"auth.json.lock":    true,
			"auth.lock":         true,
			"models_cache.json": true,
		},
		ShadowLocalEntries: map[string]bool{
			"log":                true,
			"logs":               true,
			"memories":           true,
			"tmp":                true,
			".tmp":               true,
			".DS_Store":          true,
			"app-server-control": true,
			"app-server-daemon":  true,
			"installation_id":    true,
			"logs_2.sqlite":      true,
			"logs_2.sqlite-wal":  true,
			"logs_2.sqlite-shm":  true,
			"queue_1.sqlite":     true,
			"queue_1.sqlite-wal": true,
			"queue_1.sqlite-shm": true,
		},
		ReplaceableRuntime: map[string]bool{
			"mcp-oauth-locks":          true,
			".sqlite-maintenance.lock": true,
		},
	},
	"claude": {
		Agent:                "claude",
		DefaultSharedRelHome: ".claude",
		KnownSharedDirs: []string{
			"projects",
			"sessions",
			"file-history",
			"session-env",
			"plans",
			"skills",
			"plugins",
			"tasks",
			"teams",
			"themes",
			"shell-snapshots",
			"backups",
		},
		BaseLinks: []SharedLink{
			{
				ShadowRel:       "hooks",
				SharedRels:      []string{"hooks"},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
		},
		AuthFiles: []string{".credentials.json", "credentials.json"},
		PrivateEntries: map[string]bool{
			".credentials.json":        true,
			"credentials.json":         true,
			".claude.json":             true,
			"auth.json":                true,
			"stats-cache.json":         true,
			".last-cleanup":            true,
			".last-update-result.json": true,
		},
		ShadowLocalEntries: map[string]bool{
			"cache":     true,
			"debug":     true,
			"downloads": true,
			"log":       true,
			"logs":      true,
			"telemetry": true,
			"tmp":       true,
			".DS_Store": true,
		},
	},
	"grok": {
		Agent:                "grok",
		DefaultSharedRelHome: ".grok",
		KnownSharedDirs: []string{
			"sessions",
			"skills",
			"installed-plugins",
			"memory-v2",
			"memtrace",
			"docs",
			"completions",
			"bundled",
			"bin",
		},
		BaseLinks: []SharedLink{
			{
				ShadowRel:       "hooks",
				SharedRels:      []string{"hooks"},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
		},
		AuthFiles: []string{"auth.json"},
		PrivateEntries: map[string]bool{
			"auth.json":              true,
			"auth.json.lock":         true,
			"auth.json.commit.lock":  true,
			"models_cache.json":      true,
			"models_cache.json.lock": true,
			"settings_cache.json":    true,
		},
		ShadowLocalEntries: map[string]bool{
			"logs":                 true,
			"downloads":            true,
			"marketplace-cache":    true,
			"tmp":                  true,
			".DS_Store":            true,
			"active_sessions.json": true,
			"active_sessions.lock": true,
			"agent_id":             true,
			".config-init.lock":    true,
			"managed_config.lock":  true,
		},
	},
	"antigravity": {
		Agent:                "antigravity",
		DefaultSharedRelHome: ".gemini",
		ShadowSubdir:         ".gemini",
		ShareKnownDirsOnly:   true,
		KnownSharedDirs:      []string{"history", "skills"},
		BaseLinks: []SharedLink{
			{
				ShadowRel:   filepath.Join("config", "hooks.json"),
				SharedRels:  []string{filepath.Join("config", "hooks.json")},
				DotfilesRel: filepath.Join("antigravity", ".gemini", "config", "hooks.json"),
			},
		},
		SharedLinks: []SharedLink{
			{
				ShadowRel:   filepath.Join("config", "skills"),
				SharedRels:  []string{filepath.Join("config", "skills")},
				DotfilesRel: filepath.Join("agents", ".agents", "skills"),
				DirOnly:     true,
			},
			{
				ShadowRel:   filepath.Join("config", "mcp_config.json"),
				SharedRels:  []string{filepath.Join("config", "mcp_config.json")},
				DotfilesRel: filepath.Join("antigravity", ".gemini", "config", "mcp_config.json"),
			},
			{
				ShadowRel:       filepath.Join("antigravity-cli", "conversations"),
				SharedRels:      []string{filepath.Join("antigravity-cli", "conversations")},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
			{
				ShadowRel:       filepath.Join("antigravity-cli", "brain"),
				SharedRels:      []string{filepath.Join("antigravity-cli", "brain")},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
			{
				ShadowRel:       filepath.Join("antigravity-cli", "annotations"),
				SharedRels:      []string{filepath.Join("antigravity-cli", "annotations")},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
			{
				ShadowRel:       filepath.Join("antigravity-cli", "implicit"),
				SharedRels:      []string{filepath.Join("antigravity-cli", "implicit")},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
			{
				ShadowRel:  filepath.Join("antigravity-cli", "history.jsonl"),
				SharedRels: []string{filepath.Join("antigravity-cli", "history.jsonl")},
			},
			{
				ShadowRel:  filepath.Join("antigravity-cli", "jetbox_summaries_proto.pb"),
				SharedRels: []string{filepath.Join("antigravity-cli", "jetbox_summaries_proto.pb")},
			},
		},
		AuthFiles: []string{antigravityCLIToken, "google_accounts.json", "oauth_creds.json", "credentials.json", "auth.json"},
	},
	"gemini": {
		Agent:                "gemini",
		DefaultSharedRelHome: ".gemini",
		ShadowSubdir:         ".gemini",
		KnownSharedDirs: []string{
			"history",
			"skills",
		},
		AuthFiles: []string{"oauth_creds.json", "google_accounts.json", "credentials.json", "auth.json"},
		PrivateEntries: map[string]bool{
			"oauth_creds.json":     true,
			"google_accounts.json": true,
			"credentials.json":     true,
			"auth.json":            true,
			"state.json":           true,
			"state.json.bak":       true,
		},
		ShadowLocalEntries: map[string]bool{
			"log":             true,
			"logs":            true,
			"tmp":             true,
			".DS_Store":       true,
			"installation_id": true,
			"antigravity":     true,
			"antigravity-cli": true,
			"antigravity-acp": true,
			"config":          true,
		},
	},
	"copilot": {
		Agent:                "copilot",
		DefaultSharedRelHome: ".copilot",
		KnownSharedDirs: []string{
			"session-state",
			"sidebar-sessions-state",
			"installed-plugins",
			"ide",
			"servers",
		},
		BaseLinks: []SharedLink{
			{
				ShadowRel:       "hooks",
				SharedRels:      []string{"hooks"},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
		},
		AuthFiles: []string{"config.json", "auth.json", "credentials.json"},
		PrivateEntries: map[string]bool{
			"config.json":                      true,
			"config.json.trusted-folders.lock": true,
			"auth.json":                        true,
			"credentials.json":                 true,
		},
		ShadowLocalEntries: map[string]bool{
			"logs":                   true,
			"tmp":                    true,
			".DS_Store":              true,
			"installed-plugins.lock": true,
			"settings.json.lock":     true,
		},
	},
	"deepseek": {
		Agent:                "deepseek",
		DefaultSharedRelHome: ".dsh",
		KnownSharedDirs: []string{
			"sessions",
			"storages",
			"profiles",
		},
		AuthFiles: []string{".credentials.yaml", "auth.json", "credentials.json"},
		PrivateEntries: map[string]bool{
			".credentials.yaml": true,
			"auth.json":         true,
			"credentials.json":  true,
		},
		ShadowLocalEntries: map[string]bool{
			".anonymous-user-id": true,
			"log":                true,
			"logs":               true,
			"tmp":                true,
			".DS_Store":          true,
		},
	},
	"cline": {
		Agent:                "cline",
		DefaultSharedRelHome: filepath.Join(".cline", "data"),
		KnownSharedDirs: []string{
			"sessions",
			"db",
		},
		SharedLinks: []SharedLink{
			{
				ShadowRel:   filepath.Join("settings", "cline_mcp_settings.json"),
				SharedRels:  []string{filepath.Join("settings", "cline_mcp_settings.json")},
				DotfilesRel: filepath.Join("cline", ".cline", "data", "settings", "cline_mcp_settings.json"),
			},
		},
		AuthFiles: []string{filepath.Join("settings", "providers.json"), "auth.json"},
		PrivateEntries: map[string]bool{
			"settings":  true,
			"auth.json": true,
		},
		ShadowLocalEntries: map[string]bool{
			"cache":     true,
			"locks":     true,
			"logs":      true,
			"tmp":       true,
			".DS_Store": true,
		},
	},
	"opencode": {
		Agent:                "opencode",
		DefaultSharedBase:    "data",
		DefaultSharedRelHome: "opencode",
		ShadowSubdir:         "opencode",
		KnownSharedDirs: []string{
			"repos",
			"shell",
			"snapshot",
			"tool-output",
		},
		AuthFiles: []string{"auth.json"},
		PrivateEntries: map[string]bool{
			"auth.json":     true,
			"mcp-auth.json": true,
		},
		ShadowLocalEntries: map[string]bool{
			"log":       true,
			"tmp":       true,
			".DS_Store": true,
		},
	},
	"kilo": {
		Agent:                "kilo",
		DefaultSharedBase:    "data",
		DefaultSharedRelHome: "kilo",
		ShadowSubdir:         "kilo",
		KnownSharedDirs: []string{
			"repos",
			"snapshot",
			"storage",
		},
		AuthFiles: []string{"auth.json"},
		PrivateEntries: map[string]bool{
			"auth.json":     true,
			"mcp-auth.json": true,
		},
		ShadowLocalEntries: map[string]bool{
			"log":          true,
			"telemetry-id": true,
			"tmp":          true,
			".DS_Store":    true,
		},
	},
	"pi": {
		Agent:                "pi",
		DefaultSharedRelHome: filepath.Join(".pi", "agent"),
		KnownSharedDirs: []string{
			"sessions",
			"extensions",
			"bin",
			"git",
			"install",
			"npm",
		},
		AuthFiles: []string{"auth.json"},
		PrivateEntries: map[string]bool{
			"auth.json":         true,
			"models-store.json": true,
		},
		ShadowLocalEntries: map[string]bool{
			"log":          true,
			"logs":         true,
			"pi-debug.log": true,
			"tmp":          true,
			".DS_Store":    true,
		},
	},
	"qwen": {
		Agent:                "qwen",
		DefaultSharedRelHome: ".qwen",
		KnownSharedDirs: []string{
			"projects",
			"sessions",
			"file-history",
			"memories",
			"skills",
			"extensions",
			"extension-store",
		},
		BaseLinks: []SharedLink{
			{
				ShadowRel:       "hooks",
				SharedRels:      []string{"hooks"},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
		},
		AuthFiles: []string{"oauth_creds.json", "credentials.json", "auth.json"},
		PrivateEntries: map[string]bool{
			"oauth_creds.json":    true,
			"credentials.json":    true,
			"auth.json":           true,
			"model-registry.json": true,
			"usage_record.jsonl":  true,
			"tip_history.json":    true,
		},
		ShadowLocalEntries: map[string]bool{
			"debug":           true,
			"log":             true,
			"logs":            true,
			"tmp":             true,
			"usage":           true,
			"installation_id": true,
			".DS_Store":       true,
		},
	},
	"kimi": {
		Agent:                "kimi",
		DefaultSharedRelHome: ".kimi-code",
		KnownSharedDirs: []string{
			"sessions",
			"user-history",
			"skills",
			"bin",
			"workspace-trust",
			"updates",
		},
		BaseLinks: []SharedLink{
			{
				ShadowRel:       "hooks",
				SharedRels:      []string{"hooks"},
				EnsureSharedDir: true,
				DirOnly:         true,
			},
		},
		AuthFiles: []string{"auth.json", "credentials.json", "credentials"},
		PrivateEntries: map[string]bool{
			"auth.json":        true,
			"credentials.json": true,
			"credentials":      true,
			"device_id":        true,
		},
		ShadowLocalEntries: map[string]bool{
			"cache":                true,
			"locks":                true,
			"logs":                 true,
			"telemetry":            true,
			"tmp":                  true,
			".DS_Store":            true,
			".projects-write-lock": true,
		},
	},
	"kimi-cli": {
		Agent:                "kimi-cli",
		DefaultSharedRelHome: ".kimi",
		KnownSharedDirs: []string{
			"sessions",
			"plans",
			"user-history",
			"skills",
		},
		AuthFiles: []string{"credentials", "auth.json", "credentials.json"},
		PrivateEntries: map[string]bool{
			"credentials":      true,
			"auth.json":        true,
			"credentials.json": true,
			"device_id":        true,
		},
		ShadowLocalEntries: map[string]bool{
			"logs":      true,
			"telemetry": true,
			"tmp":       true,
			".DS_Store": true,
		},
	},
	"mcode": {
		Agent:                "mcode",
		DefaultSharedRelHome: ".minimax",
		KnownSharedDirs: []string{
			"v2",
			"skills",
			"plugins",
			"shims",
			"integrations",
		},
		AuthFiles: []string{"auth.json", "auth", "credentials.json"},
		PrivateEntries: map[string]bool{
			"auth":             true,
			"auth.json":        true,
			"credentials.json": true,
			"model-cache.json": true,
		},
		ShadowLocalEntries: map[string]bool{
			"cache":                            true,
			"log":                              true,
			"logs":                             true,
			"run":                              true,
			"tmp":                              true,
			".DS_Store":                        true,
			"business-telemetry.identity.json": true,
		},
	},
	"muse": {
		Agent:        "muse",
		AuthFileOnly: true,
		AuthFiles:    []string{"auth.json"},
		PrivateEntries: map[string]bool{
			"auth.json": true,
		},
	},
	"fx": {
		Agent:                "fx",
		DefaultSharedRelHome: ".fx",
		KnownSharedDirs: []string{
			"sessions",
			"skills",
			"file-index",
			"backups",
		},
		AuthFiles: []string{"auth.json", "credentials.json"},
		PrivateEntries: map[string]bool{
			"auth.json":        true,
			"credentials.json": true,
			"mcp-credentials":  true,
		},
		ShadowLocalEntries: map[string]bool{
			"diagnostics":   true,
			"log":           true,
			"logs":          true,
			"tmp":           true,
			".DS_Store":     true,
			"history.lock":  true,
			"settings.lock": true,
		},
	},
	"vibe": {
		Agent:                "vibe",
		DefaultSharedRelHome: ".vibe",
		KnownSharedDirs: []string{
			"logs",
			"skills",
			"agents",
		},
		AuthFiles: []string{".env", "auth.json", "credentials.json"},
		PrivateEntries: map[string]bool{
			".env":             true,
			"auth.json":        true,
			"credentials.json": true,
			"cache.toml":       true,
		},
		ShadowLocalEntries: map[string]bool{
			"cache":                      true,
			"locks":                      true,
			"tmp":                        true,
			".DS_Store":                  true,
			"skills-registry-cache":      true,
			".trusted_folders.toml.lock": true,
			"ui_startup_config.json":     true,
		},
	},
}

// OverlayLayoutFor returns the shadow-home overlay layout for agent, if the
// agent supports shared-conversation / isolated-auth overlays.
func OverlayLayoutFor(agent string) (OverlayLayout, bool) {
	layout, ok := overlayLayouts[agent]
	return layout, ok
}

// SupportsOverlay reports whether agent has a shadow-home overlay layout.
func SupportsOverlay(agent string) bool {
	_, ok := OverlayLayoutFor(agent)
	return ok
}

// DefaultSharedHome returns the default shared home directory for an
// overlay-capable agent (e.g. $HOME/.codex, $HOME/.claude, $DataHome/opencode),
// or "" if the agent uses direct auth-file isolation or has no overlay layout.
func DefaultSharedHome(agent string, paths harnessenv.Paths) string {
	layout, ok := OverlayLayoutFor(agent)
	if !ok || layout.AuthFileOnly || layout.DefaultSharedRelHome == "" {
		return ""
	}
	base := paths.Home
	if layout.DefaultSharedBase == "data" {
		base = paths.DataHome
	}
	return filepath.Join(base, layout.DefaultSharedRelHome)
}

// DefaultCodexSharedHome returns the shared Codex home for overlay
// materialization: $HOME/.codex.
func DefaultCodexSharedHome(paths harnessenv.Paths) string {
	return DefaultSharedHome("codex", paths)
}

// overlayRoot returns the directory inside shadowDir where symlinks and auth
// files live (either shadowDir itself or shadowDir/ShadowSubdir).
func overlayRoot(layout OverlayLayout, shadowDir string) string {
	if layout.ShadowSubdir != "" {
		return filepath.Join(shadowDir, layout.ShadowSubdir)
	}
	return shadowDir
}

// AuthStoreStatus reports the primary private auth file path for a profile's
// shadow home and whether a real (non-symlink) credential file/dir is present.
// Credential contents are never read or returned.
func AuthStoreStatus(p Profile, paths harnessenv.Paths) (authPath string, present bool, ok bool) {
	layout, supported := OverlayLayoutFor(p.Agent)
	if !supported || p.ShadowHome == "" || len(layout.AuthFiles) == 0 {
		return "", false, false
	}
	root := overlayRoot(layout, p.ShadowHome)
	for _, name := range layout.AuthFiles {
		cand := filepath.Join(root, name)
		fi, err := os.Lstat(cand)
		if err == nil && fi.Mode()&os.ModeSymlink == 0 {
			if fi.IsDir() {
				// Credential directory (e.g. kimi credentials/) counts as present when non-empty.
				if items, rerr := os.ReadDir(cand); rerr == nil && len(items) > 0 {
					return cand, true, true
				}
				continue
			}
			return cand, true, true
		}
	}
	return filepath.Join(root, layout.AuthFiles[0]), false, true
}

// MaterializeShadowHome materializes an agent's auth-overlay shadow home:
//   - shadowDir (and its overlay root when ShadowSubdir is set) is created with
//     mode 0700 (owner-only) for credential isolation.
//   - For AuthFileOnly layouts (e.g. muse/MUSE_AUTH_PATH), only shadowDir is
//     ensured and primary auth files are verified not to be symlinks (the
//     agent's native XDG config/session stores remain shared).
//   - If the overlay root resolves to the same path as sharedDir, returns an error.
//   - When sharedDir is non-empty, sharedDir and its KnownSharedDirs
//     (conversation/chat stores, skills, hooks, etc.) are ensured and symlinked
//     into the overlay root along with any other non-private, non-shadow-local
//     entries currently in sharedDir.
//   - Stale symlinks on non-primary private entries (e.g. models_cache.json)
//     are removed so each profile maintains its own local cache/locks.
//   - Primary auth files (e.g. auth.json, .credentials.json) are never
//     symlinked; if a symlink exists at a primary auth path, an error is
//     returned so credentials are always stored as real per-profile files.
//   - Replaceable runtime lock entries are swapped for shared symlinks; any
//     other non-symlink conflict in the overlay root returns an error rather
//     than clobbering user data.
func overlayEnabled(p Profile) bool {
	return p.Overlay == nil || *p.Overlay
}

func MaterializeShadowHome(agent, shadowDir, sharedDir string) error {
	return MaterializeShadowHomeWithPaths(agent, shadowDir, sharedDir, harnessenv.Paths{})
}

// MaterializeShadowHomeWithPaths materializes an agent's auth-overlay shadow
// home using paths for dotfiles fallbacks (shared skills and MCP configs).
func MaterializeShadowHomeWithPaths(agent, shadowDir, sharedDir string, paths harnessenv.Paths) error {
	if shadowDir == "" {
		return fmt.Errorf("profiles: %s shadow home is empty", agent)
	}
	layout, supported := OverlayLayoutFor(agent)
	if !supported {
		if err := os.MkdirAll(shadowDir, 0o700); err != nil {
			return fmt.Errorf("profiles: create shadow home: %w", err)
		}
		return nil
	}

	if err := prepareAntigravityHome(agent, shadowDir, sharedDir); err != nil {
		return err
	}

	absShadow, err := filepath.Abs(shadowDir)
	if err != nil {
		return fmt.Errorf("profiles: resolve %s shadow home: %w", agent, err)
	}
	if err := os.MkdirAll(absShadow, 0o700); err != nil {
		return fmt.Errorf("profiles: create %s shadow home: %w", agent, err)
	}
	root := overlayRoot(layout, absShadow)
	if root != absShadow {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return fmt.Errorf("profiles: create %s overlay root: %w", agent, err)
		}
	}

	if layout.AuthFileOnly || sharedDir == "" {
		return verifyPrivateAuthFiles(agent, root, layout.AuthFiles)
	}

	absShared, err := filepath.Abs(sharedDir)
	if err != nil {
		return fmt.Errorf("profiles: resolve %s shared home: %w", agent, err)
	}
	if root == absShared {
		return fmt.Errorf("profiles: %s shadow home %s must be different from shared home %s", agent, root, absShared)
	}

	if st, err := os.Stat(absShared); err == nil && !st.IsDir() {
		return fmt.Errorf("profiles: %s shared home %s is not a directory", agent, absShared)
	}
	if err := os.MkdirAll(absShared, 0o755); err != nil {
		return fmt.Errorf("profiles: create %s shared home: %w", agent, err)
	}
	for _, dir := range layout.KnownSharedDirs {
		if err := os.MkdirAll(filepath.Join(absShared, dir), 0o755); err != nil {
			return fmt.Errorf("profiles: create %s shared dir %s: %w", agent, dir, err)
		}
	}

	entries := make(map[string]bool, len(layout.KnownSharedDirs))
	for _, dir := range layout.KnownSharedDirs {
		entries[dir] = true
	}
	if !layout.ShareKnownDirsOnly {
		dirEntries, err := os.ReadDir(absShared)
		if err != nil {
			return fmt.Errorf("profiles: read %s shared home: %w", agent, err)
		}
		for _, e := range dirEntries {
			name := e.Name()
			if layout.PrivateEntries[name] || layout.ShadowLocalEntries[name] {
				continue
			}
			entries[name] = true
		}
	}

	authSet := make(map[string]bool, len(layout.AuthFiles))
	for _, name := range layout.AuthFiles {
		authSet[name] = true
	}

	// Remove stale symlinks on non-primary private entries (e.g. models_cache.json).
	for name := range layout.PrivateEntries {
		if authSet[name] {
			continue
		}
		privPath := filepath.Join(root, name)
		if fi, err := os.Lstat(privPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if rerr := os.Remove(privPath); rerr != nil {
				return fmt.Errorf("profiles: remove stale %s private symlink %s: %w", agent, privPath, rerr)
			}
		}
	}

	for name := range entries {
		if layout.PrivateEntries[name] {
			continue
		}
		target := filepath.Join(absShared, name)
		link := filepath.Join(root, name)
		fi, err := os.Lstat(link)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				cur, rerr := os.Readlink(link)
				if rerr != nil {
					return fmt.Errorf("profiles: read %s shadow link %s: %w", agent, link, rerr)
				}
				resolvedCur := cur
				if !filepath.IsAbs(resolvedCur) {
					resolvedCur = filepath.Clean(filepath.Join(filepath.Dir(link), cur))
				}
				if resolvedCur == target {
					continue
				}
				if rerr := os.Remove(link); rerr != nil {
					return fmt.Errorf("profiles: replace %s shadow link %s: %w", agent, link, rerr)
				}
			} else if layout.ReplaceableRuntime[name] {
				if rerr := os.RemoveAll(link); rerr != nil {
					return fmt.Errorf("profiles: replace %s runtime entry %s: %w", agent, link, rerr)
				}
			} else if fi.IsDir() && isEmptyDir(link) {
				if st, serr := os.Stat(target); serr == nil && st.IsDir() {
					if rerr := os.Remove(link); rerr != nil {
						return fmt.Errorf("profiles: replace empty %s dir %s: %w", agent, link, rerr)
					}
				} else {
					return fmt.Errorf("profiles: %s shadow entry %s already exists and is not a symlink", agent, link)
				}
			} else {
				return fmt.Errorf("profiles: %s shadow entry %s already exists and is not a symlink", agent, link)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("profiles: stat %s shadow entry %s: %w", agent, link, err)
		}
		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("profiles: symlink %s shadow entry %s: %w", agent, link, err)
		}
	}

	if err := applySharedLinks(agent, layout.BaseLinks, root, absShared, paths); err != nil {
		return err
	}
	if err := applySharedLinks(agent, layout.SharedLinks, root, absShared, paths); err != nil {
		return err
	}

	return verifyPrivateAuthFiles(agent, root, layout.AuthFiles)
}

// MaterializeBaseConfigWithPaths ensures shadow home dirs and always-shared
// base config links (such as hooks) even when overlay sharing is disabled.
func MaterializeBaseConfigWithPaths(agent, shadowDir, sharedDir string, paths harnessenv.Paths) error {
	if shadowDir == "" {
		return nil
	}
	layout, supported := OverlayLayoutFor(agent)
	if !supported {
		return nil
	}
	if err := prepareAntigravityHome(agent, shadowDir, sharedDir); err != nil {
		return err
	}
	if layout.AuthFileOnly || len(layout.BaseLinks) == 0 {
		return nil
	}
	absShadow, err := filepath.Abs(shadowDir)
	if err != nil {
		return fmt.Errorf("profiles: resolve %s shadow home: %w", agent, err)
	}
	root := overlayRoot(layout, absShadow)
	absShared := ""
	if sharedDir != "" {
		absShared, err = filepath.Abs(sharedDir)
		if err != nil {
			return fmt.Errorf("profiles: resolve %s shared home: %w", agent, err)
		}
		if root == absShared {
			return fmt.Errorf("profiles: %s shadow home %s must be different from shared home %s", agent, root, absShared)
		}
	}
	if err := applySharedLinks(agent, layout.BaseLinks, root, absShared, paths); err != nil {
		return err
	}
	return verifyPrivateAuthFiles(agent, root, layout.AuthFiles)
}

func isEmptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

func resolveSharedLinkTarget(agent string, sl SharedLink, absShared, link string, paths harnessenv.Paths) (string, bool, error) {
	candidates := make([]string, 0, len(sl.SharedRels)*2+1)
	seen := map[string]bool{}
	add := func(cand string) {
		if cand == "" {
			return
		}
		clean := filepath.Clean(cand)
		if clean == link || seen[clean] {
			return
		}
		seen[clean] = true
		candidates = append(candidates, clean)
	}
	for _, rel := range sl.SharedRels {
		if absShared != "" {
			add(filepath.Join(absShared, rel))
		}
		if paths.Home != "" {
			if defShared := DefaultSharedHome(agent, paths); defShared != "" {
				add(filepath.Join(defShared, rel))
			}
		}
	}
	if sl.DotfilesRel != "" && paths.Home != "" {
		if root, ok := harnessenv.DotfilesRoot(paths); ok {
			add(filepath.Join(root, sl.DotfilesRel))
		}
	}
	for _, cand := range candidates {
		st, err := os.Stat(cand)
		if err != nil {
			continue
		}
		if sl.DirOnly && !st.IsDir() {
			continue
		}
		if fi, lerr := os.Lstat(cand); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			if resolved, rerr := filepath.EvalSymlinks(cand); rerr == nil {
				cand = resolved
			}
		}
		if abs, err := filepath.Abs(cand); err == nil {
			cand = abs
		}
		if cand == link {
			continue
		}
		return cand, st.IsDir(), nil
	}
	if sl.EnsureSharedDir && absShared != "" && len(sl.SharedRels) > 0 {
		target := filepath.Join(absShared, sl.SharedRels[0])
		if target == link {
			return "", false, nil
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return "", false, fmt.Errorf("profiles: create %s shared dir %s: %w", agent, target, err)
		}
		return target, true, nil
	}
	return "", false, nil
}

func applySharedLinks(agent string, links []SharedLink, root, absShared string, paths harnessenv.Paths) error {
	for _, sl := range links {
		link := filepath.Join(root, sl.ShadowRel)
		target, targetIsDir, err := resolveSharedLinkTarget(agent, sl, absShared, link, paths)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			return fmt.Errorf("profiles: create %s parent dir %s: %w", agent, filepath.Dir(link), err)
		}
		if fi, err := os.Lstat(link); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				cur, rerr := os.Readlink(link)
				if rerr != nil {
					return fmt.Errorf("profiles: read %s shared link %s: %w", agent, link, rerr)
				}
				resolvedCur := cur
				if !filepath.IsAbs(resolvedCur) {
					resolvedCur = filepath.Clean(filepath.Join(filepath.Dir(link), cur))
				}
				if resolvedCur == target {
					continue
				}
				if rerr := os.Remove(link); rerr != nil {
					return fmt.Errorf("profiles: replace %s shared link %s: %w", agent, link, rerr)
				}
			} else if targetIsDir && fi.IsDir() && isEmptyDir(link) {
				if rerr := os.Remove(link); rerr != nil {
					return fmt.Errorf("profiles: replace empty %s dir %s: %w", agent, link, rerr)
				}
			} else if !targetIsDir && fi.Mode().IsRegular() && fi.Size() == 0 {
				if rerr := os.Remove(link); rerr != nil {
					return fmt.Errorf("profiles: replace empty %s stub %s: %w", agent, link, rerr)
				}
			} else {
				// Preserve non-empty per-profile data rather than clobbering it.
				continue
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("profiles: stat %s shared link %s: %w", agent, link, err)
		}
		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("profiles: symlink %s shared entry %s: %w", agent, link, err)
		}
	}
	return nil
}

func verifyPrivateAuthFiles(agent, root string, authFiles []string) error {
	for _, authName := range authFiles {
		authPath := filepath.Join(root, authName)
		if fi, err := os.Lstat(authPath); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("profiles: %s shadow %s must be a real file, not a symlink", agent, authName)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("profiles: stat %s shadow %s: %w", agent, authName, err)
		}
	}
	return nil
}

// MaterializeCodexShadowHome materializes a Codex auth-overlay shadow home.
func MaterializeCodexShadowHome(shadowDir, sharedDir string) error {
	return MaterializeShadowHome("codex", shadowDir, sharedDir)
}

// MaterializeForLaunch prepares shared MCP/skills/chat overlays and checks
// Antigravity credential paths even when sharing is disabled.
func MaterializeForLaunch(r Resolution, paths harnessenv.Paths) error {
	p := r.Profile
	if p.Agent == "antigravity" {
		if _, err := agents.EnsureRestoreEnvScript(paths); err != nil {
			return err
		}
	}
	if !SupportsOverlay(p.Agent) {
		return nil
	}
	sharedHome := p.HomePath
	if sharedHome == "" || sharedHome == DefaultSharedConfigHome(p.Agent, paths) {
		sharedHome = DefaultSharedHome(p.Agent, paths)
	}
	spec := LaunchSpecFromPaths(r, paths)
	shadow := spec.ShadowHome
	if shadow == "" && p.Agent == "antigravity" {
		shadow = spec.HomePath
		if shadow == "" && !spec.NoRouter {
			shadow = antigravityRouterShadowHome(p.Agent, paths)
		}
	}
	if shadow == "" && p.Agent == "grok" && spec.HomePath == "" && !spec.NoRouter {
		shadow = filepath.Join(harnessenv.HarnessDir(paths), "grok", "model-router")
	}
	if shadow != "" && overlayEnabled(p) {
		return MaterializeShadowHomeWithPaths(p.Agent, shadow, sharedHome, paths)
	}
	return MaterializeBaseConfigWithPaths(p.Agent, shadow, sharedHome, paths)
}
