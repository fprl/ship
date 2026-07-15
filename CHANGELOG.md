# Changelog

## Unreleased

A leaner ship that says the right thing when it fails. A whole-repo
compat-and-quality sweep deleted every shim, fallback, and dead branch
that only a never-shipped version could have needed (~1,400 lines gone),
and the bugs the sweep tripped over got fixed for real.

### Fixed

- A failed `ship rollback` always leaves the previous release serving:
  old workers are stopped, not deleted, and every failure path restores
  traffic, containers, env, and manifest — restart problems included in
  the error, and a journal hiccup after a successful switch is a warning,
  not a false failure.
- `ship box update` / `box setup` converge now actually reapply the
  member key file and restart a running service whose unit file changed
  (previously both quietly kept the old state until reboot).
- `ship exec -- --json` no longer flips ship's own error output to JSON
  — flags after `--` belong to your command.
- `ship box setup --check` works on a box that has never seen ship
  (missing ufw is a pending change, not an error), and its error copy no
  longer claims a fresh box "predates" anything.
- Production `ship data restore` says restore — its own error code and a
  runnable `--confirm` remediation instead of borrowing `ship rm`'s
  wording; failed remote commands now suggest a retry command that
  matches what you ran.

### Changed

- A key without a member record is not a member (ADR-0020): the store is
  the identity truth, `authorized_keys` a rendered artifact; unrecorded
  keys don't authorize and are dropped on the next render. `member add`
  writes the record first, `box setup` is a real recovery path (re-runs
  re-enroll the keys it's given — including refreshing their recorded
  name — and rebuild a corrupt register with a loud warning), and the
  deploy-preparation verbs (`setup-env`, preview create) answer to the
  same role rules as shipping.
- `ship init` scaffolds and nothing else: `--name`/`--box`/`--host`
  deleted per spec; edit ship.toml.
- Missing required arguments print ship's own answer with a runnable
  next step instead of the parser's generic error (July 14 packet).
- Hidden helper wire verbs renamed to match the public grammar
  (`app ls`, `approval ls|grant`); update your box after installing.

## v0.6.0

One grammar, one webhook, previews on your own domain, and member
onboarding you can read before you trust it. The v0.5.0 collection verbs
die the day after they shipped — renames are free until someone depends
on them, and now the grammar is settled: `resource + subverb`, everywhere.

### Added

- `[preview]` in ship.toml — put preview URLs on your own domain with one
  wildcard DNS record: `base = "preview.example.com"` makes previews
  `<app>-<branch>-<id>.preview.example.com`, and `aliases = true` adds a
  stable `<branch>.preview.example.com` alias per branch — same capability
  protection, updated on every deploy, removed with the env. Omit the
  table and sslip.io behavior is unchanged.
- Show-first member onboarding: `ship box member add https://github.com/alice.keys
  <box> --name alice` fetches and prints the keys, fingerprints, and source
  — writing nothing — then hands you the exact confirm command bound to a
  digest of everything you just reviewed. Confirm refetches and must match
  byte-for-byte. Literal keys and `.pub` files still write immediately.
  `--name` is required everywhere; identity never derives from key
  comments, filenames, or usernames again.
- `ship box status` now counts people: `members: 2 (1 owners)` in text,
  `members: {total, owners}` in JSON — distinct names, not keys.

### Changed

- One CLI grammar (breaking): `ship box member ls`, `ship box app ls`,
  `ship box app rm`, `ship box approval ls`, `ship box approval grant`.
  The bare-plural and standalone forms (`box members`, `box apps`,
  `box approvals`, `box approve`, `box rm`) are deleted, not aliased.
- The webhook is named `webhook` (breaking): manifest key `notify` →
  `webhook`, `ship box notify` → `ship box webhook`, config key
  `notify.url` → `webhook.url`. Same two-scope event routing.
- Bare forge usernames are gone from `box member add`: for a command that
  grants deploy access, the URL is the provenance — paste the keys-URL.
- `ship exec -- <cmd>` consumes the `--` separator instead of passing it
  into the container (found in the v0.5.0 real-box gate).
- Every member and app-removal error now prints a runnable command with
  your real box, name, and source filled in — no `<box>` placeholders when
  ship knows the value.
- `--json` list fields are always arrays, never null: `status` envs,
  `box app ls` processes and routes included.
- Error codes renamed with their verbs: `box_app_rm_confirmation_required`
  (was `box_rm_confirmation_required`), `keys_url_unavailable`
  (was `github_keys_unavailable`).
- Member enrollment converges: re-running `box member add` repairs a
  half-completed earlier run; `box setup` prints `enrolled you as <name>
  (owner)` only when it actually enrolled you.

