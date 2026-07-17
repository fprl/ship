# ship agent contract

This file is written for coding agents operating `ship`. Treat it as
the durable CLI contract. The implementation can change internally; these
surfaces should not drift.

## Mental model

The product has five ideas:

- `repo`: a Git checkout containing one `ship.toml` manifest.
- `box`: one hardened Linux host reached over SSH. In `ship.toml` and
  box verbs it is a host only, never `user@host`; setup alone accepts
  `user@host` for bootstrap.
- `branch`: the environment selector. There is no public `--env` flag.
- `snapshot`: an immutable deployed release, usually a commit-derived id.
- `URL`: the thing humans review. A successful `ship` prints exactly this.

Branch resolution is client-side:

- Current branch equal to `production_branch` deploys Production. If the manifest
  omits it, `main` is used when present, otherwise `master`.
- Any other branch is Preview. The box maps the raw branch to a sanitized
  env name plus a persisted random 4-character suffix.
- Detached HEAD requires `ship --branch <name>` for deploy. On a normal
  checked-out branch, deploy rejects `--branch` because Git is the truth.
- Read verbs that accept `--branch` can inspect another branch environment.
- Production refuses dirty worktrees and stale checkouts. Preview accepts dirty
  worktrees and marks the release dirty.

Preview lifecycle:

- First deploy creates the preview mapping and URL.
- The default TTL is 72 hours from the last ship.
- `ship preview pin <branch>` clears expiry; `ship preview unpin <branch>` restores it.
- The box reaper destroys expired previews and purges their secrets.
- Production is never reaped. `ship rm` on Production requires `--confirm <app>`.
- Preview URLs default to a synthesized sslip.io host. A manifest `[preview]`
  table with `base = "preview.example.com"` addresses previews on your own
  domain instead; `aliases = true` additionally serves a stable
  `<branch-slug>.<base>` alias per branch behind the same Preview capability,
  updated on each deploy and removed with the env. On alias collision the
  existing owner keeps the name and the newcomer keeps its canonical URL with
  a warning.

Truth stores:

- Manifest truth is the repo `ship.toml` at deploy time. The effective manifest and
  release metadata travel with the immutable release envelope: the image label
  `ship.release_envelope` for image releases or a hash-named `.ship-release-<full-envelope-hash>` sidecar for
  static releases. The active pointer's envelope hash selects the sidecar. They are not host-level manifest state files.
- Box truth is host state: env identity files, preview mapping metadata, active
  intent, deploy journals, members, roles, box configuration, secrets, Podman
  labels, Caddy fragments, and doctor state.
- Members and approvals belong to the box; secrets, envs, and journals belong
  to the app.
- Use release envelopes to answer "what did this release intend?"
- Use box state to answer "what is live now?"

Runtime state layout:

- /etc/ship: members.json, box-config.json including box.address, and secrets/<app>/<env>/<key>.
- /var/lib/ship: doctor.json, approval/update journals, and other durable helper journals.
- /run/ship: pending approvals.json and lock files.
- Each environment under /var/apps/<app>.<env>/ has ship.json, active.json with an artifact tuple (`release`, `image_id`, `envelope_hash`, and `static_hash`), and runtime/activations/<id>.env; its v2 deploy journal is releases/journal.v2.jsonl.

Member identity and approvals:

- Every client helper call carries the caller SSH public key fingerprint,
  computed locally from `~/.ssh/ship.pub` or the public half of `SHIP_SSH_KEY`.
- Owner and shipper keys are the teammate trust tier: their authorized_keys
  entries are plain SSH keys, and the helper resolves the client-passed
  fingerprint through the box-global members store and authorized_keys.
- Agent keys are the pinned tier: their authorized_keys entries force
  `ship server agent-shell --member-fingerprint <fingerprint>`. The forced command rejects
  interactive SSH and arbitrary commands, allows only the ship helper protocol
  and deploy upload staging, and overwrites any client fingerprint claim with
  the fingerprint bound to the authenticated key before the privileged helper runs.
- Members and approvals are box-scoped, not app-scoped.

A member is one box-global name, one role, and one or more keys. All keys of
the member share the role. Names are normalized by collapsing whitespace;
`member rename` and `member role` affect every key belonging to that member.

Manifest env:

- `[env]` defines committed container environment variables for every deploy.
- Values are strings. `"@secret"` is the only secret form and names the secret after the env key.
- `[env.preview]` overlays `[env]` for Preview only. Keys merge, and the
  Preview value wins. Production ignores the overlay.
- `[env.preview]` secrets resolve through Preview secret scoping: branch first,
  then shared Preview, never Production.
- The scalar key `preview` is reserved under `[env]`, and no other
  `[env.<name>]` table exists.

Secret scoping:

- `ship secret set KEY` stores a value for the current branch: Production on the production branch, otherwise that branch Preview.
- `ship secret set KEY --preview` stores one shared Preview value.
- `ship secret set KEY --branch <name>` stores a value for that branch Preview env.
- Production resolves Production values only.
- Preview resolves branch value first, then shared Preview value.
- Preview never falls back to Production.
- Values are stdin-only. Keys can be listed; values are never printed.

## Output contract

- Successful `ship` without `--json` writes exactly the deployment URL to stdout.
- All progress, warnings, timings, and next steps go to stderr.
- `ship --json` writes the mutation object to stdout instead of the URL.
- During deploy, stderr has phase lines such as `preflight 0.4s`, `build 6.2s`, `release 1.1s`, `probe ok`, and `live`.
- Human errors are exactly: what failed, cause, then `next: <action>`. `next:` is the next action: a runnable command when one can make progress, or edit guidance when the fix is a file edit.
- JSON errors are `{"error":{"code":"...","message":"...","cause":"...","remediation":"..."}}`.
- Exit codes are `0` success, `1` operation failed, `2` usage or manifest error, except `ship exec` passes through the remote command exit status after setup.
- User-facing language is `Production <branch>` or `Preview <branch>`. Internal env slugs appear only in URLs and JSON fields.

## Data forks

- `ship data fork` copies Production `/data` into the current branch Preview and bounces the existing Preview containers.
- `ship data reset` empties the current branch Preview `/data` and bounces the existing Preview containers.
- `ship data save` streams a data-only gzip tar to the laptop; its default destination is `~/.ship/backups/<app>/<env>-<release>-<utc>.data.tar.gz`. Its stdout is exactly the local path.
- Saving Production data is approval-gated for shippers; owners are direct and agents use the normal approval flow.
- `ship data restore <id|path>` uploads through `/tmp/ship-deploy`, validates the archive before touching `/data`, then stops containers, swaps data, and starts them. Snapshot envs may be restored into another env.
- `ship data ls [--json]` lists local snapshots only; it never calls the helper.
- `data fork` and `data reset` require an existing Preview environment. If none exists, the error code is `no_preview_env` with remediation `ship`.
- `data fork` and `data reset` refuse Production branches with `data_fork_on_production`.
- Owner and shipper roles may run data commands. Agents get `approval_required` because Production data is above the agent default role.
- `ship data fork` prints forked relative file names and byte sizes, the Preview URL, and this exact PII line: `note: Production data, including any PII, now exists in this less-guarded Preview.`.
- If no SQLite files are found, `ship data fork` still copies non-database files and prints: `note: No SQLite files found; copied non-database files from /data only.`.
- Data snapshots use SQLite `VACUUM INTO` and `cp -a` for other files. Their consistency guarantee is per-file, not cross-file.
- Snapshots never contain secrets. After a box loss: `ship box setup`, `ship`, `ship secret set --from .env`, then `ship data restore`.

