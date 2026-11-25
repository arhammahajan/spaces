package agent

// CompanionSpec describes a rich harness companion plugin installed in spaces.
type CompanionSpec struct {
	AgentID      string
	PluginName   string
	Version      string
	InstallDir   string
	Description  string
	Capabilities []string
	Runtime      string
}

// CompanionStatus reports install/active state for a companion plugin.
type CompanionStatus struct {
	Installed bool
	Path      string
	Version   string
	Active    bool
	Detail    string
}

// CompanionInstallOptions configures companion install/uninstall.
type CompanionInstallOptions struct {
	HomeDir    string
	InstallDir string
	Version    string
	Force      bool
}

// CompanionInstallResult reports the outcome of an install.
type CompanionInstallResult struct {
	Path    string
	Version string
	Updated bool
}

// CompanionPlugin is an optional agent capability for rich harness companion
// plugins that stream v2 telemetry to the spaces listener. It is deliberately
// dependency-free from internal/hooks/core: normalized events are plain
// map[string]any so cmd/tmux-spaces can wire hookcore.CompanionNormalizer
// without import cycles.
type CompanionPlugin interface {
	CompanionSpec() CompanionSpec
	NormalizeCompanionEvent(envelope map[string]any) (map[string]any, bool)
	DetectCompanionStatus(homeDir string) CompanionStatus
	InstallCompanion(opts CompanionInstallOptions) (CompanionInstallResult, error)
	UninstallCompanion(opts CompanionInstallOptions) error
}
