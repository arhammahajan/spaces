package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/model"
)

// RecordConversationLaunchFunc persists a conversation's invoking command and
// metadata in the conversation overlay. Tests may override it.
var RecordConversationLaunchFunc = model.RecordConversationLaunch

// PaneProcArgvFunc inspects a pane's root PID and its descendants to find an
// active codex process command line (used to match `codex resume <id>` when
// routing hooks from Codex's shared app-server daemon). Tests may override it.
var PaneProcArgvFunc func(pid int) []string = defaultPaneCodexArgv

func parseLaunchCmdJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return nil
	}
	var cmd []string
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil || len(cmd) == 0 {
		return nil
	}
	return cmd
}

// TmuxBinary returns the tmux binary path ($TMUX_BIN or "tmux").
func TmuxBinary() string {
	if v := os.Getenv("TMUX_BIN"); v != "" {
		return v
	}
	return "tmux"
}

// TmuxFunc runs the tmux binary and captures stdout. Tests override it;
// production shells out via RunBinFunc, so stubbing RunBinFunc also stubs
// tmux. Consumers must nil-check: hook paths stay silent without tmux.
var TmuxFunc func(args []string, timeout time.Duration) (string, int, error) = realTmux

func realTmux(args []string, timeout time.Duration) (string, int, error) {
	return RunBinFunc(TmuxBinary(), args, "", timeout)
}

// inTmuxPane reports whether hooks run inside tmux without Zellij: no
// Zellij session name, but TMUX + TMUX_PANE are set.
func inTmuxPane() bool {
	if os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return false
	}
	return os.Getenv("TMUX") != "" && os.Getenv("TMUX_PANE") != ""
}

// hookTargetTmux resolves the (session, pane id) from the tmux environment.
// The numeric pane id comes from $TMUX_PANE ("%12" -> "12") and the session
// name from `tmux display-message`. Any failure returns ok=false so the
// caller stays silent.
func hookTargetTmux() (string, string, bool) {
	if os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return "", "", false
	}
	if os.Getenv("TMUX") == "" {
		return "", "", false
	}
	paneRef := os.Getenv("TMUX_PANE")
	if paneRef == "" {
		return "", "", false
	}
	digits := paneIDDigits.FindString(paneRef)
	if digits == "" {
		return "", "", false
	}
	var pane int
	if _, err := splitSScan(digits, &pane); err != nil {
		return "", "", false
	}
	if TmuxFunc == nil {
		return "", "", false
	}
	stdout, code, err := TmuxFunc([]string{
		"display-message", "-p", "-t", paneRef, "#{session_name}",
	}, 2*time.Second)
	if err != nil || code != 0 {
		return "", "", false
	}
	session := strings.TrimSpace(stdout)
	if session == "" {
		return "", "", false
	}
	return session, strconv.Itoa(pane), true
}

// tmuxNowMs reports the current time in Unix milliseconds for @spaces_ts.
func tmuxNowMs() int64 {
	if NowFunc == nil {
		return time.Now().UnixMilli()
	}
	return NowFunc().UnixMilli()
}

// emitTmuxProgramStatus projects a program-status record onto native tmux
// per-pane options. Empty kind/msg unset their options so stale values never
// linger. Failures are silent: hooks must never break the agent on a tmux error.
func emitTmuxProgramStatus(state, app, msg, kind string) {
	emitTmuxProgramStatusToPane(os.Getenv("TMUX_PANE"), state, app, msg, kind)
}

func emitTmuxProgramStatusToPane(pane, state, app, msg, kind string) {
	if TmuxFunc == nil || pane == "" {
		return
	}
	set := func(option, value string) {
		_, _, _ = TmuxFunc([]string{
			"set-option", "-p", "-t", pane, option, value,
		}, 2*time.Second)
	}
	unset := func(option string) {
		_, _, _ = TmuxFunc([]string{
			"set-option", "-p", "-u", "-t", pane, option,
		}, 2*time.Second)
	}
	set("@spaces_state", state)
	set("@spaces_app", app)
	// Only inherit HARNESS_PROFILE from this process's env when updating our
	// own pane; a shared daemon (e.g. Codex app-server) carries the profile of
	// whichever pane spawned it, not the target pane.
	if pane == os.Getenv("TMUX_PANE") {
		if prof := strings.TrimSpace(os.Getenv("HARNESS_PROFILE")); prof != "" {
			set("@spaces_profile", prof)
		}
	}
	if kind != "" {
		set("@spaces_kind", kind)
	} else {
		unset("@spaces_kind")
	}
	if msg != "" {
		set("@spaces_msg", msg)
	} else {
		unset("@spaces_msg")
	}
	set("@spaces_ts", strconv.FormatInt(tmuxNowMs(), 10))
	set("@spaces_source", "hook")
}

