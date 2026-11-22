package agents

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// profilePathAugmentation lists the profile-mode PATH entries from the
// contract. They apply to profile launches only, never to bare agent
// launches; see ResolveProfile.
func profilePathAugmentation(paths harnessenv.Paths) []string {
	return []string{
		filepath.Join(paths.DataHome, "fnm"),
		filepath.Join(paths.DataHome, "fnm", "aliases", "default", "bin"),
		filepath.Join(paths.DataHome, "deepseek-harness", "bin"),
		filepath.Join(paths.DataHome, "pnpm", "bin"),
	}
}

// Resolve produces the exact process for spec plus callerArgs, applying the
// per-agent behavior table from the contract and then the spec layering:
// spec Env replaces agent defaults except for managed Antigravity home
// redirects, spec PrependArgs run before caller args, and a non-empty spec
// Binary overrides the default binary.
// UnsetEnv always includes HARNESS_AGENT. An empty spec Agent and unknown
// agents are usage errors.
//
// Resolve is the bare-launch path: it never adds the profile-mode PATH
// augmentation. Profile launches must use ResolveProfile.
func Resolve(spec harnessenv.LaunchSpec, paths harnessenv.Paths, router harnessenv.RouterSettings, callerArgs []string) (Resolved, error) {
	if spec.Agent == "" {
		return Resolved{}, errors.New("no agent specified")
	}
	agent, ok := Normalize(spec.Agent)
	if !ok {
		return Resolved{}, fmt.Errorf("unknown agent: %s", spec.Agent)
	}

	base := strings.TrimRight(router.URL, "/")
	var session string
	sess := func() string {
		if session == "" {
			session = harnessenv.NewRouterSession()
		}
		return session
	}

	env := map[string]string{}
	var agentArgs []string
	var pathPrepend []string

	defaultBin, derr := defaultBinary(agent, paths)
	if derr != nil && spec.Binary == "" {
		return Resolved{}, derr
	}

	// combined is the full argv tail the agent sees: spec prepends run
	// before caller args. Flag probes (opencode --standalone, deepseek's
	// default, antigravity --model) inspect it, matching the superseded
	// script where profile prepends were already in "$@" at that point.
	combined := append(append([]string{}, spec.PrependArgs...), callerArgs...)

	switch agent {
	case "claude":
		if !spec.NoRouter {
			env["ANTHROPIC_BASE_URL"] = base
			env["ANTHROPIC_AUTH_TOKEN"] = router.APIKey
		}
	case "codex":
		if !spec.NoRouter {
			agentArgs = []string{
				"-c", `model_provider="model_router"`,
				"-c", `model="gpt-6-luna"`,
				"-c", `service_tier="default"`,
				"-c", `model_providers.model_router.name="Model Router"`,
				"-c", fmt.Sprintf(`model_providers.model_router.base_url=%q`, base+"/v1"),
				"-c", `model_providers.model_router.env_key="MODEL_ROUTER_API_KEY"`,
				"-c", `model_providers.model_router.wire_api="responses"`,
				"-c", `model_providers.model_router.requires_openai_auth=false`,
				"-c", `model_providers.model_router.supports_websockets=false`,
			}
		}
	case "grok":
		if !spec.NoRouter {
			env["GROK_HOME"] = filepath.Join(harnessenv.HarnessDir(paths), "grok", "model-router")
			env["GROK_MODELS_BASE_URL"] = base + "/v1"
			env["GROK_MODELS_LIST_URL"] = base + "/v1/models"
			env["XAI_API_KEY"] = router.APIKey
			env["GROK_MODEL_ROUTER_SESSION"] = sess()
		}
	case "cline":
		if !spec.NoRouter {
			agentArgs = []string{"--key", sess()}
		}
	case "opencode":
		if needsStandalone(combined) {
			agentArgs = []string{"--standalone"}
		}
	case "copilot":
		if !spec.NoRouter {
			env["COPILOT_PROVIDER_BASE_URL"] = base + "/v1"
			env["COPILOT_PROVIDER_API_KEY"] = router.APIKey
			env["COPILOT_PROVIDER_TYPE"] = "openai"
			env["COPILOT_PROVIDER_WIRE_API"] = "responses"
			env["COPILOT_PROVIDER_TRANSPORT"] = "http"
			if model := os.Getenv("COPILOT_MODEL"); model != "" {
				env["COPILOT_MODEL"] = model
			} else {
				env["COPILOT_MODEL"] = "gpt-6-luna"
			}
		}
	case "kimi-cli":
		if !spec.NoRouter {
			env["OPENAI_BASE_URL"] = base + "/v1"
			env["OPENAI_API_KEY"] = sess()
		}
	case "muse":
		if !spec.NoRouter {
			env["META_API_KEY"] = router.APIKey
		}
	case "fx":
		if !spec.NoRouter {
			env["FX_MODEL_ROUTER_SESSION"] = sess()
		}
	case "deepseek":
		if len(combined) == 0 {
			agentArgs = []string{"web"}
		}
		if _, err := exec.LookPath("node"); err != nil {
			pathPrepend = append(pathPrepend,
				filepath.Join(paths.DataHome, "fnm", "aliases", "default", "bin"))
		}
	case "vibe":
		if !spec.NoRouter {
			env["VIBE_ROUTER_SESSION"] = sess()
		}
	case "gemini":
		if !spec.NoRouter {
			env["GOOGLE_GEMINI_BASE_URL"] = base
			env["GEMINI_MODEL_ROUTER"] = "1"
			env["GEMINI_API_KEY"] = sess()
		}
		nodeBin := filepath.Join(paths.DataHome, "fnm", "aliases", "default", "bin", "node")
		if isExecutable(nodeBin) {
			pathPrepend = append(pathPrepend, filepath.Dir(nodeBin))
		}
		repo := os.Getenv("GEMINI_CLI_REPO")
		if repo == "" {
			repo = filepath.Join(paths.Home, "dev", "gemini-cli")
		}
		entry := filepath.Join(repo, "packages", "cli", "dist", "index.js")
		if !isExecutable(entry) && !isRegularFile(entry) {
			return Resolved{}, fmt.Errorf("build Gemini CLI in %s first (npm ci && npm run build)", repo)
		}
		agentArgs = []string{entry}
	case "antigravity":
		if !spec.NoRouter {
			env["GOOGLE_GEMINI_BASE_URL"] = base
			env["GEMINI_API_KEY"] = sess()
			if !hasFlag(combined, "--model") {
				agentArgs = []string{"--model", defaultAntigravityModel(paths.Home)}
			}
			if !hasFlag(combined, "--settings") && !hasFlag(combined, "-settings") {
				agentArgs = append(agentArgs, "--settings", `{"modelProvider":"gemini"}`)
			}
		}
	case "pi", "qwen", "kimi", "mcode", "kilo":
		// Plain exec; these agents configure their own providers.
	case "antigravity-native":
		// Plain exec of the profile-provided binary (native.sh).
	}

	binary := defaultBin
	if spec.Binary != "" {
		binary = spec.Binary
	}
	if binary == "" {
		return Resolved{}, fmt.Errorf("agent %s requires spec Binary (launch via a profile)", agent)
	}

	unset := []string{"HARNESS_AGENT"}
	addUnsets := func(keys ...string) {
		for _, k := range keys {
			if _, ok := env[k]; ok {
				continue
			}
			if _, ok := spec.Env[k]; ok {
				continue
			}
			unset = append(unset, k)
		}
	}
	switch agent {
	case "antigravity", "antigravity-native":
		addUnsets(
			"GOOGLE_API_KEY",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_CLOUD_PROJECT",
			"GEMINI_API_KEY",
		)
	}
	// When --no-router is set, scrub ambient router/API credential vars unless
	// explicitly supplied in spec.Env so the profile uses its own isolated auth store.
	if spec.NoRouter {
		switch agent {
		case "claude":
			addUnsets("ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY")
		case "codex":
			addUnsets("OPENAI_API_KEY", "OPENAI_BASE_URL")
		case "grok":
			addUnsets("XAI_API_KEY", "GROK_MODELS_BASE_URL", "GROK_MODELS_LIST_URL", "GROK_MODEL_ROUTER_SESSION")
		case "antigravity":
			addUnsets("GOOGLE_GEMINI_BASE_URL")
		case "gemini":
			addUnsets("GOOGLE_GEMINI_BASE_URL", "GEMINI_MODEL_ROUTER", "GEMINI_API_KEY")
		case "copilot":
			addUnsets("COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_WIRE_API", "COPILOT_PROVIDER_TRANSPORT")
		case "kimi-cli":
			addUnsets("OPENAI_BASE_URL", "OPENAI_API_KEY")
		case "muse":
			addUnsets("META_API_KEY")
		case "fx":
			addUnsets("FX_MODEL_ROUTER_SESSION")
		case "vibe":
			addUnsets("VIBE_ROUTER_SESSION")
		}
	}

	shadowHome := spec.ShadowHome
	if agent == "antigravity" {
		if shadowHome == "" {
			shadowHome = spec.HomePath
		}
		if shadowHome == "" && !spec.NoRouter {
			shadowHome = filepath.Join(harnessenv.HarnessDir(paths), "antigravity-router-cli")
		}
	} else if shadowHome != "" {
		for k, v := range ShadowEnvMap(agent, shadowHome) {
			env[k] = v
		}
	} else if k, v, ok := ShadowEnv(agent, spec.HomePath); ok {
		env[k] = v
	}

	for k, v := range spec.Env {
		env[k] = v
	}
	if shadowHome != "" && agent == "antigravity" {
		// Apply the credential-store boundary after overrides so the profile
		// cannot reconnect to a desktop keyring or another provider's home.
		for k, v := range ShadowEnvMap(agent, shadowHome) {
			env[k] = v
		}
		for _, name := range []string{"ANTIGRAVITY_APP_DATA_DIR", "ANTIGRAVITY_CONFIG_REL_DIR"} {
			delete(env, name)
			unset = append(unset, name)
		}
		applyRealEnvRestore(env, paths, spec.Env)
	}

	return Resolved{
		Binary:      binary,
		Args:        append(append(append([]string{}, agentArgs...), spec.PrependArgs...), callerArgs...),
		Env:         envList(env),
		PathPrepend: dedupeStrings(pathPrepend),
		UnsetEnv:    unset,
	}, nil
}

