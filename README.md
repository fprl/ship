# Simple VPS

Simple VPS is one CLI for running JS/TS apps on your own VPS without Docker.

```text
fresh Ubuntu VPS  ->  install.sh         ->  hardened box
your app repo     ->  simple-vps deploy  ->  live app
```

## Packages

```text
.
  Go module for the unified simple-vps binary. This is the migration target for
  both the app deploy CLI and the privileged server helper.

provisioning
  Ansible roles and host convergence assets. The Go host installer runs these
  playbooks and installs the Go server binary.
```

## Start Here

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
make fake-vps-smoke   # Docker-backed Go client/helper smoke
```

Implementation references:

- [provisioning/SPEC.md](provisioning/SPEC.md)
