# ADR-0022: Automatic-First Convergence and Retention GC

- **Status**: Accepted (Franco, July 16, 2026 — blessed with RFD-0009's
  stage plan; landed as stage 5 of the state-diet arc)
- **Date**: 2026-07-16
- **Related**: RFD-0009 (the model this completes), ADR-0018/0019 (verb
  grammar), ADR-0021 (idempotent self-heal precedent).

## Context

RFD-0009's crash-only lifecycle made `active.json` the single commit
point and convergence the only recovery path: a deploy interrupted
after its commit leaves "committed but not converged", healed by
converging forward — never by restoring. That model needs three things
the codebase lacked: a way to converge without a laptop, a boot story
(the July 16 reboot probe measured a full outage: units written but
never enabled, and `unless-stopped` containers are not covered by
podman-restart), and something that cleans the debris crash-only
deploys deliberately leave behind.

The product bar shaped the answer: Vercel does not ship a "repair"
button — convergence there is invisible. Ship's equivalent is
**automatic-first**: the box repairs itself at the moments that matter,
and the verbs exist because ship's operators are often agents who need
a runnable `next:` step at 3am, not a timer to wait for.

## Decision

**Machinery first (automatic):**

- `ship-boot-converge.service` (oneshot, enabled by the provisioner,
  after network-online and podman) converges every env at boot. Apps
  come back after a reboot because the box brings them back, not
  because a container flag happens to fire.
- Post-crash self-heal: lifecycle verbs notice a committed-but-not-
  converged env and heal it as part of their normal work (generalizing
  ADR-0021's drift self-heal).
- `ship-gc.timer` (hourly after boot, then six-hourly) plus a GC pass
  after every successful deploy/rollback — replacing the ad-hoc
  post-deploy image prune. Retention is a fixed policy, not a chore:
  keep the active release plus the newest verified rollback candidates
  (5 for production, 2 for previews), each with its image and static
  release directory; 10-minute grace for fresh artifacts. Activation
  env files are runtime state, not rollback artifacts: only the active
  activation's frozen env is retained — rollback mints a fresh
  activation and re-resolves current secrets, so a non-active
  activation env is an unread copy of old secret values and is
  collected. Deletion of release artifacts requires positive proof: an
  unverifiable release is protected in full, and any journal
  uncertainty (torn tail, parse failure) skips the env's sweep
  entirely rather than shrinking retention.

**Verbs second (the escape hatch):**

- `ship converge` — app-scope, top-level like `ship status`; resolves
  app+env from ship.toml + branch; needs no local source (converges
  purely from box-side state); role-gated exactly like deploy. Every
  `committed_unconverged`/`committed_degraded` remediation and the
  status line point at it. A no-op converge journals nothing.
- `ship box gc [<box>]` — box-scope sweep on demand, same retention
  policy, prints and journals what it removed (GC entries are written
  only when something was removed, and `why` ignores them — `why`
  answers "what happened to my releases", never "the box swept").
- Helper verbs: `server app converge` (role-gated), `server gc`,
  `server converge-boot` (root-only, per-env tolerant: one broken env
  never blocks the rest at boot).

## Rejected

- **`ship box converge` as a separate verb**: `ship box setup` already
  is the stored-state box converge — it renders the member key file,
  sudoers, and the unit set from what the box knows, and enables what
  it writes. A second verb would duplicate that machinery without
  adding a distinct recovery operation. Revisit only if setup ever
  grows a mandatory interactive step.
- **Relying on podman restart policies for boot**: `unless-stopped` is
  not honored by podman-restart on this platform generation, and even
  `always` only restarts containers — it cannot re-render a fragment,
  reload Caddy, or notice a half-converged commit. Boot convergence
  subsumes it.
- **Configurable retention**: a knob would demand a schema key, docs,
  and validation for a decision with one sane answer at this scale.
  Fixed numbers; revisit with evidence.
- **GC deleting on absence of information**: a release the box cannot
  verify might be mid-upload, mid-build, or evidence of a bug. Keep is
  the only safe default; disk is cheaper than a lost rollback target.
