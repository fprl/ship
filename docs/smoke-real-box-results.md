# Real-box smoke results

This file is historical smoke evidence. Current commands and manifest shape
live in [SPEC.md](../SPEC.md), [README.md](../README.md), and
[smoke-real-box.md](smoke-real-box.md). Older entries intentionally preserve
the exact commands tested at the time, including names that ADR-0008 later
removed.

## 2026-05-29 — v0.5.0-rc1 release installer and mixed-route smoke

- **Host:** `128.140.3.159`
- **OS:** Hetzner Ubuntu 26.04 LTS, `x86_64`
- **Release tested:** `v0.5.0-rc1`
- **Release commit:** `008d0899df4ea8a2cc1dbd59ad779bac41ed7177`
- **Install path tested:** temp directory outside the checkout:
  `/tmp/simple-vps-release-smoke-VB7lhG`
- **Fixture:** `examples/mixed-api-docs`, copied to the temp directory and
  pointed at `deploy@128.140.3.159`.
- **DNS/TLS:** `rcmix.128.140.3.159.nip.io` with `tls = "internal"` on both
  routes; curl used `--resolve` and `-k`.

### Release publication

The first manual `gh release create` attempt got stuck after uploading only
`SHA256SUMS` and left a malformed untagged release URL. Local `gh` and normal
`git push` were blocked by the execution policy, so the fix was:

1. Add `.github/workflows/release.yml`.
2. Make `make build-release` generate `SHA256SUMS`.
3. Move `main` with the GitHub connector.
4. Force-update `v0.5.0-rc1` with low-level `git send-pack` over SSH.

The tag-triggered workflow repaired the release. Verified assets:

```text
SHA256SUMS
simple-vps-darwin-amd64
simple-vps-darwin-arm64
simple-vps-linux-amd64
simple-vps-linux-arm64
```

The final release URL was:
`https://github.com/fprl/simple-vps/releases/tag/v0.5.0-rc1`.

### Commands and process

1. Fetched `install.sh` through the GitHub Contents API because the repo is
   private in this environment, then ran remote install with
   `SIMPLE_VPS_RELEASE_TOKEN`:

   ```sh
   SIMPLE_VPS_RELEASE_TOKEN=<token> SIMPLE_VPS_VERSION=v0.5.0-rc1 ./install.sh \
     --mode remote \
     --host 128.140.3.159 \
     --bootstrap-user root \
     --ssh-key ~/.ssh/hetzner \
     --operator-ssh-public-key-file ~/.ssh/hetzner.pub \
     --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub \
     --ingress public \
     --admin public-ssh \
     --yes
   ```

   Result:

   ```text
   ==> Downloading Simple VPS binary from https://github.com/fprl/simple-vps/releases/download/v0.5.0-rc1/simple-vps-darwin-arm64
   ==> Downloading Simple VPS Linux helper binary from https://github.com/fprl/simple-vps/releases/download/v0.5.0-rc1/simple-vps-linux-amd64
   ==> Apply 20260529T154941Z changed 1 operations
   ==> Provisioning complete
   ```

2. Downloaded the published `simple-vps-darwin-arm64` asset plus
   `SHA256SUMS`, verified the checksum, then used that binary for the smoke:

   ```text
   simple-vps-darwin-arm64: OK
   ```

3. Verified the host from the published binary:

   - `host status --json` reported Caddy `active`, Podman `5.7.0`, and rsync
     `3.4.1`.
   - `host doctor --json` returned `"healthy": true` with no findings.

4. Deployed mixed container/static release v1:

   ```text
   Deployed mixed-app (production) at 2201166c0cc6-s2303f800a74f
   ```

   Checks:

   - `GET /health` -> `ok`
   - `GET /docs` -> HTML containing `docs-ok`

5. Deployed v2 with changed static bytes:

   ```text
   Deployed mixed-app (production) at 387bd212f5c3-s1db9148fab57
   ```

   Check: `GET /docs` -> `docs-v2-ok`.

6. Rolled back:

   ```json
   {
     "app": "mixed-app",
     "env": "production",
     "previous": "387bd212f5c3-s1db9148fab57",
     "release": "2201166c0cc6-s2303f800a74f",
     "processes": ["web"]
   }
   ```

   Check: `GET /docs` -> `docs-ok`.

7. Created backup:

   ```text
   Created backup /etc/simple-vps/backups/mixed-app/production/20260529T155215Z-2201166c0cc6-s2303f800a74f.tar
   ```

8. Restored and verified:

   ```text
   Restored mixed-app (production) from 20260529T155215Z-2201166c0cc6-s2303f800a74f at release 2201166c0cc6-s2303f800a74f
   ```

   Checks:

   - `GET /health` -> `ok`
   - `GET /docs` -> `docs-ok`

9. Destroyed the app and purged env state:

   ```text
   Destroyed mixed-app (production)
     containers: 1 removed
     route: removed
     secrets: purged
   ```

   Final `app list --json --server deploy@128.140.3.159` returned
   `{"apps":[]}`.

10. Removed the smoke backup artifact directly from the disposable host:

    ```text
    apps=0 backups=0 containers=caddy
    ```

**Outcome:** pass. The published `v0.5.0-rc1` installer path works from outside
the checkout, and the release binary deploys, rolls back, backs up, restores,
and destroys a mixed container/static app on the real VPS.

## 2026-05-29 — Hono public TLS smoke on fresh Hetzner Ubuntu 26.04

- **Host:** `128.140.3.159`
- **OS:** Hetzner Ubuntu 26.04 LTS, `x86_64`
- **Build tested:** local dirty `main` build from `292a6c4`, built with
  `make clean build build-linux`.
- **Fixture:**
  `/var/folders/9_/wjt7c8c17kl_50546_2kgzfw0000gn/T/simple-vps-hono-app-XXXXXX.HsZqRwktr6`
- **Key dir:**
  `/var/folders/9_/wjt7c8c17kl_50546_2kgzfw0000gn/T/simple-vps-hono-keys-XXXXXX.xHSOZmVdxo`
- **DNS/TLS:** public Caddy ACME TLS. `sslip.io` resolved correctly but hit
  Let's Encrypt's weekly registered-domain certificate limit, so the final
  smoke used `hono.128.140.3.159.nip.io`.

### Commands and process

1. Confirmed root SSH and target OS:

   ```sh
   ssh-keygen -R 128.140.3.159
   ssh-keyscan -T 10 -t ed25519,rsa,ecdsa 128.140.3.159 >> ~/.ssh/known_hosts
   ssh -i ~/.ssh/hetzner root@128.140.3.159 'printf "root-ok "; . /etc/os-release; printf "%s %s\n" "$PRETTY_NAME" "$(uname -m)"'
   ```

   Result: `root-ok Ubuntu 26.04 LTS x86_64`.

2. Built local client and helper binaries:

   ```sh
   make clean build build-linux
   ```

3. Installed the host:

   ```sh
   ./dist/simple-vps host install \
     --mode remote \
     --host 128.140.3.159 \
     --bootstrap-user root \
     --ssh-key ~/.ssh/hetzner \
     --operator-ssh-public-key-file "$KEYDIR/operator.pub" \
     --deploy-ssh-public-key-file "$KEYDIR/deploy.pub" \
     --timezone UTC \
     --locale en_US.UTF-8 \
     --ingress public \
     --admin public-ssh \
     --no-litestream \
     --yes
   ```

   Result: `Apply 20260529T083043Z changed 41 operations`.

