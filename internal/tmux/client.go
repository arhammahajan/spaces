package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// sessionListFormat is the -F format for ListSessions.
const sessionListFormat = "#{session_id}\x1f#{session_name}\x1f" +
	"#{session_attached}\x1f#{session_created}\x1f#{session_windows}\x1f" +
	"#{@spaces_worktree_path}\x1f#{@spaces_worktree_name}\x1f" +
	"#{@spaces_worktree_base}\x1f#{@spaces_worktree_managed}"

// windowListFormat is the -F format for ListWindows.
const windowListFormat = "#{session_name}\x1f#{window_id}\x1f#{window_index}\x1f" +
	"#{window_name}\x1f#{window_active}\x1f#{window_bell_flag}\x1f" +
	"#{window_activity_flag}\x1f#{window_silence_flag}\x1f#{window_layout}\x1f" +
	"#{window_last_flag}\x1f#{window_zoomed_flag}\x1f#{window_width}\x1f" +
	"#{window_height}\x1f#{window_activity}\x1f" +
	"#{@spaces_worktree_path}\x1f#{@spaces_worktree_name}\x1f" +
	"#{@spaces_worktree_base}\x1f#{@spaces_worktree_managed}"

// spacesOptions are the native per-pane tmux user options maintained by
// hooks/shims and read back by ListPanes.
var spacesOptions = []string{
	"@spaces_state",
	"@spaces_app",
	"@spaces_kind",
	"@spaces_msg",
	"@spaces_ts",
}

// Client drives the tmux CLI through a Runner.
//
// The zero value is not usable: build Clients with NewClient (tests) or
// DefaultClient (production, honoring $TMUX_BIN).
type Client struct {
	// Runner executes tmux commands. Required.
	Runner Runner
	// Getenv reads environment variables; nil means os.Getenv.
	// Exposed as a seam so tests can fake TMUX/TMUX_PANE.
	Getenv func(string) string
}

// NewClient returns a Client using r.
func NewClient(r Runner) *Client {
	return &Client{Runner: r}
}

// DefaultClient returns a Client using ExecRunner (honoring $TMUX_BIN).
func DefaultClient() *Client {
	return NewClient(NewExecRunner())
}

func (c *Client) getenv(key string) string {
	if c.Getenv != nil {
		return c.Getenv(key)
	}
	return os.Getenv(key)
}

// run executes a tmux command and returns stdout, translating failures.
func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, exitCode, err := c.Runner.Run(ctx, args...)
	if err != nil {
		return "", commandError(args, stderr, exitCode, err)
	}
	return stdout, nil
}

// InsideTmux reports whether this process runs inside a tmux client.
func (c *Client) InsideTmux() bool {
	return c.getenv("TMUX") != ""
}

// CurrentPaneID returns the tmux pane id of this process (e.g. "%12"),
// or "" when not running inside a tmux pane.
func (c *Client) CurrentPaneID() string {
	return c.getenv("TMUX_PANE")
}

// CurrentSession returns the name of the session the tmux client is attached to.
func (c *Client) CurrentSession(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "display-message", "-p", "#{session_name}")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return "", fmt.Errorf("tmux display-message: no current session: %w", ErrNoServer)
	}
	return name, nil
}

// ServerRunning reports whether a tmux server with at least one session
// is reachable.
func (c *Client) ServerRunning(ctx context.Context) bool {
	_, _, exitCode, err := c.Runner.Run(ctx, "has-session")
	return err == nil && exitCode == 0
}

// ParseNumericPaneID converts a tmux pane id like "%12" to 12,
// returning -1 for malformed ids.
func (c *Client) ParseNumericPaneID(id string) int {
	return ParseNumericPaneID(id)
}

