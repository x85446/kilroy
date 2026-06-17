#!/usr/bin/env bash
# kilroy-activeCleanup.sh — safely free disk while a kilroy run is in flight.
#
# Lives at: /opt/darkfactory/scripts/kilroy-activeCleanup.sh
# Symlinked into: /opt/darkfactory/bin/kilroy-activeCleanup.sh (on PATH).
# Master copy: ~/workspace/x85446/creds/kilroy-activeCleanup.sh on the workstation.
#
# WHAT IT DOES (safety analysis is performed at run time, NOT hardcoded)
#
#   The script reads the run's own state to decide what is dead vs in-flight:
#
#     · the active run is found via $RUNS_ROOT/last-run, or --run <path>
#     · the in-flight node id is read from live.json (kilroy keeps this current);
#       if absent, the run is treated as not-live and Tier 1 is still safe
#     · for each fanout under parallel/<fanout>/, the script enumerates the
#       pass directories (pass1, pass2, ...) and the HIGHEST pass per fanout
#       is treated as the active pass — never touched. All earlier passes
#       are dead and their Cargo target/ dirs are eligible for Tier 1 deletion.
#     · for each node dir directly under the run root, the highest visit_N
#       is kept; older visit_N/stage.tgz archives are eligible for Tier 2.
#     · the in-flight node (from live.json) is skipped entirely — its highest
#       AND second-highest visit are both preserved, in case the engine is
#       mid-write.
#     · root-level <node>/stage.tgz (i.e. NOT inside a visit_N dir) is the
#       stage's "current snapshot" fed forward to the next node — never deleted.
#
# WHAT TIERS REMOVE
#
#   Tier 1 (default, biggest win, lowest risk):
#     `target/` directories inside completed pass worktrees under
#     parallel/<fanout>/pass<not-active>/*/worktree/. These are Cargo build
#     caches that kilroy never re-reads after its pass completes.
#
#   Tier 2 (modest win, still safe):
#     Older `visit_N/stage.tgz` archives — keep newest per node, delete the
#     rest. Skips the in-flight node entirely.
#
# WHAT IS NEVER TOUCHED
#   - worktree/ (main, in-flight)
#   - parallel/<fanout>/pass<MAX>/ (active pass for any fanout)
#   - the in-flight node's directory (from live.json)
#   - the highest visit_N/stage.tgz per node
#   - root-level <node>/stage.tgz (no visit_N parent)
#   - progress.ndjson, live.json, manifest.json, run.lock.json, run.pid,
#     status.json, checkpoint.json, run.yaml, run_config.json, graph.dot
#   - inputs_manifest.json / input_snapshot/ / .ai/runs/<run-id>/ content
#   - the source repo's .git directory and any branch refs
#
# USAGE
#   kilroy-activeCleanup.sh                     # tier1, prompts before deleting
#   kilroy-activeCleanup.sh --pretend           # show what would be removed (no deletions)
#   kilroy-activeCleanup.sh --tier1 --yes       # tier1, no prompt
#   kilroy-activeCleanup.sh --tier2             # tier2 only
#   kilroy-activeCleanup.sh --all               # both tiers
#   kilroy-activeCleanup.sh --run <run-path>    # specific run dir, not last-run

set -uo pipefail

RUNS_ROOT="${KILROY_RUNS_ROOT:-$HOME/.local/state/kilroy/runs}"
LAST_RUN_POINTER="$RUNS_ROOT/last-run"

DO_TIER1=1
DO_TIER2=0
PRETEND=0
ASSUME_YES=0
RUN_ARG=""

usage() { sed -n '2,60p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage 0 ;;
    --pretend|--dry-run|-n) PRETEND=1 ;;
    -y|--yes) ASSUME_YES=1 ;;
    --tier1) DO_TIER1=1; DO_TIER2=0 ;;
    --tier2) DO_TIER1=0; DO_TIER2=1 ;;
    --all|--both) DO_TIER1=1; DO_TIER2=1 ;;
    --run) shift; RUN_ARG="${1:?}" ;;
    --run=*) RUN_ARG="${1#*=}" ;;
    *) echo "unknown arg: $1" >&2; usage 2 ;;
  esac
  shift
done

c_red(){ printf '\033[0;31m%s\033[0m' "$*"; }
c_grn(){ printf '\033[0;32m%s\033[0m' "$*"; }
c_yel(){ printf '\033[0;33m%s\033[0m' "$*"; }
c_dim(){ printf '\033[0;90m%s\033[0m' "$*"; }
log()  { printf '%s %s\n' "$(c_dim "$(date -u +%H:%M:%S)")" "$*"; }
say()  { printf '%s\n' "$*"; }

