# ssh-jumper

The menu (hosts, online state, ready-to-copy commands; pick a number to connect):

```
ssh -A -p <jumper port> <person>@<jumper address>
```

e.g. `ssh -A -p 22 alice@jumperhost.online.net`. `-A` forwards your agent, which the menu uses to
log in to the host; your client must run an agent holding the key (`ssh-add -l`, see "Agent
forwarding" below).

Straight to a host, as `<person>` on the jump host and `<user>` on the target:

```
ssh -J <person>@<jumper address>:<jumper port> -p <tunnel port> -o HostKeyAlias=<host> <user>@localhost
```

e.g. `ssh -J alice@jumperhost.online.net:22 -p 22101 -o HostKeyAlias=earth bob@localhost`.
The tunnel port of each host is in `etc/jumper/hosts` and in the menu (`ssh <person>@<jumper>`).
`HostKeyAlias` keeps the targets' keys apart in your `known_hosts`, since all of them are
`localhost`. No agent forwarding needed; for short commands see `client/ssh_config.sample`.

Public SSH jump host: firewalled **hosts** keep a reverse tunnel open to it, **persons** log in and
jump through those tunnels. Design: [docs/design.md](docs/design.md).

All admin commands run inside the container (compose service `jumper`):

```
docker compose exec jumper jumper <subcommand>        # interactive (questions, paste)
docker compose exec -T jumper jumper <subcommand> < f # piped input, e.g. a .pub file
```

Run them from this directory on the docker host. Subcommands that change sshd's rules reload sshd
themselves; new keys work at the next login without a reload.

## Start

```
docker compose up -d --build
docker compose logs -f
```

The first start fills `./etc` (the only state; back up this folder) and creates the host key.
Its fingerprint, for clients to compare (the image has no shell, so run this on the docker host):
`ssh-keygen -lf etc/ssh/ssh_host_ed25519_key.pub`.

Tell the menu how persons reach the jump host from outside (the router's address and forwarded
port), so its hint lines are complete `ssh -J` commands. Add to `etc/jumper/hosts`:

```
public  jumperhost.online.net  22
```

Without this line the hints use the `jumper` alias from `client/ssh_config.sample`.

## Add a host

A host is a machine behind a firewall. It gets a tunnel port (suggested 22001–22999).

1. On the jump host, create the account and its line in `etc/jumper/hosts`:
   ```
   docker compose exec jumper jumper add host mars
   ```
   This asks for the tunnel port (next free one offered), the default login user on the host
   (empty = none, the menu then asks) and a description. Non-interactive form:
   ```
   docker compose exec jumper jumper add host mars 22102 bob Build server on Mars
   ```
   Use `-` as user for "no default user".

2. On the host, as root, with a copy of `client/`:
   ```
   ./install-tunnel.sh mars jumperhost.online.net 22102 [jumper port]
   ```
   It creates a tunnel key, records the jump host's key (compare the fingerprint it prints),
   installs and enables `jumper-tunnel@mars`, and prints the public key.

3. On the jump host, add that public key:
   ```
   docker compose exec -T jumper jumper key mars < mars_ed25519.pub
   ```
   (or `docker compose exec jumper jumper key mars` and paste, end with an empty line)

4. On the host: `systemctl start jumper-tunnel@mars`.
   Check: `systemctl status jumper-tunnel@mars`, `journalctl -u jumper-tunnel@mars`.

5. On the jump host, once the tunnel is up, trust the host's ssh key so the menu can connect:
   ```
   docker compose exec jumper jumper trust mars
   ```
   Compare the fingerprint with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the host,
   then answer `y`.

### One-time tunnel (without systemd)

To test, or on a host without the unit: run on the host. The tunnel stays open until Ctrl-C.

```
ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
    -i <key> -p <jumper port> -R 22102:localhost:22 mars@jumperhost.online.net
```

`<key>` is the private key whose public half was added with `jumper key mars`; after
`install-tunnel.sh` that is `/etc/jumper-tunnel/mars_ed25519` (run as root). `22102` must be the
host's port from `etc/jumper/hosts`; any other port is refused. Stop the unit first if it is
running, or the port is already taken.

### LAN targets behind a host

Add `lan` lines after the host's `host` line in `etc/jumper/hosts` (as root on the docker host;
keep the file owner), then run `docker compose exec jumper jumper sync`:

```
host  mars    22102  bob       Build server on Mars
lan   mars    nas.local        NAS behind mars
```

### Change a host

Edit its line in `etc/jumper/hosts`, then `jumper sync`. After a port change also update
`TUNNEL_PORT` in `/etc/jumper-tunnel/mars.env` on the host, restart the unit, and run
`jumper trust mars` again.

## Remove a host

1. On the jump host:
   ```
   docker compose exec jumper jumper remove mars
   ```
   Deletes the account, its keys, its `host`/`lan` lines and its trusted host key.