// ListSessions returns all tmux sessions.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	stdout, stderr, exitCode, err := c.Runner.Run(ctx, "list-sessions", "-F", sessionListFormat)
	if err != nil {
		return nil, commandError([]string{"list-sessions", "-F", sessionListFormat}, stderr, exitCode, err)
	}
	var sessions []Session
	for _, line := range splitOutputLines(stdout) {
		session, ok := parseSessionRecord(line)
		if !ok {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// ListWindows returns all windows across all sessions.
func (c *Client) ListWindows(ctx context.Context) ([]Window, error) {
	stdout, stderr, exitCode, err := c.Runner.Run(ctx, "list-windows", "-a", "-F", windowListFormat)
	if err != nil {
		return nil, commandError([]string{"list-windows", "-a", "-F", windowListFormat}, stderr, exitCode, err)
	}
	var windows []Window
	for _, line := range splitOutputLines(stdout) {
		window, ok := parseWindowRecord(line)
		if !ok {
			continue
		}
		windows = append(windows, window)
	}
	return windows, nil
}

// ListPanes returns all panes across all sessions, including native
// @spaces_* per-pane options. Titles, paths, and messages may contain spaces
// and tabs: records are split on \x1f, never on whitespace.
func (c *Client) ListPanes(ctx context.Context) ([]Pane, error) {
	stdout, stderr, exitCode, err := c.Runner.Run(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return nil, commandError([]string{"list-panes", "-a", "-F", paneFormat}, stderr, exitCode, err)
	}
	var panes []Pane
	for _, line := range splitOutputLines(stdout) {
		pane, ok := parsePaneRecord(line)
		if !ok {
			continue
		}
		panes = append(panes, pane)
	}
	return panes, nil
}

// Snapshot returns a consistent view of sessions, windows, and panes.
//
// When no tmux server is running it returns an empty snapshot and a nil
// error so callers can treat "no server" as "no spaces". The current
// session/pane lookup is best-effort: failures leave those fields empty
// (falling back to $TMUX_PANE for the pane) rather than failing the whole
// snapshot.
func (c *Client) Snapshot(ctx context.Context) (ServerSnapshot, error) {
	var snap ServerSnapshot
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return snap, nil
		}
		return snap, err
	}
	snap.Sessions = sessions

	windows, err := c.ListWindows(ctx)
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return ServerSnapshot{}, nil
		}
		return snap, err
	}
	snap.Windows = windows

	panes, err := c.ListPanes(ctx)
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return ServerSnapshot{}, nil
		}
		return snap, err
	}
	snap.Panes = panes

	if stdout, _, _, err := c.Runner.Run(ctx, "display-message", "-p", "-F",
		"#{session_name}\x1f#{pane_id}"); err == nil {
		if fields := strings.Split(strings.TrimSpace(stdout), unitSep); len(fields) == 2 {
			snap.CurrentSession = fields[0]
			snap.CurrentPaneID = fields[1]
		}
	}
	if snap.CurrentPaneID == "" {
		snap.CurrentPaneID = c.CurrentPaneID()
	}
	return snap, nil
}

// CapturePane returns the visible text of a pane, including up to lines
// lines of scrollback. Non-positive lines selects a 200-line default.
func (c *Client) CapturePane(ctx context.Context, paneID string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	return c.run(ctx, "capture-pane", "-p", "-t", paneID, "-S", fmt.Sprintf("-%d", lines))
}

// CapturePaneStyled returns the visible viewport of a pane with ANSI SGR
// color/style escape sequences and trailing spaces preserved (no scrollback),
// suitable for live TUI previews.
func (c *Client) CapturePaneStyled(ctx context.Context, paneID string) (string, error) {
	return c.run(ctx, "capture-pane", "-p", "-e", "-N", "-t", paneID)
}

// SwitchToSpace switches the client to session. Inside tmux it uses
// switch-client; outside tmux it attaches to the session.
func (c *Client) SwitchToSpace(ctx context.Context, session string) error {
	if c.InsideTmux() {
		_, err := c.run(ctx, "switch-client", "-t", session)
		return err
	}
	_, err := c.run(ctx, "attach-session", "-t", session)
	return err
}

// FocusPane focuses paneID in windowID of session: it switches sessions when
// needed, then selects the window and pane. Empty windowID/paneID steps are
// skipped.
func (c *Client) FocusPane(ctx context.Context, session, windowID, paneID string) error {
	if session != "" {
		current, err := c.CurrentSession(ctx)
		if err != nil || current != session {
			if c.InsideTmux() {
				if _, err := c.run(ctx, "switch-client", "-t", session); err != nil {
					return err
				}
			} else if _, err := c.run(ctx, "attach-session", "-t", session); err != nil {
				return err
			}
		}
	}
	if windowID != "" {
		if _, err := c.run(ctx, "select-window", "-t", windowID); err != nil {
			return err
		}
	}
	if paneID != "" {
		if _, err := c.run(ctx, "select-pane", "-t", paneID); err != nil {
			return err
		}
	}
	return nil
}

// NewSession creates a detached session named name (in cwd when non-empty)
// and then switches to it.
func (c *Client) NewSession(ctx context.Context, name, cwd string) error {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return err
	}
	return c.SwitchToSpace(ctx, name)
}

// RenameSession renames the session oldName to newName.
func (c *Client) RenameSession(ctx context.Context, oldName, newName string) error {
	_, err := c.run(ctx, "rename-session", "-t", oldName, newName)
	return err
}

