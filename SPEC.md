# Simple VPS Spec

Source of truth for the Simple VPS product. Implementation details live in
`provisioning/SPEC.md`, `cmd/`, and `internal/`; this file documents the public
contract.

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
simple-vps host status [--server <ssh-target>]
simple-vps host doctor [--server <ssh-target>]
simple-vps host install [install options]
```

`status` and `doctor` run on the box and report on host readiness. `host install`
runs the Ansible-backed host installer from the Go binary. The public
`install.sh` entrypoint remains as a tiny bootstrap for the one-line install
path.

### Diagnostics

```bash
simple-vps route list [--json] [--server <ssh-target>]
```

Read-only view of the route table.

## Internal CLI (server-side)

The Go `simple-vps` binary serves both the public app-deploy CLI and the
privileged server-side API installed at `/usr/local/bin/simple-vps`.
Public verbs run on a laptop or CI runner. Internal verbs run on the host
through SSH via `sudo` and are not user-facing product commands.

`simple-vps --help` advertises the public verbs. The internal API remains
documented here because it is the contract between the deploy client,
installer, and host helper.

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

sudo simple-vps route proxy --port <port> --app <name> <host>
sudo simple-vps route static --root <path> --app <name> <host>
sudo simple-vps route redirect --to <url> --app <name> <host>
sudo simple-vps route remove --app <name>
```

These have not changed shape from 0.1.x. The sudoers contract remains one line
for the whole server binary, installed at `/etc/sudoers.d/simple-vps` (the file
was named `simple-deploy` in 0.1.x; renamed in 0.2.0). In 0.3 fresh installs
the grant belongs to the deploy user:

```text
/etc/sudoers.d/simple-vps
  deploy ALL=(root) NOPASSWD: /usr/local/bin/simple-vps
```

## Manifest

The manifest is `simple-vps.toml` at the app repo root.

Schema, validation rules, three build modes (A/B/C), env override blocks,
include/dotenv handling, and lockfile detection are owned by the Go config
package and covered by the Go test suite.

`simple-deploy.toml` is not read. There is no fallback path.

## Installation

Bootstrapping a fresh Ubuntu 24.04 host starts with `install.sh`. The script
finds, downloads, or builds a Go binary, then execs `simple-vps host install`.

```text
# on a fresh box, ssh'd as root:
curl -fsSL https://simple-vps.dev/install.sh | bash \
    --tailscale-auth-key=... \
    --cloudflare-tunnel-token=... \
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub

# or from a laptop, against a fresh box:
./install.sh --mode remote --host <ip> --bootstrap-user root \
    --ssh-key ~/.ssh/id_ed25519 \
    --operator-ssh-public-key-file ~/.ssh/id_ed25519.pub \
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
```

`install.sh` supports both remote-from-laptop and local-on-box modes.
The Go command underneath accepts the same flags:

```bash
simple-vps host install --mode remote --host <ip> --bootstrap-user root
```

After install, the primary checks are:

```bash
simple-vps host status --server deploy@100.x.y.z
simple-vps host doctor --server deploy@100.x.y.z    # if chasing a problem
```

The expected host security posture is documented in
[docs/security-model.md](docs/security-model.md).

## Boundary With Internal Packages

```text
SPEC.md                          public product contract (this file)
provisioning/SPEC.md             host installer + Ansible roles
cmd/, internal/                  active Go implementation
```

The active implementation lives in the root Go module. Ansible remains the
host convergence layer, but the CLI and privileged server API are both served
by the compiled Go `simple-vps` binary.

## Go Port Direction

The root Go module is the migration target for both sides of the product:
the public deploy CLI and the privileged host helper. New behavior should
land in Go first.

The legacy Bun CLI and Python helper have been removed from the active tree.
The cleanup target is one maintained implementation: Go plus the provisioning
shell/Ansible layer.

## Versioning

Standard SemVer.

```text
0.1.x    preview line, patch fixes only
         no contract changes

0.2.0    unified `simple-vps` CLI lands
         manifest renamed to `simple-vps.toml`
         no server layout / sudoers grant target / systemd changes
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

## 0.2.0 Scope (Historical)

This section records the shipped 0.2 TypeScript/Python milestone. The active
direction is the Go port described above.

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
- Internal server API staging and release markers (`/tmp/simple-deploy`,
  `.simple-deploy-success`).
- Sudoers grant target (still the `simple-vps` binary, one line).
- systemd unit naming (`simple-<app>-<service>.service`).
- The three build modes (A/B/C) and their detection.
- Per-app user + 2775 group-write contract.
- The privileged helper language. Python stdlib is good at the sudo
  boundary (small audit surface, no supply chain). Port only if there is a
  concrete reason, not for cohesion.
- The `install.sh` bootstrap flow.

### What stays out of 0.2.0

- Replacing the `install.sh` one-liner. The script stays as bootstrap glue.
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
  `provisioning/` stays.

## Future Architecture Candidates

No active candidates in this phase. The next work is hardening the Go installer
and release path while leaving Ansible in place.

What stays out of consideration:

- **Replacing Ansible.** Ansible is the right tool for host convergence
  (apt, systemd, UFW, sudoers, idempotent state). Rewriting that host
  convergence layer is months of work for marginal user-facing improvement.
  Ansible stays unless a concrete product reason appears.

The 0.3.0 slice was picked from real friction after 0.2.0 landed:
operator/deploy separation, Cloudflare setup, install prompts, manifest
simplification, and day-0 diagnostics.

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
7. Bump `packages/cli/package.json` to
   `0.2.0`.
8. Tag `v0.2.0`.

Each step is small enough to land as its own commit. Steps 1, 3, 4, 5 are
the load-bearing ones; the rest are docs and metadata.

## 0.3.0 Scope

0.3 closes day-0 gaps without hiding the box. Users still own the VPS; when the
host is unhealthy, the product response is clearer diagnosis and sharper errors,
not a dashboard abstraction.

What lands:

- Fresh installs split host identities into `operator` and `deploy`.
- Ansible host convergence runs as `operator`; app setup, deploy, route, and
  host read commands authenticate as `deploy`.
- `/etc/sudoers.d/operator` grants broad sudo to the operator user, while
  `/etc/sudoers.d/simple-vps` grants deploy only the server helper.
- `simple-vps host doctor` reports the legacy 0.2 `admin` conflation as
  degraded and the split model as healthy.
- Cloudflare Tunnel setup keeps the default trust boundary at tunnel token or
  config-file access; API-managed public hostnames and CNAME records are an
  explicit advanced mode, not the default path.
- `simple-vps host install` owns non-TTY flag/env behavior; `install.sh` is
  bootstrap glue.
- Manifest `path` becomes optional and defaults to `/var/apps/<name>`.
- Host status/doctor and route listing can target `--server <ssh-target>` from
  outside an app repo.
- `simple-vps init` inspects the repo before writing the manifest template.

Existing 0.2 hosts are not auto-migrated. They keep working, but doctor reports
the old `admin` shape as degraded until the manual migration in
[docs/0.3-operator-deploy-split.md](docs/0.3-operator-deploy-split.md) is done.

What stays out:

- Dashboard UI.
- Managed-resource abstractions.
- OAuth for Cloudflare or Tailscale.
- Multi-host orchestration.
- A TS/Bun privileged helper.
