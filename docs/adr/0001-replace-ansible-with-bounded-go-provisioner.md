# ADR-0001: Replace Ansible with a Bounded Go Provisioner

- **Status**: Accepted
- **Date**: 2026-05-20
- **Supersedes**: implicit decisions in `SPEC.md` and `provisioning/SPEC.md`
  that kept Ansible as the host convergence tool.
- **Related**: ADR-0002 (state file layout), `docs/ansible-replacement-review.md`
  (rationale brief).

## Context

`simple-vps host install` currently shells out to `ansible-playbook` from the
user's laptop. That breaks the desired product flow: `curl -fsSL .../install.sh
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
cmd/hostinstall/        CLI surface - typed flags only
internal/provision/     plan/apply/status/doctor orchestration
internal/provision/remote/   SSH runner using OpenSSH CLI
internal/provision/host/     the bounded operation primitives
internal/provision/artifact/  verified Simple VPS server-helper artifact fetch
internal/provision/state/    /etc/simple-vps/*.json schemas (see ADR-0002)
```

### 2. Operation Budget - Exactly These Primitives

`internal/provision/host` exports exactly the following operations. Adding,
removing, or changing the contract of any of them requires a new ADR.

| Operation | Purpose |
|---|---|
| `EnsurePackage` | apt package install/absent |
| `EnsureAptRepo` | apt repo + GPG key + sources.list.d entry |
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

Twelve operations. No `EnsureCron`, no `EnsureMount`, no `EnsureSelinuxBoolean`,
no template engine, no plugin loader.

### 3. Operation contract

Every operation has the same signature:

```go
type Operation func(ctx Context) (changed bool, err error)
```

- `changed=true` iff the operation mutated host state.
- In check mode (`ctx.CheckMode == true`), operations return `(wouldChange,
  nil)` without writing.
- Errors halt the apply unless the caller wraps with explicit
  `ContinueOnFailure`.

Restart logic is written as plain Go conditionals against the returned
`changed` value:

```go
caddyConfigChanged, err := host.EnsureFile(ctx, caddyConfigPath, content)
if err != nil { return err }
if caddyConfigChanged {
    if _, err := host.EnsureSystemdUnit(ctx, "caddy", host.Reloaded); err != nil {
        return err
    }
}
```

No handler registry, no event bus, no notify table.

### 4. Server helper artifact

The laptop CLI is OS-specific. A Darwin binary cannot be copied to an Ubuntu
VPS and run as `/usr/local/bin/simple-vps`.

During `host install`, the Go provisioner installs a Linux `simple-vps` helper
binary on the target host. The default source is a verified release artifact
matching the local CLI version and target architecture. The provisioner may
reuse the local binary only when the local OS/arch matches the target.

This preserves the product trust boundary:

- `curl | sh` installs the local CLI only.
- `simple-vps host install` is the explicit infrastructure mutation and may
  download the matching Linux helper needed by the target host.

The helper artifact path/source can be overridden for local development and
CI, but the default product path must verify checksums before upload.

### 5. Check mode from day one

`simple-vps host install --check` and `simple-vps host doctor --check` must
work in v1. Every operation must support check mode at the same time it lands.
A PR adding an operation without check-mode support is incomplete.

### 6. Transport

Use the system OpenSSH CLI via `os/exec`, not a Go SSH library. OpenSSH already
handles agent, `known_hosts`, `~/.ssh/config`, `ProxyJump`, multiple identities,
and platform quirks. Re-implementing those would expand the audit surface
without product benefit. The `internal/provision/remote` package wraps OpenSSH
with a typed `Runner` interface; tests substitute a `FakeRunner` that records
typed operations.

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
| Node | NodeSource | pinned major LTS, app-driven install |
| Bun | official installer | pinned exact, app-driven install |

Language runtimes (Node, Bun, pnpm) are **not installed by default during
`host install`**. They install only when explicitly requested via
`--features=node22,bun` or equivalent. `simple-vps deploy` fails fast with a
"run `simple-vps host install --features=bun` to enable Bun on this host"
message if the runtime is missing. Surprise installs at deploy time are
explicitly excluded.

### 8. Migration plan

1. Land the Go provisioner behind a `--provisioner=go` opt-in flag. Default
   stays Ansible.
2. Bring the Docker fake-VPS smoke to parity:
   - fresh install (ingress `cloudflare`)
   - fresh install (ingress `public`)
   - rerun idempotency
   - `host doctor` after either install
   - `setup` + `deploy` against the resulting host
3. Flip the default to `--provisioner=go`. Keep the Ansible path one release.
4. Delete `provisioning/playbooks/`, `provisioning/roles/`,
   `provisioning/inventory/`, `provisioning/install.sh`, and Ansible-specific
   install paths in a separate cleanup commit, not folded into the replacement
   PR.

## Consequences

### What this enables

- One local CLI install. The `curl | sh` story holds for the laptop.
- No Python/Ansible runtime dependency on the VPS for core convergence.
- Tests can substitute the `Runner` and `EnsureFunc` interfaces, so apply
  sequences are exercisable without a real VPS.
- `--check` mode is first-class, not bolted on.

### What this gives up

- Ansible's `lineinfile`/`blockinfile`/`template` idempotency, hand-rolled in
  `EnsureLineInFile` / `EnsureBlockInFile` / `EnsureFile`. These will have
  edge cases (encoding, line endings, empty-file behavior) the Ansible modules
  have already handled. Plan: each primitive ships with table-driven tests
  covering at least the cases Ansible documents.
- Multi-distro support. Ubuntu 24.04 only. Adding RHEL or Debian-non-Ubuntu
  requires its own ADR and a different package implementation.
- Ansible's apt-cache amortization across a play. The provisioner runs
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
- Full `--check` parity with Ansible (per-task diff output, fact gathering,
  free-form `command:` execution).
- A declarative DAG of operations with dependencies. Apply is a linear
  sequence in Go code.
- `ansible-vault`-equivalent secret storage. Secrets live under
  `/etc/simple-vps/secrets/` (see ADR-0002).
- Multi-host orchestration. One apply targets one host.

## Notes

- `docs/ansible-replacement-review.md` contains the original rationale and the
  three-way review (laptop author, Claude, ChatGPT) that fed this ADR. Once
  this ADR lands, that brief should either be archived or its open questions
  removed.
- The `(changed bool, err error)` contract is the single design decision most
  likely to age well: it forces every primitive to answer "did this matter?"
  before the caller decides what to do next.
