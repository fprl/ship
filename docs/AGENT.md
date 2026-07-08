# ship agent contract

This file is written for coding agents operating `ship`. Treat it as
the durable CLI contract. The implementation can change internally; these
surfaces should not drift.

## Mental model

The product has five ideas:

- `repo`: a Git checkout containing one `ship.toml` manifest.
- `box`: one hardened Linux host reached over SSH.
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
- `ship pin <branch>` clears expiry; `ship unpin <branch>` restores it.
- The box reaper destroys expired previews and purges their secrets.
- Production is never reaped. `ship rm` on Production requires `--confirm <app>`.
- Preview URLs are the preview env host, usually a synthesized sslip.io host
  unless a later wildcard-domain feature exists.

Truth stores:

- Manifest truth is the repo `ship.toml` plus the manifest snapshot stored with each
  release under the env release directory on the box.
- Box truth is host state: env identity files, preview mapping metadata,
  release metadata, deploy journals, secrets, Podman labels, Caddy fragments,
  and doctor state.
- Use manifest snapshots to answer "what did this release intend?"
- Use box state to answer "what is live now?"

Manifest env:

- `[env]` defines committed container environment variables for every deploy.
- Values are strings. `"@secret"` means secret name equals the env key;
  `"@secret:NAME"` points at a different secret key.
- `[env.preview]` overlays `[env]` for Preview only. Keys merge, and the
  Preview value wins. Production ignores the overlay.
- `[env.preview]` secrets resolve through Preview secret scoping: branch first,
  then shared Preview, never Production.
- The scalar key `preview` is reserved under `[env]`, and no other
  `[env.<name>]` table exists.

Secret scoping:

- `ship secret set KEY` stores the Production value.
- `ship secret set KEY --preview` stores one shared Preview value.
- `ship secret set KEY --branch <name>` stores a value for that branch Preview env.
- Production resolves Production values only.
- Preview resolves branch value first, then shared Preview value.
- Preview never falls back to Production.
- Values are stdin-only. Keys can be listed; values are never printed.

## Public verbs

<!-- BEGIN VERBS -->
### `ship`
- Purpose: Deploy the current branch and print the deployment URL.
- Usage: `ship [--json] [--branch <name>] [--tls auto|internal] [--rebuild] [--include-dotenv] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--json`: Emit the mutation object instead of stdout-is-URL; `--branch <name>`: Detached HEAD only; supplies the branch used for branch=env resolution; `--tls auto|internal` default `auto`: Select automatic public TLS or internal TLS for synthesized routes; `--rebuild`: Refresh base images and bypass the container build cache; `--include-dotenv`: Allow .env-style files in the uploaded artifact.
- `--json` stdout schema: `{"url":"https://...","env":"prod","release":"abc123","processes":["web"],"durationMs":1234}`
- Notes: Successful non-JSON stdout is exactly one URL plus a trailing newline; all phase lines go to stderr. Production refuses dirty worktrees and stale checkouts; Preview accepts dirty worktrees and creates the preview mapping if needed.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `not_a_git_repo`, `detached_head_requires_branch`, `branch_flag_requires_detached_head`, `unmappable_branch_name`, `dirty_worktree`, `behind_production`, `manifest_invalid`, `dockerfile_missing`, `multi_process_no_web_route`, `secret_missing`, `remote_preflight_failed`, `remote_preflight_after_prepare_failed`, `deploy_blocked_local_checks`, `release_command_failed`, `probe_failed`, `dotenv_rejected`

### `init`
- Purpose: Create local project files and a ship.toml manifest.
- Usage: `ship init [--template container|static|php|hono] [--name <app>] [--box <ssh-target>] [--host <host>] [--port <port>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--template container|static|php|hono` default `container`: Scaffold shape; `--name <app>`: App name. Defaults to package.json name or the directory name; `--box <ssh-target>` default `deploy@example.com`: Box SSH target written to the manifest; `--host <host>`: Route host. Defaults to <app>.example.com; `--port <port>`: Internal process port for container templates.
- Notes: Never overwrites existing files; kept files are reported on stdout.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `usage_error`, `manifest_invalid`

