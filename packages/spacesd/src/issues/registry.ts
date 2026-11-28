import type { IssueLink, IssueTrackerKind } from "@spaces/types";
import type { ForgeProvider } from "../forge/types";
import {
  isStandaloneIssueTracker,
  type IssueTrackerProvider,
  type StandaloneIssueTrackerProvider,
} from "./types";

export interface ParsedIssueUrlMatch {
  readonly provider: IssueTrackerProvider;
  readonly link: IssueLink;
}

export class IssueTrackerRegistry {
  private readonly providers = new Map<IssueTrackerKind, IssueTrackerProvider>();

  register(provider: IssueTrackerProvider): this {
    if (this.providers.has(provider.kind)) {
      throw new Error(`Issue tracker '${provider.kind}' is already registered.`);
    }
    this.providers.set(provider.kind, provider);
    return this;
  }

  get(kind: IssueTrackerKind): IssueTrackerProvider | undefined {
    return this.providers.get(kind);
  }

  list(): ReadonlyArray<IssueTrackerProvider> {
    return [...this.providers.values()];
  }

  listStandalone(): ReadonlyArray<StandaloneIssueTrackerProvider> {
    return [...this.providers.values()].filter(isStandaloneIssueTracker);
  }

  parseIssueUrl(rawUrl: string): ParsedIssueUrlMatch | null {
    let url: URL;
    try {
      url = new URL(rawUrl.trim());
    } catch {
      return null;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      return null;
    }
    for (const provider of this.providers.values()) {
      const link = provider.parseIssueUrl?.(url) ?? null;
      if (link !== null) {
        return { provider, link };
      }
    }
    return null;
  }

  resolveForForge(forgeProvider: ForgeProvider): IssueTrackerProvider | undefined {
    if (forgeProvider.issues) {
      return forgeProvider.issues;
    }
    if (forgeProvider.kind !== "unknown") {
      return this.providers.get(forgeProvider.kind);
    }
    return undefined;
  }
}
