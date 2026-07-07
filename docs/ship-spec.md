# ship — implementation specification (v1)

**Status: authoritative.** Where this document conflicts with `SPEC.md`
(the current public contract), this document wins; `SPEC.md` describes the
engine you are adapting, not the target.

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
5. **Zero security decisions.** One hardened shape, no knobs, ever.
6. **Exit is a feature.** Delete ship and a boring, well-configured Linux
   server keeps serving. No PaaS can say this sentence.
7. **Proof, not promises.** The agent eval suite (§9) is public evidence
   that an agent can operate the whole thing unaided.

v1 builds none of this speculatively: Phases 1–3 below are the smallest
surface that points at it. The unscheduled bets live in `docs/rfd/`
(data forks, resident, members).

## 0. Mission and constraints

ship is the v2 surface of the existing simple-vps engine in this repository.
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

Read before starting: `SPEC.md`, `internal/config` (manifest schema),
`cmd/client` (CLI + deploy orchestration), `cmd/helper` (privileged on-host
API), `cmd/hostinstall`, `tests/fake-vps`. The existing helper API and
sudoers model stay as-is unless a section below says otherwise.

## 1. Naming

- Binary and product: `ship`. Update module naming minimally; keep internal
  package names where renaming is churn without value.
- On-host install path becomes `/usr/local/bin/ship`; sudoers grant updated
  accordingly. There are no external users; no compatibility shims for the
  old binary name are needed.
- Manifest file: `ship.toml` only. No legacy `simple-vps.toml` support.

## 2. Manifest (`ship.toml`)

Extend the existing schema in `internal/config`; keep field semantics where
they already exist. Target shape:

```toml
name = "taskflow"
box  = "deploy@203.0.113.7"        # existing server field, renamed
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

release = "npx drizzle-kit migrate" # top-level only; the [deploy] section is gone
probe   = "/healthz"                # health check for the routed process
notify  = "https://ntfy.sh/..."     # NEW: webhook, §7
```

Rules:

- `@secret` references are whole-value only (existing behavior). The bare
  `"@secret"` form resolves to a secret named after the variable.
- `production_branch` (optional) names the branch that deploys to prod;
  default `main`, else `master`.
- Per-env var sections (`[env.<name>.vars]`) are **removed** from the new
  format: config rides the branch. The systematic per-environment values
  are injected instead (§6).
- `preview = false` on a process excludes it from preview environments.
- Per-process `resources = { memory = "512m", cpus = 0.5 }` (existing
  engine knob) is kept.
- Per-process `port` is kept (default: the Dockerfile's sole `EXPOSE`,
  else 3000). Per-process `health` is removed — the top-level `probe`
  gates the routed process; other processes just get supervision.
- `ship init` detects the stack (package.json / requirements / go.mod /
  static dir), writes `ship.toml` AND a starter `Dockerfile` when none
  exists (reuse the existing template system in `cmd/client/init.go`).
  Never overwrite existing files (existing behavior, keep).

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
- A reaper runs on the box (systemd timer installed by `box init`,
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
ship init                 scaffold ship.toml + Dockerfile (stack detection)
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
ship secret set <KEY> [--preview|--branch <name>]   stdin-only (§6)
ship secret ls [--json] / ship secret rm <KEY> [--preview|--branch <name>]
ship save [--to path] / ship restore --from <id|path>   existing backup/restore
ship ssh                  existing
ship box init <ssh-target> [--ingress ...] [--admin ...]   existing host install
ship box add-key <github-user|key|path>   authorize a teammate's SSH key
                          (bare word → fetches github.com/<user>.keys)
ship box doctor [--json]  existing doctor, output upgraded per §9
ship box ls [--json]      existing app list (explicit scope, works
                          anywhere); --json is the fleet view: per-app
                          envs, branches, urls, releases, health,
                          expiry (Phase 3)
ship box rm <app> [--confirm <app>]   destroy an app and all its envs
                          without the repo dir (orphan cleanup; same
                          confirm guard as prod rm; Phase 3)
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
  (`{app, env, event, release, summary, why, remediation, ts}`) on:
  deploy aborted, deploy succeeded after a previous failure, preview
  reaped, doctor check newly degraded/failed. `why` and `remediation`
  carry the full journal entry, so an agent receiving the webhook can
  act without first querying the box. Fire-and-forget, 2 s timeout,
  never fails the operation.
- `box init` installs a doctor timer (daily, systemd — same pattern as
  the reaper): newly degraded or failed checks fire `notify` with their
  remediation. Set-and-forget means the box reports in; nobody has to
  remember to run doctor.

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
  sslip route synthesis; `box add-key` end-to-end (newly added key can
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
`box add-key`, `box rm <app>` + the `box ls --json` fleet view,
install one-liner (curl script) + Homebrew tap + shell completions,
README rewritten around the four moments (§0), `box doctor`
remediation coverage, error-text audit driven by eval transcripts,
CHANGELOG, release.

Work honestly serially: Phase 1 acceptance before Phase 2 work. Deferred
backlog (do not start, tracked for later): mounted-cargo fast path,
git-push transport, pre-release data snapshot/rewind-lite, private preview
auth.

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
