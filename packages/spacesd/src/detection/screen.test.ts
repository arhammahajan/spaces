import { describe, expect, test } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { compilePattern, translatePattern } from "./regex";
import {
  afterCurrentPromptBlockMarker,
  currentCodexPromptIndex,
  currentPromptBlockMarker,
  extractRegion,
  validateRegionSpec,
  type DetectionInput,
} from "./regions";
import {
  agentIsKnown,
  canonicalApp,
  detect,
  detectManifest,
  hasEvidence,
  hasManifest,
  loadBundledScreenEngine,
  normalizeApp,
  oscAuthority,
  ScreenEngine,
  unknownDetected,
  type DetectedScreen,
  type Gate,
  type ManifestFile,
} from "./screen";

const BUNDLED_MANIFESTS_PATH = path.resolve(
  import.meta.dir,
  "../../../../internal/screen/manifests.json",
);

function readBundledManifests(): ManifestFile {
  const raw = fs.readFileSync(BUNDLED_MANIFESTS_PATH, "utf8");
  return JSON.parse(raw) as ManifestFile;
}

describe("ScreenEngine manifest loading & compilation", () => {
  test("bundled manifests load (23 manifests and expected aliases)", () => {
    const file = readBundledManifests();
    expect(file.manifests).toHaveLength(23);

    const eng = loadBundledScreenEngine();
    expect(eng.manifests).toHaveLength(23);

    for (const id of [
      "agy",
      "claude",
      "codex",
      "cursor",
      "deepseek",
      "kilo",
      "kimi",
      "muse",
      "opencode",
      "copilot",
      "gemini",
      "amp",
    ]) {
      expect(eng.manifest(id)).toBeDefined();
    }

    expect(eng.manifest("claude-code")).toBe(eng.manifest("claude"));
    expect(eng.manifest("cursor-agent")).toBe(eng.manifest("cursor"));
    expect(eng.manifest("antigravity")).toBe(eng.manifest("agy"));
    expect(eng.manifest("github-copilot")).toBe(eng.manifest("copilot"));
    expect(eng.manifest("dsh")).toBe(eng.manifest("deepseek"));
    expect(eng.manifest("dsh-tui")).toBe(eng.manifest("deepseek"));
  });

  test("every pattern in manifests.json compiles cleanly", () => {
    const file = readBundledManifests();
    const bad: string[] = [];
    let checked = 0;

    const walk = (gate: Gate, where: string): void => {
      for (const p of gate.regex ?? []) {
        checked += 1;
        try {
          compilePattern(p);
        } catch (err) {
          bad.push(`${where} regex ${p}: ${err instanceof Error ? err.message : String(err)}`);
        }
      }
      for (const p of gate.line_regex ?? []) {
        checked += 1;
        try {
          compilePattern(p);
        } catch (err) {
          bad.push(
            `${where} line_regex ${p}: ${err instanceof Error ? err.message : String(err)}`,
          );
        }
      }
      for (const nested of gate.all ?? []) {
        walk(nested, `${where}.all`);
      }
      for (const nested of gate.any ?? []) {
        walk(nested, `${where}.any`);
      }
      for (const nested of gate.not ?? []) {
        walk(nested, `${where}.not`);
      }
    };

    for (const m of file.manifests) {
      expect(m.rules.length).toBeGreaterThan(0);
      for (const r of m.rules) {
        walk(r, `${m.id}/${r.id}`);
      }
    }

    expect(checked).toBeGreaterThan(0);
    expect(bad).toEqual([]);
  });

  test("translatePattern & compilePattern", () => {
    expect(translatePattern(`^\\s*[\\u2801-\\u28FF]+\\s+\\S`)).toEqual({
      pattern: `^\\s*[\\u2801-\\u28FF]+\\s+\\S`,
      flags: "u",
    });
    expect(translatePattern(`^[\\x{2800}-\\x{28FF}] `)).toEqual({
      pattern: `^[\\u{2800}-\\u{28FF}] `,
      flags: "u",
    });
    expect(translatePattern(`(?i)^\\s*╰\\s+\\S+`)).toEqual({
      pattern: `^\\s*╰\\s+\\S+`,
      flags: "iu",
    });
    expect(translatePattern(`(?m)^\\s*─{3,}`)).toEqual({
      pattern: `^\\s*─{3,}`,
      flags: "mu",
    });
    expect(translatePattern(`(?s).+`)).toEqual({
      pattern: `.+`,
      flags: "su",
    });
    expect(translatePattern(`(?im)\\Astart\\z`)).toEqual({
      pattern: `(?<![\\s\\S])start(?![\\s\\S])`,
      flags: "imu",
    });
    expect(translatePattern(`(?is)foo.*\\z`)).toEqual({
      pattern: `foo.*(?![\\s\\S])`,
      flags: "isu",
    });
    expect(translatePattern(`(?si)(?m)foo`)).toEqual({
      pattern: `foo`,
      flags: "imsu",
    });
    expect(translatePattern(`\\\\A \\\\z \\\\x{2800} \\\\u2800 literal`)).toEqual({
      pattern: `\\\\A \\\\z \\\\x{2800} \\\\u2800 literal`,
      flags: "u",
    });
    expect(translatePattern(`big \\U0001F600 end`)).toEqual({
      pattern: `big \\u{0001F600} end`,
      flags: "u",
    });
    expect(translatePattern(`mark \\u{fe0f}? end`)).toEqual({
      pattern: `mark \\u{fe0f}? end`,
      flags: "u",
    });
    expect(translatePattern(`\\p{Alphabetic}+\\w*ing\\b`)).toEqual({
      pattern: `\\p{Alphabetic}+\\w*ing\\b`,
      flags: "u",
    });

    // Invalid escapes are preserved so compilePattern fails loudly.
    expect(() => compilePattern(`short \\u12 tail`)).toThrow();
    expect(() => compilePattern(`bad \\u{xyz} end`)).toThrow();

    // Braille spinner patterns match natively under /u.
    for (const p of [
      `^\\s*[\\u2801-\\u28FF]+\\s+\\S`,
      `^\\s*(⬡|⬢|[\\u2800-\\u28FF]+)\\s+\\p{Alphabetic}+\\w*ing\\b`,
      `^[\\x{2800}-\\x{28FF}] `,
    ]) {
      const re = compilePattern(p);
      expect(re.test("⠋ Working on it")).toBe(true);
    }

    // (?m) combined with \z only matches absolute end of string, not before trailing newline.
    const multilineEndRe = compilePattern(`(?m)^end$\\z`);
    expect(multilineEndRe.test("start\nend")).toBe(true);
    expect(multilineEndRe.test("start\nend\n")).toBe(false);

    // Escaped \\A does not act as start-of-string anchor.
    const escapedAnchor = compilePattern(`\\\\A`);
    expect(escapedAnchor.test("foo\\Abar")).toBe(true);
    expect(escapedAnchor.test("foo")).toBe(false);
  });

  test("engine load errors (all 10 cases) and alias collision behavior", () => {
    const errorCases: Record<string, string> = {
      empty: `{}`,
      "empty object": `{"source":"t","engine_version":3,"manifests":[]}`,
      "no rules": `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[]}]}`,
      duplicate: `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r"}]},{"id":"X","rules":[{"id":"r"}]}]}`,
      "bad region": `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","region":"middle_lines(3)"}]}]}`,
      "bad state": `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","state":"sleeping"}]}]}`,
      "bad regex": `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","regex":["(unclosed"]}]}]}`,
      "bad field": `{"source":"t","engine_version":3,"manifests":[{"id":"x","rules":[{"id":"r","bogus":1}]}]}`,
      "not json": `definitely not json`,
      "min_engine_version < 3 with top_non_empty_lines": `{"source":"t","engine_version":3,"manifests":[{"id":"x","min_engine_version":2,"rules":[{"id":"r","region":"top_non_empty_lines(5)","contains":["a"]}]}]}`,
    };

    for (const [name, bundle] of Object.entries(errorCases)) {
      expect(() => ScreenEngine.fromJson(bundle), `${name} should throw`).toThrow();
    }

    // Duplicate caused by earlier alias matching later manifest id also throws.
    expect(() =>
      ScreenEngine.fromJson(
        `{"source":"t","engine_version":3,"manifests":[{"id":"a","aliases":["b"],"rules":[{"id":"r"}]},{"id":"b","rules":[{"id":"r"}]}]}`,
      ),
    ).toThrow(/duplicate manifest id b/);

    // Aliases keep the first manifest on collision.
    const eng = ScreenEngine.fromJson(
      `{"source":"t","engine_version":3,"manifests":[{"id":"a","rules":[{"id":"r"}]},{"id":"b","aliases":["a"],"rules":[{"id":"r"}]}]}`,
    );
    expect(eng.manifest("a")?.id).toBe("a");
  });
});

