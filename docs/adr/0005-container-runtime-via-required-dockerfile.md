# ADR-0005: Container Runtime via Required Dockerfile

- **Status**: Accepted
- **Date**: 2026-05-24
- **Supersedes**: the runtime story in ADR-0001 (which kept `node`/`bun` as
  external host prerequisites and `static` as a parallel deploy mode).
- **Related**: ADR-0001 (bounded Go provisioner), ADR-0002 (state file layout),
  ADR-0003 (apt repo key trust policy).

## Context

The current code supports three deploy runtimes per env: `bun`, `node`,
`static`. The manifest carries a required `runtime` field that selects between
them. The deploy lifecycle ships a code checkout to
`/var/apps/<name>/releases/<sha>`, runs `<pm> install --production` on the
host, symlinks `current`, and renders a bespoke systemd unit that runs the
configured `command`.

That model has accumulated cost:

- Every new language is new code (lockfile detection, package-manager-specific
  install commands, runtime tool checks).
- Long-tail dependencies the user actually wants to self-host (Postgres,
  Redis, Plausible, n8n, Umami) ship as container images, not apt packages.
- The CLI shape (wrangler-like: many narrow primitives) is mismatched with the
  actual surface (one primary noun: "app"). flyctl-shape is the closer fit
  and is folded into this ADR rather than punted to a follow-up.
- The "what runtime am I" question is redundant the moment a `Dockerfile`
  exists in the repo. The file is the answer.

Three options were considered:

1. **Keep native runtimes.** Rejected: every new language is new code, and
   containers are required for the long tail anyway.
2. **Hybrid (native fast path for Bun/Node + container escape hatch).**
   Rejected: two code paths, and the cold-start optimization (5 ms vs
   ~300 ms) does not matter for long-running web services.
3. **Container-shaped via required Dockerfile, with static files as the only
   special case.** Chosen by this ADR.

## Decision

### 1. Two app shapes, no `runtime` field

The `runtime` field is removed from the manifest. App shape is inferred from
the working tree:

| Has Dockerfile | Has `static = "..."` | Shape | Outcome |
|---|---|---|---|
| yes | no | container | build image + run container |
| no | yes | static | upload directory + Caddy `file_server` |
| yes | yes | error | manifest is ambiguous, fail fast |
| no | no | error | nothing to deploy, fail fast |

Static is preserved as a first-class shape because Caddy already terminates
TLS and serves files. Wrapping a static directory in a container to serve it
adds cost with no benefit.

"Static" here means the build output requires no runtime to serve.
Frameworks that produce static output by default qualify: Hugo, 11ty,
Jekyll, Astro `output: 'static'`, SvelteKit `adapter-static`, and plain
SPAs (Vite + React/Vue). Frameworks running in SSR mode are container
apps regardless of how "static-looking" the source tree appears: Astro
`output: 'server'`/`'hybrid'`, Next.js with server features, Remix,
SvelteKit `adapter-node`. The shape is determined by what the build
produces, not by which framework was used.

### 2. Container engine: Podman

Podman has no long-running daemon, integrates with systemd via
`podman generate systemd`, and accepts unmodified Dockerfiles via
BuildKit-compatible `podman build`. Same UX as Docker, meaningfully
smaller blast radius — no persistent root daemon, no socket to leak.

The helper invokes Podman as **root** (it already holds root via
passwordless sudo from `deploy@`). Container *processes* run as the
per-app user `app-<name>` per Section 7's security floor (`--user`,
`--cap-drop=ALL`, `--security-opt=no-new-privileges`, read-only rootfs,
resource caps).

Truly rootless Podman per app user was considered and rejected:
per-user image storage breaks shared image caches across apps, rootless
networking has port-binding limitations (no <1024 binds without
setcap), and on a single-host VPS the operator already holds root via
SSH — the marginal "extra layer before host root" isolation gain does
not outweigh the operational complexity. The win that does carry over
from rootless mode is the absence of a persistent root daemon; that
remains true under rootful Podman invocation because `podman` is a
short-lived CLI, not a long-running service.

Docker remains a valid escape hatch if a future use case demonstrates
it is required, but Podman is the supported default.

### 3. Required Dockerfile — no `image:` field

Two shapes were considered:

