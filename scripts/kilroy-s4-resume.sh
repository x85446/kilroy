#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-s4-resume.sh
#
# Resume the last (or a specified) kilroy run. Three modes:
#   kilroy-s4-resume.sh                          # resume from $RUNS_DIR/last-run pointer
#   kilroy-s4-resume.sh <logs-root>              # resume from a specific logs dir
#   kilroy-s4-resume.sh --cxdb <context-id>      # resume from CXDB by context id

set -euo pipefail

RUNS_DIR="${RUNS_DIR:-$HOME/.local/state/kilroy/runs}"
LAST_RUN_POINTER="$RUNS_DIR/last-run"
CXDB_HTTP="${CXDB_HTTP:-http://127.0.0.1:9010}"

# Probe cliproxy gateway end-to-end (service active + port reachable + claude
# round-trip) before resuming. Bails before re-launching a long run if auth
# is dead. No-op if cliproxy-status.sh isn't installed.
preflight_cliproxy() {
  if ! command -v cliproxy-status.sh >/dev/null 2>&1; then
    return 0
  fi
  echo "→ cliproxy gateway preflight"
  if ! cliproxy-status.sh; then
    echo "kilroy-s4-resume.sh: cliproxy gateway not healthy; fix it before re-running" >&2
    echo "  hints: cliproxy-restart.sh    (clean exit, no auth issue)" >&2
    echo "         cliproxy-login.sh claude   (re-auth Claude OAuth)" >&2
    exit 1
  fi
}

# Workaround for an upstream kilroy bug: checkpoint.json's git_commit_sha is
# `omitempty` and the engine sometimes serializes the checkpoint with an
# empty sha (the `git commit` event in run.log lands a few ms *after* the
# matching `checkpoint.saved`). Resume then bails with
#   "checkpoint missing git_commit_sha"
# Heal it idempotently from the worktree's HEAD before invoking kilroy. No-op
# if jq isn't installed, the worktree is missing, or the field is already set.
heal_checkpoint() {
  local lr="$1"
  local cp_path="$lr/checkpoint.json"
  local mf_path="$lr/manifest.json"
  [ -f "$cp_path" ] || return 0
  command -v jq >/dev/null 2>&1 || return 0
  local existing
  existing=$(jq -r '.git_commit_sha // ""' "$cp_path" 2>/dev/null)
  if [ -n "$existing" ]; then
    return 0
  fi
  [ -e "$lr/worktree/.git" ] || return 0
  local sha
  sha=$(git -C "$lr/worktree" rev-parse HEAD 2>/dev/null) || return 0
  [ -n "$sha" ] || return 0
  echo "→ healing checkpoint: injecting git_commit_sha=$sha (kilroy bug workaround)" >&2
  cp -p "$cp_path" "$cp_path.bak"
  if jq --arg s "$sha" '. + {git_commit_sha: $s}' "$cp_path" > "$cp_path.new"; then
    mv "$cp_path.new" "$cp_path"
  else
    rm -f "$cp_path.new"
    echo "  warn: jq rewrite of $cp_path failed; leaving original in place" >&2
    return 0
  fi
  if [ -f "$mf_path" ]; then
    cp -p "$mf_path" "$mf_path.bak"
    if jq --arg s "$sha" '. + {git_commit_sha: (.git_commit_sha // $s), final_git_commit_sha: (.final_git_commit_sha // $s), initial_git_sha: (.initial_git_sha // $s)}' "$mf_path" > "$mf_path.new"; then
      mv "$mf_path.new" "$mf_path"
    else
      rm -f "$mf_path.new"
    fi
  fi
}

