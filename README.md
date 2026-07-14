# ship

ship deploys a repo to one hardened Linux box. A Git branch is the environment:
`main` is Production, every other branch is a Preview, and a successful `ship`
prints the HTTPS URL to stdout so a human or agent can open it immediately.
The box handles builds, process supervision, Caddy routing, TLS, secrets,
rollback, backups, and diagnostics without becoming a platform you have to
operate. It is built for people at a terminal and for agents that need stable
commands, JSON, and plain failure text.

Data lives in a SQLite file under `/data` or behind a managed URL. The box never
runs a database server.

## Install

```bash
curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash
```

The installer downloads the release asset for your OS/CPU, verifies
`SHA256SUMS`, and writes `ship` to `~/.local/bin` unless `SHIP_INSTALL_DIR` is
set. Shell completions are static:

```bash
ship completion zsh > ~/.zsh/completions/_ship
```

Use `ship completion bash` or `ship completion fish` for other shells.

## First Run

Start with a fresh Ubuntu box, then converge it:

```bash
ship box setup 203.0.113.7
```

`box setup` creates this machine's ship identity at `~/.ssh/ship` on first use.
The member name comes from `git config user.name`, falling back to `$USER`, and
that public key is enrolled as the box's first member with the `owner` role. For
split-key or CI setups, pass `--deploy-ssh-public-key-file`; otherwise no key
flags are needed.

If the provider gave you a root password instead of installing your SSH key:

```bash
ssh-copy-id -i ~/.ssh/ship.pub root@203.0.113.7
ship box setup 203.0.113.7
```

ship never uses password auth itself; hardening disables password login after
the install.

The installer output is a host-convergence log. First contact trusts and pins
the box host key in `~/.config/ship/known_hosts`; ship never writes to
`~/.ssh/known_hosts`. A changed key is refused. If you rebuild the VPS at the
same address, rerun `ship box setup <ssh-target>` to re-establish the pin; no
manual `ssh-keygen -R` is needed.

Inside a repo:

```bash
ship init
```

`ship init` writes only `ship.toml`. It never overwrites existing files and ends with
`next: ship`. Set `box = "203.0.113.7"` in `ship.toml`; app manifests store a
host only, never `user@host`. Commit the repo, then deploy:

```bash
ship
```

Deployment progress goes to stderr:

```text
preflight 0.4s
build 6.2s
release 1.1s
probe ok
live
next: add DNS A <your-domain> → 203.0.113.7 and add it under [routes]
```

Stdout is only the URL — app-first, no env words:

```text
https://taskflow.203-0-113-7.sslip.io
```

That stdout contract is deliberate. Pipe it, paste it, or hand it to an agent.

## Daily Loop

Make a branch, ship it, review the URL:

```bash
git switch -c feature/billing
ship
```

The stdout URL is the Preview for that branch — a capability URL that
works as printed:

```text
https://taskflow-feature-billing-x7q2.203-0-113-7.sslip.io/?ship=<token>
```

Every Preview is protected by a capability URL printed by `ship`. CI and
agents can send its token as `x-ship-capability: <token>`. Production stays
public. Reprint or rotate a Preview capability with:

```bash
ship preview share
ship preview share --rotate
```

Useful commands during review:

```bash
ship preview pin feature/billing
ship logs web --tail 200
ship exec -- node scripts/check-data.js
ship rollback
```

When a change needs production-shaped data, fork Production `/data` into the
Preview:

```bash
ship data fork
ship exec -- npx drizzle-kit migrate
ship data reset
```

Run `ship data fork` from a Preview branch after that Preview exists. It copies
Production `/data` on the box, bounces the Preview, and never sends data to the
client. Production stays read-only. `ship data reset` empties that Preview's
`/data` when you are done.

Scary migration loop:

```bash
git switch -c migration/accounts-v2
ship
ship data fork
ship exec -- npm run migrate
ship
```

The Preview now has real production-shaped data, so you can verify the migration
against the Preview URL while Production is untouched. Data commands are
Preview-only, require `owner` or `shipper`, and return `approval_required` for
`agent` keys.

Merge to Production and run the same deploy command:

```bash
git switch main
git merge feature/billing
ship
```

The guardrails are hard errors with fixed text. Production does not ship a
dirty worktree:

```text
Production ship failed
production branch "main" has uncommitted changes
next: git add . && git commit -m "<message>"
```

Production does not ship behind what is already live:

