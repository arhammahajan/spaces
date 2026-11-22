package profiles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

const (
	antigravityCLIToken = "antigravity-cli/antigravity-oauth-token"
)

func prepareAntigravityHome(agent, shadow, shared string) error {
	if shadow == "" || agent != "antigravity" {
		return nil
	}
	if err := rejectAntigravityLink(shadow); err != nil {
		return err
	}
	geminiDir := filepath.Join(shadow, ".gemini")
	if err := rejectAntigravityLink(geminiDir); err != nil {
		return err
	}

	// Old overlays linked entire application and config directories. Detach
	// links we can identify as overlay links, leaving their shared targets alone.
	for _, name := range []string{"antigravity-cli", "antigravity-acp", "antigravity", "jetski", "config"} {
		link := filepath.Join(geminiDir, name)
		target, err := os.Readlink(link)
		if err == nil && shared != "" {
			if !filepath.IsAbs(target) {
				target = filepath.Join(geminiDir, target)
			}
			sharedTarget, err := filepath.Abs(filepath.Join(shared, name))
			if err != nil {
				return err
			}
			if filepath.Clean(target) == sharedTarget {
				if err := os.Remove(link); err != nil {
					return fmt.Errorf("profiles: detach Antigravity overlay link: %w", err)
				}
			}
		}
		if err := rejectAntigravityLink(link); err != nil {
			return err
		}
	}

	for _, name := range []string{
		filepath.Join(".gemini", antigravityCLIToken),
		".config", ".config/antigravity", ".config/antigravity/credential-user-scope",
		".local", ".local/share", ".local/state", ".cache",
	} {
		if err := rejectAntigravityLink(filepath.Join(shadow, name)); err != nil {
			return err
		}
	}
	return nil
}

func rejectAntigravityLink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("profiles: inspect Antigravity state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("profiles: Antigravity state must stay in the profile home: %s is a symlink", path)
	}
	return nil
}

func antigravityRouterShadowHome(agent string, paths harnessenv.Paths) string {
	if agent == "antigravity" {
		return filepath.Join(harnessenv.HarnessDir(paths), "antigravity-router-cli")
	}
	return ""
}
