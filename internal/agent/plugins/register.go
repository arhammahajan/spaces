// Package plugins provides the built-in coding-agent plugins for tmux-spaces.
//
// Importing this package (typically as a blank import) registers all built-in
// agent plugins into agent.DefaultRegistry(). Callers that want an isolated
// registry (such as unit tests) can call RegisterAll(agent.NewRegistry()).
package plugins

import "github.com/arhammahajan/spaces/internal/agent"

func init() {
	RegisterAll(agent.DefaultRegistry())
}

// All returns freshly constructed instances of every built-in agent plugin.
func All() []agent.Plugin {
	list := []agent.Plugin{
		NewAntigravity(),
		NewClaude(),
		NewCodex(),
		NewCursor(),
		NewDeepSeek(),
		NewKilo(),
		NewKimi(),
		NewKimiCLI(),
		NewMuse(),
		NewOpenCode(),
	}
	list = append(list, harnessManagedPlugins()...)
	list = append(list, otherRuntimePlugins()...)
	list = append(list, worktreeOnlyPlugins()...)
	for _, p := range list {
		AttachProviderTelemetry(p)
	}
	return list
}

// RegisterAll registers every built-in agent plugin into r.
func RegisterAll(r *agent.Registry) {
	for _, p := range All() {
		r.Register(p)
	}
}
