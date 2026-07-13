# Changelog

## v0.4.1

Security hardening across the agent and box-update surfaces the v0.4.0 features
lean on. Nothing here changes a command you run; it changes what a compromised
or over-eager key can do with one.

### Security

- Agent keys now resolve to their member and role by the SSH key that actually
  authenticated, never by a `--member` name the caller supplies. Two keys
  enrolled under one name with different roles can no longer let an agent key
  act as the owner; enrollment rejects the role conflict up front.
- `ship box update` sends a version, and the box downloads and checksum-verifies
  that official release itself instead of running bytes the client uploads. An
  owner-approved agent can move a box only to a genuine release, never to an
  arbitrary root binary. See ADR-0011.

### Fixed

- Box updates now take a box-wide lock, journal the attempt before mutating, and
  a new `box_update` doctor check flags an update that started without
  completing or a helper whose version has drifted from its own artifacts.
- Version comparison honors semantic-version pre-release ordering, so
  `v0.4.1-rc1` sorts before `v0.4.1`.
- `SHIP_LINUX_HELPER` overrides must be an ELF binary matching the target
  architecture.
- A restore validates the backup manifest before swapping live `/data`, so a
  corrupt backup leaves current data intact.
- Destroying a preview environment removes its share token, and the app's
  generated preview-protection credentials are removed with the app's last
  environment.

## v0.4.0

Protected Previews and share links keep unfinished work in your team's hands:
one generated team password, one CI bypass token, one capability link for an
outsider — no accounts, no dashboard. Box skew now has one update command;
each box has one pager.

### Added

- `[previews] protected = true` puts every Preview URL behind generated HTTP
  basic auth (`team` is the username). `ship preview password` prints the team
  password and automation bypass token; `--rotate` changes only the password.
  Production can never get this auth.
- `ship share` prints one capability URL for the current protected Preview. It
  grants the recipient's browser access to the clean URL; re-running prints the
  same active link, and `ship share --rm` revokes it. Links die with Previews.
- `ship box status [<box>] [--json]` reports helper/client version, disk, apps,
  and pending approvals. `ship box update [<box>]` converges the helper and its
  version-owned artifacts to the client version.
- `ship box notify [<box> [<url>]]` reads or sets the one box pager URL; `--rm`
  clears it. Deploy and Preview reaper events stay on the app `notify` URL;
  doctor degradation and approval requests now go once to the box URL.

### Fixed

- `ship box rm` now removes app-wide Preview protection credentials with the
  app.
- Failed deploys restore protected Caddy fragment permissions correctly.
- Caddy reloads are serialized box-wide, preventing stale configuration races
  during concurrent deploys, share-link changes, and Preview reaping.
- Source-built helpers are stamped with the client version, so version skew is
  reported correctly.
- `ship logs --tail 0` now returns zero historical lines instead of the default
  tail.
- Failed restores leave live `/data` intact, and `ship box setup` works again
  with a non-root bootstrap user after an earlier setup.

## v0.3.0

Data forks — test against real prod-shaped data in a throwaway sandbox.
A PaaS cannot do this; it does not own your data. With the SQLite
doctrine a fork is a file copy, so it is RAM-cheap.

### Added

