# Positioning

simple-vps is a deploy primitive for solo developers and small teams who want
to run their own apps on a single VPS, with the security and operations work
done up front and out of the way. The user owns the VPS. simple-vps owns:
hardening the host once, deploying containerized apps onto it, managing
routes, secrets, and backups, and getting out of the way between commands.
There is no control plane, no dashboard, no managed services tier. The CLI
is the product.

This document is the design discipline reference: who simple-vps is for,
what is in scope, what is out, and the tests every new feature must pass.

## Audience

- **Solo developers and small teams** shipping their own apps to production.
- **Indie hackers** running one or more side projects on a $5–$20 VPS.
- **Engineers who self-host** because they value reproducibility,
  ownership, and a small bill — not because they want to spend weekends
  managing infrastructure.

Not the audience:

- Teams running enterprise compliance workflows on fleets of machines.
- Engineers who want a free Heroku — Coolify is closer.
- Anyone whose deploy story needs multi-host fleet management.

## In scope

- **One CLI** doing host hardening, app deploy, routing, secrets, and
  backups.
- **One VPS**, multiple apps, multiple envs per app.
- **Manifest in your git repo** as the source of truth.
- **Hardened by default**: firewall, deploy user, TLS via Caddy,
  locked-down systemd / Podman.
- **Ingress modes, not a provider matrix**: public Caddy as the
  direct VPS ownership path; Cloudflare Tunnel and Tailscale admin
  access as supported hardening modes for teams that want them.
- **Composable primitive**: state in flat files (per ADR-0002), CLI is
  the API, `--json` output on every read. Dashboards, schedulers, and
  multi-host coordinators are someone else's product (or our future
  product) built on top of this primitive.
- **Container runtime** via required Dockerfile (per ADR-0005), with
  static files as the one other shape.

## Out of scope

Not features we're going to add by accident. If any of these become
right for the product, they get their own design from scratch, not
feature creep into the current shape.

- **Multi-host fleet management.** simple-vps is single-host by
  design. When users outgrow that, they've outgrown this tool —
  multi-host is a different product shape, not a feature to bolt on.
- **Dashboard UI shipped by us.** The CLI is the product. A dashboard
  would be an *optional layer* built on the composable primitive — by
  the community, by a future paid offering, or by us if it ever
  earns its way in — not the answer to bare-CLI UX gaps.
- **Managed services tier** (managed Postgres, managed Redis, managed
  object storage). Those run as containers like everything else; the
  user owns the data and the lifecycle.
- **Built-in recurring scheduler / jobs primitive.** Framework
  schedulers (Sidekiq, Oban, Celery beat, BullMQ, Laravel scheduler)
  cover recurring work inside app stacks. System cron, systemd timers,
  or external schedulers cover host-level scheduling. simple-vps does
  not own the scheduler.
- **Git-push deploy.** Explicit `deploy` keeps the trigger surface
  controlled and scriptable.
- **Plugin system.** Extension happens through the composable
  primitive, not a sanctioned plugin API.

## Closest reference: Kamal

The clearest competitor and the right mental reference is
**Kamal** (37signals). Both are:

- CLI-driven, no control plane.
- Manifest-in-repo as the source of truth.
- Container-runtime-required.
- Designed for "I own this VPS, deploy my app to it."

What simple-vps does that Kamal doesn't:

- **Hardened-host installer baked in** (per ADR-0001). UFW, fail2ban,
  deploy user, Caddy, TLS — configured by us, not by the user. Kamal's
  host hardening (`kamal setup`) is opt-in and minimal; simple-vps's
  is default and comprehensive.
- **Static apps as a first-class shape.** No container required for
  static output; Caddy serves files directly.
- **Per-env first-class** in the manifest, not grafted on.
- **Backup and restore as a primitive.** "Fresh VPS, restore app to
  running" is a product bar, not a recipe (per ADR-0007).
- **Go binary** — no Ruby runtime required on the client.

What Kamal does that simple-vps doesn't (and won't):

- Multi-host deploys.
- Custom in-house proxy (kamal-proxy). simple-vps stays on Caddy —
  already small, no Traefik-grade complexity to escape from.

## Why not the other options

