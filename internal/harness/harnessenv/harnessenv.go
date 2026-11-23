// Package harnessenv is the shared runtime-environment kernel for the
// harness CLI. It resolves XDG paths, the shared model-router endpoint
// settings (the ai/model-router.env contract), dotfiles discovery, and the
// launch-spec type shared by the router, profile, and agent packages.
//
// All functions that touch the user's home read their locations from the
// process environment through Paths, so tests can point every lookup at a
// temporary directory with t.Setenv.
package harnessenv

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Paths holds the resolved home and XDG base directories used by every
// harness CLI subsystem.
type Paths struct {
	Home       string
	ConfigHome string // XDG_CONFIG_HOME
	DataHome   string // XDG_DATA_HOME
	StateHome  string // XDG_STATE_HOME
	CacheHome  string // XDG_CACHE_HOME
}

// RealHomeDir unwraps a harness shadow profile HOME
// (`<realHome>/.harness/profiles/<agent>/<profile>/home` or any path under
// `<realHome>/.harness/...`) back to `<realHome>`. When home is empty, it
// reads $HOME and falls back to os.UserHomeDir().
func RealHomeDir(home string) string {
	h := strings.TrimSpace(home)
	if h == "" {
		h = strings.TrimSpace(os.Getenv("HOME"))
	}
	if h == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			h = dir
		}
	}
	marker := string(filepath.Separator) + ".harness" + string(filepath.Separator)
	if idx := strings.Index(h, marker); idx > 0 {
		return h[:idx]
	}
	return h
}

// DefaultPaths resolves the XDG base directories from the process
// environment with the standard fallbacks. Relative XDG values are ignored
// in favor of the home-based defaults, matching the common systemd
// convention. If HOME (or an XDG_* var) points inside a harness shadow home
// under `<realHome>/.harness/...`, it is unwrapped back to `<realHome>` so
// invoking `spaces` from inside a shadow HOME always finds real `~/.harness`.
func DefaultPaths() Paths {
	rawHome := strings.TrimSpace(os.Getenv("HOME"))
	home := RealHomeDir(rawHome)
	marker := string(filepath.Separator) + ".harness" + string(filepath.Separator)
	pick := func(env, fallback string) string {
		if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
			if !strings.Contains(v, marker) {
				return v
			}
		}
		return fallback
	}
	return Paths{
		Home:       home,
		ConfigHome: pick("XDG_CONFIG_HOME", filepath.Join(home, ".config")),
		DataHome:   pick("XDG_DATA_HOME", filepath.Join(home, ".local", "share")),
		StateHome:  pick("XDG_STATE_HOME", filepath.Join(home, ".local", "state")),
		CacheHome:  pick("XDG_CACHE_HOME", filepath.Join(home, ".cache")),
	}
}

// HarnessDir returns the harness/model-router home directory where
// profiles, shadow homes, templates, and credentials live: $HOME/.harness.
func HarnessDir(paths Paths) string {
	return filepath.Join(paths.Home, ".harness")
}

// RouterSettings is the resolved model-router endpoint used by launching
// agents and by router status commands.
type RouterSettings struct {
	URL    string // no trailing slash
	APIKey string
}

// RouterEnvFile is the shared env file each harness maps into its own
// provider variables, relative to ConfigHome.
func RouterEnvFile(paths Paths) string {
	return filepath.Join(paths.ConfigHome, "ai", "model-router.env")
}

