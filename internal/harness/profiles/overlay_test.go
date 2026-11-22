package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSharedCodexHome(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readLinkTarget(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", path)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestDefaultCodexSharedHome(t *testing.T) {
	paths := testPaths(t)
	if got := DefaultCodexSharedHome(paths); got != filepath.Join(paths.Home, ".codex") {
		t.Fatalf("DefaultCodexSharedHome = %q", got)
	}
}

func TestMaterializeBasic(t *testing.T) {
	tmp := t.TempDir()
	shared := filepath.Join(tmp, "shared")
	shadow := filepath.Join(tmp, "shadow")
	writeSharedCodexHome(t, shared, map[string]string{
		"auth.json":         `{"tok":1}`,
		"models_cache.json": `{}`,
		"config.toml":       "x",
		"sessions/a.json":   "a",
		"log/x":             "skip-me",
		"memories/m":        "skip-me",
		"tmp/f":             "skip-me",
		".DS_Store":         "skip-me",
	})
	if err := MaterializeCodexShadowHome(shadow, shared); err != nil {
		t.Fatal(err)
	}
	// Shared entries symlinked.
	for _, name := range []string{"config.toml", "sessions"} {
		if got := readLinkTarget(t, filepath.Join(shadow, name)); got != filepath.Join(shared, name) {
			t.Errorf("link %s -> %q, want shared", name, got)
		}
	}
	// Private and shadow-local entries never symlinked.
	for _, name := range []string{"auth.json", "models_cache.json", "log", "memories", "tmp", ".DS_Store"} {
		if _, err := os.Lstat(filepath.Join(shadow, name)); !os.IsNotExist(err) {
			t.Errorf("shadow must not contain %s (err=%v)", name, err)
		}
	}
	// Idempotent second run.
	if err := MaterializeCodexShadowHome(shadow, shared); err != nil {
		t.Fatalf("second materialize = %v", err)
	}
}

func TestMaterializeMissingShared(t *testing.T) {
	tmp := t.TempDir()
	shadow := filepath.Join(tmp, "shadow")
	if err := MaterializeCodexShadowHome(shadow, filepath.Join(tmp, "nope")); err != nil {
		t.Fatalf("missing shared must be tolerated, got %v", err)
	}
	if st, err := os.Stat(shadow); err != nil || !st.IsDir() {
		t.Fatalf("shadow must still be created: %v", err)
	}
	if err := MaterializeCodexShadowHome(shadow, ""); err != nil {
		t.Fatalf("empty shared must be tolerated, got %v", err)
	}
}

func TestMaterializeStaleSymlinkReplaced(t *testing.T) {
	tmp := t.TempDir()
	shared := filepath.Join(tmp, "shared")
	shadow := filepath.Join(tmp, "shadow")
	writeSharedCodexHome(t, shared, map[string]string{"config.toml": "x"})
	if err := os.MkdirAll(shadow, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "elsewhere"), filepath.Join(shadow, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeCodexShadowHome(shadow, shared); err != nil {
		t.Fatal(err)
	}
	if got := readLinkTarget(t, filepath.Join(shadow, "config.toml")); got != filepath.Join(shared, "config.toml") {
		t.Fatalf("stale link not replaced: %q", got)
	}
}

func TestMaterializeConflictErrors(t *testing.T) {
	tmp := t.TempDir()
	shared := filepath.Join(tmp, "shared")
	shadow := filepath.Join(tmp, "shadow")
	writeSharedCodexHome(t, shared, map[string]string{"config.toml": "x"})
	if err := os.MkdirAll(shadow, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file at the link location must never be clobbered.
	if err := os.WriteFile(filepath.Join(shadow, "config.toml"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeCodexShadowHome(shadow, shared); err == nil {
		t.Fatal("real-file conflict must error")
	}
	// A symlinked auth.json must be refused.
	shadow2 := filepath.Join(tmp, "shadow2")
	if err := os.MkdirAll(shadow2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "auth.json"), filepath.Join(shadow2, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeCodexShadowHome(shadow2, shared); err == nil {
		t.Fatal("symlinked auth.json must error")
	}
}

func TestCreateCodexOverlay(t *testing.T) {
	t.Run("shared overlay enabled by default", func(t *testing.T) {
		paths := testPaths(t)
		shared := t.TempDir()
		writeSharedCodexHome(t, shared, map[string]string{"skills/s.md": "s", "config.toml": "x", "sessions/c.json": "{}"})
		s, _ := Open(paths)
		if err := s.Create("cx", Profile{Agent: "codex", Enabled: true, HomePath: shared},
			CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		np, _ := s.Get("cx")
		if np.Profile.Overlay == nil || !*np.Profile.Overlay {
			t.Fatalf("default create must enable overlay, got %v", np.Profile.Overlay)
		}
		for _, name := range []string{"skills", "config.toml", "sessions"} {
			if got := readLinkTarget(t, filepath.Join(np.Profile.ShadowHome, name)); got != filepath.Join(shared, name) {
				t.Fatalf("default overlay link %s -> %q, want %q", name, got, filepath.Join(shared, name))
			}
		}
	})
	t.Run("explicit overlay opt-out", func(t *testing.T) {
		paths := testPaths(t)
		shared := t.TempDir()
		writeSharedCodexHome(t, shared, map[string]string{"skills/s.md": "s", "config.toml": "x"})
		off := false
		s, _ := Open(paths)
		if err := s.Create("cxoff", Profile{Agent: "codex", Enabled: true, Router: true, HomePath: shared, Overlay: &off},
			CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		np, _ := s.Get("cxoff")
		for _, name := range []string{"skills", "config.toml"} {
			if _, err := os.Lstat(filepath.Join(np.Profile.ShadowHome, name)); !os.IsNotExist(err) {
				t.Fatalf("overlay=false must not overlay %s (err=%v)", name, err)
			}
		}
	})
	t.Run("invalid name still rejected", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		if err := s.Create("bad name", Profile{Agent: "pi", Enabled: true, Router: true},
			CreateOptions{NoSeed: true}); err == nil {
			t.Fatal("space name must still be rejected")
		}
	})
}

func TestAllKnownAgentsSupportOverlay(t *testing.T) {
	for _, agent := range KnownAgents() {
		if !SupportsOverlay(agent) {
			t.Errorf("known agent %q is missing an OverlayLayout", agent)
		}
	}
}

func TestMultiAgentShadowOverlayAndPrivateAuth(t *testing.T) {
	cases := []struct {
		agent        string
		sharedBase   string // "home" or "data"
		sharedDirRel string
		shadowSubdir string
		sharedConv   string
		authFile     string
		shadowLocal  string
		authFileOnly bool
	}{
		{agent: "codex", sharedDirRel: ".codex", sharedConv: "sessions", authFile: "auth.json", shadowLocal: "tmp"},
		{agent: "claude", sharedDirRel: ".claude", sharedConv: "projects", authFile: ".credentials.json", shadowLocal: "telemetry"},
		{agent: "grok", sharedDirRel: ".grok", sharedConv: "sessions", authFile: "auth.json", shadowLocal: "logs"},
		{agent: "antigravity", sharedDirRel: ".gemini", shadowSubdir: ".gemini", sharedConv: "history", authFile: "oauth_creds.json", shadowLocal: "tmp"},
		{agent: "gemini", sharedDirRel: ".gemini", shadowSubdir: ".gemini", sharedConv: "history", authFile: "oauth_creds.json", shadowLocal: "tmp"},
		{agent: "copilot", sharedDirRel: ".copilot", sharedConv: "session-state", authFile: "config.json", shadowLocal: "logs"},
		{agent: "deepseek", sharedDirRel: ".dsh", sharedConv: "sessions", authFile: ".credentials.yaml", shadowLocal: ".anonymous-user-id"},
		{agent: "cline", sharedDirRel: filepath.Join(".cline", "data"), sharedConv: "sessions", authFile: "auth.json", shadowLocal: "logs"},
		{agent: "opencode", sharedBase: "data", sharedDirRel: "opencode", shadowSubdir: "opencode", sharedConv: "repos", authFile: "auth.json", shadowLocal: "log"},
		{agent: "kilo", sharedBase: "data", sharedDirRel: "kilo", shadowSubdir: "kilo", sharedConv: "storage", authFile: "auth.json", shadowLocal: "log"},
		{agent: "pi", sharedDirRel: filepath.Join(".pi", "agent"), sharedConv: "sessions", authFile: "auth.json", shadowLocal: "tmp"},
		{agent: "qwen", sharedDirRel: ".qwen", sharedConv: "sessions", authFile: "oauth_creds.json", shadowLocal: "tmp"},
		{agent: "kimi", sharedDirRel: ".kimi-code", sharedConv: "sessions", authFile: "auth.json", shadowLocal: "logs"},
		{agent: "kimi-cli", sharedDirRel: ".kimi", sharedConv: "sessions", authFile: "auth.json", shadowLocal: "logs"},
		{agent: "mcode", sharedDirRel: ".minimax", sharedConv: "v2", authFile: "auth.json", shadowLocal: "cache"},
		{agent: "muse", authFile: "auth.json", authFileOnly: true},
		{agent: "fx", sharedDirRel: ".fx", sharedConv: "sessions", authFile: "auth.json", shadowLocal: "diagnostics"},
		{agent: "vibe", sharedDirRel: ".vibe", sharedConv: "logs", authFile: ".env", shadowLocal: "cache"},
	}

	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			paths := testPaths(t)
			var shared string
			if !tc.authFileOnly {
				base := paths.Home
				if tc.sharedBase == "data" {
					base = paths.DataHome
				}
				shared = filepath.Join(base, tc.sharedDirRel)
				writeSharedCodexHome(t, shared, map[string]string{
					tc.authFile:              `{"secret":"shared-token"}`,
					tc.sharedConv + "/c.txt": "chat-data",
					tc.shadowLocal + "/x":    "local-only",
				})
			}
			s, err := Open(paths)
			if err != nil {
				t.Fatal(err)
			}
			on := true
			if err := s.Create("work", Profile{Agent: tc.agent, Enabled: true, Router: false, Overlay: &on}, CreateOptions{NoSeed: true}); err != nil {
				t.Fatalf("Create(%s) = %v", tc.agent, err)
			}
			np, ok := s.Get("work")
			if !ok {
				t.Fatal("profile missing")
			}
			// Shadow home must be owner-only (0700).
			st, err := os.Stat(np.Profile.ShadowHome)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o700 {
				t.Errorf("shadow home mode = %o, want 0700", st.Mode().Perm())
			}
			root := np.Profile.ShadowHome
			if tc.shadowSubdir != "" {
				root = filepath.Join(root, tc.shadowSubdir)
			}
			if !tc.authFileOnly {
				// Conversation/chat store must be symlinked to shared home.
				if got := readLinkTarget(t, filepath.Join(root, tc.sharedConv)); got != filepath.Join(shared, tc.sharedConv) {
					t.Errorf("%s link -> %q, want %q", tc.sharedConv, got, filepath.Join(shared, tc.sharedConv))
				}
				// Auth store must NOT be symlinked from shared home.
				if _, err := os.Lstat(filepath.Join(root, tc.authFile)); !os.IsNotExist(err) {
					t.Errorf("auth file %s must not be symlinked into shadow home (err=%v)", tc.authFile, err)
				}
				// Shadow-local entry must NOT be symlinked.
				if _, err := os.Lstat(filepath.Join(root, tc.shadowLocal)); !os.IsNotExist(err) {
					t.Errorf("shadow-local %s must not be symlinked (err=%v)", tc.shadowLocal, err)
				}
			}
			// AuthStoreStatus reports not-logged-in until a real auth file exists.
			authPath, present, supported := AuthStoreStatus(np.Profile, paths)
			if !supported || present {
				t.Errorf("AuthStoreStatus before login = (%q, %v, %v), want present=false", authPath, present, supported)
			}
			if err := os.WriteFile(filepath.Join(root, tc.authFile), []byte(`{"token":"profile-own"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			authPath, present, supported = AuthStoreStatus(np.Profile, paths)
			if !supported || !present || authPath != filepath.Join(root, tc.authFile) {
				t.Errorf("AuthStoreStatus after login = (%q, %v, %v), want present=true", authPath, present, supported)
			}
			// MaterializeForLaunch must succeed with real auth file.
			res, err := Resolve("work", paths)
			if err != nil {
				t.Fatal(err)
			}
			if err := MaterializeForLaunch(res, paths); err != nil {
				t.Fatalf("MaterializeForLaunch with real auth = %v", err)
			}
		})
	}
}

func TestMaterializeShadowHomeGuards(t *testing.T) {
	t.Run("same shadow and shared rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := MaterializeShadowHome("codex", dir, dir); err == nil {
			t.Fatal("shadow == shared must error")
		}
	})

	t.Run("replaceable runtime entry swapped for symlink", func(t *testing.T) {
		tmp := t.TempDir()
		shared := filepath.Join(tmp, "shared")
		shadow := filepath.Join(tmp, "shadow")
		if err := os.MkdirAll(filepath.Join(shadow, "mcp-oauth-locks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := MaterializeShadowHome("codex", shadow, shared); err != nil {
			t.Fatalf("replaceable runtime lock must succeed: %v", err)
		}
		if got := readLinkTarget(t, filepath.Join(shadow, "mcp-oauth-locks")); got != filepath.Join(shared, "mcp-oauth-locks") {
			t.Fatalf("mcp-oauth-locks link = %q", got)
		}
	})

	t.Run("stale private cache symlink removed", func(t *testing.T) {
		tmp := t.TempDir()
		shared := filepath.Join(tmp, "shared")
		shadow := filepath.Join(tmp, "shadow")
		if err := os.MkdirAll(shadow, 0o700); err != nil {
			t.Fatal(err)
		}
		stale := filepath.Join(shadow, "models_cache.json")
		if err := os.Symlink(filepath.Join(shared, "models_cache.json"), stale); err != nil {
			t.Fatal(err)
		}
		if err := MaterializeShadowHome("codex", shadow, shared); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale models_cache.json symlink must be removed (err=%v)", err)
		}
	})
}

func TestAntigravityLaunchDetachesLegacyCredentialOverlay(t *testing.T) {
	paths := testPaths(t)
	shared := filepath.Join(paths.Home, ".gemini")
	globalAuth := filepath.Join(shared, "antigravity-cli", "antigravity-oauth-token")
	if err := os.MkdirAll(filepath.Dir(globalAuth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalAuth, []byte("global account"), 0o600); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(t.TempDir(), "profile")
	gemini := filepath.Join(shadow, ".gemini")
	if err := os.MkdirAll(gemini, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(gemini, "antigravity-cli")
	if err := os.Symlink(filepath.Dir(globalAuth), legacy); err != nil {
		t.Fatal(err)
	}
	res := Resolution{Profile: Profile{Agent: "antigravity", ShadowHome: shadow}}
	if err := MaterializeForLaunch(res, paths); err != nil {
		t.Fatal(err)
	}
	res.Profile.Overlay = boolptr(true)
	if err := MaterializeForLaunch(res, paths); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "antigravity-oauth-token"), []byte("profile account"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(globalAuth)
	if err != nil || string(data) != "global account" {
		t.Fatalf("global token changed: %v", err)
	}
}

func TestAntigravitySharedSkillsAcrossProfiles(t *testing.T) {
	t.Run("store profile with overlay off links resolved shared skills", func(t *testing.T) {
		paths := testPaths(t)
		dotsSkills := filepath.Join(paths.Home, "dev", "dots", "agents", ".agents", "skills")
		if err := os.MkdirAll(filepath.Join(dotsSkills, "my-skill"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Mirror setup.sh: ~/.gemini/config/skills -> ~/dev/dots/harnesses/shared/skills
		realCfg := filepath.Join(paths.Home, ".gemini", "config")
		if err := os.MkdirAll(realCfg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dotsSkills, filepath.Join(realCfg, "skills")); err != nil {
			t.Fatal(err)
		}

		s, err := Open(paths)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create("pratham", Profile{Agent: "antigravity", Enabled: true, Router: false}, CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		np, ok := s.Get("antigravity/pratham")
		if !ok {
			t.Fatal("profile missing")
		}
		skillsLink := filepath.Join(np.Profile.ShadowHome, ".gemini", "config", "skills")
		if got := readLinkTarget(t, skillsLink); got != dotsSkills {
			t.Fatalf("skills link on create = %q, want %q", got, dotsSkills)
		}
		// .gemini/config itself must remain a real directory, never a symlink.
		if fi, err := os.Lstat(filepath.Dir(skillsLink)); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			t.Fatalf(".gemini/config must be a real dir, got fi=%v err=%v", fi, err)
		}

		// Launch materialization keeps link idempotent and replaces stale symlinks.
		if err := os.Remove(skillsLink); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "old"), skillsLink); err != nil {
			t.Fatal(err)
		}
		res, err := Resolve("antigravity/pratham", paths)
		if err != nil {
			t.Fatal(err)
		}
		if err := MaterializeForLaunch(res, paths); err != nil {
			t.Fatal(err)
		}
		if got := readLinkTarget(t, skillsLink); got != dotsSkills {
			t.Fatalf("stale skills link = %q, want %q", got, dotsSkills)
		}
	})

	t.Run("falls back to dotfiles shared skills when ~/.gemini/config/skills is absent", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_DOTFILES", "")
		dotsSkills := filepath.Join(paths.Home, "dev", "dots", "agents", ".agents", "skills")
		if err := os.MkdirAll(filepath.Join(dotsSkills, "fallback-skill"), 0o755); err != nil {
			t.Fatal(err)
		}
		s, err := Open(paths)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create("work", Profile{Agent: "antigravity", Enabled: true, Router: false}, CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		res, err := Resolve("antigravity/work", paths)
		if err != nil {
			t.Fatal(err)
		}
		if err := MaterializeForLaunch(res, paths); err != nil {
			t.Fatal(err)
		}
		skillsLink := filepath.Join(res.Profile.ShadowHome, ".gemini", "config", "skills")
		if got := readLinkTarget(t, skillsLink); got != dotsSkills {
			t.Fatalf("fallback skills link = %q, want %q", got, dotsSkills)
		}
	})

	t.Run("router default and native antigravity profiles receive skills", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_DOTFILES", "")
		dotsSkills := filepath.Join(paths.Home, "dev", "dots", "agents", ".agents", "skills")
		if err := os.MkdirAll(dotsSkills, 0o755); err != nil {
			t.Fatal(err)
		}
		// Router-mode default profile materializes into antigravity-router-cli.
		if err := MaterializeForLaunch(DefaultResolution("antigravity"), paths); err != nil {
			t.Fatal(err)
		}
		routerSkills := filepath.Join(paths.Home, ".harness", "antigravity-router-cli", ".gemini", "config", "skills")
		if got := readLinkTarget(t, routerSkills); got != dotsSkills {
			t.Fatalf("router default skills link = %q, want %q", got, dotsSkills)
		}
	})

	t.Run("preserves user-managed real skills directory", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_DOTFILES", "")
		dotsSkills := filepath.Join(paths.Home, "dev", "dots", "agents", ".agents", "skills")
		if err := os.MkdirAll(dotsSkills, 0o755); err != nil {
			t.Fatal(err)
		}
		shadow := filepath.Join(t.TempDir(), "custom-home")
		localSkills := filepath.Join(shadow, ".gemini", "config", "skills")
		if err := os.MkdirAll(filepath.Join(localSkills, "local-only"), 0o755); err != nil {
			t.Fatal(err)
		}
		res := Resolution{Profile: Profile{Agent: "antigravity", ShadowHome: shadow}}
		if err := MaterializeForLaunch(res, paths); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(localSkills)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			t.Fatalf("real non-empty skills dir must be preserved, got fi=%v err=%v", fi, err)
		}
	})

	t.Run("shares antigravity mcps and chats while keeping token private and replacing 0-byte stub", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_DOTFILES", "")
		dotsMCP := filepath.Join(paths.Home, "dev", "dots", "antigravity", ".gemini", "config", "mcp_config.json")
		if err := os.MkdirAll(filepath.Dir(dotsMCP), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dotsMCP, []byte(`{"mcpServers":{"agent-browser":{}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		dotsHooks := filepath.Join(paths.Home, "dev", "dots", "antigravity", ".gemini", "config", "hooks.json")
		if err := os.WriteFile(dotsHooks, []byte(`{"spaces":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		sharedGemini := filepath.Join(paths.Home, ".gemini")
		writeSharedCodexHome(t, sharedGemini, map[string]string{
			"antigravity-cli/antigravity-oauth-token": "shared-secret",
			"antigravity-cli/conversations/c1.db":     "chat-1",
			"antigravity-cli/brain/c1/note.md":        "brain-1",
			"antigravity-cli/history.jsonl":           "{\"msg\":1}\n",
		})

		s, err := Open(paths)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create("p1", Profile{Agent: "antigravity", Enabled: true, Router: false}, CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		np, _ := s.Get("antigravity/p1")
		shadowGemini := filepath.Join(np.Profile.ShadowHome, ".gemini")

		// MCP and hooks configs linked from dotfiles fallback.
		mcpLink := filepath.Join(shadowGemini, "config", "mcp_config.json")
		if got := readLinkTarget(t, mcpLink); got != dotsMCP {
			t.Fatalf("mcp_config.json link = %q, want %q", got, dotsMCP)
		}
		hooksLink := filepath.Join(shadowGemini, "config", "hooks.json")
		if got := readLinkTarget(t, hooksLink); got != dotsHooks {
			t.Fatalf("hooks.json link = %q, want %q", got, dotsHooks)
		}
		// 0-byte stub created by agy is replaced cleanly on launch.
		if err := os.Remove(mcpLink); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mcpLink, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		res, _ := Resolve("antigravity/p1", paths)
		if err := MaterializeForLaunch(res, paths); err != nil {
			t.Fatal(err)
		}
		if got := readLinkTarget(t, mcpLink); got != dotsMCP {
			t.Fatalf("0-byte stub not replaced: %q", got)
		}

		// Chats shared inside real antigravity-cli directory, token never symlinked.
		for _, rel := range []string{
			"antigravity-cli/conversations",
			"antigravity-cli/brain",
			"antigravity-cli/annotations",
			"antigravity-cli/implicit",
			"antigravity-cli/history.jsonl",
		} {
			if got := readLinkTarget(t, filepath.Join(shadowGemini, rel)); got != filepath.Join(sharedGemini, rel) {
				t.Errorf("%s link = %q, want %q", rel, got, filepath.Join(sharedGemini, rel))
			}
		}
		if _, err := os.Lstat(filepath.Join(shadowGemini, "antigravity-cli", "antigravity-oauth-token")); !os.IsNotExist(err) {
			t.Fatalf("antigravity-oauth-token must never be symlinked (err=%v)", err)
		}
		if fi, err := os.Lstat(filepath.Join(shadowGemini, "antigravity-cli")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("antigravity-cli dir itself must remain real (fi=%v, err=%v)", fi, err)
		}
	})
}

func TestAllHarnessesShareMCPsSkillsChatsByDefault(t *testing.T) {
	t.Run("cline shares MCPs and chats while isolating providers.json", func(t *testing.T) {
		paths := testPaths(t)
		shared := filepath.Join(paths.Home, ".cline", "data")
		writeSharedCodexHome(t, shared, map[string]string{
			"sessions/s1.json":                 "{}",
			"db/state.db":                      "db",
			"settings/cline_mcp_settings.json": `{"mcpServers":{}}`,
			"settings/providers.json":          `{"secret":"key"}`,
		})
		s, _ := Open(paths)
		if err := s.Create("c1", Profile{Agent: "cline", Enabled: true, Router: false}, CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		np, _ := s.Get("cline/c1")
		if got := readLinkTarget(t, filepath.Join(np.Profile.ShadowHome, "sessions")); got != filepath.Join(shared, "sessions") {
			t.Errorf("cline sessions -> %q", got)
		}
		if got := readLinkTarget(t, filepath.Join(np.Profile.ShadowHome, "settings", "cline_mcp_settings.json")); got != filepath.Join(shared, "settings", "cline_mcp_settings.json") {
			t.Errorf("cline mcp -> %q", got)
		}
		if _, err := os.Lstat(filepath.Join(np.Profile.ShadowHome, "settings", "providers.json")); !os.IsNotExist(err) {
			t.Errorf("cline providers.json must remain private (err=%v)", err)
		}
	})

	t.Run("mcode shares mcps skills chats while isolating auth dir", func(t *testing.T) {
		paths := testPaths(t)
		shared := filepath.Join(paths.Home, ".minimax")
		writeSharedCodexHome(t, shared, map[string]string{
			"auth/prod/token":   "secret",
			"mcp.json":          `{"mcpServers":{}}`,
			"skills/s/SKILL.md": "---\nname: s\n---",
			"v2/chat.json":      "{}",
		})
		s, _ := Open(paths)
		if err := s.Create("m1", Profile{Agent: "mcode", Enabled: true, Router: false}, CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		np, _ := s.Get("mcode/m1")
		for _, name := range []string{"mcp.json", "skills", "v2"} {
			if got := readLinkTarget(t, filepath.Join(np.Profile.ShadowHome, name)); got != filepath.Join(shared, name) {
				t.Errorf("mcode %s -> %q", name, got)
			}
		}
		if _, err := os.Lstat(filepath.Join(np.Profile.ShadowHome, "auth")); !os.IsNotExist(err) {
			t.Errorf("mcode auth/ directory must never be symlinked (err=%v)", err)
		}
	})

	t.Run("gemini shares skills mcps chats without pulling in antigravity-cli", func(t *testing.T) {
		paths := testPaths(t)
		shared := filepath.Join(paths.Home, ".gemini")
		writeSharedCodexHome(t, shared, map[string]string{
			"antigravity-cli/antigravity-oauth-token": "agy-secret",
			"oauth_creds.json":                        "gem-secret",
			"settings.json":                           `{"mcpServers":{}}`,
			"skills/s/SKILL.md":                       "skill",
			"history/h1.json":                         "{}",
		})
		s, _ := Open(paths)
		if err := s.Create("g1", Profile{Agent: "gemini", Enabled: true, Router: false}, CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		np, _ := s.Get("gemini/g1")
		root := filepath.Join(np.Profile.ShadowHome, ".gemini")
		for _, name := range []string{"settings.json", "skills", "history"} {
			if got := readLinkTarget(t, filepath.Join(root, name)); got != filepath.Join(shared, name) {
				t.Errorf("gemini %s -> %q", name, got)
			}
		}
		for _, private := range []string{"oauth_creds.json", "antigravity-cli"} {
			if _, err := os.Lstat(filepath.Join(root, private)); !os.IsNotExist(err) {
				t.Errorf("gemini must not symlink %s (err=%v)", private, err)
			}
		}
	})

	t.Run("claude codex grok copilot pi qwen kimi kimi-cli fx vibe share mcps skills and chats by default", func(t *testing.T) {
		cases := []struct {
			agent  string
			rel    string
			shared []string
			auth   string
		}{
			{agent: "claude", rel: ".claude", shared: []string{"projects", "sessions", "skills", "settings.json"}, auth: ".credentials.json"},
			{agent: "codex", rel: ".codex", shared: []string{"sessions", "skills", "config.toml"}, auth: "auth.json"},
			{agent: "grok", rel: ".grok", shared: []string{"sessions", "skills", "config.toml"}, auth: "auth.json"},
			{agent: "copilot", rel: ".copilot", shared: []string{"session-state", "mcp-config.json", "settings.json"}, auth: "config.json"},
			{agent: "pi", rel: filepath.Join(".pi", "agent"), shared: []string{"sessions", "mcp.json", "settings.json"}, auth: "auth.json"},
			{agent: "qwen", rel: ".qwen", shared: []string{"sessions", "skills", "settings.json"}, auth: "oauth_creds.json"},
			{agent: "kimi", rel: ".kimi-code", shared: []string{"sessions", "skills", "mcp.json"}, auth: "auth.json"},
			{agent: "kimi-cli", rel: ".kimi", shared: []string{"sessions", "skills", "mcp.json"}, auth: "credentials"},
			{agent: "fx", rel: ".fx", shared: []string{"sessions", "skills", "mcp.json"}, auth: "auth.json"},
			{agent: "vibe", rel: ".vibe", shared: []string{"logs", "skills", "config.toml"}, auth: ".env"},
		}
		for _, tc := range cases {
			t.Run(tc.agent, func(t *testing.T) {
				paths := testPaths(t)
				shared := filepath.Join(paths.Home, tc.rel)
				files := map[string]string{tc.auth: "secret"}
				for _, entry := range tc.shared {
					if filepath.Ext(entry) != "" {
						files[entry] = "cfg"
					} else {
						files[filepath.Join(entry, "item")] = "data"
					}
				}
				writeSharedCodexHome(t, shared, files)
				s, _ := Open(paths)
				if err := s.Create("work", Profile{Agent: tc.agent, Enabled: true, Router: false}, CreateOptions{NoSeed: true}); err != nil {
					t.Fatalf("Create(%s): %v", tc.agent, err)
				}
				np, _ := s.Get(tc.agent + "/work")
				for _, entry := range tc.shared {
					if got := readLinkTarget(t, filepath.Join(np.Profile.ShadowHome, entry)); got != filepath.Join(shared, entry) {
						t.Errorf("%s %s -> %q, want %q", tc.agent, entry, got, filepath.Join(shared, entry))
					}
				}
				if _, err := os.Lstat(filepath.Join(np.Profile.ShadowHome, tc.auth)); !os.IsNotExist(err) {
					t.Errorf("%s %s must not be symlinked", tc.agent, tc.auth)
				}
			})
		}
	})
}

func TestHooksAlwaysSharedWithoutOverlay(t *testing.T) {
	noOverlay := false

	t.Run("antigravity links hooks.json even when --no-overlay", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_DOTFILES", "")
		dotsHooks := filepath.Join(paths.Home, "dev", "dots", "antigravity", ".gemini", "config", "hooks.json")
		if err := os.MkdirAll(filepath.Dir(dotsHooks), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dotsHooks, []byte(`{"hooks":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		sharedGemini := filepath.Join(paths.Home, ".gemini")
		writeSharedCodexHome(t, sharedGemini, map[string]string{
			"antigravity-cli/conversations/c1.pb": "chat",
		})
		s, _ := Open(paths)
		if err := s.Create("isolated", Profile{Agent: "antigravity", Enabled: true, Overlay: &noOverlay}, CreateOptions{NoSeed: true}); err != nil {
			t.Fatal(err)
		}
		res, err := Resolve("antigravity/isolated", paths)
		if err != nil {
			t.Fatal(err)
		}
		if err := MaterializeForLaunch(res, paths); err != nil {
			t.Fatal(err)
		}
		shadowGemini := filepath.Join(res.Profile.ShadowHome, ".gemini")
		if got := readLinkTarget(t, filepath.Join(shadowGemini, "config", "hooks.json")); got != dotsHooks {
			t.Errorf("hooks.json link = %q, want %q", got, dotsHooks)
		}
		if _, err := os.Lstat(filepath.Join(shadowGemini, "antigravity-cli", "conversations")); !os.IsNotExist(err) {
			t.Errorf("conversations must not be linked when --no-overlay (err=%v)", err)
		}
	})

	t.Run("codex claude grok link hooks even when --no-overlay", func(t *testing.T) {
		cases := []struct {
			agent   string
			rel     string
			hookRel string
			chatRel string
			isFile  bool
		}{
			{agent: "codex", rel: ".codex", hookRel: "hooks.json", chatRel: "sessions", isFile: true},
			{agent: "claude", rel: ".claude", hookRel: "hooks", chatRel: "projects", isFile: false},
			{agent: "grok", rel: ".grok", hookRel: "hooks", chatRel: "sessions", isFile: false},
		}
		for _, tc := range cases {
			t.Run(tc.agent, func(t *testing.T) {
				paths := testPaths(t)
				shared := filepath.Join(paths.Home, tc.rel)
				files := map[string]string{
					filepath.Join(tc.chatRel, "item"): "chat",
				}
				if tc.isFile {
					files[tc.hookRel] = `{"hooks":[]}`
				} else {
					files[filepath.Join(tc.hookRel, "spaces.json")] = `{"hooks":[]}`
				}
				writeSharedCodexHome(t, shared, files)
				s, _ := Open(paths)
				if err := s.Create("isolated", Profile{Agent: tc.agent, Enabled: true, Overlay: &noOverlay}, CreateOptions{NoSeed: true}); err != nil {
					t.Fatal(err)
				}
				np, _ := s.Get(tc.agent + "/isolated")
				if got := readLinkTarget(t, filepath.Join(np.Profile.ShadowHome, tc.hookRel)); got != filepath.Join(shared, tc.hookRel) {
					t.Errorf("%s %s -> %q, want %q", tc.agent, tc.hookRel, got, filepath.Join(shared, tc.hookRel))
				}
				if _, err := os.Lstat(filepath.Join(np.Profile.ShadowHome, tc.chatRel)); !os.IsNotExist(err) {
					t.Errorf("%s %s must not be linked when --no-overlay (err=%v)", tc.agent, tc.chatRel, err)
				}
			})
		}
	})
}
