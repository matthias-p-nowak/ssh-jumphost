# Design

How the jump host meets the requirements in `requests.md` (IDs in brackets).
Reference for patterns: `/net/xen/lvms/nas/docker/sftp-server` (key-only sshd, first-run seeding, user helper).

## Overview

```
 hostA (behind firewall)                      person laptop
   │ ssh -N -R 22001:localhost:22                │ ssh -J jumper -p 22001 me@localhost
   ▼                                             ▼
 ┌─────────────────────── jumper container ───────────────────────┐
 │ sshd 192.168.1.7:22 (backbone; router forwards the public port) │
 │ localhost:22001 ──► tunnel to hostA:22                          │
 │ localhost:22002 ──► tunnel to hostB:22                          │
 └─────────────────────────────────────────────────────────────────┘
```

A person always ends up at `localhost:<port>` inside the container; sshd forwards that to the host
through its reverse tunnel. Authentication to the target is end to end (client key or forwarded agent).

## Components

| Component | Path in container | Purpose |
|---|---|---|
| sshd config | `/etc/ssh/sshd_config` | key-only, per-group rules [R8, R9, R14] |
| hosts file | `/etc/jumper/hosts` | host name, port, default user, description, LAN targets [R6, R7] |
| `jumper` binary | `/usr/local/bin/jumper` | all own logic, see below [R16] |
| `/etc` skeleton | `/usr/share/jumper/etc/` | defaults copied into an empty `/etc` volume [R4] |
| client unit + installer | `client/jumper-tunnel@.service`, `client/install-tunnel.sh` | keeps a host's tunnel up [R15] |
| client config | `client/ssh_config.sample` | `Host` entries with `ProxyJump` for persons [R15] |

`jumper` subcommands:

| Invocation | Purpose |
|---|---|
| `jumper init [-n]` | entrypoint: prepare `/etc`, then exec sshd (`-n`: prepare only) [R4] |
| `jumper add host\|person <name>`, `jumper key <name>`, `jumper remove <name>` | manage accounts and keys [R8] |
| `jumper trust [-y] <host>` | record a host's key in `ssh_known_hosts` for menu connect [R11] |
| `jumper sync` | regenerate per-account sshd rules from the hosts file, reload sshd |
| login shell (`-jumper`, `jumper -c <cmd>`) | menu for persons, refusal message for hosts [R10, R11] |

Admin commands run via `docker compose exec jumper jumper <subcommand>`.

## Image [R16]

Multi-stage build, following sftp-server:
1. `golang:*-alpine`: builds `jumper` statically (`CGO_ENABLED=0`).
2. `alpine` + `openssh-server`: collects into `/stage` the sshd programs (`sshd`, `sshd-session`, `sshd-auth`), the shared libraries `ldd` lists for them, `moduli`, the `/etc` skeleton, `jumper`, and empty `/var/empty`, `/run`, `/tmp` (1777, for forwarded agent sockets).
3. `scratch`: copies `/stage`.

No shell exists in the image. sshd runs every session through the user's login shell (`<shell> -c <command>`), so `jumper` is the login shell of all accounts.

## Hosts file

One entry per line, `#` for comments, whitespace-separated:

```
host  hostA     22001  matthias  Build server in office
lan   hostA     nas.local        NAS behind hostA
host  hostB     22002  pi        Raspberry Pi at home
```

- `host <name> <port> <default-user> <description...>` — `<default-user>` is the login user on the host, used by the menu for connect and hint lines; `-` = none (the menu asks, hints show `<user>`)
- `lan <host> <target> <description...>` — LAN target reachable via that host [R7]

The admin edits this file; `jumper sync` and the menu read it. Ports are unique; suggested range 22001–22999.

## Accounts

Unix groups separate the two account types [R8]:

| | host account | person account |
|---|---|---|
| group | `hosts` | `persons` |
| shell | `/usr/local/bin/jumper` (prints "tunnel only", exits) | `/usr/local/bin/jumper` (menu; any `-c` command is refused) |
| remote forward (`-R`) | only `localhost:<own port>` | no |
| local forward (`-J`) | no | only to `localhost:<tunnel ports>` |
| agent forwarding | no | yes (menu connect) |
| session | none (`-N`) | menu |

Public keys live in `/etc/ssh/authorized_keys/<name>` (admin-managed, persisted by the `/etc` volume). Accounts have home `/var/empty`; nothing per user is persisted [R4].
There is no `adduser` in the image; `jumper` edits `passwd` and `shadow` itself:
- `jumper add person <name>` creates the account (uid from 2000 up, primary group `persons` 1002, home `/var/empty`, shell `jumper`, shadow password `*` = not locked for key login) with an empty key file.
- `jumper add host <name> [<port> [<default-user> [description]]]` does the same with group `hosts` 1001. If the hosts file has no `host <name>` line, it writes one. Without a port argument the values are asked interactively (port default: next free from 22001; empty default user = `-`); without a terminal that is an error. With only a port, default user and description stay empty.
- `jumper key <name>` appends public keys to `/etc/ssh/authorized_keys/<name>` (validated, duplicates skipped): read from stdin, or pasted on the terminal until an empty line.
- `jumper remove <name>` deletes the account (uid ≥ 2000 only), its key file and, for a host, its `host`/`lan` lines and its `ssh_known_hosts` entry.
- All of them finish with `jumper sync`.

Usage (`-it` for questions/paste, `-i` for piped input):
```
docker exec -it <container> jumper add host jetson
docker exec -i  <container> jumper key jetson < jetson.pub
```

## sshd configuration [R9, R14]

The skeleton `sshd_config` holds only global settings, restrictive by default:

