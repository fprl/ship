# Simple VPS

Simple VPS is one CLI for deploying containerized apps to a single hardened
VPS — built for solo developers and small teams.

```text
fresh Ubuntu VPS  ->  install.sh         ->  hardened box
your app repo     ->  simple-vps deploy  ->  live app
```

## Packages

```text
simple-vps
  Unified Go binary for the app deploy CLI, host installer, and privileged
  server API.
```

## Start Here

The product positioning and design discipline live in
[docs/positioning.md](docs/positioning.md).
The public product contract lives in [SPEC.md](SPEC.md).
The host security model lives in [docs/security-model.md](docs/security-model.md).

The root installer is a thin bootstrap that runs `simple-vps host install`:

```bash
./install.sh --mode remote --host 203.0.113.10 --ssh-key ~/.ssh/id_ed25519
```

Build the Go CLI locally:

```bash
make build
./dist/simple-vps check production
```

Build release binaries:

```bash
make build-release
```

Run the main checks:

```bash
make test
make fake-vps-smoke           # Docker-backed Go client/helper smoke
make fake-vps-install-smoke   # Docker-backed fresh host install smoke
```

Implementation references:

- [SPEC.md](SPEC.md)
- [docs/adr/0001-replace-ansible-with-bounded-go-provisioner.md](docs/adr/0001-replace-ansible-with-bounded-go-provisioner.md)
- [docs/adr/0002-state-file-layout.md](docs/adr/0002-state-file-layout.md)
