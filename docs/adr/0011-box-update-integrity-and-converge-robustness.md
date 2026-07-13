# ADR-0011: Box Update Integrity And Converge Robustness

- **Status:** Accepted
- **Date:** 2026-07-13
- **Context:** ship-era, v0.4.0 security packet. Records why `ship box
  update` makes the box fetch its own release by version rather than
  accept uploaded bytes, and why the converge gets bounded robustness
  (lock + journal-first + skew detection) rather than transactional
  rollback.
- **Depends on:** ADR-0004 (non-apt release artifact verification).
- **Related:** ADR-0001 (bounded Go provisioner), RFD-0003 (members and
  roles), docs/security-model.md.

## Context

Two problems in the v0.4.0 `box update` path, surfaced by a zero-context
security hunt:

1. **Integrity.** The client built or fetched a Linux helper binary and
   **uploaded the bytes**; the box executed whatever it received. `box
   update` is owner-gated, but the agent role (RFD-0003) means an
   owner-approved agent could get one box-mutation approval and install
   an **arbitrary root binary**. The whole point of the agent role is that
   a compromised or misaligned agent key is bounded by its role; "upload
   any bytes, run as root" punched straight through that boundary. The
   agent era (RFD-0004) is exactly when this stops being theoretical.

2. **Robustness.** The converge held no box-wide lock, wrote its journal
   **after** mutating, and a killed update could leave a new helper next
   to old sudoers/timers while doctor still reported healthy.

## Decision

### Integrity — the box fetches by version, never accepts bytes

The client sends a **version string**; it never uploads an artifact. The
box downloads the official `ship-linux-<arch>` for that version and
verifies it against the release `SHA256SUMS` **before executing
anything**, reusing the artifact-verification machinery from ADR-0004
(the same code path `box setup` already trusts). The worst a rogue or
coerced agent can now do is move the box to a **genuine published
release** — never to bytes of its choosing.

- Released versions only. Dev / git-describe builds keep the existing
  `box_version_ambiguous` refusal and converge via `box setup` over owner
  bootstrap SSH — the agent/approval path never carries dev bytes.
- No downgrade, no equal re-install (semver comparator, ADR none / P3).
- Download+verify happen before any mutation: a failed or tampered
  download aborts having changed nothing.

**Considered and rejected:** *client uploads bytes, box verifies against a
checksum.* If the checksum comes from the same client it proves nothing;
if the box fetches `SHA256SUMS` itself it still needs the bytes it could
just download. Fetch-by-version is strictly simpler and removes the
attacker's byte channel entirely. This is the `deno upgrade` / `k3s` /
`flyctl` pattern, not an invention.

**Cost we accept:** the box needs outbound HTTPS at update time (it
already has egress for ACME, apt, and podman pulls). Same-origin checksums
do not defend against GitHub itself serving malicious assets — release
**signing** (minisign over `SHA256SUMS`) would. We leave a clean seam for
that and do not build it now: it is fleet-scale authenticity machinery,
unjustified for a 1–5 person tool whose trust root is already the GitHub
release.

### Robustness — bounded, not transactional

The box-side converge is wrapped in a **box-wide exclusive lock** (reusing
the existing flock primitive), the journal is written **first** (start
record before mutation, complete record after), and **doctor detects the
skew** (a started-without-completed journal, or a helper whose version
does not match its own installed artifacts) and points at a re-run.

**Considered and rejected:** *two-phase commit, rollback-to-previous
binary, resumable state machines.* The converge is **idempotent and
re-runnable by design** (ADR-0001 — a bounded provisioner that computes
and applies a diff). Given that, the honest failure story is "a killed
update is visibly incomplete and re-running finishes it," which lock +
journal-first + skew-detection delivers. Transactional rollback would add
a parallel binary-versioning and staging apparatus to protect an
operation you are told to simply run again — machinery guarding against a
cost the design already erased. If a real failure mode ever escapes
idempotent re-run, revisit then; not speculatively.

## Consequences

- The client/helper update contract changes shape: version-in, not
  bytes-in. Zero users exist, so the old upload protocol is replaced
  outright with no compatibility path; existing test boxes re-converge via
  `box setup`.
- The box gains a hard dependency on reaching the release host during
  `box update` (not during normal operation). Doctor's existing checks and
  the abort-before-mutation rule keep a network failure non-destructive.
- Doctor grows one check (partial/incomplete update). It shares the shape
  of the helper-version and doctor-timer checks and is only meaningful on
  manual runs / its timer / `box status` — acceptable, same window as the
  rest of doctor.
- The minisign-signing seam is documented but empty. The day ship grows an
  audience that warrants supply-chain authenticity beyond GitHub's TLS,
  that is the drop-in point.

## Related

- ADR-0004 (release artifact verification — the verify path reused here)
- ADR-0001 (bounded, idempotent provisioner — why re-run beats rollback)
- RFD-0003 (members and roles — the boundary this protects)
- docs/security-model.md (agent-role threat model)
