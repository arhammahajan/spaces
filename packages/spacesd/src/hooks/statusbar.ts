import type { BackendStatusBarResult, Space, StatusBarCounts } from "@spaces/types";

export type StatusBucket = "needs" | "failed" | "working" | "done" | "idle";

/**
 * Maps a raw pane state onto a coarse status-bar bucket, matching `internal/cli/cli.go`.
 * Unknown and empty values map to `"idle"`.
 */
export function bucketOf(state: string): StatusBucket {
  switch (state.trim().toLowerCase()) {
    case "blocked":
    case "needs_you":
    case "needs-you":
    case "needsyou":
      return "needs";
    case "failed":
    case "error":
      return "failed";
    case "done":
      return "done";
    case "working":
      return "working";
    default:
      return "idle";
  }
}

/**
 * Aggregates pane status buckets across all `spaces`.
 * Presentation (icons, colors, mux-specific format strings) is owned by clients/mux plugins/TUI.
 */
export function computeStatusBar(spaces: readonly Space[]): BackendStatusBarResult {
  const counts: StatusBarCounts = {
    needs: 0,
    failed: 0,
    working: 0,
    done: 0,
    idle: 0,
  };

  for (const space of spaces) {
    for (const pane of space.panes) {
      switch (bucketOf(pane.status)) {
        case "needs":
          counts.needs += 1;
          break;
        case "failed":
          counts.failed += 1;
          break;
        case "working":
          counts.working += 1;
          break;
        case "done":
          counts.done += 1;
          break;
        default:
          counts.idle += 1;
          break;
      }
    }
  }

  return { counts };
}
