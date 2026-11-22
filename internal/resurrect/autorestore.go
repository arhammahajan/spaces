package resurrect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/tmux"
)

// AutoRestoreClient is the tmux surface required by AutoRestore. *tmux.Client
// implements it directly.
type AutoRestoreClient interface {
	TmuxRestorer
	Snapshot(ctx context.Context) (tmux.ServerSnapshot, error)
	HasClients(ctx context.Context) bool
	SwitchClient(ctx context.Context, session string) error
	RenameSession(ctx context.Context, oldName, newName string) error
	KillSession(ctx context.Context, name string) error
}

// AutoRestoreResult summarizes what AutoRestore recreated.
type AutoRestoreResult struct {
	Restored           []string
	Skipped            []string
	ActiveSession      string
	RemovedPlaceholder bool
	Errors             []error
}

// bootstrapShells is the set of interactive login shells expected in a fresh
// default tmux session ("0") right after a reboot.
var bootstrapShells = map[string]bool{
	"":      true,
	"sh":    true,
	"dash":  true,
	"bash":  true,
	"zsh":   true,
	"fish":  true,
	"nu":    true,
	"ksh":   true,
	"csh":   true,
	"tcsh":  true,
	"login": true,
}

// isSingleEmptyShellSession reports whether snap represents a brand-new tmux
// server with only a single untouched session (1 window, 1 plain shell pane,
// no running agent).
func isSingleEmptyShellSession(snap tmux.ServerSnapshot) (tmux.Session, bool) {
	if len(snap.Sessions) != 1 {
		return tmux.Session{}, false
	}
	sess := snap.Sessions[0]
	var wins []tmux.Window
	for _, w := range snap.Windows {
		if w.SessionName == sess.Name {
			wins = append(wins, w)
		}
	}
	if len(wins) != 1 {
		return tmux.Session{}, false
	}
	var panes []tmux.Pane
	for _, p := range snap.Panes {
		if p.SessionName == sess.Name {
			panes = append(panes, p)
		}
	}
	if len(panes) != 1 {
		return tmux.Session{}, false
	}
	p := panes[0]
	if strings.TrimSpace(p.SpacesApp) != "" || CanonicalAgent(p.CurrentCommand) != "" {
		return tmux.Session{}, false
	}
	cmd := strings.ToLower(strings.TrimPrefix(filepath.Base(strings.TrimSpace(p.CurrentCommand)), "-"))
	if !bootstrapShells[cmd] {
		return tmux.Session{}, false
	}
	return sess, true
}

// AutoRestore reopens only the spaces that were actively open (not exited) in
// the last saved OpenState, leaving exited space snapshots untouched on disk
// so they remain available to restore on demand from the Exited list.
//
// When the tmux server starts with only a default empty placeholder session
// ("0") — or a single empty shell session whose name matches one of the saved
// open spaces (e.g. `tmux new-session -A -s main`) — AutoRestore restores all
// open spaces (including full tabs/panes for that session), switches the
// attached client to the last active space, and removes the temporary
// placeholder session.
func AutoRestore(ctx context.Context, client AutoRestoreClient, store *Store, opts RestoreOptions) (AutoRestoreResult, error) {
	var res AutoRestoreResult
	state, err := store.LoadOpenState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return res, nil
		}
		return res, err
	}
	if len(state.Spaces) == 0 {
		return res, nil
	}

	snap, err := client.Snapshot(ctx)
	if err != nil {
		return res, err
	}

	wantSet := make(map[string]bool, len(state.Spaces))
	for _, name := range state.Spaces {
		wantSet[name] = true
	}

	placeholderName := ""
	placeholderOrigName := ""
	if sess, ok := isSingleEmptyShellSession(snap); ok {
		if wantSet[sess.Name] {
			// Fresh empty session with the same name as a saved open space
			// (e.g. `tmux` when "0" was saved, or `tmux new -A -s main`).
			// Rename it aside so we can restore the full saved layout for it.
			tempName := fmt.Sprintf("__spaces_bootstrap_%d", os.Getpid())
			if err := client.RenameSession(ctx, sess.Name, tempName); err == nil {
				placeholderName = tempName
				placeholderOrigName = sess.Name
			}
		} else if sess.Name == "0" {
			placeholderName = "0"
			placeholderOrigName = "0"
		}
	}

	running := make(map[string]bool, len(snap.Sessions))
	for _, s := range snap.Sessions {
		if placeholderName != "" && s.Name == placeholderOrigName {
			continue
		}
		running[s.Name] = true
	}

	restoreOpts := opts
	restoreOpts.Switch = false

	liveAfter := make(map[string]bool, len(running)+len(state.Spaces))
	for name := range running {
		liveAfter[name] = true
	}

	for _, name := range state.Spaces {
		if running[name] {
			res.Skipped = append(res.Skipped, name)
			continue
		}
		sp, err := store.Load(name)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("load %s: %w", name, err))
			continue
		}
		if err := Restore(ctx, client, sp, restoreOpts); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("restore %s: %w", name, err))
			continue
		}
		res.Restored = append(res.Restored, name)
		liveAfter[name] = true
	}

	if len(res.Restored) == 0 {
		// If we renamed the placeholder aside and nothing restored, restore its name.
		if placeholderName != "" && placeholderName != placeholderOrigName {
			_ = client.RenameSession(ctx, placeholderName, placeholderOrigName)
		}
		return res, nil
	}

	target := ""
	if state.ActiveSession != "" && liveAfter[state.ActiveSession] {
		target = state.ActiveSession
	} else {
		target = res.Restored[0]
	}
	res.ActiveSession = target

	// Switch the client to the last active space when starting from a blank
	// bootstrap placeholder (or when no explicit non-placeholder session existed).
	shouldSwitch := placeholderName != "" || len(running) == 0
	switched := false
	hasClients := client.HasClients(ctx)
	if shouldSwitch && target != "" && hasClients {
		if err := client.SwitchClient(ctx, target); err == nil {
			switched = true
		}
	}

	// Clean up the temporary bootstrap placeholder session once we have either
	// switched the attached client away from it or confirmed no client is
	// attached (e.g., `tmux new-session -d`).
	if placeholderName != "" && (switched || !hasClients) {
		if err := client.KillSession(ctx, placeholderName); err == nil {
			res.RemovedPlaceholder = true
		}
	}
	return res, nil
}
