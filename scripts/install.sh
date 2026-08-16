#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICE_USER="gitmirror"
SERVICE_GROUP="gitmirror"
BIN_PATH="/usr/local/bin/gitmirror"
CONFIG_DIR="/etc/gitmirror"
CONFIG_PATH="${CONFIG_DIR}/gitmirror.toml"
ENV_PATH="${CONFIG_DIR}/gitmirror.env"
STATE_DIR="/var/lib/gitmirror"
UNIT_PATH="/etc/systemd/system/gitmirror.service"
START_SERVICE=1

usage() {
  cat <<'EOF'
Usage: sudo bash scripts/install.sh [--no-start]

Builds gitmirror and installs the hardened systemd deployment.
Existing config, environment, state, and SSH credentials are preserved.
EOF
}

for arg in "$@"; do
  case "$arg" in
    --no-start) START_SERVICE=0 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ ${EUID} -ne 0 ]]; then
  echo "install.sh must run as root (try: sudo bash scripts/install.sh)" >&2
  exit 1
fi

for cmd in go git install systemctl useradd groupadd getent sed mktemp grep; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "required command not found: $cmd" >&2; exit 1; }
done

if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
  groupadd --system "$SERVICE_GROUP"
fi
if ! getent passwd "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system \
    --gid "$SERVICE_GROUP" \
    --home-dir "$STATE_DIR" \
    --create-home \
    --shell /usr/sbin/nologin \
    "$SERVICE_USER"
fi

install -d -o root -g "$SERVICE_GROUP" -m 0750 "$CONFIG_DIR"
install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$STATE_DIR"

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
(
  cd "$ROOT_DIR"
  go build -trimpath -ldflags='-s -w' -o "$BUILD_DIR/gitmirror" ./cmd/gitmirror
)
install -o root -g root -m 0755 "$BUILD_DIR/gitmirror" "$BIN_PATH"

if [[ ! -e "$CONFIG_PATH" ]]; then
  sed 's#data_dir = ".gitmirror"#data_dir = "/var/lib/gitmirror"#' \
    "$ROOT_DIR/gitmirror.example.toml" >"$BUILD_DIR/gitmirror.toml"
  install -o root -g "$SERVICE_GROUP" -m 0640 "$BUILD_DIR/gitmirror.toml" "$CONFIG_PATH"
  echo "created $CONFIG_PATH from the example; edit repository endpoints before production use"
else
  echo "preserving existing $CONFIG_PATH"
fi

if [[ ! -e "$ENV_PATH" ]]; then
  install -o root -g "$SERVICE_GROUP" -m 0640 \
    "$ROOT_DIR/deploy/systemd/gitmirror.env.example" "$ENV_PATH"
  echo "created $ENV_PATH; add the webhook secrets required by your configured providers"
else
  echo "preserving existing $ENV_PATH"
fi

install -o root -g root -m 0644 "$ROOT_DIR/deploy/systemd/gitmirror.service" "$UNIT_PATH"
systemctl daemon-reload
systemctl enable gitmirror.service >/dev/null

if [[ $START_SERVICE -eq 0 ]]; then
  echo "gitmirror installed; service start skipped (--no-start)"
elif grep -Eq '^GITMIRROR_(GITHUB_WEBHOOK_SECRET|GITEA_WEBHOOK_SECRET|WEBHOOK_SECRET)=[^[:space:]]+$' "$ENV_PATH"; then
  systemctl restart gitmirror.service
  echo "gitmirror installed and started"
else
  echo "gitmirror installed but not started: configure webhook secrets in $ENV_PATH first"
fi

echo "config: $CONFIG_PATH"
echo "state:  $STATE_DIR"
echo "unit:   $UNIT_PATH"