## Deploy journal schema

Each env has an append-only `journal.v2.jsonl`. Each line is:

```json
{"schema_version":2,"app":"api","env":"production","outcome":"converged | deployed | rolled_back | committed_unconverged | committed_degraded | failed | gc","started_at":"2026-07-07T10:00:00Z","ended_at":"2026-07-07T10:00:10Z","previous_release":"abc123","attempted_release":"def456","activation":"def456-0123abcd","artifact":{"release":"def456","image_id":"<full-64-hex>","envelope_hash":"<full-64-hex>","static_hash":"<full-64-hex>"},"failing_step":"resolve | apply | build | release | probe | converge | durability","stderr_tail":"last scrubbed stderr lines","gc":"cleanup summary","identity":{"ssh_key_comment":"alice","git_author":"Name <name@example.com>"},"member":{"fingerprint":"fingerprint","name":"alice","role":"owner"},"probe":{"status":502,"body_snippet":"scrubbed response body"}}
```

`activation`, `artifact`, `gc`, and `member` are optional (`omitempty`); `artifact` is the exact tuple identity and is attached to every committed outcome. `member.fingerprint` is also optional. `probe` is present as an object or `null`. `committed_unconverged` and `committed_degraded` mean `active.json` was committed but convergence or durability failed; neither path auto-restores the previous release, and both prescribe `ship converge`.

## Webhook payload schemas

All events POST `{"app","env","event","release","summary","why","remediation","ts"}` and never fail the operation. Box events also include `box` (the box hostname).

App events go only to the affected app manifest `webhook` URL: `deploy_aborted`, `deploy_recovered`, and `preview_reaped`. Box events go once to the box URL configured by `ship box webhook`, never to app URLs: `doctor_degraded` and `approval_requested`. With no configured box URL, box events are dropped without failing anything; journals and doctor state are still recorded.

- `deploy_aborted`: `why` is a deploy journal entry; `remediation` is `{"command":"ship","journal":"<entry>"}`.
- `deploy_recovered`: `why` is `{"previous_failure":"<entry>","current":"<entry>"}`; `remediation` is `{"command":"ship status","journal":"<current>","previous_failure":"<previous>"}`.
- `preview_reaped`: `why` is `{"branch":"feature/x","env":"Preview feature/x","expired_at":"..."}`; `remediation` is `{"command":"git checkout feature/x && ship","branch":"feature/x","env":"Preview feature/x"}`.
- `doctor_degraded`: box event; `why` is a doctor check `{"id","status","evidence","remediation"}`; `remediation` is `{"command":"<check.remediation>","check":"<doctor check>"}`.
- `approval_requested`: box event; `why` is `{"id","member","verb","target","expires"}`; `remediation` is `{"command":"ship box approval grant <id> <box>","request":"<approval request>"}`. The request target retains the affected app and env when present; box-target approvals have empty app/env.

## Role matrix

This is the helper's direct authorization table; `approval_required` means a one-shot approval flow is available.

| Role | Read | Ship Preview | Ship Production | Member/box mutations | Grant approval |
|---|---|---|---|---|---|
| owner | direct | direct | direct | direct | any request, except own |
| shipper | direct | direct | direct | approval where required | shipper-gated requests |
| agent | direct | direct | approval | approval | never |

Member mutations and owner-only box mutations therefore do not become direct
shipper or agent access merely because an approval can be requested.

## `ship.toml`

The manifest is strict: unknown top-level keys, process fields, route target
fields, or preview fields fail parsing. The accepted schema is:

- `name` (string, required): app name.
- `box` (string, required): box hostname; `user@host` is not accepted.
- `production_branch` (string, optional): branch treated as Production. Default is `main` when it exists, otherwise `master`, otherwise `main`.
- `release` (string, optional, default empty): container release command.
- `probe` (string, optional, default empty): container probe path; when set it must start with `/`.
- `webhook` (URL string, optional, default empty): app webhook; only `http` and `https` URLs are accepted.
- `[processes]` maps process names to a command string shorthand or a `[processes.<name>]` table. Its `cmd` is a string (default empty), `port` is an optional integer 1..65535, `preview` defaults to `true`, and nested `[processes.<name>.resources]` accepts `memory` only as a positive integer with a lowercase `k`, `m`, or `g` suffix (for example `512m` or `2g`), or `cpus` (a positive number). A port-holding process has `port` and may receive HTTP routes; a portless process is a worker and cannot be a process route. A routed process with no explicit port inherits the sole Dockerfile `EXPOSE` port, or `3000` when there is no sole exposed port.
- `[routes]` maps `host` or `host/path` keys to a process-name string, or to exactly one target table: `{ static = "relative-dir" }` or `{ redirect = "host" }`. Process targets must name a process with a port; static directories are relative to the repo and redirects target a hostname.
- `[preview]` accepts `base` (bare DNS suffix, default empty, which keeps synthesized sslip.io addressing) and `aliases` (boolean, default `false`).
- `[env]` accepts dynamic environment-name keys whose values are strings; `"@secret"` refers to the secret with the same key. `[env.preview]` is the only supported env subtable and overlays `[env]` for Preview. There are no other `[env.<name>]` tables.

<!-- BEGIN SHIP.TOML EXAMPLE -->
```toml
name = "api"
box = "203.0.113.7"
production_branch = "main"
release = "bun run migrate"
probe = "/health"
webhook = "https://ntfy.example/ship"

[env]
LOG_LEVEL = "info"
DATABASE_URL = "@secret"

[env.preview]
LOG_LEVEL = "debug"

[processes.web]
cmd = "bun run server"
port = 8080

[processes.web.resources]
memory = "512m"
cpus = 0.5

[processes.worker]
cmd = "bun run worker"
preview = false

[routes]
"api.example.com" = "web"

[preview]
base = "preview.example.com"
aliases = true

```
<!-- END SHIP.TOML EXAMPLE -->

## Public verbs

