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
ship box init deploy@203.0.113.7
```

The installer output is a host-convergence log. It ends with the next commands
to run, including `ship box doctor ...` and `ship init --box ... --host
<app-domain>`.

Inside a repo:

```bash
ship init
```

`ship init` writes `ship.toml`, a starter `Dockerfile`, and a tiny app when the
chosen template needs one. It never overwrites existing files and ends with
`next: ship`. Set `box = "deploy@203.0.113.7"` in `ship.toml`, commit the repo,
then deploy:

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

Stdout is only the URL:

```text
https://prod.203-0-113-7.sslip.io
```

That stdout contract is deliberate. Pipe it, paste it, or hand it to an agent.

## Daily Loop

Make a branch, ship it, review the URL:

```bash
git switch -c feature/billing
ship
```

The stdout URL is the Preview for that branch:

```text
https://feature-billing-x7q2.203-0-113-7.sslip.io
```

Useful commands during review:

```bash
ship pin feature/billing
ship logs web --tail 200
ship exec -- node scripts/check-data.js
ship rollback
```

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
shipped by: Name <name@example.com> (ssh key: ship-deploy)
next: fix the process port or probe path in ship.toml, then ship
```

If `notify` is set in `ship.toml`, ship posts failure and recovery events:

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
    "env": "prod",
    "outcome": "aborted_probe",
    "started_at": "2026-07-07T10:00:00Z",
    "ended_at": "2026-07-07T10:00:10Z",
    "previous_release": "abc123",
    "attempted_release": "def456",
    "failing_step": "probe",
    "stderr_tail": "HTTP status 502...",
    "identity": {
      "ssh_key_comment": "ship-deploy",
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
ship box doctor deploy@203.0.113.7 --json
```

Doctor JSON is a list of checks:

```json
[
  {
    "id": "disk_space",
    "status": "ok",
    "evidence": "used=10%",
    "remediation": "ship box doctor deploy@203.0.113.7"
  }
]
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
the output contract, deploy journals, notify payloads, and the error catalogue.
Operational reads expose `--json`, and `ship --json` gives agents a structured
deploy result.

The eval suite in `tests/agent-evals/` proves the contract. Six recovery
scenarios pass with a real agent given only the binary and `ship docs`: missing
secret, failing release command, probe failure, missing Dockerfile, expired
preview reference, and dirty branch state. Passing transcripts live in
`tests/agent-evals/transcripts/`.

## Manifest

```toml
name = "taskflow"
box  = "deploy@203.0.113.7"        # existing server field, renamed
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
DATABASE_URL = "@secret"            # NEW shorthand: secret name = var name
SMTP_URL     = "@secret:MAIL_URL"   # existing explicit form kept

[env.preview]                       # optional; overlays [env] in previews only
LOG_LEVEL    = "debug"
POSTHOG_KEY  = "phc_test456"

release = "npx drizzle-kit migrate" # top-level only; the [deploy] section is gone
probe   = "/healthz"                # health check for the routed process
notify  = "https://ntfy.sh/..."     # NEW: webhook, §7
```

| Key | Meaning |
| --- | --- |
| `name` | App name on the box. |
| `box` | SSH target used by app commands. |
| `production_branch` | Branch that maps to Production; default `main`, else `master`. |
| `[processes]` | Container processes. String values are commands; table values can set `cmd`, `port`, `preview`, and `resources`. |
| `[routes]` | Host or host/path routes to a process, static directory, or redirect. |
| `[env]` | Committed non-secret env plus `@secret` references. |
| `[env.preview]` | Preview-only overlay; Production ignores it. |
| `release` | Command run after build and before traffic moves. |
| `probe` | Health path for the routed process. |
| `notify` | Webhook URL for deploy, reaper, and doctor events. |

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
