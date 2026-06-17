#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-s1-ingest.sh
#
# Run `kilroy attractor ingest` against the current repo, producing the
# pipeline .dot. kilroy passes the cwd to claude via `--add-dir`, so the
# agent has Read/Glob/Grep over the whole repo — no need to pipe files in.

set -euo pipefail

OUT="${OUT:-}"  # default resolved to <repo>/pipeline.dot after REPO is finalized
MODEL="${MODEL:-claude-opus-4-7}"
MAX_TURNS="${MAX_TURNS:-40}"
REPO="${REPO:-$PWD}"

# Default requirements: when the caller doesn't pass a prompt, instruct the
# agent to read every <repo>/docs/*.md itself. kilroy already mounts the repo
# via `--add-dir`, so claude has Read/Glob/Grep over them — no need to inline
# the contents (which would blow MAX_ARG_STRLEN on large doc trees).
build_default_req() {
  local repo="$1"
  shopt -s nullglob
  local files=("$repo"/docs/*.md)
  shopt -u nullglob
  if [ ${#files[@]} -eq 0 ]; then
    echo "kilroy-s1-ingest.sh: no docs/*.md files found under $repo" >&2
    return 1
  fi
  local list=""
  local f
  for f in "${files[@]}"; do
    list+="  - ${f#"$repo"/}"$'\n'
  done
  cat <<PREAMBLE
The full system spec is in the docs/ tree of this repo. Read every file
listed below (plus any other repo files they reference), then produce an
Attractor .dot pipeline that delivers the system end-to-end. Use the
create-dotfile skill conventions exactly. If anything is ambiguous, prefer
the conservative interpretation and add a comment on the relevant node
explaining the assumption.

Files to read:
${list}
PREAMBLE
}

usage() {
  cat <<USAGE
usage: kilroy-s1-ingest.sh [-o <out.dot>] [-m <model>] [-t <max-turns>] [-r <repo>] ["<requirements>"]

  -o, --output     output .dot path                 (default: <repo>/pipeline.dot)
  -m, --model      LLM model                        (default: $MODEL)
  -t, --max-turns  agentic turn budget              (default: $MAX_TURNS)
  -r, --repo       repo root claude is mounted on   (default: \$PWD)
  <requirements>   prompt text for the ingest agent (default: concat <repo>/docs/*.md)

env-var equivalents: OUT MODEL MAX_TURNS REPO REQUIREMENTS

after success, run kilroy-s3-run.sh to execute the pipeline (with CXDB).
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    -o|--output)    OUT="$2"; shift 2 ;;
    -m|--model)     MODEL="$2"; shift 2 ;;
    -t|--max-turns) MAX_TURNS="$2"; shift 2 ;;
    -r|--repo)      REPO="$2"; shift 2 ;;
    -h|--help)      usage; exit 0 ;;
    --)             shift; break ;;
    -*)             echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
    *)              break ;;
  esac
done

# Resolve OUT default after REPO is finalized so it follows --repo.
OUT="${OUT:-$REPO/pipeline.dot}"

if [ -n "${REQUIREMENTS:-}" ]; then
  :
elif [ $# -ge 1 ] && [ -n "$1" ]; then
  REQUIREMENTS="$1"
else
  if ! REQUIREMENTS="$(build_default_req "$REPO")"; then
    exit 1
  fi
fi

if ! git -C "$REPO" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "kilroy-s1-ingest.sh: $REPO is not inside a git work tree" >&2; exit 1
fi
if ! git -C "$REPO" rev-parse HEAD >/dev/null 2>&1; then
  echo "kilroy-s1-ingest.sh: $REPO has no commits yet (kilroy needs at least one)" >&2; exit 1
fi

cat <<INFO
→ kilroy attractor ingest
  repo:       $REPO
  output:     $OUT
  model:      $MODEL
  max-turns:  $MAX_TURNS
INFO

cd "$REPO"

# Pass requirements via a temp file so we don't hit MAX_ARG_STRLEN (~128KB)
# when the default builder concatenates a big docs/ tree. We deliberately do
# NOT exec, so the EXIT trap can clean up the temp file after kilroy returns.
REQ_FILE="$(mktemp -t kilroy-req.XXXXXX)"
trap 'rm -f "$REQ_FILE"' EXIT
printf '%s' "$REQUIREMENTS" > "$REQ_FILE"

kilroy attractor ingest \
  -o "$OUT" \
  --model "$MODEL" \
  --max-turns "$MAX_TURNS" \
  --repo "$REPO" \
  --requirements-file "$REQ_FILE"
