# ADR-0023: Artifact-Keyed Trust — One Check for Release Artifacts

- **Status**: Accepted (Franco, July 17, 2026 — v0.9.0 design gate;
  independent sol consult concluded in explicit agreement after four
  rounds plus a deletion sweep)
- **Date**: 2026-07-17
- **Related**: ADR-0022 / RFD-0009 (the activation model this builds
  on), the v0.8.0 image-immutability guard (which this deletes and
  supersedes), ADR-0017 (quality doctrine).

## Context

Four verbs — rollback, `box gc`, `exec`, and status/context loading —
each answered "is this old release safe to run, keep, or delete?" with
their own hand-written lookup ladder over the same artifacts (deploy
journal, activation pointer, envelope sidecars, image labels). The
class had receipts: the v0.8.0 arc shipped a rollback-discovery
regression that existed only because discovery was bespoke to rollback;
rollback truncated committed history to the retention window *before*
verifying candidates while GC verified *before* retaining, so the two
verbs demonstrably disagreed when the newest candidates were broken;
and the accepted residues of the last hunt (explicit rollback resolving
through a non-newest journaled envelope, GC counting debris) existed
only because each ladder invented its own source order.

Underneath all four ladders sat one root cause: **release-keyed,
mutable references**. The image tag was derived from the release name,
so a rebuild would overwrite committed bytes — which forced the v0.8.0
`planContainerBuild` guard (refuse `--rebuild` on committed releases,
configuration-identity comparison, envelope adoption) as a workaround.
Podman already content-addresses every image with an immutable ID; ship
was hand-guarding a mutable name instead of using the primitive.

Doctrine applied throughout (Franco, July 16): use what primitives
already give, cut ceremony, and build additions only where they
genuinely improve the product.

## Decision

**Three identities, kept separate.** A *release* is source identity (the
commit-derived id). An *artifact* is exact deployable bytes plus the
envelope describing them. An *activation* is one runtime selection of
an artifact with freshly resolved env. A release can have many
artifacts (rebuilds); an artifact can have many activations (secret
rotations, retries).

**The tuple is the identity — no invented hash.** An artifact is named
by the plain tuple `{release, image_id?, envelope_hash?, static_hash?}`
stored as named JSON fields; struct equality keys dedup and retention;
resolve compares field by field and fails closed. There is no derived
"artifact ID", no canonical binary encoding. Display identity is
`release@image_id[:12]` (container), `release@static_hash[:12]`
(static-only), `release@image[:12]+static[:12]` (hybrid — both parts,
because hybrids can share an image while differing in static bytes).
`envelope_hash` exists only for static-only artifacts, where it selects
the exact envelope sidecar (full hash in the sidecar filename); for
image-bearing artifacts the pinned image ID already binds the envelope
label, so the field is dropped.

**Runtime runs exact image IDs.** Every path that starts or inspects a
container — deploy, converge, boot self-heal, rollback, exec, probes —
uses the immutable podman image ID recorded in the pointer/journal,
never a name. Each committed image also carries one never-reused tag,
`ship/<infra>:img-<full-64-hex-image-id>` (full ID: a truncated tag
could collide and silently reassign), whose only jobs are to keep
dangling-image pruning away from committed bytes and to mark ship
ownership. Tags are not the registry; the journal is.

**`--rebuild` on a committed release becomes legal.** A rebuild mints a
new artifact (new image ID, new tuple); committed bytes are never
touched. Same-configuration redeploys reuse the previous artifact
whole, envelope included. The v0.8.0 guard — `planContainerBuild`'s
refusal/adoption dance, `releaseArtifactsCommitted`,
`imageTagConfirmedAbsent`, and the `release_immutable` error — is
deleted, not generalized: immutability is now a property of the
reference, not a policy bolted onto a mutable one.

**Static trees get the same cure.** A static release lives at
`releases/<release>-<full-static-hash>/` — the content hash in the
directory name (full hash; a truncated key could collide) makes the
reference immutable the same way the image ID does: a rebuild with
different site bytes gets a different directory, and same-content
redeploys reuse the existing one. `static_hash` is a single root hash
computed once at prepare over a sorted, length-delimited listing of
(relative path, size, content sha256) per file; prepare rejects
absolute or tree-escaping symlinks, control characters in paths, and
special file types; permissions are normalized before hashing; envelope
sidecars live outside the hashed payload. The hash is verified at
exactly three points — while building the shared rollback/GC candidate
set (hash static candidates until N verify), on the rollback target
immediately before commit, and immediately before GC deletes a
committed static tree (mismatch = protect). Boot, status, and converge
never hash trees.

