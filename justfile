set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# Show available recipes.
default:
  @just --list

# Format all Go source files.
fmt:
  gofmt -w .

# Run formatting, vet, and tests.
verify:
  test -z "$(gofmt -l .)"
  go vet ./...
  go test ./...
  bash -n scripts/install.sh scripts/uninstall.sh
  systemd-analyze verify deploy/systemd/gitmirror.service

# Build the production binary into ./bin/gitmirror.
build:
  mkdir -p bin
  go build -trimpath -ldflags='-s -w' -o bin/gitmirror ./cmd/gitmirror

# Install/update the hardened systemd deployment and start it when configured.
install:
  sudo bash scripts/install.sh

# Install/update files without starting the service.
install-no-start:
  sudo bash scripts/install.sh --no-start

# Remove binary/unit while preserving config, state, credentials, and service user.
uninstall:
  sudo bash scripts/uninstall.sh

# Remove gitmirror including config, state, credentials, and service account.
purge:
  @echo 'WARNING: this permanently removes /etc/gitmirror and /var/lib/gitmirror.'
  sudo bash scripts/uninstall.sh --purge

# Show the systemd service status.
status:
  systemctl status gitmirror.service

# Follow service logs.
logs:
  journalctl -u gitmirror.service -f

# Restart the installed service.
restart:
  sudo systemctl restart gitmirror.service

# Validate the installed unit and show systemd's security assessment.
security:
  systemd-analyze verify deploy/systemd/gitmirror.service
  systemd-analyze security gitmirror.service