// RenameWindow renames the target window (or current window when target == "") to newName.
func (c *Client) RenameWindow(ctx context.Context, target, newName string) error {
	if target == "" {
		_, err := c.run(ctx, "rename-window", "--", newName)
		return err
	}
	_, err := c.run(ctx, "rename-window", "-t", target, "--", newName)
	return err
}

// KillSession kills the named session.
func (c *Client) KillSession(ctx context.Context, name string) error {
	_, err := c.run(ctx, "kill-session", "-t", name)
	return err
}

// KillWindow kills the target window (e.g. "@1" or "session:1").
func (c *Client) KillWindow(ctx context.Context, target string) error {
	_, err := c.run(ctx, "kill-window", "-t", target)
	return err
}

// NewWindow creates a window in session (appended at the end), optionally in
// cwd with name, running command when provided. When name is empty, `-n` is
// omitted so tmux keeps `automatic-rename` active (displaying the active process).
func (c *Client) NewWindow(ctx context.Context, session, cwd, name string, command ...string) error {
	args := []string{"new-window"}
	if session != "" {
		args = append(args, "-t", session+":")
	}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if name != "" {
		args = append(args, "-n", name)
	}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	_, err := c.run(ctx, args...)
	return err
}

// NewWindowReturningID creates a window like NewWindow and returns its tmux
// window ID (e.g. "@12") via `-P -F #{window_id}`.
func (c *Client) NewWindowReturningID(ctx context.Context, session, cwd, name string, command ...string) (string, error) {
	args := []string{"new-window", "-P", "-F", "#{window_id}"}
	if session != "" {
		args = append(args, "-t", session+":")
	}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if name != "" {
		args = append(args, "-n", name)
	}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SetWindowOption sets a per-window tmux user option on windowID.
func (c *Client) SetWindowOption(ctx context.Context, windowID, option, value string) error {
	args := []string{"set-option", "-w"}
	if windowID != "" {
		args = append(args, "-t", windowID)
	}
	args = append(args, normalizeOption(option), value)
	_, err := c.run(ctx, args...)
	return err
}

// SetSessionOption sets a per-session tmux user option on session.
func (c *Client) SetSessionOption(ctx context.Context, session, option, value string) error {
	args := []string{"set-option"}
	if session != "" {
		args = append(args, "-t", session)
	}
	args = append(args, normalizeOption(option), value)
	_, err := c.run(ctx, args...)
	return err
}

// GetGlobalOption reads a global tmux option (returning "" if unset).
func (c *Client) GetGlobalOption(ctx context.Context, option string) (string, error) {
	out, err := c.run(ctx, "show-option", "-gqv", option)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SetGlobalOption sets a global tmux option quietly.
func (c *Client) SetGlobalOption(ctx context.Context, option, value string) error {
	_, err := c.run(ctx, "set-option", "-gq", option, value)
	return err
}

// HasClients reports whether at least one client is currently attached to the
// tmux server.
func (c *Client) HasClients(ctx context.Context) bool {
	out, err := c.run(ctx, "list-clients", "-F", "#{client_name}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// SwitchClient switches the active tmux client to session without requiring
// $TMUX to be set (suitable for background run-shell hooks).
func (c *Client) SwitchClient(ctx context.Context, session string) error {
	_, err := c.run(ctx, "switch-client", "-t", session)
	return err
}

// normalizeOption ensures a pane option carries the "@" user-option prefix.
func normalizeOption(option string) string {
	if strings.HasPrefix(option, "@") {
		return option
	}
	return "@" + option
}

// SetPaneOption sets a per-pane tmux user option on paneID.
// An empty paneID targets the current pane.
func (c *Client) SetPaneOption(ctx context.Context, paneID, option, value string) error {
	args := []string{"set-option", "-p"}
	if paneID != "" {
		args = append(args, "-t", paneID)
	}
	args = append(args, normalizeOption(option), value)
	_, err := c.run(ctx, args...)
	return err
}

// UnsetPaneOption clears a per-pane tmux user option on paneID.
// An empty paneID targets the current pane.
func (c *Client) UnsetPaneOption(ctx context.Context, paneID, option string) error {
	args := []string{"set-option", "-p"}
	if paneID != "" {
		args = append(args, "-t", paneID)
	}
	args = append(args, "-u", normalizeOption(option))
	_, err := c.run(ctx, args...)
	return err
}

// SetSpacesStatus writes the native @spaces_* per-pane options for paneID.
// A state of "clear" or "" unsets all five options instead.
func (c *Client) SetSpacesStatus(ctx context.Context, paneID, state, app, kind, msg string, tsMs int64) error {
	if state == "" || state == "clear" {
		for _, option := range spacesOptions {
			if err := c.UnsetPaneOption(ctx, paneID, option); err != nil {
				return err
			}
		}
		return nil
	}
	values := map[string]string{
		"@spaces_state": state,
		"@spaces_app":   app,
		"@spaces_kind":  kind,
		"@spaces_msg":   msg,
		"@spaces_ts":    strconv.FormatInt(tsMs, 10),
	}
	for _, option := range spacesOptions {
		if err := c.SetPaneOption(ctx, paneID, option, values[option]); err != nil {
			return err
		}
	}
	return nil
}

// SendKeys sends literal text to target; when enter is true it then sends
// the Enter key. Text is always passed with -l so tmux key names inside the
// text are not interpreted.
func (c *Client) SendKeys(ctx context.Context, target, text string, enter bool) error {
	if _, err := c.run(ctx, "send-keys", "-t", target, "-l", text); err != nil {
		return err
	}
	if enter {
		if _, err := c.run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
			return err
		}
	}
	return nil
}

// DisplayPopup opens a tmux display-popup per opts.
func (c *Client) DisplayPopup(ctx context.Context, opts PopupOptions) error {
	args := []string{"display-popup"}
	if opts.Close {
		args = append(args, "-E")
	}
	if opts.Width != "" {
		args = append(args, "-w", opts.Width)
	}
	if opts.Height != "" {
		args = append(args, "-h", opts.Height)
	}
	if opts.Title != "" {
		args = append(args, "-T", opts.Title)
	}
	if opts.Border != "" {
		args = append(args, "-b", opts.Border)
	}
	if opts.CWD != "" {
		args = append(args, "-d", opts.CWD)
	}
	args = append(args, opts.Command...)
	_, err := c.run(ctx, args...)
	return err
}

// NewSessionDetached creates a detached tmux session without switching to it,
// optionally naming its initial window and running a command.
func (c *Client) NewSessionDetached(ctx context.Context, name, cwd, windowName string, command ...string) error {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if windowName != "" {
		args = append(args, "-n", windowName)
	}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	_, err := c.run(ctx, args...)
	return err
}

// SplitWindow splits the target window/pane, optionally in cwd and running command.
func (c *Client) SplitWindow(ctx context.Context, target, cwd string, command ...string) error {
	args := []string{"split-window", "-t", target}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	_, err := c.run(ctx, args...)
	return err
}

// SelectLayout applies a saved tmux window layout string to target.
func (c *Client) SelectLayout(ctx context.Context, target, layout string) error {
	if strings.TrimSpace(layout) == "" {
		return nil
	}
	_, err := c.run(ctx, "select-layout", "-t", target, layout)
	return err
}

// SelectWindow activates the given window target (e.g. "session:1").
func (c *Client) SelectWindow(ctx context.Context, target string) error {
	_, err := c.run(ctx, "select-window", "-t", target)
	return err
}

// SelectPane activates the given pane target (e.g. "session:1.2").
func (c *Client) SelectPane(ctx context.Context, target string) error {
	_, err := c.run(ctx, "select-pane", "-t", target)
	return err
}

// NewSessionDetachedWithID creates a detached tmux session (optionally with
// initial width/height) and returns its initial window ID and pane ID.
func (c *Client) NewSessionDetachedWithID(ctx context.Context, name, cwd, windowName string, width, height int, command ...string) (string, string, error) {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if windowName != "" {
		args = append(args, "-n", windowName)
	}
	if width > 0 {
		args = append(args, "-x", strconv.Itoa(width))
	}
	if height > 0 {
		args = append(args, "-y", strconv.Itoa(height))
	}
	args = append(args, "-P", "-F", "#{window_id}\x1f#{pane_id}")
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.TrimSpace(out), unitSep)
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return strings.TrimSpace(out), "", nil
}

// NewWindowDetachedWithID creates a detached window in session and returns its
// window ID and initial pane ID.
func (c *Client) NewWindowDetachedWithID(ctx context.Context, session, cwd, name string, command ...string) (string, string, error) {
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}\x1f#{pane_id}"}
	if session != "" {
		args = append(args, "-t", session+":")
	}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if name != "" {
		args = append(args, "-n", name)
	}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.TrimSpace(out), unitSep)
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return strings.TrimSpace(out), "", nil
}

// SplitWindowWithID splits target without changing focus and returns the new
// pane ID.
func (c *Client) SplitWindowWithID(ctx context.Context, target, cwd string, command ...string) (string, error) {
	args := []string{"split-window", "-d", "-t", target, "-P", "-F", "#{pane_id}"}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ResizePaneZoom toggles zoom on target pane/window.
func (c *Client) ResizePaneZoom(ctx context.Context, target string) error {
	_, err := c.run(ctx, "resize-pane", "-Z", "-t", target)
	return err
}