| Tool | What it offers an indie hacker | What simple-vps offers over it |
|---|---|---|
| **Coolify / Dokploy / CapRover** | Dashboard-first self-hosted PaaS: control panel, database-backed state, app templates, click-driven flows | CLI-first deploy primitive: repo-owned manifest, flat-file host state, JSON API, no dashboard process, no hidden UI-owned config |
| **Dokku** | Mature, Heroku-shaped, git-push, plugins | Modern Go binary instead of bash sprawl; hardened host included; no buildpack guesswork |
| **Compose + Traefik + scripts** | Standard Docker tools, fully under your control | Hardened host included; secret management included; route synthesis included; backup/restore included; not a script you have to maintain |
| **Kubernetes / k3s** | Real fleet management, autoscaling | If you need k8s, you are not the audience |

The honest answer to "why not Compose + Traefik + scripts": if you have
already written those scripts and they work, simple-vps probably is not
worth the migration. If you have not, simple-vps gives you a maintained
version of the same shape, plus the hardened-host installer most
self-hosters never get around to writing.

## Design discipline tests

Every proposed feature must pass these tests. Reviewers and ADR authors
should reference them by name.

### 1. Hold the Kamal line

> *"We're a primitive" can become "we ship UX through future layers
> that never materialize." The bare CLI must be a complete experience.
> Future control planes are optional layers, not the answer to bare-CLI
> UX gaps.*

Concretely:

- `status` ships rich output (services, ports, routes, last deploy,
  partial-deploy states, recent journal lines). Not "deploy succeeded."
- `logs` does sensible structured tailing. Not "here, run
  `journalctl -u ...` yourself."
- Error messages are actionable: `secret "db_url" not set for prod;
  run \`simple-vps secret put prod db_url\``. Not `missing reference`.
- "We will fix that in the dashboard" is not an allowed answer during
  implementation review.

### 2. Composable primitive

State is in files (ADR-0002). CLI is the API. `--json` output on
every read-only command. Someone — the user later, the community, a
paid SaaS — can build a dashboard, scheduler, or multi-host
coordinator on top of simple-vps without changing simple-vps.

This is a moat. The discipline cost is real (see ADR-0006:
additive-only schema changes, migration tooling shipped with the
binary, deprecation windows). The moat is only real if the discipline
holds.

### 3. Less complexity unless really needed

Every new feature has to pay for itself in user-visible value.
"Could be useful someday" is not justification. ADR-0005's
atomic-at-app swap, port allocator, and content-addressed
signatures were all cut in ADR-0006 on this test.

### 4. Single-host first

If a feature exists to make multi-host work better, it is the wrong
feature for this tool *as it is shaped today*. Multi-host is a
different design problem; if it ever becomes relevant, it warrants
its own product, not feature creep into this one.

## Evolution policy

- The CLI surface (SPEC.md), manifest schema, and state schema are
  versioned contracts. Pre-1.0 they may break between minors. Post-1.0
  they break only on majors, with the migration tooling and deprecation
  window committed to in ADR-0006.
- New ADRs amend or supersede prior ones explicitly. ADRs are not
  deleted; superseded ones get a header.
- The audience and the design discipline tests above do not change.
  New audiences are addressed by new products, not by scope creep
  here.

## Related documents

- [SPEC.md](../SPEC.md) — public CLI surface and manifest contract
- [docs/security-model.md](security-model.md) — host security posture and
  ingress / admin modes
- [docs/adr/0001-replace-ansible-with-bounded-go-provisioner.md](adr/0001-replace-ansible-with-bounded-go-provisioner.md)
  — hardened-host installer
- [docs/adr/0002-state-file-layout.md](adr/0002-state-file-layout.md)
  — state schema (the composable-primitive contract)
- [docs/adr/0005-container-runtime-via-required-dockerfile.md](adr/0005-container-runtime-via-required-dockerfile.md)
  — runtime pivot
- [docs/adr/0006-cuts-and-composability-commitments.md](adr/0006-cuts-and-composability-commitments.md)
  — cuts and clarifications on top of 0005
- [docs/adr/0007-backup-restore-primitive.md](adr/0007-backup-restore-primitive.md)
  — backup + restore primitive
