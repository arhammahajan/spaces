import { createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import * as path from "node:path";
import type { OscResumeHint } from "@spaces/types";

/**
 * Allowlist of non-agent interactive TUIs safe to automatically restart on
 * pane restore, matching `internal/resurrect/restore.go`.
 */
export const SAFE_RESTORE_COMMANDS: ReadonlySet<string> = new Set([
  "nvim",
  "vim",
  "vi",
  "nano",
  "emacs",
  "lazygit",
  "tig",
  "htop",
  "btop",
  "top",
  "ranger",
  "yazi",
  "k9s",
  "man",
  "less",
]);

const SHELL_WRAPPERS: ReadonlySet<string> = new Set([
  "sh",
  "bash",
  "zsh",
  "dash",
  "ash",
  "ksh",
  "csh",
  "tcsh",
  "fish",
]);

const CONTROL_CHAR_RE = /[\x00-\x1f\x7f]/;
const BINARY_DISALLOWED_RE = /[\s\x00-\x1f\x7f;|&$`<>\\'"*?[\]{}!#~()]/;
const ARG_SHELL_META_RE = /[;|&$`<>]/;
const HEX_RE = /^[0-9a-fA-F]+$/;
const SHA256_HEX_RE = /^[0-9a-fA-F]{64}$/;

export interface ValidateResumeCommandOptions {
  agent?: string;
  argv: string[];
  knownAgents?: ReadonlySet<string>;
}

export type ValidateResumeCommandResult =
  | { ok: true; argv: string[] }
  | { ok: false; reason: string };

/**
 * Generates a 32-byte cryptographically random per-pane HMAC secret encoded
 * as a 64-character lowercase hex string.
 */
export function generatePaneSecret(): string {
  return randomBytes(32).toString("hex");
}

/**
 * Builds the canonical NUL-delimited v1 signing payload for an OSC 7577 resume hint.
 */
export function canonicalResumeHintPayload(
  paneId: string,
  hint: OscResumeHint,
): string {
  return `v1\0${paneId}\0${hint.agent}\0${hint.session_id}\0${hint.profile ?? ""}\0${hint.cwd ?? ""}\0${hint.resume_target ?? ""}`;
}

function decodeHexSecret(secretHex: string): Buffer | null {
  if (
    typeof secretHex !== "string" ||
    secretHex.length === 0 ||
    secretHex.length % 2 !== 0 ||
    !HEX_RE.test(secretHex)
  ) {
    return null;
  }
  return Buffer.from(secretHex, "hex");
}

/**
 * Signs an OSC 7577 resume hint with HMAC-SHA256 using the pane's hex secret.
 */
export function signOscResumeHint(
  secretHex: string,
  paneId: string,
  hint: OscResumeHint,
): string {
  const key = decodeHexSecret(secretHex);
  if (!key) {
    throw new Error("Invalid pane secret: expected non-empty even-length hex string");
  }
  const payload = canonicalResumeHintPayload(paneId, hint);
  return createHmac("sha256", key).update(payload, "utf8").digest("hex");
}

/**
 * Verifies an OSC 7577 resume hint's HMAC-SHA256 signature in constant time.
 */
export function verifyOscResumeHint(
  secretHex: string,
  paneId: string,
  hint: OscResumeHint,
): boolean {
  const key = decodeHexSecret(secretHex);
  if (!key) {
    return false;
  }
  if (typeof hint.signature !== "string" || !SHA256_HEX_RE.test(hint.signature)) {
    return false;
  }

  const payload = canonicalResumeHintPayload(paneId, hint);
  const expected = createHmac("sha256", key).update(payload, "utf8").digest();
  const actual = Buffer.from(hint.signature, "hex");

  if (expected.length !== actual.length) {
    return false;
  }
  return timingSafeEqual(expected, actual);
}

/**
 * Validates a command vector before re-executing it on pane/thread restore.
 *
 * Rejects empty commands, NUL/control characters, shell/eval wrappers, and
 * shell metacharacters. Non-agent commands must be in `SAFE_RESTORE_COMMANDS`.
 */
export function validateResumeCommand(
  opts: ValidateResumeCommandOptions,
): ValidateResumeCommandResult {
  const { agent, argv, knownAgents } = opts;

  if (!Array.isArray(argv) || argv.length === 0) {
    return { ok: false, reason: "argv must not be empty" };
  }

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (typeof arg !== "string") {
      return { ok: false, reason: `argv[${i}] must be a string` };
    }
    if (arg.includes("\0")) {
      return { ok: false, reason: `argv[${i}] contains NUL byte` };
    }
    if (CONTROL_CHAR_RE.test(arg)) {
      return { ok: false, reason: `argv[${i}] contains control character` };
    }
  }

  const cmd = argv[0]!;
  if (cmd.trim().length === 0) {
    return { ok: false, reason: "command binary must not be empty" };
  }
  if (BINARY_DISALLOWED_RE.test(cmd)) {
    return {
      ok: false,
      reason: "command binary contains disallowed whitespace or shell characters",
    };
  }

  const base = path.basename(cmd);
  if (base === "" || base === "." || base === "..") {
    return { ok: false, reason: "invalid command binary name" };
  }
  const baseLower = base.toLowerCase();

  if (baseLower === "eval" || SHELL_WRAPPERS.has(baseLower)) {
    return { ok: false, reason: `shell or eval wrapper '${base}' is not allowed` };
  }

  if (baseLower === "env") {
    const wrapped = resolveEnvTarget(argv.slice(1));
    if (!wrapped) {
      return { ok: false, reason: "env wrapper missing target command" };
    }
    const wrappedLower = path.basename(wrapped).toLowerCase();
    if (wrappedLower === "eval" || SHELL_WRAPPERS.has(wrappedLower)) {
      return {
        ok: false,
        reason: `shell or eval wrapper '${wrappedLower}' via env is not allowed`,
      };
    }
  }

  for (let i = 1; i < argv.length; i += 1) {
    const arg = argv[i]!;
    if (ARG_SHELL_META_RE.test(arg)) {
      return { ok: false, reason: `argv[${i}] contains shell metacharacter` };
    }
    // Reject nested `sh -c` / `bash -c` style wrapper invocations anywhere in argv.
    const argBase = path.basename(arg).toLowerCase();
    if (
      (argBase === "eval" || SHELL_WRAPPERS.has(argBase)) &&
      i + 1 < argv.length &&
      /^-.*c/.test(argv[i + 1]!)
    ) {
      return { ok: false, reason: `shell wrapper '${argBase} ${argv[i + 1]}' is not allowed` };
    }
  }

  if (agent !== undefined && agent !== "") {
    if (agent.trim().length === 0 || BINARY_DISALLOWED_RE.test(agent)) {
      return { ok: false, reason: "invalid agent identifier" };
    }
    if (knownAgents !== undefined && !knownAgents.has(agent)) {
      return { ok: false, reason: `unknown agent '${agent}'` };
    }
    return { ok: true, argv: argv.slice() };
  }

  if (!SAFE_RESTORE_COMMANDS.has(baseLower)) {
    return {
      ok: false,
      reason: `non-agent command '${base}' is not in SAFE_RESTORE_COMMANDS`,
    };
  }

  return { ok: true, argv: argv.slice() };
}

function resolveEnvTarget(args: string[]): string | null {
  for (const arg of args) {
    if (arg === "--") {
      continue;
    }
    if (arg.startsWith("-") || arg.includes("=")) {
      continue;
    }
    return arg;
  }
  return null;
}
