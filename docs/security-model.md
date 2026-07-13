# Security model

`ship box setup <ssh-target>` converges one hardened host shape: Caddy serves
public traffic on ports 80 and 443, and SSH accepts keys only. UFW allows those
ports plus SSH; fail2ban, unattended upgrades, and SSH hardening are applied on
every setup.

Bootstrap SSH is transport only. Setup enrolls the ship identity and pins the
box host key in `~/.config/ship/known_hosts` after a successful install.
