# ADR-0014: Box Config Foundation

- **Status**: Accepted (Franco, July 13, 2026 — decision D11; Franco
  explicitly pulled this forward rather than parking it)
- **Date**: 2026-07-13
- **Related**: `docs/ship-spec.md` §19 (normative surface +
  acceptance), ADR-0011 (bounded converge — reused for side-effect
  keys), ADR-0013 (hardening returns as a key here), RFD-0007 (backup
  keys land here), §17 (notify.url is the first key).

## Context

Box-level settings were about to sprawl: the notify webhook had its
own storage and verb; hardening (ADR-0013) and scheduled backups
(RFD-0007) would each have invented their own flags and files. The
OpenClaw configuration model (`openclaw.json`: one schema-validated
document, atomic writes, unknown keys refuse to start, `config
get/set/unset` with dotted paths) was studied as the scaling
benchmark — Franco's argument: a foundation where every future option
is a key "scales indefinitely", and it is cheap to lay while there are
two knobs instead of twenty.

The objection on record was the trust boundary: ship's box mutations
are privileged acts with role/approval semantics (§13); a generic
setter that mutates arbitrary state is a hole through that model —
OpenClaw affords it only because it is single-user software.

## Decision

Adopt the storage discipline, gate the mutation path:

- One canonical schema-validated config document on the box (atomic
  temp+rename writes, unknown keys refused, version field).
- The schema declares **per key**: type, default, write role, approval
  behavior, and apply mode (`none` | `converge`). Authorization lives
  in the schema — a generic `box config set` is safe here precisely
  because no key exists without a declared owner.
- Side-effect keys apply via the bounded-converge machinery
  (ADR-0011). No converge-mode key exists yet, so no converge plumbing
  is built yet.
- Existing and future verbs (`box notify`, future `box harden`) become
  sugar over keys — one storage, one journal shape, one introspection
  surface (`box config --json`, feeding the resident's state bundle,
  RFD-0002).

## Deliberately not built ("not Y, because")

- **Not** app-level config on the box — `ship.toml` in the repo stays
  the only app config: versioned, PR-reviewed, next to the code. The
  box has no app config file to drift; "the repo is the config" is a
  thesis, not an implementation detail.
- **Not** a setup wizard or `configure` flow — a key that needs a
  guided walkthrough is a decision ship should have made itself
  (thesis 5). OpenClaw needs wizards because channels/plugins/API keys
  are inherently interactive; ship's onboarding is two commands.
- **Not** hot-reload watchers or an RPC patch API — at this knob
  count, read-on-use plus explicit converge covers every key; revisit
  only if a key genuinely needs sub-second propagation.

## Consequences

Tailscale hardening, R2 backup destinations, reaper tuning — each
future box option is a schema entry plus (at most) a converge
handler, not a new flag/file/verb. The generic-setter security
question is answered structurally and doesn't need relitigating per
feature.
