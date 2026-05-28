# ADR-0006: Cuts and Composability Commitments

- **Status**: Accepted
- **Date**: 2026-05-25
- **Amends**: ADR-0005 (container runtime via required Dockerfile).
  Sections 1, 9, 10, 15, and 16 of ADR-0005 are modified by this
  ADR; the rest stand.
- **Related**: ADR-0002 (state file layout),
  [docs/positioning.md](../positioning.md) (audience and design
  discipline tests).

## Context

After outside review of ADR-0005, several design choices were
challenged on the "less complexity unless really needed" test from
[positioning.md](../positioning.md). The reviewer was substantially
right: ADR-0005 introduced Kubernetes-shaped machinery — atomic-at-app
deploys, port allocators, content-addressed signatures, identity-budget
arithmetic — for a tool whose audience is solo developers and small
teams.

This ADR makes four cuts to ADR-0005, adds five clarifications the
original left implicit, and locks in three composability commitments
that make the "deploy primitive someone can build a control plane on
top of" framing in the positioning doc structurally real rather than
marketing.

Net code impact: approximately 40-50% less new code than ADR-0005
implied, with every user-visible benefit preserved.

## Cuts

### 1. Per-service rolling, not atomic-at-app swap

**Replaces ADR-0005 Section 10.** Each service in a multi-service
app deploys independently: new container starts, verifies, and swaps
Caddy upstream on its own. If `worker` fails verification, only
`worker` rolls back; `web`, already verified and swapped, stays at
the new version.

