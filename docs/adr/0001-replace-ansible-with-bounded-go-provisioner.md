# ADR-0001: Replace Ansible with a Bounded Go Provisioner

- **Status**: Accepted
- **Date**: 2026-05-20
- **Supersedes**: earlier implementation notes that kept Ansible as the host
  convergence tool.
- **Related**: ADR-0002 (state file layout).

## Context

`simple-vps host install` previously shelled out to `ansible-playbook` from the
user's laptop. That broke the desired product flow: `curl -fsSL .../install.sh
| sh` should install one local CLI, not a CLI plus Ansible plus playbooks plus
roles.

Three options were considered:

1. **Keep Ansible, install it on the host.** The Go CLI uploads playbooks and
   invokes `ansible-playbook` over SSH on the target. Laptop has zero Ansible.
   Rejected: still ships playbooks + roles + Python/Ansible on the VPS for
   convergence the rest of the product does not need, and keeps a magical layer
   between the CLI and the host.
2. **Bundle Ansible into the CLI release.** Embed playbooks and shell out to a
   vendored `ansible-playbook`. Rejected: same magical layer, plus binary-size
   and license-surface cost.
3. **Replace Ansible with a bounded Go provisioner.** Chosen by this ADR.

The convergence surface Simple VPS actually needs is small: ~50 well-known
operations on Ubuntu 24.04. The general-purpose machinery Ansible offers
(facts engine, handlers, inventory language, multi-distro module abstraction,
plugin system) is not load-bearing for this product. Rebuilding all of it would
be a mistake; rebuilding none of it would ship a worse provisioner. The
question is where the line goes, and this ADR draws it.

## Decision

### 1. Package shape

```text
cmd/hostinstall/        CLI surface and remote bootstrap over OpenSSH CLI
internal/provision/     plan/apply/status/doctor orchestration
internal/provision/host/     the bounded operation primitives
internal/provision/local/    on-host runner for local apply
internal/store/         /etc/simple-vps/*.json schemas (see ADR-0002)
```

### 2. Operation Budget - Exactly These Primitives

`internal/provision/host` exports exactly the following operations. Adding,
removing, or changing the contract of any of them requires a new ADR.

| Operation | Purpose |
|---|---|
| `EnsureDirectory` | directory create/update with owner, group, mode |
| `EnsurePackage` | apt package install/absent |
| `EnsureAptRepo` | apt repo + pinned GPG key fingerprint + sources.list.d entry |
| `EnsureUser` | system user with shell/home/uid policy |
| `EnsureGroupMembership` | user-in-group |
| `EnsureSudoersFile` | a single `/etc/sudoers.d/*` entry, `visudo -c` validated |
| `EnsureLineInFile` | one idempotent line, anchored by regex |
| `EnsureBlockInFile` | one idempotent block, anchored by markers |
| `EnsureFile` | atomic write with owner, group, mode |
| `EnsureUfwRule` | one ufw allow/deny rule |
| `EnsureSystemdUnit` | unit file, daemon-reload, service state, and caller-gated reload/restart |
| `EnsureTimezone` | one-shot `timedatectl set-timezone` |
| `EnsureLocale` | one-shot `localectl set-locale` |

Thirteen operations. No `EnsureCron`, no `EnsureMount`, no `EnsureSelinuxBoolean`,
no template engine, no plugin loader.

### 3. Operation contract

Every operation accepts `host.Apply` plus typed arguments and returns
`(changed bool, err error)`.

- `changed=true` iff the operation mutated host state.
- In check mode (`apply.CheckMode == true`), operations return `(wouldChange,
  nil)` without writing.
- Errors halt the apply unless the caller wraps with explicit
  `ContinueOnFailure`.

Restart logic is written as plain Go conditionals against the returned
`changed` value:

```go
caddyConfigChanged, err := host.EnsureFile(apply, host.File{
    Path: caddyConfigPath, Content: content, Owner: "root", Group: "root", Mode: 0644,
})
if err != nil {
    return err
}
if caddyConfigChanged {
    if _, err := host.EnsureSystemdUnit(apply, host.SystemdUnit{Name: "caddy.service", Action: host.Reloaded}); err != nil {
        return err
    }
}
```

No handler registry, no event bus, no notify table.

### 4. Server helper artifact

The laptop CLI is OS-specific. A Darwin binary cannot be copied to an Ubuntu
VPS and run as `/usr/local/bin/simple-vps`.

During remote `host install`, the laptop CLI builds or finds Linux
`simple-vps` helper binaries, uploads the binary matching the target
architecture, and executes `simple-vps host install --mode local` on the
target. During local install, the current binary is installed as
`/usr/local/bin/simple-vps`.

This preserves the product trust boundary:

- `curl | sh` installs the local CLI only.
- `simple-vps host install` is the explicit infrastructure mutation and may
  build or upload the Linux helper needed by the target host.

Release artifact download and checksum verification can be added later without
reintroducing a second convergence engine.

### 5. Check mode from day one

`simple-vps host install --check` and `simple-vps host doctor --check` must
work in v1. Every operation must support check mode at the same time it lands.
A PR adding an operation without check-mode support is incomplete.

### 6. Transport

Use the system OpenSSH CLI via `os/exec`, not a Go SSH library. OpenSSH already
handles agent, `known_hosts`, `~/.ssh/config`, `ProxyJump`, multiple identities,
and platform quirks. Re-implementing those would expand the audit surface
without product benefit.

Remote install is bootstrap-only: `cmd/hostinstall` builds or finds Linux helper
binaries, detects the target architecture with `ssh`, copies the matching helper
with `scp`, then runs `simple-vps host install --mode local` on the VPS. The
bounded provisioner executes on the target through `internal/provision/local`.
Tests substitute the `host.Runner` interface at that local apply boundary and
record typed operations there.

### 7. Dependency policy

Package versions live in `host.json` under `desired.packages.<name>` (see
ADR-0002). The provisioner records observed versions alongside.

| Package | Source | Version policy |
|---|---|---|
| Caddy | Caddy apt repo | stable track |
| Tailscale | Tailscale apt repo | stable track |
| cloudflared | Cloudflare apt repo | stable track |
| Litestream | GitHub release `.deb` | pinned exact |
| Docker | Docker apt repo | stable track, opt-in only |
| Node | external host prerequisite | app-driven requirement |
| Bun | external host prerequisite | app-driven requirement |

Third-party apt repositories verify their signing keys by pinned OpenPGP
fingerprint before the `signed-by=` source entry is trusted. See ADR-0003.

Language runtimes (Node, Bun, pnpm) are **not installed by default during
`host install`**. In the current product shape they are explicit host
prerequisites: `simple-vps deploy` fails fast when the manifest runtime or
lockfile requires a missing host tool (`node`, `npm`, `bun`, `pnpm`, or
`yarn`). Surprise installs at deploy time are explicitly excluded. A future ADR
can add first-class `host install` runtime feature flags once version and source
policy are pinned.

### 8. Cutover plan

This project is pre-user. There is no compatibility window and no legacy
provisioner flag. The Go provisioner is the only host convergence engine.

Cutover is complete when:

1. `simple-vps host install` uses Go primitives in local and remote mode.
2. `/etc/simple-vps/*.json` state follows ADR-0002.
3. CI and Make targets exercise only the Go installer path.
4. Ansible playbooks, roles, inventories, and install scripts are deleted.
5. Fake-VPS coverage includes fresh install, rerun idempotency, `host doctor`,
   `setup`, and `deploy`.

## Consequences

### What this enables

- One local CLI install. The `curl | sh` story holds for the laptop.
- No Python/Ansible runtime dependency on the VPS for core convergence.
- Tests can substitute the `Runner` and `EnsureFunc` interfaces, so apply
  sequences are exercisable without a real VPS.
- `--check` mode is first-class, not bolted on.

### What this gives up

- Mature module idempotency such as `lineinfile`/`blockinfile`/`template`,
  hand-rolled in
  `EnsureLineInFile` / `EnsureBlockInFile` / `EnsureFile`. These will have
  edge cases (encoding, line endings, empty-file behavior). Plan: each
  primitive ships with table-driven tests covering the expected product cases.
- Multi-distro support. Ubuntu 24.04 only. Adding RHEL or Debian-non-Ubuntu
  requires its own ADR and a different package implementation.
- Apt-cache amortization across a full play-style run. The provisioner runs
  `apt update` at most once per apply, tracked via a per-apply flag.

### What becomes harder

- Adding new convergence behavior. The operation budget is the forcing
  function: a thirteenth primitive is an ADR conversation, not a PR. This is
  intentional. It is the line that prevents "we slowly rebuilt Ansible."
- Drift detection beyond what `host doctor` reports. The provisioner is
  convergent, not declarative-with-reconciliation. A cron'd `host doctor`
  catches drift; the provisioner does not run unattended.

## Out of scope (do not litigate later without a new ADR)

- Multi-distro support.
- A plugin or module system.
- Per-task diff output, fact gathering, and free-form remote command
  execution.
- A declarative DAG of operations with dependencies. Apply is a linear
  sequence in Go code.
- Vault-style secret storage. Secrets live under `/etc/simple-vps/secrets/`
  (see ADR-0002).
- Multi-host orchestration. One apply targets one host.

## Notes

- The `(changed bool, err error)` contract is the single design decision most
  likely to age well: it forces every primitive to answer "did this matter?"
  before the caller decides what to do next.
