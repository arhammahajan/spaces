package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/cli"
	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/resurrect"
	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/worktree"
)

const spacesRestoredOption = "@spaces_restored"

func saveSpacesToStore(ctx context.Context, client *tmux.Client, store *resurrect.Store, onlySpace string) (int, error) {
	// Never overwrite open-space state while startup auto-restore is in progress.
	if onlySpace == "" {
		if status, err := client.GetGlobalOption(ctx, spacesRestoredOption); err == nil && status == "pending" {
			return 0, nil
		}
	}
	snap, err := newTUIBackend(client).ResolveSnapshot(ctx)
	if err != nil {
		snap, err = client.Snapshot(ctx)
		if err != nil {
			return 0, err
		}
	}
	if len(snap.Sessions) == 0 {
		return 0, nil
	}
	now := time.Now()
	var hooks *model.HookSnapshot
	if hs, err := model.FetchHooks(ctx); err == nil && hs.OK {
		hooks = &hs
	}
	captured := resurrect.CaptureSpaces(snap, hooks, now)
	wtMgr := worktree.NewManager()
	saved := 0
	for _, sp := range captured {
		if onlySpace != "" && sp.Name != onlySpace {
			continue
		}
		if sp.WorktreePath != "" && sp.MainRepoRoot == "" {
			if repo, rerr := wtMgr.DiscoverRepo(ctx, sp.WorktreePath); rerr == nil && repo != nil {
				sp.MainRepoRoot = repo.MainRoot
			}
		}
		if err := store.Save(sp); err != nil {
			return saved, err
		}
		saved++
	}
	if onlySpace == "" {
		_ = store.SaveOpenState(resurrect.CaptureOpenState(snap, now))
		_ = store.TouchAutoSave()
	}
	return saved, nil
}

func runResurrectCLI(client *tmux.Client, store *resurrect.Store, selfPath string, env cli.Env, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintf(env.Stdout, `usage: tmux-spaces resurrect <subcommand> [args]

  save [SPACE]               snapshot all running spaces (or one space)
  autosave [--force]         save all spaces if the 30s interval has elapsed
  auto-restore               restore spaces that were last open (not exited)
  list [--json]              list exited/resurrectable space snapshots
  restore <SPACE | --all>    restore a saved space (and resume AI agents)
  delete <SPACE>             delete a saved space snapshot
`)
		return cli.ExitOK
	}

	sub, rest := args[0], args[1:]
	timeout := 15 * time.Second
	if sub == "auto-restore" || (sub == "restore" && len(rest) > 0 && rest[0] == "--all") {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch sub {
	case "save":
		only := ""
		if len(rest) > 0 {
			only = strings.TrimSpace(rest[0])
		}
		n, err := saveSpacesToStore(ctx, client, store, only)
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces resurrect save: %v\n", err)
			return cli.ExitError
		}
		fmt.Fprintf(env.Stdout, "saved %d space(s)\n", n)
		return cli.ExitOK

	case "autosave":
		force := len(rest) > 0 && (rest[0] == "--force" || rest[0] == "-f")
		if !force && !store.ShouldAutoSave() {
			return cli.ExitOK
		}
		if _, err := saveSpacesToStore(ctx, client, store, ""); err != nil {
			return cli.ExitOK
		}
		return cli.ExitOK

	case "auto-restore":
		defer func() {
			_ = client.SetGlobalOption(ctx, spacesRestoredOption, "done")
		}()
		// Wait briefly for tmux's initial session/client if launched during
		// ~/.tmux.conf startup via `run-shell -b`.
		waitForInitialTmuxReady(ctx, client)
		res, err := resurrect.AutoRestore(ctx, client, store, resurrect.RestoreOptions{
			SelfPath: selfPath,
		})
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces resurrect auto-restore: %v\n", err)
			return cli.ExitError
		}
		if len(res.Restored) > 0 {
			_ = store.TouchAutoSave()
		}
		fmt.Fprintf(env.Stdout, "restored %d space(s)\n", len(res.Restored))
		return cli.ExitOK

	case "list":
		asJSON := len(rest) > 0 && rest[0] == "--json"
		running := map[string]bool{}
		if snap, err := client.Snapshot(ctx); err == nil {
			for _, s := range snap.Sessions {
				running[s.Name] = true
			}
		}
		exited, err := store.ListExited(running)
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces resurrect list: %v\n", err)
			return cli.ExitError
		}
		if asJSON {
			raw, _ := json.Marshal(exited)
			fmt.Fprintf(env.Stdout, "%s\n", raw)
			return cli.ExitOK
		}
		if len(exited) == 0 {
			fmt.Fprintf(env.Stdout, "no exited spaces\n")
			return cli.ExitOK
		}
		for _, ex := range exited {
			agents := "-"
			if len(ex.Agents) > 0 {
				agents = strings.Join(ex.Agents, ",")
			}
			fmt.Fprintf(env.Stdout, "%-22s %2dw %2dp  %-20s  %s\n", ex.Name, ex.WindowCount, ex.PaneCount, agents, ex.CWD)
		}
		return cli.ExitOK

	case "restore":
		if len(rest) == 0 {
			fmt.Fprintf(env.Stderr, "usage: tmux-spaces resurrect restore <SPACE | --all>\n")
			return cli.ExitUsage
		}
		opts := resurrect.RestoreOptions{
			SelfPath: selfPath,
			Switch:   rest[0] != "--all" && client.InsideTmux(),
		}
		if rest[0] == "--all" {
			running := map[string]bool{}
			if snap, err := client.Snapshot(ctx); err == nil {
				for _, s := range snap.Sessions {
					running[s.Name] = true
				}
			}
			all, err := store.ListAll()
			if err != nil {
				fmt.Fprintf(env.Stderr, "tmux-spaces resurrect restore: %v\n", err)
				return cli.ExitError
			}
			restored := 0
			for _, sp := range all {
				if running[sp.Name] {
					continue
				}
				if err := resurrect.Restore(ctx, client, sp, opts); err != nil {
					fmt.Fprintf(env.Stderr, "tmux-spaces resurrect restore %s: %v\n", sp.Name, err)
					continue
				}
				_ = store.AddOpenSpace(sp.Name, false)
				restored++
			}
			fmt.Fprintf(env.Stdout, "restored %d space(s)\n", restored)
			return cli.ExitOK
		}
		name := rest[0]
		sp, err := store.Load(name)
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces resurrect restore: %v\n", err)
			return cli.ExitError
		}
		if err := resurrect.Restore(ctx, client, sp, opts); err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces resurrect restore: %v\n", err)
			return cli.ExitError
		}
		_ = store.AddOpenSpace(name, opts.Switch)
		fmt.Fprintf(env.Stdout, "restored %s\n", name)
		return cli.ExitOK

	case "delete":
		if len(rest) == 0 {
			fmt.Fprintf(env.Stderr, "usage: tmux-spaces resurrect delete <SPACE>\n")
			return cli.ExitUsage
		}
		if err := store.Delete(rest[0]); err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces resurrect delete: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK

	default:
		fmt.Fprintf(env.Stderr, "tmux-spaces resurrect: unknown subcommand %q\n", sub)
		return cli.ExitUsage
	}
}

// waitForInitialTmuxReady waits briefly (up to 400ms) when auto-restore is
// invoked during ~/.tmux.conf loading so tmux finishes creating its initial
// session and attaching the client before we inspect/switch sessions.
func waitForInitialTmuxReady(ctx context.Context, client *tmux.Client) {
	for i := 0; i < 8; i++ {
		if snap, err := client.Snapshot(ctx); err == nil && len(snap.Sessions) > 0 && client.HasClients(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}
