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
   attached (journal + notify + doctor here; resident loop in RFD-0002).
5. **Zero security decisions.** One hardened shape. The only choices
   are topology modes (`--ingress`, `--admin`, §4) — never mechanisms,
   ciphers, ports, or policies.
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
routing, secrets storage, backup machinery, helper privilege model, and the
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
DATABASE_URL = "@secret"            # NEW shorthand: secret name = var name
SMTP_URL     = "@secret:MAIL_URL"   # existing explicit form kept

[env.preview]                       # optional; overlays [env] in previews only
LOG_LEVEL    = "debug"
POSTHOG_KEY  = "phc_test456"

release = "npx drizzle-kit migrate" # top-level only; the [deploy] section is gone
probe   = "/healthz"                # health check for the routed process
notify  = "https://ntfy.sh/..."     # NEW: webhook, §7
```

Rules:

- `@secret` references are whole-value only (existing behavior). The bare
  `"@secret"` form resolves to a secret named after the variable.
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
- `ship init` writes `ship.toml` AND a starter `Dockerfile` when none
  exists, from an explicit template
  (`--template container|static|php|hono`, default `container`; the
  existing template system in `cmd/client/init.go`). Stack *detection*
  (package.json / requirements / go.mod / static dir choosing the
  template) is not built yet — TODO(§2), a v0.4.x candidate. Never
  overwrite existing files (existing behavior, keep).

## 3. Addressing: branch = environment

The new surface has no `--env`. Resolution rule, implemented client-side:

- Current git branch equal to `production_branch` (default `main`, else
  `master`) → env name `prod`.
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
- **TTL 72 h** from last ship; `ship pin <branch>` clears expiry,
  `ship unpin` restores it.
- A reaper runs on the box (systemd timer installed by `box setup`,
  invoking a new helper verb `server env reap`) and destroys expired
  preview envs — equivalent to the existing destroy path, secrets purged.
  Reap events fire `notify`. `prod` is never reaped and `ship rm` on prod
  requires `--confirm <name>` (existing destroy guard).
- Preview URL: `<envname>.<base-domain>`. Base domain: the project's
  wildcard domain if configured; otherwise sslip.io (§8). Previews
  collapse to this single URL, serving the default host's routes (paths
  included); extra hosts and redirects are production-only.
- Preview processes run under default CPU/memory caps unless the process
  declares its own `resources` — a runaway preview must not starve prod.

## 4. CLI surface (complete)

```
ship                      deploy current branch; stdout = URL (see §5)
                          flags: --tls internal (§8), --rebuild,
                          --include-dotenv, --branch (detached HEAD only,
                          §3), --json (§5), --config <path>
ship init                 scaffold ship.toml + Dockerfile (stack detection)
                          flags: --config <path>
ship help [verb] [--json] per-verb usage from the ship docs source (§9)
ship status [--json]      all live envs for this app: branch, url, release,
                          who shipped, health, age, expires/pinned, dirty hint
ship logs [process] [--follow] [--tail N]   current branch's env
ship exec <cmd...>        run a one-off command inside the current
                          branch's env container, with its secrets and
                          /data mounted (Heroku's `heroku run`; built
                          in Phase 2 — agents need it to inspect state
                          without ssh+podman guessing)
ship why [--json]              explain the last failed/aborted deploy (§7)
ship rollback [release]        previous release of the current branch's env;
                          release = commit short-sha (from ship status)
ship rm <branch> [--confirm <name>]   destroy an environment
ship pin <branch> / ship unpin <branch>
ship preview password [--rotate]   print/rotate the team password and
                          bypass token for this app's protected
                          previews (§15)
ship share [--rm]         mint/print (or revoke) the capability link
                          for this preview (§15)
ship secret set <KEY> [--preview|--branch <name>]   stdin-only (§6)
ship secret ls [--json] / ship secret rm <KEY> [--preview|--branch <name>]
ship save [--to path] / ship restore --from <id|path>   existing backup/restore
ship ssh                  existing
ship box setup <ssh-target> [--ingress ...] [--admin ...]   host install
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
                            ingress: public 80/443 (--ingress ...)
                            admin: SSH keys only (--admin tailscale)
                            member added: franco (SHA256:...)
                          The <ssh-target> accepts host or user@host;
                          user@host sets the bootstrap user (a
                          conflicting --bootstrap-user is a usage
                          error).
                          One sentence: ship knows who you are; setup
                          introduces you to the box; everyone else is
                          ship member add. Members and approvals are
                          BOX-scoped (the trust boundary); secrets,
                          envs, and journals are app-scoped.
