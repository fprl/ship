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
  Host installer, Ansible roles, and legacy Python helper kept during parity
  migration.

packages/cli
  Legacy Bun CLI for app deploys and app operations. The Go binary is replacing
  this package.
```

## Start Here

The public product contract lives in [SPEC.md](SPEC.md).
The host security model lives in [docs/security-model.md](docs/security-model.md).

The root installer delegates to [provisioning](provisioning):

```bash
./install.sh --mode remote --host 203.0.113.10 --ssh-key ~/.ssh/id_ed25519
```

Build the Go CLI locally:

```bash
make build
./dist/simple-vps check production
```

Build Linux helper binaries for provisioning:

```bash
make build-linux
```

Implementation references:

- [provisioning/SPEC.md](provisioning/SPEC.md)
- [packages/cli/SPEC.md](packages/cli/SPEC.md)
