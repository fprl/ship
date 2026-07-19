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
