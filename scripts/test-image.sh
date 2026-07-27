#!/usr/bin/env bash
# Smoke-test a built golden image without a Raspberry Pi, inside the Lima dev
# VM (teslcam-dev):
#
#   scripts/test-image.sh [image.img.xz]     default: newest build/teslcam-pi4-*
#
# What it covers (on a scratch copy — the .img.xz is never modified):
#   1. first-boot provisioning, run for real in a chroot with the VM's loop
#      devices: sparse MBR+exFAT backing image, TeslaCam/ dir, atomic
#      completion marker
#   2. a full userspace boot of the image via systemd-nspawn: systemd reaches
#      running/degraded, ssh + NetworkManager up, teslcam-agent enabled and
#      attempting start, first-boot unit skipped once provisioned, no
#      unexpected failed units
#
# What it can NOT cover (needs the real Pi 4): the Pi firmware boot chain,
# EEPROM SD/USB boot order, dwc2 gadget hardware, BLE radio, and the car
# itself. The gadget datapath has its own hardware-free harness
# (scripts/vm-agent-test.sh, dummy_hcd).
set -euo pipefail

VM=teslcam-dev
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

IMAGE="${1:-}"
if [[ -z "$IMAGE" ]]; then
  IMAGE="$(ls -t "$PROJECT_DIR"/build/teslcam-pi4-*.img.xz 2>/dev/null | head -1)"
  [[ -n "$IMAGE" ]] || die "no images in build/ — run scripts/build-image.sh first"
fi
[[ -f "$IMAGE" ]] || die "image not found: $IMAGE"

if [ "$(uname)" = "Darwin" ]; then
  if ! limactl list --format '{{.Name}} {{.Status}}' | grep -q "^$VM Running"; then
    say "starting Lima VM $VM"
    limactl start "$VM"
  fi
  exec limactl shell "$VM" -- sudo bash "$PROJECT_DIR/scripts/test-image.sh" "$IMAGE"
fi

[[ "$(id -u)" == 0 ]] || die "VM phase must run as root"
say "image under test: $IMAGE"

WORK=/var/tmp/teslcam-image-test
IMG="$WORK/scratch.img"
MNT="$WORK/mnt"
MACHINE=pi-smoke
LOOP=""
FAIL=0
ok()  { printf '  \033[1;32mok\033[0m   %s\n' "$1"; }
bad() { printf '  \033[1;31mFAIL\033[0m %s\n' "$1"; FAIL=1; }

# Stop by unit, not machine name: a container that dies (or gets killed)
# before registering with machined is invisible to machinectl but still
# holds the transient unit.
stop_container() {
  systemctl stop "$MACHINE-run" 2>/dev/null
  for _ in $(seq 1 30); do
    systemctl is-active --quiet "$MACHINE-run" 2>/dev/null || break
    sleep 1
  done
  systemctl reset-failed "$MACHINE-run" 2>/dev/null
  return 0
}

cleanup() {
  set +e
  stop_container
  for m in "$MNT/dev/pts" "$MNT/dev" "$MNT/proc" "$MNT/sys" "$MNT/boot/firmware" "$MNT"; do
    mountpoint -q "$m" && umount "$m"
  done
  [ -n "$LOOP" ] && losetup -d "$LOOP" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

command -v systemd-nspawn >/dev/null || {
  say "installing systemd-container"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd-container >/dev/null
}

say "unpacking scratch copy"
rm -rf "$WORK"; mkdir -p "$MNT"
xz -dc "$IMAGE" > "$IMG"
LOOP="$(losetup --find --show --partscan "$IMG")"
for _ in $(seq 1 20); do [[ -e "${LOOP}p2" ]] && break; sleep 0.5; done
[[ -e "${LOOP}p2" ]] || die "partition nodes for $LOOP never appeared"
mount "${LOOP}p2" "$MNT"
mount "${LOOP}p1" "$MNT/boot/firmware"

say "test 1: first-boot provisioning (chroot, real loop devices)"
mount --bind /dev "$MNT/dev"
mount -t proc proc "$MNT/proc"
mount -t sysfs sys "$MNT/sys"
if chroot "$MNT" /usr/local/sbin/teslcam-firstboot; then
  ok "first-boot script exited 0"
else
  bad "first-boot script failed"
fi
B="$MNT/var/lib/teslcam/backing.img"
[[ -f "$B" ]] && ok "backing.img created" || bad "backing.img missing"
[[ -f "$MNT/var/lib/teslcam/.firstboot-done" ]] && ok "completion marker" || bad "completion marker missing"
[[ ! -e "$B.partial" ]] && ok "no partial left behind" || bad "backing.img.partial left behind"
sfdisk -d "$B" 2>/dev/null | grep -q 'type=7' && ok "MBR partition type 7" || bad "MBR partition type"
BL="$(losetup --find --show --partscan "$B")"
for _ in $(seq 1 20); do [[ -e "${BL}p1" ]] && break; sleep 0.5; done
BM="$(mktemp -d)"
if mount "${BL}p1" "$BM" 2>/dev/null; then
  [[ -d "$BM/TeslaCam" ]] && ok "exFAT mounts, TeslaCam/ present" || bad "TeslaCam/ missing"
  umount "$BM"
else
  bad "backing exFAT does not mount"
fi
rmdir "$BM"; losetup -d "$BL"
umount "$MNT/dev" "$MNT/proc" "$MNT/sys"

say "test 2: userspace boot via systemd-nspawn"
( set +e; stop_container )   # clear any stale unit from an interrupted run
systemd-run --unit="$MACHINE-run" --property=Delegate=yes -- \
  systemd-nspawn -D "$MNT" -M "$MACHINE" -b -n
STATE=""
for _ in $(seq 1 60); do
  STATE="$(systemctl -M "$MACHINE" is-system-running 2>/dev/null || true)"
  case "$STATE" in running|degraded) break ;; esac
  sleep 2
