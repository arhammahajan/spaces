import { describe, expect, test } from "bun:test";
import { detect } from "./screen";
import {
  cleanViewportTitle,
  CUSTOM_TITLE_EXTRACTORS,
  extractAntigravityTitle,
  extractClaudeTitle,
  extractCodexTitle,
  extractCursorTitle,
  extractDeepSeekTitle,
  extractGenericTitle,
  extractKimiTitle,
  extractMultilinePrompt,
  extractMuseTitle,
  extractOpencodeTitle,
  extractPromptMarkerLine,
  extractTitle,
  isEffortSegment,
  isPathSegment,
  splitLines,
} from "./titles";

describe("ExtractTitle from viewports (internal/screen/title_test.go)", () => {
  test("codex status line and prompt fallback", () => {
    const withStatus =
      "• Done.\n\n" +
      "› Write tests for @filename\n\n" +
      "  gpt-5.4 high · ~/dev/dots · Remove extra T3Code providers\n";
    const d = detect("codex", withStatus, "");
    expect(d.title).toBe("Remove extra T3Code providers");

    const promptOnly =
      "› Inspect the tmux-spaces conversation title priority\n\n" +
      "• Working on it...\n" +
      "  gpt-5.4 high · ~/dev/model-router\n";
    const d2 = detect("codex", promptOnly, "");
    expect(d2.title).toBe("Inspect the tmux-spaces conversation title priority");
  });

  test("muse status line", () => {
    const viewport =
      "  ❯ \n\n" +
      "  muse-spark · high · ~ · Researching Muse Conversation Titles   9.3k/256.0k (4%)\n";
    const d = detect("muse", viewport, "");
    expect(d.title).toBe("Researching Muse Conversation Titles");
  });

  test("opencode top tab bar and prompt box", () => {
    const withTabBar =
      "  ‹2  OpenCode Zen Default   Orca Tmux-Spaces Redesign   +    ● 2   /status\n" +
      "   ┃\n" +
      "   ┃  update the inbox preview\n" +
      "   ┃\n";
    const d = detect("opencode", withTabBar, "");
    expect(d.title).toBe("Orca Tmux-Spaces Redesign");

    const promptBoxOnly =
      "   ┃\n" +
      "   ┃  Fix the fallback title extraction in opencode\n" +
      "   ┃  Build  gpt-5.4 OpenAI · high\n" +
      "   ╹▀▀▀▀▀▀\n";
    const d2 = detect("opencode", promptBoxOnly, "");
    expect(d2.title).toBe("Fix the fallback title extraction in opencode");
  });

  test("antigravity prompt", () => {
    const viewport =
      "────────────────────────────────────────────────────────────────────────────────\n" +
      "> currently the tmux plugin i believe is capturing window title of the harness\n" +
      "  to get the working title.\n\n" +
      "● Thinking...\n";
    const d = detect("antigravity", viewport, "");
    expect(d.title).toBe(
      "currently the tmux plugin i believe is capturing window title of the harness to get the working title.",
    );
  });

  test("claude prompt ignores suggestion placeholder", () => {
    const viewport =
      "❯ Implement screen reader title fallback\n" +
      "● Running tests\n" +
      "───────────────\n" +
      '❯ Try "refactor view.go"\n' +
      "───────────────\n";
    const d = detect("claude-code", viewport, "");
    expect(d.title).toBe("Implement screen reader title fallback");
  });

  test("deepseek prompt fallback", () => {
    const viewport =
      "❯ Add deepseek screen manifest rules\n" +
      "Done.\n" +
      "───────────────\n" +
      "❯\n" +
      "───────────────\n" +
      "deepseek-reasoner · ~/dev/spaces · /help\n";
    const d = detect("dsh-tui", viewport, "");
    expect(d.title).toBe("Add deepseek screen manifest rules");
  });
});

