import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { SharedMCP, SharedSkill } from "@spaces/types";
import {
  defaultSharedDir,
  fuzzyMatch,
  fuzzyMatchSharedMCP,
  fuzzyMatchSharedSkill,
  fuzzyScoreSharedMCP,
  fuzzyScoreSharedSkill,
  loadShared,
  loadSharedMCPs,
  loadSharedSkills,
  sharedMCPCommandLine,
} from "./index";

const tempDirs: string[] = [];

function makeTempDir(prefix = "spaces-shared-test-"): string {
  const dir = mkdtempSync(path.join(os.tmpdir(), prefix));
  tempDirs.push(dir);
  return dir;
}

function writeFile(filePath: string, content: string): void {
  mkdirSync(path.dirname(filePath), { recursive: true });
  writeFileSync(filePath, content, "utf8");
}

afterEach(() => {
  while (tempDirs.length > 0) {
    const dir = tempDirs.pop()!;
    rmSync(dir, { recursive: true, force: true });
  }
});

describe("defaultSharedDir", () => {
  test("prefers explicit dir and expands ~", () => {
    const home = makeTempDir();
    expect(defaultSharedDir("~/custom-shared", home, {})).toBe(path.join(home, "custom-shared"));
  });

  test("falls back to TMUX_SPACES_SHARED_DIR when explicit is empty", () => {
    const home = makeTempDir();
    const env = { TMUX_SPACES_SHARED_DIR: "$HOME/env-shared" };
    expect(defaultSharedDir("", home, env)).toBe(path.join(home, "env-shared"));
  });

  test("prefers <home>/.agents and unwraps shadow profile home to <realHome>/.agents", () => {
    const realHome = makeTempDir();
    const shadowHome = path.join(realHome, ".harness", "profiles", "claude", "work", "home");
    mkdirSync(shadowHome, { recursive: true });
    const realAgents = path.join(realHome, ".agents");
    mkdirSync(realAgents, { recursive: true });

    expect(defaultSharedDir("", shadowHome, {})).toBe(realAgents);

    // If shadow home has its own .agents, it wins over realHome/.agents.
    const shadowAgents = path.join(shadowHome, ".agents");
    mkdirSync(shadowAgents, { recursive: true });
    expect(defaultSharedDir("", shadowHome, {})).toBe(shadowAgents);
  });

  test("falls back to legacy dev/dots/harnesses/shared then <realHome>/.agents", () => {
    const home = makeTempDir();
    expect(defaultSharedDir("", home, {})).toBe(path.join(home, ".agents"));

    const legacy = path.join(home, "dev", "dots", "harnesses", "shared");
    mkdirSync(legacy, { recursive: true });
    expect(defaultSharedDir("", home, {})).toBe(legacy);
  });
});

