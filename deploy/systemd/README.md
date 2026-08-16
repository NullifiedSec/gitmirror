# systemd deployment

This deployment model runs gitmirror as a dedicated unprivileged `gitmirror` account with a read-only system view and a single persistent state directory at `/var/lib/gitmirror`.

## Install layout

- binary: `/usr/local/bin/gitmirror`
- config: `/etc/gitmirror/gitmirror.toml`
- secrets: `/etc/gitmirror/gitmirror.env`
- persistent state, queue, bare repositories, and SSH home: `/var/lib/gitmirror`
- unit: `/etc/systemd/system/gitmirror.service`

The service intentionally uses a static account rather than `DynamicUser=` because Git/SSH authentication commonly needs durable SSH keys, `known_hosts`, and credential-helper state.

## Install

Build and install the binary:

```bash
go build -trimpath -ldflags='-s -w' -o gitmirror ./cmd/gitmirror
sudo install -o root -g root -m 0755 gitmirror /usr/local/bin/gitmirror
```

Create the service account and directories:

```bash
sudo useradd --system --home-dir /var/lib/gitmirror --create-home --shell /usr/sbin/nologin gitmirror
sudo install -d -o root -g gitmirror -m 0750 /etc/gitmirror
sudo install -d -o gitmirror -g gitmirror -m 0700 /var/lib/gitmirror
```

Install configuration and secrets:

```bash
sudo install -o root -g gitmirror -m 0640 gitmirror.example.toml /etc/gitmirror/gitmirror.toml
sudo install -o root -g gitmirror -m 0600 deploy/systemd/gitmirror.env.example /etc/gitmirror/gitmirror.env
```

For the systemd deployment, set this in `/etc/gitmirror/gitmirror.toml`:

```toml
data_dir = "/var/lib/gitmirror"
```

If SSH remotes are used, provision credentials under the service account's home:

```bash
sudo install -d -o gitmirror -g gitmirror -m 0700 /var/lib/gitmirror/.ssh
sudo install -o gitmirror -g gitmirror -m 0600 /path/to/private-key /var/lib/gitmirror/.ssh/id_ed25519
sudo install -o gitmirror -g gitmirror -m 0644 /path/to/known_hosts /var/lib/gitmirror/.ssh/known_hosts
```

Do not disable SSH host-key checking. Populate `known_hosts` from a trusted source and verify host fingerprints before deployment.

Install and start the unit:

```bash
sudo install -o root -g root -m 0644 deploy/systemd/gitmirror.service /etc/systemd/system/gitmirror.service
sudo systemctl daemon-reload
sudo systemctl enable --now gitmirror
```

Inspect status and logs:

```bash
systemctl status gitmirror
journalctl -u gitmirror -f
```

## Hardening

The supplied unit deliberately removes ambient Linux capabilities and enables systemd sandboxing including:

- `NoNewPrivileges=yes`
- `ProtectSystem=strict`
- `ProtectHome=yes`
- private `/tmp` and device namespaces
- kernel/control-group/proc restrictions
- executable-memory denial
- SUID/SGID and realtime restrictions
- network address families limited to Unix sockets, IPv4, and IPv6
- `UMask=0077`

`StateDirectory=gitmirror` is the daemon's writable persistent area. The configuration directory stays protected by the read-only system sandbox.

After installation, inspect the effective sandbox with:

```bash
systemd-analyze security gitmirror.service
```

Some distributions or older systemd releases may not support every hardening directive in the supplied unit. If systemd rejects a directive, verify the host's systemd version before removing it; do not broadly disable sandboxing just to make the service start.
