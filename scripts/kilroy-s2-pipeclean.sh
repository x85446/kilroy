#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-s2-pipeclean.sh
#
# Rewrite a kilroy pipeline.dot so every llm_provider is anthropic. Maps each
# foreign provider/model to a comparable anthropic model. The local
# cliproxyapi gateway only routes Claude — any non-anthropic node would
# fail at run time.
#
# Usage:
#   kilroy-s2-pipeclean.sh                     # in-place rewrite of ./pipeline.dot
#   kilroy-s2-pipeclean.sh <input.dot>         # in-place rewrite of input
#   kilroy-s2-pipeclean.sh <input> <output>    # write to output (input untouched)
#
# Env-var overrides:
#   DEFAULT_MODEL  default anthropic model        (default: claude-sonnet-4.6)
#   STRONG_MODEL   anthropic model for "strong" tier (default: claude-opus-4.7)
#   NO_VALIDATE=1  skip post-rewrite kilroy validate

set -euo pipefail

INPUT="${1:-$PWD/pipeline.dot}"
[ -f "$INPUT" ] || { echo "kilroy-s2-pipeclean.sh: $INPUT not found" >&2; exit 1; }

OUT="${2:-$INPUT}"
DEFAULT_MODEL="${DEFAULT_MODEL:-claude-sonnet-4.6}"
STRONG_MODEL="${STRONG_MODEL:-claude-opus-4.7}"

python3 - "$INPUT" "$OUT" "$DEFAULT_MODEL" "$STRONG_MODEL" <<'PY'
import re, shutil, sys

src, dst, default_model, strong_model = sys.argv[1:5]

with open(src) as f:
    text = f.read()

changes = []
strong_keywords = ("pro", "opus", "max", "ultra", "thinking", "reasoning", "-r1", "deep")

def pick(model_name: str) -> str:
    name = model_name.lower()
    return strong_model if any(k in name for k in strong_keywords) else default_model

def repl_model_first(m):
    model, provider = m.group(1), m.group(2)
    if provider.lower() == "anthropic":
        return m.group(0)
    new_model = pick(model)
    changes.append(f"  {provider}/{model} -> anthropic/{new_model}")
    return f"llm_model: {new_model}; llm_provider: anthropic;"

def repl_provider_first(m):
    provider, model = m.group(1), m.group(2)
    if provider.lower() == "anthropic":
        return m.group(0)
    new_model = pick(model)
    changes.append(f"  {provider}/{model} -> anthropic/{new_model}")
    return f"llm_provider: anthropic; llm_model: {new_model};"

p1 = re.compile(r"llm_model:\s*([A-Za-z0-9._:\-]+)\s*;\s*llm_provider:\s*([A-Za-z0-9_-]+)\s*;")
p2 = re.compile(r"llm_provider:\s*([A-Za-z0-9_-]+)\s*;\s*llm_model:\s*([A-Za-z0-9._:\-]+)\s*;")
text2 = p1.sub(repl_model_first, text)
text2 = p2.sub(repl_provider_first, text2)

if dst == src and text2 != text:
    shutil.copy2(src, src + ".bak")
with open(dst, "w") as f:
    f.write(text2)

if changes:
    print("rewrote {} model assignment(s):".format(len(changes)))
    for c in changes:
        print(c)
else:
    print("no non-anthropic llm_provider entries found")
PY

if [ "$OUT" = "$INPUT" ] && [ -f "$INPUT.bak" ]; then
  echo "backup: $INPUT.bak"
fi

if [ "${NO_VALIDATE:-0}" != "1" ]; then
  echo
  echo "→ kilroy attractor validate --graph $OUT"
  kilroy attractor validate --graph "$OUT"
fi
