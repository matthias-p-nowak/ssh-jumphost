# Architecture

Implemented state. Design intent: `design.md`; requirements: `requests.md`.

## Repository layout
- `Dockerfile` — 3 stages: `golang:1.26-alpine` builds `jumper` statically; `alpine:3.22` + `openssh-server` + `openssh-client-common` (for `moduli`) collects files into `/stage`; `scratch` copies `/stage`.
- `cmd/jumper/` — Go source of the `jumper` binary (module `github.com/matthias-p-nowak/ssh-jumphost`, dependency `golang.org/x/crypto`).
  - `main.go` — subcommand dispatch; `JUMPER_ROOT` path prefix (default `/`) for tests.
  - `init.go` — `jumper init [-n]`.
  - `hosts.go` — hosts file parser (`host`/`lan` lines, unique names and ports 1024–65535, names `^[a-z_][a-z0-9_-]{0,31}$`; default user optional, `-` or missing = none).
  - `sync.go` — `jumper sync`: writes `sshd_config.d/jumper-generated.conf` atomically, `sshd -t`, rollback on failure, SIGHUP to PID 1.
  - `accounts.go` — `jumper add person|host`, `jumper key`, `jumper remove`; edits `passwd`/`shadow` directly; `add host` appends the hosts line, `remove` drops `host`/`lan` lines (other lines and comments kept).
  - `prompt.go` — terminal questions (`golang.org/x/term` decides whether stdin is a TTY; `/dev/null` and pipes are not).
  - `shell.go` — login-shell mode (argv[0] `-jumper` or `-c`): hint for hosts; persons: `-c` refused, otherwise the menu.
  - `menu.go` — host list (online = TCP connect to `localhost:<port>` within 500 ms), hint lines, connect: `x/crypto/ssh` client with the forwarded agent, strict check against `ssh_known_hosts`, raw pty session with SIGWINCH forwarding and agent forwarding to the target.
  - `input.go` — one goroutine reads stdin; menu lines and session input take turns via a shared pending buffer (no keystrokes lost to a blocked reader). Session stdin is copied via `StdinPipe`, so `Wait` returns when the remote side ends.
  - `trust.go` — `jumper trust [-y] <host>`: captures the host key through the tunnel (handshake aborted), replaces the `[localhost]:<port>` entry.
- `client/` — `jumper-tunnel@.service` (instance = host account), `install-tunnel.sh`, `ssh_config.sample`; see design.md "Client side".
- `docker-compose.yml` — service `jumper`: builds the image, volume `./etc:/etc`, external network `backbone` (ipvlan) with `192.168.1.7` (NAS convention), no `ports:`, `restart: unless-stopped`.
- `.gitignore` — excludes the runtime `etc/` volume folder, `tmp/` and a locally built `jumper`; `.dockerignore` keeps it (and docs, .git) out of the build context.
- `rootfs/` — copied into the image as-is; `rootfs/usr/share/jumper/etc/` is the `/etc` skeleton (`passwd`, `group`, `shadow`, `ssh/sshd_config`).

## Image contents
`/usr/sbin/sshd`, `/usr/lib/ssh/sshd-session`, `/usr/lib/ssh/sshd-auth` (if present), the libraries `ldd` reports for them, `/lib/libc.musl-*` (name musl programs link against), `/usr/local/bin/jumper`, the skeleton plus Alpine's `moduli`, empty `/etc`, `/run`, `/var/empty`, `/tmp` (1777). No shell. Entrypoint: `jumper init`.

## Startup (`jumper init`)
1. Copies the skeleton into `/etc` when `/etc/ssh/sshd_config` is missing; existing files are kept.
2. Creates `/etc/ssh/ssh_host_ed25519_key` (+ `.pub`) in OpenSSH format if missing.
3. Ensures `/etc/jumper/hosts`, `/etc/ssh/authorized_keys/`, `/etc/ssh/sshd_config.d/`, `/etc/ssh/ssh_known_hosts`.
4. Generates the `Match` rules (`jumper sync` without reload); on error it logs and keeps the previous rules.
5. Runs `sshd -t`, then `exec sshd -D -e`. With `-n` it stops after step 3.

## Constraints
- Seeding triggers on a missing `sshd_config`, not an empty directory: docker has already placed `hosts`, `hostname`, `resolv.conf` in `/etc`. Existing files are never overwritten.
- Skeleton accounts use `/usr/local/bin/jumper` as shell, since no other shell exists; `root` and `sshd` are locked (`!`).
- The skeleton `sshd_config` has only restrictive globals (no forwarding, no agent, no TTY, `AllowGroups hosts persons`, `LogLevel VERBOSE`); all `Match` blocks come from `jumper sync`. Its `Include` must stay last.
- Host rules use `Match User <name> Group hosts` + `PermitListen <port>` (port only; `GatewayPorts no` forces loopback). Persons get `PermitOpen` with `localhost:` and `127.0.0.1:` for every tunnel port.
- Accounts: uid from 2000, primary group only (1001 hosts, 1002 persons; no member lists in `group`), shadow `*`. `remove` refuses uid < 2000. `add` on an existing account of the same type is a no-op, of another type an error. `key` skips duplicates (compared by key blob) and fails when no key is given.
- With `JUMPER_ROOT` set, `sync` skips the SIGHUP, and `sshd -t` is skipped when no sshd exists under the root.
- Only an ed25519 host key is created; very old clients without ed25519 support cannot connect.
- `backbone` is an ipvlan network: sshd is reachable only at `192.168.1.7:22`; `ports:` mappings are ignored. With ipvlan the NAS host itself usually cannot reach the container IP; test from another LAN machine.
- The NAS docker daemon uses user-namespace remapping: container root is uid/gid 100000 on the host, so files in `etc/` are owned by 100000. Edit them via `jumper` subcommands (`docker exec`) or as root on the NAS.
- Docker creates empty `hostname`, `hosts`, `resolv.conf` in `etc/` as mount points; they belong to docker, not to the skeleton.

## Verified (2026-09-26, on xen)
Image builds; first start seeds `/etc` and creates the host key; sshd listens on `192.168.1.7:22`; unknown user gets `Permission denied (publickey)`; host key fingerprint unchanged after `docker restart`.
Accounts and rules (temporary `testhost`/`testalice`, since removed): host opens `-R 22001` ✓, other port refused ✓, command gets tunnel-only hint ✓, `-W` refused ✓; person `-W localhost:22001` reaches the tunnelled sshd ✓, `-W localhost:22` refused ✓, `-R` refused ✓, interactive login gets the greeting ✓; `sync` reloads sshd via SIGHUP ✓.
Menu (temporary `testhost` = throwaway sshd on the dev machine behind a real tunnel, `testalice`; since removed, incl. known_hosts entry): `ssh -T` prints the list once ✓, a command is refused ✓, without `-A` connect is refused ✓, unknown host key refused with fingerprint ✓, `jumper trust -y` records the matching fingerprint ✓ (without `-y` and terminal refused ✓), `ssh -A -tt` → select → shell on the target ✓, agent visible on the target ✓, `exit` returns to the menu ✓, `q` quits ✓.
Client side: `install-tunnel.sh` passes `bash -n`; the unit's ssh command line was exercised manually. Not yet run on a systemd host.