// recordTmuxConversationTitle persists the conversation's title on the tmux
// pane when non-empty.
func recordTmuxConversationTitle(title string) {
	recordTmuxConversationTitleToPane(os.Getenv("TMUX_PANE"), title)
}

func recordTmuxConversationTitleToPane(pane, title string) {
	title = strings.TrimSpace(title)
	if TmuxFunc == nil || title == "" || pane == "" {
		return
	}
	_, _, _ = TmuxFunc([]string{
		"set-option", "-p", "-t", pane, "@spaces_title", title,
	}, 2*time.Second)
}

// recordTmuxResumeTarget persists the conversation's session_id and canonical
// agent id on the tmux pane so tmux-spaces can save and restore the exact AI
// conversation when a space is resurrected.
func recordTmuxResumeTarget(agent, sessionID string) {
	recordTmuxResumeTargetToPane(os.Getenv("TMUX_PANE"), agent, sessionID)
}

func recordTmuxResumeTargetToPane(pane, agent, sessionID string) {
	if TmuxFunc == nil || sessionID == "" || pane == "" {
		return
	}
	_, _, _ = TmuxFunc([]string{
		"set-option", "-p", "-t", pane, "@spaces_resume_session", sessionID,
	}, 2*time.Second)
	if agent != "" {
		_, _, _ = TmuxFunc([]string{
			"set-option", "-p", "-t", pane, "@spaces_resume_agent", agent,
		}, 2*time.Second)
	}
	recordPaneLaunchCmd(pane, agent, sessionID)
}

// recordPaneLaunchCmd reads @spaces_launch_cmd, @spaces_profile, session name,
// and pane CWD from the tmux pane and records the invoking command and/or
// profile in the conversation overlay as a best-effort non-fatal call.
func recordPaneLaunchCmd(pane, agentName, sessionID string) {
	if TmuxFunc == nil || RecordConversationLaunchFunc == nil || pane == "" || agentName == "" || sessionID == "" {
		return
	}
	stdout, code, err := TmuxFunc([]string{
		"display-message", "-p", "-t", pane,
		"#{session_name}\x1f#{pane_current_path}\x1f#{@spaces_profile}\x1f#{@spaces_launch_cmd}",
	}, 2*time.Second)
	if err != nil || code != 0 {
		return
	}
	parts := strings.Split(strings.TrimSuffix(stdout, "\n"), "\x1f")
	getPart := func(i int) string {
		if i < len(parts) {
			v := strings.TrimSpace(parts[i])
			if v != "" && v != "-" {
				return v
			}
		}
		return ""
	}
	space := getPart(0)
	cwd := getPart(1)
	profile := getPart(2)
	launchCmd := parseLaunchCmdJSON(getPart(3))
	if profile == "" && pane == os.Getenv("TMUX_PANE") {
		profile = strings.TrimSpace(os.Getenv("HARNESS_PROFILE"))
	}
	if profile == "" && len(launchCmd) > 0 {
		profile = agent.HarnessProfileFromArgv(launchCmd)
	}
	if len(launchCmd) == 0 && profile == "" {
		return
	}
	if cwd == "" {
		cwd = CurrentDirectory()
	}
	ctx, cancel := contextWithTimeout(2 * time.Second)
	defer cancel()
	_ = RecordConversationLaunchFunc(ctx, agentName, sessionID, profile, cwd, "", launchCmd, space, tmuxNowMs())
}

// SetTmuxPaneOption sets a per-pane tmux option on paneRef (or $TMUX_PANE when empty).
func SetTmuxPaneOption(paneRef, option, value string) {
	if TmuxFunc == nil || os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return
	}
	if paneRef == "" {
		if !inTmuxPane() {
			return
		}
		paneRef = os.Getenv("TMUX_PANE")
	}
	if paneRef == "" {
		return
	}
	_, _, _ = TmuxFunc([]string{
		"set-option", "-p", "-t", paneRef, option, value,
	}, 2*time.Second)
}

// UnsetTmuxPaneOption clears a per-pane tmux option on paneRef (or $TMUX_PANE when empty).
func UnsetTmuxPaneOption(paneRef, option string) {
	if TmuxFunc == nil || os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return
	}
	if paneRef == "" {
		if !inTmuxPane() {
			return
		}
		paneRef = os.Getenv("TMUX_PANE")
	}
	if paneRef == "" {
		return
	}
	_, _, _ = TmuxFunc([]string{
		"set-option", "-p", "-u", "-t", paneRef, option,
	}, 2*time.Second)
}

