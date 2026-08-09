#!/usr/bin/env bash
# Install the teslcam gadget agent and its dependencies on a fresh Raspberry Pi.
#
#   scripts/install-agent.sh setup             one-time OS prep (packages, dwc2,
#                                              modules, dirs, config, systemd unit)
#   scripts/install-agent.sh image <size-gb>   create the MBR+exFAT backing image
#   scripts/install-agent.sh scorer            build + install the camera scorer
#   scripts/install-agent.sh deploy            cross-compile, ship, restart agent
#   scripts/install-agent.sh ota <public-key>  install fleet updater + trust key
#   scripts/install-agent.sh check             verify prerequisites on the Pi
#   scripts/install-agent.sh status            service status
#   scripts/install-agent.sh logs [n]          last n journal lines (default 100)
#
# Target host is adri@sentyx.local; override with TESLCAM_PI=user@host.
#
# Fresh-Pi bring-up order (Raspberry Pi OS Bookworm, 64-bit Lite recommended):
#   1. setup      — installs packages, enables the dwc2 USB device-mode overlay.
#                   REBOOT the Pi if setup says so (overlay/modules changed).
#   2. image 64   — creates /var/lib/teslcam/backing.img. Must be MBR-partitioned
#                   (single exFAT partition): the Tesla MCU ignores partitionless
#                   "superfloppy" images (see internal/exfat/locate.go).
#   3. scorer     — builds tools/camera-scorer on the Pi (OpenCV, ~minutes).
#   4. deploy     — ships the agent binary and starts the service.
#   5. check      — sanity-check everything (UDC present, tools on PATH, ...).
#
# Provisioning (server URL + per-device token) is NOT done here: the agent
# starts unprovisioned and BLE onboarding from the app writes
# /etc/teslcam/agent.env + /etc/teslcam/server.token, then restarts the
# service. Nothing secret is created by this script.
#
# Runtime dependencies installed by setup:
#   ffmpeg           video compression before upload (+ ffprobe)
#   exfatprogs       mkfs.exfat for the backing image
#   bluez            bluetoothd + btmgmt (BLE onboarding; btmgmt is the
#                    legacy-advertising fallback on Pi 4 radios)
#   network-manager  nmcli, used by BLE Wi-Fi management (Bookworm default)
# vcgencmd (health/thermal telemetry) ships with Raspberry Pi OS; the agent
# degrades gracefully if it is absent.
set -euo pipefail

HOST="${TESLCAM_PI:-adri@sentyx.local}"
SSH=(ssh -o ConnectTimeout=10 "$HOST")
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

usage() {
  cat <<EOF
usage: $0 <command>

  setup             one-time OS prep: apt packages, dwc2 overlay + modules,
                    /var/lib/teslcam + /etc/teslcam, agent.env placeholder,
                    systemd unit (enabled, not started). May require a reboot.
  image <size-gb>   create the sparse MBR-partitioned exFAT backing image at
                    /var/lib/teslcam/backing.img (refuses to overwrite)
  scorer            install build deps, build tools/camera-scorer on the Pi,
                    install /usr/local/bin/teslcam-camera-scorer
  deploy            cross-compile teslcam-agent for the Pi, upload, restart
  ota <public-key>  install teslcam-updater and its Ed25519 public trust key
  check             verify prerequisites (UDC, modules, tools, image, unit)
  status            systemctl status of teslcam-agent
  logs [n]          last n journal lines (default 100)

target host: $HOST (override with TESLCAM_PI=user@host)
EOF
  exit 1
}
say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# run_sudo NAME=value... <<'EOF' ... — runs a script as root on the Pi.
# The script travels base64-encoded so quoting is a non-issue and stdin
# stays free for sudo's password prompt (hence ssh -t).
run_sudo() {
  local b64
  b64="$(base64 <"/dev/stdin" | tr -d '\n')"
  ssh -t -o ConnectTimeout=10 "$HOST" \
    "echo $b64 | base64 -d | sudo env ${*:-_=_} bash -s"
}