```text
Production ship failed
deployed commit abc123 is not an ancestor of HEAD
next: git pull
```

Preview secrets never fall back to Production secrets:

```text
deploy is missing a required secret
missing secret api_token for Preview branch "feature/secrets"
next: ship secret set api_token [--preview|--branch <name>]
```

## Teammates

Authorize a teammate from their forge keys-URL. The bare command fetches and
shows the keys — fingerprints, source, name, and role — without writing
anything, then prints the exact confirm command:

```bash
ship box member add https://github.com/alice.keys <box> --name alice
ship box member add https://github.com/alice.keys <box> --name alice --confirm alice@sha256:...
```

A literal public key or a `.pub` file writes immediately — you supplied the
exact bytes:

```bash
ship box member add ~/.ssh/alice.pub <box> --name alice
ship box member ls <box>
```

`box member add` defaults to the `shipper` role and names are explicit —
`--name` is always required. Onboarding stays two commands: review, confirm;
then invite them to the repo and their first `ship` works.

| Role | What it can do |
| --- | --- |
| `owner` | Everything, including member management, destructive box/app operations, restore, and approvals. |
| `shipper` | The daily loop: deploy, logs, exec, rollback, secrets, previews, and data forks. No member management, Production removal, or restore. |
| `agent` | Preview deploys and reads only. No Production mutations, no secret reads, no shell, and data forks require approval. |

Remove all keys for a member by name:

```bash
ship box member rm alice <box>
```

## Failure

When a deploy fails, the old release keeps serving until the new one passes its
release command and probe. Ask the box why:

```bash
ship why
```

Human output is shaped for action:

```text
Deploy aborted for Production main at 2026-07-07T10:00:10Z.
attempted release: def456
previous release: abc123
failing step: probe
probable cause: probe returned HTTP 502 with body: upstream listened on 3000, probed 3999
stderr tail:
HTTP status 502: upstream listened on 3000, probed 3999
traffic: old release abc123 kept serving; failed probes never receive traffic with the current engine.
shipped by: Name <name@example.com> (ssh key: alice)
next: fix the process port or probe path in ship.toml, then ship
```

If `webhook` is set in `ship.toml`, ship posts failure and recovery events:

```json
{
  "app": "taskflow",
  "env": "Production main",
  "event": "deploy_aborted",
  "release": "def456",
  "summary": "Deploy aborted for Production main at release def456.",
  "why": {
    "schema_version": 1,
    "app": "taskflow",
    "env": "production",
    "outcome": "aborted_probe",
    "started_at": "2026-07-07T10:00:00Z",
    "ended_at": "2026-07-07T10:00:10Z",
    "previous_release": "abc123",
    "attempted_release": "def456",
    "failing_step": "probe",
    "stderr_tail": "HTTP status 502...",
    "identity": {
      "ssh_key_comment": "alice",
      "git_author": "Name <name@example.com>"
    },
    "probe": {
      "status": 502,
      "body_snippet": "upstream listened on 3000, probed 3999"
    }
  },
  "remediation": {
    "command": "ship",
    "journal": "<same journal entry>"
  },
  "ts": "2026-07-07T10:00:10Z"
}
```

Box health is inspectable:

```bash
ship box doctor 203.0.113.7 --json
```

Doctor JSON is a list of checks:

```json
[
  {
    "id": "disk_space",
    "status": "ok",
    "evidence": "used=10%",
    "remediation": "ship box doctor 203.0.113.7"
  }
]
```

Keep the box helper at the same version as your client:

```bash
ship box status 203.0.113.7
ship box status 203.0.113.7 --json
ship box update 203.0.113.7
```

Boxes set up before v0.4.0 need one `ship box setup <ssh-target>` before they
can use `status` or `update`.

App `webhook` URLs receive deploy and Preview reaper events. Put disk, doctor,
and approval alerts on the one box pager URL:

```bash
ship box webhook 203.0.113.7 https://ntfy.sh/ship-box
ship box webhook 203.0.113.7
ship box webhook 203.0.113.7 --rm
```

## Agents

Agents should start with:

```bash
ship docs
```

For one verb:

```bash
ship help secret set --json
```

The agent contract covers the mental model, every public verb, JSON schemas,
the output contract, deploy journals, webhook payloads, and the error catalogue.
Operational reads expose `--json`, and `ship --json` gives agents a structured
deploy result.

Out-of-role actions do not silently run. They return `approval_required` with
the exact command an approver should paste — from any machine, no checkout
needed:

