#!/usr/bin/env bash
# /opt/darkfactory/scripts/kilroy-launch-detached.sh
#
# Front-end for /etc/systemd/system/kilroy-run.service — a SYSTEM service
# that runs kilroy as user travis. Lives in /system.slice so it is
# completely decoupled from ssh sessions and from user@<uid>.service
# lifecycle (linger has historically not actually kept user@1001.service
# alive on this host, so user-level transient services died).
#
# Usage:
#   kilroy-launch-detached.sh resume                # exec kilroy-s4-resume.sh
#   kilroy-launch-detached.sh resume "<args>"       # resume specific run
#   kilroy-launch-detached.sh run "<args>"          # exec kilroy-s3-run.sh
#   kilroy-launch-detached.sh status                # systemctl status
#   kilroy-launch-detached.sh stop                  # systemctl stop
#   kilroy-launch-detached.sh logs                  # journalctl -f
#
# After launch, monitor with:
#   journalctl -u kilroy-run -f
#   systemctl status kilroy-run

set -euo pipefail

UNIT=kilroy-run.service
ENV_FILE=/etc/kilroy-run.env

cmd=${1:-status}
shift || true

case "$cmd" in
  resume|run)
    if systemctl is-active --quiet "$UNIT"; then
      echo "$UNIT is already active; stop it first with: $0 stop" >&2
      exit 1
    fi
    sudo tee "$ENV_FILE" >/dev/null <<EOF
KILROY_MODE=$cmd
KILROY_ARGS=$*
EOF
    echo "→ wrote $ENV_FILE: KILROY_MODE=$cmd KILROY_ARGS=\"$*\""
    sudo systemctl reset-failed "$UNIT" 2>/dev/null || true
    sudo systemctl start "$UNIT"
    sleep 1
    sudo systemctl status "$UNIT" --no-pager 2>&1 | head -15
    echo
    echo "follow logs with:   $0 logs"
    echo "stop with:          $0 stop"
    ;;
  status)
    sudo systemctl status "$UNIT" --no-pager 2>&1 | head -25
    ;;
  stop)
    sudo systemctl stop "$UNIT" 2>&1 || true
    echo "stopped $UNIT (if it was running)"
    ;;
  logs)
    exec sudo journalctl -u "$UNIT" -f --no-pager
    ;;
  -h|--help|help)
    sed -n "2,20p" "$0" | sed "s|^# *||;s|^#$||"
    ;;
  *)
    echo "unknown subcommand: $cmd" >&2
    sed -n "2,20p" "$0" | sed "s|^# *||;s|^#$||" >&2
    exit 2
    ;;
esac