# Go cross-compile target for the Pi's userland (not just its CPU):
# a 64-bit Pi 4 running 32-bit Raspberry Pi OS reports armv7l.
remote_goarch() {
  local m
  m="$("${SSH[@]}" uname -m)"
  case "$m" in
    aarch64 | arm64) echo "GOARCH=arm64" ;;
    armv7l) echo "GOARCH=arm GOARM=7" ;;
    armv6l) echo "GOARCH=arm GOARM=6" ;;
    *) die "unsupported Pi architecture: $m" ;;
  esac
}

cmd_setup() {
  say "one-time setup on $HOST"
  run_sudo <<'EOF'
set -euo pipefail
NEED_REBOOT=0

# Runtime packages. --no-install-recommends keeps the Lite image lean.
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  ffmpeg exfatprogs bluez network-manager

# USB device-mode (gadget) support: the dwc2 overlay in peripheral mode, and
# the dwc2 + libcomposite modules at boot. Pi 4 glovebox setup — see CLAUDE.md.
CONFIG=/boot/firmware/config.txt
[[ -f "$CONFIG" ]] || CONFIG=/boot/config.txt
[[ -f "$CONFIG" ]] || { echo "no config.txt found under /boot" >&2; exit 1; }
if ! grep -q '^dtoverlay=dwc2' "$CONFIG"; then
  printf '\n# teslcam: USB device-mode gadget support\ndtoverlay=dwc2,dr_mode=peripheral\n' >> "$CONFIG"
  echo "added dwc2 overlay to $CONFIG"
  NEED_REBOOT=1
else
  echo "dwc2 overlay already in $CONFIG"
fi
if ! grep -q '^dtoverlay=ramoops' "$CONFIG"; then
  printf '\n# teslcam: persistent crash evidence\ndtoverlay=ramoops,console-size=16384\n' >> "$CONFIG"
  echo "added ramoops crash-log overlay to $CONFIG"
  NEED_REBOOT=1
else
  echo "ramoops overlay already in $CONFIG"
fi
for mod in dwc2 libcomposite; do
  if ! grep -qx "$mod" /etc/modules; then
    echo "$mod" >> /etc/modules
    echo "added $mod to /etc/modules"
    NEED_REBOOT=1
  fi
done

# Bluetooth must be up for BLE onboarding.
systemctl enable --now bluetooth

# Data + config directories. clips/ is the local extraction spool.
mkdir -p /var/lib/teslcam/clips /etc/teslcam

# agent.env: created once (unprovisioned), never overwritten. BLE onboarding
# fills POST_TO/DEVICE_ID and writes /etc/teslcam/server.token; until then the
# agent runs with uploads off (empty -post-to) and advertises for onboarding.
if [[ ! -f /etc/teslcam/agent.env ]]; then
  cat > /etc/teslcam/agent.env <<'ENV'
# teslcam agent config (systemd EnvironmentFile), written by BLE onboarding.
# Empty POST_TO = unprovisioned: uploads off, BLE onboarding advertising.
POST_TO=
DEVICE_ID=
ENV
  chmod 644 /etc/teslcam/agent.env
  echo "wrote /etc/teslcam/agent.env (unprovisioned)"
else
  echo "/etc/teslcam/agent.env already exists — left untouched"
fi

if [[ "$NEED_REBOOT" == 1 ]]; then
  echo "REBOOT-REQUIRED"
fi
EOF

  say "installing systemd units and power-evidence recorder"
  scp -q "$ROOT/scripts/teslcam-agent-pi4.service" "$HOST:/tmp/teslcam-agent.service"
  scp -q "$ROOT/scripts/teslcam-power-evidence" "$HOST:/tmp/teslcam-power-evidence"
  scp -q "$ROOT/scripts/teslcam-power-evidence.service" "$HOST:/tmp/teslcam-power-evidence.service"
  run_sudo <<'EOF'
set -euo pipefail
install -m 644 /tmp/teslcam-agent.service /etc/systemd/system/teslcam-agent.service
install -m 755 /tmp/teslcam-power-evidence /usr/local/sbin/teslcam-power-evidence
install -m 644 /tmp/teslcam-power-evidence.service /etc/systemd/system/teslcam-power-evidence.service
rm -f /tmp/teslcam-agent.service /tmp/teslcam-power-evidence /tmp/teslcam-power-evidence.service
systemctl daemon-reload
systemctl enable teslcam-agent teslcam-power-evidence.service systemd-pstore.service
echo "unit installed and enabled (start happens on deploy)"
EOF

  say "setup complete — next steps:"
  echo "  1. if setup printed REBOOT-REQUIRED: ssh $HOST sudo reboot"
  echo "  2. create the backing image:  $0 image 64"
  echo "  3. build the camera scorer:   $0 scorer"
  echo "  4. ship the agent:            $0 deploy"
  echo "  5. verify:                    $0 check"
  echo "  then provision via BLE onboarding from the app."
}

