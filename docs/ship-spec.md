# ship — implementation specification (v1)

**Status: authoritative.** This is the only spec. The pre-ship contract
(`SPEC.md`) was deleted after the adaptation completed; engine-behavior
questions defer to the code and `docs/adr/`.

## North star

Big-org tooling optimizes coordination at scale. A 1–5 team's real
constraints are **fear** (of breaking prod), **attention** (ops tax), and
**trust** (lock-in) — scale is a non-problem; a $20 box is 8 vCPU/16 GB.
Every decision below optimizes those three. The end state:

1. **Your company is a git org + one box.** Repos with manifests; the box
   materializes them; reverting anything is `git revert`.
2. **The PR is a URL, not a diff.** Agents ship; humans review the running
   preview and say yes. Merge = promote. Branch=env is this, declared.
3. **Your database is a file.** State is SQLite under `/data` or a managed
   URL — the box never runs a database server. A 1–5 team gets very far on
   SQLite-everywhere; ship is its missing deploy story: forkable per
   branch, streamed to backup, never locked in (RFD-0001).
4. **The box is a teammate, not infrastructure.** It reports in, explains
   itself (`why`), and pages your agent with the diagnosis and fix
   attached (journal + webhook + doctor here; resident loop in RFD-0002).
5. **Zero security decisions.** One hardened shape: public ingress,
   keys-only SSH. No topology choices at setup — never mechanisms,
   ciphers, ports, or policies (§18; tunnels return post-v1 as a single
   hardening act on the box-config foundation, §19).
6. **Exit is a feature.** Delete ship and a boring, well-configured Linux
   server keeps serving. No PaaS can say this sentence.
7. **Proof, not promises.** The agent eval suite (§9) is public evidence
   that an agent can operate the whole thing unaided.

v1 built none of this speculatively: Phases 1–3 below are the smallest
surface that points at it. Since then, members (§13, v0.2.0) and data
forks (§14, v0.3.0) promoted out of the RFDs; the v0.4 arc is §15–§17.
The remaining unscheduled bets live in `docs/rfd/` (resident,
agent-era catalog, sleeping previews).

## 0. Mission and constraints

ship is the v2 surface of the pre-existing engine in this repository.
It is **an adaptation, not a rewrite**: the deploy pipeline, host hardening,
routing, secrets storage, backup machinery (since replaced by §18's
data-only snapshots), helper privilege model, and the
fake-vps test harness are kept. What changes is the CLI surface, the
addressing model (git branch = environment), preview environments, the
output/error contract, and agent operability.

Product bar, in priority order:

1. **Simple** — five user-facing ideas: repo, box, branch, snapshot
   (a deployed release), URL. No `--env` flag exists anywhere in the new
   surface.
2. **Best-in-market UX** — measured at four moments: first run (two
   commands, no flags, HTTPS URL out), the daily loop (bare `ship`),
   failure (`ship why` explains plainly; bad deploys never take traffic),
   and agent operation (see §9).
3. **Agentic** — every behavior a coding agent needs is a tested contract,
   not documentation prose.

Non-goals for v1 (do not build): git-push transport (keep the current
rsync/tarball upload), copy-on-write data forks / rewind, provider
provisioning APIs, multi-box, dashboards, schedulers, TCP ingress,
forge/webhook integrations, mounted-cargo fast path (deferred; current
image-build deploy speed is acceptable).

Read before starting: `internal/config` (manifest schema),
`cmd/client` (CLI + deploy orchestration), `cmd/helper` (privileged on-host
API), `cmd/hostinstall`, `tests/fake-vps`. The existing helper API and
sudoers model stay as-is unless a section below says otherwise.

## 1. Naming

- Binary and product: `ship`. Update module naming minimally; keep internal
  package names where renaming is churn without value.
- On-host install path becomes `/usr/local/bin/ship`; sudoers grant updated
  accordingly. There are no external users; no compatibility shims for the
  old binary name are needed.
- Manifest file: `ship.toml` only; no legacy manifest names.

## 2. Manifest (`ship.toml`)

Extend the existing schema in `internal/config`; keep field semantics where
they already exist. Target shape:

```toml
name = "taskflow"
box  = "203.0.113.7"                # existing server field, host only
production_branch = "main"          # optional; default: main, else master

[processes]
web    = "npx react-router-serve build/server/index.js"
worker = { cmd = "node build/worker.js", preview = false }

[routes]                            # existing routes model, keyed by host[/path]
"taskflow.app"        = "web"
"taskflow.app/docs"   = { static = "docs/dist" }
"www.taskflow.app"    = { redirect = "taskflow.app" }

[env]                               # renamed from [vars], no alias kept
LOG_LEVEL    = "info"
DATABASE_URL = "@secret"            # the only secret form: secret name = var name

[env.preview]                       # optional; overlays [env] in previews only
LOG_LEVEL    = "debug"
POSTHOG_KEY  = "phc_test456"

release = "npx drizzle-kit migrate" # top-level only; the [deploy] section is gone
probe   = "/healthz"                # health check for the routed process
webhook = "https://ntfy.sh/..."     # webhook, §7
```

Rules:

- `@secret` references are whole-value only (existing behavior). The bare
  `"@secret"` form resolves to a secret named after the variable, and it
  is the **only** form — `@secret:NAME` aliasing was removed July 13
  (D5, §18): one rule, name the secret after the var. Two vars needing
  the same value set it twice.
- `production_branch` (optional) names the branch that deploys to prod;
  default `main`, else `master`.
- `[env.preview]` (optional) overlays `[env]` for **preview envs only**:
  keys merge, the preview value wins, production ignores the table
  entirely. Values follow the same rules as `[env]`, including `@secret`
  forms (resolved through the preview scope chain of §6). This is the
  committed home for values that differ by env *class* and are neither
  derivable from `SHIP_ENV` nor secret (e.g. a test analytics key). The
  key `preview` is reserved under `[env]`; no other `[env.<name>]`
  section exists.
- Named per-env var sections (`[env.<name>.vars]`) are **removed**:
  per-branch config rides the branch. The systematic per-environment
  values are injected instead (§6).
- `preview = false` on a process excludes it from preview environments.
- Per-process `resources = { memory = "512m", cpus = 0.5 }` (existing
  engine knob) is kept.
- Per-process `port` is kept (default: the Dockerfile's sole `EXPOSE`,
  else 3000). Per-process `health` is removed — the top-level `probe`
  gates the routed process; other processes just get supervision.
- `ship init` writes `ship.toml` and **nothing else** (D7, §18). The
  starter-app templates (python default, php, hono, static index.html)
  are removed: ship deploys the app you have, it is not a framework
  starter catalogue. No Dockerfile present and no static route →
  `ship` fails with code `dockerfile_missing` and a remediation that
  names both paths (write a Dockerfile; or declare a static route).
  Stack *detection* (package.json / requirements / go.mod / static dir
  writing a correct Dockerfile for an **existing** app) remains the
  valuable future — TODO(§2). Never overwrite existing files (existing
  behavior, keep).

