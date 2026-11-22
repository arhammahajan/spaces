package client

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/model"
)

// RecordConversationLaunchFunc persists a conversation's invoking command and
// metadata in the conversation overlay. Tests may override it.
var RecordConversationLaunchFunc = model.RecordConversationLaunch

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
	if TmuxFunc == nil {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
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
	if prof := strings.TrimSpace(os.Getenv("HARNESS_PROFILE")); prof != "" {
		set("@spaces_profile", prof)
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
	title = strings.TrimSpace(title)
	if TmuxFunc == nil || title == "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
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
	if TmuxFunc == nil || sessionID == "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
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
	if profile == "" {
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
	if TmuxFunc == nil || !inTmuxPane() {
		return
	}
	if paneRef == "" {
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
	if TmuxFunc == nil || !inTmuxPane() {
		return
	}
	if paneRef == "" {
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
	if TmuxFunc == nil || !inTmuxPane() {
		return ""
	}
	if paneRef == "" {
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