**One trust seam, two operations.** `CommittedHistory(app, env)` reads
pointer + journal and returns the committed artifact tuples, untruncated
and artifact-keyed (repeated releases stay distinct).
`ResolveArtifact(app, env, tuple)` verifies one exact artifact — inspect
the pinned image ID, decode and validate its envelope label (or the
hash-named sidecar for static-only), require every declared artifact
present with exact identity — and returns the parsed context plus exact
runtime refs. The resolver never discovers: no "any image with that
release", no plain-sidecar fallback, no journal bypass. Podman scans
remain GC inventory only, outside the trust seam.

**Rollback and GC share one candidate policy.** Both build the same
ordered set: full committed history, dedup by tuple, resolve every
candidate *before* retention, exclude the exact active artifact (not
its whole release), keep the newest N verified. Broken newer artifacts
do not consume quota. The verbs may differ only in safety polarity: GC
*protects* unverifiable committed refs (deletion needs positive proof)
and grace-period debris, while rollback does not *offer* them. Implicit
rollback selects the newest verified non-active artifact — including a
same-release artifact after a bad rebuild — and rollback remains the
recovery verb when the active artifact itself no longer resolves.
Explicit `rollback <release>` picks the newest verified retained
artifact of that release and prints the chosen identity before
committing. GC deletes physically by reachability: an image ID is
removable only when no kept/protected/grace artifact references it and
no container uses it (`rmi` without force); a static tree is keyed by
its exact directory, not by hash equality across releases.

**Per-verb contract when the active artifact fails to resolve**: exec
fails closed printing the identity and error; status exits zero and
reports the degraded truth (`artifact_unavailable`, never claiming
verification), box-wide status continues past the env; converge
resolves before touching containers or Caddy and fails leaving runtime
untouched (journaled `committed_unconverged`, `failing_step=resolve`);
boot self-heal skips env-local artifact failures but aggregates
infrastructure failures (podman down, global I/O) so systemd retries;
GC protects and moves on. Env-local corruption is permanent and needs a
redeploy; infra failure is transient and deserves a retry.

**Flag-day, one redeploy heals.** Pointer v2 records the tuple and the
activation. The first v2 deploy starts a fresh v2 journal (the v1 file
is deleted; schemas never mix) and does not backfill the v1 pointer.
Until an env is redeployed: containers and Caddy keep serving; exec and
converge refuse with redeploy guidance; status shows observed runtime
tagged `legacy_activation` without verification claims; boot and GC
skip the env; rollback has no candidates. Pre-cutover releases lose
rollback eligibility — the same contract as the RFD-0009 flag-day. The
old trust path is deleted immediately, not kept for the interim.

## Deliberate deviations (argued against sol, resolved in agreement)

- **No derived ArtifactID.** Sol initially specified a domain-separated
  binary encoding hashed into a stored-and-recomputed ID; its own cut
  analysis conceded removal is "no semantic loss". Ceremony; the tuple
  is the identity.
- **No journal backfill before pointer moves.** Sol initially proposed
  repairing the journal tail and backfilling the active tuple before
  every pointer replacement. Accepted residue instead: a crash between
  pointer publication and the success append can cost that artifact its
  rollback eligibility (it later collects as ordinary residue). Ship
  still never runs or offers an uncommitted artifact; the mechanism
  wasn't worth the rare, mild window.
- **No content-addressed static store.** Sol's full design shared
  static trees by hash under `static/artifacts/<hash>/` with manifest
  files and cross-release reachability. Accepted residue: equal trees
  from different releases duplicate disk. The full hash in the
  release-scoped directory name buys the immutability without the
  relayout.
- **No selector grammar.** Deterministic newest-verified selection plus
  printing the chosen identity is honest for a single-tenant tool. If
  arbitrary variant selection is ever needed, the syntax must key on
  image/static identity, not envelope hash (rebuilds can share an
  envelope).
- **Sol's deferral options rejected.** Sol offered "keep the v0.8.0
  guard temporarily" and "keep the guarded release tag as retention
  root" as diff-halving cuts. Rejected: both preserve the wart the arc
  exists to remove; the arc lands whole.

## Also deleted in the same flag-day (ceremony/defensiveness sweep)

A dedicated sweep of the arc's blast radius (sol, July 17; every finding
verified in code before adoption) rides the same change so these files
are touched once. Themes, not an exhaustive list:

- **Fake filesystems for real data**: context loading materializes a
  temp directory with a fabricated `ship.toml`, a `FROM scratch`
  Dockerfile, and placeholder static dirs just to reuse the source-tree
  config loader on an already-validated envelope. `ResolveArtifact`
  loads the committed manifest in memory; the temp-dir machinery,
  placeholders, and cleanup-callback plumbing go.
- **Rollback validated the thing it recovers from**: the current
  active envelope was loaded and hash-checked before candidate
  discovery, so a broken active artifact blocked the recovery verb.
  Rollback now reads the pointer only for identity and strictly
  resolves only the target.
