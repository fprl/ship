# Getting Started

This is the 10-minute path from a fresh Ubuntu VPS to a deployed app.

## 1. Install ship locally

```bash
curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash
```

The installer writes `ship` to `~/.local/bin` by default and prints the exact
`PATH` line if your shell cannot find it.

Create the default deploy key expected by `box init`:

```bash
test -f ~/.ssh/ship-deploy || ssh-keygen -q -t ed25519 -N '' -f ~/.ssh/ship-deploy
test -f ~/.ssh/ship-deploy.pub || ssh-keygen -y -f ~/.ssh/ship-deploy > ~/.ssh/ship-deploy.pub
```

## 2. Converge the box

Run this against the fresh VPS:

```bash
ship box init deploy@203.0.113.7
```

Ingress modes are selected with `--ingress public|cloudflare|private`.
`public` opens Caddy on 80/443, `cloudflare` runs Cloudflare Tunnel and keeps
public 80/443 closed, and `private` keeps public HTTP closed. Admin access is
selected with `--admin public-ssh|tailscale`.

Check the box:

```bash
ship box doctor deploy@203.0.113.7
```

## 3. Initialize the app

From your project directory:

```bash
ship init
```

Edit `ship.toml` so `box` points at the deploy user on the VPS:

```toml
box = "deploy@203.0.113.7"
```

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

## 7. Back up and restore

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