describe("loadSharedSkills", () => {
  test("parses inline, block scalar, paths, fallback paragraph, extra files, and sorts", () => {
    const root = makeTempDir();
    const skillsDir = path.join(root, "skills");

    // 1. Inline frontmatter + inline paths + references & scripts + hidden file ignored
    writeFile(
      path.join(skillsDir, "zeta-skill", "SKILL.md"),
      [
        "---",
        'name: "Alpha Skill"',
        "description: Handles   inline   whitespace",
        "disable-model-invocation: true",
        'paths: ["src/**/*.ts", "packages/core"]',
        "icon: rocket",
        "color: '#89B4FA'",
        "---",
        "# Alpha",
        "",
        "Body text.",
      ].join("\n"),
    );
    writeFile(path.join(skillsDir, "zeta-skill", "references", "guide.md"), "ref");
    writeFile(path.join(skillsDir, "zeta-skill", "scripts", "run.sh"), "#!/bin/sh");
    writeFile(path.join(skillsDir, "zeta-skill", "scripts", ".secret"), "ignored");
    writeFile(path.join(skillsDir, "zeta-skill", "notes.txt"), "extra");

    // 2. Folded block scalar description + YAML list paths
    writeFile(
      path.join(skillsDir, "beta-skill", "skill.md"),
      [
        "---",
        "name: beta-skill",
        "description: >-",
        "  Multi-line folded",
        "  skill description.",
        "paths:",
        "  - cmd/**",
        "  - internal/**",
        "---",
        "More body.",
      ].join("\n"),
    );

    // 3. No description -> falls back to first markdown paragraph skipping headings & fences
    writeFile(
      path.join(skillsDir, "gamma-skill", "SKILL.md"),
      [
        "---",
        "paths: docs/**/*.md",
        "---",
        "# Heading",
        "",
        "```bash",
        "echo hi",
        "```",
        "",
        "First paragraph line one",
        "and line two.",
        "",
        "Second paragraph ignored.",
      ].join("\n"),
    );

    // 4. Hidden dir and dir without SKILL.md should be skipped
    writeFile(path.join(skillsDir, ".hidden", "SKILL.md"), "---\nname: Hidden\n---\n");
    mkdirSync(path.join(skillsDir, "empty-dir"), { recursive: true });

    const skills = loadSharedSkills(skillsDir);
    expect(skills.map((s) => s.name)).toEqual(["Alpha Skill", "beta-skill", "gamma-skill"]);

    const alpha = skills[0]!;
    expect(alpha.dir_name).toBe("zeta-skill");
    expect(alpha.description).toBe("Handles inline whitespace");
    expect(alpha.disable_model_invocation).toBe(true);
    expect(alpha.paths).toEqual(["src/**/*.ts", "packages/core"]);
    expect(alpha.icon).toBe("rocket");
    expect(alpha.color).toBe("#89B4FA");
    expect(alpha.has_references).toBe(true);
    expect(alpha.has_scripts).toBe(true);
    expect(alpha.extra_files).toBe(3); // guide.md, run.sh, notes.txt (not .secret, not SKILL.md)
    expect(alpha.mod_time_ms).toBeGreaterThan(0);

    const beta = skills[1]!;
    expect(beta.description).toBe("Multi-line folded skill description.");
    expect(beta.paths).toEqual(["cmd/**", "internal/**"]);

    const gamma = skills[2]!;
    expect(gamma.name).toBe("gamma-skill");
    expect(gamma.description).toBe("First paragraph line one and line two.");
    expect(gamma.paths).toEqual(["docs/**/*.md"]);
  });
});

