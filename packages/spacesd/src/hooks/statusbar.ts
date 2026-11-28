import type { BackendStatusBarResult, Space, StatusBarCounts } from "@spaces/types";

// Catppuccin Mocha accents for the status-line badge, matching internal/cli/statusbar.go.
export const MOCHA_BLUE = "#89B4FA" as const; // needs you
export const MOCHA_RED = "#F38BA8" as const; // failed
export const MOCHA_YELLOW = "#F9E2AF" as const; // working
export const MOCHA_GREEN = "#A6E3A1" as const; // done

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
 * Aggregates pane status buckets across all `spaces` and formats both the
 * Catppuccin Mocha tmux status-line badge and plain-text badge.
 * Both formatted strings are empty when `needs + failed + working + done === 0`.
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

  if (counts.needs + counts.failed + counts.working + counts.done === 0) {
    return {
      counts,
      formatted_tmux: "",
      formatted_plain: "",
    };
  }

  const segs: ReadonlyArray<{ n: number; icon: string; color: string }> = [
    { n: counts.needs, icon: "●", color: MOCHA_BLUE },
    { n: counts.failed, icon: "✗", color: MOCHA_RED },
    { n: counts.working, icon: "◐", color: MOCHA_YELLOW },
    { n: counts.done, icon: "✓", color: MOCHA_GREEN },
  ];

  const tmuxParts: string[] = [];
  const plainParts: string[] = [];

  for (const seg of segs) {
    if (seg.n === 0) {
      continue;
    }
    const text = `${seg.icon}${seg.n}`;
    plainParts.push(text);
    tmuxParts.push(`#[fg=${seg.color}]${text}#[default]`);
  }

  return {
    counts,
    formatted_tmux: tmuxParts.join(" "),
    formatted_plain: plainParts.join(" "),
  };
}
