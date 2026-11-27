/**
 * Agent hook arbitration policies, command normalization, and built-in
 * detection registry ported 1:1 from `internal/agent` and `internal/agent/plugins`.
 */

/**
 * Default silence threshold (120s) after which a working hook with only
 * `local_bash` background work yields to a visibly idle screen.
 */
export const DEFAULT_STALE_LOCAL_CMD_MS = 120_000;

/**
 * Silence threshold (10s) after which a working Antigravity hook yields to a
 * visibly idle viewport (handles missed Stop hooks).
 */
export const ANTIGRAVITY_HOOK_IDLE_GRACE_MS = 10_000;

const SHELL_COMMANDS = new Set([
  "sh",
  "bash",
  "zsh",
  "fish",
  "nu",
  "nushell",
  "dash",
  "ksh",
  "tcsh",
  "csh",
]);

const STRIPPED_COMMAND_EXTENSIONS = [
  ".exe",
  ".cmd",
  ".bat",
  ".ps1",
  ".js",
  ".mjs",
  ".cjs",
] as const;

/**
 * Signals needed to evaluate agent-specific hook-vs-screen arbitration overrides.
 */
export interface HookArbitrationInput {
  hookState: string;
  hookSilenceMs: number;
  hasActiveFiniteWork: boolean;
  hasOnlyLocalCmdBackground: boolean;
  visibleIdle: boolean;
  visibleWorking: boolean;
  visibleBlocker: boolean;
  screenState: string;
}

/**
 * Policy governing how hooks, OSC 7501, and screen readings interact for an agent.
 */
export interface HookPolicy {
  /**
   * Reports whether live non-idle OSC 7501 / hook state wins over bare screen
   * working/idle readings.
   */
  oscAuthority(): boolean;

  /**
   * Reports whether a "working" hook should yield to a visibly idle viewport
   * (e.g. missed Stop hook or stale `local_bash` task).
   */
  shouldYieldWorkingHookToIdle(input: HookArbitrationInput): boolean;
}

export interface BaseHookPolicyOptions {
  authority?: boolean;
  staleLocalCmdMs?: number;
}

/**
 * Shared default arbitration policy:
 * - `oscAuthority` from spec
 * - Yields a stale (>120s) working hook when only `local_bash` background work
 *   remains and the viewport is visibly idle.
 */
export class BaseHookPolicy implements HookPolicy {
  readonly authority: boolean;
  readonly staleLocalCmdMs: number;

  constructor(options?: BaseHookPolicyOptions);
  constructor(authority: boolean, staleLocalCmdMs?: number);
  constructor(
    optionsOrAuthority: BaseHookPolicyOptions | boolean = {},
    staleLocalCmdMs = DEFAULT_STALE_LOCAL_CMD_MS,
  ) {
    if (typeof optionsOrAuthority === "boolean") {
      this.authority = optionsOrAuthority;
      this.staleLocalCmdMs = staleLocalCmdMs;
    } else {
      this.authority = optionsOrAuthority.authority ?? false;
      this.staleLocalCmdMs = optionsOrAuthority.staleLocalCmdMs ?? DEFAULT_STALE_LOCAL_CMD_MS;
    }
  }

  oscAuthority(): boolean {
    return this.authority;
  }

  shouldYieldWorkingHookToIdle(input: HookArbitrationInput): boolean {
    const threshold =
      this.staleLocalCmdMs > 0 ? this.staleLocalCmdMs : DEFAULT_STALE_LOCAL_CMD_MS;
    return (
      input.hookState === "working" &&
      input.hookSilenceMs > threshold &&
      input.hasOnlyLocalCmdBackground &&
      input.visibleIdle
    );
  }
}

/**
 * Antigravity-specific hook policy: yields after 10s silence when visibly idle
 * without active finite background work, in addition to the base stale local-cmd rule.
 */
export class AntigravityHookPolicy implements HookPolicy {
  private readonly base = new BaseHookPolicy({
    authority: false,
    staleLocalCmdMs: DEFAULT_STALE_LOCAL_CMD_MS,
  });