describe("loadSharedMCPs", () => {
  test("Pass 1: direct JSON, TOML, nested mcp/ dir, and standalone server", () => {
    const root = makeTempDir();

    writeFile(
      path.join(root, "mcp.json"),
      JSON.stringify({
        mcpServers: {
          github: {
            command: "npx",
            args: ["-y", "@modelcontextprotocol/server-github"],
            description: "GitHub  API   server",
            env: { GITHUB_TOKEN: "tok" },
          },
          remote: {
            url: "https://mcp.example.com/sse",
            enabled: false,
          },
        },
      }),
    );

    writeFile(
      path.join(root, "mcp", "solo.json"),
      JSON.stringify({
        name: "SoloServer",
        command: ["uvx", "solo-mcp", "--port", "9000"],
        type: "local",
      }),
    );

    writeFile(
      path.join(root, "extra_mcp.toml"),
      [
        "# Top-level comment",
        "[mcp_servers.postgres]",
        'command = "npx" # inline comment',
        "args = [",
        '  "-y",',
        '  "@modelcontextprotocol/server-postgres", # trailing comment',
        "]",
        'description = "Postgres query server"',
        "",
        "[mcp_servers.postgres.env]",
        'PGHOST = "localhost"',
        "",
        "[[mcp_servers]]",
        'name = "linear"',
        'url = "https://mcp.linear.app"',
        "disabled = true",
      ].join("\n"),
    );

    const mcps = loadSharedMCPs(root, root);
    expect(mcps.map((m) => m.name)).toEqual(["github", "linear", "postgres", "remote", "SoloServer"]);

    const gh = mcps.find((m) => m.name === "github")!;
    expect(gh.transport).toBe("stdio");
    expect(gh.source_kind).toBe("shared");
    expect(gh.description).toBe("GitHub API server");
    expect(gh.env).toEqual({ GITHUB_TOKEN: "tok" });
    expect(sharedMCPCommandLine(gh)).toBe("npx -y @modelcontextprotocol/server-github");

    const remote = mcps.find((m) => m.name === "remote")!;
    expect(remote.transport).toBe("http");
    expect(remote.disabled).toBe(true);
    expect(sharedMCPCommandLine(remote)).toBe("https://mcp.example.com/sse");

    const solo = mcps.find((m) => m.name === "SoloServer")!;
    expect(solo.command).toBe("uvx");
    expect(solo.args).toEqual(["solo-mcp", "--port", "9000"]);
    expect(solo.transport).toBe("stdio");

    const pg = mcps.find((m) => m.name === "postgres")!;
    expect(pg.command).toBe("npx");
    expect(pg.args).toEqual(["-y", "@modelcontextprotocol/server-postgres"]);
    expect(pg.description).toBe("Postgres query server");

    const linear = mcps.find((m) => m.name === "linear")!;
    expect(linear.url).toBe("https://mcp.linear.app");
    expect(linear.transport).toBe("http");
    expect(linear.disabled).toBe(true);
  });

  test("Pass 2: sibling harnesses dedup, merge harnesses, prefer non-shell & dedicated *mcp*.json", () => {
    const harnessesRoot = makeTempDir();
    const sharedDir = path.join(harnessesRoot, "shared");
    mkdirSync(sharedDir, { recursive: true });

    // Harness A uses a shell wrapper in settings.json
    writeFile(
      path.join(harnessesRoot, "alpha", "settings.json"),
      JSON.stringify({
        mcpServers: {
          Context7: {
            command: "/bin/sh",
            args: ["-c", "npx context7"],
            env: { A: "1" },
          },
        },
      }),
    );

    // Harness B uses a direct command in mcp.json (case-insensitive same name)
    writeFile(
      path.join(harnessesRoot, "beta", "mcp.json"),
      JSON.stringify({
        mcpServers: {
          context7: {
            command: "npx",
            args: ["-y", "@upstash/context7-mcp"],
            description: "Docs lookup",
            env: { B: "2" },
          },
        },
      }),
    );

    // Harness C uses Kilo-style "mcp": { "<name>": { "command": [...] } }
    writeFile(
      path.join(harnessesRoot, "kilo", "kilo.json"),
      JSON.stringify({
        mcp: {
          context7: {
            command: ["npx", "-y", "@upstash/context7-mcp"],
          },
          excluded: [],
        },
      }),
    );

    const mcps = loadSharedMCPs(sharedDir, harnessesRoot);
    expect(mcps).toHaveLength(1);
    const c7 = mcps[0]!;
    expect(c7.source_kind).toBe("harnesses");
    expect(c7.harnesses).toEqual(["alpha", "beta", "kilo"]);
    expect(c7.command).toBe("npx");
    expect(c7.args).toEqual(["-y", "@upstash/context7-mcp"]);
    expect(c7.description).toBe("Docs lookup");
    expect(c7.env).toEqual({ A: "1", B: "2" });
    expect(path.basename(c7.source_path)).toBe("mcp.json");
  });

  test("Pass 3: stowed home harnesses under ~/.agents using custom mcpTargets", () => {
    const home = makeTempDir();
    const agentsDir = path.join(home, ".agents");
    mkdirSync(agentsDir, { recursive: true });

    writeFile(
      path.join(home, ".claude", "mcp.json"),
      JSON.stringify({
        mcpServers: {
          filesystem: { command: "npx", args: ["-y", "mcp-fs"] },
        },
      }),
    );
    writeFile(
      path.join(home, ".codex", "config.toml"),
      ['[mcp_servers.filesystem]', 'command = "npx"', 'args = ["-y", "mcp-fs"]'].join("\n"),
    );

    const mcps = loadSharedMCPs(agentsDir, home, [".claude/mcp.json", ".codex/config.toml"]);
    expect(mcps).toHaveLength(1);
    expect(mcps[0]!.name).toBe("filesystem");
    expect(mcps[0]!.harnesses).toEqual(["claude", "codex"]);
    expect(mcps[0]!.source_kind).toBe("harnesses");
  });

  test("loadShared returns snapshot with root_exists=false when root is missing", () => {
    const home = makeTempDir();
    const snap = loadShared({ dir: path.join(home, "does-not-exist"), homeDir: home });
    expect(snap.root_exists).toBe(false);
    expect(snap.mcps).toEqual([]);
    expect(snap.skills).toEqual([]);
  });
});

