package agents

// AccessLevel describes one sandbox/approval option an agent CLI supports,
// presented by the harness CLI as a launch-time choice. Args prepend the
// agent's command line; Env layers over the launch environment.
type AccessLevel struct {
	ID    string
	Label string
	Desc  string
	Args  []string
	Env   map[string]string
}

// AccessLevels returns the sandbox/access levels the canonical agent
// supports, ordered by increasing power with the "default" (unmodified)
// level somewhere in the list. The result is empty when the agent has no
// known sandbox or approval flags: callers skip the picker entirely.
//
// Every flag below was verified against the installed CLI's --help output;
// unknown flags would make the agent fail visibly at launch.
func AccessLevels(agent string) []AccessLevel {
	levels, ok := accessLevels[agent]
	if !ok {
		return nil
	}
	return levels
}

// FindAccessLevel returns the level with the given ID (case-insensitive).
func FindAccessLevel(agent, id string) (AccessLevel, bool) {
	for _, level := range AccessLevels(agent) {
		if level.ID == id {
			return level, true
		}
	}
	return AccessLevel{}, false
}

// accessLevels is the per-agent table. The "default" level carries no
// flags and always exists for level-capable agents.
var accessLevels = map[string][]AccessLevel{
	"claude": {
		{ID: "read-only", Label: "Plan", Desc: "Analyze only; no edits or commands", Args: []string{"--permission-mode", "plan"}},
		{ID: "default", Label: "Default", Desc: "Ask for each permission"},
		{ID: "auto", Label: "Auto edits", Desc: "Automatically accept file edits", Args: []string{"--permission-mode", "acceptEdits"}},
		{ID: "full", Label: "Full access", Desc: "Bypass all permission checks", Args: []string{"--dangerously-skip-permissions"}},
	},
	"codex": {
		{ID: "read-only", Label: "Read-only", Desc: "Sandbox denies all writes", Args: []string{"--sandbox", "read-only"}},
		{ID: "default", Label: "Default", Desc: "Configured sandbox and approvals"},
		{ID: "auto", Label: "Full auto", Desc: "Workspace-write sandbox, approve after failures", Args: []string{"--sandbox", "workspace-write", "--ask-for-approval", "on-failure"}},
		{ID: "full", Label: "Full access", Desc: "No sandbox, no approval prompts", Args: []string{"--dangerously-bypass-approvals-and-sandbox"}},
	},
	"grok": {
		{ID: "default", Label: "Default", Desc: "Ask before risky tool executions"},
		{ID: "full", Label: "Full access", Desc: "Auto-approve all tool executions", Args: []string{"--always-approve"}},
	},
	"opencode": {
		{ID: "default", Label: "Default", Desc: "Ask for permissions"},
		{ID: "auto", Label: "Auto", Desc: "Auto-approve permissions that are not explicitly denied", Args: []string{"--auto"}},
	},
	"copilot": {
		{ID: "default", Label: "Default", Desc: "Configured permissions"},
		{ID: "full", Label: "Full access", Desc: "All tools, paths and URLs allowed", Args: []string{"--allow-all"}},
	},
	"kimi": {
		{ID: "default", Label: "Default", Desc: "Ask before edits and commands"},
		{ID: "auto", Label: "Ask when needed", Desc: "Routine edits and commands run automatically", Args: []string{"--yolo"}},
		{ID: "full", Label: "Never ask", Desc: "Everything runs automatically, plans still wait", Args: []string{"--auto"}},
	},
	"kimi-cli": {
		{ID: "default", Label: "Default", Desc: "Ask for approvals"},
		{ID: "full", Label: "Full access", Desc: "Automatically approve everything", Args: []string{"--yolo"}},
	},
	"qwen": {
		{ID: "read-only", Label: "Plan", Desc: "Analyze only; no edits or commands", Args: []string{"--approval-mode", "plan"}},
		{ID: "default", Label: "Default", Desc: "Require approval for edits and commands"},
		{ID: "auto", Label: "Auto", Desc: "Auto-approve safe actions, block risky ones", Args: []string{"--approval-mode", "auto"}},
		{ID: "full", Label: "Full access", Desc: "Automatically approve all tools", Args: []string{"--approval-mode", "yolo"}},
	},
	"gemini": {
		{ID: "read-only", Label: "Plan", Desc: "Read-only mode", Args: []string{"--approval-mode", "plan"}},
		{ID: "default", Label: "Default", Desc: "Prompt for approval"},
		{ID: "auto", Label: "Auto edits", Desc: "Auto-approve edit tools", Args: []string{"--approval-mode", "auto_edit"}},
		{ID: "full", Label: "Full access", Desc: "Auto-approve all tools", Args: []string{"--approval-mode", "yolo"}},
	},
	"muse": {
		{ID: "untrusted", Label: "Untrusted", Desc: "Require approval for untrusted actions", Args: []string{"--approval-mode", "untrusted"}},
		{ID: "default", Label: "On request", Desc: "Ask for tool approvals on request"},
		{ID: "full", Label: "Never ask", Desc: "No tool approval prompts", Args: []string{"--approval-mode", "never"}},
	},
	"kilo": {
		{ID: "default", Label: "Default", Desc: "Ask for permissions"},
		{ID: "auto", Label: "Auto", Desc: "Auto-approve permissions that are not explicitly denied", Args: []string{"--auto"}},
	},
	"antigravity": {
		{ID: "read-only", Label: "Plan", Desc: "Analyze only; no edits or commands", Args: []string{"--mode", "plan"}},
		{ID: "sandboxed", Label: "Sandboxed", Desc: "Run with terminal restrictions enabled", Args: []string{"--sandbox"}},
		{ID: "default", Label: "Default", Desc: "Configured approvals"},
		{ID: "accept-edits", Label: "Auto edits", Desc: "Automatically accept edit actions", Args: []string{"--mode", "accept-edits"}},
		{ID: "auto", Label: "Auto", Desc: "Automatic agent execution mode", Args: []string{"--mode", "auto"}},
		{ID: "full", Label: "Full access", Desc: "Auto-approve all tool permission requests", Args: []string{"--dangerously-skip-permissions"}},
	},
	"cline": {
		{ID: "read-only", Label: "Plan", Desc: "Run in plan mode", Args: []string{"--plan"}},
		{ID: "ask", Label: "Ask for all", Desc: "Ask before every tool execution", Args: []string{"--auto-approve", "false"}},
		{ID: "default", Label: "Default", Desc: "Act mode with the configured approvals"},
		{ID: "auto", Label: "Yolo", Desc: "Auto-approve a limited tool set for unsupervised runs", Args: []string{"--yolo"}},
	},
	"fx": {
		{ID: "ask", Label: "Ask", Desc: "Ask before restricted actions", Args: []string{"--ask"}},
		{ID: "default", Label: "Default", Desc: "Configured permission mode"},
		{ID: "auto", Label: "Auto", Desc: "Automatically approve safe actions", Args: []string{"--auto"}},
		{ID: "full", Label: "Full access", Desc: "No permission prompts", Args: []string{"--full-access"}},
	},
}
