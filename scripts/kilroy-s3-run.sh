#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-s3-run.sh
#
# Execute the pipeline at ~/pipe.dot via `kilroy attractor run`, with CXDB
# observability and a stable per-invocation logs-root so kilroy-s4-resume.sh
# can pick up where this leaves off.

set -euo pipefail

# Default GRAPH: prefer ./pipeline.dot in the current directory (the
# convention when a repo ships a checked-in pipeline). Fall back to
# ~/pipe.dot (kilroy-s1-ingest.sh's default OUT). Override with --graph
# or GRAPH=.
if [ -z "${GRAPH:-}" ]; then
  if [ -f "$PWD/pipeline.dot" ]; then
    GRAPH="$PWD/pipeline.dot"
  else
    GRAPH="$HOME/pipe.dot"
  fi
fi
REPO="${REPO:-$PWD}"
RUNS_DIR="${RUNS_DIR:-$HOME/.local/state/kilroy/runs}"
RUN_ID="${RUN_ID:-run-$(date -u +%Y%m%dT%H%M%SZ)}"
LOGS_ROOT="$RUNS_DIR/$RUN_ID"
LAST_RUN_POINTER="$RUNS_DIR/last-run"
CXDB_HTTP="${CXDB_HTTP:-http://127.0.0.1:9010}"
CXDB_BIN="${CXDB_BIN:-127.0.0.1:9009}"
CXDB_UI="${CXDB_UI:-http://127.0.0.1:9020}"
MODELDB_PATH="${MODELDB_PATH:-$HOME/projects/kilroy/internal/attractor/modeldb/pinned/openrouter_models.json}"

usage() {
  cat <<USAGE
usage: kilroy-s3-run.sh [--graph <file.dot>] [--repo <dir>] [--run-id <id>]

  --graph    .dot pipeline to execute     (auto: ./pipeline.dot if present, else ~/pipe.dot)
  --repo     repo to operate on           (default: \$PWD)
  --run-id   stable id for this run       (default: run-<UTC timestamp>)

env-var equivalents: GRAPH REPO RUN_ID RUNS_DIR CXDB_HTTP CXDB_BIN MODELDB_PATH

writes a per-run config to <logs-root>/run.yaml (CXDB always wired in) and
pins the last-run pointer at $LAST_RUN_POINTER for kilroy-s4-resume.sh.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --graph)  GRAPH="$2"; shift 2 ;;
    --repo)   REPO="$2"; shift 2 ;;
    --run-id) RUN_ID="$2"; LOGS_ROOT="$RUNS_DIR/$RUN_ID"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -f "$GRAPH" ] || { echo "graph not found: $GRAPH (run kilroy-s1-ingest.sh first?)" >&2; exit 1; }
[ -f "$MODELDB_PATH" ] || { echo "modeldb pinned file not found: $MODELDB_PATH" >&2; exit 1; }
git -C "$REPO" rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
  echo "kilroy-s3-run.sh: $REPO is not a git work tree" >&2; exit 1; }

# Probe CXDB before kicking off — fast feedback if the daemon's down.
if ! curl -sfo /dev/null -m 5 -X POST "$CXDB_HTTP/v1/contexts/create" \
      -H "Content-Type: application/json" -d '{"base_turn_id":"0"}'; then
  echo "kilroy-s3-run.sh: CXDB at $CXDB_HTTP did not respond. check 'sudo docker ps | grep cxdb'." >&2
  exit 1
fi

# Probe cliproxy gateway end-to-end (service active + port reachable + claude
# round-trip). Bails before launching a multi-hour run if auth is dead.
if command -v cliproxy-status.sh >/dev/null 2>&1; then
  echo "→ cliproxy gateway preflight"
  if ! cliproxy-status.sh; then
    echo "kilroy-s3-run.sh: cliproxy gateway not healthy; fix it before re-running" >&2
    echo "  hints: cliproxy-restart.sh    (clean exit, no auth issue)" >&2
    echo "         cliproxy-login.sh claude   (re-auth Claude OAuth)" >&2
    exit 1
  fi
fi

mkdir -p "$LOGS_ROOT"
RUN_YAML="$LOGS_ROOT/run.yaml"

cat > "$RUN_YAML" <<YAML
version: 1

repo:
  path: $REPO

cxdb:
  binary_addr: $CXDB_BIN
  http_base_url: $CXDB_HTTP
  # Daemon is managed externally (docker container 'kilroy-cxdb', auto-restart);
  # don't double-start.
  autostart:
    enabled: false

llm:
  cli_profile: real
  providers:
    anthropic:
      backend: cli   # routes through cliproxyapi via ANTHROPIC_BASE_URL=http://127.0.0.1:8317

modeldb:
  openrouter_model_info_path: $MODELDB_PATH
  openrouter_model_info_update_policy: pinned

git:
  require_clean: false
  run_branch_prefix: attractor/run
  commit_per_node: true

runtime_policy:
  stage_timeout_ms: 0
  stall_timeout_ms: 1800000
  stall_check_interval_ms: 5000
  max_llm_retries: 12
YAML

# Persist pointer for kilroy-s4-resume.sh.
echo "$LOGS_ROOT" > "$LAST_RUN_POINTER"

cat <<INFO
→ kilroy attractor run
  graph:      $GRAPH
  repo:       $REPO
  run id:     $RUN_ID
  logs-root:  $LOGS_ROOT
  cxdb http:  $CXDB_HTTP
  cxdb ui:    $CXDB_UI
  config:     $RUN_YAML
INFO

cd "$REPO"
# kilroy with llm.cli_profile=real refuses to honor KILROY_CLAUDE_PATH (it
# could mask a real install bug). claude is on PATH already via the
# cligateway env, so unset the override and let kilroy find it normally.
unset KILROY_CLAUDE_PATH
exec kilroy attractor run \
  --graph "$GRAPH" \
  --config "$RUN_YAML" \
  --logs-root "$LOGS_ROOT" \
  --run-id "$RUN_ID" \
  --no-stage-archive-stacking
