# Requests

Accepted requirements for the SSH jump host. Canonical requirement text; `design.md` refers to these IDs.

## Purpose
- **R1** A publicly reachable SSH jump host lets computers behind firewalls connect to each other.
- **R2** Computers behind firewalls connect *out* to the jump host and offer a back connection (reverse tunnel).

## Deployment
- **R3** Runs as a docker compose project in `/net/xen/lvms/nas/docker/ssh-jumper`.
- **R4** `/etc` is the only mounted volume and holds all persistent state (config, accounts, public keys, host keys). On first start (empty volume) the container seeds `/etc` from a copy in the image; later starts keep the admin's changes. `/home` is not persisted; no private user files live on the jump host.
- **R5** The container has its own LAN address (NAS network `backbone`) and sshd listens on port 22 there; the public port is forwarded by the router.
- **R16** Like the sftp-server example, the image is a minimal extract (scratch) that contains only sshd (with its helper programs and libraries) and one Go binary. The Go binary does everything else: startup, account management, login menu, and the ssh client for menu connect. No shell in the image.

## Hosts and ports
- **R6** Each firewalled host has a fixed tunnel port, assigned by the admin in one config file together with name and description.
- **R7** A host may list further targets in its LAN; these are shown under the host in the menu.

## Accounts
- **R8** Two account types:
  - *host account*: may only open the reverse tunnel on its own port; no shell, no menu.
  - *person account*: gets the menu and may reach the listed hosts.
- **R9** Login only with SSH keys.

## Menu
- **R10** An interactive login of a person shows a menu with the hosts, their port numbers, whether each tunnel is online, and a ready-to-copy `ssh -J` command.
- **R11** Selecting a host in the menu connects to it, using the person's forwarded SSH agent. The jump host stores no keys for the targets.
- **R12** Non-interactive use (`ssh -J jumper ...`) works without the menu.

## Jumping further
- **R13** A person can continue from a tunnel host into its LAN with a ProxyJump chain (`ssh -J jumper,hostA target`).

## Security
- **R14** Baseline hardening: no root login, tunnels bound to localhost inside the container only, host accounts limited to their own port, limited auth attempts and login grace time.

## Client side
- **R15** The project ships a systemd unit template for firewalled hosts that keeps the reverse tunnel up, and a sample `~/.ssh/config` for persons.