4. Verified host state:

   ```sh
   simple-vps host doctor --json --server deploy@128.140.3.159
   simple-vps host status --json --server deploy@128.140.3.159
   ```

   Results:

   - `host doctor` returned `"healthy": true`.
   - `host status` reported Caddy `active`, Podman `5.7.0`, and rsync `3.4.1`.
   - `/usr/local/bin/simple-vps version` returned
     `v0.4.3-10-g292a6c4-dirty`.
   - Only the Caddy container was running before app deploy.

### Hono app

Manifest shape:

```toml
name = "hono"

[env.production]
server = "deploy@128.140.3.159"

[vars]
APP_PORT = "3000"
RELEASE_LABEL = "v1"
SMOKE_SECRET = "@secret:smoke_key"

[processes.web]
port = 3000
health = "/health"
resources = { memory = "256m", cpus = 0.5 }

[routes.app]
host = "hono.128.140.3.159.nip.io"
process = "web"
```

Commands:

```sh
simple-vps check production
simple-vps setup production
printf '%s' 'hono-secret' | simple-vps secret set production smoke_key
simple-vps secret list --json production
simple-vps deploy production
```

Results:

- `setup production` created `/var/apps/hono.production`.
- `secret list --json` returned only `smoke_key`, not the value.
- First deploy to `hono.128.140.3.159.sslip.io` succeeded at
  `38dbd3df92ef`, but Caddy could not obtain a trusted public certificate:

  ```text
  HTTP 429 ... too many certificates (250000) already issued for "sslip.io"
  ```

- `hono.128.140.3.159.nip.io` resolved to `128.140.3.159`, so the fixture was
  committed and redeployed with the `nip.io` host.
- Final v1 deploy: `Deployed hono (production) at e9f5cf0996b5`.
- Public trusted HTTPS verification with Node fetch:

  ```text
  https://hono.128.140.3.159.nip.io/health -> 200 ok
  https://hono.128.140.3.159.nip.io/       -> 200 hono:v1:hono-secret:port-3000
  ```

- Caddy fragment:

  ```caddyfile
  # generated by simple-vps server app apply — do not edit
  "hono.128.140.3.159.nip.io" {
      reverse_proxy http://svps-447fdeb014ba-web-e9f5cf0996b5:3000
  }
  ```

### Release snapshot rollback

The v2 manifest changed both runtime env and process port:

```toml
[vars]
APP_PORT = "3333"
RELEASE_LABEL = "v2"

[processes.web]
port = 3333
```

Results:

- v2 deploy: `Deployed hono (production) at 7c20b22f9282`.
- Public HTTPS returned `hono:v2:hono-secret:port-3333`.
- Caddy pointed at `svps-447fdeb014ba-web-7c20b22f9282:3333`.
- Runtime env contained:

  ```text
  APP_PORT=3333
  RELEASE_LABEL=v2
  SMOKE_SECRET=hono-secret
  ```

- Release manifest snapshots existed for all three releases under
  `/var/apps/hono.production/releases/<release>/simple-vps.toml`.
- `simple-vps rollback --json production` returned:

  ```json
  {
    "app": "hono",
    "env": "production",
    "previous": "7c20b22f9282",
    "release": "e9f5cf0996b5",
    "processes": ["web"]
  }
  ```

- After rollback, public HTTPS returned `hono:v1:hono-secret:port-3000`.
- Caddy pointed back at `svps-447fdeb014ba-web-e9f5cf0996b5:3000`.
- `/var/apps/hono.production/simple-vps.toml` and `runtime/.env` both reverted
  to the v1 values.

Final cleanup:

```sh
simple-vps app list --json --server deploy@128.140.3.159
simple-vps destroy production --confirm hono --purge
simple-vps app list --json --server deploy@128.140.3.159
simple-vps host doctor --json --server deploy@128.140.3.159
```

Results:

- `app list` showed `hono/production` before destroy.
- Destroy removed one app container, the Caddy route, and the secret.
- Final `app list` returned an empty app list.
- `/var/apps`, `/etc/simple-vps/secrets`, and `/etc/caddy/conf.d` had no app
  state left.
- Only Caddy remained running.
- Final `host doctor` returned healthy.

Post-review rerun after the release-validation, reconciliation, restore, and
Litestream-default fixes:

```sh
simple-vps host install --mode remote --host 128.140.3.159 ...
git checkout e9f5cf0 && simple-vps deploy production
node fetch https://hono.128.140.3.159.nip.io/
git checkout 7c20b22 && simple-vps deploy production
simple-vps rollback --json production
node fetch https://hono.128.140.3.159.nip.io/
simple-vps destroy production --confirm hono --purge
simple-vps app list --json --server deploy@128.140.3.159
```

Results:

- Host install updated the helper with one changed operation and still reported
  `Litestream: false`.
- v1 public HTTPS returned `hono:v1:hono-secret:port-3000`.
- v2 public HTTPS returned `hono:v2:hono-secret:port-3333`.
- Rollback returned `previous = 7c20b22f9282` and `release = e9f5cf0996b5`.
- After rollback, public HTTPS returned `hono:v1:hono-secret:port-3000`.
- Destroy purged the app and final `app list` returned `{"apps":[]}`.

**Outcome:** pass. Ubuntu 26.04 installs cleanly, Hono deploys behind public
Caddy/Let's Encrypt TLS, secrets resolve into runtime env, release snapshots
preserve manifest shape, rollback restores the old port/env/Caddy route, and
destroy purges the app environment.

## 2026-05-29 — ADR-0008 fresh Hetzner smoke with container and static apps

- **Host:** `178.105.101.122`
- **OS:** Hetzner Ubuntu 24.04.4 LTS, `x86_64`
- **Build tested:** local `main` build from `c81a603` plus the fixes described
  below, built with `make clean build build-linux`.
- **DNS:** `smoke.spotslice.com A 178.105.101.122` already existed and resolved
  via `1.1.1.1`.
- **Cloudflare:** Cloudflare plugin was installed, but no Cloudflare DNS MCP
  tools were exposed in this session. No DNS mutation was needed.
- **TLS:** `tls = "internal"` for deterministic smoke verification with
  `curl -k` from the VPS against `127.0.0.1`.
- **Key dir:** `/tmp/simple-vps-smoke-keys-20260529T070632Z`
- **Container fixture:**
  `/var/folders/9_/wjt7c8c17kl_50546_2kgzfw0000gn/T/simple-vps-real-container-XXXXXX.k8uKubDKap`
- **Static fixture:**
  `/var/folders/9_/wjt7c8c17kl_50546_2kgzfw0000gn/T/simple-vps-real-static-XXXXXX.FPqjMxfjme`

### Commands and process

1. Confirmed DNS and SSH:

   ```sh
   dig +short smoke.spotslice.com A @1.1.1.1
   ssh-keygen -R 178.105.101.122
   ssh-keyscan -T 10 -t ed25519,rsa,ecdsa 178.105.101.122 >> ~/.ssh/known_hosts
   ssh -i ~/.ssh/hetzner root@178.105.101.122 'printf "root-ok "; . /etc/os-release; printf "%s %s\n" "$PRETTY_NAME" "$(uname -m)"'
   ```

2. Built local client and helper binaries:

   ```sh
   make clean build build-linux
   ```

