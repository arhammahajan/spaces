package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const shellInitUsage = `usage: tmux-spaces shell-init <zsh|bash|fish>

Print an idempotent interactive shell hook that reports coding-agent exit
status (done/failed + exit code) to 'spaces __space-hooks --record-outcome'
when running inside tmux.

Examples:
  eval "$(tmux-spaces shell-init zsh)"
  eval "$(tmux-spaces shell-init bash)"
  tmux-spaces shell-init fish | source
`

// cmdShellInit prints an interactive shell hook script for zsh, bash, or fish.
func cmdShellInit(args []string, env Env) int {
	pf, err := parseFlags(args, nil, nil, nil)
	if errors.Is(err, errHelp) {
		env.outf("%s", shellInitUsage)
		return ExitOK
	}
	if err != nil {
		env.errf("tmux-spaces shell-init: %v\n", err)
		return ExitUsage
	}
	if len(pf.pos) != 1 {
		env.errf("usage: tmux-spaces shell-init <zsh|bash|fish>\n")
		return ExitUsage
	}

	names := runtimeAgentCommandNames(agent.DefaultRegistry())
	shell := strings.ToLower(strings.TrimSpace(pf.pos[0]))
	switch shell {
	case "zsh":
		env.outf("%s", renderZshShellInit(names))
		return ExitOK
	case "bash":
		env.outf("%s", renderBashShellInit(names))
		return ExitOK
	case "fish":
		env.outf("%s", renderFishShellInit(names))
		return ExitOK
	default:
		env.errf("tmux-spaces shell-init: unsupported shell %q (expected zsh, bash, or fish)\n", pf.pos[0])
		return ExitUsage
	}
}

// runtimeAgentCommandNames collects the deterministic, sorted set of command
// names for all registered runtime agent plugins (canonical ID, effective
// binary, and single-token aliases).
func runtimeAgentCommandNames(reg *agent.Registry) []string {
	if reg == nil {
		reg = agent.DefaultRegistry()
	}
	seen := make(map[string]struct{})
	add := func(raw string) {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !isSafeShellCommandToken(name) {
			return
		}
		seen[name] = struct{}{}
	}
	for _, p := range reg.RuntimePlugins() {
		spec := p.Spec()
		add(spec.ID)
		add(spec.EffectiveBinary())
		for _, alias := range spec.Aliases {
			add(alias)
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// isSafeShellCommandToken reports whether s is a non-empty single-token command
// name free of whitespace and shell pattern metacharacters.
func isSafeShellCommandToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == ':' || r == '+':
		default:
			return false
		}
	}
	return true
}

func renderZshShellInit(agentNames []string) string {
	pattern := strings.Join(agentNames, "|")
	return fmt.Sprintf(`# tmux-spaces shell-init zsh
# Generated dynamically from registered tmux-spaces agent plugins.

_tmux_spaces_is_agent() {
  case "$1" in
    %s) return 0 ;;
    *) return 1 ;;
  esac
}

_tmux_spaces_json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  printf '%%s' "$s"
}

_tmux_spaces_preexec() {
  emulate -L zsh
  [[ -z "${TMUX_PANE:-}" || -z "${1:-}" ]] && return
  _TMUX_SPACES_ACTIVE_APP=""
  _TMUX_SPACES_ACTIVE_PROFILE=""

  local -a words
  words=(${(z)1})
  local idx=1
  local n=${#words[@]}
  while (( idx <= n )); do
    local w="${words[$idx]}"
    case "$w" in
      env|[A-Za-z_]*=*)
        (( idx++ ))
        continue
        ;;
    esac
    break
  done
  (( idx > n )) && return

  local cmd="${(Q)words[$idx]}"
  cmd="${cmd##*/}"
  if _tmux_spaces_is_agent "$cmd"; then
    _TMUX_SPACES_ACTIVE_APP="$cmd"
    return
  fi
  if [[ "$cmd" == "harness" && $(( idx + 1 )) -le n ]]; then
    local sub="${(Q)words[$(( idx + 1 ))]}"
    if _tmux_spaces_is_agent "$sub"; then
      _TMUX_SPACES_ACTIVE_APP="$sub"
      if (( idx + 3 <= n )) && [[ "${(Q)words[$(( idx + 2 ))]}" == "profile" ]]; then
        _TMUX_SPACES_ACTIVE_PROFILE="${(Q)words[$(( idx + 3 ))]}"
      fi
    fi
  fi
}

_tmux_spaces_precmd() {
  local ec=$?
  emulate -L zsh
  local app="${_TMUX_SPACES_ACTIVE_APP:-}"
  local profile="${_TMUX_SPACES_ACTIVE_PROFILE:-}"
  _TMUX_SPACES_ACTIVE_APP=""
  _TMUX_SPACES_ACTIVE_PROFILE=""

  if [[ -z "${TMUX_PANE:-}" || -z "$app" ]]; then
    return $ec
  fi
  command -v spaces >/dev/null 2>&1 || return $ec

  local outcome="done"
  local text="finished"
  if (( ec != 0 )); then
    outcome="failed"
    text="exited with status ${ec}"
  fi

  local ts_ms
  ts_ms="$(date +%%s%%3N 2>/dev/null)"
  case "$ts_ms" in
    *[!0-9]*|"") ts_ms="$(date +%%s)000" ;;
  esac

  local j_pane j_app j_profile j_cwd payload
  j_pane="$(_tmux_spaces_json_escape "$TMUX_PANE")"
  j_app="$(_tmux_spaces_json_escape "$app")"
  j_profile="$(_tmux_spaces_json_escape "$profile")"
  j_cwd="$(_tmux_spaces_json_escape "$PWD")"
  payload="{\"pane\":\"${j_pane}\",\"app\":\"${j_app}\",\"profile\":\"${j_profile}\",\"outcome\":\"${outcome}\",\"text\":\"${text}\",\"cwd\":\"${j_cwd}\",\"ts_ms\":${ts_ms}}"

  spaces __space-hooks --record-outcome "$payload" >/dev/null 2>&1 &!
  return $ec
}

autoload -Uz add-zsh-hook
add-zsh-hook preexec _tmux_spaces_preexec
add-zsh-hook precmd _tmux_spaces_precmd
`, pattern)
}

