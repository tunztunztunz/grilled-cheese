#!/usr/bin/env bash
# Installs the grilled-cheese app and registers it as a systemd user service.
#
# The app is deliberately separate from the plugin. A sandboxed agent cannot host
# the server itself — its processes are killed when each tool call ends, and its
# network namespace is unreachable from a browser — so the server has to be an
# ordinary background service owned by the login session.
#
# This must run outside any agent sandbox: writing a unit and talking to systemd
# both need access a sandbox denies.
set -euo pipefail

module="github.com/tunztunztunz/grilled-cheese"
# Agents run sandboxed with TMPDIR=/tmp/claude-<uid>, and that is the only
# directory they may write to, so the session has to live there.
workdir="/tmp/claude-$(id -u)/grill"
unit="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/grilled-cheese.service"

die_sandboxed() {
  echo "install.sh cannot reach systemd from here — it must run outside the agent's sandbox." >&2
  echo "Ask the user to run it in their own terminal." >&2
  exit 1
}

binary=$(command -v grilled-cheese || true)
if [ -z "$binary" ]; then
  if ! command -v go >/dev/null; then
    echo "grilled-cheese is not installed, and there is no Go toolchain to build it." >&2
    echo "Install Go, then re-run this, or fetch a release binary onto your PATH." >&2
    exit 1
  fi
  echo "installing $module ..."
  go install "$module@latest"
  binary=$(command -v grilled-cheese || echo "${GOBIN:-${GOPATH:-$HOME/go}/bin}/grilled-cheese")
fi

if [ ! -x "$binary" ]; then
  echo "installed, but no executable at $binary — is your GOBIN on PATH?" >&2
  exit 1
fi

if ! command -v systemctl >/dev/null; then
  echo "no systemd on this machine. Leave this running in a terminal instead:" >&2
  echo "  $binary serve --workdir $workdir" >&2
  exit 1
fi

mkdir -p "$(dirname "$unit")" 2>/dev/null || die_sandboxed
# Probe with touch first: a failing redirect is reported by the shell itself and
# cannot be silenced at the command.
touch "$unit" 2>/dev/null || die_sandboxed
cat > "$unit" <<UNIT
[Unit]
Description=grilled cheese grilling UI

[Service]
ExecStart=$binary serve --open=false --workdir $workdir
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
UNIT

systemctl --user daemon-reload 2>/dev/null || die_sandboxed
systemctl --user enable --now grilled-cheese.service 2>/dev/null || die_sandboxed
systemctl --user restart grilled-cheese.service 2>/dev/null || die_sandboxed

for _ in $(seq 20); do
  [ -f "$workdir/addr" ] && break
  sleep 0.25
done

if [ ! -f "$workdir/addr" ]; then
  echo "service started but published no address. Check:" >&2
  echo "  journalctl --user -u grilled-cheese -n 20" >&2
  exit 1
fi

echo "grilled cheese is serving http://$(cat "$workdir/addr")"
echo "binary:  $binary"
echo "session: $workdir"
echo "it will start automatically from now on; re-run this after an upgrade"