// ResolveProfile resolves a profile launch: Resolve plus the profile-mode
// PATH augmentation from the contract. The augmentation is appended after
// any agent-specific entries (matching the superseded script, where the
// profile branch widened PATH first and agent cases prepended after) and
// deduplicated.
func ResolveProfile(spec harnessenv.LaunchSpec, paths harnessenv.Paths, router harnessenv.RouterSettings, callerArgs []string) (Resolved, error) {
	r, err := Resolve(spec, paths, router, callerArgs)
	if err != nil {
		return r, err
	}
	r.PathPrepend = dedupeStrings(append(r.PathPrepend, profilePathAugmentation(paths)...))
	return r, nil
}

// safeToken matches tokens that need no quoting in Describe.
var safeToken = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Describe renders Binary+Args as a shell-quoted command line using shlex
// rules: bare tokens pass through, everything else is single-quoted with
// embedded quotes escaped as '"'"'.
func Describe(r Resolved) string {
	toks := append([]string{r.Binary}, r.Args...)
	quoted := make([]string, 0, len(toks))
	for _, t := range toks {
		if t != "" && safeToken.MatchString(t) {
			quoted = append(quoted, t)
		} else {
			quoted = append(quoted, "'"+strings.ReplaceAll(t, "'", `'"'"'`)+"'")
		}
	}
	return strings.Join(quoted, " ")
}

