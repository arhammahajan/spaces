package client

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

// Normalize canonicalizes an executable or command name to a known agent,
// or returns "" when it is not one. Delegates to agent.DefaultRegistry() first.
func Normalize(name string) string {
	name = strings.ToLower(Basename(name))
	for _, ext := range []string{".js", ".mjs", ".cjs", ".py", ".exe", ".cmd", ".bat"} {
		if strings.HasSuffix(name, ext) {
			name = strings.TrimSuffix(name, ext)
			break
		}
	}
	if canon := agent.DefaultRegistry().CanonicalID(name); canon != "" {
		return canon
	}
	if canon, ok := Aliases[name]; ok {
		name = canon
	}
	if Agents[name] {
		return name
	}
	return ""
}

// InferAgent maps an executable path or command name to a known agent.
func InferAgent(command string) string {
	name := Basename(command)
	if normalized := Normalize(name); normalized != "" {
		return normalized
	}
	lowered := strings.ToLower(command)
	hints := [][2]string{
		{"claude", "claude"},
		{"codex", "codex"},
		{"opencode", "opencode"},
		{"kilo", "kilo"},
		{"gemini", "gemini"},
		{"qwen", "qwen"},
		{"copilot", "copilot"},
		{"kimi-cli", "kimi-cli"},
		{"kimi-code", "kimi"},
		{"kimi", "kimi"},
		{"minimax", "mcode"},
		{"mcode", "mcode"},
		{"muse", "muse"},
		{"mistral-vibe", "vibe"},
		{"vibe", "vibe"},
		{"cline", "cline"},
		{"cursor-agent", "cursor-agent"},
		{"grok", "grok"},
		{"crush", "crush"},
		{"antigravity", "antigravity"},
		{"deepseek", "deepseek"},
		{"dsh-tui", "dsh-tui"},
		{"dsh", "deepseek"},
		{"fx-router", "fx"},
		{"/fx", "fx"},
		{"omp", "omp"},
		{"/pi", "pi"},
	}
	for _, h := range hints {
		if strings.Contains(lowered, h[0]) {
			return h[1]
		}
	}
	return ""
}

// DetectShellScript recognizes an agent launched through a shell wrapper.
func DetectShellScript(argv []string) string {
	index := 1
	for index < len(argv) {
		token := argv[index]
		if token == "-c" || token == "-lc" || token == "-ic" || token == "-cl" {
			return ""
		}
		if strings.HasPrefix(token, "-") {
			index++
			continue
		}
		break
	}
	if index >= len(argv) {
		return ""
	}
	script := argv[index]
	name := Basename(script)
	if name == "zj" && index+1 < len(argv) && argv[index+1] == "__shim" {
		hold := index+2 < len(argv) && argv[index+2] == "--hold"
		binIndex := index + 3
		if hold {
			binIndex = index + 4
		}
		if binIndex < len(argv) {
			return InferAgent(argv[binIndex])
		}
		if index+2 < len(argv) {
			return InferAgent(argv[index+2])
		}
		return InferAgent(script)
	}
	if name == "harness" && index+1 < len(argv) {
		if argv[index+1] == "profile" && index+2 < len(argv) {
			return ProfileAgent(argv[index+2])
		}
		return Normalize(argv[index+1])
	}
	return InferAgent(script)
}

