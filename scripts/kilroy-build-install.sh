#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-build-install.sh
#
# Build kilroy from /home/travis/projects/kilroy-latest and install the
# resulting binary to /usr/local/bin/kilroy.
#
# The source tree is expected to be the x85446/kilroy fork (checked out on
# the bug-89-fix-with-flag branch or any descendant) so that the
# --no-stage-archive-stacking flag is available — kilroy-s3-run.sh and
# kilroy-s4-resume.sh depend on it. The build will fail loudly here if the
# flag is not in the binary's usage output.

set -euo pipefail

# go is at /usr/local/go/bin/go on this host (system-wide install, not on
# the default non-login PATH). Ensure it is findable regardless of how the
# script was invoked.
case ":$PATH:" in *":/usr/local/go/bin:"*) ;; *) export PATH="/usr/local/go/bin:$PATH" ;; esac

SRC=${SRC:-/home/travis/projects/kilroy-latest}
BIN_DST=${BIN_DST:-/usr/local/bin/kilroy}

[ -d "$SRC" ] || { echo "kilroy source not found at $SRC" >&2; exit 1; }
[ -d "$SRC/.git" ] || { echo "$SRC is not a git checkout" >&2; exit 1; }

cd "$SRC"

echo "→ building kilroy from $SRC ($(git rev-parse --short HEAD) on $(git rev-parse --abbrev-ref HEAD))"
go build -o /tmp/kilroy.new ./cmd/kilroy

echo "→ verifying --no-stage-archive-stacking flag is present"
# kilroy prints usage on bare invocation (no --help subcommand). Both
# `attractor run` and `attractor resume` lines should mention the flag.
USAGE=$(/tmp/kilroy.new attractor 2>&1 || true)
if ! grep -q -- "attractor run.*--no-stage-archive-stacking" <<< "$USAGE"; then
  echo "build verification failed: --no-stage-archive-stacking flag not in attractor run usage" >&2
  echo "is the source on the bug-89-fix-with-flag branch (or a descendant)?" >&2
  rm -f /tmp/kilroy.new
  exit 1
fi
if ! grep -q -- "attractor resume.*--no-stage-archive-stacking" <<< "$USAGE"; then
  echo "build verification failed: --no-stage-archive-stacking flag not in attractor resume usage" >&2
  rm -f /tmp/kilroy.new
  exit 1
fi
echo "  ok: flag present on both run and resume"

echo "→ installing $BIN_DST (sudo)"
sudo install -m 0755 -o root -g root /tmp/kilroy.new "$BIN_DST"
rm -f /tmp/kilroy.new

echo "→ done. installed:"
"$BIN_DST" --version 2>&1 | head -3 || true
