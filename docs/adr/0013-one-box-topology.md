# ADR-0013: One Box Topology

- **Status**: Accepted (Franco, July 13, 2026 — decision D10)
- **Date**: 2026-07-13
- **Related**: `docs/ship-spec.md` §0.5/§4/§18, ADR-0014 (box config
  foundation — where hardening returns), RFD-0006 (control plane).

## Context

`box setup` carried a 3×2 topology matrix — `--ingress
public|cloudflare|private` × `--admin public-ssh|tailscale` — plus
five Cloudflare flags, three Tailscale flags, and `--litestream`
(which installed an unconfigured .deb: backup theater). Six
topologies, each a distinct security surface needing tests, doctor
coverage, and docs, before user #1. Thesis 5 says zero security
decisions; the matrix asked users to make two at setup.

The tunnel argument is real, not theater: Tailscale admin closes port
22 to the internet; a Cloudflare tunnel hides the origin. It was
weighed seriously (OpenClaw's loopback-plus-`tailscale serve` model
was studied as the best-in-class comparison).

## Decision

One shape: **public ingress 80/443 + keys-only SSH**. The topology and
litestream flags are deleted from `box setup`.

## Deliberately not built ("not Y, because")

- **Not** the tunnel topologies as setup flags, because:
  1. For a 1–5-person team, keys-only sshd is not the breach vector —
     app vulns and leaked secrets are; sshd is the most audited daemon
     alive, so the tunnel's marginal win is small for this audience.
  2. Each topology is a failure surface ship must own (expired tunnel
     tokens, third-party outages) — six half-tested shapes are less
     secure than one well-tested shape.
  3. Both tunnels put a third-party account signup inside day-0
     onboarding. Vercel-check: their wizard asks product questions,
     never security questions; the platform decided. Deleting the
     choice IS the Vercel move — a wizard is the nice UI for choices
     that shouldn't exist.
- **Not** OpenClaw's model wholesale (control plane tailnet-only),
  because ship's data plane exists to be public (deployed apps) and
  its control plane must work with zero accounts on any fresh VPS —
  SSH is the only channel that arrives pre-authenticated (RFD-0006).

## The way back in (recorded so it isn't relitigated)

Hiding the **admin plane** (SSH on the tailnet; apps stay public) is
genuinely stronger posture and returns as a single post-setup act —
one command, one tested path — implemented as a converge-mode key on
the box config foundation (ADR-0014), e.g. `harden.tailscale`. Its
real cost is stated then: every member joins the tailnet. Hiding the
**data plane** (private/cloudflare ingress) is an internal-tools story
ship does not tell yet; it stays parked. v0.6.0's 443 door (agent
sandboxes) will build tunnel plumbing once, properly — not as setup
flags.

## Consequences

`box setup <ssh-target>` asks nothing. The dogfood box (the testing
box) already runs this exact shape, so the one shape is the tested
shape. Cloudflare/Tailscale/litestream code paths (~150 references
for cloudflare alone) become deletable.
