# Simple VPS Spec

Source of truth for the Simple VPS product. The package-level specs
(`packages/simple-vps/SPEC.md`, `packages/simple-deploy/SPEC.md`) document
implementation; this file documents the public contract.

## Product

Simple VPS is one CLI for running JS/TS apps on your own VPS without Docker.

```text
fresh Ubuntu VPS  ->  install.sh           ->  hardened box
your app repo     ->  simple-vps deploy    ->  live app
```

Two responsibilities, one CLI:

- **Host operations** prepare and maintain the VPS. Rare. Mostly done once
  per box.
- **App operations** deploy, observe, and manage apps on a prepared VPS.
  Frequent. The 90% case.

The DX is wrangler-shaped in spirit: the app repo is the control plane, the
CLI is explicit about what runs where, no daemon, no opaque magic. Not a
wrangler clone.

## Non-Goals

- No Docker.
- No Kubernetes.
- No managed bindings (KV/D1/queues abstraction).
- No multi-provider abstraction.
- No git-push deploy.
- No dashboard UI.
- No plugin system.

## Public CLI

The user-facing surface. `simple-vps --help` lists exactly these verbs.

### App lifecycle

```bash
simple-vps init                                       # scaffold simple-vps.toml
simple-vps check [env]                                # validate manifest
simple-vps setup <env>                                # create app on the host
simple-vps deploy <env> [--dirty] [--include-dotenv]
simple-vps rollback <env> [release]
simple-vps destroy <env> [--yes] [--confirm <name>] [--purge]
simple-vps restart <env> <service>
simple-vps status <env>
simple-vps logs <env> [service] [--tail]
simple-vps ssh <env>
```

Behavior of each verb is unchanged from the prior `simple-deploy <verb>`.
Only the binary name changes.

### Secrets and env

```bash
simple-vps secret put <env> <KEY>
simple-vps secret list <env>
simple-vps secret rm <env> <KEY>
simple-vps env push <env> <file>
```

`secret put` reads stdin only, never argv.
`secret list` prints names only, never values.
Writes are atomic via the privileged server API. No auto-restart.

### Host operations

```bash
simple-vps host status
simple-vps host doctor
```

These run *on the box* and report on host readiness. Bootstrapping a fresh
VPS is the job of `install.sh` (see Installation), not the CLI.

### Diagnostics

```bash
simple-vps route list [--json]
```

Read-only view of the route table.

## Internal CLI (server-side)

The privileged server-side API is served by a separate `simple-vps` binary
installed at `/usr/local/bin/simple-vps` on the host. It is invoked over
SSH by the public CLI via `sudo` and is not run directly by users. The
public Bun CLI on the laptop and the server-side helper share a name by
design — they live in different contexts and are never both invoked from
the same shell.

The Bun public CLI's `--help` lists the public verbs above. The server
helper's `--help` lists the internal verbs below. Neither leaks into the
other.

```bash
sudo simple-vps app create <name>
sudo simple-vps app destroy <name>
sudo simple-vps app install-unit <name> <service> <path>
sudo simple-vps app uninstall-unit <name> <service>
sudo simple-vps app daemon-reload
sudo simple-vps app service <action> <name> <service>
sudo simple-vps app run-as <name> --cwd <path> -- <cmd> [args...]
sudo simple-vps app install-env <name> <path>
sudo simple-vps app read-env <name>

sudo simple-vps route proxy <host> --port <port> --app <name>
sudo simple-vps route static <host> --root <path> --app <name>
sudo simple-vps route redirect <host> --to <url> --app <name>
sudo simple-vps route remove --app <name>
```

These have not changed shape from 0.1.x. The sudoers contract remains one
line for the whole server binary, installed at `/etc/sudoers.d/simple-vps`
(the file was named `simple-deploy` in 0.1.x; renamed in 0.2.0):

```text
/etc/sudoers.d/simple-vps
  admin ALL=(root) NOPASSWD: /usr/local/bin/simple-vps
```

## Manifest

The manifest is `simple-vps.toml` at the app repo root.

Schema, validation rules, three build modes (A/B/C), env override blocks,
include/dotenv handling, and lockfile detection are unchanged from
`packages/simple-deploy/SPEC.md`. Only the filename changes.

`simple-deploy.toml` is not read. There is no fallback path.

## Installation

Bootstrapping a fresh Ubuntu 24.04 host is the job of `install.sh`. The
unified CLI does not replace it.

```text
# on a fresh box, ssh'd as root:
curl -fsSL https://simple-vps.dev/install.sh | bash \
    --tailscale-auth-key=... \
    --cloudflare-tunnel-token=...

# or from a laptop, against a fresh box:
./install.sh --mode remote --host <ip> --bootstrap-user root \
    --ssh-key ~/.ssh/id_ed25519 --admin-user admin
```

`install.sh` supports both remote-from-laptop and local-on-box modes today;
that does not change in 0.2.0.

After install, the primary checks are:

```bash
simple-vps host status
simple-vps host doctor    # if chasing a problem
```

## Boundary With Internal Packages

```text
SPEC.md                          public product contract (this file)
packages/simple-vps/SPEC.md      host installer + Ansible roles +
                                 privileged Python server helper
packages/cli/SPEC.md             unified Bun CLI implementation
```