ship member add <github-user|key|path>   authorize a teammate's SSH key
                          (bare word → fetches github.com/<user>.keys;
                          prints type + SHA256 fingerprint per key
                          added; dedupes; comment = member name, which
                          feeds §7 attribution)
ship member ls [--json]   authorized members: name, key type, SHA256
                          fingerprint
ship member rm <name>     revoke all of a member's keys; refuses to
                          remove the last remaining key (lockout
                          guard). Roles arrive post-v1 (RFD-0003);
                          until then every member has full deploy
                          access.
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
                          the list). The next: line always uses the
                          placeholder form — next: ship box <verb>
                          <box> — never a filled-in host (Franco: the
                          placeholder teaches the shape, the list
                          above supplies the value, and no
                          count-dependent special case exists). <box>
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
ship box ls [--json]      existing app list (explicit scope, works
                          anywhere); --json is the box app list: per-app
                          envs, branches, urls, releases, health,
                          expiry (Phase 3)
ship box rm <app> [--confirm <app>]   destroy an app and all its envs
                          without the repo dir (orphan cleanup; same
                          confirm guard as prod rm; Phase 3)
ship box forget <box>     drop this box's host-key pin from
                          ~/.config/ship/known_hosts (decommission;
                          pairs with box rm; box setup re-establishes)
ship box status <box> [--json]   one-box summary: helper version vs
                          client, update hint, disk, apps, pending
                          approvals (§16)
ship box update <box>     converge helper + version-owned artifacts
                          to this client's version (§16)
ship box notify <box> [<url>|--rm]   read/set/clear the box webhook
                          for box-scoped events (§17)
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

- `ship secret set KEY` → sets the **prod** value.
- `ship secret set KEY --preview` → sets a single shared **preview** value
  that all preview envs of the app resolve.
- `ship secret set KEY --branch <name>` → sets a value for that branch's
  env only (stored in the existing per-(app, env) secret store) — e.g. a
  pinned `staging` branch with its own credentials.
- Resolution at deploy: prod env → prod values only; preview env → its
  branch value if set, else the shared preview value. A `@secret`
  reference with no value for the target scope **fails the deploy** with
  code `secret_missing` and remediation
  `ship secret set KEY [--preview|--branch <name>]`. Preview envs must
  never receive prod values — add a fake-vps test asserting this.

Injected into every process (all envs): `SHIP_URL` (this env's own https
URL), `SHIP_BRANCH`, `SHIP_ENV` (`production` | `preview`),
`SHIP_RELEASE` (release id).

## 7. Failure UX: `why` and `notify`

- The helper records a structured deploy journal per env (extend existing
  release metadata): outcome (`deployed | aborted_release | aborted_probe |
  rolled_back`), failing step, captured stderr tail (last ~40 lines),
  timestamps, release ids involved, and the deploying identity (SSH key
  comment + git author) — `ship status` and `why` show who shipped what.
- `ship why` renders the latest entry as plain prose: what happened, the
  probable cause (map common patterns: non-zero release command, probe
  non-200 with body snippet, image build failure), whether traffic was
  affected (with the current engine a failed probe means the old container
  kept serving — say so explicitly), and `next:` command.
- `notify` (manifest key): the client or helper POSTs a JSON event
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
and for prod before a domain exists), synthesize
`<envname>.<box-ip-with-dashes>.sslip.io` and route it via Caddy with a
public certificate. The synthesized route targets the process named
`web`, else the sole declared process; several processes with no `web`
and no `[routes]` is a manifest error. Document (in `ship docs` and README) that sslip.io
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
  failures; notify webhook fired (assert against a local HTTP sink);
  sslip route synthesis; `member add` end-to-end (newly added key can
  ship); `behind_production` stale-checkout guard; notify payload carries
  `why` + `remediation`; doctor timer fires notify on an induced degraded
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
`notify`; error catalogue + §5 shapes across every verb; `--json` on
mutations; `ship exec`; `ship docs` + AGENT.md; doctor upgrade; eval
harness with the six scenarios passing against at least one agent. *Accept when:* every
eval scenario passes; grepping the codebase finds no error return that
bypasses the catalogue; `ship docs | wc -l` > 0 and drift test green.