// DetectAgent finds the agent a command line belongs to, or "" for normal commands.
func DetectAgent(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	exe := Basename(argv[0])
	if Shells[exe] {
		return DetectShellScript(argv)
	}
	if exe == "harness" {
		if len(argv) >= 2 {
			if argv[1] == "spaces" && len(argv) >= 4 && argv[2] == "__shim-profile" {
				if a, _, ok := strings.Cut(argv[3], "/"); ok {
					return Normalize(a)
				}
				return ProfileAgent(argv[3])
			}
			if argv[1] == "profile" {
				if len(argv) >= 3 && !strings.HasPrefix(argv[2], "-") {
					return ProfileAgent(argv[2])
				}
				return ""
			}
			return Normalize(argv[1])
		}
		return ""
	}
	if exe == "zj" || exe == "spaces" || exe == "tmux-spaces" {
		if len(argv) >= 3 && argv[1] == "__shim-profile" {
			if a, _, ok := strings.Cut(argv[2], "/"); ok {
				return Normalize(a)
			}
			return ProfileAgent(argv[2])
		}
		if len(argv) >= 3 && argv[1] == "__shim" {
			offset := 2
			for offset < len(argv) {
				tok := argv[offset]
				if tok == "--hold" {
					offset++
					continue
				}
				if tok == "--profile" && offset+1 < len(argv) {
					offset += 2
					continue
				}
				if strings.HasPrefix(tok, "--profile=") {
					offset++
					continue
				}
				if tok == "--" {
					offset++
					break
				}
				break
			}
			if len(argv) > offset+1 {
				if a := InferAgent(argv[offset+1]); a != "" {
					return a
				}
			}
			if len(argv) > offset {
				return InferAgent(argv[offset])
			}
			return ""
		}
		if len(argv) >= 3 && argv[1] == "profile" && !strings.HasPrefix(argv[2], "-") {
			if a, _, ok := strings.Cut(argv[2], "/"); ok {
				return Normalize(a)
			}
			return ProfileAgent(argv[2])
		}
		if len(argv) >= 3 && argv[1] == "launch" {
			return Normalize(argv[2])
		}
		if len(argv) >= 2 {
			return Normalize(argv[1])
		}
		return ""
	}
	if exe == "env" || exe == "command" || exe == "exec" || exe == "nohup" || exe == "time" || exe == "sudo" {
		index := 1
		for index < len(argv) {
			token := argv[index]
			if strings.HasPrefix(token, "-") || (exe == "env" && strings.Contains(token, "=")) {
				index++
				continue
			}
			break
		}
		if index < len(argv) {
			return DetectAgent(argv[index:])
		}
		return ""
	}
	if exe == "node" || exe == "bun" || exe == "deno" {
		if len(argv) >= 2 {
			return InferAgent(argv[1])
		}
		return ""
	}
	if direct := InferAgent(argv[0]); direct != "" {
		return direct
	}
	for _, token := range argv[1:] {
		if strings.HasPrefix(token, "-") || !strings.Contains(token, "/") {
			continue
		}
		if _, err := os.Stat(token); err != nil {
			continue
		}
		if candidate := InferAgent(token); candidate != "" {
			return candidate
		}
	}
	return ""
}

// ProfileAgent resolves a harness profile instance to its agent in-process.
func ProfileAgent(instance string) string {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return ""
	}
	res, err := profiles.Resolve(instance, harnessenv.DefaultPaths())
	if err != nil {
		return ""
	}
	return Normalize(res.Profile.Agent)
}

// processAncestorCommands returns ancestor command lines, nearest first.
func processAncestorCommands() [][]string {
	commands := [][]string{}
	pid := os.Getpid()
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		fields, err := procStatFields(pid)
		if err != nil || len(fields) < 2 {
			break
		}
		var ppid int
		if _, err := splitSScan(fields[1], &ppid); err != nil {
			break
		}
		if raw, err := os.ReadFile(filepath.Join("/proc", itoa(ppid), "cmdline")); err == nil {
			var argv []string
			for _, part := range strings.Split(string(raw), "\x00") {
				if part != "" {
					argv = append(argv, part)
				}
			}
			if len(argv) > 0 {
				commands = append(commands, argv)
			}
		}
		pid = ppid
	}
	return commands
}

// ProcessAncestorCommands is the exported ancestor-walk used by seams/tests.
func ProcessAncestorCommands() [][]string { return processAncestorCommands() }

