package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSharedSkillsFrontmatterAndExtras(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared")
	skillsDir := filepath.Join(root, "skills")

	writeTestFile(t, filepath.Join(skillsDir, "bro", "SKILL.md"), `---
name: bro
description: Restate the last message in plain human language, with no jargon.
disable-model-invocation: true
---

Restate your last message clearly.
`)
	writeTestFile(t, filepath.Join(skillsDir, "make-bot-ui", "SKILL.md"), `---
name: make-bot-ui
description: >-
  Use when building a custom UI that wakes a bot
  over a webhook.
disable-model-invocation: true
---

# How to make a bot UI

Body text here.
`)
	writeTestFile(t, filepath.Join(skillsDir, "ts-rules", "SKILL.md"), `---
name: typescript-best-practices
description: "TypeScript rules with \"strict\" typing."
paths: ["**/*.ts", "**/*.tsx"]
---

Always prefer strict types.
`)
	writeTestFile(t, filepath.Join(skillsDir, "ts-rules", "references", "guide.md"), "# Guide\n")
	writeTestFile(t, filepath.Join(skillsDir, "ts-rules", "scripts", "check.sh"), "#!/bin/sh\n")
	// Non-skill files in skills/ should be ignored.
	writeTestFile(t, filepath.Join(skillsDir, "UPSTREAM.md"), "Upstream info\n")

	snap, err := LoadShared(root, "/home/u")
	if err != nil {
		t.Fatalf("LoadShared: %v", err)
	}
	if !snap.RootExists {
		t.Fatalf("RootExists = false")
	}
	if len(snap.Skills) != 3 {
		t.Fatalf("len(Skills) = %d, want 3: %+v", len(snap.Skills), snap.Skills)
	}

	// Sorted alphabetically by Name: bro, make-bot-ui, typescript-best-practices.
	if snap.Skills[0].Name != "bro" || !snap.Skills[0].DisableModelInvocation {
		t.Errorf("skill[0] = %+v", snap.Skills[0])
	}
	if snap.Skills[1].Name != "make-bot-ui" || snap.Skills[1].Description != "Use when building a custom UI that wakes a bot over a webhook." {
		t.Errorf("skill[1] = %+v", snap.Skills[1])
	}
	ts := snap.Skills[2]
	if ts.Name != "typescript-best-practices" || ts.DirName != "ts-rules" {
		t.Errorf("skill[2] name/dir = %q/%q", ts.Name, ts.DirName)
	}
	if ts.Description != `TypeScript rules with "strict" typing.` {
		t.Errorf("skill[2] desc = %q", ts.Description)
	}
	if len(ts.Paths) != 2 || ts.Paths[0] != "**/*.ts" || ts.Paths[1] != "**/*.tsx" {
		t.Errorf("skill[2] paths = %v", ts.Paths)
	}
	if !ts.HasReferences || !ts.HasScripts || ts.ExtraFiles != 2 {
		t.Errorf("skill[2] extras = refs:%v scripts:%v count:%d", ts.HasReferences, ts.HasScripts, ts.ExtraFiles)
	}
	if ts.DisableModelInvocation {
		t.Errorf("skill[2] should be auto-invocable")
	}
}

func TestLoadSharedDirectMCPs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared")
	writeTestFile(t, filepath.Join(root, "mcp.json"), `{
  "mcpServers": {
    "agent-browser": {
      "command": "${HOME}/.local/bin/agent-browser",
      "args": ["mcp", "--tools", "core,debug"],
      "description": "Shared browser control"
    }
  }
}`)
	writeTestFile(t, filepath.Join(root, "mcp", "docs.json"), `{
  "url": "https://mcp.example.com/sse",
  "transport": "sse",
  "description": "Docs search server"
}`)

	snap, err := LoadShared(root, "/home/u")
	if err != nil {
		t.Fatalf("LoadShared: %v", err)
	}
	if len(snap.MCPs) != 2 {
		t.Fatalf("len(MCPs) = %d, want 2: %+v", len(snap.MCPs), snap.MCPs)
	}
	if snap.MCPs[0].Name != "agent-browser" || snap.MCPs[0].Transport != "stdio" || snap.MCPs[0].SourceKind != "shared" {
		t.Errorf("mcp[0] = %+v", snap.MCPs[0])
	}
	if snap.MCPs[1].Name != "docs" || snap.MCPs[1].Transport != "sse" || snap.MCPs[1].URL != "https://mcp.example.com/sse" {
		t.Errorf("mcp[1] = %+v", snap.MCPs[1])
	}
}

