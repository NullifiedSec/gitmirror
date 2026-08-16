# gitmirror

Bidirectional Git/GitHub synchronization bridge.

The current implementation is an intentionally small first slice: it accepts authenticated GitHub webhooks, durably queues deliveries, and safely reconciles branch refs between configured repository pairs.

## Current scope

- HMAC-SHA256 verification of `X-Hub-Signature-256`
- delivery idempotency through `X-GitHub-Delivery`
- durable file-backed queue with crash recovery and bounded retries
- bidirectional branch creation and fast-forward propagation
- stale push events become no-ops
- branch divergence is refused rather than force-pushed
- branch deletion only propagates when the destination still points at the expected pre-delete SHA
- Git remote URLs are redacted from command errors
- non-push GitHub events are accepted and safely ignored for now

Tags, pull requests, issues, comments, reviews, releases, and richer conflict workflows are planned follow-up layers. Secrets, deploy keys, Actions secrets, and other credentials should never be mirrored as repository metadata.

## Requirements

- Go 1.26+
- Git available in `PATH`
- credentials that allow fetch/push to both repositories
- HTTPS endpoint reachable by GitHub for `/webhooks/github`

SSH remotes are recommended for the initial deployment because they avoid embedding credentials in repository URLs. If HTTPS URLs contain credentials, gitmirror redacts configured remote URLs from captured Git stderr, but external process inspection and local Git config are separate concerns.

## Configuration

Copy `gitmirror.example.json` to `gitmirror.json` and configure each repository pair:

```json
{
  "listen": ":8080",
  "data_dir": ".gitmirror",
  "pairs": [
    {
      "name": "example",
      "left": {
        "full_name": "upstream-owner/private-repo",
        "url": "git@github.com:upstream-owner/private-repo.git"
      },
      "right": {
        "full_name": "your-org/private-repo",
        "url": "git@github.com:your-org/private-repo.git"
      }
    }
  ]
}
```

A repository may appear in only one pair in this first version. The daemon is also currently designed as a single active process for a given `data_dir`.

## Run

Set the same webhook secret in GitHub and the daemon:

```bash
export GITMIRROR_WEBHOOK_SECRET='replace-me'
go run ./cmd/gitmirror -config gitmirror.json
```

Endpoints:

- `POST /webhooks/github` — GitHub webhook receiver
- `GET /healthz` — liveness endpoint

Configure a GitHub repository webhook on both sides with the push event enabled and point both repositories at the same receiver URL.

## Conflict behavior

For a branch update, gitmirror fetches both tips and checks commit ancestry:

- destination missing: create it
- same SHA: no-op
- destination is behind source: fast-forward destination
- source is behind destination: treat the source webhook as stale and no-op
- neither is an ancestor: fail the delivery and do not rewrite either branch

Failed deliveries are retried up to five times and then moved to the queue's `failed` directory for inspection.

## Security notes

- webhook payloads are verified before they are persisted
- body size is capped at 10 MiB
- branch names are validated with Git before use
- Git is invoked without a shell
- force pushes are not used
- deletion requires an exact expected SHA match
- remote URLs are treated as sensitive in captured Git output

Do not place webhook secrets or repository credentials in this repository.
