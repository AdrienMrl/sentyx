#!/usr/bin/env bash
# Deploy the teslcam collector backend to a VPS.
#
#   scripts/deploy-collector.sh setup <listen-addr>   one-time server setup
#   scripts/deploy-collector.sh deploy                build + ship + restart
#   scripts/deploy-collector.sh status                service status
#   scripts/deploy-collector.sh logs [n]              last n log lines (default 100)
#
# Target host is adri@vps; override with TESLCAM_VPS=user@host.
#
# The listen address should NOT be public — use the VPS's Tailscale IP
# (e.g. 100.x.y.z:8090) or 127.0.0.1:8090 behind an SSH tunnel: the ingest
# API has no auth.
#
# Server layout (created by setup):
#   /usr/local/bin/teslcam-collect       binary (replaced by deploy)
#   /opt/teslcam/gemini                  analyzer (rsynced by deploy)
#   /var/lib/teslcam                     data: SQLite DB + received clips
#   /etc/teslcam/collect.env             config: listen addr, GEMINI_API_KEY
#   /etc/systemd/system/teslcam-collect.service
set -euo pipefail

HOST="${TESLCAM_VPS:-adri@vps}"
SSH=(ssh -o ConnectTimeout=10 "$HOST")
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

usage() {
  cat <<EOF
usage: $0 <command>

  setup <listen-addr>   one-time server setup (user, dirs, config, systemd unit)
                        listen-addr should be the VPS Tailscale IP:port or
                        127.0.0.1:8090 — the ingest API has no auth
  deploy                cross-compile, upload binary + analyzer, restart, health-check
  status                systemctl status of the service
  logs [n]              last n journal lines (default 100)

target host: $HOST (override with TESLCAM_VPS=user@host)
EOF
  exit 1
}
say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# run_sudo NAME=value... <<'EOF' ... — runs a script as root on the VPS.
# The script travels base64-encoded so quoting is a non-issue and stdin
# stays free for sudo's password prompt (hence ssh -t).
run_sudo() {
  local b64
  b64="$(base64 <"/dev/stdin" | tr -d '\n')"
  ssh -t -o ConnectTimeout=10 "$HOST" \
    "echo $b64 | base64 -d | sudo env ${*:-_=_} bash -s"
}

remote_arch() {
  local m
  m="$("${SSH[@]}" uname -m)"
  case "$m" in
    x86_64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) die "unsupported VPS architecture: $m" ;;
  esac
}

cmd_setup() {
  local listen="${1:-}"
  [[ -n "$listen" ]] || die "usage: $0 setup <listen-addr>   (e.g. the VPS Tailscale IP: 100.x.y.z:8090)"

  say "setting up $HOST (listen: $listen)"
  run_sudo "LISTEN=$listen" <<'EOF'
set -euo pipefail

# Service user + directories.
id -u teslcam >/dev/null 2>&1 || useradd --system --home /var/lib/teslcam --shell /usr/sbin/nologin teslcam
mkdir -p /var/lib/teslcam /opt/teslcam/gemini /etc/teslcam
chown teslcam:teslcam /var/lib/teslcam
chown -R "$SUDO_USER" /opt/teslcam   # deploy rsyncs the analyzer here without sudo

# Config: created once, never overwritten (it holds the API key).
if [[ ! -f /etc/teslcam/collect.env ]]; then
  cat > /etc/teslcam/collect.env <<ENV
# teslcam collector config (systemd EnvironmentFile)
LISTEN_ADDR=$LISTEN
QUIET_PERIOD=90s
# Analysis is off until both are set; then: systemctl restart teslcam-collect
GEMINI_API_KEY=
ANALYZE_CMD=
# To enable analysis, set GEMINI_API_KEY above and use:
# ANALYZE_CMD=npx tsx /opt/teslcam/gemini/analyze-video.ts --json
ENV
  chmod 640 /etc/teslcam/collect.env
  chown root:teslcam /etc/teslcam/collect.env
  echo "wrote /etc/teslcam/collect.env"
else
  echo "/etc/teslcam/collect.env already exists — left untouched"
fi

cat > /etc/systemd/system/teslcam-collect.service <<'UNIT'
[Unit]
Description=TeslaCam collector
After=network-online.target
Wants=network-online.target

[Service]
User=teslcam
EnvironmentFile=/etc/teslcam/collect.env
Environment=HOME=/var/lib/teslcam
# The analyzer dir, so `npx tsx` resolves the locally installed tsx.
WorkingDirectory=/opt/teslcam/gemini
ExecStart=/usr/local/bin/teslcam-collect \
  -data /var/lib/teslcam \
  -listen ${LISTEN_ADDR} \
  -quiet ${QUIET_PERIOD} \
  -analyze ${ANALYZE_CMD}
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=full
ReadWritePaths=/var/lib/teslcam

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable teslcam-collect

# Node for the analyzer (best effort; only needed once analysis is enabled).
if ! command -v node >/dev/null; then
  if command -v apt-get >/dev/null; then
    apt-get install -y nodejs npm
  else
    echo "NOTE: node not found and apt-get unavailable — install Node 18+ manually before enabling analysis" >&2
  fi
fi
echo "setup done"
EOF

  say "setup complete — next steps:"
  echo "  1. (optional) set GEMINI_API_KEY + ANALYZE_CMD in /etc/teslcam/collect.env on the VPS"
  echo "  2. run: $0 deploy"
}

cmd_deploy() {
  local arch tmp
  arch="$(remote_arch)"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  say "building teslcam-collect for linux/$arch"
  (cd "$ROOT" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -o "$tmp/teslcam-collect" ./cmd/teslcam-collect)

  say "uploading binary"
  scp -q "$tmp/teslcam-collect" "$HOST:/tmp/teslcam-collect.new"

  say "syncing analyzer to /opt/teslcam/gemini"
  rsync -az --delete \
    --exclude node_modules --exclude test-video --exclude .env \
    "$ROOT/experiments/gemini/" "$HOST:/opt/teslcam/gemini/"
  "${SSH[@]}" 'cd /opt/teslcam/gemini && if command -v npm >/dev/null; then npm ci --omit=dev --silent; else echo "NOTE: npm missing — analyzer deps not installed" >&2; fi'

  say "installing binary, restarting, health-checking"
  run_sudo <<'EOF'
set -euo pipefail
install -m 755 /tmp/teslcam-collect.new /usr/local/bin/teslcam-collect
rm -f /tmp/teslcam-collect.new
systemctl restart teslcam-collect

addr="$(grep '^LISTEN_ADDR=' /etc/teslcam/collect.env | cut -d= -f2)"
for _ in $(seq 1 10); do
  if curl -fsS --max-time 2 "http://$addr/healthz" >/dev/null 2>&1; then
    echo "healthy: http://$addr"
    exit 0
  fi
  sleep 1
done
echo "collector not responding on $addr after 10s" >&2
systemctl status teslcam-collect --no-pager -n 20 || true
exit 1
EOF
  say "deployed"
}

cmd_status() { "${SSH[@]}" systemctl status teslcam-collect --no-pager; }
cmd_logs() { "${SSH[@]}" journalctl -u teslcam-collect --no-pager -n "${1:-100}"; }

case "${1:-}" in
  setup) shift; cmd_setup "$@" ;;
  deploy) cmd_deploy ;;
  status) cmd_status ;;
  logs) shift; cmd_logs "$@" ;;
  *) usage ;;
esac