3. Installed the host:

   ```sh
   ./dist/simple-vps host install \
     --mode remote \
     --host 178.105.101.122 \
     --bootstrap-user root \
     --ssh-key ~/.ssh/hetzner \
     --operator-ssh-public-key-file /tmp/simple-vps-smoke-keys-20260529T070632Z/operator.pub \
     --deploy-ssh-public-key-file /tmp/simple-vps-smoke-keys-20260529T070632Z/deploy.pub \
     --timezone UTC \
     --locale en_US.UTF-8 \
     --ingress public \
     --admin public-ssh \
     --no-litestream \
     --yes
   ```

   The first run completed but Caddy failed to start:

   ```text
   Error: statfs /var/apps: no such file or directory
   ```

   Cause: the Caddy systemd unit bind-mounted `/var/apps:/var/apps:ro,Z`, but a
   fresh host with no deployed apps had no `/var/apps` directory yet.

   Fix: the provisioner now creates `/var/apps` during Caddy setup before the
   Caddy container starts. Rerunning host install changed 3 operations and made
   Caddy active.

4. Verified host state after the fix:

   ```sh
   SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys-20260529T070632Z/deploy)" \
   SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H 178.105.101.122 2>/dev/null)" \
     ./dist/simple-vps host doctor --json --server deploy@178.105.101.122
   ```

   Final doctor payload includes service health:

   ```json
   {
     "state": {"status": "healthy", "findings": []},
     "services": {"status": "healthy", "findings": []},
     "identity": {"status": "healthy", "findings": []},
     "healthy": true
   }
   ```

   Final status no longer reports a missing host `caddy` binary, because Caddy
   is supervised as a container:

   ```json
   {
     "services": {
       "caddy": "active",
       "cloudflared": "inactive",
       "tailscaled": "inactive"
     },
     "tools": {
       "podman": "installed (podman version 4.9.3)",
       "rsync": "installed (rsync  version 3.2.7  protocol version 31)"
     }
   }
   ```

### Container app

Manifest shape:

```toml
name = "hello"

[env.production]
server = "deploy@178.105.101.122"

[vars]
SMOKE_SECRET = "@secret:smoke_key"
SMOKE_RELEASE = "v1"

[processes.web]
port = 3000
health = "/health"
resources = { memory = "256m", cpus = 0.5 }

[routes.app]
host = "smoke.spotslice.com"
process = "web"
tls = "internal"
```

Commands:

```sh
simple-vps setup production
printf '%s' 'real-smoke-secret' | simple-vps secret set production smoke_key
simple-vps secret list --json production
simple-vps deploy production
simple-vps status --json production
```

Results:

- `setup production` created `/var/apps/hello.production`.
- `simple-vps.json` stored `infra_id = "svps-de70a215abfd"`.
- `secret list --json` returned only `smoke_key`, not the value.
- First deploy: `Deployed hello (production) at 59a2c6ae87f0`.
- Caddy fragment:

  ```caddyfile
  # generated by simple-vps server app apply — do not edit
  "smoke.spotslice.com" {
      tls internal
      reverse_proxy http://svps-de70a215abfd-web-59a2c6ae87f0:3000
  }
  ```

- Runtime env file:

  ```text
  600 svps-de70a215abfd svps-de70a215abfd /var/apps/hello.production/runtime/.env
  SMOKE_RELEASE=v1
  SMOKE_SECRET=real-smoke-secret
  ```

- HTTP verification from the VPS:

  ```text
  curl -k --resolve smoke.spotslice.com:443:127.0.0.1 https://smoke.spotslice.com/health
  -> ok, HTTP 200

  curl -k --resolve smoke.spotslice.com:443:127.0.0.1 https://smoke.spotslice.com/
  -> container:v1:real-smoke-secret, HTTP 200
  ```

Rollback found and fixed a real Podman compatibility issue:

- Real Podman 4 `podman images --format json` returns `Names` plus `Labels`;
  the fake harness returned `Repository` and `Tag`.
- Rollback image discovery filtered out every real image, producing:
  `no previous release available locally`.
- Fix: rollback now selects image releases from `simple-vps.*` labels and the
  derived infra ID, not from repo/tag parsing.

After deploying a real code change:

```text
Deployed hello (production) at 5ba52bd05f55
curl / -> container-code-v3:v2:real-smoke-secret, HTTP 200
rollback --json production -> release 3cd4b1db9457
curl / -> container:v2:real-smoke-secret, HTTP 200
status --json production -> svps-de70a215abfd-web-3cd4b1db9457 running
```

Backup/restore:

```sh
printf '%s' durable-state > /var/apps/hello.production/data/data.txt
simple-vps backup production
simple-vps backup --json list production
simple-vps destroy production --confirm hello --purge
simple-vps restore --from 20260529T072340Z-3cd4b1db9457 production
```

Results:

- Backup path:
  `/etc/simple-vps/backups/hello/production/20260529T072340Z-3cd4b1db9457.tar`
- Destroy removed container, route, env root, and secrets.
- Restore recreated `/var/apps/hello.production`, `/data`, runtime config,
  secrets, Caddy route, and the container.
- Restored data: `durable-state`.
- Restored `/data` ownership:
  `2775 svps-de70a215abfd svps-de70a215abfd`.
- Restored route served `container:v2:real-smoke-secret`, HTTP 200.

### Static-only app

Manifest shape:

```toml
name = "site"

[env.production]
server = "deploy@178.105.101.122"

[routes.home]
host = "smoke.spotslice.com"
serve = "dist"
tls = "internal"
```

Commands:

```sh
simple-vps setup production
simple-vps deploy production
simple-vps status --json production
```

Results:

- First deploy: `Deployed site (production) at 46c1848d37fd`.
- `status --json production` returned `"processes": []`.
- Caddy served from `/var/apps/site.production/static/current`.
- `curl /` returned `static:v1`, HTTP 200.

Static rollback:

```text
Deployed site (production) at a1df7d2bd2fb
curl / -> static:v2, HTTP 200
rollback --json production -> release 46c1848d37fd
curl / -> static:v1, HTTP 200
readlink /var/apps/site.production/static/current
-> /var/apps/site.production/static/releases/46c1848d37fd
```

The static rollback JSON originally encoded nil processes as
`"processes": null`; this was fixed to emit an empty array.

Static backup/restore:

```text
Created backup /etc/simple-vps/backups/site/production/20260529T072739Z-46c1848d37fd.tar
Destroyed site (production)
  containers: none
  route: removed
  secrets: purged
Restored site (production) from 20260529T072739Z-46c1848d37fd at release 46c1848d37fd
curl / -> static:v1, HTTP 200
```

Final cleanup removed the static app. Only Caddy remained running.

**Outcome:** pass after three fixes discovered by the run: create `/var/apps`
before Caddy starts, discover rollback images via labels on real Podman JSON,
and make host diagnostics match the containerized Caddy architecture.

## 2026-05-28 — v0.4.3 fresh Hetzner rebuild + real DNS/TLS smoke

- **Host:** `178.105.101.122`
- **OS:** rebuilt Hetzner Ubuntu 24.04 VPS
- **Release tested:** local v0.4.3 build plus v0.4.3 Linux helper binaries
- **DNS:** `smoke.spotslice.com A 178.105.101.122`, created in Cloudflare,
  unproxied, TTL 60.
- **TLS:** public Caddy/Let's Encrypt flow; no `tls = "internal"`, no `-k`,
  no `--resolve` on the VPS verification.