// Launch applies the resolved env, unsets, and PATH, then replaces the
// process via exec. It never returns on success and is never invoked in
// tests.
func Launch(r Resolved) error {
	for _, kv := range r.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	for _, name := range r.UnsetEnv {
		if err := os.Unsetenv(name); err != nil {
			return err
		}
	}
	if len(r.PathPrepend) > 0 {
		path := strings.Join(append(append([]string{}, r.PathPrepend...), os.Getenv("PATH")), ":")
		if err := os.Setenv("PATH", path); err != nil {
			return err
		}
	}
	bin := r.Binary
	if !strings.Contains(bin, "/") {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			return err
		}
		bin = resolved
	}
	return syscall.Exec(bin, append([]string{r.Binary}, r.Args...), os.Environ())
}

// isRegularFile reports whether path is an existing non-directory file.
func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// applyRealEnvRestore records the user's real HOME, XDG_*, and D-Bus session
// address in HARNESS_REAL_* and points BASH_ENV at the harness restore script.
// agy (a Go binary) ignores BASH_ENV and stays isolated in its shadow home,
// while non-interactive bash shells spawned by agy source BASH_ENV and regain
// access to authenticated user tools such as gh, git, and ssh.
func applyRealEnvRestore(env map[string]string, paths harnessenv.Paths, specEnv map[string]string) {
	if paths.Home != "" {
		env["HARNESS_REAL_HOME"] = paths.Home
	}
	if paths.ConfigHome != "" {
		env["HARNESS_REAL_XDG_CONFIG_HOME"] = paths.ConfigHome
	}
	if paths.DataHome != "" {
		env["HARNESS_REAL_XDG_DATA_HOME"] = paths.DataHome
	}
	if paths.StateHome != "" {
		env["HARNESS_REAL_XDG_STATE_HOME"] = paths.StateHome
	}
	if paths.CacheHome != "" {
		env["HARNESS_REAL_XDG_CACHE_HOME"] = paths.CacheHome
	}
	if dbus := realDBusAddress(); dbus != "" {
		env["HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS"] = dbus
	}
	restoreScript := RestoreEnvScriptPath(paths)
	prevBashEnv := specEnv["BASH_ENV"]
	if prevBashEnv == "" {
		prevBashEnv = os.Getenv("BASH_ENV")
	}
	if prevBashEnv != "" && prevBashEnv != restoreScript {
		env["HARNESS_PREV_BASH_ENV"] = prevBashEnv
	}
	env["BASH_ENV"] = restoreScript
}

func realDBusAddress() string {
	for _, key := range []string{"DBUS_SESSION_BUS_ADDRESS", "HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" && v != "disabled:" {
			return v
		}
	}
	return ""
}
