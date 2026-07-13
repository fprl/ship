# ADR-0015: One Preview Capability

- **Status**: Accepted (Franco, July 13, 2026 — decision D13)
- **Date**: 2026-07-13
- **Related**: `docs/ship-spec.md` §15 (normative surface +
  acceptance, rewritten), §5 (stdout contract), RFD-0004 (Agent
  Badges — the parked per-member evolution).

## Context

v0.4.0 shipped preview protection as three credentials behind an
opt-in manifest knob: a team password (humans type it), an automation
bypass token (header), and a per-preview share link — three
lifecycles, a rotate matrix, and an unmade protected-by-default
flag-day decision. Zero users had adopted it.

Vercel-check, done honestly: Vercel runs four mechanisms (SSO,
password add-on, share links, automation bypass), but its *primary*
mechanism makes protection invisible — a team member clicks a preview
and is simply in, because SSO recognizes them. The password/bypass/
share trio inherited Vercel's enterprise matrix without the SSO that
makes it bearable.

## Decision

Previews are **always protected** (no knob) by **one capability token
per preview** — possession of the URL is access, which is the
no-accounts equivalent of SSO invisibility:

- `ship` prints the capability URL on stdout for preview deploys; the
  same token works as URL param (cookie-set + redirect-strip — the
  shipped share-link machinery, kept), and as an automation header.
- `ship preview share [--rotate]` is the entire credential surface.
- Team password, bypass token, share mint/revoke, the `[previews]`
  knob, and the `previews_not_protected` error are deleted outright.
- Leak response is one flow: rotate. CLI holders self-heal
  (`ship status`); externals get a fresh link — the desired outcome.

## Deliberately not built ("not Y, because")

- **Not** protected-as-an-option — an unprotected work-in-progress
  URL is a leak waiting, and with capability URLs protection has zero
  UX cost, so the knob bought nothing but a flag-day question. The
  open "protected-by-default flip" decision dies with the knob.
- **Not** separate team vs guest credentials — at 1–5 people, rotate
  is cheap for the team (they have the CLI) and correct for guests.
  The one-token design is the N=1 case of "a preview has named
  capabilities": if guest-vs-team revocation is ever needed,
  `--name` mints a second capability — additive, no redesign.
- **Not** per-member auth / Agent Badges / TTL self-expiry — parked
  in RFD-0004; they layer on top of capabilities without conflict.
- **Not** a truly-public preview escape hatch — rare need, real leak
  surface; park until asked.

Accepted trade, on record: the token rides in URLs (browser history,
referrer) — mitigated by cookie-then-strip, and it is the same trade
Vercel/Figma/Docs share links make. Rotation invalidates everyone at
once — fine at team size, escape hatch designed above.

## Consequences

One credential concept instead of three; the preview namespace becomes
exactly `share | pin | unpin`; §5's "stdout is the URL" now hands
agents and humans a working link with zero extra steps.