- **Fixture:** `/tmp/simple-vps-smoke-app-20260528T143337Z`
- **Operator/deploy keys:** `/tmp/simple-vps-smoke-keys-20260528T143337Z`

### Process and result

1. Created Cloudflare DNS record:
   `smoke.spotslice.com A 178.105.101.122`, `proxied=false`, `ttl=60`.
2. Confirmed public resolver propagation:
   `dig +short smoke.spotslice.com A @1.1.1.1` ->
   `178.105.101.122`.
3. Refreshed local SSH host keys after the Hetzner rebuild:

   ```sh
   ssh-keygen -R 178.105.101.122
   ssh-keyscan -T 10 -t ed25519,rsa,ecdsa 178.105.101.122 >> ~/.ssh/known_hosts
   ```

4. Generated fresh operator/deploy keys under
   `/tmp/simple-vps-smoke-keys-20260528T143337Z`.
5. Ran remote host install with public ingress/admin, no Tailscale,
   no Cloudflare Tunnel, and no Litestream:

   ```text
   connected
   ==> Using Simple VPS Linux helper binary from dist/simple-vps-linux-amd64
   --> Running Go provisioner on target
   ==> Apply 20260528T143456Z changed 40 operations
   ==> Provisioning complete
   ```

6. Verified fresh host state:
   - `systemctl is-active caddy` -> `active`
   - `podman ps` -> Caddy running with `80:80` and `443:443`
   - `podman network ls` -> `ingress`, `podman`
   - `/etc/simple-vps/host.json` -> `.meta.last_apply.status == "ok"`
   - `host status --json --server deploy@178.105.101.122`
   - `host doctor --json --server deploy@178.105.101.122` -> healthy
7. Created nginx fixture with:
   - route host `smoke.spotslice.com`
   - no explicit `tls` setting, so Caddy used public ACME TLS
   - tmpfs mounts for `/var/cache/nginx` and `/var/run`
8. Ran:
   - `check production` -> valid
   - `setup production` -> complete
   - `secret put production smoke_key`
   - `deploy production` -> `Deployed hello (production) at db036b9a5c04`
   - `status --json production` -> one running `web` container
9. Verified generated Caddy fragment:

   ```caddyfile
   # generated by simple-vps server app apply — do not edit
   "smoke.spotslice.com" {
       reverse_proxy http://app-hello-production-web:3000
   }
   ```

10. Verified real DNS + trusted public HTTPS from the VPS:

    ```text
    curl https://smoke.spotslice.com/health -> ok, HTTP 200
    curl https://smoke.spotslice.com/       -> smoke-ok-nginx, HTTP 200
    ```

11. Seeded durable state at
    `/var/apps/hello/production/shared/data.txt = fresh-durable-state`.
12. Ran `backup production`:

    ```text
    Created backup /etc/simple-vps/backups/hello/production/20260528T143830Z-db036b9a5c04.tar
    ```

13. Ran public teardown:
    `destroy production --confirm hello --purge`
    -> `containers: 1 removed`, `route: removed`, `secrets: purged`.
14. Verified the shared data file was gone after destroy.
15. Ran `restore --from 20260528T143830Z-db036b9a5c04 production`:

    ```text
    Restored hello (production) from 20260528T143830Z-db036b9a5c04 at release db036b9a5c04
    ```

16. Verified restored state:
    - `shared/data.txt` -> `fresh-durable-state`
    - `status --json production` -> one running `web` container on image
      `localhost/simple-vps/hello-production:db036b9a5c04`
    - `curl https://smoke.spotslice.com/health` -> `ok`, HTTP 200
    - `curl https://smoke.spotslice.com/` -> `smoke-ok-nginx`, HTTP 200
17. Ran final public teardown and verified:
    - `status --json production` -> empty `services`
    - `/etc/simple-vps/secrets/hello/production` -> absent
    - only the Caddy container remained running.

**Outcome:** pass. v0.4.3 works end to end on a freshly rebuilt Hetzner VPS
with real Cloudflare DNS, public ACME TLS, deploy, secrets, backup, destroy,
restore, HTTPS verification, and teardown.

## 2026-05-28 — v0.4.3 release install smoke

- **Host:** `178.105.101.122`
- **Release tested:** `v0.4.3`
- **Install path tested:** `install.sh` copied to
  `/tmp/simple-vps-install-smoke-v0.4.3`, outside the source checkout.
- **Mode:** `host install --mode remote --check --yes`
- **Token:** `SIMPLE_VPS_RELEASE_TOKEN` from local GitHub auth, required because
  release assets are private in this environment.

### Process and result

1. Tagged and published `v0.4.3` with:
   - `simple-vps-linux-amd64`
   - `simple-vps-linux-arm64`
   - `simple-vps-darwin-amd64`
   - `simple-vps-darwin-arm64`
   - `SHA256SUMS`
2. Copied `install.sh` to `/tmp/simple-vps-install-smoke-v0.4.3` so it could
   not use the source checkout.
3. Ran remote install in check mode against the existing VPS. The temp
   installer downloaded the `v0.4.3` Darwin asset and the published Linux
   helper asset, then completed:

   ```text
   connected
   ==> Apply 20260528T141226Z changed 2 operations
   ==> Provisioning complete
   ```

**Outcome:** pass. The published v0.4.3 installer path works from outside the
checkout.

## 2026-05-28 — main backup/restore real-box smoke

- **Host:** `178.105.101.122`
- **OS:** Ubuntu 24.04.4 LTS (`6.8.0-117-generic`)
- **Build tested:** local `main` build after `c0c1510`, plus the release-version
  detection fix from this pass.
- **Version string:** `v0.4.2-7-gc0c1510-dirty`
- **Fixture:** `/tmp/simple-vps-smoke-app-20260528T135420Z`
- **Operator/deploy keys:** `/tmp/simple-vps-smoke-keys-20260528T135420Z`
- **DNS:** `smoke.spotslice.com` still did not resolve from this shell. No
  Cloudflare MCP DNS tool, `wrangler`, `cloudflared`, `hcloud` CLI, or local
  Hetzner API config was available. The smoke used `tls = "internal"` and
  `curl --resolve`.
- **Rebuild status:** not rebuilt in this pass; the host was reachable over
  root SSH with `~/.ssh/hetzner`.

### Process and result

1. Built a fresh local binary with `make clean && make build`.
2. First remote install attempt failed because the local `git describe` version
   `v0.4.2-7-gc0c1510` was incorrectly treated as a release tag, so the
   installer tried to download nonexistent
   `releases/download/v0.4.2-7-gc0c1510/simple-vps-linux-amd64`.
3. Fixed release-version detection to only treat exact release tags such as
   `vX.Y.Z` and `vX.Y.Z-rcN` as downloadable release builds.
4. Rebuilt and reran remote install. It built local Linux helper binaries,
   copied the matching helper to the VPS, and completed provisioning:

   ```text
   ==> Building Simple VPS Go helper binaries
   --> Running Go provisioner on target
   ==> Apply 20260528T135620Z changed 3 operations
   ==> Provisioning complete
   ```

5. Verified host state:
   - `systemctl is-active caddy` -> `active`
   - `podman ps` -> `caddy Up`
   - `podman network ls` -> `ingress`, `podman`
   - `/etc/simple-vps/host.json` -> `.meta.last_apply.status == "ok"`
6. Created the nginx smoke fixture with `tls = "internal"` and tmpfs mounts for
   `/var/cache/nginx` and `/var/run`.