func renderBashShellInit(agentNames []string) string {
	pattern := strings.Join(agentNames, "|")
	return fmt.Sprintf(`# tmux-spaces shell-init bash
# Generated dynamically from registered tmux-spaces agent plugins.

_tmux_spaces_is_agent() {
  case "$1" in
    %s) return 0 ;;
    *) return 1 ;;
  esac
}

_tmux_spaces_json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  printf '%%s' "$s"
}

_tmux_spaces_strip_quotes() {
  local s="$1"
  s="${s#\"}"
  s="${s%%\"}"
  s="${s#\'}"
  s="${s%%\'}"
  printf '%%s' "$s"
}

_tmux_spaces_preexec() {
  [ -z "${TMUX_PANE:-}" ] && return
  [ "${_TMUX_SPACES_IN_PROMPT:-0}" = "1" ] && return
  [ -n "${COMP_LINE:-}" ] && return
  [ -n "${_TMUX_SPACES_ACTIVE_APP:-}" ] && return

  local cmdline="$1"
  [ -z "$cmdline" ] && return

  local -a words
  IFS=$' \t\n' read -ra words <<< "$cmdline"
  local idx=0
  local n=${#words[@]}
  while [ "$idx" -lt "$n" ]; do
    local w="${words[$idx]}"
    case "$w" in
      env|[A-Za-z_]*=*)
        idx=$((idx + 1))
        continue
        ;;
    esac
    break
  done
  [ "$idx" -ge "$n" ] && return

  local cmd
  cmd="$(_tmux_spaces_strip_quotes "${words[$idx]}")"
  cmd="${cmd##*/}"
  if _tmux_spaces_is_agent "$cmd"; then
    _TMUX_SPACES_ACTIVE_APP="$cmd"
    _TMUX_SPACES_ACTIVE_PROFILE=""
    return
  fi
  if [ "$cmd" = "harness" ] && [ $((idx + 1)) -lt "$n" ]; then
    local sub
    sub="$(_tmux_spaces_strip_quotes "${words[$((idx + 1))]}")"
    if _tmux_spaces_is_agent "$sub"; then
      _TMUX_SPACES_ACTIVE_APP="$sub"
      _TMUX_SPACES_ACTIVE_PROFILE=""
      if [ $((idx + 3)) -lt "$n" ] && [ "$(_tmux_spaces_strip_quotes "${words[$((idx + 2))]}")" = "profile" ]; then
        _TMUX_SPACES_ACTIVE_PROFILE="$(_tmux_spaces_strip_quotes "${words[$((idx + 3))]}")"
      fi
    fi
  fi
}

_tmux_spaces_precmd() {
  local ec=$?
  _TMUX_SPACES_IN_PROMPT=1
  local app="${_TMUX_SPACES_ACTIVE_APP:-}"
  local profile="${_TMUX_SPACES_ACTIVE_PROFILE:-}"
  _TMUX_SPACES_ACTIVE_APP=""
  _TMUX_SPACES_ACTIVE_PROFILE=""

  if [ -n "${TMUX_PANE:-}" ] && [ -n "$app" ] && command -v spaces >/dev/null 2>&1; then
    local outcome="done"
    local text="finished"
    if [ "$ec" -ne 0 ]; then
      outcome="failed"
      text="exited with status ${ec}"
    fi

    local ts_ms
    ts_ms="$(date +%%s%%3N 2>/dev/null)"
    case "$ts_ms" in
      *[!0-9]*|"") ts_ms="$(date +%%s)000" ;;
    esac

    local j_pane j_app j_profile j_cwd payload
    j_pane="$(_tmux_spaces_json_escape "$TMUX_PANE")"
    j_app="$(_tmux_spaces_json_escape "$app")"
    j_profile="$(_tmux_spaces_json_escape "$profile")"
    j_cwd="$(_tmux_spaces_json_escape "$PWD")"
    payload="{\"pane\":\"${j_pane}\",\"app\":\"${j_app}\",\"profile\":\"${j_profile}\",\"outcome\":\"${outcome}\",\"text\":\"${text}\",\"cwd\":\"${j_cwd}\",\"ts_ms\":${ts_ms}}"

    ( spaces __space-hooks --record-outcome "$payload" >/dev/null 2>&1 & )
  fi

  _TMUX_SPACES_IN_PROMPT=0
  return $ec
}

if [ -z "${_TMUX_SPACES_BASH_HOOK_INSTALLED:-}" ]; then
  _TMUX_SPACES_BASH_HOOK_INSTALLED=1
  trap '_tmux_spaces_preexec "$BASH_COMMAND"' DEBUG
fi

case ";${PROMPT_COMMAND:-};" in
  *";_tmux_spaces_precmd;"*) ;;
  *)
    if [ -n "${PROMPT_COMMAND:-}" ]; then
      PROMPT_COMMAND="_tmux_spaces_precmd;${PROMPT_COMMAND}"
    else
      PROMPT_COMMAND="_tmux_spaces_precmd"
    fi
    ;;
esac
`, pattern)
}

