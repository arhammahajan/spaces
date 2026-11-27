import { describe, expect, test } from "bun:test";
import type { OscResumeHint } from "@spaces/types";
import {
  SAFE_RESTORE_COMMANDS,
  canonicalResumeHintPayload,
  generatePaneSecret,
  signOscResumeHint,
  validateResumeCommand,
  verifyOscResumeHint,
} from "./hmac";

describe("SAFE_RESTORE_COMMANDS", () => {
  test("matches internal/resurrect/restore.go allowlist", () => {
    expect(Array.from(SAFE_RESTORE_COMMANDS).sort()).toEqual([
      "btop",
      "emacs",
      "htop",
      "k9s",
      "lazygit",
      "less",
      "man",
      "nano",
      "nvim",
      "ranger",
      "tig",
      "top",
      "vi",
      "vim",
      "yazi",
    ]);
    expect(SAFE_RESTORE_COMMANDS.has("bash")).toBe(false);
    expect(SAFE_RESTORE_COMMANDS.has("sh")).toBe(false);
    expect(SAFE_RESTORE_COMMANDS.has("rm")).toBe(false);
  });
});

describe("OSC 7577 HMAC signing & verification", () => {
  test("generatePaneSecret produces 64-char lowercase hex secrets", () => {
    const s1 = generatePaneSecret();
    const s2 = generatePaneSecret();
    expect(s1).toMatch(/^[0-9a-f]{64}$/);
    expect(s2).toMatch(/^[0-9a-f]{64}$/);
    expect(s1).not.toBe(s2);
  });

  test("canonicalResumeHintPayload uses exact NUL-delimited v1 format", () => {
    const minimal: OscResumeHint = {
      agent: "claude",
      session_id: "sess_1",
    };
    expect(canonicalResumeHintPayload("%12", minimal)).toBe(
      "v1\0%12\0claude\0sess_1\0\0\0",
    );

    const full: OscResumeHint = {
      agent: "codex",
      session_id: "sess_2",
      profile: "work",
      cwd: "/repo/app",
      resume_target: "codex resume sess_2",
      thread_id: "thr-1",
      signature: "ignored",
      raw: "ignored",
    };
    expect(canonicalResumeHintPayload("%99", full)).toBe(
      "v1\0%99\0codex\0sess_2\0work\0/repo/app\0codex resume sess_2",
    );
  });

  test("signs and verifies valid hints, rejecting tampering and malformed signatures", () => {
    const secret = generatePaneSecret();
    const baseHint: OscResumeHint = {
      agent: "claude",
      session_id: "sess-abc",
      profile: "default",
      cwd: "/tmp/repo",
      resume_target: "claude --resume sess-abc",
    };
    const sig = signOscResumeHint(secret, "%1", baseHint);
    expect(sig).toMatch(/^[0-9a-f]{64}$/);

    const signedHint: OscResumeHint = { ...baseHint, signature: sig };
    expect(verifyOscResumeHint(secret, "%1", signedHint)).toBe(true);
    expect(
      verifyOscResumeHint(secret, "%1", {
        ...baseHint,
        signature: sig.toUpperCase(),
      }),
    ).toBe(true);

    // Wrong pane / tampered fields
    expect(verifyOscResumeHint(secret, "%2", signedHint)).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", { ...signedHint, session_id: "other" }),
    ).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", { ...signedHint, agent: "codex" }),
    ).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", { ...signedHint, profile: "evil" }),
    ).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", { ...signedHint, cwd: "/etc" }),
    ).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", {
        ...signedHint,
        resume_target: "sh -c id",
      }),
    ).toBe(false);

    // Wrong secret or malformed secret
    expect(verifyOscResumeHint(generatePaneSecret(), "%1", signedHint)).toBe(
      false,
    );
    expect(verifyOscResumeHint("not-hex", "%1", signedHint)).toBe(false);
    expect(verifyOscResumeHint("", "%1", signedHint)).toBe(false);

    // Missing, wrong-length, non-hex, or 1-bit flipped signatures
    expect(verifyOscResumeHint(secret, "%1", baseHint)).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", { ...baseHint, signature: "" }),
    ).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", { ...baseHint, signature: "deadbeef" }),
    ).toBe(false);
    expect(
      verifyOscResumeHint(secret, "%1", {
        ...baseHint,
        signature: "g".repeat(64),
      }),
    ).toBe(false);

    const flippedLast = sig.endsWith("0") ? "1" : "0";
    expect(
      verifyOscResumeHint(secret, "%1", {
        ...baseHint,
        signature: sig.slice(0, -1) + flippedLast,
      }),
    ).toBe(false);
  });

  test("signOscResumeHint throws on invalid secretHex", () => {
    const hint: OscResumeHint = { agent: "claude", session_id: "s1" };
    expect(() => signOscResumeHint("xyz", "%1", hint)).toThrow();
    expect(() => signOscResumeHint("abc", "%1", hint)).toThrow();
    expect(() => signOscResumeHint("", "%1", hint)).toThrow();
  });
});