  oscAuthority(): boolean {
    return false;
  }

  shouldYieldWorkingHookToIdle(input: HookArbitrationInput): boolean {
    if (this.base.shouldYieldWorkingHookToIdle(input)) {
      return true;
    }
    return (
      input.hookState === "working" &&
      !input.hasActiveFiniteWork &&
      input.hookSilenceMs >= ANTIGRAVITY_HOOK_IDLE_GRACE_MS &&
      input.visibleIdle
    );
  }
}

export interface AgentDetectionSpec {
  id: string;
  aliases: string[];
  screenManifestId: string;
  oscAuthority: boolean;
  worktreeOnly: boolean;
  hookPolicy: HookPolicy;
  displayApp?: string;
  cliDisplayApp?: string;
  conversationId?: string;
  shortName?: string;
  binary?: string;
}

export interface AgentDetectionSpecInput {
  id: string;
  aliases?: string[];
  screenManifestId?: string;
  oscAuthority?: boolean;
  worktreeOnly?: boolean;
  hookPolicy?: HookPolicy;
  displayApp?: string;
  cliDisplayApp?: string;
  conversationId?: string;
  shortName?: string;
  binary?: string;
}

const DEFAULT_FALLBACK_HOOK_POLICY: HookPolicy = new BaseHookPolicy({
  authority: false,
  staleLocalCmdMs: DEFAULT_STALE_LOCAL_CMD_MS,
});

/**
 * Extracts a canonical lowercase command name from a raw binary path or
 * wrapper name:
 * - strips leading `"herdr:"` prefix
 * - strips directory prefixes
 * - strips executable/script extensions (`.exe`, `.cmd`, `.bat`, `.ps1`, `.js`, `.mjs`, `.cjs`)
 * - collapses versioned binaries like `"muse-bin-1.4.24-R9131.1"` -> `"muse"`
 *   and `"claude-2.1.76"` -> `"claude"`
 */
export function normalizeCommand(raw: string): string {
  let s = raw.trim().toLowerCase();
  if (s === "") {
    return "";
  }
  if (s.startsWith("herdr:")) {
    s = s.slice("herdr:".length);
  }
  const slashIdx = s.lastIndexOf("/");
  if (slashIdx >= 0) {
    s = s.slice(slashIdx + 1);
  }
  for (const ext of STRIPPED_COMMAND_EXTENSIONS) {
    if (s.endsWith(ext)) {
      s = s.slice(0, s.length - ext.length);
      break;
    }
  }
  if (s === "") {
    return "";
  }
  const binIdx = s.indexOf("-bin-");
  if (binIdx > 0) {
    return s.slice(0, binIdx);
  }
  const dashIdx = s.indexOf("-");
  if (dashIdx > 0 && dashIdx + 1 < s.length) {
    const c = s.charCodeAt(dashIdx + 1);
    if (c >= 48 && c <= 57) {
      return s.slice(0, dashIdx);
    }
  }
  return s;
}

/**
 * Returns the executable basename of a command line
 * (`"opencode --auto"` -> `"opencode"`, `"/usr/bin/pi"` -> `"pi"`).
 * Returns `""` when the command is blank.
 */
export function commandName(cmd: string): string {
  let first = cmd.trim();
  const wsMatch = /[ \t]/.exec(first);
  if (wsMatch) {
    first = first.slice(0, wsMatch.index);
  }
  let base = first.replace(/\/+$/, "");
  const slashIdx = base.lastIndexOf("/");
  if (slashIdx >= 0) {
    base = base.slice(slashIdx + 1);
  }
  return base;
}

/**
 * Reports whether `name` is a shell executable.
 */
export function isShellCommand(name: string): boolean {
  let n = name.trim().toLowerCase();
  if (n.startsWith("-")) {
    n = n.slice(1);
  }
  return SHELL_COMMANDS.has(n);
}

/**
 * Reports whether `name` is the normalized `"shell"` label or a shell executable name.
 */
