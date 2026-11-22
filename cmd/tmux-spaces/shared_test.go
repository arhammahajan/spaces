package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
)

func TestParseSharedOptions(t *testing.T) {
	opts := parseSharedOptions([]string{"--dir", "/tmp/shared", "--query", "browser"})
	if opts.InitialMode != tui.ModeShared || opts.SharedDir != "/tmp/shared" || opts.InitialQuery != "browser" {
		t.Fatalf("unexpected opts: %+v", opts)
	}
	opts2 := parseSharedOptions([]string{"--dir=/a/b", "--query=ketchup"})
	if opts2.InitialMode != tui.ModeShared || opts2.SharedDir != "/a/b" || opts2.InitialQuery != "ketchup" {
		t.Fatalf("unexpected opts2: %+v", opts2)
	}
}

func TestTUIBackendLoadSharedAndOpenFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared")
	skillFile := filepath.Join(root, "skills", "bro", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("---\nname: bro\ndescription: Plain language.\n---\n\nHi.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var newWindows [][]string
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			switch args[0] {
			case "display-message":
				return "work\n", "", 0, nil
			case "new-window":
				newWindows = append(newWindows, append([]string(nil), args...))
				return "", "", 0, nil
			}
			return "", "", 0, nil
		},
	}
	client := tmux.NewClient(runner)
	client.Getenv = func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		return ""
	}
	b := newTUIBackend(client)

	snap, err := b.LoadShared(context.Background(), root)
	if err != nil {
		t.Fatalf("LoadShared: %v", err)
	}
	if len(snap.Skills) != 1 || snap.Skills[0].Name != "bro" {
		t.Fatalf("unexpected skills: %+v", snap.Skills)
	}

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nvim")
	if err := b.OpenSharedFile(context.Background(), skillFile); err != nil {
		t.Fatalf("OpenSharedFile: %v", err)
	}
	if len(newWindows) != 1 {
		t.Fatalf("expected 1 new-window call, got %d", len(newWindows))
	}
	joined := strings.Join(newWindows[0], " ")
	for _, want := range []string{"-t work", "-n bro", "nvim", skillFile} {
		if !strings.Contains(joined, want) {
			t.Errorf("new-window args missing %q: %s", want, joined)
		}
	}
}