// GetTmuxPaneOption reads a per-pane tmux option on paneRef (or $TMUX_PANE when empty).
func GetTmuxPaneOption(paneRef, option string) string {
	if TmuxFunc == nil || os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return ""
	}
	if paneRef == "" {
		if !inTmuxPane() {
			return ""
		}
		paneRef = os.Getenv("TMUX_PANE")
	}
	if paneRef == "" {
		return ""
	}
	stdout, code, err := TmuxFunc([]string{
		"display-message", "-p", "-t", paneRef, "#{" + option + "}",
	}, 2*time.Second)
	if err != nil || code != 0 {
		return ""
	}
	v := strings.TrimSpace(stdout)
	if v == "-" {
		return ""
	}
	return v
}

// isCodexAppServerDaemon reports whether the current hook process was spawned
// by Codex's shared `codex app-server` daemon. That daemon inherits the
// TMUX_PANE of whichever pane happened to start it first, so all subsequent
// Codex TUI panes share the same daemon and would otherwise misattribute their
// hooks to that stale/wrong pane.
func isCodexAppServerDaemon() bool {
	if AncestorsFunc == nil {
		return false
	}
	for _, argv := range AncestorsFunc() {
		if len(argv) < 2 {
			continue
		}
		if InferAgent(argv[0]) != "codex" {
			continue
		}
		for _, tok := range argv[1:] {
			if tok == "app-server" {
				return true
			}
		}
	}
	return false
}

// hookTargetForAgent resolves (session, numericPane, tmuxPaneRef, ok) for a
// hook invocation. For Codex hooks spawned by the shared `codex app-server`
// daemon, it resolves the live tmux pane owning the conversation instead of
// trusting the daemon's inherited $TMUX_PANE.
func hookTargetForAgent(agentName string, payload map[string]any) (string, string, string, bool) {
	if os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		s, p, ok := hookTarget()
		return s, p, "", ok
	}
	if agentName == "codex" && isCodexAppServerDaemon() {
		return resolveCodexDaemonTmuxTarget(payload)
	}
	s, p, ok := hookTargetTmux()
	if !ok {
		return "", "", "", false
	}
	paneRef := os.Getenv("TMUX_PANE")
	if paneRef == "" && p != "" {
		paneRef = "%" + p
	}
	return s, p, paneRef, true
}

type liveCodexPane struct {
	session    string
	paneRef    string
	paneNum    string
	cwd        string
	pid        int
	resumeSID  string
	argvSID    string
	state      string
	tsMs       int64
	paneActive bool
	winActive  bool
	attached   bool
}

func payloadCWD(payload map[string]any) string {
	for _, k := range []string{"cwd", "working_directory", "workingDirectory"} {
		if v, ok := payload[k].(string); ok && strings.TrimSpace(v) != "" {
			return filepath.Clean(strings.TrimSpace(v))
		}
	}
	return ""
}