human_bytes() {
  awk -v n="${1:-0}" 'BEGIN {
    s="B KB MB GB TB PB"; split(s,a," ")
    i=1; while (n>=1024 && i<6) { n/=1024; i++ }
    printf "%.2f %s", n, a[i]
  }'
}

# ───── resolve target run ─────
resolve_run() {
  local arg="$1" path
  if [ -n "$arg" ]; then
    if [ -d "$arg" ]; then path="$(readlink -f "$arg")"
    elif [ -d "$RUNS_ROOT/$arg" ]; then path="$RUNS_ROOT/$arg"
    else echo "could not resolve run: $arg" >&2; return 1
    fi
  else
    [ -e "$LAST_RUN_POINTER" ] || { echo "no last-run pointer at $LAST_RUN_POINTER" >&2; return 1; }
    if [ -L "$LAST_RUN_POINTER" ]; then path="$(readlink -f "$LAST_RUN_POINTER")"
    else path="$(tr -d '\r\n' < "$LAST_RUN_POINTER")"
    fi
  fi
  case "$path" in
    "$RUNS_ROOT"/*) ;;
    *) echo "refusing: resolved run $path is not under runs root $RUNS_ROOT" >&2; return 1 ;;
  esac
  [ -d "$path" ] || { echo "run dir does not exist: $path" >&2; return 1; }
  printf '%s\n' "$path"
}

RUN="$(resolve_run "$RUN_ARG")" || exit 1
RUN_ID="$(basename "$RUN")"

# Read in-flight node id from live.json (if any). Engine keeps this current.
ACTIVE_NODE=""
if [ -f "$RUN/live.json" ]; then
  if command -v jq >/dev/null 2>&1; then
    ACTIVE_NODE="$(jq -r '.node_id // empty' "$RUN/live.json" 2>/dev/null || true)"
  else
    ACTIVE_NODE="$(sed -n 's/.*"node_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$RUN/live.json" | head -1)"
  fi
fi

LIVE="no"
if [ -f "$RUN/run.pid" ]; then
  pid="$(tr -d ' \r\n' < "$RUN/run.pid" 2>/dev/null || true)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then LIVE="yes"; fi
fi

# ───── helper: highest pass number under parallel/<fanout>/ ─────
highest_pass_for_fanout() {
  local fanout_dir="$1" max=0 n
  for d in "$fanout_dir"/pass*/; do
    [ -d "$d" ] || continue
    n="${d%/}"; n="${n##*/pass}"
    case "$n" in (*[!0-9]*|"") continue ;; esac
    [ "$n" -gt "$max" ] && max="$n"
  done
  printf '%s\n' "$max"
}

# ───── inventory ─────
echo
echo "$(c_grn '== kilroy-activeCleanup ==')"
echo "Run:           $RUN_ID"
echo "Path:          $RUN"
echo "Engine alive:  $LIVE"
echo "Active node:   ${ACTIVE_NODE:-(none — run not live or no live.json)}"
echo "Tier 1:        $([ "$DO_TIER1" -eq 1 ] && echo ENABLED || echo skipped)  (Cargo target/ in dead pass worktrees)"
echo "Tier 2:        $([ "$DO_TIER2" -eq 1 ] && echo ENABLED || echo skipped)  (older visit_N/stage.tgz)"
echo "Mode:          $([ "$PRETEND" -eq 1 ] && c_yel PRETEND || echo execute)"
echo

# ───── plan tier 1 ─────
TIER1_PATHS=()
TIER1_BYTES=0
if [ "$DO_TIER1" -eq 1 ]; then
  echo "$(c_grn '— Tier 1 plan: target/ dirs in completed parallel passes —')"
  for fanout_dir in "$RUN"/parallel/*/; do
    [ -d "$fanout_dir" ] || continue
    fanout_name="$(basename "$fanout_dir")"
    max_pass="$(highest_pass_for_fanout "$fanout_dir")"
    if [ "$max_pass" -lt 1 ]; then
      printf '  %s no pass dirs found, skipping\n' "$fanout_name"
      continue
    fi
    printf '  %s: active=pass%s  cleaning passes 1..%s\n' "$fanout_name" "$max_pass" "$((max_pass-1))"
    for pass_dir in "$fanout_dir"pass*/; do
      [ -d "$pass_dir" ] || continue
      n="${pass_dir%/}"; n="${n##*/pass}"
      case "$n" in (*[!0-9]*|"") continue ;; esac
      [ "$n" -ge "$max_pass" ] && continue   # active pass: skip
      # find target/ dirs anywhere underneath this completed pass
      while IFS= read -r tgt; do
        [ -d "$tgt" ] || continue
        sz="$(du -sb "$tgt" 2>/dev/null | awk '{print $1}')"
        sz="${sz:-0}"
        TIER1_PATHS+=("$tgt")
        TIER1_BYTES=$(( TIER1_BYTES + sz ))
        printf '    %s  %s\n' "$(c_dim "$(human_bytes "$sz")")" "$tgt"
      done < <(find "$pass_dir" -maxdepth 6 -type d -name target -prune 2>/dev/null)
    done
  done
  printf '  Tier 1 total: %s across %d dirs\n\n' "$(c_yel "$(human_bytes "$TIER1_BYTES")")" "${#TIER1_PATHS[@]}"