```bash
ship box approval ls 203.0.113.7
ship box approval grant abc123xy 203.0.113.7
```

`ship box approval ls` lists pending requests. `ship box approval grant <id> <box>`
grants one retry by the original member, expires after 15 minutes, and can be
run by an `owner` or `shipper`. This is the safety valve for agent-role keys:
agents can ask for a specific risky action without receiving broader
credentials.

The eval suite in `tests/agent-evals/` proves the contract. Seven recovery
scenarios pass with a real agent given only the binary and `ship docs`: missing
secret, failing release command, probe failure, missing Dockerfile, expired
preview reference, dirty branch state, and recovering from an approval request.
Passing transcripts live in `tests/agent-evals/transcripts/`.

## Manifest

```toml
name = "taskflow"
box  = "203.0.113.7"
production_branch = "main"          # optional; default: main, else master

[processes]
web    = "npx react-router-serve build/server/index.js"
worker = { cmd = "node build/worker.js", preview = false }

[routes]                            # existing routes model, keyed by host[/path]
"taskflow.app"        = "web"
"taskflow.app/docs"   = { static = "docs/dist" }
"www.taskflow.app"    = { redirect = "taskflow.app" }

[env]                               # renamed from [vars], no alias kept
LOG_LEVEL    = "info"
DATABASE_URL = "@secret"            # secret name = var name
SMTP_URL     = "@secret"

[env.preview]                       # optional; overlays [env] in previews only
LOG_LEVEL    = "debug"
POSTHOG_KEY  = "phc_test456"

[preview]                           # optional; previews on your own domain
base    = "preview.taskflow.app"    # default: <box-ip>.sslip.io
aliases = true                      # stable <branch-slug>.<base> per branch

release = "npx drizzle-kit migrate" # top-level only; the [deploy] section is gone
probe   = "/healthz"                # health check for the routed process
webhook  = "https://ntfy.sh/..."     # NEW: webhook, §7
```

Previews are always protected. `ship` prints a capability URL when it deploys
one; `ship preview share` reprints it and `ship preview share --rotate`
replaces it. No `ship.toml` setting is required.

| Key | Meaning |
| --- | --- |
| `name` | App name on the box. |
| `box` | Box host used by app commands; the deploy user is fixed by ship. |
| `production_branch` | Branch that maps to Production; default `main`, else `master`. |
| `[processes]` | Container processes. String values are commands; table values can set `cmd`, `port`, `preview`, and `resources`. |
| `[routes]` | Host or host/path routes to a process, static directory, or redirect. |
| `[env]` | Committed non-secret env plus `@secret` references. |
| `[env.preview]` | Preview-only overlay; Production ignores it. |
| `[preview]` | Preview addressing: `base` puts preview hosts on your own domain (one wildcard DNS record); `aliases = true` adds a stable `<branch-slug>.<base>` alias per branch behind the same capability. |
| `release` | Command run after build and before traffic moves. |
| `probe` | Health path for the routed process. |
| `webhook` | Webhook URL for this app's deploy and Preview reaper events. |

## Secrets

Secret values are never passed in argv. Single-value writes read stdin:

```bash
printf '%s' "$DATABASE_URL" | ship secret set DATABASE_URL
printf '%s' "$DATABASE_URL" | ship secret set DATABASE_URL --preview
printf '%s' "$DATABASE_URL" | ship secret set DATABASE_URL --branch feature/billing
```

Bulk import reads dotenv files:

```bash
ship secret set --from .env --preview
ship secret set --from .env --branch staging --replace
```

List and remove keys, never values:

```bash
ship secret ls --json
ship secret rm DATABASE_URL --preview
```

Production resolves Production secrets only. Preview resolves the branch secret
first, then the shared Preview secret. Preview never receives Production
secret values.

## Exit

Exit is a feature. Delete the `ship` binary and you still have a boring,
well-configured Linux server: Caddy routes, supervised containers, app data
under `/data`, release files, backups, SSH users, and normal systemd units.
There is no hosted control plane to keep paying and no proprietary runtime to
unwind.

## References

- [docs/positioning.md](docs/positioning.md)
- [docs/ship-spec.md](docs/ship-spec.md)
- [docs/AGENT.md](docs/AGENT.md)
- [docs/getting-started.md](docs/getting-started.md)
- [docs/security-model.md](docs/security-model.md)
- [docs/release-checklist.md](docs/release-checklist.md)
- [docs/smoke-real-box.md](docs/smoke-real-box.md)