## 3. Addressing: branch = environment

The new surface has no `--env`. Resolution rule, implemented client-side:

- Current git branch equal to `production_branch` (default `main`, else
  `master`) → env name `production` (ADR-0018: one word for the one
  concept — the env NAME and the authorization CLASS no longer differ;
  the name is internal only, since URLs are app-first per §8 and users
  never type env names).
- Any other branch → a **preview env** named from the sanitized branch name
  (`lowercase; [^a-z0-9-] → -; collapse dashes; max 28 chars`), plus a
  random 4-char suffix generated once at env creation and persisted on the
  box (so re-shipping the same branch updates the same env). Example:
  branch `feat/new-pricing` → env `feat-new-pricing-x7q2`.
- Not a git repo → hard error with remediation.
- Detached HEAD (CI checkout) → hard error unless `--branch <name>` is
  given. On deploy, `--branch` is accepted **only** in detached-HEAD
  state — with a branch checked out it is an error (the checked-out
  branch is always the truth on a laptop). Read verbs accept `--branch`
  anywhere.
- Dirty worktree: the production branch **refuses to ship** (code
  `dirty_worktree`, remediation `git commit`; no override flag). Preview
  branches ship the working tree as-is; dirty state is flagged in
  `ship status` and in the release id (existing `-dirty-` id scheme).
- Stale checkout: a prod ship requires the currently deployed commit to
  be an ancestor of HEAD (code `behind_production`, remediation
  `git pull`; deploying older code on purpose is what `ship rollback`
  is for). Previews skip this check.

Under the hood these are ordinary engine environments (isolated user,
network, data dir, secrets — all existing machinery). Mapping metadata
(branch → env name + suffix + expiry + pinned flag) is stored on the box in
the env identity file (extend the existing identity storage).

### Preview lifecycle

- Created automatically on first `ship` from a branch. No setup verb.
- **TTL 72 h** from last ship; `ship preview pin <branch>` clears expiry,
  `ship preview unpin <branch>` restores it (nested under `preview`
  since July 13 — D13, §18).
- A reaper runs on the box (systemd timer installed by `box setup`,
  invoking a new helper verb `server env reap`) and destroys expired
  preview envs — equivalent to the existing destroy path, secrets purged.
  Reap events fire the `webhook`. Production is never reaped and `ship rm` on Production
  requires `--confirm <name>` (existing destroy guard).
- Preview URL: `<app>-<branch-slug>-<id>.<base-domain>` (app-first,
  one flat label — §8/ADR-0018). Base domain: `[preview].base` when
  configured (§8, ADR-0019); otherwise sslip.io. With
  `[preview].aliases = true`, each preview is ALSO served at the
  stable `<branch-slug>.<base>` — derived automatically from the
  branch, one extra route in the same env fragment, same capability
  guard; on a slug collision the existing owner keeps the alias and
  the newcomer warns and keeps its canonical URL. Previews collapse to
  this single URL, serving the default host's routes (paths included);
  extra hosts and redirects are production-only.
- Preview processes run under default CPU/memory caps unless the process
  declares its own `resources` — a runaway preview must not starve prod.

## 4. CLI surface (complete)

