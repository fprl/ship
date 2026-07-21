# Domain context

## Remote protocol

The remote protocol is Ship's private client-to-box interface. It owns the
version contract, internal command vocabulary, request metadata, and wire
payloads shared by the local CLI and the box helper. SSH transports the
protocol; helper commands execute it.

Normal remote requests require an exact client/helper version match. The
repair surface (`server version` and `server update`) remains available during
version skew so `ship box update` can restore the invariant.

## Deploy ingest

A deploy is one framed remote request, not a directory that the client stages
on the box. The client sends the exact bundle size and digest, then streams a
bundle containing only `source.tar` and the authoritative `ship.toml`.

The helper creates a private random directory, verifies and extracts the
bundle there, applies the release from that directory, and removes it before
returning. The internal apply module never accepts client-chosen host paths.

## Addressing

Addressing is the deterministic route plan for one app environment. It owns
route synthesis, preview collapse, TLS overlays, preview aliases, primary URL
ranking, and sslip.io fallback naming. Client deploy output and helper runtime
variables consume the same plan and ranking rule.

Preview identity remains box-owned state. Addressing may plan routes for the
resolved environment, but it never invents or trusts a local preview suffix.

## Deploy outcomes

A deploy outcome records whether active intent was committed, whether the
entry retains an artifact candidate, and whether convergence is required.
`failed` is strictly pre-commit. `committed_unconverged` and
`committed_degraded` are committed outcomes and never trigger automatic
rollback. History, webhook recovery, and CLI explanations share this
classification.

The JSON strings are a closed journal vocabulary. Artifact retention still
requires independent tuple verification; an outcome is evidence, not trust.

## Podman runtime

The Podman runtime translates Ship intent into Podman CLI behavior. It owns
the container security floor, build/process arguments, exact image-ID
normalization, physical-image/tag grouping, normalized inventory, and typed
lifecycle mutations. Real Podman execution and deterministic test runners are
adapters behind that seam.

Caddy administration, provisioning, streamed logs, and interactive exec are
separate adapters. They may execute Podman as transport, but do not own or
duplicate Ship runtime policy.