cmd_image() {
  local size_gb="${1:-}"
  [[ "$size_gb" =~ ^[0-9]+$ && "$size_gb" -gt 0 ]] || die "usage: $0 image <size-gb>   (e.g. 64)"

  say "creating ${size_gb}G MBR+exFAT backing image on $HOST"
  run_sudo "SIZE_GB=$size_gb" <<'EOF'
set -euo pipefail
IMG=/var/lib/teslcam/backing.img
if [[ -e "$IMG" ]]; then
  echo "$IMG already exists — refusing to overwrite (delete it first to recreate)" >&2
  exit 1
fi
command -v mkfs.exfat >/dev/null || { echo "mkfs.exfat missing — run setup first" >&2; exit 1; }

# Sparse image: blocks are allocated only as the car writes.
truncate -s "${SIZE_GB}G" "$IMG"

# MBR with a single exFAT partition (type 07). The Tesla MCU ignores
# partitionless "superfloppy" images — see internal/exfat/locate.go.
echo 'start=2048, type=7' | sfdisk --quiet "$IMG"

LOOP="$(losetup --find --show --partscan "$IMG")"
trap 'losetup -d "$LOOP" 2>/dev/null || true' EXIT
# --partscan is asynchronous on some kernels; wait for the partition node.
for _ in $(seq 1 20); do [[ -e "${LOOP}p1" ]] && break; sleep 0.5; done
[[ -e "${LOOP}p1" ]] || { echo "partition node ${LOOP}p1 never appeared" >&2; exit 1; }

mkfs.exfat -L TESLACAM "${LOOP}p1" >/dev/null

# The car only records to a drive that already has a top-level TeslaCam dir.
MNT="$(mktemp -d)"
mount "${LOOP}p1" "$MNT"
mkdir -p "$MNT/TeslaCam"
umount "$MNT"
rmdir "$MNT"
losetup -d "$LOOP"
trap - EXIT
echo "created $IMG (${SIZE_GB}G sparse, MBR + exFAT, TeslaCam/ present)"
EOF
  say "backing image ready"
}

cmd_scorer() {
  say "installing scorer build dependencies on $HOST"
  run_sudo <<'EOF'
set -euo pipefail
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  cmake g++ make libopencv-dev
EOF

  say "syncing tools/camera-scorer sources"
  # rsync needs a writable scratch dir; the build happens there, install goes
  # to /usr/local/bin via sudo.
  rsync -a --delete "$ROOT/tools/camera-scorer/" "$HOST:/tmp/teslcam-camera-scorer-src/"

  say "building on the Pi (this can take a few minutes; -j2 keeps thermals sane)"
  "${SSH[@]}" 'set -e
    cmake -S /tmp/teslcam-camera-scorer-src -B /tmp/teslcam-camera-scorer-build >/dev/null
    cmake --build /tmp/teslcam-camera-scorer-build -j2'

  run_sudo <<'EOF'
set -euo pipefail
install -m 755 /tmp/teslcam-camera-scorer-build/teslcam-camera-scorer /usr/local/bin/teslcam-camera-scorer
rm -rf /tmp/teslcam-camera-scorer-src /tmp/teslcam-camera-scorer-build
echo "installed /usr/local/bin/teslcam-camera-scorer"
EOF
  say "camera scorer installed"
}

