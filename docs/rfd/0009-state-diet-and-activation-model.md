# RFD-0009: The state diet and the activation model

**Status: accepted, in implementation** (Franco, July 16, 2026 — blessed
in-session after a five-round design convergence with an external
consult; stages land as release arcs immediately, not post-v1).
Supersedes the SHIP-STATE-SQLITE investigation question (closed, see
Rejected). Related: ADR-0002/0005/0006 (runtime as truth), ADR-0012
(data-only backups), ADR-0014 (box config), ADR-0020/0021 (member store
and mutation surface). Diagrams: [assets/state-diet-deploy-lifecycle.svg](assets/state-diet-deploy-lifecycle.svg),
[assets/state-diet-inventory.svg](assets/state-diet-inventory.svg).

## Pitch

Ship's box state today is fourteen durable file shapes whose mutual
consistency is maintained by hand: write-ordering rules, five-snapshot
restore dances, and journal appends that can disagree with the state
they describe. The July 15 arc fixed the same crash-coherence bug class
twice in one day. This RFD deletes the class's habitat instead of
patching instances: **less state, each kind stored by the primitive that
matches its nature — and a deploy lifecycle where crash recovery is the
normal path, not special code.**

The investigation that produced this design also closed the "move state
into SQLite" question: after the diet, no surviving durable invariant
needs a transaction across files. A database would add 4.5 MB and a
box-wide failure domain to solve a problem this RFD deletes.

## Doctrine: four kinds of state, four primitives

1. **Intent** — what the operator declared. Small files, atomic
   rename writes, rebuildable from operator input via `box setup`.
   Lives in `/etc/ship` (plus one tiny pointer per env, below).
2. **History** — what happened. Append-only journals, torn-tail-safe
   readers, fsynced appends. Lives in `/var/lib/ship` (box scope) and
   the env root (deploy journal). Unreconstructible; loss is degraded
   evidence, never a broken box.
