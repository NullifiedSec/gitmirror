# gitmirror

Bidirectional Git repository synchronization bridge.

The current implementation accepts authenticated GitHub and Gitea webhooks, durably queues deliveries, and safely reconciles branch refs between configured repository pairs. A pair can be GitHub ↔ GitHub, GitHub ↔ Gitea, or Gitea ↔ Gitea.

## Current scope

- GitHub and Gitea webhook ingestion
- HMAC-SHA256 verification of `X-Hub-Signature-256`
- provider-scoped delivery idempotency
- durable file-backed queue with crash recovery and bounded retries
- bidirectional branch creation and fast-forward propagation
- optional per-side branch polling
- stale push events become no-ops
- divergent or suspicious updates are diverted to fresh `gitmirror/quarantine/...` branches instead of rewriting the requested target branch
- optional human-in-the-loop approval gates for selected destination branches
- branch deletion only propagates when the destination still points at the expected pre-delete SHA
- Git remote URLs are redacted from command errors
- non-push events are accepted and safely ignored for now

Tags, pull requests, issues, comments, reviews, releases, richer conflict workflows, and more-than-two-way mirror groups are planned follow-up layers. Secrets, deploy keys, Actions secrets, and other credentials should never be mirrored as repository metadata.

## Requirements

- Go 1.26+
- Git available in `PATH`
- credentials that allow fetch/push to both repositories
- HTTPS endpoint reachable by the configured providers when webhooks are used

SSH remotes are recommended for the initial deployment because they avoid embedding credentials in repository URLs. If HTTPS URLs contain credentials, gitmirror redacts configured remote URLs from captured Git stderr, but external process inspection and local Git config are separate concerns.

## Configuration

TOML is the preferred configuration format. Copy `gitmirror.example.toml` to `gitmirror.toml`:

```toml
listen = ":8080"
data_dir = ".gitmirror"

[[pairs]]
name = "github-gitea-example"

[pairs.left]
provider = "github"
full_name = "upstream-owner/private-repo"
url = "git@github.com:upstream-owner/private-repo.git"
polling = true
polling_frequency = 120
human_in_loop_branches = ["main"]

[pairs.right]
provider = "gitea"
full_name = "mirror-owner/private-repo"
url = "git@git.example.com:mirror-owner/private-repo.git"
```

`polling` and `polling_frequency` are configured independently on each side. When polling is enabled without an explicit frequency it defaults to 120 seconds. Polling intervals below 10 seconds are rejected.

`human_in_loop_branches` is also configured per side. It protects that repository when it is the **destination** of a synchronization operation. For example, putting `human_in_loop_branches = ["main"]` on the left repository means updates flowing from right → left may not change left/main until a human explicitly approves the exact transition.

When a protected update is ready, gitmirror writes a durable approval record under `data_dir/approvals/pending/` and logs its ID. Approve it with:

```bash
gitmirror -config /etc/gitmirror/gitmirror.toml -approve <approval-id>
```

Approval is bound to the exact source SHA and the exact target SHA observed when the request was created. Before pushing, gitmirror re-reads both remotes. If either side moved, the approval expires and nothing is pushed.

The nested TOML tables map naturally to the two repositories in a pair, and `[[pairs]]` can be repeated for additional mirror pairs.

JSON configuration remains supported for compatibility with existing installations. `gitmirror.example.json` is kept as a legacy example, and `-config` accepts either `.toml` or `.json` files. Files with unsupported extensions are rejected rather than guessed.

Supported providers are currently `github` and `gitea`. Omitting `provider` preserves the original behavior and defaults that repository to `github`.

Repository identity is provider-aware, so `owner/repo` on GitHub and `owner/repo` on Gitea are treated as different repositories. A repository may appear in only one pair in this first version. The daemon is also currently designed as a single active process for a given `data_dir`.

## Run

Set webhook secrets for the providers used by sides that rely on webhooks:

```bash
export GITMIRROR_GITHUB_WEBHOOK_SECRET='replace-me'
export GITMIRROR_GITEA_WEBHOOK_SECRET='replace-me-too'
go run ./cmd/gitmirror -config gitmirror.toml
```

A provider for which every configured side uses polling does not require a webhook secret.

`gitmirror.toml` is now the default config path, so `-config` can be omitted when using that filename.

`GITMIRROR_WEBHOOK_SECRET` remains supported as a legacy alias for the GitHub webhook secret.

