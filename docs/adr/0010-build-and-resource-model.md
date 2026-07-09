# ADR-0010: Build And Resource Model

- **Status:** Accepted
- **Date:** 2026-07-08
- **Context:** ship-era (post-pivot). Records why ship builds on the box
  and why we deliberately add no build-resource machinery.

## Context

ship uploads a source tarball and builds the container image **on the
box**, then runs it and deletes the source (the image is the release
artifact; the git repo is the source of truth — the image is a cache of
a commit, ADR-0005 / north star #1). A recurring worry is RAM on small
boxes: does building on a $5/512MB box eat memory, and is on-box build
wise?

Findings that settle it:

- **The build spike is a build-tooling property, not a container or ship
  property.** A container is a normal Linux process with namespaces +
  cgroups; podman is daemonless. At *runtime* a container adds ~nothing —
  the app uses what the app uses. `pnpm build` uses the same RAM on bare
  metal as inside podman.
- **The spike is the *build step*, not the run.** Whole-program bundling
  (webpack/vite/esbuild) + whole-program type-checking (`tsc`,
  `next build`) hold your entire codebase + every dependency's ASTs and
  type graph in memory at once, on a memory-generous V8. That is the
  1–4 GB hog. Runtimes that run source (Bun/`node --strip-types`, plain
  Node, Python, PHP, Go) have little or no build step and little or no
  spike.
- **The problem is shrinking.** The ecosystem is moving toward run-source
  (Node 22+ strips types; 23 closer to default; Bun/Deno native).

## Decision

1. **Build on the box remains the default.** It preserves the "two
   commands, no registry account" onboarding. The alternatives (pure
   build-then-push-to-registry à la Kamal; buildpacks) were considered
   and rejected — a registry is an account+credential dependency, and
   buildpacks are opaque magic where an explicit, portable Dockerfile is
   legible and yours (exit-is-a-feature). Dockerfile-required stands
   (ADR-0005).
2. **Small-box feasibility is a stack decision, not a ship feature.**
   The honest guidance: don't run a heavy JS *build* on a small box —
   either use a run-source runtime (Bun/Node-TS/Python/PHP/Go/
   Rails-8-importmaps), or build off-box. The single bad combo is
   *heavy-JS-build + build-on-box + small-box*; knocking out any one leg
   is fine. ship's natural audience mostly runs no-heavy-build stacks, so
   this is fine for them today.
3. **We add no build-resource machinery now (YAGNI).** Specifically we do
   **not** add: a build-memory cap, swap-at-`box setup`, or a build-
   location config. They only matter for the combo we tell people to
   avoid, and the ecosystem trend erodes the need further. Adding a safety
   net for an avoided case is over-engineering, and any knob here would
   violate zero-ops-decisions — if these ever land, they are invisible
   auto-derived defaults (build capped to leave prod headroom; swap sized
   to RAM), never ship.toml keys.

## Consequences

- **Honest sizing:** serving is cheap on tiny boxes; heavy JS *builds*
  want headroom or off-box. The tagline is "runs great on a €5 box," not
  "$5" — true for serving, asterisked for heavy builds.
- **Runtime caps stay app-shaped:** per-process
  `resources = { memory, cpus }` remains in ship.toml — that is the app's
  *serving* budget and is legitimately per-app, distinct from any
  (unbuilt) box-level build cap.

## Deferred / noted (not gaps)

- **Proactive image pruning.** Every stack accumulates one image per
  release on disk, independent of the build question. Today this is
  handled *reactively* — `box doctor` reports disk pressure and the
  future resident (RFD-0002) prunes. If it bites, promote to *proactive*:
  keep the last N releases (rollback window) and prune older on every
  successful deploy, as an invisible default (N not configurable). Not
  urgent because the reactive net exists.
- **Build-location seam.** Three ways to get an image onto the box:
  on-box (today); local-save-load-over-SSH (build on the laptop,
  `podman save | ssh | podman load` — no registry, needs arch match);
  registry-pull (Kamal-style). If a real user needs off-box build, refactor
  "produce the image" into one swappable stage *then* — need-driven, not
  speculative. The deferred mounted-cargo item in ship-spec §0 is this.

## Related

- ADR-0005 (container runtime via required Dockerfile)
- ADR-0007 (backup/restore — image-as-cache, repo-as-truth lineage)
- `docs/rfd/0001` (SQLite-first: why data forks are file copies, keeping
  the resource story RAM-cheap)
- `docs/rfd/0002` (resident — the eventual owner of proactive pruning)