- **A. Support both `image = "repo@sha256:..."` and a Dockerfile.** A
  digest-pinned image string is a legitimate shape for apps where the
  upstream image needs zero configuration. Small, verifiable, explicit.
- **B. Require a Dockerfile, even a one-liner.** Single schema; the
  moment any tweak is needed (env init script, custom config, alternate
  entrypoint), the Dockerfile is the only place that can express it.

Chosen: **B**. The argument for A is real — a no-config image is one
line either way. The argument against A: any non-trivial container deploy
eventually wants configuration, at which point users migrate from
`image:` to `dockerfile:` and the schema has carried both forms
indefinitely for nothing. The uniformity of one path through the
build/run pipeline is worth the small friction of writing a one-line
Dockerfile for the no-config edge case:

```dockerfile
# apps/postgres/Dockerfile
FROM postgres:16-alpine
# config tweaks live here, if any
```

Every container app has a Dockerfile, even a one-liner. The manifest
carries only VPS-facing concerns: ports, health checks, routes, secret
bindings, resource limits.

### 4. Convention-driven volumes

simple-vps bind-mounts `/var/apps/<app>/shared/` into every container at the
same path. No `volumes` field in the manifest for the common case. The
shared directory contains the env file and any persistent data the app
writes.

Cross-app sharing (for example, Litestream reading another app's data
directory) uses a small `mounts = ["other-app"]` field that resolves to
`--mount type=bind,src=/var/apps/other-app/shared,dst=/var/apps/other-app/shared,ro`
on the container.

### 5. Image-based releases — `releases/` removed for container apps

Container images are the immutable release artifact. For **container
apps**, the per-deploy `releases/<sha>` source checkout pattern (current
code) is removed; images replace it. Per-app on-disk state for container
apps shrinks to:

```
/var/apps/<name>/
  shared/            bind-mounted into container, persists across deploys
```

Images are tagged `simple-vps/<app>:<sha>` with labels `app=<name>` and
`simple_vps_release=<sha>`. Listing deploys: `podman images --filter
label=app=<name>`. Rollback: run an older tagged image.

Static apps keep a `releases/` directory for snapshot-based retention;
see Section 12. The `keep_releases` knob is shared by both worlds.

### 6. Release retention

The existing `keep_releases` knob (default 5) applies to images. After a
successful deploy:

1. List images by label `app=<name>` sorted by created date.
2. Keep the currently-running image, the previous-successful image, and the
   N most recent.
3. Untag the rest.
4. Run `podman image prune -f` to reap untagged images and unused layers.

The release retention semantics from the current code (current + previous +
keep N) are preserved end-to-end.

### 7. Container security floor (default, not opt-in)

Every container starts with:

```
--user $(id -u app-<name>):$(id -g app-<name>)
--cap-drop=ALL
--cap-add=NET_BIND_SERVICE        # only when binding <1024
--security-opt=no-new-privileges
--read-only
--tmpfs=/tmp:size=64m
--memory=<from manifest>
--cpus=<from manifest>
--pids-limit=512
--network=app-<name>
```

Disallowed at the API level: `--privileged`, `docker.sock` mounts,
`--pid=host`, `--ipc=host`, `--network=host`. These defeat the security
floor and have no use case the manifest needs to express.

### 8. Routes can share a host via path

Today routes are keyed by host. The new key is `(host, path)`. Two apps may
share a host when their path prefixes differ:

```toml
[apps.api.routes.api]
host = "myapp.com"
path = "/api"
type = "proxy"
service = "web"

[apps.spa.routes.www]
host = "myapp.com"
# path defaults to "/"
type = "static"
```

Caddy emits one site block per host and one `handle` block per path.