// RouterEnv resolves the router endpoint. Pre-existing process values win
// over the shared env file, which wins over the documented defaults. This
// mirrors the launcher contract of the superseded harness shell script.
func RouterEnv(paths Paths) RouterSettings {
	url := os.Getenv("MODEL_ROUTER_URL")
	key := os.Getenv("MODEL_ROUTER_API_KEY")
	if data, err := os.ReadFile(RouterEnvFile(paths)); err == nil {
		fileEnv := ParseEnvFile(string(data), func(name string) (string, bool) {
			if v, ok := os.LookupEnv(name); ok {
				return v, true
			}
			return "", false
		})
		if url == "" {
			url = fileEnv["MODEL_ROUTER_URL"]
		}
		if key == "" {
			key = fileEnv["MODEL_ROUTER_API_KEY"]
		}
	}
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		url = "http://127.0.0.1:4000"
	}
	if key == "" {
		key = "unused"
	}
	return RouterSettings{URL: url, APIKey: key}
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnvFile parses the small export-file dialect used by
// ai/model-router.env: optional `export `, NAME=VALUE, where VALUE is a
// single-quoted literal, a double-quoted string with ${VAR} and
// ${VAR:-default} expansion, or a bare unquoted token. Blank lines and
// comments are skipped; anything else is ignored. lookup supplies values for
// ${VAR} expansion; file values accumulate so later lines can reference
// earlier ones.
func ParseEnvFile(data string, lookup func(name string) (string, bool)) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:eq])
		if !envName.MatchString(name) {
			continue
		}
		out[name] = expandValue(strings.TrimSpace(line[eq+1:]), func(v string) (string, bool) {
			if got, ok := out[v]; ok {
				return got, true
			}
			if lookup != nil {
				return lookup(v)
			}
			return "", false
		})
	}
	return out
}

// expandValue evaluates one env-file value: single quotes are literal,
// double quotes and bare tokens expand ${VAR} and ${VAR:-default}.
func expandValue(value string, lookup func(string) (string, bool)) string {
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
		return value[1 : len(value)-1]
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
		value = value[1 : len(value)-1]
	}
	return expandSubstitutions(value, lookup)
}

var substRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

func expandSubstitutions(value string, lookup func(string) (string, bool)) string {
	return substRe.ReplaceAllStringFunc(value, func(match string) string {
		parts := substRe.FindStringSubmatch(match)
		name, fallback := parts[1], parts[2]
		if got, ok := lookup(name); ok && got != "" {
			return got
		}
		if len(parts) > 2 { // ${VAR:-default}
			return fallback
		}
		return ""
	})
}

// NewRouterSession returns a fresh per-launch router session token of the
// form router-session-<uuid4>, matching the affinity contract the router
// accepts from launching harnesses.
func NewRouterSession() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal in practice; fall back to a
		// clearly-marked non-random session rather than failing launch.
		return fmt.Sprintf("router-session-weak-%s", hex.EncodeToString(b[:]))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return "router-session-" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ExpandHome expands leading "~" / "~/" and "${HOME}" / "$HOME" references
// in value using home. When home is empty, value is returned unchanged.
func ExpandHome(value, home string) string {
	if value == "" || home == "" {
		return value
	}
	if strings.Contains(value, "${HOME}") {
		value = strings.ReplaceAll(value, "${HOME}", home)
	}
	if value == "$HOME" {
		return home
	}
	if strings.HasPrefix(value, "$HOME/") || strings.HasPrefix(value, "$HOME"+string(filepath.Separator)) {
		value = filepath.Join(home, value[len("$HOME")+1:])
	}
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	return value
}

// DotfilesRoot returns the dotfiles checkout used for profile seeding and
// config wiring: $HARNESS_DOTFILES when set, otherwise <home>/dev/dots.
// The bool reports whether the directory exists.
func DotfilesRoot(paths Paths) (string, bool) {
	if root := ExpandHome(os.Getenv("HARNESS_DOTFILES"), paths.Home); root != "" {
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			return root, true
		}
		return root, false
	}
	root := filepath.Join(paths.Home, "dev", "dots")
	if st, err := os.Stat(root); err == nil && st.IsDir() {
		return root, true
	}
	return root, false
}

// LaunchSpec is the resolved launch instruction for one agent invocation.
// The agents package merges agent defaults with the spec: spec Env wins
// over agent defaults, PrependArgs run before caller args, and a non-empty
// Binary replaces the agent's default executable.
type LaunchSpec struct {
	Agent       string
	AccessLevel string
	DisplayName string
	Env         map[string]string
	PrependArgs []string
	Binary      string
	HomePath    string
	ShadowHome  string
	NoRouter    bool
}
