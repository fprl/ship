# ADR-0024: Concentrate protocol, addressing, outcomes, and Podman policy

## Status

Accepted

## Context

Ship had strong deploy-bundle and artifact modules, but several important
facts were still repeated across client and helper files: remote command
exposure, member identity injection, route ranking, deploy outcome meaning,
and Podman JSON/tag behavior. Exact client/helper lockstep means these facts do
not need a compatibility framework, but they do need one authority.

## Decision

- `internal/remoteprotocol` is a closed command catalogue. Kong remains the
  parser/dispatcher adapter and SSH remains the transport adapter. There is no
  generic RPC registry.
- `internal/deployrequest` owns deploy metadata and validation without host
  paths. Helper-owned private ingest paths never enter the request.
- `internal/addressing` owns route planning and primary URL selection.
- `internal/deployoutcome` owns the closed journal outcome vocabulary and its
  committed, recovery, and retention classifications.
- `internal/podmanruntime` owns Ship's Podman policy and normalization. Caddy,
  provisioning, logs, and interactive exec remain distinct adapters.

Each module exposes domain input and typed results. Command syntax, JSON
quirks, shell quoting, and CLI mechanics stay behind the relevant seam.

## Consequences

Changing a remote verb, route-ranking rule, outcome meaning, or Podman
normalization now has one primary implementation and one focused test surface.
The codebase gains several internal packages, but deletes more duplicated
policy than it adds. New operations remain compile-time additions; there is no
plugin or backwards-compatibility layer.