describe("App normalization, canonicalization & authority helpers", () => {
  test("state strings", () => {
    const valid: Array<DetectedScreen["state"]> = ["idle", "working", "blocked", "unknown"];
    for (const s of valid) {
      const eng = ScreenEngine.fromJson(
        `{"source":"t","engine_version":3,"manifests":[{"id":"z","rules":[{"id":"r","state":"${s}","contains":["x"]}]}]}`,
      );
      expect(eng.detect("z", "x").state).toBe(s);
    }
    expect(unknownDetected("").state).toBe("unknown");
  });

  test("canonicalApp", () => {
    const cases: Record<string, string> = {
      "claude-code": "claude",
      claude: "claude",
      "cursor-agent": "cursor",
      antigravity: "agy",
      "github-copilot": "copilot",
      "dsh-tui": "deepseek",
      dsh: "deepseek",
      "fx-router": "fx",
      "vibe-acp": "vibe",
      "kimi-cli": "kimi",
      "kimi-code": "kimi",
      deepseek: "deepseek",
      "some-unknown": "some-unknown",
      "/usr/local/bin/claude-code": "claude",
      "CLAUDE-CODE": "claude",
      "opencode.exe": "opencode",
      "codex.js": "codex",
      "foo/": "foo",
    };
    for (const [input, want] of Object.entries(cases)) {
      expect(canonicalApp(input)).toBe(want);
    }
  });

  test("agentIsKnown", () => {
    for (const app of [
      "claude",
      "claude-code",
      "cursor-agent",
      "deepseek",
      "dsh",
      "dsh-tui",
      "gemini",
      "codex",
      "opencode",
      "amp",
      "antigravity",
      "kilo",
      "kimi",
      "muse",
    ]) {
      expect(agentIsKnown(app)).toBe(true);
    }
    for (const app of ["some-unknown-app", ""]) {
      expect(agentIsKnown(app)).toBe(false);
    }
  });

  test("oscAuthority", () => {
    for (const app of [
      "claude-code",
      "mcode",
      "fx-router",
      "vibe-acp",
      "dsh-tui",
      "deepseek",
      "muse",
      "codex",
      "opencode",
      "pi",
      "crush",
      "grok",
    ]) {
      expect(oscAuthority(app)).toBe(true);
    }
    for (const app of [
      "cursor-agent",
      "cursor",
      "gemini",
      "amp",
      "cline",
      "kilo",
      "unknown-x",
    ]) {
      expect(oscAuthority(app)).toBe(false);
    }
  });

  test("normalizeApp", () => {
    const table: Record<string, string> = {
      "": "",
      "  Claude-Code  ": "claude-code",
      "/usr/local/bin/opencode": "opencode",
      "codex.js": "codex",
      "agent.EXE": "agent",
      "run.mjs": "run",
      "trail/": "trail",
      "a/b/cursor-agent": "cursor-agent",
      "UPPER.CMD": "upper",
      "keep.batx": "keep.batx",
      "C:\\tools\\gemini": "c:\\tools\\gemini",
      "herdr:opencode": "herdr:opencode",
      "muse-bin-1.4.24-R9131.1": "muse",
    };
    for (const [input, want] of Object.entries(table)) {
      expect(normalizeApp(input)).toBe(want);
    }
  });

  test("hasEvidence", () => {
    expect(hasEvidence(unknownDetected())).toBe(false);
    expect(hasEvidence({ ...unknownDetected(), visibleIdle: true })).toBe(true);
    expect(hasEvidence({ ...unknownDetected(), visibleBlocker: true })).toBe(true);
    expect(hasEvidence({ ...unknownDetected(), visibleWorking: true })).toBe(true);
  });

  test("unknown manifest has no rules", () => {
    const eng = loadBundledScreenEngine();
    expect(eng.detect("no-such-agent", "anything at all")).toEqual(unknownDetected(""));
  });

  test("detectManifest evaluates rules without extracting title", () => {
    expect(detectManifest("claude", "   \n", "")).toEqual(unknownDetected(""));
    const working =
      "❯ Implement feature\n✽ Working…\n───────────────\n❯\n───────────────\n  ⏵ auto mode · esc to interrupt\n";
    const dm = detectManifest("claude-code", working, "");
    expect(dm.state).toBe("working");
    expect(dm.visibleWorking).toBe(true);
    expect(dm.title).toBe("");

    const full = detect("claude-code", working, "");
    expect(full.state).toBe("working");
    expect(full.title).toBe("Implement feature");
  });
});