func TestLoadSharedSiblingHarnessMCPFallbackAndDedup(t *testing.T) {
	harnesses := filepath.Join(t.TempDir(), "harnesses")
	shared := filepath.Join(harnesses, "shared")
	if err := os.MkdirAll(filepath.Join(shared, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, filepath.Join(harnesses, "claude", "mcp.json"), `{
  "mcpServers": {
    "agent-browser": {
      "command": "${HOME}/.local/bin/agent-browser",
      "args": ["mcp", "--tools", "core,debug"]
    }
  }
}`)
	writeTestFile(t, filepath.Join(harnesses, "pi", "mcp.json"), `{
  "mcpServers": {
    "agent-browser": {
      "command": "${HOME}/.local/bin/agent-browser",
      "args": ["mcp", "--tools", "core,debug"],
      "exposure": "deferred",
      "description": "agent-browser controls for the existing shared Helium browser."
    }
  }
}`)
	writeTestFile(t, filepath.Join(harnesses, "opencode", "opencode.json"), `{
  "mcp": {
    "servers": {
      "agent-browser": {
        "type": "local",
        "command": ["${HOME}/.local/bin/agent-browser", "mcp", "--tools", "core,debug"]
      }
    }
  }
}`)
	writeTestFile(t, filepath.Join(harnesses, "kilo", "kilo.json"), `{
  "mcp": {
    "agent-browser": {
      "type": "local",
      "command": ["${HOME}/.local/bin/agent-browser", "mcp"],
      "enabled": true
    }
  }
}`)
	writeTestFile(t, filepath.Join(harnesses, "codex", "config.toml"), `
[mcp_servers.agent-browser]
command = "${HOME}/.local/bin/agent-browser"
args = ["mcp", "--tools", "core,debug"]
`)
	writeTestFile(t, filepath.Join(harnesses, "vibe", "config.toml"), `
[[mcp_servers]]
name = "agent-browser"
transport = "stdio"
command = "${HOME}/.local/bin/agent-browser"
args = [
    "mcp",
    "--tools",
    "core,debug",
]
`)
	// Gemini has an unrelated "mcp": {"excluded": []} block that must not be parsed as a server.
	writeTestFile(t, filepath.Join(harnesses, "gemini", "settings.json"), `{
  "mcp": { "excluded": [] },
  "mcpServers": {
    "agent-browser": {
      "command": "${HOME}/.local/bin/agent-browser",
      "args": ["mcp"]
    }
  }
}`)

	snap, err := LoadShared(shared, "/home/u")
	if err != nil {
		t.Fatalf("LoadShared: %v", err)
	}
	if len(snap.MCPs) != 1 {
		t.Fatalf("len(MCPs) = %d, want 1: %+v", len(snap.MCPs), snap.MCPs)
	}
	m := snap.MCPs[0]
	if m.Name != "agent-browser" || m.SourceKind != "harnesses" {
		t.Errorf("mcp = %+v", m)
	}
	if m.Description != "agent-browser controls for the existing shared Helium browser." {
		t.Errorf("description = %q", m.Description)
	}
	if m.Exposure != "deferred" {
		t.Errorf("exposure = %q", m.Exposure)
	}
	wantHarnesses := "claude,codex,gemini,kilo,opencode,pi,vibe"
	if got := strings.Join(m.Harnesses, ","); got != wantHarnesses {
		t.Errorf("harnesses = %q, want %q", got, wantHarnesses)
	}
}

func TestDefaultSharedDirShadowHomeFallback(t *testing.T) {
	base := t.TempDir()
	realShared := filepath.Join(base, "dev", "dots", "harnesses", "shared")
	if err := os.MkdirAll(realShared, 0o755); err != nil {
		t.Fatal(err)
	}
	shadowHome := filepath.Join(base, ".harness", "profiles", "antigravity", "work", "home")
	t.Setenv("TMUX_SPACES_SHARED_DIR", "")

	got := DefaultSharedDir("", shadowHome)
	if got != realShared {
		t.Errorf("DefaultSharedDir shadow fallback = %q, want %q", got, realShared)
	}

	gotExplicit := DefaultSharedDir("~/custom/shared", "/home/u")
	if gotExplicit != "/home/u/custom/shared" {
		t.Errorf("DefaultSharedDir explicit = %q", gotExplicit)
	}
}

func TestDefaultSharedDirAndLoadSharedFromStowedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TMUX_SPACES_SHARED_DIR", "")

	// Stow-resolved ~/.agents/skills and harness MCP configs in $HOME.
	writeTestFile(t, filepath.Join(home, ".agents", "skills", "bro", "SKILL.md"), `---
name: bro
description: Quick second opinion.
---

Body.
`)
	writeTestFile(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), `{
  "mcpServers": {
    "agent-browser": {
      "command": "/usr/local/bin/agent-browser",
      "args": ["mcp"]
    }
  }
}`)
	writeTestFile(t, filepath.Join(home, ".codex", "config.toml"), `
[mcp_servers.agent-browser]
command = "/usr/local/bin/agent-browser"
args = ["mcp"]
`)

	// Shadow profile under ~/.harness/ resolves to real $HOME/.agents.
	shadowHome := filepath.Join(home, ".harness", "profiles", "antigravity", "work", "home")
	wantRoot := filepath.Join(home, ".agents")
	if got := DefaultSharedDir("", shadowHome); got != wantRoot {
		t.Fatalf("DefaultSharedDir shadow = %q, want %q", got, wantRoot)
	}

	snap, err := LoadShared("", shadowHome)
	if err != nil {
		t.Fatalf("LoadShared: %v", err)
	}
	if snap.RootDir != wantRoot || len(snap.Skills) != 1 || snap.Skills[0].Name != "bro" {
		t.Fatalf("unexpected skills snap: %+v", snap)
	}
	if len(snap.MCPs) != 1 || snap.MCPs[0].Name != "agent-browser" {
		t.Fatalf("unexpected MCPs: %+v", snap.MCPs)
	}
	if got := strings.Join(snap.MCPs[0].Harnesses, ","); got != "antigravity,codex" {
		t.Fatalf("harnesses = %q, want antigravity,codex", got)
	}
}

func TestFuzzyScoreShared(t *testing.T) {
	sk := SharedSkill{
		Name:        "pstack-tdd",
		DirName:     "pstack-tdd",
		Description: "Write a failing test first, then make it pass.",
	}
	score, hl, ok := FuzzyScoreSharedSkill(sk, []string{"tdd"})
	if !ok || score <= 0 || len(hl) != 3 {
		t.Fatalf("FuzzyScoreSharedSkill = (%d, %v, %v)", score, hl, ok)
	}
	if _, _, ok := FuzzyScoreSharedSkill(sk, []string{"nonexistent"}); ok {
		t.Fatalf("expected non-match")
	}

	mcp := SharedMCP{
		Name:        "agent-browser",
		Command:     "~/.local/bin/agent-browser",
		Args:        []string{"mcp"},
		Description: "Controls Helium browser",
		Harnesses:   []string{"claude", "codex"},
	}
	ms, mhl, mok := FuzzyScoreSharedMCP(mcp, []string{"browser", "helium"})
	if !mok || ms <= 0 || len(mhl) == 0 {
		t.Fatalf("FuzzyScoreSharedMCP = (%d, %v, %v)", ms, mhl, mok)
	}
}