### `status`
- Purpose: Show all live environments for this app.
- Usage: `ship status [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--json`: Emit structured JSON instead of the text table.
- `--json` stdout schema: `{"app":"api","envs":[{"class":"production","branch":"main","url":"https://...","env":"prod","release":"abc123","health":"healthy","ageSeconds":10,"expiresAt":"2026-07-10T10:00:00Z","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","image":"...","release":"abc123","dirty":false,"base_commit":"...","created_at":"...","status":"Up 1 minute"}]}]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `manifest_invalid`, `ssh_unreachable`, `box_not_initialized`, `operation_failed`

### `logs`
- Purpose: Print logs for the current branch environment.
- Usage: `ship logs [process] [--follow] [--tail N] [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `process`: Process name. Optional only when one process exists; `--follow`: Stream new log lines; `--tail <N>` default `100`: Number of trailing lines in non-follow mode; `--json`: Emit captured log lines as JSON. Cannot be combined with --follow.
- `--json` stdout schema: `{"app":"api","env":"prod","process":"web","lines":["line 1","line 2"]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `logs_follow_json_conflict`, `unknown_preview_branch`, `operation_failed`

### `exec`
- Purpose: Run a one-off command inside the current branch environment.
- Usage: `ship exec [--branch <name>] [--config <path>] -- <cmd...>`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--branch <name>`: Read/exec another branch environment; `cmd <cmd...>`: Command and arguments passed through to the remote process environment.
- Notes: After setup, stdin/stdout/stderr are passthrough. The command runs with resolved secrets and /data mounted. Use `--` before commands that start with a dash.
- Exit codes: 0 when the remote command exits 0; the remote command exit status is passed through unchanged; 1 only for setup/transport failure; 2 usage or manifest error.
- Common error codes: `usage_error`, `unknown_preview_branch`, `no_deploys`, `operation_failed`

### `why`
- Purpose: Explain the latest deploy journal entry for the current branch environment.
- Usage: `ship why [--branch <name>] [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--branch <name>`: Inspect another branch environment; `--json`: Emit the raw deploy journal entry.
- `--json` stdout schema: `{"schema_version":1,"app":"api","env":"prod","outcome":"aborted_probe","started_at":"...","ended_at":"...","previous_release":"abc","attempted_release":"def","failing_step":"probe","stderr_tail":"...","identity":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"probe":{"status":502,"body_snippet":"..."}}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `unknown_preview_branch`, `no_deploys`, `operation_failed`

### `rollback`
- Purpose: Move the current branch environment back to a previous release.
- Usage: `ship rollback [release] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `release`: Release to run. Omitted means previous local release.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `unknown_preview_branch`, `no_deploys`, `operation_failed`

### `rm`
- Purpose: Destroy an environment by branch name.
- Usage: `ship rm <branch> [--confirm <app>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `branch`: Branch whose environment should be removed; `--confirm <app>`: Required app-name confirmation for Production.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `rm_confirmation_required`, `unknown_preview_branch`, `production_branch_not_preview`, `operation_failed`

### `pin`
- Purpose: Pin a Preview environment so the reaper leaves it running.
- Usage: `ship pin <branch> [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `branch`: Preview branch to pin.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `production_branch_not_preview`, `unmappable_branch_name`, `unknown_preview_branch`, `operation_failed`

### `unpin`
- Purpose: Unpin a Preview environment so normal expiry applies.
- Usage: `ship unpin <branch> [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `branch`: Preview branch to unpin.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `production_branch_not_preview`, `unmappable_branch_name`, `unknown_preview_branch`, `operation_failed`

### `save`
- Purpose: Create a backup for the current branch environment.
- Usage: `ship save [--to <path>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--to <path>`: Destination directory on the host. Supports plain paths and file:// URLs.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `unknown_preview_branch`, `operation_failed`

### `restore`
- Purpose: Restore the current branch environment from a backup.
- Usage: `ship restore --from <id|path> [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--from <id|path>`: Backup ID or path on the host.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `unknown_preview_branch`, `operation_failed`

### `ssh`
- Purpose: Open an SSH session to the box for the current app.
- Usage: `ship ssh [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest.
- Exit codes: 0 when SSH exits 0; SSH failures return 1; usage or manifest errors return 2.
- Common error codes: `manifest_invalid`, `ssh_unreachable`, `operation_failed`

### `secret set`
- Purpose: Read one secret value from stdin or bulk-import dotenv KEY=VALUE pairs.
- Usage: `ship secret set (<KEY>|--from <path> [--replace]) [--preview|--branch <name>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `KEY`: Environment variable name, matching ^[A-Za-z_][A-Za-z0-9_]*$; `--preview`: Store the shared Preview value; `--branch <name>`: Store the value for one branch Preview environment; `--from <path>`: Bulk import dotenv KEY=VALUE pairs from a file. Cannot be combined with KEY; `--replace`: With --from, make the file authoritative for the selected scope and remove omitted keys.
- Notes: Single-value mode reads the value from stdin. Bulk mode reads values from the file path; values are never echoed, placed in argv, or written into the repo. Bulk dotenv rules: blank lines and full-line # comments are ignored; an `export ` prefix is accepted; unquoted values are trimmed; matching single or double quotes around the whole value are stripped; inline # is treated as value text. Bulk merge is the default. `--replace` removes scope keys absent from the file and reports removed key names on stderr. Bulk stdout is empty.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `usage_error`, `invalid_secret_key`, `dotenv_malformed`, `secret_scope_conflict`, `unknown_preview_branch`, `operation_failed`

### `secret ls`
- Purpose: List secret keys for a scope. Values are never printed.
- Usage: `ship secret ls [--preview|--branch <name>] [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `--preview`: List the shared Preview scope; `--branch <name>`: List one branch Preview scope; `--json`: Emit structured JSON.
- `--json` stdout schema: `{"app":"api","env":"prod","keys":["DATABASE_URL"]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `secret_scope_conflict`, `unknown_preview_branch`, `operation_failed`

### `secret rm`
- Purpose: Remove a secret key from a scope.
- Usage: `ship secret rm <KEY> [--preview|--branch <name>] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest; `KEY`: Environment variable name to remove; `--preview`: Remove from the shared Preview scope; `--branch <name>`: Remove from one branch Preview scope.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `invalid_secret_key`, `secret_scope_conflict`, `unknown_preview_branch`, `operation_failed`

### `box setup`
- Purpose: Install or converge a box.
- Usage: `ship box setup <ssh-target> [flags]`
- Arguments and flags: `ssh-target`: SSH target like deploy@example.com; `--mode auto|local|remote` default `auto`: Execution mode; `--host <host>`: Target VPS host for remote bootstrap; `--bootstrap-user <user>`: SSH user for remote bootstrap; `--ssh-key <path>`: SSH private key for remote mode; `--operator-ssh-public-key-file <path>`: SSH public key file for operator access; `--deploy-ssh-public-key-file <path>`: SSH public key file for deploy access. Default: your ship identity becomes the first member; `--operator-user <user>`: Operator user; `--deploy-user <user>`: Deploy user; `--timezone <tz>`: Host timezone; `--locale <locale>`: Host locale; `--ingress public|cloudflare|private`: Ingress mode; `--admin public-ssh|tailscale`: Admin access mode; `--tailscale / --no-tailscale`: Install and configure Tailscale; `--tailscale-auth-key <key>`: Tailscale auth key; `--tailscale-hostname <name>`: Tailscale hostname; `--cloudflare-tunnel / --no-cloudflare-tunnel`: Install and configure Cloudflare Tunnel; `--cloudflare-api-token <token>`: Cloudflare API token; `--cloudflare-account-id <id>`: Cloudflare account ID; `--cloudflare-tunnel-token <token>`: Cloudflare tunnel token; `--cloudflare-tunnel-config <path>`: Cloudflare tunnel config path; `--docker / --no-docker`: Install Docker; `--litestream / --no-litestream`: Install Litestream; `--check`: Plan changes without mutating the host; `--yes`: Non-interactive mode.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `usage_error`, `invalid_box_target`, `deploy_key_missing`, `operator_key_missing`, `ssh_private_key_missing`, `ssh_public_key_file_missing`, `ssh_public_key_file_empty`, `host_install_requires_root`, `host_install_ssh_failed`, `unsupported_target_architecture`, `host_helper_unavailable`, `host_helper_download_failed`, `host_install_unsupported_os`, `host_install_missing_tool`, `host_install_permission_denied`, `host_install_apply_failed`, `operation_failed`

### `member add`
- Purpose: Authorize SSH public key access for a deploy member.
- Usage: `ship member add <github-user|key|path> [--config <path>]`
- Arguments and flags: `github-user|key|path`: A GitHub username, literal SSH public key, or path to a .pub file; `--config <path>` default `ship.toml`: Path to the app manifest containing box.
- Notes: Bare GitHub usernames fetch https://github.com/<user>.keys. The command prints every fetched key as added or skipped, with key type and SHA256 fingerprint. Existing keys are deduplicated by key material.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `manifest_invalid`, `invalid_box_target`, `github_keys_unavailable`, `ssh_public_key_invalid`, `operation_failed`

### `member ls`
- Purpose: List deploy members from authorized_keys.
- Usage: `ship member ls [--json] [--config <path>]`
- Arguments and flags: `--config <path>` default `ship.toml`: Path to the app manifest containing box; `--json`: Emit structured JSON.
- `--json` stdout schema: `{"members":[{"name":"alice","key_type":"ssh-ed25519","fingerprint":"SHA256:..."}]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `manifest_invalid`, `invalid_box_target`, `operation_failed`

### `member rm`
- Purpose: Remove all SSH keys for a deploy member.
- Usage: `ship member rm <name> [--config <path>]`
- Arguments and flags: `name`: Member name, matching the authorized key comment; `--config <path>` default `ship.toml`: Path to the app manifest containing box.
- Notes: Removes every key whose comment equals the member name. Refuses to remove the last remaining authorized key.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `manifest_invalid`, `invalid_box_target`, `member_not_found`, `member_last_key`, `operation_failed`

### `box doctor`
- Purpose: Run box diagnostics.
- Usage: `ship box doctor [ssh-target] [--json]`
- Arguments and flags: `ssh-target`: SSH target. Defaults to ship.toml box when run in an app directory; `--json`: Emit structured checks instead of text.
- `--json` stdout schema: `[{"id":"disk_space","status":"ok","evidence":"used=10%","remediation":"ship box doctor deploy@example.com"}]`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `ssh_unreachable`, `box_not_initialized`, `operation_failed`

### `box ls`
- Purpose: List app environments visible on a box.
- Usage: `ship box ls [ssh-target] [--json]`
- Arguments and flags: `ssh-target`: SSH target. Defaults to ship.toml box when run in an app directory; `--json`: Emit the fleet view JSON.
- `--json` stdout schema: `{"apps":[{"app":"api","envs":[{"class":"production","branch":"main","url":"https://api.example.com","env":"prod","current_release":"abc123","health":"healthy","age_seconds":60,"expires_at":"","pinned":false,"dirty":false,"shipped_by":{"ssh_key_comment":"key","git_author":"Name <n@example.com>"},"processes":[{"process":"web","container":"...","state":"running","release":"abc123"}],"static":{"release":"abc123","routes":["api.example.com"]}}]}]}`
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_target_required`, `invalid_box_target`, `ssh_unreachable`, `box_not_initialized`, `operation_failed`

### `box rm`
- Purpose: Destroy an app and all of its environments on a box.
- Usage: `ship box rm <app> [ssh-target] --confirm <app>`
- Arguments and flags: `app`: App name to destroy; `ssh-target`: SSH target. Defaults to ship.toml box when run in an app directory; `--confirm <app>`: Required app-name confirmation.
- Exit codes: 0 success; 1 operation failed with an error object when available; 2 usage or manifest error.
- Common error codes: `box_rm_confirmation_required`, `box_target_required`, `invalid_box_target`, `operation_failed`

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

## Output contract

- Successful `ship` without `--json` writes exactly the deployment URL to stdout.
- All progress, warnings, timings, and next steps go to stderr.
- `ship --json` writes the mutation object to stdout instead of the URL.
- During deploy, stderr has phase lines such as `preflight 0.4s`, `build 6.2s`, `release 1.1s`, `probe ok`, and `live`.
- Human errors are exactly: what failed, cause, then `next: <command>`.
- JSON errors are `{"error":{"code":"...","message":"...","cause":"...","remediation":"..."}}`.
- Exit codes are `0` success, `1` operation failed, `2` usage or manifest error, except `ship exec` passes through the remote command exit status after setup.
- User-facing language is `Production <branch>` or `Preview <branch>`. Internal env slugs appear only in URLs and JSON fields.

## Deploy journal schema

Each env has an append-only `journal.jsonl`. Each line is:

```json
{"schema_version":1,"app":"api","env":"prod","outcome":"deployed | aborted_build | aborted_release | aborted_probe | rolled_back","started_at":"2026-07-07T10:00:00Z","ended_at":"2026-07-07T10:00:10Z","previous_release":"abc123","attempted_release":"def456","failing_step":"build | release | probe","stderr_tail":"last scrubbed stderr lines","identity":{"ssh_key_comment":"alice","git_author":"Name <name@example.com>"},"probe":{"status":502,"body_snippet":"scrubbed response body"}}
```

## Notify payload schemas

All events POST `{"app","env","event","release","summary","why","remediation","ts"}` and never fail the operation.

- `deploy_aborted`: `why` is a deploy journal entry; `remediation` is `{"command":"ship","journal":"<entry>"}`.
- `deploy_recovered`: `why` is `{"previous_failure":"<entry>","current":"<entry>"}`; `remediation` is `{"command":"ship status","journal":"<current>","previous_failure":"<previous>"}`.
- `preview_reaped`: `why` is `{"branch":"feature/x","env":"Preview feature/x","expired_at":"..."}`; `remediation` is `{"command":"git checkout feature/x && ship","branch":"feature/x","env":"Preview feature/x"}`.
- `doctor_degraded`: `why` is a doctor check `{"id","status","evidence","remediation"}`; `remediation` is `{"command":"<check.remediation>","check":"<doctor check>"}`.

## Error-code catalogue

<!-- BEGIN GENERATED ERRCAT -->
- `behind_production`: Production ship failed; cause: deployed commit {deployed} {detail}; remediation: `git pull`.
- `box_missing_tool`: box preflight failed; cause: required server tool is missing on {target}: {tool}; remediation: `ship box setup {target}`.
- `box_not_initialized`: box preflight failed; cause: ship server API is missing at /usr/local/bin/ship on {target}; remediation: `ship box setup {target}`.
- `box_rm_confirmation_required`: box rm confirmation failed; cause: box rm requires --confirm {app}; remediation: `ship box rm {app} --confirm {app}`.
- `box_target_required`: box target is required; cause: this command needs an SSH target outside an app directory; remediation: `{command}`; defaults: `command="ship box ls <ssh-target>"`.
- `branch_flag_requires_detached_head`: branch resolution failed; cause: --branch is only accepted on ship when HEAD is detached; remediation: `ship`.
- `deploy_blocked_local_checks`: deploy blocked by local checks; cause: {detail}; remediation: `{command}`; defaults: `command="fix local checks", detail="local checks reported errors; see stderr above"`.
- `deploy_key_missing`: bootstrap SSH key is missing; cause: {detail}; remediation: `{command}`; defaults: `command="ssh-copy-id -i ~/.ssh/ship.pub root@<ip>", detail="provider gave a password; this installs your ship key using it once; hardening then disables password login permanently"`.
- `deploy_tmp_invalid`: host preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `deploy_tmp_missing`: host preflight failed; cause: deploy tmp dir is missing: {path}; remediation: `ship box setup <ssh-target>`.
- `detached_head_requires_branch`: branch resolution failed; cause: HEAD is detached; pass --branch <name> so ship can resolve the environment; remediation: `{command}`.
- `dirty_worktree`: Production ship failed; cause: production branch {branch} has uncommitted changes; remediation: `git add . && git commit -m "<message>"`.
- `dockerfile_missing`: Dockerfile is missing; cause: manifest declares processes but is missing a Dockerfile; remediation: `ship init`.
- `dotenv_malformed`: dotenv import failed; cause: {detail}; remediation: `{command}`; defaults: `command="ship secret set --from path/to/.env"`.
- `dotenv_rejected`: deploy artifact contains dotenv files; cause: refusing to deploy dotenv file: {files}; remediation: `ship --include-dotenv`.
- `env_invalid`: app environment preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `env_missing`: app environment preflight failed; cause: {detail}; remediation: `ship`.
- `github_keys_unavailable`: GitHub SSH key lookup failed; cause: no public SSH keys found for GitHub user {user}; remediation: `ship member add <path-to-public-key>`.
- `host_helper_download_failed`: host install helper download failed; cause: {detail}; remediation: `{command}`; defaults: `command="SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>"`.
- `host_helper_unavailable`: host install helper is unavailable; cause: {detail}; remediation: `{command}`; defaults: `command="SHIP_REPO_ROOT=<path-to-ship-checkout> ship box setup <ssh-target>"`.
- `host_install_apply_failed`: host provisioning failed; cause: {detail}; remediation: `{command}`.
- `host_install_missing_tool`: host install dependency is missing; cause: missing required host tool: {tool}; remediation: `sudo apt-get update && sudo apt-get install -y {tool}`.
- `host_install_permission_denied`: host install needs elevated permissions; cause: {detail}; remediation: `{command}`.
- `host_install_requires_root`: local host install needs root; cause: local mode must run as root; remediation: `{command}`.
- `host_install_ssh_failed`: host install SSH failed; cause: {detail}; remediation: `{command}`.
- `host_install_unsupported_os`: host OS is unsupported; cause: host install requires Ubuntu/Debian apt tooling; missing {tool}; remediation: `ship box setup <ubuntu-24.04-ssh-target>`.
- `host_invalid`: host preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `host_not_installed`: host preflight failed; cause: host is not installed; remediation: `ship box setup <ssh-target>`.
- `ingress_invalid`: ingress preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `invalid_box_target`: box target is invalid; cause: box target must be an SSH target like deploy@example.com; remediation: `{command}`; defaults: `command="ship box ls deploy@example.com"`.
- `invalid_secret_key`: secret key is invalid; cause: secret key {key} must match ^[A-Za-z_][A-Za-z0-9_]*$; remediation: `ship secret set KEY`.
- `logs_follow_json_conflict`: logs command is invalid; cause: logs --json cannot be combined with --follow; remediation: `ship logs`.
- `manifest_invalid`: ship.toml validation failed; cause: {details}; remediation: `{command}`; defaults: `command="fix ship.toml"`.
- `member_last_key`: member rm refused; cause: removing {name} would remove the last remaining authorized key; remediation: `ship member add <github-user|key|path>`.
- `member_not_found`: member rm failed; cause: no authorized keys found for member {name}; current members: {members}; remediation: `ship member ls`.
- `missing_tool`: host preflight failed; cause: missing host tool: {tool}; remediation: `ship box setup <ssh-target>`.
- `multi_process_no_web_route`: route synthesis failed; cause: manifest declares multiple processes but no [routes] host and no process named "web"; remediation: `fix ship.toml`.
- `no_deploys`: deploy journal lookup failed; cause: no deploys recorded for {app} ({env}); remediation: `ship`.
- `not_a_git_repo`: git worktree required; cause: current directory is not inside a Git worktree; remediation: `git init && git add . && git commit -m "initial ship app"`.
- `operation_failed`: operation failed; cause: {detail}; remediation: `{command}`; defaults: `command="ship status"`.
- `operator_key_missing`: operator SSH key is missing; cause: no SSH public key source found for operator user; remediation: `{command}`.
- `probe_failed`: probe failed; cause: {detail}; remediation: `ship why`.
- `production_branch_not_preview`: preview command failed; cause: branch {branch} maps to Production; remediation: `{command}`; defaults: `command="ship pin <preview-branch>"`.
- `release_command_failed`: release command failed; cause: {detail}; remediation: `ship why`.
- `remote_preflight_after_prepare_failed`: deploy preflight failed after preparing the app environment; cause: {detail}; remediation: `ship box doctor`.
- `remote_preflight_failed`: deploy preflight failed before upload/build/mutation; cause: {detail}; remediation: `ship box doctor`.
- `rm_confirmation_required`: Production rm confirmation failed; cause: Production rm requires --confirm {app}; remediation: `ship rm {branch} --confirm {app}`.
- `secret_invalid`: secret preflight failed; cause: {detail}; remediation: `ship secret set KEY`.
- `secret_missing`: deploy is missing a required secret; cause: missing secret {secret} for {scope}; remediation: `{command}`.
- `secret_read_error`: secret preflight failed; cause: {detail}; remediation: `ship box doctor`.
- `secret_scope_conflict`: secret scope is invalid; cause: --preview and --branch cannot be combined; remediation: `{command}`; defaults: `command="ship secret set KEY --preview"`.
- `ssh_private_key_missing`: SSH private key is missing; cause: SSH private key file not found: {path}; remediation: `{command}`.
- `ssh_public_key_file_empty`: SSH public key file is empty; cause: SSH public key file is empty: {path}; remediation: `{command}`.
- `ssh_public_key_file_missing`: SSH public key file is missing; cause: SSH public key file not found: {path}; remediation: `{command}`.
- `ssh_public_key_invalid`: SSH public key is invalid; cause: {detail}; remediation: `ship member add <github-user|key|path>`.
- `ssh_unreachable`: box preflight failed; cause: SSH failed for {target}: {detail}; remediation: `ssh {target}`.
- `unknown_preview_branch`: preview environment lookup failed; cause: no preview environment is mapped for branch {branch}; remediation: `{command}`; defaults: `command="git checkout <branch> && ship"`.
- `unmappable_branch_name`: branch resolution failed; cause: branch {branch} does not produce a valid environment name; remediation: `git branch -m <new-name>`.
- `unsupported_target_architecture`: host architecture is unsupported; cause: target architecture {arch} is not supported; remediation: `ship box setup <amd64-or-arm64-ssh-target>`.
- `usage_error`: command usage failed; cause: {detail}; remediation: `{command}`; defaults: `command="ship help"`.
<!-- END GENERATED ERRCAT -->