export function isShellApp(name: string): boolean {
  const n = name.trim().toLowerCase();
  return n === "shell" || isShellCommand(n);
}

export function builtinAgentDetectionSpecs(): AgentDetectionSpecInput[] {
  return [
    // Primary dedicated plugins (10)
    {
      id: "antigravity",
      aliases: ["agy", "antigravity-cli"],
      displayApp: "antigravity",
      cliDisplayApp: "antigravity",
      conversationId: "antigravity",
      shortName: "agy",
      screenManifestId: "agy",
      oscAuthority: false,
      worktreeOnly: false,
      hookPolicy: new AntigravityHookPolicy(),
    },
    {
      id: "claude",
      aliases: ["claude-code", "claude_code", "claudecode"],
      displayApp: "claude",
      cliDisplayApp: "claude-code",
      conversationId: "claude",
      shortName: "claude",
      screenManifestId: "claude",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "codex",
      aliases: ["openai-codex"],
      screenManifestId: "codex",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "cursor-agent",
      aliases: ["cursor", "cursor_agent", "cursor-cli"],
      displayApp: "cursor-agent",
      cliDisplayApp: "cursor-agent",
      conversationId: "cursor-agent",
      shortName: "cursor",
      binary: "cursor-agent",
      screenManifestId: "cursor",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "deepseek",
      aliases: ["dsh", "dsh-tui"],
      displayApp: "deepseek",
      cliDisplayApp: "deepseek",
      conversationId: "deepseek",
      shortName: "deepseek",
      screenManifestId: "deepseek",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "kilo",
      aliases: ["kilo-code", "kilo code", "herdr:kilo"],
      screenManifestId: "kilo",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "kimi",
      aliases: ["kimi-code", "kimi code"],
      displayApp: "kimi",
      cliDisplayApp: "kimi",
      conversationId: "kimi",
      shortName: "kimi",
      screenManifestId: "kimi",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "kimi-cli",
      aliases: [],
      displayApp: "kimi-cli",
      cliDisplayApp: "kimi-cli",
      conversationId: "kimi",
      shortName: "kimi",
      screenManifestId: "kimi",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "muse",
      aliases: ["muse-code", "muse-cli"],
      screenManifestId: "muse",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "opencode",
      aliases: ["open-code", "herdr:opencode", "oc"],
      screenManifestId: "opencode",
      oscAuthority: true,
      worktreeOnly: false,
    },
    // Harness-managed & OSC-authority plugins (11)
    {
      id: "cline",
      screenManifestId: "cline",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "copilot",
      aliases: ["github-copilot", "ghcs"],
      screenManifestId: "copilot",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "fx",
      aliases: ["fx-router"],
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "gemini",
      aliases: ["gemini-cli"],
      screenManifestId: "gemini",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "grok",
      aliases: ["grok-build"],
      screenManifestId: "grok",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "mcode",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "pi",
      aliases: ["herdr:pi"],
      screenManifestId: "pi",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "qwen",
      aliases: ["qwen-code", "qwen code"],
      screenManifestId: "qwen",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "vibe",
      aliases: ["vibe-acp"],
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "crush",
      oscAuthority: true,
      worktreeOnly: false,
    },
    {
      id: "omp",
      oscAuthority: true,
      worktreeOnly: false,
    },
    // Other runtime manifest-backed plugins (8)
    {
      id: "amp",
      aliases: ["amp-local"],
      screenManifestId: "amp",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "devin",
      aliases: ["devin-cli", "devin cli"],
      screenManifestId: "devin",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "droid",
      screenManifestId: "droid",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "hermes",
      aliases: ["hermes-agent"],
      screenManifestId: "hermes",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "kiro",
      aliases: ["kiro-cli"],
      screenManifestId: "kiro",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "letta",
      aliases: ["letta-code", "letta code"],
      screenManifestId: "letta",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "maki",
      screenManifestId: "maki",
      oscAuthority: false,
      worktreeOnly: false,
    },
    {
      id: "qodercli",
      aliases: ["qoderclicn", "qoder", "qodercn"],
      screenManifestId: "qodercli",
      oscAuthority: false,
      worktreeOnly: false,
    },
    // Worktree-only agents (4)
    {
      id: "aider",
      oscAuthority: false,
      worktreeOnly: true,
    },
    {
      id: "goose",
      oscAuthority: false,
      worktreeOnly: true,
    },
    {
      id: "windsurf",
      oscAuthority: false,
      worktreeOnly: true,
    },
    {
      id: "roo",
      aliases: ["roo-code"],
      oscAuthority: false,
      worktreeOnly: true,
    },
  ];
}