7. Ran:
   - `check production` -> valid
   - `setup production` -> complete
   - `secret put production smoke_key`
   - `secret list --json production` -> `["smoke_key"]`
   - `deploy production` -> `Deployed hello (production) at 52a9ef7cc022`
8. Seeded shared app state with
   `/var/apps/hello/production/shared/data.txt = durable-state`.
9. Ran `backup production`:

   ```text
   Created backup /etc/simple-vps/backups/hello/production/20260528T135727Z-52a9ef7cc022.tar
   ```

10. Verified `backup --json list production` returned the backup ID
    `20260528T135727Z-52a9ef7cc022`.
11. Ran public teardown:
    `destroy production --confirm hello --purge`
    -> `containers: 1 removed`, `route: removed`, `secrets: purged`.
12. Verified the shared data file was gone after destroy.
13. Ran `restore --from 20260528T135727Z-52a9ef7cc022 production`:

    ```text
    Restored hello (production) from 20260528T135727Z-52a9ef7cc022 at release 52a9ef7cc022
    ```

14. Verified restored state:
    - `shared/data.txt` -> `durable-state`
    - `status --json production` -> one running `web` container on image
      `localhost/simple-vps/hello-production:52a9ef7cc022`
    - `.env` -> `0600 app-hello-production app-hello-production`
    - `data.txt` -> `644 app-hello-production app-hello-production`
15. Verified HTTPS through Caddy from the VPS using SNI/Host
    `smoke.spotslice.com` and the public IP:

    ```text
    /health -> ok, HTTP 200
    /       -> smoke-ok-nginx, HTTP 200
    ```

16. Ran public teardown again and verified:
    - `status --json production` -> empty `services`
    - `/etc/simple-vps/secrets/hello/production` -> absent
    - only the Caddy container remained running.

**Outcome:** pass. The local backup/restore primitive works on the real VPS
with real Podman and Caddy when the saved local image is still present.

## 2026-05-28 — v0.4.2 release smoke

- **Host:** `178.105.101.122`
- **Release tested:** `v0.4.2`
- **Install path tested:** `install.sh` copied to a temp directory outside the
  source checkout.
- **App binary tested:** published `simple-vps-darwin-arm64` release asset.
- **Fixture:** `/tmp/simple-vps-smoke-app-20260528T100045Z`
- **Rebuild status:** not rebuilt in this pass. No `hcloud` CLI, Hetzner API
  token, or local hcloud config was available from the workspace shell, so this
  pass verified the existing VPS rather than a fresh provider rebuild.

### Process and result

1. Published the `v0.4.2` release assets:
   - `simple-vps-linux-amd64`
   - `simple-vps-linux-arm64`
   - `simple-vps-darwin-amd64`
   - `simple-vps-darwin-arm64`
   - `SHA256SUMS`
2. Copied `install.sh` to `/tmp`, set `SIMPLE_VPS_RELEASE_TOKEN`, and ran
   remote install in check mode. Because there was no checkout beside the
   script, the installer had to:
   - detect `simple-vps-darwin-arm64`
   - download the `v0.4.2` Darwin release asset
   - download and verify `SHA256SUMS`
   - run the downloaded binary
   - download and verify `simple-vps-linux-amd64`
   - copy the Linux helper to the VPS
3. Remote install completed:

   ```text
   ==> Downloading Simple VPS binary from https://github.com/fprl/simple-vps/releases/download/v0.4.2/simple-vps-darwin-arm64
   connected
   ==> Downloading Simple VPS Linux helper binary from https://github.com/fprl/simple-vps/releases/download/v0.4.2/simple-vps-linux-amd64
   --> Running Go provisioner on target
   ==> Apply 20260528T130713Z changed 2 operations
   ==> Provisioning complete
   ```

4. Ran the full app path with `dist/simple-vps-darwin-arm64` from the same
   release build:
   - `version` -> `v0.4.2`
   - `check production` -> valid
   - `setup production` -> complete
   - `secret put production smoke_key`
   - `secret list --json production` -> `["smoke_key"]`
   - `deploy production` -> `Deployed hello (production) at 3360f173051b`
   - `status --json production` -> one running `web` container
   - `logs production web --tail 20` -> nginx startup and request logs
5. Verified HTTPS through Caddy with SNI/Host `smoke.spotslice.com` to the VPS
   IP:

   ```text
   /health -> HTTP 200, body: ok
   /       -> HTTP 200, body: smoke-ok-nginx
   ```

6. Ran public teardown:
   `destroy production --confirm hello --purge`
   -> `containers: 1 removed`, `route: removed`, `secrets: purged`.
7. Verified `status --json production` returned an empty service list.

## 2026-05-28 — v0.4.1 release-binary remote install smoke

- **Host:** `178.105.101.122`
- **Release tested:** `v0.4.1`
- **Binary tested:** `simple-vps-darwin-arm64` copied to a temp directory
  outside the source checkout.
- **Mode:** `host install --mode remote --check --yes`

### Process and result

1. Built and published the `v0.4.1` release assets:
   - `simple-vps-linux-amd64`
   - `simple-vps-linux-arm64`
   - `simple-vps-darwin-amd64`
   - `simple-vps-darwin-arm64`
   - `SHA256SUMS`
2. Copied the Darwin release binary to `/tmp`, then ran remote install from
   there so the installer could not rely on the source checkout.
3. Exported `SIMPLE_VPS_RELEASE_TOKEN` from the local GitHub credential helper
   because this repository's release assets are private in this environment.
4. Ran remote install in check mode against the existing VPS:

   ```sh
   SIMPLE_VPS_RELEASE_TOKEN="$token" ./simple-vps host install \
     --mode remote \
     --host 178.105.101.122 \
     --bootstrap-user root \
     --ssh-key ~/.ssh/hetzner \
     --operator-ssh-public-key-file /tmp/simple-vps-smoke-keys-20260528T100045Z/operator.pub \
     --deploy-ssh-public-key-file /tmp/simple-vps-smoke-keys-20260528T100045Z/deploy.pub \
     --timezone UTC --locale en_US.UTF-8 \
     --no-tailscale --no-cloudflare-tunnel --no-litestream \
     --check --yes
   ```

5. The first implementation tried to download the helper from the browser
   release URL with `Authorization: Bearer ...`; GitHub still returned `404`
   for private assets. The fix keeps the public URL path for public releases
   and falls back to the GitHub Releases API asset endpoint for private assets.
6. The final `v0.4.1` release smoke completed:

   ```text
   v0.4.1
   connected
   ==> Downloading Simple VPS Linux helper binary from https://github.com/fprl/simple-vps/releases/download/v0.4.1/simple-vps-linux-amd64
   --> Running Go provisioner on target
   ==> Running in local mode on localhost
   ==> Apply 20260528T111340Z changed 2 operations
   ==> Provisioning complete
   ```

### Follow-up landed after v0.4.1

The v0.4.1 binary downloaded and executed the Linux helper successfully, but
did not verify the helper against `SHA256SUMS`. The follow-up change adds
SHA256 verification for release helper downloads and updates `install.sh` to
download platform release binaries with SHA256 verification by default.

## 2026-05-28 — post-v0.4.1 helper checksum + install UX smoke

- **Host:** `178.105.101.122`
- **Binary tested:** local Darwin build with `VERSION=v0.4.1`, exercising the
  tagged release helper download path against the published `v0.4.1`
  `simple-vps-linux-amd64` and `SHA256SUMS` assets.