## v0.5.0

The simplification arc lands: data-only backups, one box config file, one
preview capability, branch-scoped secrets — and a CLI surface rebuilt around
where things actually live. Verbs moved, the production env got its real
name, and generated URLs now lead with your app instead of an env word.
Breaking on most user-visible surfaces; there is nothing to migrate.

### Added

- `ship data save | restore | ls` — stream an environment's `/data` to your
  laptop as a snapshot and put it back later. Snapshots carry metadata and
  data only; secrets are never included. Default names come from the
  archive's own metadata and a save never overwrites an existing snapshot.
- `ship box config <box> [set|unset]` — one schema-validated box config
  file (unknown keys refuse, atomic writes, per-key roles and approvals).
  `ship box notify` is sugar over its `notify.url` key.
- One preview capability: every Preview is protected by a single token that
  works as a URL parameter, a cookie, or an `x-ship-capability` header;
  `ship preview share --rotate` kills the old link. The `[previews]` knob,
  team password, and bypass token are gone.
- `ship box status` — cheap one-screen summary: version skew with the exact
  update command, disk, app count, pending approvals, and the last
  doctor-timer result with its age. `ship box apps` is the table;
  `ship box doctor` is the probe.
- Approval grant integrity: every request records the role the denied action
  requires, the approver's role must cover it, nobody can approve their own
  request, and granting refreshes the 15-minute window.
- The box records its client-routable address at `box setup`, so every
  approval remediation and webhook prints a command you can paste from any
  machine: `ship box approve <id> <box>`.

### Changed

- Approvals and members live on the box, so their verbs do too:
  `ship box approvals`, `ship box approve <id> [<box>]`,
  `ship box member add|rm`, `ship box members`. Top-level `approve` and
  `member` verbs are gone. Run them from anywhere by naming the box.
- Generated URLs are app-first and env words never appear: production is
  `<app>.<ip>.sslip.io`, previews are `<app>-<branch>-<id>.<ip>.sslip.io`.
  Two routeless production apps on one box can no longer synthesize the
  same host; the app name is never truncated.
- The production environment is named `production` (was `prod`) in
  directories, snapshot names, JSON, and approval rows.
- `ship data rm` is now `ship data reset` — it empties a Preview's `/data`;
  it never destroyed an environment.
- Bare `ship secret set KEY` follows branch=env: it targets the branch you
  are on instead of silently writing production from a feature branch.
- `ship init` writes a manifest with no `[routes]` unless you pass
  `--host`; the first deploy prints the automatic URL and the output tells
  you how to add a real domain later.
- Secrets keep exactly one trailing newline and refuse embedded newlines
  (the container env-file format cannot carry them; encode multi-line
  material instead).
- Deploys, data commands, and rotation report honestly at every point of no
  return: a live deploy is never journaled as aborted, partial container
  stops are always restarted, and post-success URL-lookup failures warn
  instead of reporting a failed mutation.
- `data fork`/`data reset` print exactly the Preview URL on stdout;
  narration, the PII note, and warnings go to stderr. Helper warnings on
  successful commands now reach your terminal.

### Removed

- Whole-app save/restore and its backup format, `@secret:NAME` aliasing,
  `--include-dotenv`, `ship init` starter templates, multi-provider setup
  paths and topology flags, `box ls` (now `box apps`), and the unused
  box-config apply-mode scaffolding.

## v0.4.2

Deploys start faster and failures explain themselves. No command changes:
this release cuts SSH round-trips before a deploy and makes error paths say
what actually happened and what to run next.

### Changed

- Deploy preflight makes one SSH round-trip instead of three before any work
  starts.

### Fixed

- When a Caddy reload fails AND restoring the previous route also fails, every
  path (deploy, rollback, restore, destroy, preview protection) now reports
  "manual fix required" with the fragment path. This warning was silently
  dropped on the reload stage in most paths.
- Remote command failures carry the failing command's own next-step hint
  instead of a one-size-fits-all `ship box doctor`.
- A helper newer than the client can introduce new error codes without
  crashing the client: unknown codes degrade to a plain operation error that
  names the unrecognized code.
- `ship box status` and deploy preflight preserve a host-key-changed failure
  instead of flattening it to a generic error, and host-key detection reads
  only SSH's stderr, so remote output printing "offending key" can't fake it.
- Deploy preflight rejects a response for the wrong app or environment.
- SSH public keys must be real key material everywhere: `member add` and
  `box setup` wire-parse the key body and match its declared type, rejecting
  up front a key OpenSSH could never use instead of installing it and
  reporting a member who can't actually connect.
- One SSH key parser, one version comparator, one remote-error decoder across
  client and box; several drifted copies consolidated.

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