```
ship                      deploy current branch; stdout = URL (see §5;
                          for previews, the URL carries the capability,
                          §15)
                          flags: --tls internal (§8), --rebuild,
                          --branch (detached HEAD only, §3), --json (§5),
                          --config <path>
                          (--include-dotenv removed July 13 — D6, §18:
                          `secret set --from .env` is the only path for
                          .env contents; .env never rides a release
                          artifact)
ship init                 scaffold ship.toml only (D7, §18; stack
                          detection TODO §2)
                          flags: --config <path>
ship help [verb] [--json] per-verb usage from the ship docs source (§9)
ship status [--json]      all live envs for this app: branch, url, release,
                          who shipped, health, age, expires/pinned, dirty hint
ship logs [process] [--follow] [--tail N]   current branch's env
ship exec -- <cmd...>     run a one-off command inside the current
                          branch's env container, with its secrets and
                          /data mounted (Heroku's `heroku run`; built
                          in Phase 2 — agents need it to inspect state
                          without ssh+podman guessing)
ship why [--json]              explain the last failed/aborted deploy (§7)
ship rollback [release]        previous release of the current branch's env;
                          release = commit short-sha (from ship status)
ship rm <branch> [--confirm <name>]   destroy an environment
ship preview share [--rotate]   print (or rotate) this preview's
                          capability URL (§15)
ship preview pin <branch> / ship preview unpin <branch>
ship secret set <KEY> [--preview|--branch <name>]   stdin-only (§6);
                          bare form targets the CURRENT BRANCH's env
                          (D4, §18) — prod is set from the production
                          branch, same rule as every other verb
ship secret ls [--json] / ship secret rm <KEY> [--preview|--branch <name>]
ship data save [--out <path>] / ship data restore <id|path> / ship data ls
                          data-only snapshots, pulled to the laptop
                          (D1, §18 — replaces whole-app save/restore)
ship ssh                  existing
ship box setup <ssh-target>   host install — ONE shape, no topology
                          flags (D10, §18: public ingress 80/443,
                          keys-only SSH; the --ingress/--admin 3×2
                          matrix and --litestream are removed)
                          (renamed from box init — two unrelated inits
                          confused; "setup" is Kamal's word).
                          IDENTITY MODEL (the login ship doesn't have):
                          the first time ship needs it, it creates the
                          laptop's ship identity — name derived from
                          `git config user.name` (fallback $USER),
                          sanitized; ed25519 key at ~/.ssh/ship with
                          the name as its comment; one narrated line,
                          never a prompt; idempotent thereafter.
                          box setup uses bootstrap access as TRANSPORT
                          ONLY (the provider-injected root key via
                          normal ssh resolution, or — for password-
                          provisioned boxes — the one-time bridge in
                          the error remediation:
                          `ssh-copy-id -i ~/.ssh/ship.pub root@<ip>`,
                          after which hardening disables passwords
                          forever), then ENROLLS the ship identity as
                          the box's first member (owner once roles
                          exist); the operator user receives the same
                          identity key. Bootstrap keys are never
                          bulk-imported as members — the door key
                          does not move in. Every ship SSH connection
                          uses `-i ~/.ssh/ship` with IdentitiesOnly
                          when the identity exists; SHIP_SSH_KEY and
                          --deploy-ssh-public-key-file (split-key/CI:
                          enrolls that key instead) override. Output
                          narrates every decision with its alternative
                          flag, wizard-grade without prompts — and
                          truthfully: each line prints when the fact
                          becomes true, never before (identity at
                          local creation; member added only after
                          enrollment actually executes on the box):
                            identity: franco (created ~/.ssh/ship)
                            connected as root (bootstrap)
                            ingress: public 80/443
                            admin: SSH keys only
                            member added: franco (SHA256:...)
                          The <ssh-target> accepts host or user@host;
                          user@host sets the bootstrap user (a
                          conflicting --bootstrap-user is a usage
                          error).
                          One sentence: ship knows who you are; setup
                          introduces you to the box; everyone else is
                          ship box member add. Members and approvals are
                          BOX-scoped (the trust boundary); secrets,
                          envs, and journals are app-scoped.
ship box member add <key|path|https-url> [<box>] --name <n>
                          authorize a teammate's SSH key. --name is
                          mandatory and feeds §7 attribution (never
                          derived from key comments). Literal key/file
                          writes immediately; an https keys-URL is
                          show-first: prints keys + fingerprints and
                          writes nothing until the digest-bound
                          --confirm (ADR-0019 §4). Bare forge
                          usernames are gone — the URL is the
                          provenance.
ship box member ls [<box>] [--json]
                          members grouped with their keys: name, role,
                          copy-pasteable short key id (12-char floor),
                          type, CURRENT marker on the connecting key.
                          Readable by every enrolled member. --json is
                          nested: members[] → keys[] (ADR-0021).
ship box member rename <old> <new> [<box>]
                          identity-only mutation: every key keeps its
                          material and role. Refuses unknown <old> or
                          a <new> that names any existing member
                          (never merges principals). Idempotent on
                          <old> == <new>. (ADR-0021)
ship box member role <name> <owner|shipper|agent> [<box>]
                          change a member's role across all their keys
                          and re-render role-dependent access lines
                          (agent keys get the forced agent-shell line).
                          Idempotent on the current role. (ADR-0021)
ship box member rm <name> [--key <id>] [<box>]
                          revoke all of a member's keys, or exactly one
                          with --key (full SHA256 fingerprint or unique
                          prefix, 12-char floor; the key must belong to
                          <name>). Key rotation is add → verify the new
                          connection → rm --key the old (no atomic
                          rotate verb: atomicity can't prove possession
                          of the new private key, ADR-0021).
                          One invariant guards every member mutation:
                          it must leave at least one effective owner
                          key (record in members.json AND line in
                          authorized_keys; stray unrecorded lines don't
                          count, ADR-0020). Roles shipped in v0.4
                          (owner / shipper / agent, §17); RFD-0003
                          records the original post-v1 plan.
ship box doctor [--json]  existing doctor, output upgraded per §9
                          BOX ADDRESSING (v0.2 polish, Franco July 8):
                          boxes are addressed by HOST only — the deploy
                          user is ship's constant, never typed
                          (ship.toml: box = "203.0.113.7"; user@ forms
                          remain only in box setup's bootstrap target).
                          Box verbs resolve their target: explicit
                          positional → app dir's ship.toml. Outside an
                          app dir with no target they ALWAYS refuse —
                          never implicit selection at any count
                          (Franco: one behavior forever; no cliff when
                          box #2 appears) — and the refusal lists the
                          known boxes from the memo file
                          ~/.config/ship/boxes (written on successful
                          setup, plain text, hand-editable, reminder
                          not resolver; deleting it costs nothing but
                          the list). In THAT refusal — where no target
                          could be resolved — the next: line uses the
                          placeholder form, next: ship box <verb>
                          <box>, never a host picked from the list
                          (Franco: the placeholder teaches the shape,
                          the list above supplies the value, and no
                          count-dependent special case exists). When
                          the box IS known — box-side remediations,
                          approval flows, any message minted with the
                          target in hand — the next: line prints the
                          fully resolved command (ADR-0018): a
                          remediation an approver pastes from chat
                          must run verbatim. <box>
                          is the placeholder everywhere a box verb
                          takes its host; box setup alone keeps
                          <ssh-target> (bootstrap may be user@host).
                          ANTI-MAGIC RULE (product-wide): whenever
                          ship fills a blank, it narrates the value
                          and its source on stderr.
                          NARRATION DIET (box setup): print decisions,
                          facts, and non-defaults only — never internal
                          unix users (operator/deploy), never
                          default-off features, never the same header
                          block twice; the full state dump belongs to
                          box doctor, not the deploy terminal.
                          BOX IDENTITY / HOST KEYS (v0.2.1): a box's SSH
                          host key is part of its identity, pinned in a
                          ship-owned file and NEVER in the user's
                          ~/.ssh/known_hosts (symmetric with the ship
                          identity — ship's SSH is fully self-contained).
                          Mechanism, not a reimplementation: every ship
                          SSH passes -o UserKnownHostsFile=
                          ~/.config/ship/known_hosts and sets
                          -o StrictHostKeyChecking per context; that file
                          also IS the known-boxes list for the
                          targetless refusal (hostnames parsed from it;
                          the plain boxes memo is subsumed). Rules:
                          (a) box setup is the trust-establishing act —
                          connects accept-new and PERSISTS the host key
                          only AFTER bootstrap auth + provisioning
                          succeed (auth is the real proof; a stranger's
                          box on a recycled IP fails auth, so re-pin buys
                          an attacker nothing); a changed key at setup =
                          a rebuild → re-pin, narrated (`host key changed
                          since last setup — re-pinning (box rebuilt?)`).
                          (b) daily verbs verify STRICT — a changed key
                          refuses (errcat host_key_changed, remediation
                          `ship box setup <ssh-target>` or investigate);
                          this is where MITM / IP-recycling is caught.
                          (c) ship box forget <box> clears the entry
                          (ssh-keygen -R on ship's file); box setup
                          re-pins next run. Rationale: ~/.ssh/known_hosts
                          made ship depend on the user's SSH history and
                          polluted it (a rebuilt box left 30 stale
                          entries in dev) and fought the
                          rebuild-a-box-freely ethos.
ship box app ls [<box>] [--json]   the box's app table (renamed from
                          box ls July 13 — D8, §18: `ls` names nothing,
                          and it is reserved for listing BOXES if
                          multi-box ever lands); --json is the box app
                          list: per-app envs, branches, urls, releases,
                          health, expiry
ship box app rm <app> [<box>] --confirm <app>   destroy an app and all
                          its envs without the repo dir (orphan
                          cleanup; same confirm guard as prod rm)
ship box forget <box>     drop this box's host-key pin from
                          ~/.config/ship/known_hosts (decommission;
                          pairs with box app rm; box setup re-establishes)
ship box status [<box>] [--json]   one-box summary: helper version vs
                          client, update hint, disk, apps, members,
                          pending approvals, last doctor result (§16)
ship box update [<box>]   converge helper + version-owned artifacts
                          to this client's version (§16)
ship box webhook <box> [<url>|--rm]  read/set/clear the box webhook
                          for box-scoped events (§17); sugar over the
                          box config key webhook.url (§19)
ship box config [<box>] [--json]      print the box's effective config
ship box config [<box>] set <key> <val> / unset <key>   per-key policy
                          enforced helper-side (§19)
ship docs                 print the agent contract (§9)
ship version
```

