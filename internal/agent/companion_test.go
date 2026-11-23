package agent

import (
	"context"
	"testing"
)

type fakeCompanion struct {
	spec CompanionSpec
}

func (f fakeCompanion) CompanionSpec() CompanionSpec { return f.spec }
func (f fakeCompanion) NormalizeCompanionEvent(envelope map[string]any) (map[string]any, bool) {
	agent, _ := envelope["agent"].(string)
	if agent == "" {
		return nil, false
	}
	return map[string]any{
		"agent": agent, "app": agent, "source": "plugin",
		"event": "working", "state": "working", "text": "ok",
	}, true
}
func (f fakeCompanion) DetectCompanionStatus(homeDir string) CompanionStatus {
	return CompanionStatus{Installed: homeDir != "", Path: homeDir + "/plugin", Version: "1.0.0", Active: true}
}
func (f fakeCompanion) InstallCompanion(opts CompanionInstallOptions) (CompanionInstallResult, error) {
	return CompanionInstallResult{Path: opts.InstallDir, Version: "1.0.0", Updated: true}, nil
}
func (f fakeCompanion) UninstallCompanion(opts CompanionInstallOptions) error { return nil }

func TestCompanionPluginCapability(t *testing.T) {
	// Default BasePlugin has no companion.
	base := &BasePlugin{PluginSpec: Spec{ID: "test-agent"}}
	if base.CompanionPlugin() != nil {
		t.Errorf("default CompanionPlugin should be nil")
	}

	reg := NewRegistry()
	reg.Register(&BasePlugin{
		PluginSpec: Spec{ID: "opencode"},
		CustomCompanionPlugin: fakeCompanion{spec: CompanionSpec{
			AgentID: "opencode", PluginName: "opencode-spaces",
			Version: "1.0.0", Capabilities: []string{"lifecycle", "usage"},
			Runtime: "node",
		}},
	})
	reg.Register(&BasePlugin{PluginSpec: Spec{ID: "pi"}})

	cp := reg.CompanionPlugin("opencode")
	if cp == nil {
		t.Fatal("CompanionPlugin(opencode) = nil, want non-nil")
	}
	if reg.CompanionPlugin("pi") != nil {
		t.Errorf("CompanionPlugin(pi) should be nil")
	}
	if reg.CompanionPlugin("unknown") != nil {
		t.Errorf("CompanionPlugin(unknown) should be nil")
	}
	plugins := reg.CompanionPlugins()
	if len(plugins) != 1 || plugins[0].Spec().ID != "opencode" {
		t.Errorf("CompanionPlugins = %v, want [opencode]", plugins)
	}

	spec := reg.CompanionPlugin("opencode").CompanionSpec()
	if spec.PluginName != "opencode-spaces" || spec.AgentID != "opencode" {
		t.Errorf("spec = %+v", spec)
	}
	norm, good := reg.CompanionPlugin("opencode").NormalizeCompanionEvent(map[string]any{"agent": "opencode"})
	if !good || norm["source"] != "plugin" {
		t.Errorf("NormalizeCompanionEvent = (%v, %v)", norm, good)
	}
	st := reg.CompanionPlugin("opencode").DetectCompanionStatus("/home/u")
	if !st.Installed || st.Version != "1.0.0" {
		t.Errorf("status = %+v", st)
	}
	res, err := reg.CompanionPlugin("opencode").InstallCompanion(CompanionInstallOptions{InstallDir: "/tmp/x"})
	if err != nil || res.Path != "/tmp/x" {
		t.Errorf("install = (%+v, %v)", res, err)
	}
	if err := reg.CompanionPlugin("opencode").UninstallCompanion(CompanionInstallOptions{}); err != nil {
		t.Errorf("uninstall = %v", err)
	}
	_ = context.Background
}
