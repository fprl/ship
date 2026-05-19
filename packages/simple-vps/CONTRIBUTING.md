# Contributing

Thanks for helping improve Simple VPS.

## Local Validation Before PR

Run these checks from repo root:

```bash
bash -n install.sh
PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile roles/infra/files/simple-vps
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests
tests/install_plan_test.sh
tests/bootstrap_tarball_smoke.sh
ansible-playbook --syntax-check playbooks/vps-bootstrap.yml
ansible-playbook --syntax-check playbooks/vps-apply.yml
ansible-lint playbooks/vps-bootstrap.yml playbooks/vps-apply.yml
```

## Manual Test Matrix

For installer or role changes, validate both paths:

1. Existing VPS (compatibility + idempotency)
2. Fresh VPS (true bootstrap)

Recommended remote-mode test sequence:

```bash
./install.sh --mode remote --host <VPS_IP> --bootstrap-user root --ssh-key <KEY_PATH> --deploy-ssh-public-key-file <DEPLOY_PUBKEY> --check
./install.sh --mode remote --host <VPS_IP> --bootstrap-user root --ssh-key <KEY_PATH> --deploy-ssh-public-key-file <DEPLOY_PUBKEY>
./install.sh --mode remote --host <VPS_IP> --bootstrap-user root --ssh-key <KEY_PATH> --deploy-ssh-public-key-file <DEPLOY_PUBKEY>
```

## Password-Only Providers

If provider gives only password credentials first:

1. SSH into VPS as root with password.
2. Add public key to `/root/.ssh/authorized_keys` and prepare a deploy public key.
3. Run local mode on the VPS:

```bash
./install.sh --mode local --deploy-ssh-public-key-file <DEPLOY_PUBKEY>
```

Simple VPS intentionally blocks local mode when no key source exists to avoid lockout after SSH hardening.

## PR Focus

Please prioritize:

- Idempotency
- Reproducibility
- Clear errors and recovery paths
- Keeping `SPEC.md` accurate
