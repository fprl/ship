# RFD-0008: Member Invite Flow

- **Status**: Parked (post-1.0, demand-driven). Sketched July 14, 2026
  from the ADR-0019 onboarding consult.
- **Related**: ADR-0019 §4 (current onboarding: literal keys +
  raw-URL show-first/digest-confirm), spec §13 (members and roles),
  RFD-0003 (members and roles origins).

## The itch

Onboarding today requires the OWNER to obtain the teammate's key
(file, or a forge keys-URL that authorizes whatever set of keys the
forge serves). The cleaner shape — what a managed platform would do
with an invite link — lets the TEAMMATE enroll exactly the key they
will use, named at enrollment, with the owner only minting an
invitation:

```
owner:     ship box member invite [<box>] [--role shipper]
           → prints a one-time token (and the join command)
teammate:  ship box join <box> --name <name>
           → token read from stdin/terminal, NEVER argv
           → enrolls the teammate's active ship public key
```

No key fetching, forge-agnostic, the enrolled key is by construction
the key that will be used.

## Why this is parked, not built

An invitation is an **unauthenticated bearer credential that creates
a new identity**. That is the opposite trust assumption from
approvals (which authorize one retry by an already-authenticated
member) — so the approvals machinery must NOT be reused, only its
storage patterns (hashed-at-rest, atomic single-use consumption,
expiry, journaling).

The hard part is transport: an unknown SSH key cannot reach today's
SSH-only helper at all. `join` therefore needs a new enrollment
channel — a TLS-authenticated endpoint that accepts exactly one
message shape, or another bootstrap path. That is a new trust seam on
the box (spec thesis: zero security decisions — a new listener needs
to be the box's decision, hardened once, not a knob).

## Requirements recorded from the consult

- Token: high entropy, short expiry, hashed at rest, atomic
  single-use, bound to the FIRST fingerprint presented; replay races
  and crash recovery handled atomically.
- The invite fixes the role; the joiner cannot elevate it.
- Member names box-globally unique (a duplicate name must not let
  `member rm` revoke unrelated keys).
- Token never in argv (shell history / process listing); stdin only.
- Enrollment endpoint must present verifiable TLS; no insecure
  fallback, ever.
- Journaled like every trust mutation.

## Triggers to unpark

Real teams onboarding regularly; or the resident/serve protocol
(RFD-0002, RFD-0006) landing a TLS door on the box that this flow
can ride instead of minting its own listener.
