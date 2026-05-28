# Real-box smoke results

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