# kilroy's deterministic-failure-cycle detector keeps a counter per
# (node|class|reason) signature in checkpoint.extra.{loop,restart}_failure_
# signatures, and aborts if any signature hits its per-graph limit. The
# counter persists across resumes by design.
#
# Once the cliproxy gateway preflight has passed, we know that any past
# `provider_failure` / connection-refused / auth_failed counters were caused
# by a now-resolved infra outage. Drop just those keys so the next resume
# isn't shut down by stale infra evidence. Real AC- / spec- / impl-failure
# signatures (which represent product bugs) are left intact.
prune_stale_infra_signatures() {
  local lr="$1"
  local cp_path="$lr/checkpoint.json"
  [ -f "$cp_path" ] || return 0
  command -v jq >/dev/null 2>&1 || return 0
  local pattern='provider_failure|auth_failed|connection.refused|connectionrefused|upstream_unavailable|gateway.unhealthy|context_deadline|i/o timeout|kilroy_claude_path|cli_profile=real forbids'
  local stale_keys
  stale_keys=$(jq -r --arg p "$pattern" '
    [(.extra.loop_failure_signatures // {}), (.extra.restart_failure_signatures // {})]
    | map(to_entries // [])
    | flatten
    | map(select(.key | test($p; "i")))
    | unique_by(.key)
    | .[].key
  ' "$cp_path" 2>/dev/null)
  if [ -z "$stale_keys" ]; then
    return 0
  fi
  echo "→ pruning stale infra-failure signatures (cliproxy preflight just passed):" >&2
  while IFS= read -r k; do
    [ -z "$k" ] && continue
    printf '    %s\n' "$k" >&2
  done <<< "$stale_keys"
  cp -p "$cp_path" "$cp_path.bak.prune"
  if jq --arg p "$pattern" '
    (.extra.loop_failure_signatures   //= {})
    | (.extra.restart_failure_signatures //= {})
    | .extra.loop_failure_signatures    |= with_entries(select(.key | test($p; "i") | not))
    | .extra.restart_failure_signatures |= with_entries(select(.key | test($p; "i") | not))
  ' "$cp_path" > "$cp_path.new"; then
    mv "$cp_path.new" "$cp_path"
  else
    rm -f "$cp_path.new"
    echo "  warn: jq prune of $cp_path failed; original left in place" >&2
  fi
}

usage() {
  cat <<USAGE
usage:
  kilroy-s4-resume.sh                          resume last run (from $LAST_RUN_POINTER)
  kilroy-s4-resume.sh <logs-root>              resume from a specific logs-root
  kilroy-s4-resume.sh --cxdb <context-id>      resume from CXDB by context id
  kilroy-s4-resume.sh --list                   list known local runs (newest first)
USAGE
}

case "${1:-}" in
  -h|--help) usage; exit 0 ;;
  --list)
    if [ -d "$RUNS_DIR" ]; then
      ls -1tr "$RUNS_DIR" | grep -v '^last-run$' | tac
    fi
    exit 0
    ;;
  --cxdb)
    [ -n "${2:-}" ] || { echo "--cxdb requires <context-id>" >&2; exit 2; }
    preflight_cliproxy
    echo "→ kilroy attractor resume --cxdb $CXDB_HTTP --context-id $2"
    unset KILROY_CLAUDE_PATH
    exec kilroy attractor resume --cxdb "$CXDB_HTTP" --context-id "$2"
    ;;
  "")
    if [ ! -f "$LAST_RUN_POINTER" ]; then
      echo "no last-run pointer at $LAST_RUN_POINTER" >&2
      echo "options: pass <logs-root> explicitly, or run 'kilroy-s4-resume.sh --list' to see local runs" >&2
      exit 1
    fi
    LOGS_ROOT="$(cat "$LAST_RUN_POINTER")"
    ;;
  -*)
    echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
  *)
    LOGS_ROOT="$1"
    ;;
esac

[ -d "$LOGS_ROOT" ] || { echo "logs-root not found: $LOGS_ROOT" >&2; exit 1; }

heal_checkpoint "$LOGS_ROOT"
preflight_cliproxy
prune_stale_infra_signatures "$LOGS_ROOT"

cat <<INFO
→ kilroy attractor resume
  logs-root:  $LOGS_ROOT
INFO

# kilroy with llm.cli_profile=real refuses to honor KILROY_CLAUDE_PATH (it
# could mask a real install bug). claude is on PATH already; unset the
# override and let kilroy find it normally. Mirrors kilroy-s3-run.sh.
unset KILROY_CLAUDE_PATH
exec kilroy attractor resume --logs-root "$LOGS_ROOT" --no-stage-archive-stacking