2. On the host, as root:
   ```
   systemctl disable --now jumper-tunnel@mars
   rm /etc/jumper-tunnel/mars.env /etc/jumper-tunnel/mars_ed25519*
   ```

## Add a person

1. Create the account and add their public key(s):
   ```
   docker compose exec jumper jumper add person alice
   docker compose exec -T jumper jumper key alice < alice.pub
   ```
   Run `jumper key` again to add more keys (duplicates are skipped).
2. Give them `client/ssh_config.sample` as a template for `~/.ssh/config`. Then:
   - `ssh jumper` — menu: hosts, online state, ready-to-copy `ssh -J` lines; pick a number to connect
   - `ssh mars` — straight to a host via `ProxyJump`
   - `ssh lab-nas` — into the LAN behind a host

   The person's account on the target host must also accept their key; the jump host only
   forwards.

**Agent forwarding:** connecting from the menu needs `ssh -A` (`ForwardAgent yes` in the
`Host jumper` block) **and a running agent holding the key** on the person's machine: `-A` only
forwards an existing agent. Check with `ssh-add -l`; if there is none, run `eval $(ssh-agent)` and
`ssh-add` first. Without an agent the menu lists the hosts but never connects (it does not even ask
for the login user). The key must be accepted by the login user on the target. Whoever controls the jump host can use a forwarded agent while the session
is open. If that is not acceptable, leave it off and use `ssh -J` / `ProxyJump` only; the menu still
lists the hosts.

## Remove a person

```
docker compose exec jumper jumper remove alice
```

To drop a single key instead, delete its line from `etc/ssh/authorized_keys/alice` (as root on
the docker host). Keep the file's owner: sshd ignores a key file owned by someone else, which
can happen when an editor saves a new copy and docker remaps uids (`ls -ln` to check).

## Troubleshooting

**Menu connect fails:** the error is shown below the host list. Select the same host again: the
retry explains each step (forwarded agent and its keys, tunnel port, handshake, host key, login)
and gives a hint for the step that fails. Common causes:

| Menu says | Cause | Fix |
|---|---|---|
| `connecting needs agent forwarding` | no agent on the client, or login without `-A` | see "Agent forwarding" above |
| `the agent holds no keys` | agent runs, but empty | `ssh-add` on the client |
| `<host> is offline` | the host's tunnel is down | on the host: `systemctl status jumper-tunnel@<host>` |
| `host key of <host> is unknown` / `has CHANGED` | key not trusted yet, or the host was reinstalled | compare the fingerprint, `jumper trust <host>` |
| `unable to authenticate` | the target refuses the agent's keys for that user | add the key to `~<user>/.ssh/authorized_keys` on the target |
| `handshake failed: ... connection reset` / `EOF` | tunnel is up, but nothing answers where it points | see below |

**Tunnel up, but the far end refuses:** the jump host log (`docker compose logs`) shows
`channel 2: open failed: connect failed: Connection refused`. The `ssh -R` on the host forwards
to a port where its sshd does not listen. Check on the host with `ss -tlnp | grep ssh` and fix
`LOCAL_PORT` in `/etc/jumper-tunnel/<host>.env` (or the `-R` of a one-time tunnel).

**Check a tunnel from the jump host:** `jumper trust <host> </dev/null` prints the host key it
gets through the tunnel and changes nothing (without a terminal it refuses to record the key). Each
run counts as one connection without login at the target (see the next point), so use it sparingly.

**`srclimit_penalise` in the target's sshd log:** OpenSSH ≥ 9.8 (`PerSourcePenalties`) penalises
connections that leave without logging in, and everything through the tunnel comes from `::1`.
Enough of them block `::1` for a while, so every connection through that tunnel fails. The menu
therefore checks "online" without connecting; avoid repeated probes (`nc`, `ssh-keyscan`, `jumper
trust`) through the tunnel.

**Test sshd on a host** (more log output, several connections; `-d` would serve only one):
```
sudo /usr/sbin/sshd -D -e -p 7777 -o LogLevel=DEBUG3 2>&1
```
and point the tunnel at it with `-R <tunnel port>:localhost:7777`. A plain `nc -l` is useless as
a test target: it exits after the first connection; use `nc -lk`.

## Command reference

| Command | Purpose |
|---|---|
| `jumper add person <name>` | create a person account |
| `jumper add host <name> [<port> [<user>\|- [description]]]` | create a host account and its hosts-file line |
| `jumper key <name>` | add public keys (stdin or paste) |
| `jumper remove <name>` | delete an account, its keys and (host) its hosts-file lines and trusted key |
| `jumper trust [-y] <host>` | record a host's ssh key for menu connect |
| `jumper sync` | regenerate sshd rules from `etc/jumper/hosts` and reload sshd |

Names: lower-case letters, digits, `_`, `-`; at most 32 characters.