The atomic-at-app guarantee ("any failure tears down the entire new
set") is dropped. Solo-dev apps do not typically have versioned
web↔worker contracts that require it, and the choreography cost
(dual-state teardown, port-pool dance, all-services verification
gate) is high relative to the benefit.

Zero-downtime is preserved: each individual service deploy is still
blue/green. What changes is *what is atomic* — service, not app.

### 2. Caddy in a container on a shared `ingress` network

**Replaces ADR-0005 Section 16.** Caddy runs as a container on a
shared `ingress` Podman network. Every app container joins
`ingress` in addition to its per-`(app, env)` network. Caddy
upstreams use container DNS:
`http://app-<app>-<env>-<service>:<port>`.

The host-loopback port allocator and the
per-`(app, env, service) -> host_port` state from ADR-0005 are
removed. Blue/green during a rolling deploy uses container naming
(`app-<app>-<env>-<service>-new`, then rename on success), not
allocated ports.

The "Caddy is a plain host service" pillar from ADR-0005 was
load-bearing for the complexity it created; running Caddy in a
container is the smaller net cost.

### 3. Drop container content-addressing

**Replaces ADR-0005 Section 9 for container apps.** The
`(git_sha, manifest_hash)` signature, the SSH probe for the
last-deployed signature, and the server-side re-verification are
removed for container apps. Every container `deploy` invocation
streams the source tarball and lets `podman build`'s layer cache
decide what actually rebuilds.

What survives from Section 9:

- **Static `tree_hash`** stays. Static directories are typically
  gitignored, so the helper has no other way to detect whether the
  shipped tree changed. Skip-upload on unchanged tree is a real win.
- **Manifest-only fast path** stays. Manifest-only diffs (routes,
  env, resource limits) skip the build entirely and reconcile config
  in 1-3 seconds.

Removed: container signature scheme, SSH probe, server
re-verification, and the `--dirty` signature semantics. `--rebuild`
is reshaped — see Clarification 9.

### 4. Internal identity hashing, human names for display

**Replaces ADR-0005 Section 1's identity-length constraints.** The
16-char app / 8-char env / 10-char service regex limits are removed.
Users name things with the regex they would naturally use.

Internally, simple-vps derives a short stable hash from
`(app, env, service)` when constructing system identifiers that hit
host/container limits (Unix usernames, container DNS names). Display surfaces
(`status`, `logs`, error messages) use the human names; the hash is
implementation detail.

The user never has to budget characters across nested identifiers
to fit Linux's 31-char username limit. That limit becomes
simple-vps's problem, not the user's.

## Clarifications

### 5. Closed-set policy for helper synthesis

ADR-0005 Section 15 said the helper synthesizes all privileged
artifacts from typed manifest input. This ADR locks in *what the
manifest will never be able to express*, as a versioned commitment.

**Permanently disallowed at the manifest schema:**

- `--privileged`
- `--cap-add=SYS_ADMIN` and any capability outside the
  ADR-0005 Section 7 allowlist
- `--device=` for any host device
- Host mounts outside `/var/apps/<app>/<env>/shared/` or the declared
  `mounts` field's known targets
- `--pid=host`, `--ipc=host`, `--network=host`, `--userns=host`,
  `--uts=host`
- `docker.sock` / `podman.sock` mounts
- Arbitrary `--security-opt` strings (only the Section 7 set is
  accepted)

**Policy:**

- Additions to the closed set require a new ADR.
- Removals from the closed set require a new major version.
- Schema additions for legitimately needed knobs (e.g., a `tmpfs`
  size override) are normal evolution and do not need an ADR.

Without an explicit closed set, "we synthesize, you don't inject"
is a moving line. With it, ADR-0005 Section 15's trust boundary is
durable.

### 6. State-schema discipline

ADR-0002's "state in flat files" is not enough on its own to make
the tool composable. State has to be a *contract*, not just a
format.

**Commitments (additions to ADR-0002):**

- **Versioned writes.** Every JSON state file carries `version`.
  A binary that sees a future version refuses to write.
- **Migrations land with the first real schema bump.** The current
  product is pre-user and state is still version `1`; shipping
  `migrate-state` before a version `2` exists would create
  compatibility ballast, not leverage.
- **Additive-only after the first released version.** Once users
  exist, new fields are normal evolution; removals and renames
  require an explicit migration and an ADR.

Without these, "state is a public contract" is marketing. Before
users exist, the same principle means deleting stale state surfaces
instead of preserving them.

### 7. Per-service rolling failure semantics

Per-service rolling (Cut 1) needs explicit failure semantics:

- **Each service has its own verification.** Service A failing does not
  hide Service B's result. Service B that succeeded earlier in the deploy
  stays at the new version.
- **Partial-deploy state is a valid state.** `web v2, worker v1`
  after `worker` failed to deploy is not an error — it is the honest
  state. `status --json` displays the running containers it can see:

  ```
  {
    "app": "myapp",
    "env": "prod",
    "services": [
      {"service": "web", "state": "running", "release": "v2"},
      {"service": "worker", "state": "running", "release": "v1"}
    ]
  }
  ```

  No single "deploy succeeded" or "deploy failed" verdict hides
  this.
- **No automatic healing loop.** Next `deploy` retries the failed
  service. Explicit rollback is a future verb, not hidden behavior.
- **Deploy exit code** is non-zero if any service failed verification,
  even when other services succeeded. Scripted callers can detect
  partial deploys reliably.

### 8. First-deploy of a new service to an existing app

Adding `[services.cron]` to a manifest that already runs
`web` + `worker`:

- Same code path as a per-service rolling deploy. The new service
  has no old container to swap from; the helper starts the new
  container, verifies, joins it to the `ingress` and per-`(app,
  env)` networks. No special first-run path.
- Per-service state is read from Podman labels. The new service appears
  in `status` after its container exists.
- Removing a service from the manifest stops and removes the
  container on the next `deploy`. No `--purge` required; the
  manifest is the source of truth.

### 9. `--rebuild` flag simplified

ADR-0005's `--rebuild` did two jobs: override the content-addressed
signature *and* pass `--no-cache --pull=always` to `podman build`.
With container content-addressing gone (Cut 3), only the second
remains.

`--rebuild` is now purely a passthrough for `--no-cache
--pull=always`. Users who want strict supply-chain control of base
images pin digests in their `FROM` lines (`FROM oven/bun:1@sha256:...`);
`--rebuild` is the pragmatic escape hatch for mutable tags.

## Composability commitments

The positioning doc names "deploy primitive someone can build a
control plane on top of" as a moat. These commitments are the
structural preconditions; without them, the moat is marketing.

### 10. State file schema is a documented public contract

ADR-0002 is the authoritative description; helper changes that
touch state must keep the documented schema in sync, and the
discipline of Clarification 6 (additive-only, migrations,
deprecation window) applies. State files are not internal
implementation detail.

### 11. `--json` output on automation-facing read commands

Automation-facing read verbs (`status`, `secret list`, `host status`,
`host doctor`) accept `--json` and emit structured output suitable for
piping into another tool. The JSON shape is part of the public contract
under the same discipline as the state schema.

`logs` intentionally remains a stream of process output, not a JSON
object. The old `route list` helper is gone with `apps.json` /
`routes.json`; the planned replacement is `app list --json`, sourced from
Podman labels and Caddy fragments.

Human-readable table/text output remains the default. JSON is
opt-in per invocation; no environment variable, no config knob.

### 12. Per-`(app, env)` locking on the host

The helper takes an exclusive file lock on
`/run/simple-vps/locks/<app>-<env>.lock` before any same-env mutation
(`setup-env`, `app apply`, `restart`, `destroy-env`, `secret put`,
`secret rm`).
Concurrent operations on the same `(app, env)` — whether two
terminals, a human plus a scheduler, or CI plus a dashboard —
serialize cleanly.

Read-only operations do not take the lock. Lock acquisition uses
`flock(2)` with no timeout; scripted callers that prefer to fail fast
should enforce their own process timeout.

## Consequences

### What this enables

- "Control plane on top" in the positioning doc becomes
  structurally real: documented state, JSON everywhere, locking. A
  future dashboard or scheduler does not require simple-vps
  changes.
- Atomic-at-app code and port-allocator code never get written.
  ADR-0005's cutover plan is materially smaller.
- The closed-set policy makes the helper trust boundary durable
  against feature creep over time.

### What this gives up

- The "deploy is one verdict" simplification. `status` and the
  deploy exit code now have to surface partial states. The right
  trade for honesty over false signal.
- App-level atomic guarantees. Web/worker contract changes that
  require both to flip together now require deliberate coordination
  by the user (deploy worker first, then web; or pin web to old
  worker until the worker rollout settles).
- "Caddy is a plain host service, easy to debug from the host
  shell." Debugging Caddy now means `podman exec` into the Caddy
  container or reading its logs through the helper.

### What becomes harder

- Adding a manifest knob now requires considering whether it could
  be expressed *outside* the closed set, and updating the
  documented state schema if it persists. Slower than ad-hoc
  growth, on purpose.

## Cutover plan delta

This ADR modifies ADR-0005's cutover plan as follows.

**Replaced:**

- **Item 15** (atomic deploy lifecycle): replaced by per-service
  rolling per Cut 1 and Clarification 7.
- **Item 19** (port allocator state): removed. State schema records
  per-`(app, env, service)` container generation (`-new` vs current)
  for blue/green naming during rolling deploys.
- **Item 21** (host-loopback published ports): replaced by Caddy in
  container on the shared `ingress` network per Cut 2.
- **Item 11** (container app `(git_sha, manifest_hash)` signature):
  removed. Static `tree_hash` and manifest-only fast path retained.

**Added:**

- **Item 29:** Add migration tooling when the first real state
  schema bump exists. No pre-user compatibility shim.
- **Item 30:** Implement `--json` output on automation-facing read
  commands. Landed for `status`, `secret list`, `host status`, and
  `host doctor`.
- **Item 31:** Implement per-`(app, env)` file locking. Landed with
  fake-VPS coverage exercising two concurrent deploys of the same
  `(app, env)`.
- **Item 32:** Enforce the closed-set policy at manifest-validation
  time. Landed through strict TOML decoding plus explicit validation
  for supported knobs.

## Notes

- The reviewer's "document size as a signal" critique applies here
  too. This ADR is intentionally short — it does not re-derive
  ADR-0005's decisions, only amends them.
- The closed-set policy in Clarification 5 is the structural
  follow-up to ADR-0005 Section 15. Without an explicit closed set,
  the "compromised client cannot inject arbitrary unit content"
  guarantee weakens over time as the schema grows.
- The Caddy-in-container change (Cut 2) introduces one new
  configuration concern: the `ingress` network must be created by
  the host installer (ADR-0001 work) before any app deploy can
  succeed. This is a one-time setup step, not a per-deploy
  concern.
- ADR-0007 may introduce backup/restore state. That state joins the
  documented state schema under Commitment 10 and inherits the
  discipline of Clarification 6.