The unified Bun CLI lives in `packages/cli/`. The privileged server-side
helper and the host installer live in `packages/simple-vps/`. Users do not
need to know about this split.

## Versioning

Standard SemVer.

```text
0.1.x    preview line, patch fixes only
         no contract changes

0.2.0    unified `simple-vps` CLI lands
         manifest renamed to `simple-vps.toml`
         no server layout / sudoers / systemd changes
         no fallback to the old shape

0.3.0+   slice chosen from real friction surfaced by 0.2.0 usage
         not from a predetermined architecture goal
         contract changes acceptable between minors

1.0.0    much later
         "manifest schema, CLI verbs, server layout are stable enough
         that breaking them would be a real compatibility event"
```

Pre-1.0 minors may include breaking changes. Patch versions are
non-breaking by intent. Tag `1.0.0` once the product has survived real use
for a meaningful window without needing contract changes.

## 0.2.0 Scope

### What lands

- Unified `simple-vps` Bun/TS CLI with the public verbs above.
- Internal verbs unchanged in shape and behavior.
- `simple-vps.toml` is the only manifest filename.
- `simple-deploy` binary goes away. The `packages/simple-deploy/` folder
  is renamed to `packages/cli/`.
- README, package SPECs, and the fake-VPS smoke updated to reflect the
  single CLI.

### What does not change

- Server layout (`/var/apps/<name>/...`).
- Sudoers grant target (still the `simple-vps` binary, one line).
- systemd unit naming (`simple-<app>-<service>.service`).
- The three build modes (A/B/C) and their detection.
- Per-app user + 2775 group-write contract.
- The privileged helper language. Python stdlib is good at the sudo
  boundary (small audit surface, no supply chain). Port only if there is a
  concrete reason, not for cohesion.
- The `install.sh` bootstrap flow.

### What stays out of 0.2.0

- Bootstrap orchestration as a CLI verb. `install.sh` keeps that job.
- Auto-setup on first deploy. `setup` stays explicit because it creates
  system users.
- Compatibility shims for `simple-deploy.toml` or the `simple-deploy`
  binary. Pre-public; no contract to preserve.

## Non-Goals For 0.2.0

- Port the privileged helper from Python to Bun.
- Add new public verbs not in this spec. Refactor first, feature later.
- Rename the project. `simple-vps` is the name.
- Change the manifest schema beyond filename.
- Rename internal package folders beyond `simple-deploy → cli`.
  `packages/simple-vps/` stays.

## Future Architecture Candidates

Options worth considering after 0.2.0 surfaces concrete friction. Not
scheduled. Not promised. Listed so they aren't forgotten and don't get
re-litigated from scratch every time someone asks.

- **Thin `simple-vps host install` wrapper.** A CLI verb that shells out
  to the existing `install.sh` flow so every user-typed command starts
  with `simple-vps`. Pure cohesion polish. Worth doing only if "two
  entry points" turns out to be real friction. Cheap (~50 lines of
  Bun).

- **Bun privileged server helper.** Replace the Python helper with a Bun
  equivalent. Worth doing only if a spike proves the Bun version stays
  small, stdlib-only (no npm dependencies at the sudo boundary),
  equally auditable, and unlocks meaningful code-sharing with the
  laptop CLI. Cohesion alone is not sufficient justification — the
  helper is invisible to users.

- **Thinner bootstrap.** Shrink `install.sh` to install Bun and the CLI
  only, then exec `simple-vps host install` for the rest. Requires the
  privileged helper port first. Revisit only if `install.sh` becomes
  a real maintenance burden.

What stays out of consideration:

- **Replacing Ansible.** Ansible is the right tool for host convergence
  (apt, systemd, UFW, sudoers, idempotent state). Rewriting it in Bun
  is months of work for marginal user-facing improvement. Ansible
  stays unless a concrete product reason appears.

The 0.3.0 slice is picked from real friction after 0.2.0 lands —
deploy ergonomics, log readability, secret flows, install surprises,
maintenance pain. Not from this list.

## Implementation Order

Suggested sequence for the 0.2.0 slice:

1. Rename `packages/simple-deploy/` to `packages/cli/`. Rename the binary
   target from `simple-deploy` to `simple-vps`. Internal verbs
   (`app *`, `route *`) continue to be served by the Python helper.
2. Hide internal verbs from `simple-vps --help`. They remain callable.
3. Switch all manifest reads from `simple-deploy.toml` to `simple-vps.toml`.
   Remove the old filename from validation, error messages, and `init`.
4. Rename `/etc/sudoers.d/simple-deploy` to `/etc/sudoers.d/simple-vps` in
   the Ansible role. The sudoers grant target does not change.
5. Update the fake-VPS smoke to invoke `simple-vps` and use
   `simple-vps.toml`.
6. Update README and SPEC files to reflect single CLI. Delete prose that
   references `simple-deploy` as a separate tool.
7. Bump `packages/simple-deploy/package.json` (or its successor) to
   `0.2.0`.
8. Tag `v0.2.0`.

Each step is small enough to land as its own commit. Steps 1, 3, 4, 5 are
the load-bearing ones; the rest are docs and metadata.
