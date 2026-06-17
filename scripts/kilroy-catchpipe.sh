#!/usr/bin/env bash
# kilroy-catchpipe.sh — snapshot a pipeline.dot (or any file) on every size
# change, before kilroy deletes its temp dir.
#
# Lives at: /opt/darkfactory/scripts/kilroy-catchpipe.sh
# Symlinked into: /opt/darkfactory/bin/kilroy-catchpipe.sh (on PATH).
# Master copy: ~/workspace/x85446/creds/kilroy-catchpipe.sh on the workstation.
#
# WHAT IT DOES
#   Watches the path(s) you give it. Each time a watched file's size changes
#   from its previously-recorded size, copies it to a snapshot dir under a
#   fresh, timestamped name. History accumulates — nothing is overwritten.
#   Survives the source file being deleted (it stops emitting for that path
#   until/unless the file reappears).
#
# USAGE
#   kilroy-catchpipe.sh <path-or-glob> [more paths...]
#   kilroy-catchpipe.sh "/tmp/kilroy-ingest-*/pipeline.dot"
#   kilroy-catchpipe.sh -d ~/snapshots -i 0.1 /tmp/foo/pipeline.dot
#
# OPTIONS
#   -d, --dir <dir>      snapshot output dir (default: /tmp/kilroy-catchpipe)
#   -i, --interval <s>   poll interval seconds (default: 0.2)
#   -q, --quiet          only print on snapshot (default: print every poll change)
#       --once           snapshot the first non-empty observation and exit
#   -h, --help           this message
#
# Tip: pass globs IN QUOTES so the shell doesn't expand them once at start —
# this script re-globs on every poll so newly-created paths get picked up.

set -uo pipefail

OUT_DIR="${KILROY_CATCH_DIR:-/tmp/kilroy-catchpipe}"
INTERVAL="0.2"
QUIET=0
ONCE=0
PATTERNS=()

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage 0 ;;
    -d|--dir) shift; OUT_DIR="${1:?}" ;;
    -i|--interval) shift; INTERVAL="${1:?}" ;;
    -q|--quiet) QUIET=1 ;;
    --once) ONCE=1 ;;
    --) shift; while [ $# -gt 0 ]; do PATTERNS+=("$1"); shift; done; break ;;
    -*) echo "unknown arg: $1" >&2; usage 2 ;;
    *) PATTERNS+=("$1") ;;
  esac
  shift
done

if [ "${#PATTERNS[@]}" -eq 0 ]; then
  echo "error: at least one path or glob required" >&2
  usage 2
fi

mkdir -p "$OUT_DIR"

c_grn(){ printf '\033[0;32m%s\033[0m' "$*"; }
c_yel(){ printf '\033[0;33m%s\033[0m' "$*"; }
c_dim(){ printf '\033[0;90m%s\033[0m' "$*"; }
log(){ printf '%s %s\n' "$(c_dim "$(date -u +%H:%M:%S)")" "$*"; }

# associative array: last seen size keyed by absolute path
declare -A LAST_SIZE
declare -A SNAPSHOT_COUNT

file_size() {
  # Cross-platform-ish: GNU stat first, fall back to BSD stat, fall back to wc -c.
  local f="$1" sz
  sz="$(stat -c '%s' -- "$f" 2>/dev/null || stat -f '%z' -- "$f" 2>/dev/null || wc -c <"$f" 2>/dev/null | awk '{print $1}')"
  printf '%s' "${sz:-0}"
}

snapshot() {
  local src="$1" sz="$2"
  local base ts dst
  base="$(basename -- "$src")"
  ts="$(date -u +%Y%m%dT%H%M%S.%3N)"
  # include the source dir's tail so concurrent watches on multiple matches
  # don't clobber each other when basenames coincide
  local tail
  tail="$(printf '%s' "$src" | awk -F/ '{print $(NF-1)}')"
  dst="$OUT_DIR/${tail}__${base%.*}-${ts}-sz${sz}.${base##*.}"
  # if no extension, fall back to a simpler name
  case "$base" in
    *.*) : ;;
    *)   dst="$OUT_DIR/${tail}__${base}-${ts}-sz${sz}" ;;
  esac
  if cp -p -- "$src" "$dst" 2>/dev/null; then
    SNAPSHOT_COUNT["$src"]=$(( ${SNAPSHOT_COUNT["$src"]:-0} + 1 ))
    log "$(c_grn 'SNAP') ${src}  ($(c_yel "${sz}B")) -> $(basename "$dst")  [count=${SNAPSHOT_COUNT[$src]}]"
  else
    log "$(c_yel 'WARN') failed to copy ${src} (vanished mid-snapshot?)"
  fi
}

trap 'echo; log "stopped"; exit 0' INT TERM

log "watching: ${PATTERNS[*]}"
log "out dir:  $OUT_DIR"
log "interval: ${INTERVAL}s"
log "press Ctrl+C to stop"
echo

while :; do
  matched_any=0
  for pat in "${PATTERNS[@]}"; do
    # let the shell expand the glob; if no match, the literal pattern is left,
    # which we then test for existence and skip if absent.
    # shellcheck disable=SC2206
    candidates=( $pat )
    for f in "${candidates[@]}"; do
      [ -f "$f" ] || continue
      matched_any=1
      sz="$(file_size "$f")"
      prev="${LAST_SIZE[$f]:-__NONE__}"
      if [ "$prev" != "$sz" ]; then
        # only snap when nonzero AND stable-enough (immediate snap)
        if [ "$sz" -gt 0 ]; then
          snapshot "$f" "$sz"
          if [ "$ONCE" -eq 1 ]; then
            log "exiting (--once)"; exit 0
          fi
        else
          [ "$QUIET" -eq 0 ] && log "$(c_dim 'wait') ${f} (size=0)"
        fi
        LAST_SIZE["$f"]="$sz"
      fi
    done
  done
  sleep "$INTERVAL"
done