func resolveCodexDaemonTmuxTarget(payload map[string]any) (string, string, string, bool) {
	if TmuxFunc == nil || os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return "", "", "", false
	}
	stdout, code, err := TmuxFunc([]string{
		"list-panes", "-a", "-F",
		"#{session_name}\x1f#{pane_id}\x1f#{pane_dead}\x1f#{pane_current_command}\x1f#{pane_current_path}\x1f#{pane_pid}\x1f#{@spaces_app}\x1f#{@spaces_resume_session}\x1f#{@spaces_state}\x1f#{@spaces_ts}\x1f#{pane_active}\x1f#{window_active}\x1f#{session_attached}",
	}, 2*time.Second)
	if err != nil || code != 0 || strings.TrimSpace(stdout) == "" {
		return "", "", "", false
	}

	var candidates []liveCodexPane
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 13 {
			continue
		}
		if strings.TrimSpace(parts[2]) == "1" {
			continue // dead pane
		}
		session := strings.TrimSpace(parts[0])
		paneRef := strings.TrimSpace(parts[1])
		digits := paneIDDigits.FindString(paneRef)
		if session == "" || digits == "" {
			continue
		}
		cmd := strings.TrimSpace(parts[3])
		cwd := strings.TrimSpace(parts[4])
		if cwd != "" {
			cwd = filepath.Clean(cwd)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(parts[5]))
		app := strings.TrimSpace(parts[6])
		resumeSID := strings.TrimSpace(parts[7])
		if resumeSID == "-" {
			resumeSID = ""
		}
		state := strings.TrimSpace(parts[8])
		if state == "-" {
			state = ""
		}
		tsMs, _ := strconv.ParseInt(strings.TrimSpace(parts[9]), 10, 64)

		isCodex := Normalize(cmd) == "codex" || Normalize(app) == "codex"
		var argvSID string
		if pid > 0 && PaneProcArgvFunc != nil {
			if argv := PaneProcArgvFunc(pid); len(argv) > 0 {
				isCodex = true
				argvSID = agent.ExtractSessionIDFromArgs(argv, true)
			}
		}
		if !isCodex {
			continue
		}
		candidates = append(candidates, liveCodexPane{
			session:    session,
			paneRef:    paneRef,
			paneNum:    digits,
			cwd:        cwd,
			pid:        pid,
			resumeSID:  resumeSID,
			argvSID:    argvSID,
			state:      state,
			tsMs:       tsMs,
			paneActive: strings.TrimSpace(parts[10]) == "1",
			winActive:  strings.TrimSpace(parts[11]) == "1",
			attached:   strings.TrimSpace(parts[12]) != "" && strings.TrimSpace(parts[12]) != "0",
		})
	}
	if len(candidates) == 0 {
		return "", "", "", false
	}

	sid := strings.TrimSpace(ExtractSessionID(payload))
	cwd := payloadCWD(payload)

	// Priority 1: Exact session_id match via @spaces_resume_session or live
	// `codex resume <id>` argv.
	if sid != "" {
		for _, c := range candidates {
			if c.resumeSID == sid || c.argvSID == sid {
				return c.session, c.paneNum, c.paneRef, true
			}
		}
		// Also check if the hook store already tracks this session_id on one
		// of the live Codex panes.
		if SpaceHooksSummary != nil {
			if summary := SpaceHooksSummary(); summary != nil {
				if panesMap, ok := summary["panes"].(map[string]any); ok {
					for _, c := range candidates {
						sp, ok := panesMap[c.session].(map[string]any)
						if !ok {
							continue
						}
						rec, ok := sp[c.paneNum].(map[string]any)
						if !ok {
							continue
						}
						if recSID, _ := rec["session_id"].(string); strings.TrimSpace(recSID) == sid {
							return c.session, c.paneNum, c.paneRef, true
						}
					}
				}
			}
		}
	}

	// Priority 2: Unbound live Codex panes (not already bound to a different
	// session ID) matching payload CWD.
	isUnbound := func(c liveCodexPane) bool {
		if sid == "" {
			return true
		}
		if c.resumeSID != "" && c.resumeSID != sid {
			return false
		}
		if c.argvSID != "" && c.argvSID != sid {
			return false
		}
		return true
	}

	var cwdMatches []liveCodexPane
	if cwd != "" {
		for _, c := range candidates {
			if isUnbound(c) && c.cwd == cwd {
				cwdMatches = append(cwdMatches, c)
			}
		}
	}
	if len(cwdMatches) == 1 {
		c := cwdMatches[0]
		return c.session, c.paneNum, c.paneRef, true
	}
	if len(cwdMatches) > 1 {
		best := pickBestCodexPane(cwdMatches)
		return best.session, best.paneNum, best.paneRef, true
	}

	// Priority 3: If payload has no CWD, match when there is exactly one
	// unbound live Codex pane.
	if cwd == "" {
		var unbound []liveCodexPane
		for _, c := range candidates {
			if isUnbound(c) {
				unbound = append(unbound, c)
			}
		}
		if len(unbound) == 1 {
			c := unbound[0]
			return c.session, c.paneNum, c.paneRef, true
		}
	}

	return "", "", "", false
}

func pickBestCodexPane(panes []liveCodexPane) liveCodexPane {
	best := panes[0]
	score := func(c liveCodexPane) (int64, int) {
		active := 0
		if c.attached && c.winActive && c.paneActive {
			active = 3
		} else if c.winActive && c.paneActive {
			active = 2
		} else if c.paneActive {
			active = 1
		}
		return c.tsMs, active
	}
	bestTs, bestActive := score(best)
	for _, c := range panes[1:] {
		ts, act := score(c)
		if ts > bestTs || (ts == bestTs && act > bestActive) {
			best = c
			bestTs = ts
			bestActive = act
		}
	}
	return best
}

// defaultPaneCodexArgv walks pid and its descendants in /proc looking for an
// interactive codex process (excluding `codex app-server`) and returns its argv.
func defaultPaneCodexArgv(rootPID int) []string {
	if rootPID <= 1 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		fields, err := procStatFields(pid)
		if err != nil || len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid <= 0 {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}

	visited := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	var fallback []string
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline")); err == nil && len(raw) > 0 {
			var argv []string
			for _, part := range strings.Split(string(raw), "\x00") {
				if part != "" {
					argv = append(argv, part)
				}
			}
			if len(argv) > 0 && InferAgent(argv[0]) == "codex" {
				isServer := false
				for _, tok := range argv[1:] {
					if tok == "app-server" {
						isServer = true
						break
					}
				}
				if !isServer {
					if agent.ExtractSessionIDFromArgs(argv, true) != "" {
						return argv
					}
					if len(fallback) == 0 {
						fallback = argv
					}
				}
			}
		}
		for _, child := range children[pid] {
			if !visited[child] {
				visited[child] = true
				queue = append(queue, child)
			}
		}
	}
	return fallback
}