function normalizeDetectionSpec(input: AgentDetectionSpecInput): AgentDetectionSpec {
  const oscAuthority = Boolean(input.oscAuthority);
  const worktreeOnly = Boolean(input.worktreeOnly);
  const hookPolicy =
    input.hookPolicy ??
    new BaseHookPolicy({
      authority: oscAuthority,
      staleLocalCmdMs: DEFAULT_STALE_LOCAL_CMD_MS,
    });
  return {
    id: input.id.trim(),
    aliases: [...(input.aliases ?? [])],
    screenManifestId: input.screenManifestId ?? "",
    oscAuthority,
    worktreeOnly,
    hookPolicy,
    ...(input.displayApp !== undefined ? { displayApp: input.displayApp } : {}),
    ...(input.cliDisplayApp !== undefined ? { cliDisplayApp: input.cliDisplayApp } : {}),
    ...(input.conversationId !== undefined ? { conversationId: input.conversationId } : {}),
    ...(input.shortName !== undefined ? { shortName: input.shortName } : {}),
    ...(input.binary !== undefined ? { binary: input.binary } : {}),
  };
}

/**
 * Registry of coding-agent detection specs, aliases, screen manifest IDs, and
 * hook policies. Pre-populated with all 29 runtime and 4 worktree-only built-in
 * specs when constructed with no arguments.
 */
export class DetectionRegistry {
  private readonly ordered: AgentDetectionSpec[] = [];
  private readonly byID = new Map<string, AgentDetectionSpec>();
  private readonly byKey = new Map<string, AgentDetectionSpec>();

  constructor(specs?: AgentDetectionSpecInput[]) {
    const initial = specs ?? builtinAgentDetectionSpecs();
    for (const spec of initial) {
      this.register(spec);
    }
  }

  /**
   * Returns an empty registry without the built-in specs.
   */
  static empty(): DetectionRegistry {
    return new DetectionRegistry([]);
  }

  /**
   * Registers or replaces an agent detection spec, indexing its primary ID,
   * aliases, and metadata keys while protecting primary IDs and runtime aliases
   * from being overwritten by secondary keys or worktree-only entries.
   */
  register(input: AgentDetectionSpecInput): void {
    const rawId = input.id.trim();
    const idKey = rawId.toLowerCase();
    if (idKey === "") {
      return;
    }
    const spec = normalizeDetectionSpec({ ...input, id: rawId });

    const existingIdx = this.ordered.findIndex(
      (item) => item.id.trim().toLowerCase() === idKey,
    );
    if (existingIdx < 0) {
      this.ordered.push(spec);
    } else {
      this.ordered[existingIdx] = spec;
    }
    this.byID.set(idKey, spec);

    const keys: Array<string | undefined> = [
      idKey,
      spec.displayApp,
      spec.cliDisplayApp,
      spec.conversationId,
      spec.binary,
      spec.screenManifestId,
      ...spec.aliases,
    ];
    for (let i = 0; i < keys.length; i++) {
      const nk = (keys[i] ?? "").trim().toLowerCase();
      if (nk === "") {
        continue;
      }
      const isPrimaryId = i === 0;
      const existing = this.byKey.get(nk);
      if (existing && !existing.worktreeOnly && spec.worktreeOnly) {
        continue;
      }
      if (!isPrimaryId) {
        const owner = this.byID.get(nk);
        if (owner && owner !== spec) {
          continue;
        }
      }
      this.byKey.set(nk, spec);

      const norm = normalizeCommand(nk);
      if (norm !== "" && norm !== nk) {
        if (!isPrimaryId) {
          const owner = this.byID.get(norm);
          if (owner && owner !== spec) {
            continue;
          }
        }
        const existingNorm = this.byKey.get(norm);
        if (!existingNorm || existingNorm.worktreeOnly || !spec.worktreeOnly) {
          this.byKey.set(norm, spec);
        }
      }
    }
  }

