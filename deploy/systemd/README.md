# systemd deployment

This deployment model runs gitmirror as a dedicated unprivileged `gitmirror` account with a read-only system view and a single persistent state directory at `/var/lib/gitmirror`.

## Install layout

- binary: `/usr/local/bin/gitmirror`
- config: `/etc/gitmirror/gitmirror.toml`
- secrets: `/etc/gitmirror/gitmirror.env`
- persistent state, queue, bare repositories, and SSH home: `/var/lib/gitmirror`
- unit: `/etc/systemd/system/gitmirror.service`

The service intentionally uses a static account rather than `DynamicUser=` because Git/SSH authentication commonly needs durable SSH keys, `known_hosts`, and credential-helper state.

## Quick install

From the repository root:

```bash
sudo bash scripts/install.sh
```

The installer:

- builds the current checkout with `-trimpath` and stripped debug tables
- creates the `gitmirror` system user/group if missing
- creates `/etc/gitmirror` and `/var/lib/gitmirror` with restrictive ownership/modes
- installs `/usr/local/bin/gitmirror` and the hardened unit
- creates config/secrets from examples only when those files do not already exist
- preserves existing configuration, state, SSH credentials, and service identity
- enables the service
- starts/restarts it only when a non-empty webhook secret has been configured

Use `--no-start` when staging a deployment:

```bash
sudo bash scripts/install.sh --no-start
```

If `just` is installed, the equivalent commands are:

```bash
just install
just install-no-start
```

## Configure

For the systemd deployment, `/etc/gitmirror/gitmirror.toml` should use:

```toml
data_dir = "/var/lib/gitmirror"
```

The installer writes that absolute state path into a fresh config automatically.

Set only the webhook secrets used by the configured providers in `/etc/gitmirror/gitmirror.env`. The checked-in example keeps all secret assignments commented so a fresh install cannot accidentally start with placeholder credentials.

If SSH remotes are used, provision credentials under the service account's home:

```bash
sudo install -d -o gitmirror -g gitmirror -m 0700 /var/lib/gitmirror/.ssh
sudo install -o gitmirror -g gitmirror -m 0600 /path/to/private-key /var/lib/gitmirror/.ssh/id_ed25519
sudo install -o gitmirror -g gitmirror -m 0644 /path/to/known_hosts /var/lib/gitmirror/.ssh/known_hosts
```

Do not disable SSH host-key checking. Populate `known_hosts` from a trusted source and verify host fingerprints before deployment.

After editing configuration/secrets:

```bash
sudo systemctl restart gitmirror
systemctl status gitmirror
journalctl -u gitmirror -f
```

or:

```bash
just restart
just status
just logs
```

## Uninstall

Normal uninstall removes the service and binary while preserving config, persistent mirror state, SSH credentials, and the service account:

```bash
sudo bash scripts/uninstall.sh
```

or:

```bash
just uninstall
```

To permanently remove everything owned by the deployment, explicitly request a purge:

```bash
sudo bash scripts/uninstall.sh --purge
```

or:

```bash
just purge
```

`--purge` removes `/etc/gitmirror`, `/var/lib/gitmirror`, and the service user/group. Treat it as destructive.

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

or:

```bash
just security
```

Some distributions or older systemd releases may not support every hardening directive in the supplied unit. If systemd rejects a directive, verify the host's systemd version before removing it; do not broadly disable sandboxing just to make the service start.