<!-- BEGIN VERBS -->
### `ship`
- Purpose: Deploy the current branch and print the deployment URL.
- Usage: `ship [--json] [--branch <name>] [--tls auto|internal] [--rebuild] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--json`: Emit the mutation object instead of stdout-is-URL; `--branch <name>`: Detached HEAD only; supplies the branch used for branch=env resolution; `--tls auto|internal` default `auto`: Select automatic public TLS or internal TLS for synthesized routes; `--rebuild`: Refresh base images and bypass the container build cache.
- `--json` stdout schema: `{"url":"https://...","env":"production","release":"abc123","processes":["web"],"durationMs":1234}`
- Notes: Successful non-JSON stdout is exactly one URL plus a trailing newline; all phase lines go to stderr. Production refuses dirty worktrees and stale checkouts; Preview accepts dirty worktrees and creates the preview mapping if needed. The crash-only lifecycle prepares beside the serving release, commits active.json, then converges. A crash after the commit never auto-restores the previous release: the journal outcome is `committed_unconverged` or `committed_degraded`, and the next step is `ship converge`. Release commands may run more than once across retries and recovery; make them at-least-once safe and idempotent.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `not_a_git_repo`, `detached_head_requires_branch`, `branch_flag_requires_detached_head`, `unmappable_branch_name`, `dirty_worktree`, `behind_production`, `manifest_invalid`, `dockerfile_missing`, `multi_process_no_web_route`, `secret_missing`, `remote_preflight_failed`, `remote_preflight_after_prepare_failed`, `deploy_blocked_local_checks`, `release_command_failed`, `probe_failed`, `dotenv_rejected`, `host_key_changed`

