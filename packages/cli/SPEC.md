# Simple VPS CLI Spec

Implementation reference for the Bun CLI. The public product contract lives in
the root [SPEC.md](../../SPEC.md).

## Goal

Simple VPS CLI turns an app repo into a running release on a Simple VPS host:

```text
app repo + simple-vps.toml -> simple-vps deploy production -> live app
```

The model is **Wrangler-shaped for your own VPS**:

- The app repo is the control plane. Manifest is committed.
- Releases are identified by git SHA.
- The CLI deploys explicitly. No opaque server-side magic.
- No Docker, no daemon, no dashboard.

The audience is teams running production JS/TS workloads on a small number of
VPS hosts.

## Non-Goals (v1)

- No Docker.
- No git-push deploy. The CLI is the only entrypoint.
- No remote (server-side) build. Build runs in CI or on a laptop.
- No bindings/KV/D1/queues abstraction. There are no managed services to bind
  to. `DATABASE_URL` is a string in `.env`.
- No multi-server orchestration. One deploy targets one host.
- No Postgres management.
- No dashboard UI.
- No plugin system.

## Boundary With Simple VPS

```text
packages/simple-vps -> "this server is ready to host apps"
packages/cli        -> "this app is running here, with these services and routes"
```

Simple VPS owns:

- Host packages, users, SSH, UFW, Tailscale, Cloudflare Tunnel, Caddy install.
- Routing state at `/etc/simple-vps/state.json`.
- Generated Caddy files at `/etc/caddy/simple-vps/`.
- The `simple-vps route proxy|static|redirect|remove` primitives.
- The `simple-vps app ...` privileged API for app lifecycle on the server.
- The sudoers policy that lets Simple VPS CLI invoke `simple-vps` over SSH
  (see [Server API](#server-api)).

Simple VPS CLI owns:

- The app's release directory layout.
- Per-app system user and per-service systemd units.
- Per-release dependency install.
- The `shared/.env` file and env layering.
- Calling `sudo simple-vps route ...` to publish routes.
- Rollback, health-check, and prune behavior.

Simple VPS CLI never edits `/etc/caddy/*` directly. It calls
`sudo simple-vps route ...` on the server.

## CLI

```bash
simple-vps init
simple-vps setup <env>
simple-vps deploy <env>
simple-vps deploy <env> --dirty
simple-vps deploy <env> --include-dotenv
simple-vps rollback <env>
simple-vps rollback <env> <sha>
simple-vps status <env>
simple-vps logs <env>
simple-vps logs <env> <service>
simple-vps logs <env> <service> --tail
simple-vps restart <env> <service>
simple-vps secret put <env> KEY
simple-vps secret list <env>
simple-vps secret rm <env> KEY
simple-vps env push <env> <file>
simple-vps ssh <env>
simple-vps route list [--json] [--server <ssh-target>]
simple-vps host status [--server <ssh-target>]
simple-vps host doctor [--server <ssh-target>]
simple-vps destroy <env>
simple-vps destroy <env> --purge
simple-vps destroy <env> --confirm <app>
simple-vps destroy <env> --purge --yes --confirm <app>
```

Rules:

- `<env>` matches a `[env.<name>]` block in the manifest.
- `simple-vps deploy` is the only mutating command on app code.
- `secret put` reads the value from stdin or prompts. Never accepts the value
  as an argument.
- `secret list` prints names only, never values.
- data-preserving `destroy` requires `--yes` or `--confirm <app>`.
- `destroy --purge` requires both `--yes` and `--confirm <app>`.
- Host and route read-only commands target the only env in the manifest. If
  multiple envs exist, they fail rather than guessing a server.

## Manifest

The manifest lives at `simple-vps.toml` in the app repo root.

### Schema

```toml
# Required identity. Matches package.json name by convention.
name = "my-app"

# Optional. Absent = no build (Mode A). Present = build required.
[build]
command = "bun run build"           # required when [build] is present
output = "dist"                     # required when [build] is present
include = ["public", "prisma"]      # optional, see Artifact Rules
install = true                      # optional, default true (Mode B vs C)

# One [env.<name>] block per deploy target.
[env.production]
server = "deploy@100.x.y.z"         # required, SSH target
runtime = "bun"                     # required: bun | node | static
keep_releases = 5                   # optional, default 5

# Zero or more services. Each becomes a systemd unit.
[services.web]
command = "bun run start"           # required
port = 3000                         # optional, required if any route targets this service
healthcheck = "/health"             # optional, required if port is set
healthcheck_timeout = 10            # optional, default 10 seconds total
healthcheck_status = 200            # optional, default 200

[services.worker]
command = "bun run worker"

# Zero or more named routes. Each maps to a simple-vps route primitive.
[routes.app]
host = "app.example.com"
type = "proxy"                      # proxy | static | redirect
service = "web"                     # required for type=proxy

[routes.assets]
host = "data.example.com"
type = "static"
# root is always /var/apps/<name>/current
```

### Env Overrides

`[env.<name>]` can override anything declared at the top level:

```toml
name = "my-app"

[build]
command = "bun run build"
output = "dist"

[services.web]
command = "bun run start"
port = 3000

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[env.staging]
server = "deploy@100.x.y.z"
runtime = "bun"

[env.staging.services.web]
command = "bun run start --debug"
```

### Validation Rules

- `name` matches `^[a-z][a-z0-9-]{1,40}$`.
- `[env.<name>].path` is optional. If absent, it is computed as
  `/var/apps/<name>`. If present for 0.2 manifest compatibility, it must equal
  `/var/apps/<name>`.
- Service names match `^[a-z][a-z0-9-]{0,30}$`.
- Route names match the same shape as service names.
- A route with `type = "proxy"` must reference a service with a `port`.
- A service with a `port` must declare a `healthcheck`.
- Multiple services may declare ports, but each port must be unique within the
  manifest.
- A service may not be named `current`, `releases`, or `shared`.

## Server Layout

```text
/var/apps/<name>
|-- current -> releases/<sha>
|-- releases
|   |-- a1b2c3d4...           # full git SHA per release
|   `-- a1b2c3d5...-dirty-20260518153022
|-- shared
|   |-- .env                  # 0600, owned by app-<name>
|   |-- db/                   # SQLite, Litestream targets, etc.
|   |-- storage/              # uploads, generated files
|   `-- logs/                 # app-managed logs (systemd logs go to journal)
`-- systemd
    `-- (rendered units, also installed into /etc/systemd/system/)
```

Inside each release:

```text
releases/<sha>/
|-- (artifact contents)
|-- node_modules/             # produced by server-side install (Mode A and B)
|-- .env -> ../../shared/.env
|-- db -> ../../shared/db
|-- storage -> ../../shared/storage
`-- logs -> ../../shared/logs
```

## Release Identity

- Release directory name = full git SHA of `HEAD`.
- CLI output displays the short SHA (`a1b2c3d`).
- Dirty deploys require `--dirty`. The release directory becomes
  `<sha>-dirty-<unix-utc-timestamp>` and `simple-vps status` prints
  `dirty: yes` prominently.
- `simple-vps deploy` against a SHA already present on the server skips
  build/upload and re-runs the activation phase (idempotent re-deploy).

## Build Modes

Detected from the manifest. There are exactly three.

### Mode A — No Build

```toml
# No [build] block.
```

Pipeline:

```text
git archive HEAD
upload artifact (rsync)
server: install production deps using detected package manager
start services
```

Use when the app runs directly from source (`bun run src/server.ts`,
`tsx server.ts`, plain Node).

### Mode B — Build + Install

```toml
[build]
command = "bun run build"
output = "dist"
include = ["public", "prisma"]
# install defaults to true
```

Pipeline:

```text
git archive HEAD into temp checkout
run [build] command in temp checkout
assemble artifact = output/ + include + package.json + lockfile
upload artifact (rsync)
server: install production deps
start services
```

Use for Next.js, Vite SSR, apps with a build step that still need runtime
dependencies on the server.

### Mode C — Bundled / No Install

```toml
[build]
command = "bun build src/worker.ts --target=bun --outfile=dist/worker.js"
output = "dist"
install = false
```

Pipeline:

```text
git archive HEAD into temp checkout
run [build] command in temp checkout
assemble artifact = output/ only
upload artifact (rsync)
no server install
start services
```

Use when the build emits a self-contained bundle. Fastest deploys.

### Mode Selection

| `[build]` | `install`        | Mode |
|-----------|------------------|------|
| absent    | n/a              | A    |
| present   | true (default)   | B    |
| present   | false            | C    |

`runtime = "static"` always implies `install = false` and starts no services.

## Lifecycle

### setup

```bash
simple-vps setup <env>
```

Idempotent. Required before the first `deploy`. `deploy` hard-fails with a
clear error if setup has not run.

Steps:

```text
1.  verify SSH works to env.server
2.  verify required server tools: simple-vps, rsync, the runtime binary
3.  via sudo on the server:
      create system user app-<name> (no login shell, no home)
      add the invoking sudo user to the app-<name> group
      create /var/apps/<name>
      create /var/apps/<name>/{releases,shared,shared/db,shared/storage,shared/logs}
      chown -R app-<name>:app-<name> /var/apps/<name>
      chmod /var/apps/<name> and /var/apps/<name>/releases to 2775
      create /tmp/simple-deploy with mode 1777 for unit uploads
      create /var/apps/<name>/shared/.env (0600, owned by app-<name>) if absent
4.  register the app with simple-vps state so it appears in `simple-vps status`
5.  print success summary
```

### deploy

The remote staging directory remains `/tmp/simple-deploy` and the successful
release marker remains `.simple-deploy-success` in 0.2.0. They are internal
server API details preserved to avoid a server-layout migration during the
public CLI rename.

```bash
simple-vps deploy <env>
simple-vps deploy <env> --dirty
```

Pipeline:

```text
1.  validate manifest
2.  resolve SHA (refuse dirty without --dirty)
3.  verify setup has run on the server
4.  git archive HEAD into local temp dir (or tar working tree if --dirty)
5.  if [build] present: run [build] command in temp dir
6.  assemble artifact per build mode
7.  block .env files unless --include-dotenv (see Artifact Rules)
8.  create release dir with mode 2775, rsync artifact to server:/var/apps/<name>/releases/<sha>/,
    then restore release dir mode to 2775 because rsync can copy the local temp dir mode
9.  server: link shared/.env, shared/db, shared/storage, shared/logs into release dir
10. server: if install enabled, run production install
11. server: render systemd unit files, install to /etc/systemd/system/
12. server: systemctl daemon-reload
13. server: stop existing simple-<name>-* services (no-op on first deploy)
14. server: flip current symlink to new release
15. server: start simple-<name>-* services
16. server: health-check each service with a port
17. on health failure: stop new, flip current back, restart previous, mark failed
18. for each route, invoke `sudo simple-vps cloudflare publish ...` on the
    server, then `sudo simple-vps route ...`
19. touch `<release>/.simple-deploy-success`
20. prune releases beyond keep_releases (newest wins, preserves currently-linked)
```

Health check semantics:

- HTTP `GET http://127.0.0.1:<port><healthcheck>`.
- Acceptable status: `healthcheck_status` (default 200).
- Attempts: `healthcheck_timeout` attempts (default 10), one per second.
  Each attempt has a 2s HTTP timeout and must return `healthcheck_status`.

Release pruning keeps the newest `keep_releases` release directories by mtime
and always preserves the currently linked release plus the newest successful
rollback target, even if either falls outside the count.
Pruning failures warn but do not fail the deploy after the release has been
activated, routed, and marked successful.

Failure mode is **stop-and-replace**. Blue-green is a future
`[env.<name>] strategy = "blue-green"` and is not in v1.

### status

```bash
simple-vps status <env>
```

Read-only.

Shows:

```text
- current release (`readlink -f /var/apps/<name>/current`)
- service state for each declared service
- routes owned by the app from `sudo simple-vps route list --json`
```

Service state is whatever `sudo simple-vps app service is-active` prints.

### logs

```bash
simple-vps logs <env>
simple-vps logs <env> <service>
simple-vps logs <env> <service> --tail
```

Read-only. With one declared service, the service argument is optional. With
multiple services, the caller must name the service.

Maps directly to:

```bash
journalctl -u simple-<name>-<service>.service -n 200 --no-pager
journalctl -u simple-<name>-<service>.service -f    # --tail
```

### rollback

```bash
simple-vps rollback <env>           # previous successful release
simple-vps rollback <env> <sha>     # explicit release in releases/
```

Pipeline:

```text
1.  resolve target release (must exist in releases/)
2.  server: stop simple-<name>-* services
3.  server: flip current symlink
4.  server: start services
5.  server: health-check
6.  touch `<release>/.simple-deploy-success`
7.  no route changes (routes follow services by name, not by release)
```

With no release argument, the target is the newest release directory with a
`.simple-deploy-success` marker, excluding the current symlink target, sorted
by mtime. Explicit rollback accepts any existing release directory.

Rollback does not modify Caddy state and does not publish routes. A rollback
target that passes health is marked successful. Routes follow services by name,
not by release.

### destroy

```bash
simple-vps destroy <env>
simple-vps destroy <env> --purge
```

Default (data-preserving):

```text
1.  stop and disable all simple-<name>-*.service units
2.  remove unit files from /etc/systemd/system/
3.  systemctl daemon-reload
4.  invoke `sudo simple-vps route remove --app <name>`
5.  remove /var/apps/<name>/current symlink
6.  preserve /var/apps/<name>/{releases,shared}
```

With `--purge`:

```text
1.  all of the above, then
2.  remove /var/apps/<name>
3.  remove system user app-<name>
4.  unregister from simple-vps state
```

The data-preserving form requires either `--yes` or `--confirm <app>`.
`--purge` requires both `--yes` and `--confirm <app>`, where `<app>` matches
the manifest `name`.

Known v1 limitation: `destroy` removes services declared in the current
manifest. If a service was removed from the manifest before destroy, its old
unit may need manual cleanup or a future server-side unit enumeration command.

## Systemd Model

### Naming

```text
simple-<name>-<service>.service
```

Examples: `simple-my-app-web.service`, `simple-my-app-worker.service`.

### Unit Template

```ini
[Unit]
Description=simple-vps: <name>/<service>
After=network.target

[Service]
Type=simple
User=app-<name>
Group=app-<name>
WorkingDirectory=/var/apps/<name>/current
EnvironmentFile=/var/apps/<name>/shared/.env
Environment="SIMPLE_APP_NAME=<name>"
Environment="SIMPLE_ENV=<env>"
Environment="SIMPLE_RELEASE=<sha>"
Environment="SIMPLE_RELEASE_DIR=/var/apps/<name>/releases/<sha>"
Environment="NODE_ENV=production"
Environment="PORT=<port>"             # only when service has a port
ExecStart=/usr/bin/env bash -c 'exec <command>'
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/apps/<name>/shared

[Install]
WantedBy=multi-user.target
```

### Service Command Semantics

Service `command` strings are shell commands, not raw systemd `ExecStart`
strings. The rendered unit wraps them with `bash -c 'exec ...'` so that:

- Shell syntax works: inline env (`FOO=bar bun server.js`), redirects, pipes.
- Build commands and service commands behave identically (both run through a
  shell).
- `exec` replaces bash with the app process, so the service PID is the app
  itself. Signals propagate cleanly and `systemctl stop` does not race a
  bash wrapper.

`bash -c` (not `-lc`) is intentional. App users have no login shell or home,
and login-shell profile sourcing would introduce env variability that
conflicts with `EnvironmentFile=` and `Environment=` declarations.

Implementation note: the rendered unit must shell-escape the command before
embedding it inside `bash -c '...'`. Naive string concatenation breaks on
commands containing single quotes or systemd-special characters (`%`, `$`).
Use a single-quote-safe escaper (replace `'` with `'\''`) and emit
`ExecStart=/usr/bin/env bash -c '<escaped>'`.

### Hardening Notes

- `MemoryDenyWriteExecute=true` is intentionally **not** set. It breaks JIT in
  some Bun configurations and is a poor default for JS runtimes.
- Per-app `User=app-<name>` is required, not optional. Retrofitting per-app
  users is painful.
- `ReadWritePaths` is the only writable location outside of `PrivateTmp`.
  Apps that need other writable paths must declare them in the manifest
  (future: `[services.<name>] writable_paths = [...]`; not in v1).

### Logs

Logs go to the journal. `simple-vps logs <env> <service>` is a thin wrapper
around:

```bash
journalctl -u simple-<name>-<service>.service -n 200 --no-pager
journalctl -u simple-<name>-<service>.service -f    # --tail
```

## Environment Variables

Two layers.

### shared/.env (user-controlled)

- One file per app per env, at `/var/apps/<name>/shared/.env`.
- `0600`, owned by `app-<name>`.
- Parsed by systemd's `EnvironmentFile=`, **not** by a shell. Specifically:
  - No quote interpretation.
  - No `export` keyword.
  - No variable expansion.
  - No inline comments after a value.
- `simple-vps env push` and `simple-vps secret put` MUST validate the
  resulting file against systemd's parser before writing it.

### systemd Environment= (tool-controlled)

```text
SIMPLE_APP_NAME
SIMPLE_ENV
SIMPLE_RELEASE
SIMPLE_RELEASE_DIR
NODE_ENV=production
PORT          # only for services with a port
```

### Collision Rule

On conflict, `shared/.env` wins. User-controlled config beats tool-generated
defaults.

### Secrets vs Env

`simple-vps secret put` and `simple-vps env push` both write to
`shared/.env`. The split is operational:

- `secret put` is interactive, never echoes the value, never accepts it as an
  argument. Use for credentials.
- `env push` takes a file. Use for bulk non-secret config and initial bootstrap.

Both produce the same on-disk format.

### Env Commands

```bash
simple-vps env push <env> <file>
simple-vps secret put <env> KEY
simple-vps secret list <env>
simple-vps secret rm <env> KEY
simple-vps restart <env> <service>
```

`env push <file>` replaces the entire `shared/.env` with `<file>`.
`secret put KEY` reads the value from stdin when piped, or prompts with no echo
when stdin is a TTY. Values are never accepted as CLI arguments.
`secret rm KEY` removes matching `KEY=...` entries and is a no-op if absent.
`secret list` prints names only, never values.

Secret writes are read-modify-write and are not lock-protected in v1. Serialize
concurrent `secret put`/`secret rm` runs from CI; racing writes can lose one
update.

Writes are atomic:

```text
1. validate resulting EnvironmentFile syntax locally
2. rsync temp file to /tmp/simple-deploy/
3. sudo simple-vps app install-env <name> <temp-file>
4. simple-vps validates again, chowns to app-<name>:app-<name>, chmod 0600,
   and renames into /var/apps/<name>/shared/.env
```

`env push`, `secret put`, and `secret rm` do **not** restart services. They
print the explicit restart command to run. `restart` invokes
`sudo simple-vps app service restart <name> <service>` and then runs that
service's health check.

## Route Contract

Simple VPS CLI does not edit Caddy. It calls `sudo simple-vps route ...` on
the server over SSH. Simple VPS validates and owns the ingress state machine
(see the
[Simple VPS CLI Server API](../simple-vps/SPEC.md#simple-vps-cli-server-api)
in the Simple VPS spec).

```bash
# routes.app of type=proxy targeting services.web (port 3000)
sudo simple-vps route proxy app.example.com --port 3000 --app <name>

# routes.assets of type=static
sudo simple-vps route static data.example.com \
  --root /var/apps/<name>/current --app <name>

# routes.redirect of type=redirect
sudo simple-vps route redirect old.example.com \
  --to https://new.example.com --app <name>

# destroy
sudo simple-vps route remove --app <name>
```

Rules:

- Routes are published at the end of a successful deploy, not before. A failed
  health check leaves the previous routes untouched.
- Cloudflare API publication runs through the privileged server helper before
  local Caddy route publication, so API errors fail the deploy before local
  route state changes.
- Route deletion happens only on `destroy`. Rollback never touches routes.
- Static route `root` is always `/var/apps/<name>/current`. The release
  directory is the artifact root: build output contents are copied directly
  into the release, so `current/` already contains what should be served.
  The manifest cannot override this in v1.
- With `runtime = "static"` and no `[build]`, the release contains the full
  git archive snapshot, so `package.json`, source files, and config files are
  publicly served. Use a build step to control exposure
  (e.g. `command = "cp -r public dist"`, `output = "dist"`).
- The `--app <name>` flag on `simple-vps route` ties the route to the app for
  `simple-vps route remove --app <name>`.

## SSH and Auth

### Laptop

Standard OpenSSH:

```text
~/.ssh/config
ssh-agent
IdentityFile
```

Simple VPS CLI shells out to `ssh` and `rsync`. No special handling.

### CI

```text
SIMPLE_VPS_SSH_KEY        # private key contents
SIMPLE_VPS_KNOWN_HOSTS    # known_hosts entries for env.server
```

When `SIMPLE_VPS_SSH_KEY` is set:

- Write the key to a temp file with `0600`.
- Pass `-i <tempfile>` to `ssh` and `rsync`.
- Use `-o StrictHostKeyChecking=yes -o UserKnownHostsFile=<tempfile>`.
- Refuse to run if `SIMPLE_VPS_KNOWN_HOSTS` is missing.
- Never write `StrictHostKeyChecking=no`. Anywhere. Ever.

Secrets are never written to the manifest. `env.<name>.server` is allowed and
expected to be committed.

## Server API

Simple VPS CLI needs narrow root privileges on the server. Simple VPS exposes
them as subcommands of the `simple-vps` binary, gated by a single sudoers
line for `/usr/local/bin/simple-vps`. Simple VPS CLI invokes them over SSH:

```bash
# app lifecycle
sudo simple-vps app create <name>
sudo simple-vps app destroy <name>
sudo simple-vps app read-env <name>
sudo simple-vps app install-env <name> <env-file>
sudo simple-vps app install-unit <name> <service> <unit-file>
sudo simple-vps app uninstall-unit <name> <service>
sudo simple-vps app daemon-reload
sudo simple-vps app service <action> <name> <service>
sudo simple-vps app run-as <name> --cwd <path> -- <command> [args...]

# routes
sudo simple-vps route proxy <host> --port <port> --app <name>
sudo simple-vps route static <host> --root <path> --app <name>
sudo simple-vps route redirect <host> --to <url> --app <name>
sudo simple-vps route remove --app <name>
```

Validation lives inside `simple-vps`. Argument shape, app/service naming,
host/port ranges, unit file ownership, and `run-as --cwd` scoping
are all enforced server-side, not in sudoers globs. The full contract lives
in the
[Simple VPS CLI Server API](../simple-vps/SPEC.md#simple-vps-cli-server-api)
section of the Simple VPS spec.

If the sudoers entry or the `app` subcommands are missing on the server,
`simple-vps setup` fails with a clear pointer to re-run the Simple VPS
install. Simple VPS CLI never installs server-side capability itself.

## Artifact Rules

### Source

The artifact is built from a `git archive HEAD` checkout in a local temp dir,
not from the working tree. This guarantees clean builds and matches release
identity to the SHA. `--dirty` opts into `tar` of the working tree.

### Contents

- Mode A: the full `git archive HEAD` tree.
- Mode B: contents of `[build] output`, plus every path in `[build] include`,
  plus `package.json` and the detected lockfile (auto-included).
- Mode C: contents of `[build] output` only.

`include` entries are paths relative to the temp checkout root. Directories are
recursive. No glob support in v1.

### Exclusions

The following are **always** excluded from the artifact:

- `node_modules/` (any depth)
- `.git/`
- `.simple-vps/` (reserved local working dir)
- Build caches: `.next/cache/`, `.turbo/`, `.parcel-cache/`, `.cache/`,
  `node_modules/.cache/`
- `.DS_Store`, `Thumbs.db`

### Dotenv Blocklist

The following filenames are refused by default when present in the assembled
artifact:

```text
.env
.env.local
.env.development
.env.development.local
.env.staging
.env.staging.local
.env.production
.env.production.local
.env.test
.env.test.local
```

Allowed: `.env.example`, `.env.sample`, `.env.defaults`.

Override: `--include-dotenv`. The CLI prints a loud warning and lists each
blocked file it is about to ship.

### Symlinks

Symlinks are preserved as symlinks while assembling the artifact. Absolute
symlinks and relative symlinks that resolve outside the artifact root are
refused before upload.

## Package Manager Detection

Detected from the lockfile present in the artifact root:

| Lockfile             | Install command                                        |
|----------------------|--------------------------------------------------------|
| `bun.lock`           | `bun install --production --frozen-lockfile`           |
| `bun.lockb`          | `bun install --production --frozen-lockfile`           |
| `pnpm-lock.yaml`     | `pnpm install --prod --frozen-lockfile`                |
| `package-lock.json`  | `npm ci --omit=dev`                                    |
| `yarn.lock`          | `yarn install --production --frozen-lockfile`          |

Rules:

- Exactly one lockfile must be present in Mode A and Mode B. Multiple lockfiles
  are a project error: `simple-vps deploy` refuses and lists them.
- The package manager binary must be installed on the server. Simple VPS
  installs `bun` and `node`/`npm` by default. `pnpm` is installed by Simple
  VPS. `yarn` is not installed by default.
- `[env.<name>] runtime` is independent of the install tool. `runtime = "bun"`
  means `bun` starts the service. `bun.lockb` means `bun install` provisions
  deps. Mixing is allowed.

## Init

```bash
simple-vps init
```

Generates a starter `simple-vps.toml` by inspecting:

- `package.json` for `name` and a likely `start` script.
- The presence of a lockfile to suggest `runtime`.
- The presence of a `build` script to suggest a `[build]` block (Mode B).

Never overwrites an existing manifest. Prints next steps:

```text
1. edit simple-vps.toml
2. simple-vps setup production
3. simple-vps deploy production
```

## Validation

The CLI MUST run before every deploy:

```text
- manifest parses
- name/service/route names match shape rules
- env target exists
- all routes reference existing services with ports (for type=proxy)
- all services with ports have healthchecks
- exactly one lockfile (Mode A and B)
- no blocked dotenv files in artifact (unless --include-dotenv)
- shared/.env content is systemd-EnvironmentFile-parseable
```

The CLI SHOULD run on save (`simple-vps check`):

```bash
simple-vps check          # validate manifest only
simple-vps check production   # also check SSH, server tooling, setup
```

## Implementation Plan

1. Lock this spec.
2. Implement `init`, `check`, and manifest parsing.
3. Implement `setup` end-to-end against a Simple VPS host.
4. Implement Mode A deploy end-to-end (smallest viable path).
5. Add Mode B and Mode C.
6. CI examples (GitHub Actions) using `SIMPLE_VPS_SSH_KEY` and
   `SIMPLE_VPS_KNOWN_HOSTS`.
7. End-to-end smoke test deploying a Hono/Bun example app to a fresh
   Simple VPS host.
8. Only then tag v1.