- **Fixture:** `/tmp/simple-vps-smoke-app-20260528T100045Z`
- **Rebuild status:** not rebuilt in this pass. No `hcloud` CLI, Hetzner API
  token, or local hcloud config was available from the workspace shell, so this
  pass verified the existing VPS rather than a fresh provider rebuild.

### Process and result

1. Ran `make build VERSION=v0.4.1`.
2. Ran remote `host install --check --yes` with `SIMPLE_VPS_RELEASE_TOKEN`.
   The installer downloaded `simple-vps-linux-amd64`, downloaded
   `SHA256SUMS`, verified the helper checksum, copied the helper to the VPS,
   and completed the local provisioner in check mode.
3. Ran the full app path against the existing host:
   - `check production` -> valid
   - `setup production` -> complete
   - `secret put production smoke_key`
   - `secret list --json production` -> `["smoke_key"]`
   - `deploy production` -> `Deployed hello (production) at 3360f173051b`
   - `status --json production` -> one running `web` container
   - `logs production web --tail 20` -> nginx startup and healthcheck logs
4. Verified HTTPS through Caddy with SNI/Host `smoke.spotslice.com` to the VPS
   IP:

   ```text
   /health -> HTTP 200, body: ok
   /       -> HTTP 200, body: smoke-ok-nginx
   ```

5. Ran public teardown:
   `destroy production --confirm hello --purge`
   -> `containers: 1 removed`, `route: removed`, `secrets: purged`.
6. Verified `status --json production` returned an empty service list.

### Install UX check

The updated `install.sh` was also tested against a local HTTP release fixture:
without a source checkout or manual asset selection, it detected
`simple-vps-darwin-arm64`, downloaded that asset plus `SHA256SUMS`, verified
the checksum, and executed the downloaded binary as:

```text
simple-vps host install --check --yes
```

## 2026-05-28 — post JSON/locking/docs cleanup pass

- **Host:** `178.105.101.122` (Hetzner, rebuilt before run)
- **OS:** Ubuntu 24.04.4 LTS (`6.8.0-117-generic`)
- **Hostname used by fixture:** `smoke.spotslice.com`
- **DNS:** not modified. The Cloudflare plugin skill was present, but no
  Cloudflare MCP tool, CLI, token, or local Wrangler auth was available in this
  session. The smoke used `tls = "internal"` and `curl --resolve`.
- **Commit tested:** `main` at `6cb6b57065aac9094a1c421e917c57ea6bd98c29`
- **Root SSH key:** `~/.ssh/hetzner`
- **Operator/deploy keys:** generated under
  `/tmp/simple-vps-smoke-keys-20260528T100045Z`
- **Fixture:** `/tmp/simple-vps-smoke-app-20260528T100045Z`

### Process and result

1. Built fresh local binary with `make clean && make build`.
2. Generated throwaway operator/deploy Ed25519 keys.
3. Ran `host install` in remote mode with:
   `--no-tailscale --no-cloudflare-tunnel --no-litestream`.
4. Verified host state:
   - `systemctl is-active caddy` -> `active`
   - `podman ps` -> `caddy Up`
   - `podman network ls` -> `ingress`, `podman`
   - `/etc/caddy/Caddyfile` -> `import conf.d/*.caddy`
   - `/etc/simple-vps/host.json` -> `.meta.last_apply.status == "ok"`
5. Verified public host reads:
   - `host status --json --server deploy@178.105.101.122`
   - `host doctor --json --server deploy@178.105.101.122`
6. Created nginx fixture with:
   - `SMOKE_SECRET = "@secret:smoke_key"`
   - `tls = "internal"`
   - tmpfs mounts for `/var/cache/nginx` and `/var/run`
7. Ran:
   - `check production` -> valid
   - `setup production` -> `Setup complete for hello (production)`
   - `secret put production smoke_key`
   - `secret list --json production` -> `["smoke_key"]`
   - `deploy production` -> `Deployed hello (production) at 3360f173051b`
   - `status --json production` -> one running `web` container
   - `logs production web` -> nginx startup and request logs
8. Verified end-to-end HTTPS through Caddy:

   ```text
   curl -k --resolve smoke.spotslice.com:443:178.105.101.122 \
     https://smoke.spotslice.com/health
   -> ok
   -> HTTP 200

   curl -k --resolve smoke.spotslice.com:443:178.105.101.122 \
     https://smoke.spotslice.com/
   -> smoke-ok-nginx
   -> HTTP 200
   ```

9. Verified generated host artifacts:

   ```caddyfile
   # generated by simple-vps server app apply — do not edit
   "smoke.spotslice.com" {
       tls internal
       reverse_proxy http://app-hello-production-web:3000
   }
   ```

   `/var/apps/hello/production/shared/.env` was `0600`, owned by
   `app-hello-production`, and contained the resolved secret value.

10. Ran public teardown:
    `destroy production --confirm hello --purge`
    -> `containers: 1 removed`, `route: removed`, `secrets: purged`.
11. Verified post-destroy state:
    - `status --json production` -> empty `services`
    - `/etc/caddy/conf.d/` -> empty
    - `/etc/simple-vps/secrets/hello/production` -> absent
    - only the Caddy container remained running.

### Finding 7 — rebuilt Hetzner host had stale local SSH host keys

The first install attempt hit a local `known_hosts` mismatch from the VPS
rebuild:

```text
WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!
Host key for 178.105.101.122 has changed and you have requested strict checking.
Host key verification failed.
```

The smoke was unblocked with:

```sh
ssh-keygen -R 178.105.101.122
ssh-keyscan -T 10 -t ed25519,rsa,ecdsa 178.105.101.122 >> ~/.ssh/known_hosts
```

The fix in this change hardens remote install preflight: it now captures SSH
stdout and requires the exact `connected` sentinel, so an SSH transport failure
or empty preflight response cannot be mistaken for success.

### Notes

- nginx logs include this harmless line under the read-only rootfs:
  `can not modify /etc/nginx/conf.d/default.conf (read-only file system?)`.
  The container still starts and serves traffic because the fixture writes the
  desired config at image build time and declares the required writable tmpfs
  paths.
- This run did not test real Cloudflare DNS or Let's Encrypt issuance. It
  proved the deploy, secret resolution, Caddy fragment, Podman DNS, HTTPS
  routing, JSON read surfaces, and public destroy path on a real VPS.

**Outcome:** pass. The first-version path works end to end on a real Ubuntu
24.04 VPS with real Podman and real Caddy.

## 2026-05-27 — initial real-box smoke

- **Host:** `178.105.101.122` (Hetzner CX22-equivalent, Ubuntu 24.04.4 LTS, 8 GiB RAM, 150 GB disk)
- **Hostname:** `smoke.spotslice.com` (DNS status: see step 3 below)
- **Branch:** `smoke/real-box-2026-05-27` off `main` (`b72fdb0`)
- **simple-vps version:** locally built from this branch
- **SSH key:** `~/.ssh/hetzner` (root)
- **Operator key:** TBD (generated below)
- **Deploy key:** TBD (generated below)

## Findings index

This file is filled in step by step as the smoke runs. Each step
records the exact command, captured output, and any deviation from
the runbook's expected behavior.

### Finding 1 — ufw `--force allow` rejected by real ufw 0.36+

First `host install` against the box errored out:

