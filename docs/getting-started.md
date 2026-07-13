# Getting Started

This is the 10-minute path from a fresh Ubuntu VPS to a deployed app.

## 1. Install ship locally

```bash
curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash
```

The installer writes `ship` to `~/.local/bin` by default and prints the exact
`PATH` line if your shell cannot find it.

## 2. Converge the box

Run this against the fresh VPS:

```bash
ship box setup 203.0.113.7
```

`box setup` creates this machine's ship identity at `~/.ssh/ship` on first use.
The member name comes from `git config user.name`, falling back to `$USER`, and
that public key is enrolled as the box's first member with the `owner` role. No
key flags are needed for the normal path.

If your provider gave you a root password instead of installing your SSH key,
install your key once, then run `box setup`:

```bash
ssh-copy-id -i ~/.ssh/ship.pub root@203.0.113.7
ship box setup 203.0.113.7
```

ship uses key auth only. During hardening it disables password login
permanently.

First contact trusts and pins the box host key in
`~/.config/ship/known_hosts`; ship never writes to `~/.ssh/known_hosts`. A
changed key is refused. If you rebuild the VPS at the same address, rerun
`ship box setup <ssh-target>` to re-establish the pin; no manual
`ssh-keygen -R` is needed.

Box setup always opens Caddy on public 80/443 and hardens SSH to keys-only
access. There are no topology flags.

Check the box:

```bash
ship box doctor 203.0.113.7
```

## 3. Initialize the app

From your project directory:

```bash
ship init
```

Edit `ship.toml` so `box` points at the VPS host:

```toml
box = "203.0.113.7"
```

The manifest value is host-only. Use `203.0.113.7`, not `root@203.0.113.7`.
Only `ship box setup <ssh-target>` accepts a bootstrap target with a user.

Commit before the first Production deploy:

```bash
git init
git add .
git commit -m "initial ship app"
```

## 4. Ship it

```bash
ship
```

Progress goes to stderr. Stdout is exactly one HTTPS URL.

```text
https://prod.203-0-113-7.sslip.io
```

## 5. Add a domain later

Point DNS at the box:

```text
A app.example.com → 203.0.113.7
```

Then add a route:

```toml
[routes]
"app.example.com" = "web"
```

Deploy the route change:

```bash
git add ship.toml
git commit -m "route app domain"
ship
```

## 6. Add a teammate

Authorize a GitHub user's public SSH keys:

```bash
ship member add alice
```

You can also pass a literal public key or a path to a `.pub` file:

```bash
ship member add ~/.ssh/alice.pub
```

The default role is `shipper`, which covers deploys, logs, exec, rollback,
secrets, previews, and data forks. Use `--role owner` for someone who should
manage members and destructive box/app operations, or `--role agent` for a key
limited to Preview deploys and reads:

```bash
ship member add alice --role owner
ship member add ~/.ssh/agent.pub --role agent
ship member ls
```

`member add` prints each key's SHA256 fingerprint. After this, invite the
teammate to the repo; their first `ship` will use their key and the box member
record.

## 7. Protect a Preview

Previews are always protected. No `ship.toml` setting is required.

Create and deploy a Preview branch:

```bash
git switch -c feature/billing
ship
```

`ship` prints the Preview capability URL. CI and agents send its token as
`x-ship-capability: <token>`.

Reprint the URL or rotate its token:

```bash
ship preview share
ship preview share --rotate
```

Opening the URL grants that browser access to the clean Preview URL. Production
stays public.

## 8. Test a risky data change

Create and deploy a Preview branch first:

```bash
git switch -c migration/accounts-v2
ship
```

Then copy Production `/data` into that Preview:

```bash
ship data fork
ship exec -- npm run migrate
ship
```

Now the Preview has real production-shaped data for verification, while
Production stays read-only. Data commands only run from Preview branches.
`owner` and `shipper` can run them directly; an `agent` key gets
`approval_required`.

Empty the Preview data when you are done:

```bash
ship data rm
```

If an out-of-role action asks for approval, an owner or shipper can list and
grant one retry:

```bash
ship approve
ship approve abc123xy
```

Approvals expire after 15 minutes.

## 9. Back up and restore

Create a backup for the current branch environment:

```bash
ship save
```

Restore by backup ID or path:

```bash
ship restore --from 20260707T100000Z-abc123
```

Backups include `/data`, active static assets, the applied manifest snapshot,
release metadata, and secrets for that app environment.