describe("Regions extraction & validation", () => {
  const input = (screen: string): DetectionInput => ({ screen });

  test("regions extract sections (bottom_lines & bottom_non_empty_lines)", () => {
    const screen = "top\n───\n│ a │\n───\nlast\n";
    expect(extractRegion("bottom_lines(2)", input(screen))).toBe("───\nlast\n");
    expect(extractRegion("bottom_non_empty_lines(1)", input(screen))).toBe("last\n");
    expect(extractRegion("bottom_non_empty_lines(2)", input("one\n\n\ntwo\n"))).toBe(
      "one\n\n\ntwo\n",
    );
    expect(extractRegion("bottom_lines(0)", input(screen))).toBe("");
    expect(extractRegion("bottom_lines(99)", input(screen))).toBe(screen);
  });

  test("top_non_empty_lines region & engine version gate", () => {
    const screen = "first\n\nsecond\nthird\n";
    expect(extractRegion("top_non_empty_lines(2)", input(screen))).toBe("first\n\nsecond\n");
    expect(extractRegion("top_non_empty_lines(01)", input(screen))).toBe("");
    expect(validateRegionSpec("top_non_empty_lines(01)").ok).toBe(false);
    expect(validateRegionSpec("top_non_empty_lines(65536)").ok).toBe(false);
    expect(validateRegionSpec("top_non_empty_lines(65535)").ok).toBe(true);

    const badBundle = `{"source":"t","engine_version":3,"manifests":[
      {"id":"x","min_engine_version":2,"rules":[{"id":"r","region":"top_non_empty_lines(5)","contains":["a"]}]},
      {"id":"y","rules":[{"id":"r","region":"top_non_empty_lines(5)","contains":["a"]}]}]}`;
    expect(() => ScreenEngine.fromJson(badBundle)).toThrow(/top_non_empty_lines/);

    const okBundle = `{"source":"t","engine_version":3,"manifests":[
      {"id":"y","rules":[{"id":"r","region":"top_non_empty_lines(5)","contains":["a"]}]}]}`;
    expect(() => ScreenEngine.fromJson(okBundle)).not.toThrow();
  });

  test("OSC title & progress regions", () => {
    const withTitle: DetectionInput = {
      screen: "screen text\n",
      oscTitle: "✳ working",
      oscProgress: "50%",
    };
    expect(extractRegion("osc_title", withTitle)).toBe("✳ working");
    expect(extractRegion("osc_progress", withTitle)).toBe("50%");
  });

  test("horizontal rule regions", () => {
    const screen = "work output\n───\n│ prompt │\n───\n❯ input\n";
    expect(extractRegion("prompt_box_body", input(screen))).toBe("│ prompt │\n");
    expect(extractRegion("above_prompt_box", input(screen))).toBe("work output\n");
    expect(extractRegion("last_non_empty_above_prompt_box", input(screen))).toBe("work output");
    expect(extractRegion("after_last_horizontal_rule", input(screen))).toBe("❯ input\n");
    expect(extractRegion("prompt_box_body", input("no rules here\n"))).toBe("");
    expect(extractRegion("after_last_horizontal_rule", input("no rules here\n"))).toBe(
      "no rules here\n",
    );
  });

  test("prompt marker regions", () => {
    const screen = "old\n› prompt one\n• did x\n› current\noutput\n";
    expect(extractRegion("after_last_prompt_marker", input(screen))).toBe("output\n");
    expect(extractRegion("before_current_prompt_marker", input(screen))).toBe(
      "old\n› prompt one\n• did x\n",
    );
    expect(extractRegion("whole_recent_without_current_prompt_marker", input(screen))).toBe("");
    expect(extractRegion("current_prompt_block_marker", input(screen))).toBe("• did x");

    const screen2 = "old\n› current\n";
    expect(extractRegion("whole_recent_without_current_prompt_marker", input(screen2))).toBe("");

    const screen3 = "› do it\n• Running\n✓ Done\n› next\ntail\n";
    expect(currentPromptBlockMarker(screen3)).toBe("✓ Done");
    const afterBlock = afterCurrentPromptBlockMarker(screen3);
    expect(afterBlock).not.toBeNull();
    expect(afterBlock!.startsWith("✓ Done")).toBe(true);

    expect(currentCodexPromptIndex("› a\n• b\n› c\n• d\n".split("\n"))).toBe(-1);
  });
});

