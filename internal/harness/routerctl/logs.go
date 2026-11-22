// Direct-mode and journald log access for the router daemon.
package routerctl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
)

// Logs prints router logs to Options.Stdout. In systemd mode it runs
// journalctl --user -u <unit> with -n N and optional -f. In direct mode it
// prints the last N lines of LogPath; follow on a file is unsupported.
func Logs(ctx context.Context, opts Options, follow bool, lines int) error {
	if systemdManaged(ctx, opts) {
		args := []string{"--user", "-u", opts.unit()}
		if lines > 0 {
			args = append(args, "-n", strconv.Itoa(lines))
		}
		if follow {
			args = append(args, "-f")
		}
		if err := opts.runOutFn()(ctx, opts.stdout(), opts.journalctl(), args...); err != nil {
			return fmt.Errorf("journalctl --user -u %s: %w", opts.unit(), err)
		}
		return nil
	}
	if follow {
		return fmt.Errorf("follow is not supported for direct-mode log files (re-run under the systemd unit for live logs)")
	}
	return printTail(opts, lines)
}

// printTail writes the last lines of the direct-mode log file. A
// non-positive lines value prints the whole file.
func printTail(opts Options, lines int) error {
	f, err := os.Open(opts.LogPath)
	if err != nil {
		return fmt.Errorf("open log %s: %w", opts.LogPath, err)
	}
	defer f.Close()
	out := opts.stdout()
	if lines <= 0 {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			fmt.Fprintln(out, sc.Text())
		}
		return sc.Err()
	}
	ring := make([]string, lines)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		ring[n%len(ring)] = sc.Text()
		n++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	start := 0
	if n > len(ring) {
		start = n - len(ring)
	}
	for i := start; i < n; i++ {
		fmt.Fprintln(out, ring[i%len(ring)])
	}
	return nil
}
