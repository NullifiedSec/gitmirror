#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_PATH="${GITMIRROR_CONFIG:-$ROOT_DIR/gitmirror.toml}"
OUTPUT_ROOT="${GITMIRROR_BACKUP_DIR:-$ROOT_DIR/backups}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
SNAPSHOT_DIR="$OUTPUT_ROOT/$TIMESTAMP"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

usage() {
  cat <<'EOF'
Usage: scripts/justbackup.sh [--config PATH] [--output DIR]

Creates one portable Git bundle per repository tracked by gitmirror and writes
all bundles into a timestamped snapshot directory. Repository credentials are
used only by Git and are not written into the manifest.

Environment overrides:
  GITMIRROR_CONFIG      config path (default: ./gitmirror.toml)
  GITMIRROR_BACKUP_DIR backup root (default: ./backups)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config)
      [[ $# -ge 2 ]] || { echo "--config requires a path" >&2; exit 2; }
      CONFIG_PATH="$2"
      shift 2
      ;;
    --output)
      [[ $# -ge 2 ]] || { echo "--output requires a directory" >&2; exit 2; }
      OUTPUT_ROOT="$2"
      SNAPSHOT_DIR="$OUTPUT_ROOT/$TIMESTAMP"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

for cmd in git python3 date mktemp; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "required command not found: $cmd" >&2; exit 1; }
done

[[ -r "$CONFIG_PATH" ]] || { echo "config is not readable: $CONFIG_PATH" >&2; exit 1; }

mkdir -p "$SNAPSHOT_DIR"
chmod 0700 "$SNAPSHOT_DIR"
MANIFEST="$SNAPSHOT_DIR/manifest.tsv"
printf 'provider\tfull_name\tbundle\tcreated_at_utc\n' >"$MANIFEST"

mapfile -t REPOS < <(python3 - "$CONFIG_PATH" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
raw = path.read_bytes()

if path.suffix.lower() == ".toml":
    try:
        import tomllib
    except ModuleNotFoundError:
        raise SystemExit("Python 3.11+ is required for TOML backup parsing")
    cfg = tomllib.loads(raw.decode())
elif path.suffix.lower() == ".json":
    cfg = json.loads(raw)
else:
    raise SystemExit(f"unsupported config extension: {path.suffix}")

seen = set()
for pair in cfg.get("pairs", []):
    for side in ("left", "right"):
        repo = pair.get(side) or {}
        provider = str(repo.get("provider") or "github").strip().lower()
        full_name = str(repo.get("full_name") or "").strip()
        url = str(repo.get("url") or "").strip()
        if not full_name or not url:
            continue
        key = (provider, full_name.lower())
        if key in seen:
            continue
        seen.add(key)
        print("\t".join((provider, full_name, url)))
PY
)

if [[ ${#REPOS[@]} -eq 0 ]]; then
  echo "no repositories found in $CONFIG_PATH" >&2
  exit 1
fi

sanitize() {
  local value="$1"
  value="${value//\//__}"
  value="$(printf '%s' "$value" | tr -cs 'A-Za-z0-9._-' '_')"
  printf '%s' "${value##_}"
}

for record in "${REPOS[@]}"; do
  IFS=$'\t' read -r provider full_name url <<<"$record"

  safe_provider="$(sanitize "$provider")"
  safe_name="$(sanitize "$full_name")"
  bundle_name="${safe_provider}__${safe_name}.bundle"
  mirror_dir="$WORK_DIR/${safe_provider}__${safe_name}.git"

  echo "backing up ${provider}:${full_name}"
  git clone --mirror --quiet "$url" "$mirror_dir"
  git -C "$mirror_dir" bundle create "$SNAPSHOT_DIR/$bundle_name" --all
  git bundle verify "$SNAPSHOT_DIR/$bundle_name" >/dev/null

  printf '%s\t%s\t%s\t%s\n' \
    "$provider" "$full_name" "$bundle_name" "$TIMESTAMP" >>"$MANIFEST"
done

chmod 0600 "$SNAPSHOT_DIR"/*.bundle "$MANIFEST"

echo "backup complete: $SNAPSHOT_DIR"
echo "manifest: $MANIFEST"