done
case "$STATE" in
  running|degraded) ok "system booted ($STATE)" ;;
  *) bad "system never reached running/degraded (last: ${STATE:-unreachable})" ;;
esac

chk_active()  { [[ "$(systemctl -M "$MACHINE" is-active "$1" 2>/dev/null)" == "$2" ]] \
                  && ok "$1 $2" || bad "$1 not $2 ($(systemctl -M "$MACHINE" is-active "$1" 2>/dev/null))"; }
chk_enabled() { [[ "$(systemctl -M "$MACHINE" is-enabled "$1" 2>/dev/null)" == "$2" ]] \
                  && ok "$1 $2" || bad "$1 not $2"; }
chk_active ssh active
chk_active NetworkManager active
chk_enabled teslcam-agent enabled
# No UDC/modules in a container: the agent must be trying (activating) — or
# conceivably up, but never absent.
AG="$(systemctl -M "$MACHINE" is-active teslcam-agent 2>/dev/null || true)"
[[ "$AG" == activating || "$AG" == active ]] \
  && ok "teslcam-agent attempting start ($AG)" || bad "teslcam-agent state: $AG"
# Provisioned marker present -> the unit must skip, not run or fail.
FB="$(systemctl -M "$MACHINE" is-active teslcam-firstboot 2>/dev/null || true)"
[[ "$FB" == inactive ]] && ok "teslcam-firstboot skipped (condition)" || bad "teslcam-firstboot state: $FB"
chk_enabled userconfig.service masked

# Persistent journal must actually engage: Storage=persistent was once
# verified present in an image whose journald still never wrote a byte to
# /var/log/journal on real hardware. The config existing is not the property
# we care about — the directory being created and written at boot is.
JDIR="$(ls -d "$MNT/var/log/journal/"*/ 2>/dev/null | head -1 || true)"
if [[ -n "$JDIR" ]] && ls "$JDIR"*.journal >/dev/null 2>&1; then
  ok "journald persisting to /var/log/journal/$(basename "$JDIR")"
else
  bad "journald NOT persisting (no machine-id dir with .journal files under /var/log/journal)"
fi

# The black-box report must land on the FAT partition during boot.
if [[ -f "$MNT/boot/firmware/teslcam-boot-report.txt" ]]; then
  ok "boot report written to FAT partition"
  grep -q "== bluetooth ==" "$MNT/boot/firmware/teslcam-boot-report.txt" \
    && ok "boot report has expected sections" || bad "boot report malformed"
else
  bad "teslcam-boot-report.txt missing from /boot/firmware after boot"
fi

# Anything failed beyond the known container artifacts is a real problem.
ALLOW='rpi-eeprom-update|systemd-growfs-root|systemd-remount-fs'
UNEXPECTED="$(systemctl -M "$MACHINE" list-units --failed --no-legend --plain 2>/dev/null \
  | awk '{print $1}' | grep -Ev "^($ALLOW)\.service$" || true)"
[[ -z "$UNEXPECTED" ]] && ok "no unexpected failed units" \
  || bad "unexpected failed units: $(echo "$UNEXPECTED" | tr '\n' ' ')"

if [[ "$FAIL" == 0 ]]; then
  say "PASS — image looks boot-ready (Pi-hardware paths untested; see header)"
else
  say "FAIL — see items above"
fi
exit "$FAIL"