cmd_deploy() {
  local goarch tmp version
  goarch="$(remote_goarch)"
  tmp="$(mktemp -d)"
  # Expand now: tmp is local, and an EXIT trap fires after it goes out of scope.
  trap "rm -rf '$tmp'" EXIT
  # CalVer: tags are vYYYY.M.N, so exact builds report "2026.8.1" and dev
  # builds "2026.8.1-3-g1f39698[-dirty]". Untagged history degrades to a hash.
  version="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)"

  say "building teslcam-agent for linux/$goarch (version $version)"
  # env applies $goarch ("GOARCH=arm64" or "GOARCH=arm GOARM=7"); a bare
  # expansion would be parsed as a command name, not an assignment.
  (cd "$ROOT" && env GOOS=linux CGO_ENABLED=0 $goarch \
    go build -trimpath -ldflags "-X main.version=$version" \
    -o "$tmp/teslcam-agent" ./cmd/teslcam-agent)

  say "uploading binary"
  scp -q "$tmp/teslcam-agent" "$HOST:/tmp/teslcam-agent.new"

  # Restart note: if the car currently holds the mass-storage LUN, gadget
  # teardown can block the restart for up to systemd's stop timeout — that is
  # expected; don't interrupt it (interrupting churns USB and can drop SSH).
  say "installing binary and restarting"
  run_sudo "VERSION=$version" <<'EOF'
set -euo pipefail
RELEASE="/opt/teslcam/releases/$VERSION"
install -d -m 755 "$RELEASE"
install -m 755 /tmp/teslcam-agent.new "$RELEASE/teslcam-agent"
[ ! -x /usr/local/bin/teslcam-camera-scorer ] || install -m 755 /usr/local/bin/teslcam-camera-scorer "$RELEASE/teslcam-camera-scorer"
ln -sfn "$RELEASE" /opt/teslcam/current.new
mv -Tf /opt/teslcam/current.new /opt/teslcam/current
rm -f /tmp/teslcam-agent.new
systemctl restart teslcam-agent
sleep 2
systemctl is-active --quiet teslcam-agent || {
  echo "teslcam-agent failed to start:" >&2
  journalctl -u teslcam-agent --no-pager -n 20 >&2
  exit 1
}

echo "teslcam-agent running"
EOF
  say "deployed"
}

cmd_ota() {
  local key="${1:-}" goarch tmp
  [[ -f "$key" ]] || die "usage: $0 ota <ota-public-key.pem>"
  goarch="$(remote_goarch)"
  tmp="$(mktemp -d)"
  trap "rm -rf '$tmp'" EXIT
  say "building teslcam-updater for linux/$goarch"
  (cd "$ROOT" && env GOOS=linux CGO_ENABLED=0 $goarch \
    go build -trimpath -o "$tmp/teslcam-updater" ./cmd/teslcam-updater)
  scp -q "$tmp/teslcam-updater" "$HOST:/tmp/teslcam-updater.new"
  scp -q "$key" "$HOST:/tmp/ota-release.pub.pem"
  scp -q "$ROOT/scripts/teslcam-updater.service" "$HOST:/tmp/teslcam-updater.service"
  scp -q "$ROOT/scripts/teslcam-agent-pi4.service" "$HOST:/tmp/teslcam-agent.service"
  run_sudo <<'EOF'
set -euo pipefail
install -m 755 /tmp/teslcam-updater.new /usr/local/bin/teslcam-updater
install -m 644 /tmp/ota-release.pub.pem /etc/teslcam/ota-release.pub.pem
install -m 644 /tmp/teslcam-updater.service /etc/systemd/system/teslcam-updater.service
install -m 644 /tmp/teslcam-agent.service /etc/systemd/system/teslcam-agent.service
rm -f /tmp/teslcam-updater.new /tmp/ota-release.pub.pem /tmp/teslcam-updater.service /tmp/teslcam-agent.service
# Bootstrap a hand-provisioned unit that predates versioned application
# releases. Future deploys and OTA releases replace this legacy slot.
if [[ ! -x /opt/teslcam/current/teslcam-agent && -x /usr/local/bin/teslcam-agent ]]; then
  install -d -m 755 /opt/teslcam/releases/legacy
  install -m 755 /usr/local/bin/teslcam-agent /opt/teslcam/releases/legacy/teslcam-agent
  [[ ! -x /usr/local/bin/teslcam-camera-scorer ]] || install -m 755 /usr/local/bin/teslcam-camera-scorer /opt/teslcam/releases/legacy/teslcam-camera-scorer
  ln -sfn releases/legacy /opt/teslcam/current
fi
systemctl daemon-reload
systemctl enable teslcam-updater
systemctl restart teslcam-agent
systemctl restart teslcam-updater || true
EOF
  say "OTA updater installed"
}