Removed from the public surface (engine keeps the capability): `check`
(folded into `ship` preflight and `ship init` validation), `deploy`,
`restart` (bare `ship` re-ships, `rollback` re-points — no third verb),
positional env args, `--server` on app commands (comes from `ship.toml`).

## 5. Output contract (hard rules, enforced by tests)

1. On a successful `ship`, **stdout is exactly the deployment URL** and
   nothing else. All progress, notes, and warnings go to stderr.
2. No interactive prompts anywhere. Anything that would prompt is an error
   with a remediation instead.
3. Every read verb supports `--json` with a stable schema. New for v1:
   `ship --json` (mutation) emits
   `{"url":..., "env":..., "release":..., "processes":[...], "durationMs":...}`
   on stdout.
4. Exit codes: `0` success, `1` operation failed (error object available),
   `2` usage/manifest error.
5. Error shape, everywhere, both human and `--json` forms:
   - human: one line *what failed*, one line *why/cause*, one line
     `next: <exact command>`.
   - json: `{"error":{"code":"...","message":"...","cause":"...","remediation":"..."}}`
   - Every distinct failure gets a stable `code` (snake_case), catalogued
     in one Go file so `ship docs` can list them.
6. Surface language mirrors Vercel: **Production** / **Preview** plus the
   branch name — never internal env slugs (`feat-x-x7q2`), which appear
   only inside URLs and `--json` fields.
7. During `ship`, stderr streams one line per phase with timing
   (`build 6.2s`, `release 1.1s`, `probe ok`, `live`); spinner on a TTY,
   plain lines when piped.
8. Successes also guide: when a natural next step exists, the last
   stderr line is `next: <command>` — `ship init` ends with `next: ship`;
   the first prod ship without a domain ends with the exact DNS record
   to add.

## 6. Secrets and injected variables

Engine secrets are already per-(app, env), stdin-only. New scoping layer:

- `ship secret set KEY` → targets the **current branch's env**, exactly
  like every other verb (branch=env, §3): on the production branch it
  sets the prod value; on a feature branch it sets that branch's value.
  (Changed July 13 — D4, §18. The old bare-form-means-prod rule was the
  one verb family that ignored branch=env: it silently mutated
  production from a feature branch.)
- `ship secret set KEY --preview` → sets a single shared **preview** value
  that all preview envs of the app resolve.
- `ship secret set KEY --branch <name>` → sets a value for that branch's
  env only (stored in the existing per-(app, env) secret store) — e.g. a
  pinned `staging` branch with its own credentials, or prod from a
  detached-HEAD CI checkout.
- Resolution at deploy: prod env → prod values only; preview env → its
  branch value if set, else the shared preview value. A `@secret`
  reference with no value for the target scope **fails the deploy** with
  code `secret_missing` and remediation
  `ship secret set KEY [--preview|--branch <name>]`. Preview envs must
  never receive prod values — add a fake-vps test asserting this.

Injected into every process (all envs): `SHIP_URL` (this env's own https
URL), `SHIP_BRANCH`, `SHIP_ENV` (`production` | `preview`),
`SHIP_RELEASE` (release id).

## 7. Failure UX: `why` and `webhook`

- The helper records a structured deploy journal per env (extend existing
  release metadata): outcome (`deployed | aborted_build | aborted_release |
  aborted_probe | rolled_back`), failing step (`apply | build | release |
  probe`), captured stderr tail (last ~40 lines),
  timestamps, release ids involved, and the deploying identity (SSH key
  comment + git author) — `ship status` and `why` show who shipped what.
- `ship why` renders the latest entry as plain prose: what happened, the
  probable cause (map common patterns: non-zero release command, probe
  non-200 with body snippet, image build failure), whether traffic was
  affected (with the current engine a failed probe means the old container
  kept serving — say so explicitly), and `next:` command.
- `webhook` (manifest key; ADR-0019 renamed it from `notify` — the
  configured object is a webhook everywhere the user has seen one):
  the client or helper POSTs a JSON event
  (`{app, env, event, release, summary, why, remediation, ts}`) on the
  **app-scoped events**: deploy aborted, deploy succeeded after a
  previous failure, preview reaped. `why` and `remediation` carry the
  full journal entry, so an agent receiving the webhook can act without
  first querying the box. Fire-and-forget, 2 s timeout, never fails the
  operation. Box-scoped events (doctor degraded, approval requested)
  fire the **box webhook** instead — once per event, never per app
  (§17).
- `box setup` installs a doctor timer (daily, systemd — same pattern as
  the reaper): newly degraded or failed checks fire the box webhook
  (§17) with their remediation. Set-and-forget means the box reports
  in; nobody has to remember to run doctor.

## 8. Zero-DNS URLs

When no route host is configured for the target env (typical for previews,
and for production before a domain exists), synthesize an APP-FIRST
host (ADR-0018) and route it via Caddy with a public certificate:

```
production:  <app>.<box-ip-with-dashes>.sslip.io
preview:     <app>-<branch-slug>-<id>.<box-ip-with-dashes>.sslip.io
```

Environment names never appear in URLs. The base is configurable
per app (ADR-0019):

```toml
[preview]
base    = "preview.example.com"   # bare DNS suffix; default <ip>.sslip.io
aliases = true                    # default false; adds <branch-slug>.<base>
```

`base` validation: no scheme, path, port, credentials, wildcard
prefix, or trailing dot; labels must be valid DNS; the generated full
host must fit DNS limits; lowercased. Unknown `[preview]` keys refuse.
The table is addressing policy ONLY — `processes.<n>.preview = false`
(runtime inclusion) and `[env.preview]` (env values) stay independent,
and D13's always-on preview protection is not configurable here or
anywhere. Production addressing is untouched. The app name is the box-global
identity, which is what makes the host collision-free: two routeless
production apps on one box synthesize different hosts (the old
`<envname>.` scheme made them identical). Preview label budget: the
app name is never truncated; `slug_budget = min(28, 57 - len(app))`
(app max 41 → the slug always keeps ≥ 16 chars); the persisted 4-char
id disambiguates, and its collision retry keys on the FINAL host
label, not the env name (two long slugs can truncate to one prefix).
One flat label so the identical shape rides sslip today and the
wildcard-base future (`*.preview.example.com`) tomorrow — a standard
wildcard covers exactly one label, which is why the nested
`<app>.<slug>-<id>.<base>` shape was rejected.

The synthesized route targets the process named `web`, else the sole
declared process; several processes with no `web` and no `[routes]` is
a manifest error. Document (in `ship docs` and README) that sslip.io
shares CA rate quotas and an owned wildcard domain is the steady state.
`--tls internal` remains available (existing flag) for disposable boxes.

Preflight: when a `[routes]` host does not resolve to the box's IP,
`ship` warns on stderr with the exact record to create
(`A taskflow.app → 203.0.113.7`) before attempting certificates — warn,
not fail, since DNS may still be propagating.

## 9. Agent contract