fi

# ───── plan tier 2 ─────
TIER2_PATHS=()
TIER2_BYTES=0
if [ "$DO_TIER2" -eq 1 ]; then
  echo "$(c_grn '— Tier 2 plan: older visit_N/stage.tgz archives —')"
  for node_dir in "$RUN"/*/; do
    [ -d "$node_dir" ] || continue
    node_name="$(basename "$node_dir")"
    # skip non-node dirs
    case "$node_name" in
      worktree|parallel|input_snapshot|modeldb) continue ;;
    esac
    # if this is the in-flight node, skip entirely (preserve every visit dir)
    if [ -n "$ACTIVE_NODE" ] && [ "$node_name" = "$ACTIVE_NODE" ]; then
      printf '  %s: in-flight, skipped entirely\n' "$node_name"
      continue
    fi
    # find visit_N/stage.tgz under this node, keep the highest visit_N
    mapfile -t visits < <(ls -v "$node_dir"visit_*/stage.tgz 2>/dev/null)
    [ "${#visits[@]}" -le 1 ] && continue   # 0 or 1 visits: nothing to drop
    keep="${visits[$(( ${#visits[@]} - 1 ))]}"
    for v in "${visits[@]}"; do
      [ "$v" = "$keep" ] && continue
      sz="$(stat -c '%s' "$v" 2>/dev/null || echo 0)"
      TIER2_PATHS+=("$v")
      TIER2_BYTES=$(( TIER2_BYTES + sz ))
    done
    printf '  %s: keep %s, drop %d older visits\n' "$node_name" "$(basename "$(dirname "$keep")")" "$(( ${#visits[@]} - 1 ))"
  done
  printf '  Tier 2 total: %s across %d files\n\n' "$(c_yel "$(human_bytes "$TIER2_BYTES")")" "${#TIER2_PATHS[@]}"
fi

TOTAL_BYTES=$(( TIER1_BYTES + TIER2_BYTES ))
TOTAL_COUNT=$(( ${#TIER1_PATHS[@]} + ${#TIER2_PATHS[@]} ))

echo "─────────────────────────────────────────────"
echo "Total to remove: $(c_grn "$(human_bytes "$TOTAL_BYTES")") across $TOTAL_COUNT items"
df -h "$RUN" 2>/dev/null | tail -2
echo

if [ "$TOTAL_COUNT" -eq 0 ]; then
  echo "Nothing to do."
  exit 0
fi

if [ "$PRETEND" -eq 1 ]; then
  echo "$(c_yel '(pretend mode — nothing deleted)')"
  exit 0
fi

if [ "$ASSUME_YES" -ne 1 ]; then
  printf 'Proceed? Type %s to confirm: ' "$(c_red CLEAN)"
  read -r ans
  [ "$ans" = "CLEAN" ] || { echo "aborted."; exit 1; }
fi

# ───── execute ─────
removed_bytes=0
removed_count=0

if [ "${#TIER1_PATHS[@]}" -gt 0 ]; then
  log "Tier 1: removing ${#TIER1_PATHS[@]} target/ dirs"
  for p in "${TIER1_PATHS[@]}"; do
    [ -d "$p" ] || continue
    sz="$(du -sb "$p" 2>/dev/null | awk '{print $1}')"
    if rm -rf -- "$p" 2>/dev/null; then
      removed_bytes=$(( removed_bytes + ${sz:-0} ))
      removed_count=$(( removed_count + 1 ))
    else
      log "$(c_yel WARN) could not remove $p"
    fi
  done
fi

if [ "${#TIER2_PATHS[@]}" -gt 0 ]; then
  log "Tier 2: removing ${#TIER2_PATHS[@]} stage.tgz files"
  for p in "${TIER2_PATHS[@]}"; do
    [ -f "$p" ] || continue
    sz="$(stat -c '%s' "$p" 2>/dev/null || echo 0)"
    if rm -f -- "$p" 2>/dev/null; then
      removed_bytes=$(( removed_bytes + sz ))
      removed_count=$(( removed_count + 1 ))
    else
      log "$(c_yel WARN) could not remove $p"
    fi
  done
fi

echo
echo "$(c_grn 'OK')  removed $removed_count items, $(human_bytes "$removed_bytes")"
df -h "$RUN" 2>/dev/null | tail -2
