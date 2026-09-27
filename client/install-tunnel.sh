#!/usr/bin/env bash
# Installs the reverse tunnel to the jump host on this (firewalled) host.
# Run as root:  ./install-tunnel.sh <name> <jumper-host> <tunnel-port> [jumper-port]
#   <name>        host account on the jump host (jumper add host <name>)
#   <jumper-host> public name or address of the jump host
#   <tunnel-port> the host's port from the jump host's hosts file
#   [jumper-port] ssh port of the jump host, default 22
# Afterwards hand the printed public key to the admin:
#   docker exec -i <container> jumper key <name> < <name>_ed25519.pub

set -euo pipefail

if [ $# -lt 3 ] || [ $# -gt 4 ]; then
    sed -n '3,9p' "$0"
    exit 1
fi
name="$1"
jumper_host="$2"
tunnel_port="$3"
jumper_port="${4:-22}"
local_port=22                         # sshd of this host
dir=/etc/jumper-tunnel
unit_src="$(dirname "$0")/jumper-tunnel@.service"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
install -d -m 700 "$dir"

# Key used only for the tunnel.
if [ ! -f "$dir/${name}_ed25519" ]; then
    ssh-keygen -q -t ed25519 -N '' -C "jumper-tunnel ${name}@$(hostname)" -f "$dir/${name}_ed25519"
fi

cat > "$dir/${name}.env" <<EOF
JUMPER_HOST=${jumper_host}
JUMPER_PORT=${jumper_port}
TUNNEL_PORT=${tunnel_port}
LOCAL_PORT=${local_port}
EOF

# Record the jump host's key (trust on first use) and show it for comparison.
ssh-keyscan -p "$jumper_port" -t ed25519 "$jumper_host" 2>/dev/null > "$dir/known_hosts.new"
[ -s "$dir/known_hosts.new" ] || { echo "cannot reach $jumper_host:$jumper_port" >&2; rm -f "$dir/known_hosts.new"; exit 1; }
cat "$dir/known_hosts.new" >> "$dir/known_hosts"
rm "$dir/known_hosts.new"
echo "jump host key (compare with ssh-keygen -lf etc/ssh/ssh_host_ed25519_key.pub on the jump host):"
ssh-keygen -lf "$dir/known_hosts" | tail -1

install -m 644 "$unit_src" /etc/systemd/system/jumper-tunnel@.service
systemctl daemon-reload
systemctl enable "jumper-tunnel@${name}"

echo
echo "public key for the jump host admin (jumper key ${name}):"
cat "$dir/${name}_ed25519.pub"
echo
echo "once the key is added:  systemctl start jumper-tunnel@${name}"
echo "status / log:           systemctl status jumper-tunnel@${name}; journalctl -u jumper-tunnel@${name}"
