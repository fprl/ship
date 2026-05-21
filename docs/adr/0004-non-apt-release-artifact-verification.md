# ADR-0004: Non-Apt Release Artifact Verification

- **Status**: Accepted
- **Date**: 2026-05-21
- **Depends on**: ADR-0001 (bounded Go provisioner).
- **Related**: ADR-0002, ADR-0003.

## Context

Some host dependencies are not installed from apt repositories. Litestream is
installed from a pinned GitHub release `.deb`, and future helper binary
downloads may follow the same release-artifact shape.

A pinned version without a pinned digest still trusts the bytes returned by the
download endpoint at apply time. That is weaker than the apt repository policy
in ADR-0003, where key trust is deliberately pinned and rotations are explicit
code changes.

## Decision

Every non-apt release artifact installed by the host provisioner must include:

- exact upstream version
- exact download URL shape
- expected SHA256 digest for each supported architecture

The provisioner downloads the artifact into a private temporary directory,
verifies SHA256, and only then installs or executes the artifact. A checksum
mismatch is a hard apply failure. The artifact is not installed, and the apply
records failure state.

Litestream `0.5.8` is the first artifact covered by this policy.

## Rotation Policy

Version or digest rotation is a deliberate code change. The PR must update the
pinned version, all supported architecture digests, and the tests that prove a
mismatched artifact does not reach install.

If upstream republishes bytes for an existing version, do not silently replace
the digest. Treat it as a supply-chain event: verify the upstream reason, prefer
upgrading to a new immutable release when available, and document the reason in
the PR.

## Consequences

- Non-apt downloads now fail closed on unexpected bytes.
- New supported architectures require a pinned digest before install support is
  enabled.
- Helper release artifact verification should use the same policy when release
  downloads are added.