- `ship docs` prints a complete markdown usage contract embedded in the
  binary via `go:embed` (source: `docs/AGENT.md`, written as part of this
  work, ~200 lines): mental model, every verb with its JSON schema, the
  full error-code catalogue with remediations, the output-contract rules
  of §5. Keep it regenerable: error codes and verb lists should be
  asserted against the real CLI in a unit test so the doc cannot drift.
- `ship help [verb] [--json]` gives per-verb usage from the same source
  that feeds `ship docs`, so an agent can query one verb cheaply.
- `ship box doctor --json` upgraded to a list of
  `{"id","status":"ok|degraded|failed","evidence","remediation"}` checks
  (remediation = runnable command). Extend existing doctor checks; add:
  disk space, cert status, reaper timer present, deploy journal readable.
- **Eval suite** (`tests/agent-evals/`): scenario definitions that induce a
  failure on a fake-vps box, then verify a coding agent — given only the
  `ship` binary and the output of `ship docs` — recovers unaided within
  N=6 tool calls. v1 scenarios (minimum): missing secret, failing release
  command, probe failure (wrong port), missing Dockerfile, expired preview
  referenced, dirty/unknown branch state. The harness invokes a
  configurable agent CLI (`SHIP_EVAL_AGENT_CMD`); runs behind
  `make agent-evals` (not default CI — needs API credentials). **Error
  message quality is judged by this suite**: if an agent fails a scenario,
  fix the error/remediation text, not the test.

## 10. Testing requirements

- All existing fake-vps suites keep passing (adapted to the new surface).
- New fake-vps coverage: branch→env mapping incl. sanitization edge cases
  (`feat/x`, unicode, >28 chars); `production_branch` override; dirty
  worktree rejected on the production branch, allowed on previews;
  `--branch` on deploy accepted only with detached HEAD; branch-scoped
  rollback; preview create/update/reap/pin; preview secret isolation
  (prod value must not leak) and branch-value-over-shared-preview
  precedence; stdout-is-URL contract; `why` after induced release/probe
  failures; webhook fired (assert against a local HTTP sink);
  sslip route synthesis; `box member add` end-to-end (newly added key can
  ship); `behind_production` stale-checkout guard; webhook payload carries
  `why` + `remediation`; doctor timer fires the webhook on an induced degraded
  check; attribution (who shipped) present in status and journal.
- Unit tests: error catalogue completeness (every returned code exists in
  the catalogue), docs/CLI drift check (§9).

## 11. Phases and acceptance criteria

**Phase 1 — surface & addressing.** Rename to ship; manifest rename +
`[env]`/`@secret` shorthand; branch=env resolution (`production_branch`,
dirty-on-prod rejection, stale-checkout guard, detached-HEAD `--branch`
gate); preview envs with TTL,
suffix, reaper, pin; `ship`/`status`/`logs`/`rm`/`pin`; stdout-URL rule;
sslip synthesis. *Accept when:* on a fake-vps box, `ship init && ship` from
`main` prints only a URL that serves 200; shipping from a feature branch
yields a second URL; the reaper destroys an expired preview; all §10
mapping/preview tests green.

**Phase 2 — failure UX & agent contract.** Deploy journal + `why`;
`webhook`; error catalogue + §5 shapes across every verb; `--json` on
mutations; `ship exec`; `ship docs` + AGENT.md; doctor upgrade; eval
harness with the six scenarios passing against at least one agent. *Accept when:* every
eval scenario passes; grepping the codebase finds no error return that
bypasses the catalogue; `ship docs | wc -l` > 0 and drift test green.

**Phase 3 — polish.** Preview-scoped secrets UX refinements, including
bulk import: `ship secret set --from .env [--preview|--branch <name>]`
(merge by default; `--replace` makes the file authoritative for the
scope and lists removed key names on stderr — never values);
`box member add`/`box member ls`/`box member rm`, `box app rm <app>` + the `box app ls --json` app list,
install one-liner (curl script — the only install story; a Homebrew
tap is deliberately cut, deferred until users ask) + shell
completions, README rewritten around the four moments (§0),
`box doctor` remediation coverage, error-text audit driven by eval
transcripts, CHANGELOG, release.

