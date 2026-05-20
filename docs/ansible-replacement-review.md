# Ansible Replacement Review Archive

This brief captured the pre-ADR review of replacing Ansible with a Go
provisioner. The decisions are now recorded in:

- [ADR-0001: Replace Ansible with a Bounded Go Provisioner](adr/0001-replace-ansible-with-bounded-go-provisioner.md)
- [ADR-0002: State File Layout Under `/etc/simple-vps/`](adr/0002-state-file-layout.md)

Use those ADRs as the source of truth.

## Review Outcome

The review converged on replacing Ansible only for the core Simple VPS product
installer, not for personal/devbox extras.

The accepted direction:

- Keep the public install path as a local CLI install.
- Move host convergence into a bounded Go provisioner.
- Keep OpenSSH CLI as the transport.
- Keep the provisioner Ubuntu-only until a future ADR says otherwise.
- Require check mode from day one.
- Use a fixed operation budget for `internal/provision/host`.
- Use `(changed bool, err error)` operation results instead of a handler/event
  system.
- Keep language runtimes out of the default host install; make them explicit
  host features or app-driven requirements.
- Model ingress internally as `expose` plus `tunnel`, with simple CLI presets.
- Store host/app/route/provider state in separate JSON files under
  `/etc/simple-vps/`.

## Open Implementation Work

- Add the Go provisioner behind an opt-in flag.
- Keep the Ansible path until Docker fake-VPS smoke reaches parity.
- Migrate `/etc/simple-vps/state.json` to the ADR-0002 layout in a separate
  state-package change.
- Delete Ansible assets only after the Go path is defaulted and covered.