cmd_check() {
  say "checking prerequisites on $HOST"
  "${SSH[@]}" 'set -u; fail=0
    ok()   { printf "  \033[1;32mok\033[0m   %s\n" "$1"; }
    bad()  { printf "  \033[1;31mMISS\033[0m %s\n" "$1"; fail=1; }
    grep -q "^dtoverlay=dwc2" /boot/firmware/config.txt /boot/config.txt 2>/dev/null \
      && ok "dwc2 overlay in config.txt" || bad "dwc2 overlay in config.txt (run setup)"
    [ -n "$(ls -A /sys/class/udc 2>/dev/null)" ] \
      && ok "UDC present ($(ls /sys/class/udc | tr "\n" " "))" || bad "no UDC in /sys/class/udc (rebooted after setup?)"
    lsmod | grep -q "^libcomposite" && ok "libcomposite loaded" || bad "libcomposite not loaded (unit modprobes it; reboot or modprobe)"
    for c in ffmpeg ffprobe btmgmt nmcli mkfs.exfat; do
      command -v "$c" >/dev/null && ok "$c on PATH" || bad "$c missing (run setup)"
    done
    command -v vcgencmd >/dev/null && ok "vcgencmd on PATH" || echo "  warn vcgencmd missing (thermal telemetry degraded; fine on non-RPiOS)"
    systemctl is-active --quiet bluetooth && ok "bluetoothd running" || bad "bluetoothd not running"
    [ -f /var/lib/teslcam/backing.img ] && ok "backing image present" || bad "backing image (run: image <size-gb>)"
    [ -x /opt/teslcam/current/teslcam-camera-scorer ] && ok "camera scorer installed" || bad "camera scorer (run: scorer + deploy)"
    [ -x /opt/teslcam/current/teslcam-agent ] && ok "agent binary installed" || bad "agent binary (run: deploy)"
    [ -f /etc/systemd/system/teslcam-agent.service ] && ok "systemd unit installed" || bad "systemd unit (run: setup)"
    [ -f /etc/teslcam/server.token ] && ok "provisioned (server.token present)" \
      || echo "  info unprovisioned — pair via BLE onboarding from the app"
    exit $fail'
  say "check passed"
}

cmd_status() { "${SSH[@]}" systemctl status teslcam-agent --no-pager; }
cmd_logs() { "${SSH[@]}" journalctl -u teslcam-agent --no-pager -n "${1:-100}"; }

case "${1:-}" in
  setup) cmd_setup ;;
  image) shift; cmd_image "$@" ;;
  scorer) cmd_scorer ;;
  deploy) cmd_deploy ;;
  ota) shift; cmd_ota "$@" ;;
  check) cmd_check ;;
  status) cmd_status ;;
  logs) shift; cmd_logs "$@" ;;
  *) usage ;;
esac
