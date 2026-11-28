import { normalizeCommand } from "../detection/policies";
import type { HarnessResumePolicy } from "../resurrect/capture";
import type { HarnessPlugin } from "./types";

/**
 * Registry of unified `HarnessPlugin` implementations indexed by canonical
 * `spec.id` and aliases (case-insensitive), plus a bridge into `resurrect`'s
 * agent-agnostic `HarnessResumePolicy` seam.
 */
export class HarnessPluginRegistry {
  private readonly ordered: HarnessPlugin[] = [];
  private readonly byId = new Map<string, HarnessPlugin>();
  private readonly byKey = new Map<string, HarnessPlugin>();

  /**
   * Registers or replaces a plugin, indexing its canonical `spec.id`,
   * `spec.aliases`, and secondary launch identifiers (`displayApp`,
   * `cliDisplayApp`, `binary`) case-insensitively without overwriting another
   * plugin's primary ID.
   */
  register(plugin: HarnessPlugin): void {
    const idKey = plugin.spec.id.trim().toLowerCase();
    if (!idKey) {
      throw new Error("HarnessPluginRegistry.register: spec.id must be non-empty");
    }

    const existingIdx = this.ordered.findIndex(
      (p) => p.spec.id.trim().toLowerCase() === idKey,
    );
    if (existingIdx < 0) {
      this.ordered.push(plugin);
    } else {
      this.ordered[existingIdx] = plugin;
    }
    this.byId.set(idKey, plugin);

    const keys: Array<string | undefined> = [
      idKey,
      ...plugin.spec.aliases,
      plugin.spec.displayApp,
      plugin.spec.cliDisplayApp,
      plugin.spec.binary,
    ];
    for (let i = 0; i < keys.length; i += 1) {
      const key = (keys[i] ?? "").trim().toLowerCase();
      if (!key) {
        continue;
      }
      const isPrimary = i === 0;
      if (!isPrimary) {
        const owner = this.byId.get(key);
        if (owner && owner !== plugin) {
          continue;
        }
      }
      this.byKey.set(key, plugin);
    }
  }

  /**
   * Resolves a registered plugin by canonical ID, alias, binary path, or
   * versioned executable name (case-insensitive), returning `null` if unknown.
   */
  get(agent: string): HarnessPlugin | null {
    const raw = agent.trim().toLowerCase();
    if (!raw) {
      return null;
    }
    const direct = this.byKey.get(raw);
    if (direct) {
      return direct;
    }
    const normalized = normalizeCommand(raw);
    if (normalized) {
      return this.byKey.get(normalized) ?? null;
    }
    return null;
  }

  /**
   * Returns all registered plugins in registration order.
   */
  list(): HarnessPlugin[] {
    return this.ordered.slice();
  }

  /**
   * Returns all non-worktree-only runtime plugins in registration order.
   */
  runtimePlugins(): HarnessPlugin[] {
    return this.ordered.filter((p) => !p.spec.worktreeOnly);
  }

  /**
   * Returns the deduplicated union of all registered plugins' MCP config targets.
   */
  allMcpTargets(): string[] {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const plugin of this.ordered) {
      for (const target of plugin.spec.mcpTargets) {
        const trimmed = target.trim();
        if (trimmed && !seen.has(trimmed)) {
          seen.add(trimmed);
          out.push(trimmed);
        }
      }
    }
    return out;
  }

  /**
   * Bridges every registered plugin into `resurrect`'s agent-agnostic
   * `HarnessResumePolicy` seam.
   */
  asResumePolicy(): HarnessResumePolicy {
    return {
      binaryName: (agent) => {
        const plugin = this.get(agent);
        return plugin ? plugin.spec.binary || plugin.spec.id : "";
      },
      resumeArgs: (agent, sessionId) => {
        const plugin = this.get(agent);
        return plugin ? plugin.resumeArgs(sessionId) : [];
      },
      positionalResume: (agent) => {
        const plugin = this.get(agent);
        return plugin ? plugin.spec.positionalResume : false;
      },
      uniqueExplicitSessions: (agent) => {
        const plugin = this.get(agent);
        return plugin ? plugin.spec.uniqueExplicitSessions : false;
      },
      discoverLiveSessionId: (agent, pid) => {
        const plugin = this.get(agent);
        return plugin?.discoverLiveSessionId ? plugin.discoverLiveSessionId(pid) : "";
      },
      resolveVendorSession: (agent, cwd, profile, title, exclude) => {
        const plugin = this.get(agent);
        if (!plugin?.resolveVendorSession) {
          return { sessionId: "", storeAvailable: false };
        }
        return plugin.resolveVendorSession(agent, cwd, profile, title, exclude);
      },
    };
  }
}