describe("Rule evaluation & detection behavior", () => {
  test("priority, gates, and not gates", () => {
    const bundle = `{
      "source":"test","engine_version":3,
      "manifests":[{"id":"claude","rules":[
        {"id":"low","state":"idle","priority":1,"contains":["match"]},
        {"id":"high","state":"working","priority":10,"contains":["match"],
         "all":[{"any":[{"regex":["w[io]n"]},{"contains":["fallback"]}]}],
         "not":[{"contains":["blocked"]}]},
        {"id":"tie","state":"blocked","priority":10,"visible_blocker":true,"contains":["match","tiebreak"]},
        {"id":"line","state":"blocked","priority":20,"visible_blocker":true,"line_regex":["^exact line$"]}
      ]}]
    }`;
    const eng = ScreenEngine.fromJson(bundle);

    const high = eng.detect("claude", "match win");
    expect(high.state).toBe("working");
    expect(high.matchedRule).toBe("high");

    const blocked = eng.detect("claude", "match win blocked");
    expect(blocked.state).toBe("idle");

    // Tie at priority 10: first rule wins.
    const tie = eng.detect("claude", "match win tiebreak");
    expect(tie.state).toBe("working");
    expect(tie.matchedRule).toBe("high");

    const line = eng.detect("claude", "before\nexact line\nafter");
    expect(line.state).toBe("blocked");
    expect(line.visibleBlocker).toBe(true);

    // Case-insensitive contains vs case-sensitive regex.
    expect(eng.detect("claude", "MATCH win").state).toBe("working");
    const caseFail = eng.detect("claude", "match WIN");
    expect(caseFail.state).toBe("idle");
    expect(caseFail.matchedRule).toBe("low");
  });

  test("skip rules reported", () => {
    const bundle = `{
      "source":"test","engine_version":3,
      "manifests":[{"id":"claude","rules":[
        {"id":"viewer","state":"unknown","skip_state_update":true,"contains":["showing detailed transcript"]}
      ]}]
    }`;
    const eng = ScreenEngine.fromJson(bundle);
    const d = eng.detect("claude", "showing detailed transcript");
    expect(d.skipStateUpdate).toBe(true);
    expect(d.state).toBe("unknown");
    expect(hasEvidence(d)).toBe(false);
  });

  test("rule defaults & visible flag isolation", () => {
    const eng = ScreenEngine.fromJson(
      `{"source":"t","engine_version":3,"manifests":[{"id":"z","rules":[{"id":"r","contains":["needle"]}]}]}`,
    );
    const d = eng.detect("z", "a needle here\n");
    expect(d.state).toBe("unknown");
    expect(d.matchedRule).toBe("r");

    const eng2 = ScreenEngine.fromJson(
      `{"source":"t","engine_version":3,"manifests":[{"id":"z","rules":[{"id":"r","state":"idle","visible_blocker":true,"visible_idle":true,"contains":["needle"]}]}]}`,
    );
    const d2 = eng2.detect("z", "a needle here\n");
    expect(d2.visibleIdle).toBe(true);
    expect(d2.visibleBlocker).toBe(false);
    expect(hasEvidence(d2)).toBe(true);
  });

  test("detect empty screens", () => {
    for (const screen of ["", "   ", "\n\n  \n", "\r\n"]) {
      const d = detect("claude", screen, "");
      expect(d.state).toBe("unknown");
      expect(hasEvidence(d)).toBe(false);
      expect(d.matchedRule).toBe("");
    }
  });

  test("detect unknown app", () => {
    const d = detect(
      "some-unknown-app",
      "● Working on the task\n  ⎿  running tests\n\n───\n❯\n",
      "",
    );
    expect(d.state).toBe("unknown");
    expect(hasEvidence(d)).toBe(false);
  });

  test("gemini blocker", () => {
    const screen = "│ Apply this change\n│ Allow execution\n│ Do you want to proceed?\n";
    const d = detect("gemini", screen, "");
    expect(d.state).toBe("blocked");
    expect(d.visibleBlocker).toBe(true);
  });

  test("cursor blocker", () => {
    const screen =
      "Waiting for approval to run this command?\n  Run (once) (y)\n  Skip (esc or n)\n";
    const d = detect("cursor-agent", screen, "");
    expect(d.state).toBe("blocked");
    expect(d.visibleBlocker).toBe(true);
  });

  test("claude working and blocked", () => {
    const working =
      "● Extracting skill names from upstream trees\n" +
      "  ⎿  $ cd /tmp\n" +
      " \n" +
      "✽ Pontificating… (2m 12s · ↓ 4.3k tokens)\n" +
      " \n" +
      "───────────────\n" +
      "❯\n" +
      "───────────────\n" +
      "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt\n";
    const dw = detect("claude-code", working, "");
    expect(dw.state).toBe("working");
    expect(dw.visibleWorking).toBe(true);

    const blocked =
      "──────────────────────────────\n" +
      " Allow this read outside the working directories?\n" +
      " ❯ 1. Yes, and keep allowing any reads\n" +
      "   2. No, and block reads\n" +
      " \n" +
      " Esc to cancel · Tab to amend\n";
    const db = detect("claude-code", blocked, "");
    expect(db.state).toBe("blocked");
  });

  test("plain shell no agent", () => {
    const d = detect("gemini", "$ ls\nCargo.toml  src\n$ ", "");
    expect(d.state).toBe("unknown");
    expect(hasEvidence(d)).toBe(false);
  });

  test("amp OSC title", () => {
    const working = detect("amp", "some viewport text\n", "⠋ working on task");
    expect(working.state).toBe("working");
    expect(working.visibleWorking).toBe(true);

    const blocked = detect(
      "amp",
      "some viewport text\n",
      "Plugin confirmation needed: edit file?",
    );
    expect(blocked.state).toBe("blocked");
    expect(blocked.visibleBlocker).toBe(true);

    const prio = detect("amp", "some viewport text\n", "⠋ Plugin confirmation needed");
    expect(prio.state).toBe("blocked");
  });

  test("codex prompt marker regions", () => {
    const screen = "› do the thing\n• Running tests\npress enter to confirm or esc to cancel\n";
    const d = detect("codex", screen, "");
    expect(d.state).toBe("blocked");
    expect(d.visibleBlocker).toBe(true);

    const screen2 =
      "› earlier\n↑/↓ to scroll pgup/pgdn to home/end to jump q to quit esc to edit prev\n";
    const eng = loadBundledScreenEngine();
    const d2 = eng.detect("codex", screen2, "", "");
    expect(d2.skipStateUpdate).toBe(true);
    expect(d2.state).toBe("unknown");
  });

  test("opencode permission blocked", () => {
    const screen =
      "△ Permission required\n↑↓ select · enter confirm · esc dismiss\nedit src/main.rs\n";
    const d = detect("opencode", screen, "");
    expect(d.state).toBe("blocked");
    expect(d.visibleBlocker).toBe(true);
  });

  test("generic noise is unknown across all manifests except cline", () => {
    const noise = "hello world\njust a plain shell with $ prompts\nand some output 12345\n";
    const file = readBundledManifests();
    for (const m of file.manifests) {
      if (m.id === "cline") {
        continue;
      }
      const d = detect(m.id, noise, "");
      expect(d.state, `${m.id} noise state`).toBe("unknown");
      expect(hasEvidence(d), `${m.id} noise evidence`).toBe(false);
    }
  });

  test("translated spinner patterns end to end", () => {
    const cases: Array<{ app: string; screen: string }> = [
      { app: "agy", screen: "doing things\n⠋ Working on it\n" },
      { app: "kimi", screen: "⠙ thinking...\n" },
      { app: "qodercli", screen: "⠋ Loading context\n" },
      { app: "droid", screen: "⠋ working\nesc to stop\n" },
    ];
    for (const tc of cases) {
      const d = detect(tc.app, tc.screen, "");
      expect(d.state, tc.app).toBe("working");
      expect(d.visibleWorking, tc.app).toBe(true);
    }
  });

  test("antigravity idle prompt and working priority", () => {
    const idleViewport = [
      "  Done with the change.",
      "────────────────────────────────────────────────────────────────────────────────",
      ">",
      "────────────────────────────────────────────────────────────────────────────────",
      "? for shortcuts                                    auto · Gemini 4 Hydrogen · medium",
    ].join("\n");
    const d = detect("antigravity", idleViewport, "");
    expect(d.state).toBe("idle");
    expect(d.visibleIdle).toBe(true);

    const workingViewport = [
      "⠋ Thinking...",
      "────────────────────────────────────────────────────────────────────────────────",
      ">",
      "──────────────── esc to cancel ────────────────",
    ].join("\n");
    const dw = detect("antigravity", workingViewport, "");
    expect(dw.state).toBe("working");
    expect(dw.visibleWorking).toBe(true);
  });

  test("nine provider manifests blocked, working, and idle matrix", () => {
    interface Sample {
      screen: string;
      oscTitle?: string;
    }
    const cases: Array<{
      id: string;
      alias: string;
      blocked: Sample;
      working: Sample;
      idle: Sample;
    }> = [
      {
        id: "agy",
        alias: "antigravity",
        blocked: {
          screen:
            "Requesting permission for: rm -rf tmp\n↑↓ navigate · Tab amend · Enter confirm\n",
        },
        working: {
          screen: "⠋ Thinking...\n───────────────\n>\n─── esc to cancel ───\n",
        },
        idle: {
          screen: "Done.\n───────────────\n>\n───────────────\n? for shortcuts · auto\n",
        },
      },
      {
        id: "claude",
        alias: "claude-code",
        blocked: {
          screen: "───────────────\nAllow Bash command?\nEnter to confirm · Esc to cancel\n",
        },
        working: {
          screen: "✽ Working… (12s)\n───────────────\n❯\n───────────────\n  ⏵ auto mode · esc to interrupt\n",
        },
        idle: {
          screen: "Done.\n───────────────\n❯\n───────────────\n",
        },
      },
      {
        id: "codex",
        alias: "codex",
        blocked: {
          screen: "› run tests\n• Running command\npress enter to confirm or esc to cancel\n",
        },
        working: {
          screen: "› run tests\n",
          oscTitle: "⠋ codex",
        },
        idle: {
          screen: "› \n",
          oscTitle: "codex",
        },
      },
      {
        id: "cursor",
        alias: "cursor-agent",
        blocked: {
          screen:
            "Waiting for approval to run this command?\n  Run (once) (y)\n  Skip (esc or n)\n→ Add a follow-up\n/ commands · @ files\n",
        },
        working: {
          screen:
            "⠋ Thinking about changes\n→ Add a follow-up\n/ commands · @ files · ctrl+c to stop\n",
        },
        idle: {
          screen: "Done editing file.\n→ Add a follow-up\n/ commands · @ files · ! shell\n",
        },
      },
      {
        id: "deepseek",
        alias: "dsh-tui",
        blocked: {
          screen:
            "Permission required to run shell command\nAllow once · Deny\nEnter to confirm · Esc to cancel\n❯\ndeepseek-reasoner · /help\n",
        },
        working: {
          screen:
            "⠋ Reasoning through the diff...\n❯\nesc to interrupt · deepseek-reasoner\n",
        },
        idle: {
          screen:
            "Completed.\n───────────────\n❯\n───────────────\ndeepseek-reasoner · ~/repo · /help for commands\n",
        },
      },
      {
        id: "kilo",
        alias: "kilo-code",
        blocked: {
          screen:
            "△ Permission required\n↑↓ select · enter confirm · esc dismiss\n┃  Ask anything...\n╹▀▀▀▀▀▀\n",
        },
        working: {
          screen: "┃  Ask anything...\n╹▀▀▀▀▀▀\n  esc interrupt · tab agents\n",
        },
        idle: {
          screen:
            "   ┃\n   ┃  Ask anything...\n   ┃  Build  claude-sonnet · medium\n   ╹▀▀▀▀▀▀\n   tab agents  ctrl+p commands\n",
        },
      },
      {
        id: "kimi",
        alias: "kimi-code",
        blocked: {
          screen:
            "Run this command?\n▶ Approve once\n  Reject\n1/2 choose · ↵ confirm\n✨\nkimi-k2 · context: 12%\n",
        },
        working: {
          screen: "⠙ thinking...\n✨\nkimi-k2 thinking [1 agent running]\n",
        },
        idle: {
          screen:
            "All set.\n───────────────\n✨\n───────────────\nkimi-k2.5 · context: 12% · ctrl-x: mode · /help\n",
        },
      },
      {
        id: "muse",
        alias: "muse-code",
        blocked: {
          screen: "Allow once\nAllow for this session\n⟩\n",
        },
        working: {
          screen: "Working on task\n⟩\nesc to interrupt\n",
        },
        idle: {
          screen: "Done.\n⟩\nmuse-spark · high · ~/repo · Summary\n",
        },
      },
      {
        id: "opencode",
        alias: "open-code",
        blocked: {
          screen:
            "△ Permission required\n↑↓ select · enter confirm · esc dismiss\n┃  Ask anything...\n╹▀▀▀▀▀▀\n",
        },
        working: {
          screen:
            "■■■■■■ working\n┃  Ask anything...\n╹▀▀▀▀▀▀\n  esc to interrupt · ctrl+p commands\n",
        },
        idle: {
          screen:
            "   ┃\n   ┃  Ask anything...\n   ┃  Build  gpt-5.4 OpenAI · high\n   ╹▀▀▀▀▀▀\n   tab agents  ctrl+p commands\n",
        },
      },
    ];

    for (const tc of cases) {
      expect(hasManifest(tc.id), `hasManifest(${tc.id})`).toBe(true);
      expect(hasManifest(tc.alias), `hasManifest(${tc.alias})`).toBe(true);
      expect(agentIsKnown(tc.id), `agentIsKnown(${tc.id})`).toBe(true);

      const db = detect(tc.alias, tc.blocked.screen, tc.blocked.oscTitle ?? "");
      expect(db.state, `${tc.id} blocked state`).toBe("blocked");
      expect(db.visibleBlocker, `${tc.id} blocked flag`).toBe(true);

      const dw = detect(tc.alias, tc.working.screen, tc.working.oscTitle ?? "");
      expect(dw.state, `${tc.id} working state`).toBe("working");
      expect(dw.visibleWorking, `${tc.id} working flag`).toBe(true);

      const di = detect(tc.alias, tc.idle.screen, tc.idle.oscTitle ?? "");
      expect(di.state, `${tc.id} idle state`).toBe("idle");
      expect(di.visibleIdle, `${tc.id} idle flag`).toBe(true);
    }
  });
});