### `init`
- Purpose: Create a ship.toml manifest.
- Usage: `ship init [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest.
- Notes: Never overwrites existing files; kept files are reported on stdout. Writes a skeleton only: name (package.json name or directory name), a placeholder box, and [processes] web = {}. Edit ship.toml to set the real box, ports, [routes], and [preview]; without [routes] the first deploy prints the automatic sslip.io URL.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `usage_error`, `operation_failed`

### `status`
- Purpose: Show all live environments for this app.
- Usage: `ship status [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--json`: Emit structured JSON instead of the text table.
- `--json` stdout schema: `{"app":"api","envs":[{"class":"preview","branch":"feature/x","url":"https://...","capability_url":"https://...?ship=...","env":"feature-x-ab12","current_release":"abc123","health":"running","ageSeconds":10,"expiresAt":"2026-07-10T10:00:00Z","pinned":true,"dirty":true,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","image":"...","release":"abc123","dirty":false,"base_commit":"...","created_at":"...","status":"Up 1 minute"}],"state":"committed, not converged","next":"ship converge"}]}`
- Notes: capability_url is optional and appears only for Preview environments. pinned and dirty are omitted when false. state and next appear when active.json is committed but runtime has not converged; state is `committed, not converged` and next is `ship converge`. A committed failure is reported as degraded rather than auto-restoring the previous release. envs is always an array; it is [] when nothing is live.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `manifest_invalid`, `ssh_unreachable`, `box_not_initialized`, `host_key_changed`, `operation_failed`

### `logs`
- Purpose: Print logs for the current branch environment.
- Usage: `ship logs [process] [--follow|-f] [--tail N] [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `process`: Process name. Optional only when one process exists; `--follow / -f`: Stream new log lines. `-f` is the shorthand; `--tail <N>` default `100`: Number of trailing lines. With --follow, use 0 to stream new lines only; `--json`: Emit captured log lines as JSON. Cannot be combined with --follow.
- `--json` stdout schema: `{"app":"api","env":"production","process":"web","lines":["line 1","line 2"]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `logs_follow_json_conflict`, `unknown_preview_branch`, `host_key_changed`, `operation_failed`

### `exec`
- Purpose: Run a one-off command inside the current branch environment.
- Usage: `ship exec [--branch <name>] [--config <path>] -- <cmd...>`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--branch <name>`: Read/exec another branch environment; `cmd <cmd...>`: Command and arguments passed through to the remote process environment.
- Notes: After setup, stdin/stdout/stderr are passthrough. The command runs with resolved secrets and /data mounted. Use `--` before commands that start with a dash.
- Exit codes: 0 when the remote command exits 0; the remote command exit status is passed through unchanged; 1 only for setup/transport failure; 2 usage or manifest error.
- Common error codes: `usage_error`, `unknown_preview_branch`, `no_deploys`, `host_key_changed`, `operation_failed`

### `why`
- Purpose: Explain the latest deploy journal entry for the current branch environment.
- Usage: `ship why [--branch <name>] [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--branch <name>`: Inspect another branch environment; `--json`: Emit the raw deploy journal entry.
- `--json` stdout schema: `{"schema_version":2,"app":"api","env":"production","outcome":"converged | deployed | rolled_back | committed_unconverged | committed_degraded | failed | gc","started_at":"...","ended_at":"...","previous_release":"abc","attempted_release":"def","activation":"def-0123abcd","artifact":{"release":"def","image_id":"<full-64-hex>","envelope_hash":"<full-64-hex>","static_hash":"<full-64-hex>"},"failing_step":"resolve | apply | build | release | probe | converge | durability","stderr_tail":"...","gc":"...","identity":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"member":{"fingerprint":"SHA256:...","name":"alice","role":"owner"},"probe":null}`
- Notes: JSON is the raw helper journal entry. Outcomes include `converged`, successful deploy/rollback, `committed_unconverged`, `committed_degraded`, `failed`, and `gc`; committed failures mean active intent was committed and prescribe `ship converge`, never automatic restore.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `unknown_preview_branch`, `no_deploys`, `host_key_changed`, `operation_failed`

### `rollback`
- Purpose: Select a verified committed release and converge the current branch environment to it.
- Usage: `ship rollback [release] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `release`: Release to run. Omitted means previous local release.
- Notes: Rollback is intent selection: candidates come from committed `deployed`, `rolled_back`, `committed_unconverged`, and `committed_degraded` journal entries with a non-null artifact tuple whose image or static envelope and runtime artifacts verify. Omitted release selects the newest available candidate that is not active. A torn deploy journal makes implicit selection unsafe and requires an explicit release. Rollback does not auto-restore after a crash; after active.json is committed, failures are `committed_unconverged` or `committed_degraded` and the next step is `ship converge`.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `unknown_preview_branch`, `no_deploys`, `host_key_changed`, `operation_failed`

### `converge`
- Purpose: Make the current branch environment match active.json from box-side state.
- Usage: `ship converge [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--json`: Emit the helper convergence summary as JSON.
- `--json` stdout schema: `{"app":"api","env":"production","release":"abc123","outcome":"converged","stale_containers":["ship-old-container"]}`
- Notes: This is an app-scope repair for the current branch: it uses active.json and release artifacts already on the box, never reads or uploads the local source tree, and is safe to repeat as a no-op. It heals committed-but-not-converged state; the same command is the next step after `committed_unconverged` or `committed_degraded`. Agent-role keys use the normal one-shot approval flow.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `unknown_preview_branch`, `no_deploys`, `approval_required`, `host_key_changed`, `operation_failed`

### `rm`
- Purpose: Destroy an environment by branch name.
- Usage: `ship rm <branch> [--confirm <app>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `branch`: Branch whose environment should be removed; `--confirm <app>`: Required app-name confirmation for Production.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `rm_confirmation_required`, `unknown_preview_branch`, `production_branch_not_preview`, `operation_failed`

### `data fork`
- Purpose: Fork Production /data into the current branch Preview.
- Usage: `ship data fork [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest.
- Notes: Run from a Preview branch whose environment already exists. Production branches are refused. Requires owner or shipper. Agent-role keys mint `approval_required`; after `ship box approval grant <id> <box>`, retry the same command. SQLite files are copied on the box with `VACUUM INTO`; other files copy with `cp -a` using reflink when supported. The client never receives data contents. stdout is exactly the Preview URL; the fork report and PII note are stderr. If the fork landed but the URL lookup fails, exit stays 0 with a stderr warning and `next: ship status`.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `data_fork_on_production`, `no_preview_env`, `approval_required`, `host_key_changed`, `missing_tool`, `operation_failed`

### `data reset`
- Purpose: Reset the current branch Preview /data to empty.
- Usage: `ship data reset [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest.
- Notes: Run from a Preview branch whose environment already exists. Production branches are refused. Requires owner or shipper. Agent-role keys mint `approval_required`; after `ship box approval grant <id> <box>`, retry the same command. stdout is exactly the Preview URL; the reset confirmation is stderr. If the reset landed but the URL lookup fails, exit stays 0 with a stderr warning and `next: ship status`.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `data_fork_on_production`, `no_preview_env`, `approval_required`, `host_key_changed`, `operation_failed`

### `data save`
- Purpose: Save this environment's /data as a local snapshot.
- Usage: `ship data save [--out <path>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--out <path>`: Local path for the snapshot.
- Notes: Snapshots land at ~/.ship/backups/<app>/<env>-<release>-<utc>.data.tar.gz unless --out is supplied. stdout is exactly that local path; narration is stderr. SQLite files use VACUUM INTO and other files use cp -a. Consistency is per-file, not cross-file; live writes across files are not one atomic point in time. Snapshots contain metadata.json and data/ only. Secrets are never included.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `approval_required`, `host_key_changed`, `missing_tool`, `operation_failed`

### `data restore`
- Purpose: Restore this environment's /data from a local snapshot.
- Usage: `ship data restore <id|path> [--confirm <app>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `id|path`: Snapshot filename stem or local path; `--confirm <app>`: Required app-name confirmation when restoring Production.
- Notes: The client uploads to /tmp/ship-deploy; the helper validates gzip/tar, metadata, app identity, and data/ before it stops containers or swaps /data. Snapshot env may differ from the target env. Production restore requires --confirm <app> and an owner role. Shippers may restore preview data; agents receive approval_required.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `data_restore_confirmation_required`, `approval_required`, `data_snapshot_invalid`, `host_key_changed`, `operation_failed`

### `data ls`
- Purpose: List local data snapshots for this app.
- Usage: `ship data ls [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--json`: Emit stable snapshot JSON.
- `--json` stdout schema: `{"snapshots":[{"id":"...","name":"...","size":123,"created":"2026-07-07T10:00:00Z","env":"production","release":"abc123"}]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `operation_failed`

### `preview pin`
- Purpose: Pin a Preview environment so the reaper leaves it running.
- Usage: `ship preview pin <branch> [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `branch`: Preview branch to pin.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `production_branch_not_preview`, `unmappable_branch_name`, `unknown_preview_branch`, `operation_failed`

### `preview unpin`
- Purpose: Unpin a Preview environment so normal expiry applies.
- Usage: `ship preview unpin <branch> [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `branch`: Preview branch to unpin.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `production_branch_not_preview`, `unmappable_branch_name`, `unknown_preview_branch`, `operation_failed`

### `preview share`
- Purpose: Print or rotate this Preview's capability URL.
- Usage: `ship preview share [--rotate] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--rotate`: Generate a new Preview capability and rerender its routes.
- Notes: Requires a current Preview environment. Any member may read; owners and shippers may rotate; agent-role keys receive approval_required for rotation. Stdout is exactly the capability URL. Every Preview is protected and its capability dies when that Preview is reaped.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `no_preview_env`, `share_on_production`, `approval_required`, `host_key_changed`, `operation_failed`

### `ssh`
- Purpose: Open an SSH session to the box for the current app.
- Usage: `ship ssh [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest.
- Exit codes: 0 when SSH exits 0; SSH failures return 1; usage or manifest errors return 2.
- Common error codes: `manifest_invalid`, `ssh_unreachable`, `host_key_changed`, `operation_failed`

### `secret set`
- Purpose: Read one secret value from stdin or bulk-import dotenv KEY=VALUE pairs.
- Usage: `ship secret set (<KEY>|--from <path> [--replace]) [--preview|--branch <name>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `KEY`: Environment variable name, matching ^[A-Za-z_][A-Za-z0-9_]*$; `--preview`: Store the shared Preview value; `--branch <name>`: Store the value for one branch Preview environment; `--from <path>`: Bulk import dotenv KEY=VALUE pairs from a file. Cannot be combined with KEY; `--replace`: With --from, make the file authoritative for the selected scope and remove omitted keys.
- Notes: Single-value mode reads the value from stdin. Bulk mode reads values from the file path; values are never echoed, placed in argv, or written into the repo. Values keep exactly one trailing newline if piped with one; embedded newlines are refused (`secret_invalid`) because the container env-file format cannot carry them — encode multi-line material (for example base64) and decode it in the app. Without --preview or --branch, the current branch selects the secret scope: Production on the production branch, otherwise that branch's Preview. Bulk dotenv rules: blank lines and full-line # comments are ignored; an `export ` prefix is accepted; unquoted values are trimmed; matching single or double quotes around the whole value are stripped; inline # is treated as value text. Bulk merge is the default. `--replace` removes scope keys absent from the file and reports removed key names on stderr. Bulk stdout is empty.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `usage_error`, `invalid_secret_key`, `dotenv_malformed`, `secret_scope_conflict`, `unknown_preview_branch`, `host_key_changed`, `operation_failed`

### `secret ls`
- Purpose: List secret keys for a scope. Values are never printed.
- Usage: `ship secret ls [--preview|--branch <name>] [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--preview`: List the shared Preview scope; `--branch <name>`: List one branch Preview scope; `--json`: Emit structured JSON.
- `--json` stdout schema: `{"app":"api","env":"production","keys":["DATABASE_URL"]}`
- Notes: Without --preview or --branch, lists the current branch's scope: Production on the production branch, otherwise that branch's Preview.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `secret_scope_conflict`, `unknown_preview_branch`, `host_key_changed`, `operation_failed`

### `secret rm`
- Purpose: Remove a secret key from a scope.
- Usage: `ship secret rm <KEY> [--preview|--branch <name>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `KEY`: Environment variable name to remove; `--preview`: Remove from the shared Preview scope; `--branch <name>`: Remove from one branch Preview scope.
- Notes: Without --preview or --branch, removes from the current branch's scope: Production on the production branch, otherwise that branch's Preview.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `invalid_secret_key`, `secret_scope_conflict`, `unknown_preview_branch`, `host_key_changed`, `operation_failed`

### `box setup`
- Purpose: Install or converge a box.
- Usage: `ship box setup <ssh-target> [flags]`
- Arguments and flags: `ssh-target`: Bootstrap SSH target like root@example.com or example.com; `--mode auto|local|remote` default `auto`: Execution mode; `--bootstrap-user <user>`: SSH user for remote bootstrap; `--ssh-key <path>`: SSH private key for remote mode; `--operator-ssh-public-key-file <path>`: SSH public key file for operator access; `--deploy-ssh-public-key-file <path>`: SSH public key file for deploy access. Default: your ship identity becomes the first member; `--check`: Plan changes without mutating the host.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `usage_error`, `invalid_box_target`, `deploy_key_missing`, `operator_key_missing`, `ssh_private_key_missing`, `ssh_public_key_file_missing`, `ssh_public_key_file_empty`, `host_install_requires_root`, `host_install_ssh_failed`, `unsupported_target_architecture`, `host_helper_unavailable`, `host_helper_download_failed`, `host_install_unsupported_os`, `host_install_missing_tool`, `host_install_permission_denied`, `host_install_apply_failed`, `operation_failed`

### `box member add`
- Purpose: Authorize SSH public key access for a deploy member.
- Usage: `ship box member add <key|path|https-url> [<box>] --name <n> [--role owner|shipper|agent] [--confirm <name>@sha256:<digest>]`
- Arguments and flags: `key|path|https-url`: A literal SSH public key, a path to a .pub/.pem file, or an HTTPS keys-URL such as https://github.com/alice.keys; `box`: Box host. Defaults to ship.toml box when run in an app directory; `--name <n>`: Box-global member name recorded for the keys. Always required; identity never derives from key comments or filenames; `--role owner|shipper|agent` default `shipper`: Role recorded for newly added keys; `--confirm <name>@sha256:<digest>`: Commit a previously printed keys-URL plan. The digest binds box, source, name, role, and key material.
- Notes: Literal keys and local files write immediately. An HTTPS keys-URL alone fetches and prints every key with its SHA256 fingerprint, the source URL, and the proposed name and role — it writes nothing — then emits the exact `--confirm <name>@sha256:<digest>` command. Confirm refetches the URL and requires a byte-identical match, so what was reviewed is exactly what installs. Existing keys are deduplicated by key material. Agent-role keys are installed with a forced `agent-shell` command; owner and shipper keys remain plain authorized_keys entries. Adding a key to an existing member prints the real short id for the verify-then-`member rm --key` rotation step.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `keys_url_unavailable`, `ssh_public_key_invalid`, `approval_required`, `member_unknown`, `host_key_changed`, `operation_failed`

### `box member ls`
- Purpose: List members from members.json, grouped with their authorized keys.
- Usage: `ship box member ls [<box>] [--json]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `--json`: Emit structured JSON.
- `--json` stdout schema: `{"members":[{"name":"alice","role":"shipper","keys":[{"id":"SHA256:DUvOnIMvzMmJ","fingerprint":"SHA256:...","type":"ssh-ed25519","current":true}]}]}`
- Notes: Text output has one row per key, with rows sorted so a member's keys are adjacent. JSON groups those key rows under each member.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `member_unknown`, `host_key_changed`, `operation_failed`

### `box member rm`
- Purpose: Remove all or one SSH key for a deploy member.
- Usage: `ship box member rm <name> [--key <id>] [<box>]`
- Arguments and flags: `name`: Stored box-global member name; `--key <id>`: Remove exactly one key by full fingerprint or unique fingerprint-payload prefix (minimum 12 characters); `box`: Box host. Defaults to ship.toml box when run in an app directory.
- Notes: Without --key, removes every key for the member and also clears a stray unrecorded authorized_keys line whose comment matches the name; this is the deliberate way to drop such a stray without setup. With --key, the selector must identify exactly one key belonging to that member. Every mutation must leave an effective owner key.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `member_not_found`, `member_key_not_found`, `member_key_ambiguous`, `member_last_owner`, `approval_required`, `member_unknown`, `host_key_changed`, `operation_failed`

### `box member rename`
- Purpose: Rename a deploy member without changing key material or role.
- Usage: `ship box member rename <old> <new> [<box>]`
- Arguments and flags: `old`: Existing box-global member name; `new`: Unused box-global member name; `box`: Box host. Defaults to ship.toml box when run in an app directory.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `member_not_found`, `member_name_taken`, `member_last_owner`, `approval_required`, `member_unknown`, `host_key_changed`, `operation_failed`

### `box member role`
- Purpose: Change a deploy member's role across every key.
- Usage: `ship box member role <name> <owner|shipper|agent> [<box>]`
- Arguments and flags: `name`: Existing box-global member name; `role owner|shipper|agent`: Role to apply to every key for the member; `box`: Box host. Defaults to ship.toml box when run in an app directory.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `member_not_found`, `member_last_owner`, `approval_required`, `member_unknown`, `host_key_changed`, `operation_failed`

### `box approval ls`
- Purpose: List pending one-shot approvals for out-of-role requests.
- Usage: `ship box approval ls [<box>] [--json]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `--json`: Emit structured pending approvals.
- `--json` stdout schema: `{"approvals":[{"id":"abc123xy","member":"alice","role":"agent","request":"ship app=api env=production class=production release=abc123","expires":"2026-07-08T10:15:00Z"}]}`
- Notes: Listing prunes expired entries. Approvals are box-scoped: run from anywhere by naming the box.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `member_unknown`, `host_key_changed`, `operation_failed`

### `box approval grant`
- Purpose: Grant one pending out-of-role approval.
- Usage: `ship box approval grant <id> [<box>]`
- Arguments and flags: `id`: Approval id to grant; `box`: Box host. Defaults to ship.toml box when run in an app directory.
- Notes: Each request records the role the denied action requires; the approver's role must cover it (owner covers everything, shipper covers shipper-gated requests only) and nobody can grant their own request. Granting refreshes the 15-minute expiry window and gives one retry by the original member. Remediations always print the fully resolved command (`ship box approval grant <id> <box>`) so it can be pasted from anywhere.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `approval_expired`, `member_unknown`, `role_denied`, `host_key_changed`, `operation_failed`

### `box status`
- Purpose: Show helper version, disk use, apps, members, pending approvals, and the last doctor result for one box.
- Usage: `ship box status [<box>] [--json]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `--json`: Emit {helper_version,client_version,ship_version,update_available,helper_ahead,disk:{status,evidence},apps:[{app,env_count}],members:{total,owners},pending_approvals,doctor:{status,recorded_at}}. members and doctor are omitted when their stores are unavailable or have not recorded a run.
- `--json` stdout schema: `{"helper_version":"v0.8.0","client_version":"v0.8.0","ship_version":"v0.8.0","update_available":false,"helper_ahead":false,"disk":{"status":"ok","evidence":"/: used=10.0%"},"apps":[{"app":"api","env_count":2}],"members":{"total":2,"owners":1},"pending_approvals":1,"doctor":{"status":"ok","recorded_at":"2026-07-14T06:00:00Z"}}`
- Notes: Any member may read. When the helper is behind, text output includes `next: ship box update <box>`. Text output prints an app count (`apps: 2 (3 envs)`) — the full table is `ship box app ls` — a member count of distinct member names (`members: 2 (1 owners)`; `members: unknown` and no JSON members field when the member store is unreadable), and ends with the last doctor-timer result and its age (`doctor: ok (2h ago)`, or `doctor: never run`).
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `ssh_unreachable`, `box_not_initialized`, `host_key_changed`, `operation_failed`

### `box gc`
- Purpose: Sweep release artifacts using the box retention policy.
- Usage: `ship box gc [<box>] [--json]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `--json`: Emit per-environment removals and failures as JSON.
- `--json` stdout schema: `{"environments":[{"app":"api","env":"production","active_release":"abc123","kept_releases":["abc123","def456"],"absent":["old@<image-prefix>"],"removed":["image ship-...:old"],"skipped":["activation /var/apps/api.production/runtime/activations/old.env"],"failures":["container old: permission denied"]}]}`
- Notes: The box-wide sweep prints absent, removed, skipped, and failed items. It keeps the active release plus up to 5 newest verified candidates for Production or 2 for Preview; unverifiable artifacts are protected instead of deleted, while confirmed-absent tuples are reported without becoming protected roots, and fresh debris inside the grace period is skipped. Per-environment removals append an `outcome=gc` entry with the removal summary to that env's journal. Agents use the normal approval flow.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `approval_required`, `host_key_changed`, `operation_failed`

### `box update`
- Purpose: Converge a box to this client helper version.
- Usage: `ship box update [<box>]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory.
- Notes: Only owners may update directly; other roles use the normal one-shot approval flow. `box update: already current` is the exact no-op output.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `approval_required`, `client_behind_helper`, `box_target_required`, `invalid_box_target`, `host_key_changed`, `operation_failed`

### `box doctor`
- Purpose: Run box diagnostics.
- Usage: `ship box doctor [<box>] [--json]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `--json`: Emit structured checks instead of text.
- `--json` stdout schema: `[{"id":"hardening_drift","status":"degraded","evidence":"/etc/default/ufw: 1 difference","remediation":"ship box setup 203.0.113.7"},{"id":"service_health","status":"degraded","evidence":"caddy active but disabled","remediation":"ship box doctor 203.0.113.7"}]`
- Notes: Checks include hardening drift against provisioning expectations and required-unit enablement; a required service that is active but disabled is degraded. Doctor state is recorded by the daily timer. Saving Production data is approval-gated for shippers; agents use the normal approval flow.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `ssh_unreachable`, `box_not_initialized`, `host_key_changed`, `operation_failed`

### `box config`
- Purpose: Show effective box configuration and where every value comes from.
- Usage: `ship box config [<box>] [--json]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `--json`: Emit stable effective config JSON.
- `--json` stdout schema: `{"config":{"webhook.url":{"value":"https://ntfy.example/ship","default":"","source":"set"}}}`
- Notes: Any member may read. Every key reports its effective value, default, and whether the value is default or explicitly set.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_config_key_unknown`, `box_config_value_invalid`, `box_target_required`, `invalid_box_target`, `host_key_changed`, `operation_failed`

### `box config set`
- Purpose: Set one schema-authorized box configuration value.
- Usage: `ship box config [<box>] set <key> <value>`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `key`: Configuration key. Current key: webhook.url; `value`: Value validated by the key schema.
- Notes: Authorization is declared by the key schema. webhook.url is owner-set; an out-of-role request mints one approval and succeeds once after ship box approval grant <id> <box>.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `approval_required`, `box_config_key_unknown`, `box_config_value_invalid`, `box_target_required`, `invalid_box_target`, `host_key_changed`, `operation_failed`

### `box config unset`
- Purpose: Restore one box configuration key to its schema default.
- Usage: `ship box config [<box>] unset <key>`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `key`: Configuration key. Current key: webhook.url.
- Notes: Unset removes the explicit value and restores the schema default.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `approval_required`, `box_config_key_unknown`, `box_target_required`, `invalid_box_target`, `host_key_changed`, `operation_failed`

### `box webhook`
- Purpose: Read, set, or clear the box webhook.
- Usage: `ship box webhook <box> [url] [--rm] [--json]`
- Arguments and flags: `box`: Box host. Omit only for a read in an app directory, which uses ship.toml box; `url`: Webhook URL to set. Omit to print the current URL; `--rm`: Clear the box webhook; `--json`: Read only: emit {"url":"..."} with an empty string when unset. Rejected on set and --rm.
- Notes: Any member may read. Only owners may set or clear; other roles receive approval_required and retry after ship box approval grant <id> <box>. This is sugar over box config key webhook.url; both paths share one value and journal shape. When unset, the command prints an unset notice and next: ship box webhook <box> <url> on stderr; stdout stays empty.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `usage_error`, `box_config_value_invalid`, `box_target_required`, `invalid_box_target`, `approval_required`, `host_key_changed`, `operation_failed`

### `box app ls`
- Purpose: Show the box's app table.
- Usage: `ship box app ls [<box>] [--json]`
- Arguments and flags: `box`: Box host. Defaults to ship.toml box when run in an app directory; `--json`: Emit the box app/environment list as JSON.
- `--json` stdout schema: `{"apps":[{"app":"api","envs":[{"class":"production","branch":"main","url":"https://api.example.com","env":"production","current_release":"abc123","health":"running","age_seconds":60,"expires_at":"","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","release":"abc123"}],"static":{"release":"abc123","routes":["api.example.com"]}}]}]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `ssh_unreachable`, `box_not_initialized`, `host_key_changed`, `operation_failed`

### `box app rm`
- Purpose: Destroy an app and all of its environments on a box.
- Usage: `ship box app rm <app> [<box>] --confirm <app>`
- Arguments and flags: `app`: App name to destroy; `box`: Box host. Defaults to ship.toml box when run in an app directory; `--confirm <app>`: Required app-name confirmation.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_app_rm_confirmation_required`, `box_target_required`, `invalid_box_target`, `host_key_changed`, `operation_failed`

### `docs`
- Purpose: Print this complete agent contract.
- Usage: `ship docs`
- Arguments and flags: none.
- Exit codes: 0 success.

### `help`
- Purpose: Print compact usage for one verb.
- Usage: `ship help [verb] [--json]`
- Arguments and flags: `verb`: Command name, such as status, secret ls, or box doctor; `--json`: Emit {verb,purpose,usage,flags,errors}.
- `--json` stdout schema: `{"verb":"status","purpose":"Show all live environments for this app.","usage":"ship status [--json] [--config <path>]","flags":[{"name":"--json","value":"","default":"","purpose":"Emit structured JSON instead of the text table."}],"errors":["manifest_invalid"]}`
- Exit codes: 0 success; 2 unknown verb or usage error.
- Common error codes: `usage_error`

### `completion`
- Purpose: Emit a static shell completion script.
- Usage: `ship completion <bash|zsh|fish>`
- Arguments and flags: `bash|zsh|fish`: Shell to generate completions for.
- Notes: Install bash: `ship completion bash > /etc/bash_completion.d/ship`. Install zsh: `mkdir -p ~/.zsh/completions && ship completion zsh > ~/.zsh/completions/_ship`. Install fish: `mkdir -p ~/.config/fish/completions && ship completion fish > ~/.config/fish/completions/ship.fish`.
- Exit codes: 0 success; 2 unsupported shell or usage error.
- Common error codes: `usage_error`

### `version`
- Purpose: Print the ship version.
- Usage: `ship version`
- Arguments and flags: none.
- Exit codes: 0 success.

<!-- END VERBS -->

## Error-code catalogue

<!-- BEGIN GENERATED ERRCAT -->
- `approval_expired`: approval expired; cause: approval {id} expired for {summary}; remediation: `ship box approval ls {box}`; defaults: `box="<box>"`.
- `approval_required`: approval required for {summary}; cause: {member} ({role}) requested {summary}; approval id {id}; remediation: `ship box approval grant {id} {box}`; defaults: `box="<box>"`.
- `behind_production`: Production ship failed; cause: deployed commit {deployed} {detail}; remediation: `git pull`.
- `box_app_rm_confirmation_required`: box app rm confirmation failed; cause: box app rm requires --confirm {app}; remediation: `ship box app rm {app} {box} --confirm {app}`; defaults: `box="<box>"`.
- `box_config_key_unknown`: box config key is unknown; cause: {key} is not a valid box config key; valid keys: {valid}; remediation: `{command}`.
- `box_config_value_invalid`: box config value is invalid; cause: {key}: {detail}; remediation: `{command}`.
- `box_missing_tool`: box preflight failed; cause: required server tool is missing on {target}: {tool}; remediation: `ship box setup {target}`.
- `box_not_initialized`: box preflight failed; cause: ship server API is missing at /usr/local/bin/ship on {target}; remediation: `ship box setup {target}`.
- `box_setup_required`: box is not set up for ship; cause: the ship helper (or its sudo rules) is missing or stale on this box; remediation: `ship box setup {server}`.
- `box_target_required`: target a box; cause: {known_boxes}; remediation: `{command}`; defaults: `command="ship box app ls <box>", known_boxes="known boxes (~/.config/ship/known_hosts):\n  none known yet"`.
- `box_version_ambiguous`: box update cannot order these builds; cause: helper {helper_version} and client {client_version} are different builds of the same release; remediation: `ship box setup {server}`.
- `branch_flag_requires_detached_head`: branch resolution failed; cause: --branch is only accepted on ship when HEAD is detached; remediation: `ship`.
- `client_behind_helper`: client is behind the box helper; cause: helper version {helper_version} is newer than client version {client_version}; remediation: `curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash`.
- `data_fork_on_production`: data command refused on Production; cause: branch {branch} maps to Production; data commands target Preview branches only; remediation: `git checkout <preview-branch>`.
- `data_restore_confirmation_required`: Production restore confirmation failed; cause: Production restore requires --confirm {app}; remediation: `ship data restore {id_or_path} --confirm {app}`.
- `data_snapshot_invalid`: data snapshot is invalid; cause: {detail}; remediation: `ship data ls`; defaults: `detail="snapshot metadata or data payload is invalid"`.
- `deploy_blocked_local_checks`: deploy blocked by local checks; cause: {detail}; remediation: `{command}`; defaults: `command="fix local checks", detail="local checks reported errors; see stderr above"`.
- `deploy_committed_degraded`: committed but degraded; cause: {detail}; remediation: `ship converge`.
- `deploy_committed_unconverged`: committed but not converged; cause: {detail}; remediation: `ship converge`.
- `deploy_key_missing`: bootstrap SSH key is missing; cause: {detail}; remediation: `{command}`; defaults: `command="ssh-copy-id -i ~/.ssh/ship.pub root@<ip>", detail="provider gave a password; this installs your ship key using it once; hardening then disables password login permanently"`.
- `deploy_tmp_invalid`: host preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `deploy_tmp_missing`: host preflight failed; cause: deploy tmp dir is missing: {path}; remediation: `ship box setup <ssh-target>`.
- `detached_head_requires_branch`: branch resolution failed; cause: HEAD is detached; pass --branch <name> so ship can resolve the environment; remediation: `{command}`.
- `dirty_worktree`: Production ship failed; cause: production branch {branch} has uncommitted changes; remediation: `git add . && git commit -m "<message>"`.
- `dockerfile_missing`: Dockerfile is missing; cause: the declared processes need a Dockerfile to build; remediation: `write a Dockerfile, or declare a [routes] static route in ship.toml`.
- `dotenv_malformed`: dotenv import failed; cause: {detail}; remediation: `{command}`; defaults: `command="ship secret set --from path/to/.env"`.
- `dotenv_rejected`: deploy artifact contains dotenv files; cause: refusing to deploy dotenv file: {files}; import it with ship secret set --from {file}, then remove it; allowed names: .env.example, .env.sample, .env.defaults; remediation: `ship secret set --from {file}`; defaults: `file=".env"`.
- `env_invalid`: app environment preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `env_missing`: app environment preflight failed; cause: {detail}; remediation: `ship`.
- `host_helper_download_failed`: host install helper download failed; cause: {detail}; remediation: `{command}`; defaults: `command="SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>"`.
- `host_helper_unavailable`: host install helper is unavailable; cause: {detail}; remediation: `{command}`; defaults: `command="SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>"`.
- `host_install_apply_failed`: host provisioning failed; cause: {detail}; remediation: `{command}`.
- `host_install_missing_tool`: host install dependency is missing; cause: missing required host tool: {tool}; remediation: `sudo apt-get update && sudo apt-get install -y {tool}`.
- `host_install_permission_denied`: host install needs elevated permissions; cause: {detail}; remediation: `{command}`.
- `host_install_requires_root`: local host install needs root; cause: local mode must run as root; remediation: `{command}`.
- `host_install_ssh_failed`: host install SSH failed; cause: {detail}; remediation: `{command}`.
- `host_install_unsupported_os`: host OS is unsupported; cause: host install requires Ubuntu/Debian apt tooling; missing {tool}; remediation: `ship box setup <ubuntu-24.04-ssh-target>`.
- `host_invalid`: host preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `host_key_changed`: box host key changed; cause: SSH host key for {box} is unknown or changed; if the box was rebuilt, re-establish the pin (ship box forget {box} clears it); if not, investigate before trusting this host; remediation: `ship box setup <ssh-target>`.
- `host_label_conflict`: production hostname collision; cause: app {app} (production) generates host label {label}, already used by {existing_app} ({existing_env}); remediation: `change the top-level name in ship.toml, then ship`.
- `host_not_installed`: host preflight failed; cause: host is not installed; remediation: `ship box setup <ssh-target>`.
- `ingress_invalid`: ingress preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `invalid_box_target`: box target is invalid; cause: box target must be a host like 203.0.113.7; remove any user@ prefix; remediation: `{command}`; defaults: `command="ship box app ls 203.0.113.7"`.
- `invalid_secret_key`: secret key is invalid; cause: secret key {key} must match ^[A-Za-z_][A-Za-z0-9_]*$; remediation: `ship secret set KEY`.
- `keys_url_unavailable`: remote SSH key lookup failed; cause: no public SSH keys found at {source}; remediation: `ship box member add {source} {box} --name {name}`; defaults: `box="<box>", name="<name>", source="<https-url>"`.
- `logs_follow_json_conflict`: logs command is invalid; cause: logs --json cannot be combined with --follow; remediation: `ship logs`.
- `manifest_invalid`: ship.toml validation failed; cause: {details}; remediation: `{command}`; defaults: `command="edit ship.toml to fix the validation error above, then ship"`.
- `member_key_ambiguous`: member key selector is ambiguous; cause: key selector {selector} matches multiple keys: {matches}; remediation: `ship box member rm {name} --key <full-fingerprint> {box}`; defaults: `box="<box>", name="<name>"`.
- `member_key_not_found`: member key selector failed; cause: no such key for member {name}; remediation: `ship box member ls {box}`; defaults: `box="<box>", name="<name>"`.
- `member_last_owner`: member mutation refused; cause: the mutation would leave no effective owner key; at least one effective owner key (an owner record with a matching authorized_keys line) must remain; remediation: `ship box member add <https-url|key|path> {box} --name <new-owner> --role owner`; defaults: `box="<box>"`.
- `member_name_taken`: member rename refused; cause: member name {name} already exists; remediation: `ship box member ls {box}`; defaults: `box="<box>"`.
- `member_not_found`: member rm failed; cause: no authorized keys found for member {name}; current members: {members}; remediation: `ship box member ls {box}`; defaults: `box="<box>"`.
- `member_unknown`: member identity is not authorized; cause: fingerprint {fingerprint} is not in authorized_keys; remediation: `ship box member add <https-url|key|path> {box} --name <name>`; defaults: `box="<box>"`.
- `missing_tool`: host preflight failed; cause: missing host tool: {tool}; remediation: `ship box setup <ssh-target>`.
- `multi_process_no_web_route`: route synthesis failed; cause: manifest declares multiple processes but no [routes] host and no process named "web"; remediation: `name one process web, or add a [routes] host for a process, then ship`.
- `no_deploys`: deploy journal lookup failed; cause: no deploys recorded for {app} ({env}); remediation: `ship`.
- `no_preview_env`: preview environment lookup failed; cause: no Preview environment exists for branch {branch}; remediation: `ship`.
- `not_a_git_repo`: git worktree required; cause: current directory is not inside a Git worktree; remediation: `git init && git add . && git commit -m "initial ship app"`.
- `operation_failed`: operation failed; cause: {detail}; remediation: `{command}`; defaults: `command="ship status"`.
- `operator_key_missing`: operator SSH key is missing; cause: no SSH public key source found for operator user; remediation: `{command}`.
- `probe_failed`: probe failed; cause: {detail}; remediation: `ship why`.
- `production_branch_not_preview`: preview command failed; cause: branch {branch} maps to Production; remediation: `{command}`; defaults: `command="ship preview pin <preview-branch>"`.
- `release_command_failed`: release command failed; cause: {detail}; remediation: `ship why`.
- `remote_preflight_after_prepare_failed`: deploy preflight failed after preparing the app environment; cause: {detail}; remediation: `ship box doctor`.
- `remote_preflight_failed`: deploy preflight failed before upload/build/mutation; cause: {detail}; remediation: `ship box doctor`.
- `rm_confirmation_required`: Production rm confirmation failed; cause: Production rm requires --confirm {app}; remediation: `ship rm {branch} --confirm {app}`.
- `role_denied`: operation denied; cause: {member} ({role}) cannot {summary}; remediation: `{command}`; defaults: `command="ship status"`.
- `secret_invalid`: secret preflight failed; cause: {detail}; remediation: `ship secret set KEY`.
- `secret_missing`: deploy is missing a required secret; cause: missing secret {secret} for {scope}; remediation: `{command}`.
- `secret_read_error`: secret preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `secret_scope_conflict`: secret scope is invalid; cause: --preview and --branch cannot be combined; remediation: `{command}`; defaults: `command="ship secret set KEY --preview"`.
- `share_on_production`: share command refused on Production; cause: branch {branch} maps to Production; share links are for Preview branches only; remediation: `git checkout <preview-branch>`.
- `ssh_private_key_missing`: SSH private key is missing; cause: SSH private key file not found: {path}; remediation: `{command}`.
- `ssh_public_key_file_empty`: SSH public key file is empty; cause: SSH public key file is empty: {path}; remediation: `{command}`.
- `ssh_public_key_file_missing`: SSH public key file is missing; cause: SSH public key file not found: {path}; remediation: `{command}`.
- `ssh_public_key_invalid`: SSH public key is invalid; cause: {detail}; remediation: `ship box member add <https-url|key|path> {box} --name {name}`; defaults: `box="<box>", name="<name>"`.
- `ssh_unreachable`: box preflight failed; cause: SSH failed for {target}: {detail}; remediation: `ssh {target}`.
- `unknown_preview_branch`: preview environment lookup failed; cause: no preview environment is mapped for branch {branch}; remediation: `{command}`; defaults: `command="git checkout <branch> && ship"`.
- `unmappable_branch_name`: branch resolution failed; cause: branch {branch} does not produce a valid environment name; remediation: `git branch -m <new-name>`.
- `unsupported_target_architecture`: host architecture is unsupported; cause: target architecture {arch} is not supported; remediation: `ship box setup <amd64-or-arm64-ssh-target>`.
- `usage_error`: command usage failed; cause: {detail}; remediation: `{command}`; defaults: `command="ship help"`.
<!-- END GENERATED ERRCAT -->