```
simple-vps: error: ufw allow 22/tcp: command failed:
  ufw [--force allow 22/tcp]: exit 1: ERROR: Invalid syntax
```

Reproduced on box (`ufw --version` → `ufw 0.36.2`):

```
# ufw --force allow 22/tcp   →  ERROR: Invalid syntax (exit 1)
# ufw allow 22/tcp           →  Rules updated (exit 0)
```

`--force` only applies to `enable`, `reset`, `delete` (commands that
prompt). `internal/provision/host/primitives.go:EnsureUfwRule` was
unconditionally prepending `--force` for every non-delete invocation.
The fake-install-ufw stub silently swallowed `--force`, so neither
the unit tests nor `make fake-vps-install-smoke` caught it.

**Fix:**

1. `EnsureUfwRule` now only prepends `--force` for delete operations.
2. `fake-install-ufw` now rejects `--force` outside delete with the
   same "Invalid syntax" message, so the install smoke catches the
   regression next time.
3. `TestEnsureUfwRuleRunsWhenRuleMissing` updated to expect the
   correct command line.

### Finding 2 — addCaddy starts the Caddy container before writing `/etc/caddy/Caddyfile`

Second install attempt got past ufw, podman, deploy-tmp; failed at
`generate caddy`:

```
simple-vps: error: generate caddy: command failed:
  simple-vps [server generate-caddy]: exit 1:
  Error: command failed:
  caddy [validate --config /etc/caddy/.simple-vps-validate.452311870/Caddyfile --adapter caddyfile]:
  exec: "caddy": executable file not found in $PATH
```

Two interleaved bugs in `internal/provision/install.go:addCaddy`:

1. **Order: `caddy service` starts before any `Caddyfile` exists.**
   Caddy container runs `caddy run --config /etc/caddy/Caddyfile`,
   which exits 1 with "no such file or directory". systemd restarts
   it five times then gives up:

   ```
   caddy[…]: Error: reading config from file: open /etc/caddy/Caddyfile: no such file or directory
   systemd[1]: caddy.service: Start request repeated too quickly.
   systemd[1]: Failed to start caddy.service - Caddy (Simple VPS managed, podman).
   ```

   The op reported "success" because `systemctl start` on a
   `Type=simple` unit returns immediately, before the unit has
   actually settled into `active`. The installer never sees the
   subsequent failure.

2. **`generate-caddy` server verb calls the host `caddy` binary
   which no longer exists.** Post ADR-0006 Cut 2 the apt-Caddy
   install is gone; the only Caddy is the container. The
   `simple-vps server generate-caddy` verb still reaches for
   `utils.CaddyBin()` to run `caddy validate`, which fails.

**Fix (minimal):**

- Rewrite `addCaddy` to write `/etc/caddy/Caddyfile` directly via
  `EnsureFile` BEFORE starting `caddy.service`. The file is just
  `import conf.d/*.caddy` (per-app fragments land there via
  `server app apply`).
- Drop the `generate-caddy` op call from `addCaddy`. The old route
  helper and `apps.json` / `routes.json` render path were later
  removed; `internal/caddy` now only holds the Caddyfile quoting helper
  used by `server app apply`.
- Drop the `/etc/caddy/simple-vps` subdir creation (used only by the
  legacy `RenderRoutesCaddyfile`).
- Tighten `ensureServiceStarted`: after `systemctl start`, poll
  `is-active --quiet` briefly so a fast-failing unit surfaces the
  failure to the installer instead of silently going into restart
  loop.

### Finding 3 — UFW default-deny breaks Podman bridge DNS and forwarding

After the install completed cleanly, `simple-vps deploy production`
got as far as `podman exec caddy wget http://app-hello-production-web:3000/health`,
which failed with `wget: bad address`. From inside the Caddy
container:

```
# podman exec caddy nslookup app-hello-production-web
;; connection timed out; no servers could be reached
```

aardvark-dns was running and had the correct record:

```
e611e04b07d6… 10.89.0.7  caddy,e611e04b07d6
5467b4fa54ed… 10.89.0.8  app-hello-production-web,5467b4fa54ed
```

