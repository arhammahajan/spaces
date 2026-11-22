package osc

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// TmuxEmitFunc projects an emitted attention record onto native `@spaces_*`
// tmux pane options when running inside a tmux pane (without Zellij). It is
// only invoked when TtyWriter is the default ttyDevice so unit tests with
// injected TtyWriters never shell out to tmux.
var TmuxEmitFunc = emitTmuxPaneOptions

// TmuxProfileFunc projects the active harness profile name onto the native
// `@spaces_profile` tmux pane option (or unsets it when empty). Like
// TmuxEmitFunc, it is only invoked when TtyWriter is the default ttyDevice.
var TmuxProfileFunc = setTmuxPaneProfile

// Out is the fallback record target: it receives OSC 7501 records
// when the tty is not usable. Tests replace it to capture the bytes.
var Out io.Writer = os.Stdout

// TtyWriter is the primary record target: the pane tty, tried first.
// The default lazily opens TtyPath per record and closes it again;
// a nil or failing TtyWriter makes every record fall back to Out.
// Tests inject buffers or failing writers here so /dev/tty is never touched.
var TtyWriter io.Writer = ttyDevice{}

// TtyPath is the device the default TtyWriter opens.
var TtyPath = "/dev/tty"

// ttyDevice is the production TtyWriter: /dev/tty opened lazily per
// record and closed right after. A failed open surfaces as a write error
// so Emit falls back to Out.
type ttyDevice struct{}

func (ttyDevice) Write(p []byte) (int, error) {
	f, err := os.OpenFile(TtyPath, os.O_WRONLY, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Write(p)
}

// TtyWritable reports whether the record target can accept bytes: a nil
// TtyWriter cannot, otherwise a zero-length probe write decides. The default
// device answers by opening TtyPath; injected test writers answer with their
// Write result.
func TtyWritable() bool {
	if TtyWriter == nil {
		return false
	}
	_, err := io.WriteString(TtyWriter, "")
	return err == nil
}

// FormatRecord builds one complete OSC 7501 escape sequence:
//
//	\x1b]7501;state=<state>:app=<app>[:profile=<profile>][:kind=<kind>][:msg=<base64>]\x1b\\
func FormatRecord(state, app, profile, kind, msg string) string {
	body := "state=" + state + ":app=" + app
	if profile != "" {
		body += ":profile=" + profile
	}
	if kind != "" {
		body += ":kind=" + kind
	}
	if msg != "" {
		body += ":msg=" + base64.StdEncoding.EncodeToString([]byte(msg))
	}
	return "\x1b]7501;" + body + "\x1b\\"
}

// WrapTmuxPassthrough wraps an escape sequence in tmux DCS passthrough
// framing (\x1bPtmux;...\x1b\\), doubling embedded ESC bytes so tmux forwards
// the sequence to the outer terminal when allow-passthrough is enabled.
func WrapTmuxPassthrough(seq string) string {
	if seq == "" {
		return ""
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// Emit writes one OSC 7501 record to TtyWriter, falling back to Out
// when TtyWriter is nil or its write fails. Inside a real tmux pane (when
// TtyWriter is the default ttyDevice), it also projects the record onto
// native `@spaces_*` pane options so direct shell launches without a
// pipe-pane watcher update `@spaces_app` and `@spaces_state` immediately.
func Emit(app, state, msg, kind string) {
	EmitWithProfile(app, "", state, msg, kind)
}

// EmitWithProfile writes one OSC 7501 record (including an optional profile
// field) to TtyWriter, falling back to Out when TtyWriter is nil or fails.
func EmitWithProfile(app, profile, state, msg, kind string) {
	rec := FormatRecord(state, app, profile, kind, msg)
	wroteTty := false
	if TtyWriter != nil {
		if _, err := io.WriteString(TtyWriter, rec); err == nil {
			wroteTty = true
		}
	}
	if !wroteTty {
		_, _ = io.WriteString(Out, rec)
	}
	if _, isDefaultDevice := TtyWriter.(ttyDevice); isDefaultDevice && TmuxEmitFunc != nil {
		TmuxEmitFunc(app, state, msg, kind)
	}
}

func emitTmuxPaneOptions(app, state, msg, kind string) {
	if os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || pane == "" {
		return
	}
	bin := os.Getenv("TMUX_BIN")
	if bin == "" {
		bin = "tmux"
	}
	run := func(args ...string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, bin, args...).Run()
	}
	run("set-option", "-p", "-t", pane, "@spaces_state", state)
	if app != "" {
		run("set-option", "-p", "-t", pane, "@spaces_app", app)
	}
	if kind != "" {
		run("set-option", "-p", "-t", pane, "@spaces_kind", kind)
	} else {
		run("set-option", "-p", "-u", "-t", pane, "@spaces_kind")
	}
	if msg != "" {
		run("set-option", "-p", "-t", pane, "@spaces_msg", msg)
	} else {
		run("set-option", "-p", "-u", "-t", pane, "@spaces_msg")
	}
	run("set-option", "-p", "-t", pane, "@spaces_ts", strconv.FormatInt(time.Now().UnixMilli(), 10))
	run("set-option", "-p", "-t", pane, "@spaces_source", "osc7501")
}

// SetTmuxProfile sets or unsets the `@spaces_profile` tmux pane option when
// running inside a tmux pane (without Zellij) and using the default ttyDevice.
func SetTmuxProfile(profile string) {
	if _, isDefaultDevice := TtyWriter.(ttyDevice); isDefaultDevice && TmuxProfileFunc != nil {
		TmuxProfileFunc(profile)
	}
}

func setTmuxPaneProfile(profile string) {
	if os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || pane == "" {
		return
	}
	bin := os.Getenv("TMUX_BIN")
	if bin == "" {
		bin = "tmux"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if profile != "" {
		_ = exec.CommandContext(ctx, bin, "set-option", "-p", "-t", pane, "@spaces_profile", profile).Run()
	} else {
		_ = exec.CommandContext(ctx, bin, "set-option", "-p", "-u", "-t", pane, "@spaces_profile").Run()
	}
}