**Phase 3 — polish.** Preview-scoped secrets UX refinements, including
bulk import: `ship secret set --from .env [--preview|--branch <name>]`
(merge by default; `--replace` makes the file authoritative for the
scope and lists removed key names on stderr — never values);
`member add|ls|rm`, `box rm <app>` + the `box ls --json` app list,
install one-liner (curl script — the only install story; a Homebrew
tap is deliberately cut, deferred until users ask) + shell
completions, README rewritten around the four moments (§0),
`box doctor` remediation coverage, error-text audit driven by eval
transcripts, CHANGELOG, release.

Work honestly serially: Phase 1 acceptance before Phase 2 work. Deferred
backlog (do not start, tracked for later): mounted-cargo fast path;
git-push transport; pre-release data snapshot/rewind-lite; private
preview auth (sketch: `preview_auth = true` → Caddy basic-auth on
preview hosts with a generated per-app credential surfaced by
`ship status`, probe/agent bypass token); `preview_domain` wildcard
base (previews on `*.preview.yourdomain.com` instead of sslip —
Vercel's preview-suffix equivalent); watch mode (dev-class env: source
synced from the laptop into a container running the framework's own
dev server — development on the box, prod images stay immutable);
notify fan-out stays the receiver's job (single URL by design).

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
ship member add <src> [--role owner|shipper|agent]   default: shipper
ship member ls [--json]     now shows the role per member
ship approve                bare form: list pending approvals (box-wide)
ship approve <id>           grant one-shot; requests expire in 15 min
                            (expiry is the only "deny"; no deny verb)
```

- `box setup` enrolls the first member as **owner**.
- Role bundles (verb × scope, enforced helper-side):
  - **owner** — everything, including member management, box setup/rm,
    restore, rm prod.
  - **shipper** — ship prod + previews, status/logs/why/exec, rollback,
    secrets set, pin/unpin, rm previews. Not: member verbs, restore,
    rm prod, box-level mutations.
  - **agent** — ship/rollback/pin/exec on **preview envs only**;
    status/logs/why/docs everywhere. Not: prod mutations, secret read
    or set, rm, save/restore, shell.
- Out-of-role → errcat `approval_required` carrying the request id and
  the literal `ship approve <id>`; the helper mints the request into a
  box-global queue; a `notify` event `approval_requested` fires with
  the same payload (to the box webhook once v0.4.0 lands, §17).
  Approvals are one-shot and journaled.
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
`approval_required` with id; `ship approve <id>` → the same command
succeeds exactly once; a second attempt re-requests; expired request
refuses with a fresh-request remediation; bare `ship approve` lists
pending with ids; shipper denied member add; owner unrestricted;
`member add` default role is shipper; box setup first member is owner;
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
ship data rm          reset this preview's /data to empty
```