Endpoints:

- `POST /webhooks/github` — GitHub webhook receiver
- `POST /webhooks/gitea` — Gitea webhook receiver
- `GET /healthz` — liveness endpoint

Configure push webhooks only for sides that should use webhook delivery. Each provider should use its corresponding endpoint and matching secret.

## Quick production install

The repository includes conservative install/uninstall scripts for the hardened systemd deployment:

```bash
sudo bash scripts/install.sh
```

The installer builds the current checkout, creates the dedicated service account and directories if needed, installs the binary/unit, and preserves any existing config, secrets, state, and SSH credentials. A fresh install does not start the service until the required webhook secrets have been configured in `/etc/gitmirror/gitmirror.env`.

Uninstall the executable/unit while preserving persistent data:

```bash
sudo bash scripts/uninstall.sh
```

Explicitly purge configuration, mirror state, SSH credentials, and the service account:

```bash
sudo bash scripts/uninstall.sh --purge
```

`--purge` is destructive by design and is never the default.

## just commands

If [`just`](https://github.com/casey/just) is installed, the root `justfile` provides the common workflows:

```text
just build
just verify
just install
just install-no-start
just status
just logs
just restart
just security
just uninstall
just purge
```

Running `just` with no recipe lists the available commands.

## systemd deployment

A hardened production unit is provided at `deploy/systemd/gitmirror.service` together with a secrets-file example and installation guide.

The recommended layout is:

- `/usr/local/bin/gitmirror` — root-owned executable
- `/etc/gitmirror/gitmirror.toml` — root-owned configuration
- `/etc/gitmirror/gitmirror.env` — root-owned webhook secrets
- `/var/lib/gitmirror` — persistent queue, polling snapshots, HIL approvals, bare repositories, and service-account SSH state

The supplied unit runs as a dedicated unprivileged `gitmirror` user and enables systemd sandboxing including `NoNewPrivileges`, `ProtectSystem=strict`, private temporary/device namespaces, kernel and control-group protections, an empty capability set, and a restrictive umask.

See `deploy/systemd/README.md` for the deployment layout and SSH credential guidance.

## Conflict and push safety behavior

For a branch update, gitmirror fetches both tips and checks commit ancestry:

- destination missing: eligible for creation
- same SHA: no-op
- destination is behind source: eligible for fast-forward
- source is behind destination: treat the source event as stale and no-op
- neither is an ancestor: do **not** touch the requested destination branch; preserve the source tip on a fresh `gitmirror/quarantine/...` branch instead

Before an automatic branch update, gitmirror runs `git push --dry-run --porcelain` and accepts only the machine-readable statuses expected for a normal fast-forward, new branch, or already-up-to-date ref. A failed dry run, stderr output, rejected status, forced-update status, deletion status, or otherwise unexpected result prevents the requested branch update and diverts the source tip to a quarantine branch.

If the real push itself fails after a clean preflight, gitmirror does not retry that target ref as though nothing happened. It attempts to preserve the source tip on a fresh quarantine branch instead.

No force push is used for normal synchronization or quarantine branches.

Human-in-the-loop branches add another barrier: even a perfectly clean fast-forward is held until the exact source/target SHA transition is explicitly approved.

Branch deletions remain conservative: the source branch must actually be absent and the target must still point at the exact expected pre-delete SHA. Deletion push preflight must also be clean.

Failed operations that cannot be safely quarantined or completed are retried up to five times and then moved to the queue's `failed` directory for inspection.

## Security notes

- webhook payloads are verified before they are persisted
- body size is capped at 10 MiB
- branch names are validated with Git before use
- Git is invoked without a shell
- force pushes are not used for synchronization
- divergent/suspicious commits are preserved on new quarantine branches instead of overwriting the requested branch
- HIL approvals are bound to exact before/after SHAs and are revalidated immediately before push
- approval IDs reject path-like input
- deletion requires an exact expected SHA match
- remote URLs are treated as sensitive in captured Git output
- the production systemd unit is designed to run without Linux capabilities and with a read-only system view
- uninstall preserves persistent config/state unless `--purge` is explicitly requested

For repositories that must be treated as authoritative, application-level safeguards should be combined with credentials that are unable to push to that authoritative repository. Credential-level write denial remains the strongest final barrier against an application bug.

Do not place webhook secrets or repository credentials in this repository.
