#!/usr/bin/env bash
# tmux-spaces TPM plugin entrypoint.
#
# - Binds configurable keys to display-popup views (Navigator, Agents, new).
# - Interpolates #{spaces_status} in status-right/status-left to the
#   `tmux-spaces status-bar` badge segment.
set -u

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

find_bin() {
	if [ -x "$CURRENT_DIR/tmux-spaces" ]; then
		printf '%s\n' "$CURRENT_DIR/tmux-spaces"
		return 0
	fi
	if command -v tmux-spaces >/dev/null 2>&1; then
		command -v tmux-spaces
		return 0
	fi
	if command -v go >/dev/null 2>&1 && [ -f "$CURRENT_DIR/go.mod" ] && [ -d "$CURRENT_DIR/cmd/tmux-spaces" ]; then
		if (cd "$CURRENT_DIR" && go build -o tmux-spaces ./cmd/tmux-spaces >/dev/null 2>&1); then
			printf '%s\n' "$CURRENT_DIR/tmux-spaces"
			return 0
		fi
	fi
	return 1
}

BIN="$(find_bin || true)"
if [ -z "${BIN:-}" ]; then
	echo "tmux-spaces: binary not found (expected ./tmux-spaces or tmux-spaces on PATH)" >&2
	echo "tmux-spaces: run ./build.sh in the plugin directory, then reload tmux" >&2
	exit 1
fi

# Read a global tmux option with a fallback default.
get_tmux_option() {
	local option="$1" default_value="$2" value=""
	value="$(tmux show-option -gqv "$option" 2>/dev/null || true)"
	if [ -z "$value" ]; then
		printf '%s\n' "$default_value"
	else
		printf '%s\n' "$value"
	fi
}

SPACES_KEY="$(get_tmux_option "@spaces-key" "s")"
SPACES_AGENTS_KEY="$(get_tmux_option "@spaces-agents-key" "a")"
SPACES_NEW_KEY="$(get_tmux_option "@spaces-new-key" "N")"
SPACES_WORKTREES_KEY="$(get_tmux_option "@spaces-worktrees-key" "W")"
SPACES_INBOX_KEY="$(get_tmux_option "@spaces-inbox-key" "i")"
SPACES_CHATS_KEY="$(get_tmux_option "@spaces-conversations-key" "y")"
SPACES_SHARED_KEY="$(get_tmux_option "@spaces-shared-key" "u")"
SPACES_SHARED_DIR="$(get_tmux_option "@spaces-shared-dir" "")"
SPACES_TABS_KEY="$(get_tmux_option "@spaces-tabs-key" "T")"
SPACES_NEW_TAB_KEY="$(get_tmux_option "@spaces-new-tab-key" "c")"
POPUP_WIDTH="$(get_tmux_option "@spaces-popup-width" "86%")"
POPUP_HEIGHT="$(get_tmux_option "@spaces-popup-height" "82%")"
POPUP_BORDER="$(get_tmux_option "@spaces-border" "rounded")"

tmux set-option -g status-position bottom

# Keep tmux attached and switch to the most recently active remaining space
# (and its active tab/pane) when all panes/tabs of the current space are
# closed, instead of detaching. Override with e.g.
# `set -g @spaces-detach-on-destroy on` (or `no-detached` / `previous` / `next`).
DETACH_ON_DESTROY="$(get_tmux_option "@spaces-detach-on-destroy" "off")"
tmux set-option -g detach-on-destroy "$DETACH_ON_DESTROY"

# Show the active coding agent (and profile separated by an em dash when
# launched through the harness CLI) on top of every pane.
# Disable with `set -g @spaces-pane-border off`.
PANE_BORDER="$(get_tmux_option "@spaces-pane-border" "on")"
if [ "$PANE_BORDER" = "on" ] || [ "$PANE_BORDER" = "1" ] || [ "$PANE_BORDER" = "true" ]; then
	tmux set-option -gw pane-border-status top
	tmux set-option -gw pane-border-format " #{?#{@spaces_app},#{@spaces_app}#{?#{@spaces_profile}, — #{@spaces_profile},},#{pane_current_command}} "
fi

# Stabilize full-screen coding harness TUIs (Claude Code, Codex, OpenCode,
# Agy, Muse, etc.) inside tmux:
# - allow-passthrough: let harness OSC sequences pass cleanly through tmux.
# - sync/RGB features: ensure atomic DECSET 2026 synchronized updates (idempotent).
# - Enmg@/Dsmg@: disable DECSLRM left/right margin mode, which desynchronizes
#   with WezTerm and other terminals in split panes and causes parts of harness
#   UIs to flicker or disappear.
tmux set-option -gq allow-passthrough on
term_features="$(tmux show-options -s terminal-features 2>/dev/null || true)"
case "$term_features" in
	*'xterm*:sync'*) ;;
	*) tmux set-option -as terminal-features ",xterm*:sync:RGB:usstyle:hyperlinks:extkeys" 2>/dev/null || true ;;
esac
term_overrides="$(tmux show-options -s terminal-overrides 2>/dev/null || true)"
case "$term_overrides" in
	*'Cmg@'*) ;;
	*) tmux set-option -as terminal-overrides ",*:Enmg@:Dsmg@:Cmg@:Clmg@" 2>/dev/null || true ;;
esac