describe("validateResumeCommand", () => {
  test("allows safe non-agent TUI restore commands and rejects unsafe ones", () => {
    expect(validateResumeCommand({ argv: ["nvim", "src/index.ts"] })).toEqual({
      ok: true,
      argv: ["nvim", "src/index.ts"],
    });
    expect(
      validateResumeCommand({ argv: ["/usr/bin/lazygit", "-p", "/my repo"] }),
    ).toEqual({
      ok: true,
      argv: ["/usr/bin/lazygit", "-p", "/my repo"],
    });

    const denied = validateResumeCommand({ argv: ["rm", "-rf", "/tmp/x"] });
    expect(denied.ok).toBe(false);

    const deniedShell = validateResumeCommand({ argv: ["bash", "-l"] });
    expect(deniedShell.ok).toBe(false);
  });

  test("allows valid agent commands and enforces knownAgents when supplied", () => {
    const known = new Set(["claude", "codex", "opencode"]);

    expect(
      validateResumeCommand({
        agent: "claude",
        argv: ["claude", "-c"],
        knownAgents: known,
      }),
    ).toEqual({
      ok: true,
      argv: ["claude", "-c"],
    });

    expect(
      validateResumeCommand({
        agent: "claude",
        argv: [
          "/usr/local/bin/tmux-spaces",
          "__shim",
          "--profile",
          "work",
          "claude",
          "--resume",
          "sess-42",
        ],
        knownAgents: known,
      }).ok,
    ).toBe(true);

    expect(
      validateResumeCommand({
        agent: "codex",
        argv: ["codex", "resume", "0194b1c2-abc"],
      }).ok,
    ).toBe(true);

    const unknown = validateResumeCommand({
      agent: "mystery-agent",
      argv: ["mystery-agent"],
      knownAgents: known,
    });
    expect(unknown.ok).toBe(false);
  });

  test("rejects empty argv, NUL bytes, control chars, shell wrappers, and metacharacters", () => {
    expect(validateResumeCommand({ argv: [] }).ok).toBe(false);
    expect(validateResumeCommand({ argv: [""] }).ok).toBe(false);
    expect(validateResumeCommand({ argv: ["   "] }).ok).toBe(false);

    // NUL bytes and control characters
    expect(
      validateResumeCommand({ agent: "claude", argv: ["claude\0", "--resume"] })
        .ok,
    ).toBe(false);
    expect(
      validateResumeCommand({ argv: ["nvim", "foo\0bar"] }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({ argv: ["nvim", "foo\nbar"] }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({ agent: "claude", argv: ["claude", "\x1b]7577;x"] })
        .ok,
    ).toBe(false);

    // Shell / eval wrappers
    expect(
      validateResumeCommand({ agent: "claude", argv: ["sh", "-c", "claude"] })
        .ok,
    ).toBe(false);
    expect(
      validateResumeCommand({
        agent: "claude",
        argv: ["/bin/bash", "-c", "claude --resume s1"],
      }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({ agent: "claude", argv: ["eval", "claude"] }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({
        agent: "claude",
        argv: ["/usr/bin/env", "FOO=1", "sh", "-c", "claude"],
      }).ok,
    ).toBe(false);

    // Whitespace or shell metacharacters in binary or args
    expect(
      validateResumeCommand({ agent: "claude", argv: ["claude --resume"] }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({ agent: "claude", argv: ["claude;id"] }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({
        agent: "claude",
        argv: ["claude", "--resume", "s1; rm -rf /"],
      }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({
        agent: "claude",
        argv: ["claude", "$(whoami)"],
      }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({
        argv: ["nvim", "`id`"],
      }).ok,
    ).toBe(false);
    expect(
      validateResumeCommand({
        argv: ["nvim", "a.txt", "|", "cat"],
      }).ok,
    ).toBe(false);
  });
});