3. **Derived artifacts** — files other programs read (Caddy fragments,
   the deploy user's key file, systemd units, runtime env files).
   Rendered FROM intent, never read back AS truth. Drift is
   one-directional and repaired by convergence (ADR-0020's pattern,
   generalized).
4. **Ephemeral coordination** — locks and pending approvals. Lives in
   `/run/ship`; the OS clearing it at boot is the designed behavior.

The directory layout IS the doctrine: `/etc` = intent, `/var/lib` =
evidence, `/run` = ephemera, env root = per-env intent + history. This
is the same split every daemon on the box already uses (Docker, Caddy,
Tailscale); ship follows the platform instead of inventing one.

## End-state inventory

```text
/etc/ship/
  members.json            who can use the box (identity truth, ADR-0020)
  box-config.json         box options (ADR-0014) + new guarded key box.address
  secrets/<app>/<env>/<KEY>, capability tokens

/var/lib/ship/
  doctor.json             last health checkpoint (checks + recorded_at only;
                          the never-read delta field dies)
  approvals-journal.jsonl updates-journal.jsonl

/run/ship/
  approvals.json          pending approvals (15-min TTL; reboot clears — correct)
  locks/*.lock

/var/apps/<app>.<env>/
  ship.json               env identity, shrunk: app, env, infra_id,
                          preview{branch, last_ship_at, expires_at}
                          (sanitized_branch, suffix, pinned are derived)
  active.json             THE activation pointer: {version, release,
                          activation, envelope_hash} — written only by the
                          deploy/rollback commit, one atomic rename
  releases/journal.jsonl  deploy history (stays; hardened)
  runtime/activations/<activation-id>.env   frozen resolved env per
                          activation; immutable once written
  data/  static/releases/<release>/...

Inside each app image (OCI labels):
  ship.release_envelope   base64(manifest + release metadata), 64 KiB cap
  (static-only releases: a .ship-release sidecar in the static release dir)

Rendered artifacts (unchanged role): /etc/caddy/conf.d fragments,
deploy authorized_keys, systemd units.
```

**Deleted outright (flag-day, no legacy readers):** `host.json`,
the applied `ship.toml` copy, `releases/<release>/ship.toml` +
`release.json`, the `static/current` symlink, the singleton mutable
`runtime/.env`. Pre-cutover releases lose rollback eligibility until the
next deploy repopulates history; the serving release is untouched;
`box setup` + one deploy heals every box (there is exactly one).

**host.json replacements:** `observed` was already fiction — doctor
probes live state. `desired` is compiled constants — they stay in the
Ensure operations that apply them. `meta.client_address` becomes the
guarded `box.address` config key. `ship_version` derives from the last
completed update-journal entry; `installed_at`, `last_apply`,
`last_client_version` have no reader worth keeping. The host-installed
sentinel is replaced by the live prerequisite probes preflight already
runs.

## The activation model (deploy/rollback lifecycle)

```text
prepare candidate  →  validate  →  commit active.json  →  converge  →  journal  →  lazy GC
(beside serving;      (probe +      (one atomic           (containers,
 debris if it dies)    staged        rename — the          fragment,
                       caddy check)  point of truth)       reload, workers)
```

- **Prepare** builds the image (envelope label attached), publishes the
  static release dir, writes the activation env file, and stages a
  validated Caddy configuration. Nothing serving is touched. A crash
  here leaves inert debris for GC; the error message says "nothing
  changed".
- **Commit** writes `active.json` via atomic rename (with parent-dir
  fsync — see Findings). Before this instant, the old release owns the
  env; after it, the new one does. This file is deliberately tiny and
  single-purpose so the rename IS the transaction.
- **Converge** makes the runtime match the pointer: install fragment,
  reload Caddy, start processes, stop the old release's processes.
  Workers (portless processes that must not run twice) converge
  stop-old-then-start-new; if the new worker fails to start, ship
  best-effort restarts the old one — the single surviving compensation.
- **Journal, then GC.** The success entry is appended (fsynced) BEFORE
  any cleanup or pruning; if the append fails, cleanup is skipped and
  the deploy reports degraded, never dishonest.
- **Roll-forward semantics (blessed):** after the commit, ship never
  automatically reverses intent. A crash mid-converge leaves
  "committed but not converged" — visible in status, repaired by
  convergence (automatic at boot / next verb / explicit). Returning to
  the old release is always an explicit `rollback`, which selects an
  older release into `active.json` and runs the SAME converge path. No
  separate rollback engine, no five-snapshot unwind.
- **Release commands are at-least-once (blessed):** documented contract
  — they must be idempotent (migrations are). No machinery can promise
  exactly-once across a process kill; we document the truth instead.
- **Rollback discovery:** candidates = committed journal entries whose
  artifacts still verify (image present / static dir present). The
  journal reader tolerates exactly one torn final line (a crashed
  append), reports it as degraded, and hard-fails on any earlier
  malformed record. Insufficient history degrades to "pass an explicit
  release", never guessing.

### What deliberately remains

- The member write-direction rule (ADR-0021): grants record identity
  before access, revocations remove access before identity. It guards
  the boundary between ship's register and the login file the SSH
  daemon reads, which no storage change can absorb.
- Runtime choreography: fragment → validated reload → containers is
  external-system sequencing, not file coherence; the staged validation
  and error joining stay.
- The env lock serializes mutations per env exactly as today.

## Automatic-first convergence (the Vercel-bar amendment)

Convergence is machinery first, verbs second. The product promise:
**you should never need to run the repair command — the box repairs
itself.**

- **Boot**: a ship-managed systemd unit converges every env at startup —
  apps come back after a reboot because the box brings them back, not
  because a container flag happens to fire. (The July 16 reboot probe
  measured the gap this closes; see Findings.)
- **Post-crash**: verbs that touch an env notice "committed but not
  converged" and heal as part of their normal work (generalizing
  ADR-0021's drift self-heal).
- **GC is a retention policy, not a chore**: after every deploy and on
  the existing timer, keep the active release + verified rollback
  candidates, delete the rest, print what was removed. Only the active
  activation's frozen env file is retained — rollback mints a fresh
  activation and re-resolves current secrets, so older activation env
  files are unread copies of pre-rotation secret values and are
  collected as hygiene. Conservative grace period for fresh debris.
- **Public verbs — the agent escape hatch**: box-scope converge
  (members render, sudoers, timers, Caddy baseline), app-scope converge
  (env runtime ← active.json), box-scope gc. Every failure that
  convergence can fix prints the matching verb as its `next:` step.
  Exact names/grammar per ADR-0018/0019 conventions, recorded in the
  landing ADR. Hardening drift stays detection-only (M3): the doctor
  check compares live hardened files against the compiled desired
  state and points at setup/converge; the timer never rewrites
  security-relevant files by itself.

## Coherence residue (the honest list)

After this RFD, multi-step consistency in ship is exactly:

1. members.json ↔ authorized_keys direction rule (justified, kept).
2. active.json → derived artifacts, one-directional, converge-repairable.
3. The worker availability repair (best-effort old-worker restart).
4. Release commands documented at-least-once.

Everything else that required ordering or restoration is deleted or
became idempotent convergence.

## Findings folded into the arc (pre-existing, independent)

1. Rollback cannot reproduce TLS-overlay deploys (overlay mutates only
   the in-memory context; the raw manifest was what got snapshotted) —
   dissolved by the envelope carrying the effective manifest.
2. `last_client_version` lockless concurrent writes — dies with host.json.
3. Preview identity double-write leaves an orphan env on a crash —
   stage 2 makes setup write the final identity once.
4. A stale applied manifest reserves preview alias hosts — dies with
   the applied manifest; the rendered fragment set is the route truth.
5. `AtomicWrite` never fsyncs the parent directory — fixed in stage 3
   (load-bearing once active.json is the commit).
6. Deploy prunes before the success journal append — fixed by the
   commit → journal → cleanup order.
7. The approvals audit writer discards errors and never syncs — fixed
   in stage 1 when it becomes the only durable approval record.
8. Deploy-journal appends lack fsync while the reader hard-fails on a
   torn tail (already threatens `exec`'s release selection) — fixed by
   the shared torn-tail-safe reader + fsynced appends (stage 1).

July 16 reboot probe (empirical, Franco-approved): box rebooted with
one healthy production app. Result — **full outage**: the ship-managed
caddy unit is written and started but never enabled, so it stayed down;
the app container (restart-policy unless-stopped, which podman-restart
does not cover) stayed in Created. Nothing served until manual
restoration. Two fixes land: stage 1 makes the provisioner enable every
unit and timer it writes (and doctor asserts enablement); stage 5's
boot-time convergence is the durable answer independent of podman
restart-policy semantics. The box was restored to its exact pre-probe
state (started, deliberately not enabled) so the real-box gate
exercises the fix honestly.

## Stages (each lands + gates independently)

1. **State diet**: approvals → /run; doctor checkpoint → /var/lib
   (shrunk); host.json deleted; `box.address` key; journal hardening
   (shared torn-tail reader, fsynced appends, explicit audit-writer
   errors). Folded hygiene items: production `data save` becomes
   approval-gated like every other gated verb (M2); doctor
   `hardening_drift` check vs compiled desired (M3, detection only);
   spec accepted-risk note for uncapped restore extraction (M1).
2. **Preview identity**: single-write setup; ship.json shrinks;
   readers (reaper, auth, status) updated.
3. **Activation foundation**: active.json + activation-scoped env
   files; release envelope (label + static sidecar; 64 KiB cap;
   redacted from error argv display); staged Caddy validation; Caddy
   render/write consolidation; AtomicWrite dir-fsync.
4. **Crash-only lifecycle**: deploy/rollback on the prepare → commit →
   converge path; unwind machinery and static/current deleted; status
   shows intended vs running.
5. **Convergence + GC**: boot unit, post-crash self-heal, retention GC,
   public verbs + landing ADR.

Parallel: fuzzing for the three input parsers (member key lines,
passthrough args, manifest) with Go native fuzzing; panics/hangs are
findings. Parked: RFD-0010 records the member trust-mutation journaling
design against the hardened journal contract; implementation waits.

Every stage: contract-first briefs (public seams + test list before
implementation), worktree workers, line-by-line diff review,
zero-context hunts, full gate ladder ending on the real box, release
question to Franco.

## Rejected

- **One SQLite database for box state** (the investigation that
  spawned this RFD): after the diet no surviving invariant spans files;
  the driver costs 4.5 MB; one corrupt file would gate every verb; and
  the two motivating bug classes live at boundaries (login file the SSH
  daemon reads, container/proxy runtime) a database cannot reach.
  Reopen only for genuinely new transactional workloads (e.g. a future
  resident's job queue), never as a storage swap.
- **The Caddy fragment as the durable release pointer** (earlier draft
  of this design): a rendered artifact cannot be both disposable output
  and the thing you trust — and absence would be ambiguous
  (undeployed? crashed? half-created?). The tiny active.json intent
  file wins; the fragment stays derived.
- **Zero compensation** (earlier draft): non-overlapping workers keep
  one local availability repair; pretending otherwise trades a small
  honest exception for downtime.
- **Legacy readers for the old files**: zero users; flag-day; the only
  live box heals with setup + one deploy.
- **Linux eventing (inotify/D-Bus/podman events) as a truth mechanism**:
  edge-triggered events are lossy with no listener between CLI
  invocations; convergence is level-triggered and subsumes them. Events
  may someday wake a resident sooner; they never decide what is true.
- **Persisted step cursors / state-machine tables for deploy**: explicit
  phases in code + one atomic commit + idempotent convergence give the
  same crash-safety with no persisted machinery. Approvals remain the
  one justified persisted multi-actor flow (they already are one).
- **Merging identity into the login key file** (single-file member
  store): a hand-added structured comment would mint identity — the
  fabrication class ADR-0020 killed; making comments unforgeable needs
  a signing key and its lifecycle. members.json stays.
