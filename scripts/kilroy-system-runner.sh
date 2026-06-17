#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-system-runner.sh
#
# Invoked by /etc/systemd/system/kilroy-run.service. Reads which kilroy
# wrapper to invoke from env vars (set in /etc/kilroy-run.env) and execs
# it. Defaults to s4 resume.

set -euo pipefail

KILROY_MODE=${KILROY_MODE:-resume}
KILROY_ARGS=${KILROY_ARGS:-}

case "$KILROY_MODE" in
  resume) exec /opt/darkfactory/bin/kilroy-s4-resume.sh $KILROY_ARGS ;;
  run)    exec /opt/darkfactory/bin/kilroy-s3-run.sh    $KILROY_ARGS ;;
  *)      echo "kilroy-system-runner: unknown KILROY_MODE=$KILROY_MODE" >&2; exit 2 ;;
esac
