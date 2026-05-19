# Simple VPS CLI

The Bun CLI deploys JS/TS apps from an app repo or CI runner to a prepared
Simple VPS host. It does not install the host; use the root `install.sh` for
that.

## Commands

```bash
simple-vps init
simple-vps check
simple-vps check production
simple-vps setup production
simple-vps deploy production
simple-vps rollback production
simple-vps status production
simple-vps logs production
simple-vps logs production web
simple-vps restart production web
simple-vps env push production .env.production
simple-vps secret put production API_KEY
simple-vps secret list production
simple-vps secret rm production API_KEY
simple-vps ssh production
simple-vps route list --json
simple-vps route list --server deploy@100.x.y.z
simple-vps host status --server deploy@100.x.y.z
```

## Manifest

The CLI reads `simple-vps.toml` from the app repo root.

```toml
name = "my-app"

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.web]
command = "bun run src/server.ts"
port = 3000
healthcheck = "/health"

[routes.app]
host = "app.example.com"
type = "proxy"
service = "web"
```

## CI SSH Auth

Store these as repository secrets:

- `SIMPLE_VPS_SSH_KEY`: private key for the deploy SSH user
- `SIMPLE_VPS_KNOWN_HOSTS`: known_hosts entry for the VPS

```yaml
name: Deploy

on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4
      - uses: oven-sh/setup-bun@v2
      - run: bun install --frozen-lockfile
      - run: bunx simple-vps deploy production
        env:
          SIMPLE_VPS_SSH_KEY: ${{ secrets.SIMPLE_VPS_SSH_KEY }}
          SIMPLE_VPS_KNOWN_HOSTS: ${{ secrets.SIMPLE_VPS_KNOWN_HOSTS }}
```

When `SIMPLE_VPS_SSH_KEY` is set, the CLI refuses to run without
`SIMPLE_VPS_KNOWN_HOSTS` and uses strict host-key checking for both `ssh` and
`rsync`.