describe("Custom plugin title extractors & helpers", () => {
  test("cursor custom title extractor (headers & prompt fallbacks)", () => {
    expect(extractCursorTitle("Cursor Agent · Fix sidebar focus\n› ignored prompt\n")).toBe(
      "Fix sidebar focus",
    );
    expect(extractCursorTitle("Cursor · Refactor auth middleware\n")).toBe(
      "Refactor auth middleware",
    );
    expect(extractCursorTitle("Session: Debug flaky watcher\n")).toBe("Debug flaky watcher");
    expect(extractCursorTitle("› add cursor mcp target\n")).toBe("add cursor mcp target");
    expect(extractTitle("cursor-agent", "Cursor Agent · Fix sidebar focus\n")).toBe(
      "Fix sidebar focus",
    );
    expect(detect("cursor-agent", "› add cursor mcp target\n").title).toBe(
      "add cursor mcp target",
    );
  });

  test("deepseek custom title extractor (headers & prompt fallbacks)", () => {
    expect(extractDeepSeekTitle("dsh · Refactor plugin registry\n❯ ignored prompt\n")).toBe(
      "Refactor plugin registry",
    );
    expect(extractDeepSeekTitle("DeepSeek · Optimize SQLite queries\n")).toBe(
      "Optimize SQLite queries",
    );
    expect(extractDeepSeekTitle("dsh-tui · Tune context threshold\n")).toBe(
      "Tune context threshold",
    );
    expect(extractDeepSeekTitle("Session: Inspect WAL checkpoints\n")).toBe(
      "Inspect WAL checkpoints",
    );
    expect(extractDeepSeekTitle("❯ add deepseek manifest\n")).toBe("add deepseek manifest");
    expect(extractTitle("dsh", "dsh · Refactor plugin registry\n")).toBe(
      "Refactor plugin registry",
    );
  });

  test("kimi custom title extractor (headers & prompt fallbacks)", () => {
    expect(extractKimiTitle("Kimi Code · Implement parity tests\n❯ ignored prompt\n")).toBe(
      "Implement parity tests",
    );
    expect(extractKimiTitle("Kimi · Port screen regions\n")).toBe("Port screen regions");
    expect(extractKimiTitle("Session: Audit title fallbacks\n")).toBe("Audit title fallbacks");
    expect(extractKimiTitle("❯ verify kimi prompt parsing\n")).toBe(
      "verify kimi prompt parsing",
    );
    expect(extractTitle("kimi-cli", "Kimi Code · Implement parity tests\n")).toBe(
      "Implement parity tests",
    );
  });

  test("CUSTOM_TITLE_EXTRACTORS exposes all 9 custom plugin extractors", () => {
    expect(CUSTOM_TITLE_EXTRACTORS.antigravity).toBe(extractAntigravityTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.agy).toBe(extractAntigravityTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.claude).toBe(extractClaudeTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.codex).toBe(extractCodexTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.cursor).toBe(extractCursorTitle);
    expect(CUSTOM_TITLE_EXTRACTORS["cursor-agent"]).toBe(extractCursorTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.deepseek).toBe(extractDeepSeekTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.kilo).toBe(extractOpencodeTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.kimi).toBe(extractKimiTitle);
    expect(CUSTOM_TITLE_EXTRACTORS["kimi-cli"]).toBe(extractKimiTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.muse).toBe(extractMuseTitle);
    expect(CUSTOM_TITLE_EXTRACTORS.opencode).toBe(extractOpencodeTitle);

    // Kilo shares OpenCode's top tab bar extractor.
    const kiloTabs = "  ‹3  Main   Fix Kilo Tabs +    ● 1\n";
    expect(extractTitle("kilo", kiloTabs)).toBe("Fix Kilo Tabs");
  });

  test("cleanViewportTitle, segment predicates & generic extractors", () => {
    expect(cleanViewportTitle("   ")).toBe("");
    expect(cleanViewportTitle("New Session")).toBe("");
    expect(cleanViewportTitle("new session - worktree")).toBe("");
    expect(cleanViewportTitle("Untitled")).toBe("");
    expect(cleanViewportTitle("Launch Overrides")).toBe("");
    expect(cleanViewportTitle("Ask Codex to do anything")).toBe("");
    expect(cleanViewportTitle("/help")).toBe("");
    expect(cleanViewportTitle("/model gpt-5")).toBe("");
    expect(cleanViewportTitle("/review fix failing test")).toBe("fix failing test");
    expect(cleanViewportTitle("  hello   world\t\nfoo ")).toBe("hello world foo");

    const longTitle = "🚀".repeat(165);
    const truncated = cleanViewportTitle(longTitle);
    expect(Array.from(truncated)).toHaveLength(160);
    expect(truncated.endsWith("…")).toBe(true);

    expect(isPathSegment("~")).toBe(true);
    expect(isPathSegment("~/dev/spaces")).toBe(true);
    expect(isPathSegment("/tmp/work")).toBe(true);
    expect(isPathSegment("./repo")).toBe(true);
    expect(isPathSegment("not-a-path")).toBe(false);

    expect(isEffortSegment("high")).toBe(true);
    expect(isEffortSegment("  XHIGH ")).toBe(true);
    expect(isEffortSegment("bogus")).toBe(false);

    expect(splitLines("  a\r\n  b\n")).toEqual(["  a", "  b"]);
    expect(extractPromptMarkerLine("› Auto mode: enabled\n› You are in /tmp\n› real prompt\n", "›")).toBe(
      "real prompt",
    );
    expect(
      extractMultilinePrompt(
        "> first line\n  second line\n  third line\n  fourth line\n  fifth line\n",
        ">",
      ),
    ).toBe("first line second line third line fourth line");
    expect(extractGenericTitle("just plain output\n")).toBe("");
    expect(extractTitle("claude", "   \n  ")).toBe("");
  });
});