- **`ship data fork`** — run from a **preview** branch: copies prod's
  current `/data` into this preview's data dir, then bounces the
  preview (stop → swap → start, same release) so it runs against
  prod-shaped data. SQLite files (`*.db`, `*.sqlite*`) copy with
  `VACUUM INTO` (consistent under prod's live writes, WAL-safe); other
  `/data` files with `cp -a` (reflink when the filesystem supports it).
  **Prod is READ-ONLY throughout — never modified.** Re-running
  refreshes the fork.
- **`ship data rm`** — resets this preview's `/data` to empty and
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
serves prod's rows while prod is byte-for-byte unchanged; `data rm`
empties the preview; `data fork` on the production branch is refused;
an agent-role key → `approval_required`; a `/data` with only uploads
(no SQLite) copies the files; a second `data fork` refreshes. Tag
v0.3.0 when green on a real-box fork round-trip.

## 15. Preview protection and share links (v0.4 arc — promotes RFD-0004 #10)

Previews are for your team, not the internet. One manifest line and
every preview URL asks for the team password; prod stays public,
always. A share link hands one preview to one outsider without handing
over the password.

Surface:

```
ship preview password [--rotate]   print (or rotate) the team password +
                                   bypass token for this app's previews
ship share [--rm]                  mint/print (or revoke) this preview's
                                   share link
```

```toml
[previews]              # NEW section: preview *behavior* (not env vars —
protected = true        # [env.preview] stays a pure variable overlay)
```

- **Opt-in in v0.4.0.** Protected-by-default is a real flag-day decision
  (RFD-0004 open question) — not made here.
- **Mechanism:** Caddy `basic_auth` (username `team`, bcrypt hash) added
  to **preview vhosts only** when the manifest says `protected = true`.
  Prod vhosts never gain auth — there is no knob for it; structurally
  impossible, not just discouraged.
- **Password:** generated box-side (never user-chosen, never in argv or
  logs, §12) on the first protected apply; stored with app state like a
  secret. `ship preview password` prints it; `--rotate` regenerates and
  re-renders the fragments of all live previews. Requires shipper/owner;
  an agent key → `approval_required` (reading the team credential is
  above the agent default).
- **Automation bypass:** requests carrying `x-ship-bypass: <token>` skip
  auth (CI smoke tests, agents, screenshot tools). The token is separate
  from the password — rotating one never breaks the other. Printed by
  `ship preview password`, same role gate.
- **Probes and doctor are structurally unaffected:** they hit process
  ports on the box, not Caddy vhosts — a protected deploy can never fail
  its probe because of protection.
- **`ship share`** (preview branch only): prints a capability URL —
  `<preview-url>?ship_share=<token>`. Caddy matches a valid token, sets
  a signed cookie, redirects to the clean URL; the cookie passes from
  then on. One active share link per preview: re-running prints the
  same link, `--rm` revokes it, `--rm` then `share` rotates it. Share
  links die with the preview (reap destroys them). Requires
  shipper/owner; agent → `approval_required`. stdout = the share URL
  (§5).
- **Errors:** `previews_not_protected` (password/share on an app without
  `protected = true`; remediation: set it and `ship`),
  `share_on_production` (share from the production branch; remediation:
  previews only), plus existing `no_preview_env`.

Deferred (do not build now): the protected-by-default flip; per-member
credentials / SSO-grade auth; `share --ttl` self-expiry; a branded
password page (native browser prompt is v0.4.0).

Acceptance (fake-vps): protected preview → anonymous request 401, team
password 200, bypass header 200; prod URL serves anonymously before and
after; probe passes during a protected deploy; share token URL → cookie
→ clean-URL 200, revoked token → 401; `--rotate` invalidates the old
password while the bypass token keeps working; agent key on
password/share → `approval_required`; unprotected app →
`previews_not_protected`. Real-box: one browser round-trip (password
prompt + share link) on a live preview. Ships in the v0.4.0 tag.

## 16. Box status and update (v0.4 arc)

The box tells you when it's behind, and one command catches it up. The
helper is pushed from the client at setup, so version skew is a fact of
life (an old helper already refuses new verbs) — v0.4.0 makes it
visible and one-command fixable instead of a surprise.

Surface:

```
ship box status <box> [--json]   one-box summary: helper version vs
                                 client, disk, apps, pending approvals
ship box update <box>            converge the box to this client's
                                 version (helper + version-owned
                                 artifacts)
```

- **`ship box status`** prints: helper version vs client version with an
  update hint when behind (`next: ship box update <box>`), disk usage,
  apps with env counts, pending approvals count. Any member may read.
  `--json` is the machine view (resident substrate, RFD-0002).
- **`ship box update`** pushes the client-matched helper binary over the
  existing setup transfer path and re-applies **version-owned artifacts**
  (systemd units, sudoers fragment, agent-shell) idempotently. Journals
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

## 17. Notify split: app events vs box events (v0.4 arc)

One box, one pager. Recorded honestly: v0.3.0 fans `doctor_degraded`
and `approval_requested` to **every app's** webhook, so one disk spike
on a three-app box wakes three agents. v0.4.0 splits scopes so every
event fires exactly once, to exactly one URL.

Surface:

```
ship box notify <box>            print the box webhook (unset: says so)
ship box notify <box> <url>      set it
ship box notify <box> --rm       clear it
```

- **App events** — `deploy_aborted`, `deploy_recovered`,
  `preview_reaped` — fire to the app's manifest `notify` URL, as today.
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