tmux bind-key "$SPACES_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN popup"
tmux bind-key "$SPACES_AGENTS_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN agents"
tmux bind-key "$SPACES_NEW_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN popup --new"
tmux bind-key "$SPACES_WORKTREES_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN worktrees"
tmux bind-key "$SPACES_INBOX_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN inbox"
tmux bind-key "$SPACES_CHATS_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN conversations"
if [ -n "$SPACES_SHARED_DIR" ]; then
	tmux bind-key "$SPACES_SHARED_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN shared --dir \"$SPACES_SHARED_DIR\""
else
	tmux bind-key "$SPACES_SHARED_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN shared"
fi
tmux bind-key "$SPACES_TABS_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN tabs"
if [ "$SPACES_NEW_TAB_KEY" != "off" ]; then
	tmux bind-key "$SPACES_NEW_TAB_KEY" display-popup -E -w "$POPUP_WIDTH" -h "$POPUP_HEIGHT" -b "$POPUP_BORDER" "$BIN tabs --new"
fi

# Replace tmux's modal status-line `command-prompt` for renaming tabs (,) and
# spaces ($) with a non-blocking 1-line bottom-bar popup covering the status
# bar so space/tab names never show through behind it and panes keep updating.
# Disable with `set -g @spaces-rename-popup off`.
RENAME_POPUP="$(get_tmux_option "@spaces-rename-popup" "on")"
if [ "$RENAME_POPUP" = "on" ] || [ "$RENAME_POPUP" = "1" ] || [ "$RENAME_POPUP" = "true" ]; then
	tmux bind-key "," display-popup -E -B -x 0 -y "#{?#{==:#{status-position},top},0,#{client_height}}" -w "100%" -h 1 -s "bg=#181825,fg=#cdd6f4" "$BIN popup --rename-window"
	tmux bind-key "\$" display-popup -E -B -x 0 -y "#{?#{==:#{status-position},top},0,#{client_height}}" -w "100%" -h 1 -s "bg=#181825,fg=#cdd6f4" "$BIN popup --rename-space"
fi

# Interpolate #{spaces_status} to the live badge command. When neither side
# carries the placeholder, append the badge to status-right once.
for side in status-right status-left; do
	current="$(tmux show-option -gqv "$side" 2>/dev/null || true)"
	case "$current" in
		*'#{spaces_status}'*)
			updated="${current//'#{spaces_status}'/'#('"${BIN}"' status-bar)'}"
			tmux set-option -gq "$side" "$updated"
			;;
	esac
done

right="$(tmux show-option -gqv "status-right" 2>/dev/null || true)"
left="$(tmux show-option -gqv "status-left" 2>/dev/null || true)"
case "$right$left" in
	*'tmux-spaces status-bar'*) ;;
	*) tmux set-option -gqa status-right " #($BIN status-bar)" ;;
esac

# Dual-layer attention: attach `tmux pipe-pane -O` running `__osc-watch` so
# OSC 7501 program-status sequences from harnesses update `@spaces_*` in real
# time, with hooks and the screen reader as fallbacks. Note: `-O` (without `-o`)
# unconditionally attaches/replaces the pipe; `-o` toggles an existing pipe off
# on config reload or double-hook fire. Disable with `set -g @spaces-osc-watch off`.
OSC_WATCH="$(get_tmux_option "@spaces-osc-watch" "on")"
if [ "$OSC_WATCH" = "on" ] || [ "$OSC_WATCH" = "1" ] || [ "$OSC_WATCH" = "true" ]; then
	hook_cmd="pipe-pane -O \"TMUX=#{socket_path},#{pid},0 $BIN __osc-watch #{pane_id}\""
	for hook_name in after-new-session after-new-window after-split-window after-respawn-pane; do
		tmux set-hook -g "${hook_name}[90]" "$hook_cmd" 2>/dev/null || true
	done
	sock_path="$(tmux display-message -p '#{socket_path}' 2>/dev/null || true)"
	srv_pid="$(tmux display-message -p '#{pid}' 2>/dev/null || true)"
	while IFS= read -r pane_id; do
		[ -n "$pane_id" ] || continue
		if [ -n "$sock_path" ]; then
			tmux pipe-pane -O -t "$pane_id" "TMUX=${sock_path},${srv_pid:-0},0 $BIN __osc-watch $pane_id" 2>/dev/null || true
		else
			tmux pipe-pane -O -t "$pane_id" "$BIN __osc-watch $pane_id" 2>/dev/null || true
		fi
	done < <(tmux list-panes -a -F '#{pane_id}' 2>/dev/null || true)
fi

# Persist running spaces immediately on detach (throttled 30s autosave also runs via status-bar).
tmux set-hook -g "client-detached[90]" "run-shell -b \"$BIN resurrect autosave --force\"" 2>/dev/null || true

# Automatically restore spaces that were last open (not exited) once per fresh
# tmux server startup (e.g., after a reboot). Disable with
# `set -g @spaces-auto-restore off`.
AUTO_RESTORE="$(get_tmux_option "@spaces-auto-restore" "on")"
if [ "$AUTO_RESTORE" = "on" ] || [ "$AUTO_RESTORE" = "1" ] || [ "$AUTO_RESTORE" = "true" ]; then
	RESTORED_STATE="$(tmux show-option -gqv "@spaces_restored" 2>/dev/null || true)"
	if [ -z "$RESTORED_STATE" ]; then
		tmux set-option -gq "@spaces_restored" "pending" 2>/dev/null || true
		tmux run-shell -b "$BIN resurrect auto-restore" 2>/dev/null || true
	fi
fi