// IsNoninteractive reports whether args make an agent run one-shot/headless
// instead of interactive.
func IsNoninteractive(agentName string, args []string) bool {
	if agentName == "cline" {
		for _, arg := range args {
			if arg == "-i" || arg == "--tui" {
				return false
			}
		}
		return true
	}
	if agentName == "codex" || agentName == "opencode" || agentName == "crush" ||
		agentName == "mcode" || agentName == "muse" {
		leading := []string{}
		for _, arg := range args {
			if len(leading) >= 2 {
				break
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			leading = append(leading, arg)
		}
		for _, arg := range leading {
			if arg == "exec" || arg == "run" {
				return true
			}
		}
		return false
	}
	if agentName == "fx" || agentName == "fx-router" {
		leading := []string{}
		for _, arg := range args {
			if len(leading) >= 2 {
				break
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			leading = append(leading, arg)
		}
		for _, arg := range leading {
			if arg == "ask" || arg == "session" || arg == "sessions" {
				return true
			}
		}
		return false
	}
	printFlags := map[string][]string{
		"claude": {"-p", "--print"}, "pi": {"-p", "--print"},
		"qwen": {"-p", "--prompt"}, "copilot": {"-p", "--prompt"},
		"gemini": {"-p", "--prompt"}, "vibe": {"-p", "--prompt"},
		"grok": {"-p", "--single"}, "omp": {"-p", "--print"},
		"kimi": {"-p", "--prompt"}, "kimi-cli": {"-p", "--prompt"},
		"antigravity": {"-p", "--prompt"}, "deepseek": {"-p", "--prompt"},
		"dsh-tui": {"-p", "--prompt"},
	}
	for _, flag := range printFlags[agentName] {
		for _, arg := range args {
			if arg == flag {
				return true
			}
		}
	}
	return false
}

// AgentIsNoninteractive reports whether the nearest agent ancestor is a
// non-interactive run.
func AgentIsNoninteractive(agentName string) bool {
	candidates := AncestorsFunc()
	var agentCommands [][]string
	for _, argv := range candidates {
		if DetectAgent(argv) == agentName {
			agentCommands = append(agentCommands, argv)
		}
	}
	if len(agentCommands) == 0 {
		return false
	}
	var rest []string
	if len(agentCommands[0]) > 1 {
		rest = agentCommands[0][1:]
	}
	return IsNoninteractive(agentName, rest)
}

// DiscoverProfile infers a harness profile name for an existing conversation ID
// when the recorded outcome omitted it. Supports Antigravity profile shadow
// homes under ~/.harness/profiles/antigravity/<profile>/home.
func DiscoverProfile(agentName, cwd, sessionID string) string {
	_ = cwd
	if Normalize(agentName) != "antigravity" {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	home := homeDir()
	// If already executing inside a profile shadow home, extract its profile name.
	marker := filepath.Join(".harness", "profiles", "antigravity") + string(filepath.Separator)
	if idx := strings.Index(home, marker); idx >= 0 {
		rest := home[idx+len(marker):]
		if prof, _, ok := strings.Cut(rest, string(filepath.Separator)); ok && prof != "" {
			brainDir := filepath.Join(home, ".gemini", "antigravity-cli", "brain", sessionID)
			if fi, err := os.Stat(brainDir); err == nil && fi.IsDir() {
				return prof
			}
		}
	}
	profilesRoot := filepath.Join(harnessenv.RealHomeDir(home), ".harness", "profiles", "antigravity")
	entries, err := os.ReadDir(profilesRoot)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		brainRoot := filepath.Join(profilesRoot, e.Name(), "home", ".gemini", "antigravity-cli", "brain")
		// When brain/ is symlinked to the shared home across profiles, every
		// profile sees the same conversation directories, so brain/ cannot
		// distinguish which profile owns the conversation.
		if lfi, err := os.Lstat(brainRoot); err != nil || lfi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		brainDir := filepath.Join(brainRoot, sessionID)
		if fi, err := os.Stat(brainDir); err == nil && fi.IsDir() {
			return e.Name()
		}
	}
	return ""
}
