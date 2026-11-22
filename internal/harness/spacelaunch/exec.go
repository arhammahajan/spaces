package spacelaunch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Injectable seams. Tests replace these; production uses the defaults.
// They mirror zjcli's seams; this package keeps its own so neither
// package's contract changes.
var (
	execCommand           = exec.Command
	stdout      io.Writer = os.Stdout
	stderr      io.Writer = os.Stderr
	stdin       io.Reader = os.Stdin
	sleep                 = time.Sleep
	selfPath              = func() string {
		if exe, err := os.Executable(); err == nil && exe != "" {
			return exe
		}
		return os.Args[0]
	}
)

const (
	// ExitOK and ExitError mirror zjcli's codes.
	ExitOK    = 0
	ExitError = 1
)

func dief(format string, args ...interface{}) int {
	fmt.Fprintf(stderr, "zj: "+format+"\n", args...)
	return ExitError
}

// zellijBin honors ZELLIJ_BIN like the rest of the helper surface.
func zellijBin() string {
	if v := os.Getenv("ZELLIJ_BIN"); v != "" {
		return v
	}
	return "zellij"
}

func zellijCmd(args ...string) *exec.Cmd {
	return execCommand(zellijBin(), args...)
}

func zellijOutput(args ...string) (string, error) {
	cmd := zellijCmd(args...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	return string(out), err
}

func zellijRunExit(args ...string) int {
	cmd := zellijCmd(args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = stdin
	if err := cmd.Run(); err != nil {
		return ExitError
	}
	return ExitOK
}

func zellijBinAvailable() bool {
	bin := zellijBin()
	if strings.Contains(bin, "/") {
		fi, err := os.Stat(bin)
		return err == nil && fi.Mode()&0o111 != 0
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// parseSessionList filters `list-sessions -n` output: drop EXITED lines,
// take the first whitespace field, drop blanks. Mirrors zjcli.
func parseSessionList(out string) []string {
	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "EXITED") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		sessions = append(sessions, fields[0])
	}
	return sessions
}

func runningSessions() []string {
	out, err := zellijOutput("list-sessions", "-n")
	if err != nil {
		return nil
	}
	return parseSessionList(out)
}

func sessionExists(name string) bool {
	if name == "" {
		return false
	}
	for _, s := range runningSessions() {
		if s == name {
			return true
		}
	}
	return false
}

// sessionHasClient mirrors zjcli: any output line past the header means a
// client is attached.
func sessionHasClient(name string) bool {
	out, err := zellijOutput("--session", name, "action", "list-clients")
	if err != nil {
		return false
	}
	lines := strings.Split(out, "\n")
	if len(lines) > 0 {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "")) != ""
}

func sanitizeName(s string) string {
	var b strings.Builder
	prevDash := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' {
			b.WriteByte(c)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "agents"
	}
	if len(name) > 48 {
		name = name[:48]
	}
	return name
}

func plainPWD() string {
	if pwd := os.Getenv("PWD"); pwd != "" {
		return pwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func binOnPath(bin string) bool {
	if strings.Contains(bin, "/") {
		fi, err := os.Stat(bin)
		return err == nil && fi.Mode()&0o111 != 0
	}
	_, err := exec.LookPath(bin)
	return err == nil
}
