# Architecture (reshaped ship)

Status: draft corpus for the kernel/modules reshape. Coexists with the
current `docs/` until cutover, when the legacy ADRs/RFDs freeze to
`docs/archive/legacy-go/` and this corpus is promoted to the repo root.

## The system in four sentences

1. The **kernel** runs commands and enforces who may run them.
2. **activation-records** remembers what was deployed and what happened.
3. **apps** decides what deploying means, through three fixed adapters
   (build, runtime, proxy).
4. Everything else is a **module** that renders configuration for an
   operating-system primitive.

Diagram: `docs/diagrams/kernel-modules-now-vs-after.svg`.

## Layers and who may depend on whom

```
        ┌─────────────────────────────────────────────────────────┐
 teal   │ modules: data · litestream · cron · hardening · metrics   │
        │  one ownership dir each · render config for primitives    │
        └───────────────┬───────────────────────────────┬──────────┘
                        │ (kernel context + capabilities) │
 purple ┌───────────────▼───────────┐   ┌────────────────▼──────────┐
        │ apps                       │──▶│ activation-records         │
        │ deploy/rollback/converge   │   │ pointer · journal · trust  │
        │ sequencing (8 phases)      │   │ · candidate policy         │
        └───────┬────────────────────┘   └────────────────────────────┘
                │ typed provider interfaces
 gray   ┌───────▼────────┬─────────────┬────────────────┐
        │ build adapter  │ runtime     │ proxy adapter   │
        │                │ (podman)    │ (caddy)         │
        └────────────────┴─────────────┴────────────────┘
 kernel ┌───────────────────────────────────────────────┐
        │ dispatch · admission/authz · exec · events ·   │
        │ cancellation · namespaced state · atomic files │
        └───────────────────────────────────────────────┘
 host   podman · caddy · systemd · ufw · sysusers · tmpfiles
```

### Kernel — mechanism only

The kernel knows commands, dispatch, execution (local and over SSH),
events (human output and NDJSON), cancellation, per-module state
namespaces, and atomic-file primitives. It **does not** know the words
"application", "deployment", "release", "database", "podman", or "caddy".
It exposes to every module a small context: actor, request id, target,
cancellation signal, emit, and a namespaced state directory. It never
exposes `deploy()`, `rollback()`, or any domain verb.

The kernel enforces admission and authorization **before** dispatch.
Modules *declare* the permission a command requires; the kernel is the
only thing that enforces it. There is exactly one authorizer.

### activation-records — memory

A deep module beside the kernel. It owns the activation pointer format
and its atomic publication, the append-only journal, the outcome
vocabulary, artifact-trust verification, and the one candidate policy
shared by rollback and garbage collection. No other module may write the
pointer or journal formats. `apps` calls it to publish and to resolve;
status and GC read from it directly.

### apps — meaning

`apps` owns what deploying means: the eight-phase deploy sequence,
rollback, and convergence orchestration (including at boot). It is the
only module that calls the build, runtime, and proxy provider interfaces.
Those three providers are compile-time singletons — one implementation
each (build, podman, caddy). There is no provider selection, negotiation,
or compatibility matrix; the interfaces exist for locality and test
substitution, not runtime flexibility.

### modules — capabilities

Every other capability is a module: a single ownership directory plus one
composition file. A module contributes verbs, host resources, semantic
health checks, and (rarely) a typed participation in an `apps` phase. It
renders configuration for an operating-system primitive (a systemd unit
or timer, a config file, a sysusers/tmpfiles entry) rather than
re-implementing supervision, scheduling, or backup in Go.

## The permitted dependency graph

Enforced by three layers (compiler, a repo analyzer, and a CI ownership
diff — see `MODULE-AUTHORING.md`). The rules:

- Anything may import the **kernel** (its context and typed capabilities).
- **apps** may import **activation-records** and the three provider
  interfaces.
- **modules → modules is banned.** A teal module may not import another
  teal module, and may not import the infrastructure adapters directly.
- A real cross-module need is not solved by an import; it is **promoted
  to a kernel capability**. If two modules want to share a fact, that
  fact was mis-placed — it belongs below them, not beside them.
- A module's implementation lives under `modules/<name>/internal/…`, so
  the compiler makes a sibling import not merely forbidden but impossible.
  Only a small `modules/<name>/definition` package is exported, and the
  registry imports definition packages only.

This is why the diagram has no arrows between teal boxes and never will:
the shape is a structural guarantee, not an aspiration.

## The invariants this architecture must preserve

These were each bought with a real shipped bug in the current engine and
are normative here. Any implementation that violates one is wrong,
regardless of how clean it looks.

1. **One commit point.** Runtime intent changes only when the activation
   pointer is atomically published (write temp, fsync, rename, fsync
   dir). Nothing before that write is committed.
2. **Failure has two sides of the commit.** A failure before the pointer
   write is `failed` (clean; old release keeps serving). A failure after
   it is `committed_unconverged` or `committed_degraded` — never rendered
   as an ordinary failed deploy and never auto-rolled-back.
3. **Convergence goes forward.** After a commit, recovery reconciles the
   runtime toward the committed pointer; it never restores an older
   artifact behind the operator's back.
4. **Artifacts are referenced exactly.** A committed artifact is named by
   an immutable identity (full image id / full static-tree hash), never a
   mutable release-derived tag.
5. **Rollback and GC share one candidate policy.** Both walk the same
   untruncated committed history and verify candidates before applying
   retention limits, so they cannot disagree.
6. **GC is conservative under uncertainty.** Deletion requires positive
   proof; anything unverifiable, in-use, or inside the grace window is
   kept. A torn journal cancels the sweep.
7. **Authorization precedes dispatch.** Identity is bound and the
   required permission is checked before any handler runs.
8. **Boot restores committed intent.** After reboot the box reconciles
   every environment back to its committed pointer on its own; one broken
   environment never blocks the rest.

See `OPERATIONS.md` for how phases and failure classes realize these.
