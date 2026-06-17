#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-s5-results.sh
#
# Inspect kilroy run results.
#
# Subcommands:
#   cdlatest    Print the worktree path of the most recent successful kilroy run.
#               Use:  cd "$(kilroy-s5-results.sh cdlatest)"
#   help        Show this help.

set -euo pipefail

RUNS_DIR="${RUNS_DIR:-$HOME/.local/state/kilroy/runs}"

usage() {
  sed -n "2,9p" "$0" | sed "s/^# \{0,1\}//"
}

# Print "success" / "fail" / "active" / "unknown" for a given run dir.
run_status() {
  local rd="$1"
  local prog="$rd/progress.ndjson"
  if [ ! -f "$prog" ]; then
    echo unknown; return
  fi
  if grep -q "\"event\":\"run_completed\".*\"status\":\"success\"" "$prog"; then
    echo success
  elif grep -q "\"event\":\"run_failed\"" "$prog" \
    || grep -q "\"event\":\"run_completed\".*\"status\":\"fail\"" "$prog"; then
    echo fail
  else
    echo active
  fi
}

cmd_cdlatest() {
  local rd
  for rd in $(ls -1dt "$RUNS_DIR"/run-* 2>/dev/null); do
    if [ "$(run_status "$rd")" = "success" ] && [ -d "$rd/worktree" ]; then
      echo "$rd/worktree"
      return 0
    fi
  done
  echo "kilroy-s5-results.sh: no successful run with a worktree in $RUNS_DIR" >&2
  exit 1
}

case "${1:-help}" in
  cdlatest) cmd_cdlatest ;;
  help|-h|--help) usage ;;
  *) echo "unknown subcommand: $1" >&2; usage >&2; exit 2 ;;
esac
