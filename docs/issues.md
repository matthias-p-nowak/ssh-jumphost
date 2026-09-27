# Issues

## Active issues
1. `client/` files — *implemented; verify on the Jetson*: `install-tunnel.sh jetson <jumper> 22001`, `jumper key`, `systemctl start jumper-tunnel@jetson`, tunnel survives a reboot; then remove.
2. README: setup, adding a host, adding a person, usage examples; `docs/architecture.md` after implementation.

## Deferred backlog
- Ban repeated failed logins (fail2ban or sshd `PerSourcePenalties`).
- Audit log: who opened which tunnel, who jumped where.
- Linking several public jump hosts.