- `ship data fork` — from a preview branch, copies prod's `/data` into
  this preview and bounces it, so the preview runs the same release
  against prod-shaped data. SQLite files (extension + header magic) copy
  with `VACUUM INTO` (consistent under prod's live writes); everything
  else with `cp -a`. Prod is strictly read-only. The fork is built in a
  temp dir and committed with an atomic `renameat2(RENAME_EXCHANGE)`, so
  a crash before the swap leaves the preview's data intact. Re-running
  refreshes.
- `ship data rm` — resets this preview's `/data` to empty and bounces it.
- Guards: preview branches only (production is refused,
  `data_fork_on_production`); shipper/owner roles, agent →
  `approval_required` (reading prod data is above the agent default). A
  managed `DATABASE_URL` is untouched; a `/data` with no SQLite copies
  the remaining files.
- `box doctor` verifies `sqlite3` is available on the box.

## v0.2.2

### Fixed

- Teammates can ship: daily verbs now trust a box's host key on first
  contact (`accept-new`) and pin it, instead of refusing any box the
  local machine had not personally run `box setup` against. A *changed*
  key is still refused (`host_key_changed`) — MITM/rebuild protection is
  unchanged. Regression from v0.2.1's strict checking; a member added via
  `ship member add` never runs `box setup`, so their first `ship` was
  wrongly refused.

### Changed

- Proactive image pruning: after a successful, healthy deploy ship prunes
  that environment's old release images — prod keeps 5, previews keep 2,
  the live release is always kept — and the preview reaper purges a
  reaped env's images. Best-effort (never fails a deploy); no
  configuration (ADR-0010). `box doctor`'s disk check stays the backstop.

## v0.2.1

### Changed

- Ship SSH now uses `~/.config/ship/known_hosts` with strict host-key checking
  for daily commands. `box setup` re-pins rebuilt boxes after successful
  provisioning, and `ship box forget <box>` removes a local pin.
- Targetless box verbs list known boxes from the ship known-hosts file; the
  old `~/.config/ship/boxes` memo is gone.

## v0.2.0

Members with roles — the trust layer the resident depends on.

### Added

- Three fixed roles (`owner`, `shipper`, `agent`) on `ship member add
  --role`, default `shipper`; `box setup` enrolls the first member as
  `owner`. `member ls` shows roles.
- Helper-enforced role matrix: owner unrestricted; shipper runs the
  daily loop but not member management, restore, or rm prod; agent
  gets preview-only mutations and reads everywhere.
- Approval loop: an out-of-role request mints a 15-minute request and
  returns `approval_required` with the literal `ship approve <id>`;
  bare `ship approve` lists pending, `ship approve <id>` grants
  one-shot (owner/shipper only); a `notify` event `approval_requested`
  carries the command. Expiry is the only deny.
- Agent keys are pinned by sshd: their authorized_keys entries carry a
  forced `agent-shell` command that fixes the member identity, allows
  only the ship protocol and jailed deploy staging, and refuses
  interactive sessions, arbitrary commands, and injection attempts.
- Eval scenario 7: an agent recovering from `approval_required` via a
  human approve.
- Host-only box addressing with a hand-editable `~/.config/ship/boxes`
  memo; targetless box verbs refuse and list known boxes.

### Changed

- `box setup` narration prints decisions and non-defaults only — no
  internal unix users, no duplicate header, no default-off lines.
- `ship.toml` `box` is a host (`box = "1.2.3.4"`); `user@host` is a
  manifest error. `deploy@` is gone from every suggestion and doc.

## v0.1.1

The first-contact release, driven by real first-time usage.

### Added

- Ship identity: the first command that needs SSH creates
  `~/.ssh/ship` once per machine (name derived from git config,
  narrated in one line, never a prompt); every ship connection pins
  it. `SHIP_SSH_KEY` still overrides.
- `ship member add|ls|rm` — members with printed SHA256 fingerprints,
  GitHub key fetch (`member add <username>`), dedupe, and a last-key
  lockout guard. Replaces `box add-key`.
- `ship box setup` narrates each decision with its alternative flag,
  truthfully ordered: identity, connected-as, ingress, admin, and
  `member added` only after enrollment actually executes.
- Password-provisioned boxes get a one-command bridge in the error
  (`ssh-copy-id -i ~/.ssh/ship.pub root@<ip>`); hardening then
  disables password login permanently.

### Changed

- `ship box init` is now `ship box setup`; setup enrolls your ship
  identity as the box's first member and never bulk-imports bootstrap
  keys. Re-running setup preserves existing members (additive
  enrollment, shared with member add).
- `ship box setup user@host` sets the bootstrap user from the target;
  a conflicting `--bootstrap-user` is a usage error.
- `SHIP_URL`, `SHIP_BRANCH`, `SHIP_ENV`, `SHIP_RELEASE` are injected
  into every release process (previously exec-only).
- `ship logs` distinguishes empty success (`no log lines yet`,
  `lines: []`) from real output; status JSON unified on `class`.
- Every `box setup` failure carries a stable error code with a
  runnable remediation (the hostinstall error sweep).

### Removed

- `box add-key`, `box init`, the `~/.ssh/ship-deploy` magic default,
  and `--shared-key` — no aliases.

## v0.1.0

### Added

- New `ship` CLI surface: running `ship` in a repo deploys the current Git
  branch, with the branch acting as the environment selector.
- Production/Preview model: `production_branch` maps to Production, every other
  branch maps to a Preview with a persisted branch mapping, generated URL, TTL,
  `ship pin`, `ship unpin`, and the host-side preview reaper.
- Successful non-JSON deploys now print only the live URL to stdout; progress,
  diagnostics, and next steps stay on stderr.
- Deploy journals and `ship why` record/explain failed deploy attempts,
  including release/probe failures and the previous release that stayed live.
- `notify` webhook support for deploy, recovery, reaper, and doctor events.
- Doctor v2 through `ship box doctor`, backed by structured host checks and
  `doctor.json` state.
- `ship exec -- <cmd...>` for one-off commands inside the current branch
  environment with resolved env and `/data` mounted.
- `docs/AGENT.md`, `ship docs`, `ship help <verb> --json`, and static
  bash/zsh/fish completions for agents and shells.
- Agent eval suite covering missing secrets, failing release commands, probe
  failures, missing Dockerfiles, expired preview references, and dirty branch
  state.
- Secret scoping for Production, shared Preview, and branch Preview values;
  Preview never falls back to Production. `ship secret set --from <file>` adds
  dotenv bulk import with optional `--replace`.
- Box fleet and access commands: `ship box ls`, `ship box rm`, and
  `ship member add|ls|rm`.
- `[env.preview]` manifest overlay for Preview-only environment variables.
- `install.sh` for local CLI installation from GitHub release assets, with
  checksum verification and `latest` resolution through `fprl/ship`.

### Changed

- The project was renamed from Simple VPS to ship: the module is now
  `github.com/fprl/ship`, the binary is `ship`, the manifest is `ship.toml`,
  release assets are named `ship-<os>-<arch>`, and on-box identifiers use
  `/etc/ship`, `/tmp/ship-deploy`, `/run/ship/locks`, `/usr/local/bin/ship`,
  `/var/apps/<app>.<env>`, and `ship/*` image names.
- The manifest is repo-first: `box` replaces the old server wording, `[env]`
  replaces `[vars]`, `[env.preview]` handles Preview overrides, and top-level
  `release`, `probe`, and `notify` replace the old deploy-focused shape.
- Host operations moved behind the `box` noun: install/converge, diagnostics,
  fleet reads, app removal, and deploy-key authorization are all `ship box ...`.
- Backups are now `ship save` and `ship restore --from ...`, aligned with the
  branch-selected environment model.

### Breaking

- This is a new product surface. The old `simple-vps` CLI and compatibility
  aliases are gone: no `simple-vps` binary, no public `deploy`, `check`,
  `setup`, `restart`, `destroy`, `app`, or `host` command groups, no `--env`
  environment selector, and no positional environment arguments.
- The old manifest name and fields are gone: `simple-vps.toml`, `[vars]`,
  `[deploy]`, `server`, `secret list`, `secret put`, `backup ...`, and
  environment-specific manifest tables from the Simple VPS line are not carried
  forward.

## v0.7.0 - 2026-05-30

### Added

- First-run `init` next steps now include the required Git init/add/commit flow.
- `check --env` now lists required `@secret` keys with exact `secret set`
  commands without requiring remote secret presence.
- Django + SQLite example showing `/data` persistence and `[deploy].release`
  migrations.
- Optional Django coverage in the example matrix smoke.

### Changed

- Deploy sudoers now grants only the app lifecycle plus host status/doctor
  server APIs, and the client invokes that absolute server API path with
  non-interactive `sudo -n` over SSH `BatchMode`.
- `check --env` now reports local deploy checks instead of implying remote
  deploy state.
- Host install output prints exact next commands for host status and app init,
  plus a deploy-key env var only when the deploy key is not the default path.
- `install.sh` is now the local CLI installer; VPS provisioning starts with
  `simple-vps host install`.
- `host install` now defaults the deploy public key to
  `~/.ssh/simple-vps-deploy.pub` when present, and app commands automatically
  use the matching private key at `~/.ssh/simple-vps-deploy`.
- Remote host install accepts new SSH host keys for never-seen VPSes while
  still rejecting changed remembered keys.
- GitHub Actions workflows use `actions/checkout@v5` and
  `actions/setup-go@v6`.
- Getting started and README now lead with the tested first-run flow and
  simpler host install command.
- `deploy --env` now prepares a missing app environment automatically before
  upload/build/routing; `setup` is hidden as a repair command.
- Top-level help groups project, host, and global commands and keeps nested
  subcommands under their parent command help.

### Fixed

- Failed Caddy validate/reload now restores the previous runtime env file,
  current manifest, static pointers, and stopped worker containers.
- Release commands now time out after 10 minutes instead of holding the deploy
  lock forever.
- Release command failure diagnostics now say the failure happened before the
  traffic switch.
- Deploy preflight now uses typed remote issue codes, so missing secrets still
  block before any app-env mutation while a missing app env can be prepared
  safely.
- Project commands now explain missing `simple-vps.toml` with `--config` and
  `init` guidance instead of surfacing a low-level manifest read error.
- Running `simple-vps` with no arguments now shows clean top-level help without
  an "expected one of" parser error.
- Failed deploys now clean up their uploaded remote source directory before
  exiting.
- Failed release commands restore the previous runtime env file before
  exiting.
- Fake VPS smoke covers the release-command failure path and asserts old
  traffic remains active.

### Removed

- Removed the `SIMPLE_VPS_KNOWN_HOSTS` app-command path; Simple VPS now uses
  normal OpenSSH `known_hosts`.

## v0.6.0 - 2026-05-30

### Added

- Shared local deploy diagnostics used by both `check --env` and `deploy`.
- Read-only remote deploy preflight before upload, build, identity creation, or
  route/container mutation.
- Dirty release IDs with base commit and nanosecond created time, plus release
  metadata that makes dirty status visible.
- Real example matrix smoke for PHP, Hono/Bun, mixed API/static, and Astro
  static apps.

### Changed

- Backup, restore, rollback, status, and app list now require release metadata
  snapshots instead of tolerating pre-metadata release shapes.
- Deploy now writes release metadata before runtime mutation and fails on
  mismatched manifest app names.
- Same-release redeploys now start a replacement web container and reload Caddy
  to that instance before removing the previous routed container.

### Fixed

- The direct release download docs now spell out redirect-following curl flags.

### Removed

- Removed the stale real-box results log that documented old command and layout
  shapes.
- Removed the legacy host-install `--ssh-public-key-file` flag; use
  `--operator-ssh-public-key-file` and `--deploy-ssh-public-key-file`.

## v0.5.0 - 2026-05-30

### Added

- Manifest v2 with `[processes.*]`, `[vars]`, route-level `serve`, redirects,
  per-process resources, route TLS mode, and `[deploy].release`.
- Static-only and mixed container/static deploys, including ignored/generated
  static directories in release artifacts and rollback snapshots.
- Flat env roots at `/var/apps/<app>.<env>/`, runtime env files under
  `runtime/.env`, durable app data under `data/`, and derived infra IDs for
  users, networks, containers, routes, and locks.
- Repo-centric CLI contract with required `--env`, `--config`, `secret set`,
  `backup create/list/rm`, `restart`, `logs`, `ssh`, and `app list`.
- `simple-vps init` templates for `container`, `static`, `php`, and `hono`.
- Release asset publishing, checksum verification, private-release installer
  support, and a scripted fresh-VPS release smoke.

### Changed

- Web deploys now start the next versioned container, health-check it, reload
  Caddy to the new upstream, then remove old containers.
- Backups snapshot app data, active static release assets, applied manifest
  snapshots, and secrets while keeping generated runtime files out of user data.
- Rollback re-applies the selected release snapshot and does not mutate current
  `/data`, current secrets, or rerun `[deploy].release`.
- Docs now lead with the current v0.5.0 manifest/CLI contract, getting-started
  path, and release checklist.

### Removed

- Removed the old public manifest surface: `[services.*]`, route `service`,
  route `type`, `[env.<name>.env]`, `healthcheck`, `healthcheck_status`,
  public `tmpfs`, `net_bind_service`, and nested
  `/var/apps/<app>/<env>/shared`.
- Removed positional-env command aliases and `secret put`.

## v0.5.0-rc4 - 2026-05-30

### Added

- Fresh-VPS matrix proof for Hono/Bun, plain PHP with secrets and `/data`,
  real Astro static output, and mixed API plus `/docs` static routing.

### Changed

- The Astro static example is now a real Astro app with framework build files
  instead of a hand-written HTML placeholder.
- Public install and release docs now point at `v0.5.0-rc4`.
- `install.sh` defaults to `v0.5.0-rc4`.

### Fixed

- The scripted release smoke refreshes `known_hosts` by default for disposable
  rebuilt VPS hosts.
- Private tagged installer downloads now use the GitHub Contents API path when
  authenticated.

## v0.5.0-rc3 - 2026-05-29

### Added

- `simple-vps init` now scaffolds deployable `container`, `static`, `php`, and
  `hono` templates with explicit `--name`, `--server`, `--host`, and `--port`
  knobs.

## v0.5.0-rc2 - 2026-05-29

### Added

- Plain PHP example app with a Dockerfile-backed HTTP process, `/health`, a
  secret reference, and `/data` path convention.
- Real VPS smoke coverage for Hono/Bun, PHP, static-only, and mixed
  container/static deploys on Hetzner Ubuntu 26.04.

### Changed

- Documented the `v0.5.0-rc1` release installer smoke and private-repo
  installer fetch path.
- Replaced the temporary primitive-freeze review brief with ADR-0009, locking
  the v1 CLI and primitive contract.
- Public app commands now use required `--env`/`-e` flags instead of positional
  env arguments, accept `--config` for monorepos, and expose explicit
  `backup create/list/rm` subcommands.
- Clarified that Postgres/Redis/object-storage provisioning is outside the v1
  app primitive; use external/managed/manual services and pass URLs as secrets.
- Updated release and smoke docs for `v0.5.0-rc2`, Ubuntu 24.04/26.04, and
  the PHP example matrix.

## v0.5.0-rc1 - 2026-05-29

### Added

- Manifest v2 static-only deploys with `serve = "dist"` routes, host-side
  static releases, Caddy file serving, `app list` visibility, backup, destroy,
  restore, and rollback coverage.
- Mixed container/static apps: one release can now include the app image plus
  route-level static snapshots, with rollback and restore moving both together.
- `[deploy].release` for deploy-time migration commands in container apps.
- Flat env roots at `/var/apps/<app>.<env>/` with `data/`, `runtime/`, and
  `static/` directories plus a durable `simple-vps.json` identity anchor.
- Example apps for container-only, static-only, and mixed container/static
  deploys.

### Changed

- Public manifest shape now uses `[processes.*]`, `[vars]`,
  `[env.<name>.vars]`, route `process = "web"`, and `health = "/health"`.
- Runtime identity now uses deterministic derived infra IDs for Linux users,
  Podman networks, containers, Caddy fragments, and locks while keeping host
  paths readable.
- Web process deploys start versioned containers, verify health, reload Caddy
  to the next container, then remove the old container.
- Secrets are written with `simple-vps secret set`; runtime env files now live
  under `runtime/.env` instead of app data.
- Backups now snapshot `data/` and active static release assets rather than
  generated runtime files.
- Static route deploys include ignored/generated `serve` directories in the
  uploaded artifact, and static bytes participate in release IDs.
- Release-candidate checklist documents local, fake-VPS, release-build, and
  real-VPS smoke steps.
- Release publishing now runs from a tag-driven GitHub Actions workflow that
  builds checksummed assets and uploads them without local `gh` credentials.

### Removed

- Removed the old public manifest surface: `[services.*]`, route `service`,
  route `type`, `[env.<name>.env]`, `healthcheck`, `healthcheck_status`,
  public `tmpfs`, and `net_bind_service`.
- Removed the nested `/var/apps/<app>/<env>/shared` runtime/data layout.

## v0.4.3 - 2026-05-28

### Added

- `simple-vps app list [--server ...] [--json]` and the matching
  `server app list` helper command. App discovery now comes from Podman
  labels instead of the deleted legacy app/route registries.
- `simple-vps deploy <env> --rebuild`, which passes
  `--no-cache --pull=always` to host-side `podman build`.
- `simple-vps host install --ingress public|cloudflare|private` and
  `--admin public-ssh|tailscale` presets.
- `simple-vps rollback <env> [release] [--json]` and the matching
  `server app rollback` helper command for local image-based rollback.
- Local `simple-vps backup`, `backup list`, `backup rm`, and `restore`
  primitives covering shared data, applied manifest, secrets, and release
  metadata.

### Changed

- Spec installation examples now use the shipped raw-GitHub installer URL
  instead of the unprovisioned `simple-vps.dev/install.sh` placeholder.
- Host install defaults now use public ingress and public SSH admin access;
  Cloudflare Tunnel and Tailscale are enabled through the new presets.
- Removed the half-shipped static app manifest surface. `static = "..."`
  and `type = "static"` are now rejected until static deploys have an actual
  implementation.

### Fixed

- Local builds from `git describe` output such as `v0.4.2-7-g<sha>` no
  longer try to download nonexistent GitHub release helper assets during
  remote host install.

## v0.4.2 - 2026-05-28

### Fixed

- Release helper downloads now verify `simple-vps-linux-<arch>` against the
  release `SHA256SUMS` file before copying the helper to a VPS.

### Changed

- `install.sh` now detects the install host OS/architecture, downloads the
  matching release binary by default when no local build is available, and
  verifies it against `SHA256SUMS`.

## v0.4.1 - 2026-05-28

### Fixed

- Remote `host install` now works from a standalone downloaded release binary.
  The installer looks for a matching `simple-vps-linux-<arch>` helper beside
  the current binary or in `SIMPLE_VPS_HELPER_DIR`; if none exists and the
  current binary has a release version, it downloads the matching Linux helper
  from the GitHub release assets. Private release asset downloads honor
  `SIMPLE_VPS_RELEASE_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN`.

### Changed

- README install instructions now start from release binaries instead of a
  source checkout.

## v0.4.0 - 2026-05-28

This is the first real end-to-end Go implementation cut after the container
runtime pivot. It replaces the old pre-cutover shape with one root Go binary
that serves the public CLI, host installer, and privileged server API.

### Added

- Ubuntu 24.04 host install/converge through `simple-vps host install`.
- Podman-based container deploys from a required Dockerfile.
- Derived host identity under `/etc/simple-vps/apps/<app>/<env>.json` and a
  privileged `simple-vps-server` helper invoked through restricted sudo.
- Public Caddy ingress from generated route fragments under
  `/etc/caddy/conf.d`.
- SSH-based deploy flow that streams a source tarball, builds on the VPS, and
  starts containers through the helper.

### Changed

- Deployment now requires a `Dockerfile`. Legacy non-container deploy paths,
  runtime adapters, and framework detection were removed.
- Remote provision now uses a root-owned helper plus a deploy user instead of
  broad SSH privileges.
- The helper owns app mutation with file locks, route generation, container
  lifecycle, and rollback state.

### Removed

- Removed the legacy Node/TypeScript implementation, adapters, and generated
  `runtime/` bundle.
- Removed Terraform/Ansible provisioning paths and generated infra templates.
