# RFD-0006: Control plane — SSH pipe vs HTTPS gateway

**Status: decision record + revisit triggers.** Not a proposal to
build anything. Records the July 13, 2026 decision (Franco + Fable,
D12) to keep SSH as the control-plane transport, and names the
conditions under which the question reopens — so it is a recorded
fork, not a vibe to relitigate.

## The question

Ship's CLI reaches the box exclusively over SSH; members' keys are the
credential (fingerprint-resolved, v0.4.1). The alternative model —
studied concretely via OpenClaw's gateway (loopback bind +
`tailscale serve`, identity headers, or token auth over HTTPS) — runs
a resident daemon behind TLS and authenticates with tokens. Should
ship's control plane be a pipe or a gateway?

## What ship's model actually is

Not "members get SSH access". SSH is a **transport for the privileged
server API**: the agent role's shell is refused outright, identity
rides the authenticated key fingerprint, and the helper enforces
roles/approvals per verb. The primitive is *authenticated RPC over a
pipe* — the pipe is swappable without touching the primitives.

## Why SSH won (and wins today)

1. **Zero-account onboarding is load-bearing.** SSH is the only
   channel that exists pre-authenticated on every fresh VPS — the
   hosting provider already bootstrapped trust. `box setup root@ip`
   with no signups is a thesis, not a convenience.
2. **We don't hand-roll an auth system.** A token gateway means ship
   owns issuance, rotation, revocation, scoping, and a resident TLS
   endpoint — built by the same project that spent v0.4.0/v0.4.1
   fixing credential leaks in the much smaller preview surface. sshd
   is the most audited auth daemon alive.
3. **No third-party dependency in the core loop.** A tailnet-first
   control plane makes a Tailscale outage mean "can't deploy".

## Revisit triggers (any one reopens this RFD)

- The 443 door (RFD-0004, agentic wave) proves insufficient: if
  dressing the same SSH protocol for port 443 (cloudflared/sslh/
  proxytunnel) still leaves mainstream agent sandboxes unable to
  deploy, the pressure is structural, not cosmetic.
- Agents become the majority deployer and their platforms standardize
  on HTTPS-only egress with no tunnel escape.
- A control-plane feature genuinely needs request/response semantics
  SSH exec can't give (streaming bidirectional state, browser-origin
  calls).

If reopened, the migration path is: keep the server API and role
model, swap the pipe — a gateway would authenticate the same member
identities (keys or derived tokens) against the same helper
authorization. Nothing in today's design corners us.

## Related

- ADR-0013 (one topology; admin-plane hardening via config keys)
- ADR-0014 (box config foundation; the state/config surfaces a
  gateway would expose are being built transport-agnostic)
- RFD-0004 §443-door (the agentic wave's transport workaround)