```
HostKey /etc/ssh/ssh_host_ed25519_key
AuthorizedKeysFile /etc/ssh/authorized_keys/%u
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowGroups hosts persons
GatewayPorts no              # remote forwards bind to container localhost only
AllowTcpForwarding no        # opened per account type below
AllowAgentForwarding no
AllowStreamLocalForwarding no
PermitTunnel no
PermitTTY no
X11Forwarding no
MaxAuthTries 3
LoginGraceTime 20
LogLevel VERBOSE
Include /etc/ssh/sshd_config.d/*.conf   # must stay last
```

All `Match` blocks are generated by `jumper sync` from the hosts file into `/etc/ssh/sshd_config.d/jumper-generated.conf`:

```
Match User hostA Group hosts        # one block per host line
    AllowTcpForwarding remote
    PermitListen 22001

Match Group persons
    AllowTcpForwarding local
    PermitOpen localhost:22001 127.0.0.1:22001 ...   # all tunnel ports; "none" if no hosts
    AllowAgentForwarding yes
    PermitTTY yes
```

`jumper sync` checks the result with `sshd -t`; on failure the previous file is restored and the error shown. Then it reloads sshd (SIGHUP to PID 1). Changing the hosts file therefore means running `jumper sync`. At startup, `jumper init` runs the same generation without reload; a failure is logged and sshd starts with the previous rules.

## Connection flows

**Tunnel** [R2, R6] — the host runs `ssh -N -R <port>:localhost:22 <host-account>@jumper`, kept alive by the client unit.

**ProxyJump** [R12] — `ssh -J person@jumper -p <port> user@localhost`. `-J` opens only a `direct-tcpip` channel, no session, so no program (and no menu) runs on the jump host.

**Menu** [R10, R11] — `ssh -A person@jumper` gets a pty and sshd starts the login shell `jumper`:
1. read `/etc/jumper/hosts`; a host is *online* if a TCP connect to `localhost:<port>` succeeds
2. print a numbered list: name, port, online/offline, description, LAN targets, and hint lines (`jumper` = the `Host` alias from the sample client config):
   - `ssh -J jumper -p <port> <user>@localhost`
   - LAN target: `ssh -J jumper,<user>@localhost:<port> <user>@<target>`
3. prompt: number = connect, `r` = refresh, `q` = quit
4. connect: user = default user, or asked; built-in ssh client (`golang.org/x/crypto/ssh`) to `localhost:<port>`, authenticating with the forwarded agent (`SSH_AUTH_SOCK`), interactive pty session (raw terminal, window-size changes forwarded, agent forwarded on to the target so the person can hop further); back to the menu when the session ends
5. a person running a command (`ssh person@jumper <cmd>`) is refused; without a pty (`ssh -T`) the list is printed once

Target host keys are checked strictly against the global `/etc/ssh/ssh_known_hosts` (entries `[localhost]:<port> ...`); an unknown or changed key is refused with its fingerprint shown. The admin records a key with `jumper trust <host>` (fetches the key through the tunnel, shows the fingerprint, asks for confirmation; `-y` without a terminal; replaces an older entry). Without `-A` the menu still lists hosts but says that connecting needs agent forwarding.

**Chain into a LAN** [R13] — `ssh -J jumper,hostA user@nas.local`, with `jumper` and `hostA` from the sample client config `client/ssh_config.sample`: `Host jumper` (public address, person account), one `Host <name>` per tunnel host (`HostName localhost`, `Port <tunnel port>`, `ProxyJump jumper`, `HostKeyAlias <name>` so the client's known_hosts keeps the hosts apart although all are `localhost`), and LAN targets with `ProxyJump <name>`.

## Volumes and startup [R3, R4, R5]

Compose: `./etc:/etc` (the only volume; backup = this folder), network `backbone` (ipvlan) with a fixed IP, `restart: unless-stopped`. No `ports:` mapping: it has no effect on ipvlan [R5].

The skeleton `/usr/share/jumper/etc/` is minimal: `passwd` (root, sshd privsep user), `group` (incl. `hosts`, `persons`), `shadow`, `ssh/sshd_config`, `ssh/moduli`. `jumper init`:
1. if `/etc/ssh/sshd_config` is missing → copy the skeleton into `/etc` (first run; existing files are kept)
2. create the ed25519 host key `/etc/ssh/ssh_host_ed25519_key` if missing (persisted; the only host key type)
3. ensure `/etc/jumper/hosts`, `/etc/ssh/authorized_keys/` and `/etc/ssh/ssh_known_hosts` exist
4. `jumper sync`
5. `sshd -t`, then exec `/usr/sbin/sshd -D -e` (sshd becomes PID 1)

Image upgrades do not overwrite an existing `/etc`; new defaults must be merged by hand.

## Client side [R15]

Files in `client/`, installed on each firewalled host:
- `jumper-tunnel@.service` — systemd template, instance = host account name (`jumper-tunnel@jetson`). Reads `/etc/jumper-tunnel/<name>.env` (`JUMPER_HOST`, `JUMPER_PORT`, `TUNNEL_PORT`, `LOCAL_PORT`) and runs `ssh -N -R <tunnel port>:localhost:<local port> <name>@<jumper>` with key `/etc/jumper-tunnel/<name>_ed25519`, its own `known_hosts`, `BatchMode`, `ExitOnForwardFailure=yes`, `ServerAliveInterval=30`; `Restart=always`, `RestartSec=10`, after `network-online.target`.
- `install-tunnel.sh <name> <jumper-host> <tunnel-port> [jumper-port]` — run as root on the host: creates the key and env file, records the jump host key (`ssh-keyscan`, fingerprint shown for comparison), installs and enables the unit, prints the public key to hand to `jumper key <name>`.
- `ssh_config.sample` — for persons, see "Chain into a LAN".
