# ADR-0003: Apt Repository Key Trust Policy

- **Status**: Accepted
- **Date**: 2026-05-21
- **Depends on**: ADR-0001 (bounded Go provisioner).
- **Related**: ADR-0002.

## Context

`EnsureAptRepo` installs third-party apt repositories for Caddy, Tailscale,
cloudflared, and optional Docker support. Before this ADR, the operation treated
an existing keyring path as trusted just because the file existed.

That is not a real trust policy. A stale, corrupt, or replaced key at the
expected path would make the provisioner converge while trusting the wrong
package signer.

## Decision

`EnsureAptRepo` owns repository signing-key trust. Any apt repo key managed by
the provisioner must include:

- key download URL
- keyring path used by the `signed-by=` source entry
- expected OpenPGP primary key fingerprint
- whether the downloaded key must be dearmored before installation

The operation verifies the fingerprint of an existing key before accepting it.
If the key is missing or has the wrong fingerprint, it downloads the key to a
temporary path, verifies the downloaded key, optionally dearmors it, verifies the
installed-form key again, and only then installs it into the keyring path.

A downloaded key with the wrong fingerprint is a hard apply failure. The source
list entry is not written and apt is not updated.

## Consequences

- Existing keyring files no longer count as trusted by path alone.
- Vendor key rotations are deliberate code changes because the expected
  fingerprint must be updated.
- Caddy uses `gpg --dearmor` to match the official package instructions.
- `gnupg` remains an essential host package because key verification is part of
  the bounded provisioner contract.
