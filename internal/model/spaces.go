package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ExecFunc runs a helper binary and returns its stdout.
type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecFn is the injectable process runner used by the Fetch helpers.
// Tests may stub it; it defaults to exec.CommandContext output.
var ExecFn ExecFunc = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// SpacesBinary resolves the helper argv prefix for `spaces` space helpers:
// $SPACES_BIN when set, else "zj" from PATH, else "harness spaces".
func SpacesBinary() []string {
	if bin := strings.TrimSpace(os.Getenv("SPACES_BIN")); bin != "" {
		if fields := strings.Fields(bin); len(fields) > 0 {
			return fields
		}
	}
	if _, err := exec.LookPath("spaces"); err == nil {
		return []string{"spaces"}
	}
	return []string{"harness", "spaces"}
}

// runSpaces invokes `zj <args...>` (or the SpacesBinary fallback) and returns stdout.
func runSpaces(ctx context.Context, args ...string) ([]byte, error) {
	bin := SpacesBinary()
	name := bin[0]
	full := make([]string, 0, len(bin)-1+len(args))
	full = append(full, bin[1:]...)
	full = append(full, args...)
	out, err := ExecFn(ctx, name, full...)
	if err != nil {
		return nil, fmt.Errorf("model: %s %s: %w", name, strings.Join(full, " "), err)
	}
	return out, nil
}

func decodeJSON(out []byte, cmd string, v any) error {
	if len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("model: %s: empty output", cmd)
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("model: %s: decode: %w", cmd, err)
	}
	return nil
}

// FetchHooks runs `spaces __space-hooks` and parses the HookSnapshot.
func FetchHooks(ctx context.Context) (HookSnapshot, error) {
	var snap HookSnapshot
	out, err := runSpaces(ctx, "__space-hooks")
	if err != nil {
		return HookSnapshot{}, err
	}
	if err := decodeJSON(out, "__space-hooks", &snap); err != nil {
		return HookSnapshot{}, err
	}
	return snap, nil
}

// FetchHookDetail runs `spaces __space-hooks --detail <session> <paneNum>` and
// parses the HookDetailSnapshot.
func FetchHookDetail(ctx context.Context, session string, paneNum int) (HookDetailSnapshot, error) {
	var snap HookDetailSnapshot
	out, err := runSpaces(ctx, "__space-hooks", "--detail", session, strconv.Itoa(paneNum))
	if err != nil {
		return HookDetailSnapshot{}, err
	}
	if err := decodeJSON(out, "__space-hooks --detail", &snap); err != nil {
		return HookDetailSnapshot{}, err
	}
	return snap, nil
}

// HarnessesPath resolves the path to ~/.model-router/harnesses.json.
// If TMUX_SPACES_HARNESSES_FILE is set, it overrides the default location.
// If HOME points into a shadow profile under ~/.model-router/ and harnesses.json
// does not exist there, it falls back to the real home prefix before /.model-router/.
func HarnessesPath(homeDir string) string {
	home := strings.TrimSpace(homeDir)
	if home == "" {
		home = strings.TrimSpace(os.Getenv("HOME"))
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if override := strings.TrimSpace(os.Getenv("TMUX_SPACES_HARNESSES_FILE")); override != "" {
		return expandHomePath(override, home)
	}
	candidate := filepath.Join(home, ".model-router", "harnesses.json")
	if isRegularFile(candidate) {
		return candidate
	}
	marker := string(filepath.Separator) + ".model-router" + string(filepath.Separator)
	if idx := strings.Index(home, marker); idx > 0 {
		realHome := home[:idx]
		alt := filepath.Join(realHome, ".model-router", "harnesses.json")
		if isRegularFile(alt) {
			return alt
		}
	}
	return candidate
}

func isRegularFile(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// LoadHarnessesFile reads and parses a harnesses.json file, filtering out
// agents whose binary is missing and that have no profiles.
func LoadHarnessesFile(path string) (HarnessFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return HarnessFile{}, fmt.Errorf("model: read %s: %w", path, err)
	}
	var file HarnessFile
	if err := decodeJSON(data, path, &file); err != nil {
		return HarnessFile{}, err
	}
	filtered := make([]HarnessEntry, 0, len(file.Agents))
	for _, a := range file.Agents {
		if a.IsLaunchable() {
			filtered = append(filtered, a)
		}
	}
	file.Agents = filtered
	return file, nil
}

// FetchHarnesses reads ~/.model-router/harnesses.json directly and parses the HarnessFile.
func FetchHarnesses(_ context.Context) (HarnessFile, error) {
	return LoadHarnessesFile(HarnessesPath(""))
}

// TitleQuery describes one pane's conversation title lookup in the local
// harness session stores.
type TitleQuery struct {
	Key       string `json:"key"`
	Agent     string `json:"agent"`
	CWD       string `json:"cwd,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// TitleSnapshot is the JSON returned by `spaces __space-hooks --titles`.
type TitleSnapshot struct {
	OK     bool              `json:"ok"`
	Titles map[string]string `json:"titles"`
}

// FetchConversationTitles resolves paneID -> conversation title in-process
// using the native plugin conversation store without spawning any subprocesses.
func FetchConversationTitles(ctx context.Context, queries []TitleQuery) (map[string]string, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	snap, err := fetchConversationTitlesSnapshot(ctx, queries)
	if err != nil {
		return nil, err
	}
	return snap.Titles, nil
}

// RecordOutcome sends one recent completion or failure entry to
// `spaces __space-hooks --record-outcome <json>` so it persists in hooks.store.json.
func RecordOutcome(ctx context.Context, o OutcomeEntry) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = runSpaces(ctx, "__space-hooks", "--record-outcome", string(raw))
	return err
}

// RemoveOutcome deletes matching recent outcome entries from hooks.store.json
// when a user reopens that conversation into a live tab.
func RemoveOutcome(ctx context.Context, o OutcomeEntry) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = runSpaces(ctx, "__space-hooks", "--remove-outcome", string(raw))
	return err
}