  /**
   * Looks up any registered spec (runtime or worktree-only) by raw name, alias,
   * binary path, or versioned command string.
   */
  resolve(name: string): AgentDetectionSpec | undefined {
    const raw = name.trim().toLowerCase();
    if (raw === "") {
      return undefined;
    }
    const direct = this.byKey.get(raw);
    if (direct) {
      return direct;
    }
    const norm = normalizeCommand(raw);
    if (norm !== "") {
      return this.byKey.get(norm);
    }
    return undefined;
  }

  /**
   * Resolves only full runtime agent specs (excluding `worktreeOnly` specs).
   */
  resolveRuntime(name: string): AgentDetectionSpec | undefined {
    const spec = this.resolve(name);
    if (!spec || spec.worktreeOnly) {
      return undefined;
    }
    return spec;
  }

  /**
   * Returns all registered specs in registration order.
   */
  all(): AgentDetectionSpec[] {
    return this.ordered.slice();
  }

  /**
   * Returns all registered non-`worktreeOnly` specs in registration order.
   */
  runtimeSpecs(): AgentDetectionSpec[] {
    return this.ordered.filter((spec) => !spec.worktreeOnly);
  }

  /**
   * Returns the canonical runtime agent ID for `name`, or `""` if unknown.
   */
  canonicalId(name: string): string {
    return this.resolveRuntime(name)?.id ?? "";
  }

  /**
   * Returns the canonical runtime agent ID if known, otherwise the normalized
   * lowercase command name.
   */
  canonicalOrNormalized(name: string): string {
    const spec = this.resolveRuntime(name);
    if (spec) {
      return spec.id;
    }
    return normalizeCommand(name);
  }

  /**
   * Reports whether `name` resolves to a registered runtime agent spec.
   */
  isKnownAgent(name: string): boolean {
    return this.resolveRuntime(name) !== undefined;
  }

  /**
   * Reports whether `name` is the normalized `"shell"` label or a shell executable name.
   */
  isShellApp(name: string): boolean {
    return isShellApp(name);
  }

  /**
   * Reports whether `name` is a shell executable.
   */
  isShellCommand(name: string): boolean {
    return isShellCommand(name);
  }

  /**
   * Returns the executable basename of a command line.
   */
  commandName(cmd: string): string {
    return commandName(cmd);
  }

  /**
   * Reports whether live non-idle OSC 7501 / hook state wins over bare screen
   * working/idle readings for `name`.
   */
  isOscAuthority(name: string): boolean {
    const spec = this.resolveRuntime(name);
    if (!spec) {
      return false;
    }
    return spec.hookPolicy.oscAuthority();
  }

  /**
   * Returns the screen manifest ID for `name`, or `""` if unknown or unconfigured.
   */
  screenManifestId(name: string): string {
    return this.resolveRuntime(name)?.screenManifestId ?? "";
  }

  /**
   * Returns the `HookPolicy` for `name`, falling back to `BaseHookPolicy` when unknown.
   */
  hookPolicyFor(name: string): HookPolicy {
    const spec = this.resolveRuntime(name);
    if (spec) {
      return spec.hookPolicy;
    }
    return DEFAULT_FALLBACK_HOOK_POLICY;
  }
}

let defaultRegistrySingleton: DetectionRegistry | undefined;

/**
 * Returns the process-wide default `DetectionRegistry` singleton pre-populated
 * with all built-in runtime and worktree-only agent specs.
 */
export function defaultDetectionRegistry(): DetectionRegistry {
  if (!defaultRegistrySingleton) {
    defaultRegistrySingleton = new DetectionRegistry();
  }
  return defaultRegistrySingleton;
}