Work honestly serially: Phase 1 acceptance before Phase 2 work. Deferred
backlog (do not start, tracked for later): mounted-cargo fast path;
git-push transport; pre-release data snapshot/rewind-lite
(superseded in part by §18's data verbs); `preview_domain` wildcard
base (previews on `*.preview.yourdomain.com` instead of sslip —
Vercel's preview-suffix equivalent); watch mode (dev-class env: source
synced from the laptop into a container running the framework's own
dev server — development on the box, prod images stay immutable);
webhook fan-out stays the receiver's job (single URL by design).

## 12. Engineering ground rules

- Follow existing code idioms (`cmd/client` orchestrates, `cmd/helper`
  is the only privileged surface; keep the helper API narrow and add verbs
  rather than widening sudo).
- Never bake secret values into argv, files in the repo, or logs (existing
  discipline — preserve it in every new path).
- Any behavior in §5 (output contract) is a test, not a convention.
- **No backwards-compatibility code anywhere.** There are no external
  users. Rename and replace outright: no legacy manifest names, no key
  aliases, no deprecation shims, no old-binary-name support.
- When this spec under-specifies something, prefer: for surface/UX
  questions, whatever Vercel does (adapted to a CLI); for engine
  behavior, the existing engine's behavior; otherwise the simplest option
  that keeps the §0 bar. Note the decision in the PR description.

## 13. Members and roles (v0.2 arc — promotes RFD-0003)

Settled with Franco July 8. Members exist (§4); this section adds
authorization. **Exactly three fixed roles, no custom roles, no policy
DSL, ever.**

Surface:

```
ship box member ls [<box>] [--json]     members: name, role, key SHA256
ship box member add <key|path> [<box>] --name <n> [--role owner|shipper|agent]
                                        literal key material writes
                                        immediately; --name is
                                        mandatory (identity never
                                        derives from key comments,
                                        filenames, or git config);
                                        default role: shipper
ship box member add <https-url> <box> --name <n> [--role ...]
                                        raw keys-URL (github.com/
                                        <u>.keys etc. — a forge
                                        convention): FETCHES AND
                                        PRINTS keys, fingerprints,
                                        source, plan; WRITES NOTHING;
                                        emits the exact commit command
ship box member add ... --confirm <n>@sha256:<plan-digest>
                                        refetches, requires exact
                                        digest match (box + source +
                                        name + role + sorted key
                                        material), installs atomically
ship box member rm <name> [<box>]       revoke all of a member's keys
ship box approval ls [<box>] [--json]   list pending approvals (box-wide)
ship box approval grant <id> [<box>]    grant one-shot; requests expire
                                        in 15 min (expiry is the only
                                        "deny"; no deny verb); granting
                                        refreshes the window
```

Members and approvals are box-scoped objects, so their verbs live
under `box` with the object-first `[<box>]` fallback every box verb
uses (explicit target anywhere, ship.toml inside an app dir) — the
approver is usually NOT in the requester's checkout (ADR-0018). One
grammar covers every collection: `resource + subverb`
(`box member ls|add|rm`, `box approval ls|grant`, `box app ls|rm`); a
bare noun never lists (ADR-0019, superseding ADR-0018's plural list
forms).

- `box setup` enrolls the first member as **owner**.
- Role bundles (verb × scope, enforced helper-side):
  - **owner** — everything, including member management, box setup/rm,
    restore, rm prod.
  - **shipper** — ship prod + previews, status/logs/why/exec, rollback,
    secrets set, pin/unpin, rm previews, data save/restore on
    previews. Not: member verbs, restore into prod, rm prod,
    box-level mutations.
  - **agent** — ship/rollback/pin/exec on **preview envs only**;
    status/logs/why/docs everywhere. Not: prod mutations, secret read
    or set, rm, data save/restore, shell.
- Out-of-role → errcat `approval_required` carrying the request id and
  the FULLY RESOLVED `ship box approval grant <id> <box>` (approvers paste
  from chat; a command that depends on cwd or a placeholder is not a
  remediation). The helper mints the request into a box-global queue;
  a webhook event `approval_requested` fires with the same payload
  (to the box webhook once v0.4.0 lands, §17). Approvals are one-shot
  and journaled. The box learns its client-routable address at
  `box setup` (recorded box-side next to members state) — remediations
  and notifications must never derive the box name from the machine
  hostname.
- Grant integrity (ADR-0018): the request records the ROLE the denied
  action requires; granting needs approver role ≥ that role AND a
  different member than the requester (no self-approval); a grant
  refreshes the expiry window. Approval summaries are verb-first human
  sentences. The agent flow (agent mints, human grants) is the
  feature and stays.
- Scope rule (AGENT.md verbatim): members and approvals belong to the
  box; secrets, envs, and journals belong to the app. Per-app
  membership is explicitly parked (a future role-scope extension, not
  a redesign).
- Storage: box-global members file under the state root mapping key
  fingerprint → {name, role}; authorized_keys remains the key source.
- Identity trust tiers (per RFD-0003 sequencing): **agent keys are
  pinned server-side** — their authorized_keys entries carry a
  restricted environment/forced-command so sshd itself fixes the
  member identity and no shell is possible. Owner/shipper humans keep
  plain keys; for them the helper trusts the client-passed member
  identity (trust model: teammate, not process) — documented as such
  in AGENT.md until the serve protocol earns its way in.
- Journal records member + role on every mutation. The `.pub`-suffix
  member-name nit from v0.1.1 is fixed in this arc.

Acceptance (fake-vps unless noted): role matrix — agent ship to prod →
`approval_required` with a fully resolved `ship box approval grant <id> <box>`
→ the same command succeeds exactly once; a second attempt
re-requests; expired request refuses with a fresh-request remediation;
`ship box approval ls` lists pending with ids; a shipper cannot grant an
owner-gated request and NOBODY can grant their own (ADR-0018); a grant
refreshes the expiry window; shipper denied member add; owner
unrestricted; `box member add` default role is shipper; box setup
first member is owner;
agent-role key cannot open a shell (ssh attempt refused by
forced-command); member ls shows roles; one new agent-eval scenario:
recover from `approval_required` via the oracle approving. Tag v0.2.0
when green on a real-box role round-trip.

## 14. Data forks (v0.3 arc — promotes RFD-0001's fork slice)

Opt-in, on-demand — never automatic. The `data` noun bucket. This is
one of the two things a PaaS structurally cannot do (it doesn't own
your data); with the SQLite doctrine a fork is a file copy, so it is
RAM-cheap.

Surface:

```
ship data fork        fork prod's current /data into this branch's preview
ship data reset       reset this preview's /data to empty
```

- **`ship data fork`** — run from a **preview** branch: copies prod's
  current `/data` into this preview's data dir, then bounces the
  preview (stop → swap → start, same release) so it runs against
  prod-shaped data. SQLite files (`*.db`, `*.sqlite*`) copy with
  `VACUUM INTO` (consistent under prod's live writes, WAL-safe); other
  `/data` files with `cp -a` (reflink when the filesystem supports it).
  **Prod is READ-ONLY throughout — never modified.** Re-running
  refreshes the fork.
- **`ship data reset`** (ADR-0018; was `data rm` — every other rm destroys an addressed object, this empties a directory) — resets this preview's `/data` to empty and
  bounces it (clean-slate previews).
- **Guards:** preview branch only — the production branch errors
  `data_fork_on_production` (you cannot fork into prod, and you cannot
  fork *from* a preview). The preview env must already exist (error
  `no_preview_env`, remediation `ship`). Both verbs read/replace real
  data, so they require role **shipper** or **owner**; an **agent** →
  `approval_required` (§13) — reading prod data is above the agent
  default.
- **Managed DBs:** forks only what is on the box (`/data`: SQLite +
  uploads). A managed `DATABASE_URL` is untouched — provider branching
  plus `--branch` secrets (§6) is the managed path, deferred. If the
  app is managed-DB-only, `data fork` copies whatever non-DB files live
  in `/data` and says so.
- **Output** narrates what was forked (files + sizes) and prints the
  preview URL; a one-line note that prod data (incl. any PII) now lives
  in a less-guarded preview. Disk cost is bounded by the preview reaper
  (72 h TTL) and the `box doctor` disk check.
- **Never bakes data into logs/argv** (§12); the copy runs box-side in
  the helper (the client never touches prod data).

Deferred (RFD-0001 open questions, do not build now): manifest
`[data] fork = true` (auto-fork on preview create); a `fork_sanitize`
hook run against the fork before first use; `ship data rewind`;
provider branch-API integration for managed DBs.

Acceptance (fake-vps): fork prod SQLite into a preview → the preview
serves prod's rows while prod is byte-for-byte unchanged; `data reset`
empties the preview; `data fork` on the production branch is refused;
an agent-role key → `approval_required`; a `/data` with only uploads
(no SQLite) copies the files; a second `data fork` refreshes. Tag
v0.3.0 when green on a real-box fork round-trip.

## 15. Preview protection: one capability (v0.4 arc; redesigned July 13 — D13, §18)

Previews are for your team, not the internet — and nobody types a
password. Every preview is protected, always, by **one capability
token**: possession of the URL is access. The same token works for a
teammate clicking a PR link, an external guest you paste it to, and an
agent sending a header. Prod stays public, always.

> History, honestly: v0.4.0 shipped a three-credential design (team
> password + automation bypass token + per-preview share link) behind an
> opt-in `[previews] protected` knob. Redesigned before any user
> existed; the trio, the knob, and the open protected-by-default
> flag-day question were all deleted outright (zero users, no shims).
> The rotate matrix — three credentials, three lifecycles — died with
> them. Vercel-check: their team members never feel protection because
> SSO recognizes them; the capability URL is the no-accounts equivalent.

Surface:

```
ship preview share [--rotate]   print (or rotate) this preview's
                                capability URL
```

- **Always protected. No knob.** Anonymous requests to a preview vhost
  get 401. Prod vhosts never gain auth — structurally impossible, not
  just discouraged (unchanged from v0.4.0).
- **The capability** is generated box-side at preview-env creation
  (never user-chosen, never in argv or logs, §12), stored with app
  state like a secret. Three equivalent presentations of the same
  token:
  - **URL**: `<preview-url>?ship=<token>` — Caddy matches a valid
    token, sets a signed cookie, redirects to the clean URL (the
    v0.4.0 share-link machinery, kept). This is what `ship` prints on
    stdout for a preview deploy (§5) and what you paste in a PR or to
    a guest.
  - **Cookie**: set by the redirect; signed against the current token
    generation, so rotation invalidates outstanding cookies.
  - **Header**: `x-ship-capability: <token>` — CI smoke tests, agents,
    screenshot tools.
- **`ship preview share`** (preview branch only) re-prints the
  capability URL; `--rotate` regenerates the token and re-renders the
  fragments of all this preview's routes. stdout = the URL (§5).
  Reading is any member's right (the deploying member already sees it
  on stdout; `ship status --json` carries it too). **Rotation** is
  shipper/owner; agent → `approval_required`.
- **Leak response is one flow:** rotate. Teammates self-heal via the
  CLI (`ship status`); externals get the fresh link re-sent — which is
  exactly the desired outcome after a leak.
- **Probes and doctor are structurally unaffected:** they hit process
  ports on the box, not Caddy vhosts — protection can never fail a
  probe.
- Capabilities die with the preview (reap destroys them).
- **Errors:** `share_on_production` (capability verbs from the
  production branch; remediation: previews only), plus existing
  `no_preview_env`. (`previews_not_protected` deleted with the knob.)

Growth path, deliberately not built (record: the one-token design is
the N=1 case of "a preview has named capabilities"; if guest-vs-team
revocation is ever needed, `--name` mints a second named capability —
additive, no redesign): named capabilities; per-member credentials /
Agent Badges (RFD-0004); `--ttl` self-expiry.

Acceptance (fake-vps): preview → anonymous 401; capability URL →
cookie → clean-URL redirect → 200; capability header → 200; `ship`
stdout for a preview deploy is the capability URL and it serves 200
as printed; `--rotate` → old URL, old header value, and
previously-issued cookies all 401 while the new URL works;
`ship status --json` carries the current capability URL; prod serves
anonymously before and after; probe passes on a protected deploy;
reap destroys the capability (token 401 after reap); agent key on
`--rotate` → `approval_required`; capability verbs on the production
branch → `share_on_production`. Real-box: one browser round-trip
(click capability URL → in; rotate → old link dead) on a live preview.

## 16. Box status and update (v0.4 arc)

The box tells you when it's behind, and one command catches it up. The
helper is pushed from the client at setup, so version skew is a fact of
life (an old helper already refuses new verbs) — v0.4.0 makes it
visible and one-command fixable instead of a surprise.

Surface:

```
ship box status [<box>] [--json]  one-box summary: helper version vs
                                 client, disk, apps, members, pending
                                 approvals, last doctor result
ship box update [<box>]          converge the box to this client's
                                 version (helper + version-owned
                                 artifacts)
```

- **`ship box status`** prints a one-screen summary: helper version vs
  client version with an update hint when behind
  (`next: ship box update <box>`), disk usage, app count, member count
  (`members: N (M owners)`; `members: unknown` when the member store is
  unreadable, with the field omitted from `--json`), pending
  approvals count, and the last doctor-timer result with its age. Any
  member may read. `--json` is the machine view (resident substrate,
  RFD-0002). The full app table is `ship box app ls` (D8, §18); the
  active probe is `ship box doctor` — three verbs, three questions
  (summary / list / examine), none folded into another. Status must
  not fetch data it discards (the v0.4.0 implementation pulled the
  full app list and doctor JSON for counts — fix as plumbing).
- **`ship box update`** has the box download and checksum-verify the
  released helper binary (ADR-0011; the client pushes nothing) and
  re-applies **version-owned artifacts** (systemd units, sudoers
  fragment, agent-shell forced keys — rendered from the member store,
  ADR-0020) idempotently. Journals
  box-side; narrates changes only (narration diet); exact no-op when
  already current. Requires owner; others → `approval_required`.
  `box setup` remains day-0 (and the recovery path); `update` is day-N.
- **Downgrade guard:** helper newer than client → error
  `client_behind_helper`, remediation: upgrade ship (install one-liner).
  Never silently downgrade a box.
- **Doctor check `helper_version`:** degraded on mismatch, remediation
  `ship box update <box>` — so the daily timer surfaces skew through the
  normal §7 channel without anyone remembering to look.

Acceptance (fake-vps): box with a stale helper → `box status` shows
behind and doctor degrades `helper_version`; `box update` converges
(status clean, doctor green, journal entry); second update no-ops;
client older than helper → `client_behind_helper`; shipper/agent
`box update` → `approval_required`; `--json` schema asserted. Real-box:
status → update → status round-trip on the testing box. Ships in the
v0.4.0 tag.

## 17. Webhook split: app events vs box events (v0.4 arc)

One box, one pager. Recorded honestly: v0.3.0 fans `doctor_degraded`
and `approval_requested` to **every app's** webhook, so one disk spike
on a three-app box wakes three agents. v0.4.0 splits scopes so every
event fires exactly once, to exactly one URL.

Surface:

```
ship box webhook <box> [--json]  print the box webhook (unset: says so;
                                 --json: {"url":""} shape, read only)
ship box webhook <box> <url>     set it
ship box webhook <box> --rm      clear it
```

- **App events** — `deploy_aborted`, `deploy_recovered`,
  `preview_reaped` — fire to the app's manifest `webhook` URL, as today.
- **Box events** — `doctor_degraded`, `approval_requested`, and future
  box-scoped events (drift, disk) — fire **once** to the box webhook,
  and never to app URLs. With no box webhook set, box events go nowhere
  (the journal and doctor output still record everything); setting one
  is an explicit act, not a setup default.
- **Bare verb reads the scalar** (§4 convention: `ls` lists collections;
  a box has exactly one webhook by design — fan-out stays the receiver's
  job). Read: any member. Set/clear: owner; others → `approval_required`
  (the box webhook is where alarms go — moving it is an owner act).
- **Payloads keep their §7 journal-grade shape**; box events carry the
  box host, and `approval_requested` keeps the target app inside the
  payload.
- **Storage:** box-side state next to members/approvals — no app owns
  it.
- **Breaking vs v0.3.0** (zero users, no shim): app webhooks stop
  receiving box events; CHANGELOG says so.

Acceptance (fake-vps, local HTTP sink, two apps on one box): induced
doctor degradation → exactly one POST to the box URL, zero to either
app URL; `approval_requested` → box URL once; `deploy_aborted` → the
owning app's URL only; read/set/`--rm` round-trip with narration; role
matrix on set; AGENT.md regenerated (drift test covers the event
tables). Ships in the v0.4.0 tag.

## 18. Simplification batch (settled with Franco July 13, 2026 — D1–D13)

One product pass, decisions D1–D13, recorded in ADR-0012..0016. The
normative rules live in the sections they amend (§0.5, §2, §3, §4, §6,
§15, §16, §19); this section holds the rationale index and the
acceptance criteria for the pieces no other section owns. Arc
unscheduled at write time — Franco sequences it against the resident
(RFD-0002).

Decision index: **D1** data-only backups, verbs under `data`, snapshot
lands on the laptop (ADR-0012, amends ADR-0007) · **D2** secrets never
backed up · **D3** scheduled off-box backup parked (RFD-0007) · **D4**
bare `secret set` follows branch=env (§6) · **D5** `@secret:NAME`
removed (§2) · **D6** `--include-dotenv` removed (§4) · **D7** init
starters removed (§2) · **D8** `box status`/`box app ls`/`box doctor`
split, `ls` renamed (§16, ADR-0016) · **D9** `box forget` stays hidden,
discovered via remediation · **D10** one topology (§4, ADR-0013) ·
**D11** box config foundation (§19, ADR-0014) · **D12** control plane
stays SSH-as-pipe (RFD-0006) · **D13** one preview capability (§15,
ADR-0015).

### Data verbs (D1/D2 — replaces whole-app `save`/`restore`)

Your code is in git. Your secrets came from your laptop. The only
thing on the box that can die is `/data` — so that is what ship backs
up, and the snapshot does not live on the box it protects.

```
ship data save [--out <path>]    snapshot this branch env's /data →
                                 a tar.gz ON YOUR LAPTOP
ship data restore <id|path>      upload a snapshot and swap it in
ship data ls [--json]            list local snapshots for this app
```

- **`data save`**: the helper snapshots the env's `/data` — SQLite
  files (`*.db`, `*.sqlite*`) via `VACUUM INTO` (WAL-safe under live
  writes), everything else `cp -a` — tars it, and streams it to the
  client. Default landing:
  `~/.ship/backups/<app>/<env>-<release>-<utc>.data.tar.gz`
  (`--out` overrides). The box keeps **nothing** (work dir cleaned) —
  a backup that lives on the box it protects is theater, which is what
  killed the v0.1 whole-app design (ADR-0012). stdout = the local
  path; stderr narrates files and sizes. Consistency bar is per-file,
  not cross-file (same as `data fork`): documented, not hidden.
- **`data restore`**: uploads the tar, **validates shape and manifest
  before touching `/data`** (the v0.4.1 restore lesson, `19fa7c5`),
  then stop → swap → start (the `data fork` bounce). Restoring prod
  additionally requires `--confirm <name>` (same guard as `rm` prod).
- **Secrets are never in a snapshot** (D2): their source of truth is
  off-box already; plaintext-on-the-same-disk was the worst part of
  the old design. Box dies → `box setup` → `ship` → re-import via
  `secret set --from .env` → `data restore`.
- Roles: like `data fork` — shipper/owner; agent → `approval_required`.
  Restore **into prod** is owner-only (§13: replacing prod data
  wholesale is an owner act), on top of the `--confirm` guard.
- The whole-app `ship save`/`ship restore` verbs and their engine
  backup format are **deleted** (zero users, no shims).

### Batch acceptance

Fake-vps: `data save` on a preview with a live-written SQLite db plus
loose files → tar lands locally, box-side work dir gone; `data
restore` round-trips the sqlite rows; a corrupted tar refuses
**before** any `/data` mutation; prod restore without `--confirm`
refuses; agent on save/restore → `approval_required`; `data ls` lists
the snapshot. Bare `ship secret set` on a feature branch sets that
env's value and **prod's value is byte-identical before/after** (the
inverse of §6's old test); bare set on the production branch sets
prod. `ship` on a repo with no Dockerfile and no static route →
`dockerfile_missing` with both remediations; `ship init` writes only
`ship.toml`. `box setup --ingress`/`--admin`/`--litestream` → usage
error (flags gone). `--include-dotenv` → usage error; a `.env` in the
worktree never appears in the uploaded artifact (assert
archive contents). `@secret:NAME` in a manifest → manifest error
naming the bare-form rule. `box app ls` serves the old `box ls` table;
`box ls` → usage error suggesting `box app ls`. §15 acceptance runs as
written. AGENT.md regenerated; error catalogue drift test green
(removed codes gone, `dockerfile_missing` present). Real-box: data
save/restore round-trip on the testing box.

## 19. Box config foundation (D11 — ADR-0014)

One canonical, schema-validated config document on the box; every
future box-level option is a key in it, not a new flag or a new
storage file. Growth without sprawl: OpenClaw-grade storage
discipline, ship-grade authorization.

```
ship box config <box> [--json]          effective config: value, default,
                                        source (default|set), per key
ship box config <box> set <key> <val>   validate → authorize → apply →
                                        journal (a journal entry records
                                        a change that happened; a failed
                                        append warns, never lies)
ship box config <box> unset <key>       back to the default
```

- **The file**: box-side, next to members/approvals state. Atomic
  writes (temp + rename). Schema-validated on every read and write;
  **unknown keys refuse** (the error lists valid keys). A version
  field for future migration.
- **The schema declares, per key**: type, default, write role
  (owner/shipper), and whether out-of-role requests mint an approval
  (§13). Authorization lives **in the schema**, which is what
  makes a generic setter safe here when it would be a hole anywhere
  else — every key has a declared owner, or it doesn't exist.
- **Apply mode stays a spec-level concept, not a schema field**: every
  current key is read on next use. The first `converge` key (side
  effects; reuses the bounded-converge machinery of ADR-0011 — future:
  `harden.tailscale`, `backup.*` per RFD-0007) adds the schema field
  together with the plumbing that reads it. A declared-but-unread
  policy field is a silent no-op trap — a key marked converge would
  validate, journal, and then quietly never converge (ADR-0017).
- **Initial keys** (deliberately few): `webhook.url` (owner-set,
  read-on-next-use) — `ship box webhook` becomes sugar over it, §17
  semantics unchanged.
- **What this is not**: app config. `ship.toml` in the repo stays the
  only app-level config — versioned, PR-reviewed, next to the code it
  configures. The box config holds box-scoped operational settings
  only. There is no wizard: a key you must be walked through is a
  decision ship should have made itself (§0.5).

Acceptance (fake-vps): `box config --json` on a fresh box shows
`webhook.url` unset with its default and source; owner `set webhook.url`
→ journaled, `box webhook <box>` prints the same value, an induced box
event POSTs to it; shipper `set` → `approval_required`, approve →
succeeds once; unknown key → error listing valid keys; wrong type →
error; `unset` restores default; `box webhook <box> <url>` and
`box config set webhook.url <url>` are observably the same operation
(one storage, one journal shape). AGENT.md regenerated.