Same `(host, path)` for different apps is a hard conflict and a deploy-time
error. The `--force` flag exists only for same-app collisions (replacing
one's own route definition). It does not transfer ownership across apps.

Moving a route from app A to app B is a two-step operation: remove the
route from app A's manifest and deploy A; add the route to app B's manifest
and deploy B. Two explicit, grep-able actions; no hidden takeover path.

### 9. Deploy is content-addressed

`simple-vps deploy <env>` does as little work as the diff requires. It
is the universal verb; there is no separate `deploy --config-only` or
`apply` flag.

Mode is selected by comparing a **content signature** against the
helper's last-successful-deploy record. The signature components depend
on app shape:

- **Container apps:** `(git_sha, manifest_hash)`. The git SHA covers
  the build context because `git archive HEAD` is content-addressed by
  SHA. Dirty-worktree deploys add a timestamp suffix that forces a
  fresh signature.
- **Static apps:** `(static_tree_hash, manifest_hash)`. The static
  directory is gitignored in the common case, so git SHA does not
  cover what is actually shipped. `static_tree_hash` is computed
  deterministically server-side from the uploaded tarball (e.g.,
  `sha256sum` over a sorted listing of file paths, modes, and content
  hashes) so a rebuilt `dist/` with byte-different content always
  triggers a deploy.

| Diff | Action | Typical time |
|---|---|---|
| No diff | No-op, report "nothing to deploy" | ~1 s (one SSH check) |
| Manifest only | Reconcile config (routes / env / secrets / mounts / limits) | 1-3 s; +container restart if runtime flags changed |
| Container: code or Dockerfile changed | Build new image, blue/green swap, reconcile config | 5-90 s depending on layer cache |
| Static: tree hash changed | Upload tarball, snapshot to `releases/<id>`, atomic symlink swap | 1-3 s |

This collapses the wrangler-style "many imperative verbs" surface into
one declarative verb that converges actual state to the manifest's
intent. It matches `fly deploy`. The user's mental model is uniform:
edit manifest (or rebuild the static output), run deploy, the system
figures out the cheapest path.

The helper persists the last-successful-deploy signature per app in
its state file (extending the ADR-0002 schema).

### 10. Container deploy lifecycle (full-build path)

When Section 9 selects the full-build mode:

1. **Client:** `git archive HEAD` (or worktree tar for `--dirty`) → tarball.
2. **Client → helper (SSH):** stream tarball; helper runs
   `systemd-run --scope -p CPUQuota=75% podman build -t
   simple-vps/<app>:<sha> --label app=<name> --label
   simple_vps_release=<sha> -`. The CPUQuota cap bounds contention with
   apps already serving traffic on the same host.
3. **Helper:** allocate a fresh host-loopback port per service (stored
   in helper state, see Section 16's networking model), then
   `podman run --name app-<name>-new` with the Section 7 security
   floor, `--env-file /var/apps/<app>/shared/.env`, `--mount` for
   `/var/apps/<app>/shared` and any declared `mounts`,
   `--network=app-<name>` for intra-app container-to-container
   traffic, and
   `--publish 127.0.0.1:<allocated-port>:<service-port>` per service.
4. **Health check:** `curl 127.0.0.1:<allocated-port><healthcheck>`
   until success or timeout.
5. **Swap:** rewrite Caddy upstream from the previous
   `127.0.0.1:<old-port>` to `127.0.0.1:<new-port>`, `caddy reload`.
6. **Reap old:** stop the previous container, rename `app-<name>-new`
   to `app-<name>`, release the old allocated port back to the pool.
7. **Reconcile:** apply manifest routes (Section 13), record new
   content signature (Section 9) in helper state, untag stale images
   per Section 6, `podman image prune -f`.

Health-check failure: stop `app-<name>-new`, release its allocated
port, leave the previous container and its existing Caddy upstream
untouched, fail the deploy. No state mutation.

### 11. Config-only reconcile (manifest-only diff path)

When Section 9 selects the config-only mode, the deploy skips build entirely
and applies the diff:

- **Routes diff:** regenerate Caddyfile, `caddy reload`. No container touch.
- **Env / secret diff:** write new env file to `/var/apps/<app>/shared/.env`,
  restart the running container (env is bound at start).
- **Resource limit / mount diff:** recreate the container with new flags
  (Podman cannot live-update most security or mount settings).

Typical time: 1-3 seconds for a route-only change; longer when a container
restart is needed (still under 10 seconds for a small app).

After a successful config-only reconcile, helper state is updated with the
new `manifest_hash`; `git_sha` is unchanged.

### 12. Static deploy: ship a directory, no build

simple-vps does **not** run any build for static apps. The user produces
the directory however they want (`astro build`, `npm run build`, Hugo,
plain HTML) before calling `simple-vps deploy`. The manifest's `static`
field points at the produced directory.

This is deliberate: it keeps simple-vps language- and build-tool-agnostic
for static deploys, just as the Dockerfile owns the build for container
apps. simple-vps owns deployment, not source-to-artifact transformation.

Per-app on-disk layout for static apps:

```
/var/apps/<name>/
  shared/                       persistent app state (env file, etc.)
  web -> releases/<id>          symlink: active release
  releases/
    20260524-153045-abc1234/    deploy snapshot
    20260523-092311-def5678/
```

Lifecycle:

1. **Client:** copy the directory at the manifest's `static` path →
   tarball.
2. **Client → helper (SSH):** stream tarball.
3. **Helper:** extract to `/var/apps/<name>/releases/<id>/`, where
   `<id>` is timestamp + short git SHA (or `dirty-<timestamp>` for an
   unclean worktree).
4. **Helper:** atomic symlink swap via `rename(2)`:

   ```
   ln -sfn releases/<id> web.next
   mv -Tf web.next web
   ```

   `ln -sfn` alone is not atomic — it unlinks then re-creates the
   symlink, leaving a brief window where `web` does not exist. The
   temp-symlink + `mv -Tf` pattern uses `rename(2)`, which is atomic
   for symlink replacement on Linux. Requests in flight during the
   swap either see the old release or the new one, never a missing
   path.
5. **Helper:** reconcile manifest routes (Caddy `root` pointing at
   `/var/apps/<name>/web` + `file_server`).
6. `caddy reload`.
7. Prune older release directories per Section 6 semantics (current +
   previous-successful + keep N most recent).

No container, no port, no health check. Rollback for static apps:
`simple-vps rollback <env> [release-id]` swaps the symlink to an older
release directory. No rebuild, no re-upload.

The retention pattern (filesystem releases dir + active symlink) is
different from container apps (tagged images in Podman storage), but the
`keep_releases` knob means the same thing in both worlds: how many old
releases stay available for rollback. The user-facing `static = "..."`
field is the only knob; the server-side layout is internal and not
configurable.

### 13. CLI shape: manifest is the source of truth

The CLI is app-centric (flyctl-shape), not resource-centric (wrangler-shape).
One primary noun ("app"), few verbs operating on it, one source of truth
(the manifest), one universal reconcile verb (`deploy`).

**Declarative state lives in the manifest:**
- App shape (Dockerfile or `static = "..."`)
- Services (port, healthcheck, resources, env, mounts)
- Routes (host, path, type, service)
- Secret bindings (`@secret:key` references)

**Imperative state via CLI:**
- Secret values (`secret put/list/rm`) — sensitive, can't live in a checked-in file
- Lifecycle actions (`deploy`, `restart`, `rollback`, `destroy`)
- Observability (`status`, `logs`, `ssh`)

**Client commands after the pivot:**

| Verb | Purpose |
|---|---|
| `init` | scaffold `simple-vps.toml` + `Dockerfile` (container) or `simple-vps.toml` (static) |
| `check` | validate manifest |
| `setup <env>` | create app on host (per-env, one-time) |
| `deploy <env>` | content-addressed reconcile (Section 9) |
| `status <env>` | release, services, routes, last deploy timestamp |
| `logs <env> [service]` | tail journal |
| `ssh <env>` | SSH into VPS |
| `restart <env> <service>` | restart a service (no rebuild) |
| `rollback <env> [release]` | activate prior release |
| `destroy <env>` | tear down app on host |
| `secret put/list/rm <env> <key>` | manage secret values |
| `host status/doctor` | host-level checks |

**Removed:**
- Client-side `route` verb (route info now surfaced under `status`).
- `env push` (use `secret put` per key; bulk import was a deploy-state hazard).

**Helper-side route surface collapses** from four CRUD verbs
(`proxy/static/redirect/remove`) to one reconcile verb:

- `route apply --from-manifest <app>` — diff manifest routes against state,
  apply additions/removals atomically. Called by `deploy`.
- `route list` — stays as a read-only inspection helper.

### 14. Provisioner changes

`internal/provision/install.go`:

- **Adds:** Podman install from Ubuntu 24.04 Universe (`apt install
  podman`). ADR-0003 (third-party apt repo key trust) does not apply
  here — first-party Ubuntu archives are trusted via
  `ubuntu-keyring`. The Universe-shipped Podman (4.9.x) provides
  rootless mode, systemd integration, and the security flags Section 7
  relies on. Upstream Podman 5.x via Kubic is not required and not
  configured by default.
- **Removes:** Node, Bun, npm, pnpm, yarn install paths and runtime
  prerequisite checks.

Minimum host footprint after the pivot:

- `simple-vps` helper binary
- Podman
- Caddy
- `rsync` (Ubuntu base, used for artifact upload)

That is the complete supported install surface. Optional add-ons (Tailscale,
cloudflared, Litestream) remain user-driven; simple-vps does not enforce
them.

The bounded operation budget from ADR-0001 does not change; only the set of
packages provisioned does. No new primitives are required.

### 15. Helper owns root-side artifact generation

The helper runs as root via passwordless sudo from the `deploy@` user.
Every helper command is a root operation initiated remotely. The current
code accepts uploaded systemd unit files and installs them, which gives
the client effective control over root-installed unit content — more
privilege than the design needs.

The container pivot is the right moment to tighten this boundary. After
the pivot:

- The client uploads **only typed input**: the manifest, a source
  tarball (for container builds), a static-directory tarball, and
  secret values.
- The helper **synthesizes all privileged artifacts** server-side from
  that typed input: systemd unit files, Caddyfile, `podman run` flag
  sets, env files.
- The helper rejects any unit file, runtime flag, or Caddy directive
  that did not originate from its own synthesis.

In practice the helper exports verbs like `app apply --from-manifest`,
`route apply --from-manifest`, and `secret put`. It does **not** export
verbs like `app install-unit <unit-file>` that accept uploaded unit
content. Where current code uploads a rendered unit, the new code
uploads the manifest and the helper renders the unit.

This narrows the trust boundary to "manifest schema + secret values,"
both validated server-side. Compromised client credentials still grant
deploy access — unavoidable — but cannot inject arbitrary unit content,
container flags, or out-of-schema Caddy directives.

### 16. Networking: host-loopback published ports

Host-Caddy talks to containers via **host-loopback published ports**.
This is the only routing path; Caddy does not join any Podman network.

Concretely:

- Each service in the manifest is assigned a host-loopback port by the
  helper at deploy time, from a configurable range (default
  `33000-33999`).
- The container is started with
  `--publish 127.0.0.1:<allocated>:<service-port>`. The bind is
  loopback-only — the port is not exposed on any external interface.
- Caddy upstream is `127.0.0.1:<allocated>`.
- Health checks hit `127.0.0.1:<allocated><healthcheck-path>`.
- During a full-build deploy, the new container gets a fresh port; the
  old port is released to the pool only after the old container stops
  (Section 10 step 6). The blue/green swap is at the Caddy-upstream
  layer, not at the port-binding layer.

The per-app Podman network `app-<name>` still exists, but its purpose is
**intra-app container-to-container traffic** (multi-container apps
talking to each other by container name). It is not the path Caddy uses
to reach app services.

State: the helper records `(app, service) -> host_port` in its state
file and survives restarts. Port collisions across apps are prevented
by the allocator; manifests do not specify host ports.

Trade-offs vs the alternative "host Caddy joins the Podman network and
resolves container names":

- Host-loopback wins on simplicity (Caddy stays a plain host service,
  no Podman networking on the Caddy side), debuggability
  (`curl 127.0.0.1:<port>` from the host always works), and
  compatibility (works identically under rootful or rootless Podman).
- Container-network membership would let Caddy address upstreams by
  container name without port allocation, at the cost of putting Caddy
  inside Podman's network plane. Rejected for this tool's
  "Caddy is a host service" architecture.

Static apps do not use host-loopback ports; host Caddy serves their
files directly via `file_server` (Section 12).

## Consequences

### What this enables

- Any language with a Dockerfile is supported with zero simple-vps code.
  Elixir, Ruby, Python, Go, Rust, PHP, Bun, Node, Deno — same code path.
- Third-party self-hosted tools (Postgres, Redis, Plausible, n8n, Umami)
  deploy with a one-line Dockerfile.
- Manifest shrinks ~60% for typical apps. `runtime`, `command`,
  `build.install`, lockfile detection all delete.
- ~600–800 net lines removed from the codebase. ~150–200 added back for
  container plumbing.
- One debug axis disappears ("is this a lockfile-detection bug or a deploy
  bug?").
- Routes and other config changes take 1-3 seconds end-to-end; no rebuild,
  no restart, no waiting on layer downloads.
- One mental model: edit the manifest, run deploy. The system picks the
  cheapest reconcile path. No drift between CLI-mutated state and
  manifest-declared state.
- Privileged-helper trust boundary narrows to typed manifest input plus
  secret values (Section 15). Clients can no longer inject arbitrary
  systemd unit content, container flags, or Caddy directives.

### What this gives up

- Cold-start time per container is ~300 ms vs ~5 ms for native Bun.
  Acceptable for long-running services; would be the wrong call for
  stateless function-style workloads, which simple-vps does not target.
- Disk: each image carries its own layer set. Layers are deduplicated
  across builds, but a long-running VPS with many apps will use more disk
  than the native model. Section 6 retention bounds this.
- Build runs on the VPS via `podman build` over the streamed tarball.
  Faster client-side deploys but more VPS CPU and temporary disk during
  deploy. Section 10's CPUQuota cap bounds the worst case.
- Native systemd sandbox (`User=app-X`, `ProtectSystem=strict`, etc.) is no
  longer the primary isolation primitive for app code. The container
  security floor in Section 7 replaces it. The floor must hold from day
  one.
- No `route add` / `route remove` CLI for ad-hoc tinkering. All route
  changes go through manifest + deploy. The trade-off is bought back by
  config-only deploys being seconds, not minutes.
- VPS needs outbound HTTPS for `podman build` (base image pulls, language
  package installs). Air-gapped VPS scenarios are not supported and would
  require build-on-client, which is out of scope here.
- No client-side escape hatch for systemd unit content or container
  flags. Custom needs must be expressed through the manifest schema; if
  the schema cannot express it, the answer is to extend the schema in a
  follow-up ADR, not to side-channel unit content through the helper.

### What becomes harder

- "What is currently running?" requires `podman inspect`, not
  `cat /var/apps/<name>/current/file`. Trade-off accepted: the image is
  immutable and inspectable; the previous mutable filesystem release was
  easier to grep but easier to drift.
- Adding a new first-class deploy shape (a WASM module, a Firecracker
  microVM) would be its own ADR. Container + static in this ADR is the
  entire surface.

## Out of scope (do not litigate later without a new ADR)

- **Multi-app-per-manifest.** Each manifest describes one app. Co-locating
  Postgres + API in one repo means two simple-vps manifests in
  subdirectories, or two separate repos. A future ADR may add
  `[apps.<name>]` blocks if a real use case demands it.
- **Image registry support.** Builds happen on the host; images live in
  local Podman storage. Multi-host deploys would need a registry, but
  simple-vps is single-host.
- **Build on client / image upload.** Build runs on the VPS. The client
  ships source, not a built image. Adds a dependency only Internet-isolated
  VPS scenarios would need.
- **Native runtime support.** Removed and not re-added under a flag.
  Container or static is the entire surface.
- **BuildKit on Docker.** Podman build is the supported tool.
- **Cross-app `--force` route takeover.** Same-`(host, path)` collisions
  across apps require explicit removal from the owning manifest first.
- **Force-Docker for static apps.** Wrapping a static directory in a
  Caddy/nginx container would simplify the codebase (one lifecycle) but
  cost ~30–50 MB RAM per static app, slower deploys, and friction for
  the most common static use case. Rejected on RAM + UX grounds.
- **Container supply-chain enforcement.** Digest-pinned base images are
  the supply-chain-strict equivalent of ADR-0003/0004 for the build
  context. The friction of manual SHA lookups on every base image bump
  outweighs the win for the solo-dev VPS audience this tool targets.
  Documented in Notes; not enforced.

## Cutover plan

This project is pre-user. There is no compatibility window.

Cutover is complete when:

1. `runtime` is removed from `Manifest.EnvBlock`; the Dockerfile and
   optional `static` field replace it.
2. `cmd/client/client.go` no longer references `bun`, `node`, lockfile
   detection, or package-manager install commands.
3. Provisioner installs Podman; no longer installs Node or Bun. The Node
   and Bun rows are removed from the package source matrix in ADR-0001
   Section 7 (a one-line note in ADR-0001 points to this ADR).
4. Helper deploy verbs operate on container images and Podman containers;
   the `releases/` directory is no longer created or managed.
5. Routes accept `(host, path)` keys end-to-end; the Caddy generator emits
   per-path `handle` blocks.
6. The container security floor in Section 7 is the default in code, not a
   manifest opt-in.
7. `deploy` is content-addressed: it detects no-op, config-only, and
   full-build modes from `(git_sha, manifest_hash)` diff against helper
   state.
8. Helper state schema (ADR-0002) extends to record per-app last-deployed
   `(git_sha, manifest_hash)`.
9. Build process runs with `CPUQuota=75%` to bound contention with serving
   traffic.
10. Client `route` command is removed. `status` shows route info inline.
11. Helper route CRUD verbs (`proxy/static/redirect/remove`) are removed.
    A single `route apply --from-manifest <app>` reconcile verb replaces
    them. `route list` stays for read-only inspection.
12. Fake-VPS coverage exercises: container build, blue/green swap,
    health-check rollback, static deploy, image prune, path-based routing
    collision, no-op deploy, config-only deploy.
13. Static deploys land in `/var/apps/<name>/releases/<id>/` with an
    atomic `web` symlink swap; rollback swaps the symlink, never
    rebuilds.
14. Podman is installed from Ubuntu 24.04 Universe (no third-party repo
    configured).
15. Helper rejects uploaded systemd unit files and uploaded
    `podman run` flag sets. All privileged artifacts are synthesized
    server-side from typed manifest input. The current
    `app install-unit` verb is removed; `app apply --from-manifest`
    replaces it.
16. Podman runs rootful (helper invokes as root); container processes
    run as `app-<name>` per Section 7's security floor. No rootless
    Podman per-user setup.
17. Static deploy symlink swap uses `rename(2)` via `mv -Tf`, not
    `ln -sfn` alone.
18. Content signature is shape-dependent: container apps use
    `(git_sha, manifest_hash)`; static apps use
    `(static_tree_hash, manifest_hash)`. Helper computes
    `static_tree_hash` server-side from the uploaded tarball.
19. Helper state schema (ADR-0002) extends to record
    `(app, service) -> host_port` allocations from a configurable
    range (default 33000-33999), and the last-successful-deploy
    signature per app.
20. Host-Caddy reaches container services via
    `--publish 127.0.0.1:<allocated>:<service-port>`. Caddy does not
    join any Podman network. The per-app `app-<name>` Podman network
    exists for intra-app traffic only.

## Notes

- "Dockerfile" here means OCI-compatible build context; Podman accepts the
  same syntax. The name is convention, not lock-in.
- Section 6 (release retention) preserves the existing `keep_releases` knob
  name and default. Users who set it before the pivot get the same
  semantics after.
- Section 8 (path-based routing) is the answer to "two apps, same host."
  Cross-app `--force` takeover is intentionally not supported; the foot-gun
  outweighs any ergonomics the manifest cannot express more clearly.
- Section 13's CLI surface shrinks the cmd/helper route file from ~310
  lines to under ~100 (one apply verb + one list verb). The corresponding
  client-side `routePublishCommand` choreography in `cmd/client/client.go`
  collapses to a single per-deploy call.
- `simple-vps init` scaffolds a default Dockerfile with proper layer
  ordering — `COPY package.json bun.lock ./` and `RUN bun install` before
  `COPY . .` — so that subsequent deploys hit the install-layer cache and
  rebuild only the code-copy step (typically 2-5 seconds). For static apps
  it scaffolds a `simple-vps.toml` pointing at the conventional output
  directory (`dist/` or `out/`).
- **Container supply-chain (base image trust).** Dockerfiles routinely
  reference mutable upstream tags (`FROM node:22`), which resolve to
  whatever the registry serves at build time. ADR-0003 and ADR-0004 are
  strict about host-side downloads; the equivalent strictness inside a
  build context is digest pinning (`FROM node:22@sha256:...`).
  simple-vps does not enforce digest pinning — the friction of manual
  SHA lookups on every base image bump outweighs the supply-chain win
  for the solo-dev VPS audience. `init` scaffolds with human-readable
  tags; users may pin to digests for apps where supply chain matters.
- Static apps share the `keep_releases` knob with container apps even
  though the retention mechanism differs (filesystem release dirs +
  symlink for static; tagged images in Podman storage for container).
  Same number, same meaning ("how many old releases stay rollbackable"),
  different storage. The user-facing model is uniform.