describe("fuzzy scoring", () => {
  const sampleSkill: SharedSkill = {
    name: "Technical Blog Editor",
    dir_name: "tech-blog",
    description: "Rewrites rough engineering notes into clear articles",
    path: "/tmp/skills/tech-blog/SKILL.md",
    dir_path: "/tmp/skills/tech-blog",
    disable_model_invocation: false,
    paths: ["docs/blog/**/*.md"],
    icon: "",
    color: "",
    extra_files: 0,
    has_references: false,
    has_scripts: false,
    body: "",
    mod_time_ms: 0,
  };

  const sampleMCP: SharedMCP = {
    name: "postgres",
    transport: "stdio",
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-postgres"],
    url: "",
    description: "SQL database query server",
    exposure: "",
    disabled: false,
    env: {},
    source_path: "/tmp/mcp.json",
    source_kind: "shared",
    harnesses: ["claude", "codex"],
    raw_json: "{}",
  };

  test("fuzzyMatch rewards prefix & boundary matches and rejects non-subsequences", () => {
    const prefix = fuzzyMatch("blog", "blog-editor");
    const mid = fuzzyMatch("blog", "my-super-blog");
    expect(prefix.matched).toBe(true);
    expect(mid.matched).toBe(true);
    expect(prefix.score).toBeGreaterThan(mid.score);
    expect(fuzzyMatch("xyz", "blog-editor").matched).toBe(false);
  });

  test("fuzzyScoreSharedSkill supports empty query, multi-token AND, and field weight ordering", () => {
    expect(fuzzyScoreSharedSkill(sampleSkill, "")).toBe(0);
    expect(fuzzyScoreSharedSkill(sampleSkill, "blog notes")).toBeGreaterThan(0);
    expect(fuzzyScoreSharedSkill(sampleSkill, "blog nonexistent")).toBe(-1);

    // Name match (base 260, mult 4) beats description-only match (base 100, mult 2)
    const nameScore = fuzzyScoreSharedSkill(sampleSkill, "Editor");
    const descScore = fuzzyScoreSharedSkill(sampleSkill, "articles");
    expect(nameScore).toBeGreaterThan(descScore);

    const matchInfo = fuzzyMatchSharedSkill(sampleSkill, "Blog");
    expect(matchInfo.matched).toBe(true);
    expect(matchInfo.highlights.length).toBe(4);
  });

  test("fuzzyScoreSharedMCP supports multi-token AND across name, command, transport, harnesses", () => {
    expect(fuzzyScoreSharedMCP(sampleMCP, "")).toBe(0);
    expect(fuzzyScoreSharedMCP(sampleMCP, "postgres claude stdio")).toBeGreaterThan(0);
    expect(fuzzyScoreSharedMCP(sampleMCP, "postgres missing")).toBe(-1);

    const nameScore = fuzzyScoreSharedMCP(sampleMCP, "postgres");
    const harnessScore = fuzzyScoreSharedMCP(sampleMCP, "codex");
    expect(nameScore).toBeGreaterThan(harnessScore);

    const matchInfo = fuzzyMatchSharedMCP(sampleMCP, "pg");
    expect(matchInfo.matched).toBe(true);
    expect(matchInfo.highlights).toEqual([0, 4]);
  });
});
