package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
	hookcore "github.com/arhammahajan/spaces/internal/hooks/core"
)

// ExecFunc runs a helper binary and returns its stdout.
type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecFn is an optional injectable process runner for tests. When nil
// (production default), FetchHooks, FetchHookDetail, RecordOutcome, and
// RemoveOutcome call internal/hooks/core directly in-process.
var ExecFn ExecFunc

// SpacesBinary resolves the helper argv prefix for `spaces` helpers:
// $SPACES_BIN when set, else "spaces" from PATH, else "tmux-spaces" from PATH.
func SpacesBinary() []string {
	if bin := strings.TrimSpace(os.Getenv("SPACES_BIN")); bin != "" {
		if fields := strings.Fields(bin); len(fields) > 0 {
			return fields
		}
	}
	if _, err := exec.LookPath("spaces"); err == nil {
		return []string{"spaces"}
	}
	return []string{"tmux-spaces"}
}

// runSpaces invokes the spaces helper binary via ExecFn (or exec.CommandContext).
func runSpaces(ctx context.Context, args ...string) ([]byte, error) {
	bin := SpacesBinary()
	name := bin[0]
	full := make([]string, 0, len(bin)-1+len(args))
	full = append(full, bin[1:]...)
	full = append(full, args...)
	fn := ExecFn
	if fn == nil {
		fn = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	out, err := fn(ctx, name, full...)
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

// OutcomeEnricher optionally enriches missing outcome fields (session, pane,
// profile, session_id, title) before RecordOutcome writes to hooks.store.json.
// Wired by cmd/tmux-spaces to hookclient.EnrichOutcomeMap; nil in unit tests.
var OutcomeEnricher func(*OutcomeEntry)

func convertHookChildren(in []hookcore.Child) []HookChild {
	if len(in) == 0 {
		return nil
	}
	out := make([]HookChild, len(in))
	for i, c := range in {
		out[i] = HookChild{
			ID:       c.ID,
			Kind:     c.Kind,
			Role:     c.Role,
			Category: c.Category,
			State:    c.State,
			Outcome:  c.Outcome,
			Command:  c.Command,
			Schedule: c.Schedule,
			SinceMs:  c.SinceMs,
			TsMs:     c.TsMs,
			EndedMs:  c.EndedMs,
			Text:     c.Text,
		}
	}
	return out
}

func convertHookSummary(s hookcore.PaneSummary) HookSummary {
	return HookSummary{
		Agent:           s.Agent,
		App:             s.App,
		State:           s.State,
		Kind:            s.Kind,
		Event:           s.Event,
		TsMs:            s.TsMs,
		StateSinceMs:    s.StateSinceMs,
		TurnStartedMs:   s.TurnStartedMs,
		Text:            s.Text,
		Title:           s.Title,
		Model:           s.Model,
		SessionID:       s.SessionID,
		CWD:             s.CWD,
		ChildCount:      s.ChildCount,
		Background:      s.Background,
		FiniteActive:    s.FiniteActive,
		ServiceActive:   s.ServiceActive,
		WaitingOn:       s.WaitingOn,
		Children:        convertHookChildren(s.Children),
		BackgroundTasks: convertHookChildren(s.BackgroundTasks),
		HasDetail:       s.HasDetail,
	}
}

func convertOutcomeToCore(o OutcomeEntry) hookcore.Outcome {
	return hookcore.Outcome{
		Session:   o.Session,
		Pane:      o.Pane,
		Agent:     o.Agent,
		App:       o.App,
		Profile:   o.Profile,
		Outcome:   o.Outcome,
		Text:      o.Text,
		Title:     o.Title,
		Model:     o.Model,
		SessionID: o.SessionID,
		CWD:       o.CWD,
		TsMs:      o.TsMs,
	}
}

// FetchHooks reads the hook store summary in-process (or via ExecFn when stubbed).
func FetchHooks(ctx context.Context) (HookSnapshot, error) {
	if ExecFn != nil {
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
	raw := hookcore.Summary()
	panes := make(map[string]map[string]HookSummary, len(raw.Panes))
	for sess, pm := range raw.Panes {
		flat := make(map[string]HookSummary, len(pm))
		for paneID, sum := range pm {
			flat[paneID] = convertHookSummary(sum)
		}
		panes[sess] = flat
	}
	var outcomes []OutcomeEntry
	if len(raw.Outcomes) > 0 {
		outcomes = make([]OutcomeEntry, len(raw.Outcomes))
		for i, o := range raw.Outcomes {
			outcomes[i] = OutcomeEntry{
				Session:   o.Session,
				Pane:      o.Pane,
				Agent:     o.Agent,
				App:       o.App,
				Profile:   o.Profile,
				Outcome:   o.Outcome,
				Text:      o.Text,
				Title:     o.Title,
				Model:     o.Model,
				SessionID: o.SessionID,
				CWD:       o.CWD,
				TsMs:      o.TsMs,
			}
		}
	}
	return HookSnapshot{
		OK:           raw.OK,
		Version:      raw.Version,
		NowMs:        raw.NowMs,
		SpoolPending: raw.SpoolPending,
		Panes:        panes,
		Outcomes:     outcomes,
	}, nil
}

// FetchHookDetail reads one pane's detailed hook record in-process (or via ExecFn when stubbed).
func FetchHookDetail(ctx context.Context, session string, paneNum int) (HookDetailSnapshot, error) {
	if ExecFn != nil {
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
	raw := hookcore.Detail(session, paneNum)
	snap := HookDetailSnapshot{
		OK:      raw.OK,
		Version: raw.Version,
	}
	if raw.Record != nil {
		sum := convertHookSummary(raw.Record.PaneSummary)
		det := &HookDetail{
			HookSummary:     sum,
			Prompt:          raw.Record.Prompt,
			Reply:           raw.Record.Reply,
			Children:        sum.Children,
			BackgroundTasks: sum.BackgroundTasks,
			Background:      sum.Background,
		}
		if raw.Record.Tool != nil {
			det.Tool = &HookTool{
				Name:   raw.Record.Tool.Name,
				Input:  raw.Record.Tool.Input,
				Output: raw.Record.Tool.Output,
			}
		}
		snap.Record = det
	}
	return snap, nil
}

// HarnessesPath resolves the path to ~/.harness/harnesses.json.
// If TMUX_SPACES_HARNESSES_FILE or SPACES_HARNESSES_PATH is set, it overrides
// the default location.
// If HOME points into a shadow profile under ~/.harness/ and harnesses.json
// does not exist there, it falls back to the real home prefix before /.harness/.
func HarnessesPath(homeDir string) string {
	home := strings.TrimSpace(homeDir)
	if home == "" {
		home = strings.TrimSpace(os.Getenv("HOME"))
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if override := harnessesPathOverride(); override != "" {
		return expandHomePath(override, home)
	}
	candidate := filepath.Join(home, ".harness", "harnesses.json")
	if isRegularFile(candidate) {
		return candidate
	}
	marker := string(filepath.Separator) + ".harness" + string(filepath.Separator)
	if idx := strings.Index(home, marker); idx > 0 {
		realHome := home[:idx]
		alt := filepath.Join(realHome, ".harness", "harnesses.json")
		if isRegularFile(alt) {
			return alt
		}
	}
	return candidate
}

func harnessesPathOverride() string {
	if override := strings.TrimSpace(os.Getenv("TMUX_SPACES_HARNESSES_FILE")); override != "" {
		return override
	}
	return strings.TrimSpace(os.Getenv("SPACES_HARNESSES_PATH"))
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

// BuildHarnessesInProcess constructs the launchable HarnessFile inventory
// directly from internal/harness/agents and internal/harness/profiles without
// requiring an external `harness` binary or pre-generated cache file.
func BuildHarnessesInProcess(paths harnessenv.Paths) (HarnessFile, error) {
	marker := string(filepath.Separator) + ".harness" + string(filepath.Separator)
	if idx := strings.Index(paths.Home, marker); idx > 0 {
		paths.Home = paths.Home[:idx]
	}
	infos := agents.Infos(paths)
	entries, err := profiles.ListAll(paths)
	if err != nil {
		return HarnessFile{}, fmt.Errorf("model: list profiles: %w", err)
	}
	byAgent := map[string][]HarnessProfile{}
	for _, entry := range entries {
		if entry.Source == profiles.SourceDefault {
			continue
		}
		if _, err := profiles.ResolveForAgent(entry.Agent, entry.Name, paths); err != nil {
			continue
		}
		byAgent[entry.Agent] = append(byAgent[entry.Agent], HarnessProfile{
			Name:        entry.Name,
			DisplayName: entry.DisplayName,
			Source:      entry.Source,
			Agent:       entry.Agent,
		})
	}
	file := HarnessFile{
		Version:     1,
		GeneratedAt: time.Now().Unix(),
		Agents:      make([]HarnessEntry, 0, len(infos)),
	}
	for _, info := range infos {
		profs := byAgent[info.Name]
		sort.Slice(profs, func(i, j int) bool { return profs[i].Name < profs[j].Name })
		entry := HarnessEntry{
			Name:        info.Name,
			Binary:      info.Binary,
			BinaryState: info.BinaryState,
			Description: info.Note,
			Profiles:    profs,
		}
		if entry.IsLaunchable() {
			file.Agents = append(file.Agents, entry)
		}
	}
	return file, nil
}

// FetchHarnesses returns the launchable agent/profile inventory. When
// TMUX_SPACES_HARNESSES_FILE or SPACES_HARNESSES_PATH is set (or when
// ~/.harness/harnesses.json exists on disk), it reads that file; otherwise it
// builds the inventory directly in-process from internal/harness.
func FetchHarnesses(_ context.Context) (HarnessFile, error) {
	if harnessesPathOverride() != "" {
		return LoadHarnessesFile(HarnessesPath(""))
	}
	path := HarnessesPath("")
	if isRegularFile(path) {
		if file, err := LoadHarnessesFile(path); err == nil {
			return file, nil
		}
	}
	return BuildHarnessesInProcess(harnessenv.DefaultPaths())
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

// RecordOutcome persists one recent completion or failure entry into hooks.store.json
// in-process (or via ExecFn when stubbed).
func RecordOutcome(ctx context.Context, o OutcomeEntry) error {
	if ExecFn != nil {
		raw, err := json.Marshal(o)
		if err != nil {
			return err
		}
		_, err = runSpaces(ctx, "__space-hooks", "--record-outcome", string(raw))
		return err
	}
	if OutcomeEnricher != nil {
		OutcomeEnricher(&o)
	}
	return hookcore.RecordTypedOutcome(convertOutcomeToCore(o))
}

// RemoveOutcome deletes matching recent outcome entries from hooks.store.json
// in-process (or via ExecFn when stubbed).
func RemoveOutcome(ctx context.Context, o OutcomeEntry) error {
	if ExecFn != nil {
		raw, err := json.Marshal(o)
		if err != nil {
			return err
		}
		_, err = runSpaces(ctx, "__space-hooks", "--remove-outcome", string(raw))
		return err
	}
	return hookcore.RemoveTypedOutcome(convertOutcomeToCore(o))
}
