#!/usr/bin/env bash
set -euo pipefail

SERVICE_USER="gitmirror"
SERVICE_GROUP="gitmirror"
BIN_PATH="/usr/local/bin/gitmirror"
CONFIG_DIR="/etc/gitmirror"
STATE_DIR="/var/lib/gitmirror"
UNIT_PATH="/etc/systemd/system/gitmirror.service"
PURGE=0

usage() {
  cat <<'EOF'
Usage: sudo ./scripts/uninstall.sh [--purge]

Removes the gitmirror binary and systemd unit.
By default, configuration, persistent state, SSH credentials, and the service
account are preserved. --purge removes those too.
EOF
}

for arg in "$@"; do
  case "$arg" in
    --purge) PURGE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ ${EUID} -ne 0 ]]; then
  echo "uninstall.sh must run as root (try: sudo ./scripts/uninstall.sh)" >&2
  exit 1
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now gitmirror.service >/dev/null 2>&1 || true
fi

rm -f -- "$UNIT_PATH" "$BIN_PATH"
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload
  systemctl reset-failed gitmirror.service >/dev/null 2>&1 || true
fi

if [[ $PURGE -eq 1 ]]; then
  rm -rf -- "$CONFIG_DIR" "$STATE_DIR"
  if command -v userdel >/dev/null 2>&1 && getent passwd "$SERVICE_USER" >/dev/null 2>&1; then
    userdel "$SERVICE_USER" || true
  fi
  if command -v groupdel >/dev/null 2>&1 && getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
    groupdel "$SERVICE_GROUP" || true
  fi
  echo "gitmirror uninstalled and persistent data purged"
else
  echo "gitmirror uninstalled"
  echo "preserved config: $CONFIG_DIR"
  echo "preserved state:  $STATE_DIR"
  echo "run again with --purge to remove preserved data and the service account"
fi
