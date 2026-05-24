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

### 2. Container engine: Podman

Podman is rootless by default, has no long-running daemon, integrates with
systemd via `podman generate systemd`, and accepts unmodified Dockerfiles
via BuildKit-compatible `podman build`. Same UX as Docker, meaningfully
smaller blast radius (no root daemon, no socket to leak).

Docker remains a valid escape hatch if a future use case demonstrates it is
required, but Podman is the supported default.

### 3. Required Dockerfile — no `image:` field

Pre-built images always need configuration (env vars, init scripts, custom
configs). Supporting an `image:` field in the manifest grows the schema
toward docker-compose. The Dockerfile is the natural place to express "use
this image with these tweaks":

```dockerfile
# apps/postgres/Dockerfile
FROM postgres:16-alpine
# config tweaks live here, if any
```

Every container app has a Dockerfile, even a one-liner. The manifest carries
only VPS-facing concerns: ports, health checks, routes, secret bindings,
resource limits.

### 4. Convention-driven volumes

simple-vps bind-mounts `/var/apps/<app>/shared/` into every container at the
same path. No `volumes` field in the manifest for the common case. The
shared directory contains the env file and any persistent data the app
writes.

Cross-app sharing (for example, Litestream reading another app's data
directory) uses a small `mounts = ["other-app"]` field that resolves to
`--mount type=bind,src=/var/apps/other-app/shared,dst=/var/apps/other-app/shared,ro`
on the container.

### 5. Image-based releases — `releases/` directory removed

Container images are the immutable release artifact. The per-deploy
`releases/<sha>` checkout directory is removed. Per-app on-disk state
shrinks to:

```
/var/apps/<name>/
  shared/            bind-mounted into container, persists across deploys
```

Images are tagged `simple-vps/<app>:<sha>` with labels `app=<name>` and
`simple_vps_release=<sha>`. Listing deploys: `podman images --filter
label=app=<name>`. Rollback: run an older tagged image.

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

`simple-vps deploy <env>` does as little work as the diff requires. It is the
universal verb; there is no separate `deploy --config-only` or `apply` flag.

Mode is selected by comparing the local `(git_sha, manifest_hash)` against
the helper's last-successful-deploy record for this app:

| Diff | Action | Typical time |
|---|---|---|
| No diff | No-op, report "nothing to deploy" | ~1 s (one SSH check) |
| Manifest only | Reconcile config (routes / env / secrets / mounts / limits) | 1-3 s; +container restart if runtime flags changed |
| Code or Dockerfile changed | Build new image, blue/green swap, reconcile config | 5-90 s depending on layer cache |

This collapses the wrangler-style "many imperative verbs" surface into one
declarative verb that converges actual state to the manifest's intent. It
matches `fly deploy`. The user's mental model is uniform: edit manifest, run
deploy, the system figures out the cheapest path.

The helper persists the `(git_sha, manifest_hash)` of the last successful
deploy in its per-app state file (extending the ADR-0002 schema).

### 10. Container deploy lifecycle (full-build path)

When Section 9 selects the full-build mode:

1. **Client:** `git archive HEAD` (or worktree tar for `--dirty`) → tarball.
2. **Client → helper (SSH):** stream tarball; helper runs
   `systemd-run --scope -p CPUQuota=75% podman build -t
   simple-vps/<app>:<sha> --label app=<name> --label
   simple_vps_release=<sha> -`. The CPUQuota cap bounds contention with
   apps already serving traffic on the same host.
3. **Helper:** `podman run --name app-<name>-new` with the Section 7
   security floor, `--env-file /var/apps/<app>/shared/.env`, `--mount` for
   `/var/apps/<app>/shared` and any declared `mounts`.
4. **Health check:** `curl 127.0.0.1:<service-port><healthcheck>` until
   success or timeout.
5. **Swap:** rewrite Caddy upstream from `app-<name>` to `app-<name>-new`,
   `caddy reload`.
6. **Reap old:** stop the previous container, rename `app-<name>-new` to
   `app-<name>`.
7. **Reconcile:** apply manifest routes (Section 11), record new
   `(git_sha, manifest_hash)` in helper state, untag stale images per
   Section 6, `podman image prune -f`.

Health-check failure: stop `app-<name>-new`, leave the previous container
serving traffic, fail the deploy. No state mutation.

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

simple-vps does **not** run any build for static apps. The user produces the
directory however they want (`bun run build`, `npm run build`, Astro, Hugo,
plain HTML) before calling `simple-vps deploy`. The manifest's `static`
field points at the produced directory.

This is deliberate: it keeps simple-vps language- and build-tool-agnostic
for static deploys, just as the Dockerfile owns the build for container
apps. simple-vps owns deployment, not source-to-artifact transformation.

Lifecycle:

1. **Client:** copy the directory at the manifest's `static` path → tarball.
2. **Client → helper (SSH):** stream tarball, extract to
   `/var/apps/<name>/shared/web/` (atomic swap via a sibling temp dir +
   rename).
3. **Helper:** reconcile manifest routes (Caddy `file_server` directive
   pointing at `/var/apps/<name>/shared/web/`).
4. `caddy reload`.

No container, no port, no health check. The "release" is the directory
content; rollback is `git checkout <sha>` and `simple-vps deploy` (since
the user's git history holds the build inputs, and the tool just ships).

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

- **Adds:** Podman install via the official Ubuntu apt repository, key
  fingerprint pinned per ADR-0003.
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