func renderFishShellInit(agentNames []string) string {
	cases := strings.Join(agentNames, " ")
	return fmt.Sprintf(`# tmux-spaces shell-init fish
# Generated dynamically from registered tmux-spaces agent plugins.

function _tmux_spaces_is_agent
    switch $argv[1]
        case %s
            return 0
        case '*'
            return 1
    end
end

function _tmux_spaces_json_escape
    string replace -a '\\' '\\\\' -- "$argv[1]" | string replace -a '"' '\\"'
end

function _tmux_spaces_postexec --on-event fish_postexec
    set -l ec $status
    test -z "$TMUX_PANE"; and return
    type -q spaces; or return

    set -l cmdline (string trim -- "$argv[1]")
    test -z "$cmdline"; and return

    set -l words (string match -ra '\S+' -- "$cmdline")
    set -l idx 1
    set -l n (count $words)
    while test $idx -le $n
        set -l w $words[$idx]
        if test "$w" = "env"; or string match -qr '^[A-Za-z_][A-Za-z0-9_]*=' -- "$w"
            set idx (math $idx + 1)
            continue
        end
        break
    end
    test $idx -gt $n; and return

    set -l cmd (string trim -c "\"'" -- $words[$idx])
    set cmd (string replace -r '^.*/' '' -- "$cmd")

    set -l app ""
    set -l profile ""
    if _tmux_spaces_is_agent "$cmd"
        set app "$cmd"
    else if test "$cmd" = "harness"; and test (math $idx + 1) -le $n
        set -l sub (string trim -c "\"'" -- $words[(math $idx + 1)])
        if _tmux_spaces_is_agent "$sub"
            set app "$sub"
            if test (math $idx + 3) -le $n; and test (string trim -c "\"'" -- $words[(math $idx + 2)]) = "profile"
                set profile (string trim -c "\"'" -- $words[(math $idx + 3)])
            end
        end
    end

    test -z "$app"; and return

    set -l outcome "done"
    set -l text "finished"
    if test $ec -ne 0
        set outcome "failed"
        set text "exited with status $ec"
    end

    set -l ts_ms (date +%%s%%3N 2>/dev/null)
    if not string match -qr '^[0-9]+$' -- "$ts_ms"
        set ts_ms (date +%%s)"000"
    end

    set -l j_pane (_tmux_spaces_json_escape "$TMUX_PANE")
    set -l j_app (_tmux_spaces_json_escape "$app")
    set -l j_profile (_tmux_spaces_json_escape "$profile")
    set -l j_cwd (_tmux_spaces_json_escape "$PWD")
    set -l payload "{\"pane\":\"$j_pane\",\"app\":\"$j_app\",\"profile\":\"$j_profile\",\"outcome\":\"$outcome\",\"text\":\"$text\",\"cwd\":\"$j_cwd\",\"ts_ms\":$ts_ms}"

    spaces __space-hooks --record-outcome "$payload" >/dev/null 2>&1 &
end
`, cases)
}