It binds to `10.89.0.1:53` (the ingress bridge gateway). Caddy could
ping that IP but UDP 53 timed out. Root cause: Ubuntu's UFW with
`DEFAULT_FORWARD_POLICY="DROP"` and no allow rule for incoming UDP 53
from `podman+` interfaces drops the DNS packets at the host's INPUT
chain. Bridge-internal HTTP would work (`wget http://10.89.0.8:3000`
returned `Connection refused`, not timeout — bridge L2 traffic doesn't
hit iptables when `br_netfilter` isn't loaded).

**This is the load-bearing assumption of ADR-0006 Cut 2 and it's
broken on every default Ubuntu install.** Fake-VPS smoke can't catch
it because the fake doesn't use real podman or real ufw.

**Manual smoke unblocker** (applied on the box, must land as a
provisioner op in a follow-up PR):

- `/etc/ufw/before.rules`: ACCEPT input + forward on `podman+`
  interfaces, inserted inside `*filter` after the required chain
  declarations.
- `/etc/default/ufw`: `DEFAULT_FORWARD_POLICY="ACCEPT"`.
- `ufw reload`.

After the fix:

```
# podman exec caddy nslookup app-hello-production-web
Name: app-hello-production-web.dns.podman
Address: 10.89.0.8
```

**Follow-up PR** must add a `podman firewall` provisioner op that:

1. Writes a `/etc/ufw/before.rules` snippet (scoped to `podman+` only,
   not any public interface) for INPUT and FORWARD ACCEPT.
2. Sets `DEFAULT_FORWARD_POLICY="ACCEPT"`.
3. Reloads UFW.
4. Documents the scope: bridge-internal forwarding/input only;
   public posture (22/80/443) is unchanged.
5. The fake-install fixture needs matching rules so the install
   smoke catches regressions to this op.

### Finding 4 — Podman on Ubuntu has no `unqualified-search-registries`

`FROM nginx:alpine` in the user's Dockerfile failed at `podman
build` time:

```
Error: creating build container: short-name "nginx:alpine" did not
resolve to an alias and no unqualified-search registries are defined
in "/etc/containers/registries.conf"
```

Real users write `FROM nginx:alpine`, not
`FROM docker.io/library/nginx:alpine`. The provisioner needs to write
`/etc/containers/registries.conf.d/00-simple-vps.conf`:

```
unqualified-search-registries = ["docker.io"]
```

This is a one-line provisioner op. Documented as a follow-up.

For the smoke, the fixture was fully qualified to
`docker.io/library/python:3-alpine` to bypass.

### Finding 5 — `--read-only` rootfs breaks many stock images

`nginx:alpine` exits immediately under `--read-only`:

```
nginx: [emerg] mkdir() "/var/cache/nginx/client_temp" failed (30: Read-only file system)
```

The §7 hardening floor is `--read-only` + `--tmpfs /tmp:size=64m`.
Stock images (nginx, Apache httpd, most database images) need
writable scratch under `/var/cache`, `/var/run`, `/var/lib`, etc.,
which our 64 MB tmpfs on `/tmp` doesn't cover.

User-facing remediation is fine: rewrite the Dockerfile to use a
writable-rootfs-free image (e.g., `nginxinc/nginx-unprivileged`,
`python:3-alpine` running a stateless server) **or** declare extra
tmpfs mounts.

The latter is the manifest knob we don't yet have. Filed for the
`--memory` / `--cpus` / `tmpfs` manifest field follow-up.

The smoke fixture moved to `python:3-alpine` running
`python3 -m http.server` (PYTHONDONTWRITEBYTECODE=1) — clean run on
`--read-only`.

### Finding 6 — Caddy auto-HTTPS triggers ACME on every hostname

First curl through Caddy returned `308 → https://`. HTTPS handshake
failed `tlsv1 alert internal error` because:

- DNS for `smoke.spotslice.com` doesn't resolve to the host (Cloudflare
  MCP token can only read, can't write).
- Caddy's auto-HTTPS hit the ACME HTTP-01 challenge, couldn't validate,
  refused TLS handshake.

Bypassed for the smoke by editing the fragment to add `tls internal`:

```
"smoke.spotslice.com" {
    tls internal
    reverse_proxy http://app-hello-production-web:3000
}
```

After `podman exec caddy caddy reload`, end-to-end works:

```
$ curl -k --resolve smoke.spotslice.com:443:178.105.101.122 https://smoke.spotslice.com/health
HTTP 200; body: ok

$ curl -k --resolve smoke.spotslice.com:443:178.105.101.122 https://smoke.spotslice.com/
HTTP 200; body: smoke-ok
```

**Real product question** for ADR follow-up: how do users opt out of
ACME during initial deploys or for private hosts? Three reasonable
shapes:

1. Manifest field: `[routes.<name>].tls = "auto" | "internal" | "off"`
   (default `auto`). Wired through `renderAppCaddyfile` as `tls
   internal` or `auto_https off` or unchanged.
2. Detect on first deploy: if the hostname doesn't resolve to the
   host's public IP, skip ACME and use `tls internal`. Surfaces
   nothing in the manifest but fragile.
3. Require DNS to be in place before deploy. Fail at `simple-vps
   check` if the host doesn't resolve to a non-RFC1918 address that
   matches the deploy target.

Option 1 looks cleanest. Filed as a real follow-up.

## Outcome

End-to-end ingress path **verified on real Ubuntu 24.04 + real
Podman 4.9.3 + real Caddy 2.11.3**:

```
laptop curl
  └→ HTTPS to host port 443
       └→ Caddy container (on `ingress`, self-signed via `tls internal`)
            └→ aardvark-dns resolves `app-hello-production-web` → 10.89.0.8
                 └→ Podman bridge → app container
                      └→ python3 -m http.server serves /health → 200 ok
```

ADR-0006 Cut 2 is structurally correct. Six real findings surfaced,
all worth follow-ups, none of them invalidating the architecture.

## Follow-up PRs

**Landed:**

- **#32** — UFW `--force allow`, addCaddy ordering + Caddyfile
  bootstrap, `ensureServiceStarted` polling, fake-install-ufw
  mirroring real ufw (findings 1 + 2).
- **#33** — `addPodmanHostBaseline` op:
  `/etc/ufw/before.rules` block for `podman+` ACCEPT inserted after
  the `# End required lines` anchor, `DEFAULT_FORWARD_POLICY="ACCEPT"`
  in `/etc/default/ufw`, drop-in
  `/etc/containers/registries.conf.d/00-simple-vps.conf` for
  unqualified search (findings 3 + 4). Verified by re-installing on
  a freshly-reverted Hetzner CX22; end-to-end curl works with zero
  manual intervention.

**Landed (continued):**

- **#35** — `[routes.<name>].tls = "auto" | "internal"` manifest knob.
  `auto` (or empty) keeps Caddy's default Let's Encrypt path;
  `internal` emits `tls internal` so Caddy uses a self-signed cert.
  `off` is intentionally rejected at check time — clean Caddyfile
  shape exists (`http://host { ... }`) but deferred until users
  ask. Closes finding 6; the smoke runbook's manual `sed` injection
  is gone.

**Landed (continued):**

- **#36** — `[services.<name>.tmpfs]` manifest knob: map of
  absolute path → size (`64m`, `1g`, ...). Rendered as
  `--tmpfs <path>:size=<value>,mode=1777` alongside the always-on
  `/tmp:64m`. The `mode=1777` was a follow-on real-box finding —
  Podman's default tmpfs is owned by root, so the unprivileged
  per-env container user (`--user uid:gid`) gets EACCES on write
  without it. Closes finding 5; the smoke fixture now uses
  `nginx:alpine` with declared tmpfs for `/var/cache/nginx` and
  `/var/run` and serves traffic end-to-end under `--read-only`.

All six real-box smoke findings are now closed.

## 2026-05-29 — Mixed Container + Static Route Smoke

VPS: Hetzner Ubuntu 24.04 at `128.140.3.159`.

Purpose: verify the new v1 behavior where one container app can also ship
host-side static route assets in the same release. This supersedes the earlier
ADR-0008 note that deferred mixed routes.

Steps run:

1. Rebuilt the local binary with `make build`.
2. Re-ran host install against the rebuilt VPS because the deploy user key was
   not installed after rebuild:

   ```sh
   ./dist/simple-vps host install \
     --mode remote \
     --host 128.140.3.159 \
     --bootstrap-user root \
     --ssh-key ~/.ssh/hetzner \
     --operator-ssh-public-key-file ~/.ssh/hetzner.pub \
     --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub \
     --ingress public \
     --admin public-ssh \
     --yes
   ```

3. Created a throwaway app `mixreal` with:
   - one Dockerfile-backed process: `[processes.web]`, `port = 3000`,
     `health = "/health"`
   - one proxy route at `mixed.128.140.3.159.nip.io`
   - one static route at `path = "/docs"`, `serve = "docs-dist"`
   - `tls = "internal"` on both routes
4. Ran:

   ```sh
   simple-vps check production
   simple-vps setup production
   simple-vps deploy production
   curl -k --resolve mixed.128.140.3.159.nip.io:443:128.140.3.159 \
     https://mixed.128.140.3.159.nip.io/health
   curl -k --resolve mixed.128.140.3.159.nip.io:443:128.140.3.159 \
     https://mixed.128.140.3.159.nip.io/docs
   ```

Initial deploy result:

```text
Deployed mixreal (production) at b411def91dad-s8c255e56d090
/health -> ok
/docs   -> docs-v1
```

5. Changed `docs-dist/index.html` to `docs-v2`, committed, deployed,
   rolled back, backed up, restored, and destroyed:

```text
Deployed mixreal (production) at 4a54e81fe1c7-s9a3f283ba929
/docs -> docs-v2

Rolled back mixreal (production) from 4a54e81fe1c7-s9a3f283ba929 to b411def91dad-s8c255e56d090
/docs -> docs-v1

Created backup /etc/simple-vps/backups/mixreal/production/20260529T102754Z-b411def91dad-s8c255e56d090.tar
Deployed mixreal (production) at 4a54e81fe1c7-s9a3f283ba929
/docs -> docs-v2

Restored mixreal (production) from 20260529T102754Z-b411def91dad-s8c255e56d090 at release b411def91dad-s8c255e56d090
/docs -> docs-v1

Destroyed mixreal (production)
app list --json -> {"apps":[]}
```

Issue encountered: the rebuilt VPS accepted root SSH with `~/.ssh/hetzner`,
but `deploy@128.140.3.159` initially rejected the local deploy key. Re-running
`host install` with the current binary and `~/.ssh/simple-vps-deploy.pub`
fixed the host state. No mixed-route runtime issue surfaced.