- **Read-once discipline**: one locked operation read `active.json` up
  to five times (and exec could pair an old image with a newer env
  file). The pointer is read once per operation and the tuple passed
  down; callers stop re-filtering and re-hashing what the resolver
  already verified.
- **Defenses against the impossible**: collision retries for
  ship-generated activation IDs (the filesystem's exclusive-create is
  the primitive); `chown root:root` subprocesses on files root just
  created; a mutex+map deduplicating a warning print in a
  single-threaded CLI; guards on empty output from successful `id`/
  `podman ps`; bounds-checking a fixed-length id; a derived
  `ship.infra_id` label re-checked against the app/env labels it is
  computed from; fatal-error plumbing on a decoration helper that
  returns nil on every path.
- **Primitives over hand-rolled machinery**: `podman run --replace`
  instead of preflight `rm -f`; root ownership by inheritance instead
  of explicit chown; deferred close instead of close-error joining
  after a successful fsync; the static prepare collision lattice
  dissolves into the hash-named directory.
- **Dead weight**: uncalled process/container helpers, the never-set
  `BeforeProcess` hook, an always-empty container-removal channel in
  the apply path, duplicated journal field stamping, duplicate
  freshness stats in GC, a needless history slice copy, and the
  `WriteResult` wrapper stack (one typed published-write error stays).

Nuances pinned during verification: status keeps its root-existence
check on the active static tree (degradation reporting is not
verification); the always-empty removal channel is specific to the
container-apply path — the identically named field in the apply result
is live.

## Platform-primitive audit (subsystem level, sol July 17 — agreed table)

Adopted in-arc:

- **Caddy load-first transactions.** Caddy's admin reload is already an
  atomic validate-apply-or-rollback transaction; ship was re-implementing
  it with fragment snapshots, restore-on-failure, and `.loaded` receipts.
  New uniform algorithm on every route change (deploy, converge, destroy,
  preview reap): assemble the full candidate config in scratch (the
  existing validation-tree renderer; removal uses it with the target
  fragment omitted) → reload Caddy from the candidate, unconditionally —
  Caddy no-ops identical configs → publish or delete the fragment on disk
  only after Caddy accepted. Never publish-then-reload: that recreates
  the disk-ahead-of-live window. A crash between reload and publish
  leaves live ahead of disk; the next converge re-assembles, no-op
  reloads, and publishes. Receipts, snapshot/restore, and the
  restore-error plumbing are deleted; the box-wide reload lock and
  deploy's pre-pointer-commit validation stay. During an incomplete
  destroy, ordinary convergence may legitimately restore the
  still-committed route until the destroy retries.
- **Podman inspect as runtime identity.** Converge/status compare each
  container's full image ID against the resolved pointer tuple; `ship.*`
  labels become discovery and decoration only. This is v2's runtime
  identity applied to the read side.
- **tmpfiles.d for `/tmp/ship-deploy`.** One provisioner-written entry
  (mode 1777, 24h age) replaces ship's hand-rolled global deploy-temp GC
  sweep. The `box gc` summary drops its "temp" lines.
- **Honest status vocabulary.** `running == healthy` inference dies:
  status reports `running`/`degraded`/`stopped` — it never measured
  health and stops claiming it. Output-contract tests and docs updated.

Parked as one future RFD (pointer → derived units → systemd
convergence): Quadlet for app processes AND for the Caddy container —
one coherent unit migration later beats two, and the caddy.service
Quadlet alone replaces working unit syntax, not a subsystem, at
real-box migration risk inside an unrelated flag-day.

Rejected, with cause: podman healthchecks (probe must exist inside the
app image, and ship's probe deliberately tests the Caddy-to-app ingress
path); podman secrets (splits the activation snapshot across two stores,
deletes nothing); tmpfiles.d for `/run/ship/locks` (create-on-use +
flock already self-heals) and for per-env trees (dynamic users, explicit
lifecycle); `kube play`/compose (replacement shape conflicts with the
routed-process handoff); `sd_notify` on the oneshot boot unit (exit is
the readiness signal); podman events as state (edge history, not runtime
truth); journald as committed history and generic podman prune as GC
(neither expresses ordered, verified, fail-protected retention).

## Rejected

- **Activation-scoped image references** (the pre-consult sketch): a
  new ref per activation would defeat same-config reuse — secret
  rotation would re-reference, not reuse. Artifact identity stays
  separate from runtime activation.
- **Generalizing the v0.8.0 guard** instead of deleting it: guarding a
  mutable name is strictly worse than an immutable reference the
  runtime already provides.
- **Release-keyed anything at the trust seam**: history, retention,
  dedup, and labels key by tuple. Release names are for humans.
