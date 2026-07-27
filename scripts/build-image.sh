#!/usr/bin/env bash
# Build a flashable "golden" SD/SSD image for a teslcam Pi 4 field unit.
#
#   scripts/build-image.sh <backing-gb> <authorized-keys-file>
#
#   backing-gb            size of the sparse exFAT backing image the unit
#                         creates on first boot (e.g. 64). Required — there is
#                         no default.
#   authorized-keys-file  SSH public key(s) baked into the admin user
#                         (e.g. ~/.ssh/id_rsa.pub). Required.
#
# Output: build/teslcam-pi4-<version>.img.xz — flash to an SD card *or* a USB
# SSD with Raspberry Pi Imager or dd; the image is device-agnostic (Pi OS
# boots by PARTUUID, and the Pi 4 EEPROM default boot order tries SD then
# USB). Example:
#   xz -dc build/teslcam-pi4-v1.img.xz | sudo dd of=/dev/rdiskN bs=4m
#
# What the image contains (mirrors scripts/install-agent.sh setup/scorer):
#   - Raspberry Pi OS Lite arm64 (pinned release below), expanded +3 GiB
#   - runtime packages: ffmpeg, exfatprogs, bluez, network-manager
#   - teslcam-agent (cross-compiled from this checkout) + systemd unit, enabled
#   - teslcam-camera-scorer built in-chroot against the image's own OpenCV
#   - dwc2 USB device-mode overlay + dwc2/libcomposite boot modules
#   - admin user "adri": SSH-key-only login (password locked), NOPASSWD sudo
#   - unprovisioned /etc/teslcam/agent.env — BLE onboarding from the app
#     supplies server URL/device ID/token on first pairing; no secrets baked in
#   - first-boot oneshot: creates the sparse MBR+exFAT backing image sized
#     <backing-gb>, unblocks Wi-Fi rfkill
#   - remote-access tunnel support (wireguard-tools + a provisioning unit that
#     installs a per-unit wg1 config dropped on the boot partition by
#     scripts/flash-image.sh); no key is baked in — see
#     docs/remote-access-wireguard.md
#   - boot-time trims: apt/man-db timers off, swap off,
#     NetworkManager-wait-online off (gadget must come up before connectivity)
#
# Runs on the Mac by re-exec'ing itself inside the Lima dev VM (teslcam-dev),
# which has the native arm64 kernel + loop devices needed to chroot into the
# image. The agent binary is cross-compiled on the Mac first (the VM's Debian
# Go is older than go.mod requires).
set -euo pipefail

VM=teslcam-dev
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
INPUTS="$PROJECT_DIR/build/image-inputs"     # host -> VM handoff (mounted path)
OUT_DIR="$PROJECT_DIR/build"

# Pinned base image. Update deliberately: new URL + new sha256 together.
BASE_URL="https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-2026-06-19/2026-06-18-raspios-trixie-arm64-lite.img.xz"
BASE_SHA256="acff736ca7945e3b305f07cda4abdb870910e12634991da69783611756e381b3"

ADMIN_USER=adri
HOSTNAME_BAKED=sentyx
WIFI_REGDOM=US
EXPAND_GB=3          # rootfs growth to fit packages + scorer build

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

BACKING_GB="${1:-}"
KEYFILE="${2:-}"
[[ "$BACKING_GB" =~ ^[0-9]+$ && "$BACKING_GB" -gt 0 ]] || usage
[[ -n "$KEYFILE" ]] || usage

# ---------------------------------------------------------------- host phase
if [ "$(uname)" = "Darwin" ]; then
  [[ -f "$KEYFILE" ]] || die "authorized-keys file not found: $KEYFILE"
  grep -qE '^(ssh|ecdsa)-' "$KEYFILE" || die "$KEYFILE does not look like an SSH public key"

  VERSION="$(git -C "$PROJECT_DIR" describe --always --dirty 2>/dev/null || echo dev)"

  say "cross-compiling teslcam-agent for linux/arm64 (version $VERSION)"
  mkdir -p "$INPUTS"
  (cd "$PROJECT_DIR" && env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-X main.version=$VERSION" \
    -o "$INPUTS/teslcam-agent" ./cmd/teslcam-agent)
  (cd "$PROJECT_DIR" && env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
    go build -trimpath -o "$INPUTS/teslcam-lte" ./cmd/teslcam-lte)
  cp "$KEYFILE" "$INPUTS/authorized_keys"
  echo "$VERSION" > "$INPUTS/version"

  if ! limactl list --format '{{.Name}} {{.Status}}' | grep -q "^$VM Running"; then
    say "starting Lima VM $VM"
    limactl start "$VM"
  fi

  say "re-exec'ing inside $VM"
  exec limactl shell "$VM" -- bash "$PROJECT_DIR/scripts/build-image.sh" "$BACKING_GB" "$KEYFILE"
fi

# ------------------------------------------------------------------ VM phase
[[ -f "$INPUTS/teslcam-agent" && -f "$INPUTS/authorized_keys" ]] \
  || die "missing $INPUTS — run this script from the Mac, it prepares inputs first"
VERSION="$(cat "$INPUTS/version")"

CACHE=/var/cache/teslcam-image
WORK=/var/tmp/teslcam-image-build
IMG="$WORK/teslcam.img"
MNT="$WORK/mnt"
LOOP=""
RESOLV_WAS_LINK=""

cleanup() {
  set +e
  for m in "$MNT/dev/pts" "$MNT/dev" "$MNT/proc" "$MNT/sys" "$MNT/run" \
           "$MNT/boot/firmware" "$MNT"; do
    mountpoint -q "$m" && sudo umount "$m"
  done
  [ -n "$LOOP" ] && sudo losetup -d "$LOOP" 2>/dev/null
}
trap cleanup EXIT

say "ensuring VM build tools"
command -v xz >/dev/null && command -v curl >/dev/null || {
  sudo apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends xz-utils curl
}

say "fetching pinned base image (cached in $CACHE)"
sudo mkdir -p "$CACHE"
BASE_XZ="$CACHE/$(basename "$BASE_URL")"
if [[ ! -f "$BASE_XZ" ]] || ! echo "$BASE_SHA256  $BASE_XZ" | sha256sum -c --quiet 2>/dev/null; then
  sudo curl -fL --retry 3 --max-time 900 -o "$BASE_XZ" "$BASE_URL"
fi
echo "$BASE_SHA256  $BASE_XZ" | sha256sum -c --quiet || die "sha256 mismatch on $BASE_XZ"

say "unpacking and expanding rootfs by ${EXPAND_GB}G"
sudo rm -rf "$WORK"; mkdir -p "$MNT"
xz -dc "$BASE_XZ" > "$IMG"
truncate -s "+${EXPAND_GB}G" "$IMG"
echo ', +' | sudo sfdisk --no-reread -N 2 "$IMG" >/dev/null

LOOP="$(sudo losetup --find --show --partscan "$IMG")"
for _ in $(seq 1 20); do [[ -e "${LOOP}p2" ]] && break; sleep 0.5; done
[[ -e "${LOOP}p2" ]] || die "partition nodes for $LOOP never appeared"
sudo e2fsck -fp "${LOOP}p2" >/dev/null
sudo resize2fs "${LOOP}p2" 2>/dev/null

say "mounting image for chroot"
sudo mount "${LOOP}p2" "$MNT"
sudo mount "${LOOP}p1" "$MNT/boot/firmware"
sudo mount -t proc proc "$MNT/proc"
sudo mount -t sysfs sys "$MNT/sys"
sudo mount --bind /dev "$MNT/dev"
sudo mount --bind /dev/pts "$MNT/dev/pts"
sudo mount -t tmpfs tmpfs "$MNT/run"

# DNS inside the chroot; restore whatever the image shipped afterwards.
if [[ -L "$MNT/etc/resolv.conf" ]]; then
  RESOLV_WAS_LINK="$(readlink "$MNT/etc/resolv.conf")"
  sudo rm "$MNT/etc/resolv.conf"
elif [[ -f "$MNT/etc/resolv.conf" ]]; then
  sudo mv "$MNT/etc/resolv.conf" "$MNT/etc/resolv.conf.build-orig"
fi
echo "nameserver 1.1.1.1" | sudo tee "$MNT/etc/resolv.conf" >/dev/null
# Keep apt from starting services inside the chroot.
printf '#!/bin/sh\nexit 101\n' | sudo tee "$MNT/usr/sbin/policy-rc.d" >/dev/null
sudo chmod +x "$MNT/usr/sbin/policy-rc.d"

say "staging build inputs into image"
sudo install -m 755 "$INPUTS/teslcam-agent" "$MNT/usr/local/bin/teslcam-agent"
sudo install -m 755 "$INPUTS/teslcam-lte" "$MNT/usr/local/bin/teslcam-lte"
sudo install -m 644 "$PROJECT_DIR/scripts/teslcam-agent-pi4.service" \
  "$MNT/etc/systemd/system/teslcam-agent.service"
sudo rm -rf "$MNT/tmp/scorer-src"
sudo cp -r "$PROJECT_DIR/tools/camera-scorer" "$MNT/tmp/scorer-src"
sudo install -m 644 "$INPUTS/authorized_keys" "$MNT/tmp/authorized_keys"

say "customizing in chroot (apt + scorer build; several minutes)"
sudo env BACKING_GB="$BACKING_GB" ADMIN_USER="$ADMIN_USER" \
  HOSTNAME_BAKED="$HOSTNAME_BAKED" WIFI_REGDOM="$WIFI_REGDOM" \
  chroot "$MNT" /bin/bash -s <<'CHROOT'
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

apt-get update -qq
# Runtime set matches install-agent.sh; build tools stay in the image like they
# do on the hand-provisioned Pi (simple, and disk is cheap on a 32G+ card).
#
# pi-bluetooth is NOT in the trixie Lite base image (the bookworm-era images
# the prototype was provisioned from shipped it). It attaches the Pi's
# UART-connected Bluetooth module and brings up hci0; without it there is no
# hci0, so BLE onboarding fails silently and a fresh unit is unreachable.
# Found the hard way on the first field flash (2026-07).
apt-get install -y --no-install-recommends \
  ffmpeg exfatprogs bluez pi-bluetooth network-manager wireguard-tools \
  cmake g++ make libopencv-dev

# Camera scorer, built against this image's own OpenCV so .so versions match.
cmake -S /tmp/scorer-src -B /tmp/scorer-build >/dev/null
cmake --build /tmp/scorer-build -j2
install -m 755 /tmp/scorer-build/teslcam-camera-scorer /usr/local/bin/teslcam-camera-scorer
rm -rf /tmp/scorer-src /tmp/scorer-build

# USB device-mode gadget support (same as install-agent.sh setup).
printf '\n# teslcam: USB device-mode gadget support\ndtoverlay=dwc2,dr_mode=peripheral\n' \
  >> /boot/firmware/config.txt
printf 'disable_splash=1\nboot_delay=0\n' >> /boot/firmware/config.txt
printf 'dwc2\nlibcomposite\n' >> /etc/modules

# Wi-Fi regulatory domain on the kernel cmdline (single-line file).
sed -i "1 s/$/ cfg80211.ieee80211_regdom=${WIFI_REGDOM}/" /boot/firmware/cmdline.txt

# Admin user: SSH-key-only (password stays locked), passwordless sudo.
useradd -m -s /bin/bash "$ADMIN_USER"
for g in adm sudo dialout video plugdev input render netdev gpio i2c spi bluetooth; do
  getent group "$g" >/dev/null && usermod -aG "$g" "$ADMIN_USER"
done
install -d -m 700 -o "$ADMIN_USER" -g "$ADMIN_USER" "/home/$ADMIN_USER/.ssh"
install -m 600 -o "$ADMIN_USER" -g "$ADMIN_USER" /tmp/authorized_keys \
  "/home/$ADMIN_USER/.ssh/authorized_keys"
rm /tmp/authorized_keys
echo "$ADMIN_USER ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/010-teslcam-admin
chmod 440 /etc/sudoers.d/010-teslcam-admin
# A user is baked in, so kill the Pi OS first-boot rename wizard and its
# ssh-login block. Mask, not disable: getty pulls it in statically.
systemctl mask userconfig.service 2>/dev/null || true
rm -f /etc/ssh/sshd_config.d/rename_user.conf

# Persistent journal, bounded. The default ends up volatile on this image
# (journald never created /var/log/journal/<machine-id>/), so every runtime
# log dies with the power — an unbootable or unreachable unit leaves nothing
# to autopsy, and in-car units lose power constantly by design. Size-capped
# so it can never eat the card.
# Radios: Pi OS ships Wi-Fi AND Bluetooth rfkill-soft-blocked until the
# wireless-country wizard unblocks them — a wizard this image skips. The
# firstboot `rfkill unblock wifi` is not enough: it runs before the radio
# devices exist (~6 s in) and never covered Bluetooth, which left field units
# with hci0 present but administratively dead — no BLE onboarding, no way in.
# A udev rule unblocks every rfkill device the moment it appears, every boot.
cat > /etc/udev/rules.d/90-teslcam-rfkill-unblock.rules <<'RFKILL'
ACTION=="add", SUBSYSTEM=="rfkill", RUN+="/usr/sbin/rfkill unblock all"
RFKILL

# NOTE the 99- prefix: Pi OS ships
# /usr/lib/systemd/journald.conf.d/40-rpi-volatile-storage.conf with
# Storage=volatile (SD-wear protection), and journald merges fragments sorted
# by FILENAME across /usr/lib and /etc — a 10- prefix here parses first and
# silently loses to the 40- file. Found after a field unit booted healthy with
# this set to persistent and still wrote no journal.
mkdir -p /etc/systemd/journald.conf.d
cat > /etc/systemd/journald.conf.d/99-teslcam.conf <<'JOURNAL'
[Journal]
Storage=persistent
SystemMaxUse=64M
SystemMaxFileSize=8M
JOURNAL

# --- Black-box boot report on the FAT partition.
# The one place any machine can read without tooling is /boot/firmware, so the
# unit writes its own health summary there: a unit that never shows up on BLE,
# Wi-Fi, or Ethernet is diagnosed by pulling the card and reading a text file
# on any laptop — no debugfs, no ext4, no shell on the unit. Written at boot
# completion and refreshed by timer while running; belt-and-braces alongside
# the persistent journal (and the reason it exists: the journald persistence
# above was once observed not to take effect on real hardware).
cat > /usr/local/sbin/teslcam-boot-report <<'REPORT'
#!/bin/bash
# Write a plain-text health report to the FAT boot partition. Refreshed by
# timer; atomic (tmp + mv) so a power cut never leaves a torn file.
OUT=/boot/firmware/teslcam-boot-report.txt
TMP="$OUT.tmp"
{
  echo "teslcam boot report  $(date -Is)  uptime: $(cut -d' ' -f1 /proc/uptime)s"
  echo "machine-id: $(cat /etc/machine-id 2>/dev/null)"
  echo "system: $(systemctl is-system-running 2>/dev/null)"
  echo
  echo "== failed units =="
  systemctl list-units --failed --no-legend --plain 2>/dev/null || echo "(none)"
  echo
  echo "== network =="
  for i in eth0 wlan0 eth1; do
    ip -brief addr show "$i" 2>/dev/null || echo "$i: absent"
  done
  echo "default route: $(ip route show default 2>/dev/null | head -1)"
  echo "carrier eth0: $(cat /sys/class/net/eth0/carrier 2>/dev/null)"
  echo
  echo "== bluetooth =="
  echo "adapters: $(ls /sys/class/bluetooth 2>/dev/null || echo NONE)"
  rfkill list bluetooth 2>/dev/null
  hciconfig hci0 2>/dev/null | head -3
  echo
  echo "== teslcam-agent =="
  systemctl show teslcam-agent -p ActiveState,SubState,NRestarts,ExecMainStatus --value 2>/dev/null | paste -sd' ' -
  echo
  echo "== gadget =="
  echo "udc: $(ls /sys/class/udc 2>/dev/null || echo NONE)"
  for g in /sys/kernel/config/usb_gadget/*/UDC; do
    [ -f "$g" ] && echo "gadget bound: $(cat "$g")"
  done
  echo
  echo "== wireguard =="
  wg show wg1 latest-handshakes 2>/dev/null || echo "wg1 not up"
  echo
  echo "== power =="
  vcgencmd get_throttled 2>/dev/null
  echo
  echo "== journal: last errors this boot =="
  journalctl -p err -b --no-pager -n 20 -o short-monotonic 2>/dev/null
  echo
  echo "== journal: teslcam-agent tail =="
  journalctl -u teslcam-agent -b --no-pager -n 40 -o short-monotonic 2>/dev/null
} > "$TMP" 2>&1
mv -f "$TMP" "$OUT"
sync -f /boot/firmware 2>/dev/null || sync
REPORT
chmod 755 /usr/local/sbin/teslcam-boot-report

cat > /etc/systemd/system/teslcam-boot-report.service <<'UNIT'
[Unit]
Description=teslcam black-box report to the FAT boot partition
After=multi-user.target teslcam-agent.service
RequiresMountsFor=/boot/firmware

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/teslcam-boot-report

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/teslcam-boot-report.timer <<'UNIT'
[Unit]
Description=refresh the teslcam boot report while running

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min
AccuracySec=30s

[Install]
WantedBy=timers.target
UNIT

# SSH hardening, stated explicitly. Pi OS ships PasswordAuthentication only as
# a comment, and sshd's compiled-in default is *yes* — so leaving it unset
# means password auth is enabled. It is currently unexploitable because the
# admin password is locked, but a single `passwd` would open it, and the
# WireGuard tunnel exposes sshd to the VPS. Units are key-only by construction.
cat > /etc/ssh/sshd_config.d/010-teslcam-hardening.conf <<'SSHD'
# teslcam field units are key-only; see docs/remote-access-wireguard.md
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
SSHD
chmod 644 /etc/ssh/sshd_config.d/010-teslcam-hardening.conf

echo "$HOSTNAME_BAKED" > /etc/hostname
sed -i "s/raspberrypi/$HOSTNAME_BAKED/g" /etc/hosts

# Data + config dirs; unprovisioned agent.env (BLE onboarding fills it in).
mkdir -p /var/lib/teslcam/clips /etc/teslcam
cat > /etc/teslcam/agent.env <<'ENV'
# teslcam agent config (systemd EnvironmentFile), written by BLE onboarding.
# Empty POST_TO = unprovisioned: uploads off, BLE onboarding advertising.
POST_TO=
DEVICE_ID=
ENV
chmod 644 /etc/teslcam/agent.env
echo "$BACKING_GB" > /etc/teslcam/backing-size-gb

# First boot: create the sparse MBR+exFAT backing image (must be MBR — the
# Tesla MCU ignores partitionless "superfloppy" images), unblock Wi-Fi.
cat > /usr/local/sbin/teslcam-firstboot <<'FIRSTBOOT'
#!/usr/bin/env bash
set -euo pipefail
rfkill unblock wifi || true

IMG=/var/lib/teslcam/backing.img
SIZE_GB="$(cat /etc/teslcam/backing-size-gb)"
if [[ ! -e "$IMG" ]]; then
  # Build at a temp path and mv into place at the end: a power cut mid-way
  # must not leave a half-made backing.img that later boots treat as done.
  rm -f "$IMG.partial"
  truncate -s "${SIZE_GB}G" "$IMG.partial"
  echo 'start=2048, type=7' | sfdisk --quiet "$IMG.partial"
  LOOP="$(losetup --find --show --partscan "$IMG.partial")"
  trap 'losetup -d "$LOOP" 2>/dev/null || true' EXIT
  for _ in $(seq 1 20); do [[ -e "${LOOP}p1" ]] && break; sleep 0.5; done
  [[ -e "${LOOP}p1" ]] || { echo "partition node ${LOOP}p1 never appeared" >&2; exit 1; }
  mkfs.exfat -L TESLACAM "${LOOP}p1" >/dev/null
  MNT="$(mktemp -d)"
  mount "${LOOP}p1" "$MNT"
  mkdir -p "$MNT/TeslaCam"
  umount "$MNT"; rmdir "$MNT"
  losetup -d "$LOOP"; trap - EXIT
  sync "$IMG.partial"
  mv "$IMG.partial" "$IMG"
fi
touch /var/lib/teslcam/.firstboot-done
FIRSTBOOT
chmod 755 /usr/local/sbin/teslcam-firstboot

cat > /etc/systemd/system/teslcam-firstboot.service <<'UNIT'
[Unit]
Description=teslcam first-boot provisioning (backing image, rfkill)
ConditionPathExists=!/var/lib/teslcam/.firstboot-done
Before=teslcam-agent.service
After=local-fs.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/teslcam-firstboot
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT

# --- Remote access tunnel (see docs/remote-access-wireguard.md).
# On LTE the unit is behind the dongle's NAT *and* carrier CGNAT, so nothing
# can connect in to it; the Pi must dial out to the VPS and we ride back down
# that tunnel. No key is baked into the image — every unit would otherwise
# share one private key and collide on the same tunnel IP. Instead
# scripts/flash-image.sh drops a per-unit config onto the FAT boot partition
# (the only partition a Mac can write), and this unit installs it.
#
# Runs every boot rather than once, so re-provisioning a unit is just a matter
# of dropping a new teslcam-wg1.conf onto its boot partition — no reflash.
cat > /usr/local/sbin/teslcam-wg-provision <<'WGPROV'
#!/bin/sh
set -eu
SRC=/boot/firmware/teslcam-wg1.conf
DST=/etc/wireguard/wg1.conf
[ -f "$SRC" ] || exit 0
mkdir -p /etc/wireguard
install -m 600 -o root -g root "$SRC" "$DST"
# Remove the private key from the FAT partition: it has no ownership or mode
# bits and is readable by any machine the card is later plugged into.
rm -f "$SRC"
sync
systemctl enable wg-quick@wg1
systemctl restart wg-quick@wg1
echo "teslcam-wg-provision: installed wg1 config, tunnel enabled" | logger -t teslcam-wg-provision
WGPROV
chmod 755 /usr/local/sbin/teslcam-wg-provision
cat > /etc/systemd/system/teslcam-wg-provision.service <<'UNIT'
[Unit]
Description=teslcam remote-access tunnel provisioning (from boot partition)
After=local-fs.target
Before=teslcam-agent.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/teslcam-wg-provision
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT

# --- Metered LTE dongle (optional USB RNDIS stick, ASR-based; see
# hardware/lte-dongle.md). Three invariants keep it from burning data:
#  1. The NM profile gives it NO default route; its default lives in policy
#     table 101, reachable only by sockets bound to eth1's address (which only
#     the agent's -lte-iface fallback dialer does).
#  2. An nftables allowlist on eth1 permits just the teslcam server, the
#     dongle LAN, and its DNS — dropped-counter everything else.
#  3. All periodic system chatter (apt, man-db) is disabled below anyway.
mkdir -p /etc/NetworkManager/system-connections /etc/NetworkManager/conf.d
cat > /etc/NetworkManager/system-connections/lte-dongle.nmconnection <<'NMCONN'
[connection]
id=lte-dongle
uuid=8be34cbe-1d40-4a0e-9438-8c542a1b56a1
type=ethernet
autoconnect-priority=10
autoconnect-retries=0
interface-name=eth1

[ipv4]
method=auto
never-default=true
route1=0.0.0.0/0,192.168.8.1
route1_options=table=101
routing-rule1=priority 30100 from 192.168.8.0/24 table 101

[ipv6]
# Disabled so a carrier router-advertisement cannot install an IPv6 default
# route that would bypass never-default.
method=disabled
NMCONN
chmod 600 /etc/NetworkManager/system-connections/lte-dongle.nmconnection
cat > /etc/NetworkManager/conf.d/teslcam-lte.conf <<'NMCONF'
[main]
# Never auto-generate a "Wired connection" profile for the LTE dongle: that
# profile would DHCP a *default route* onto the metered link.
no-auto-default=eth1
NMCONF

cat > /etc/teslcam/lte-guard.nft <<'NFT'
#!/usr/sbin/nft -f
# Egress allowlist for the metered LTE dongle (eth1). Only the teslcam server
# and the dongle's own LAN (web API, DNS relay, DHCP) may be reached over LTE;
# every other destination is dropped, and nothing may be forwarded out of it.
add table inet lte_guard
delete table inet lte_guard
table inet lte_guard {
  chain output {
    type filter hook output priority 10; policy accept;
    oifname != "eth1" accept
    ip daddr 192.168.8.0/24 accept comment "dongle LAN: web API, DNS relay, DHCP"
    ip daddr 161.35.232.246 tcp dport 443 accept comment "teslcam server"
    ip daddr 161.35.232.246 udp dport 51821 accept comment "wg1 remote access"
    ip daddr { 8.8.8.8, 8.8.4.4 } udp dport 53 accept comment "dongle upstream DNS"
    counter log prefix "lte-guard drop: " drop
  }
  chain forward {
    type filter hook forward priority 10; policy accept;
    oifname "eth1" counter drop comment "no forwarding onto the metered link"
  }
}
NFT
cat > /etc/systemd/system/teslcam-lte-guard.service <<'UNIT'
[Unit]
Description=teslcam LTE egress allowlist (nftables)
Wants=network-pre.target
Before=network-pre.target

[Service]
Type=oneshot
ExecStart=/usr/sbin/nft -f /etc/teslcam/lte-guard.nft
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT

# The ASR dongle can wedge its RNDIS link (rx_errors climb, zero packets pass)
# until its USB device is re-bound — observed on cold boot. Reset only in that
# exact state; an absent or powered-off dongle is left alone.
cat > /usr/local/sbin/teslcam-lte-watchdog <<'WATCHDOG'
#!/bin/sh
IFACE=eth1
GW=192.168.8.1
[ -d "/sys/class/net/$IFACE" ] || exit 0
ping -c 1 -W 2 -I "$IFACE" "$GW" >/dev/null 2>&1 && exit 0
ERR=$(cat "/sys/class/net/$IFACE/statistics/rx_errors")
[ "$ERR" -gt 100 ] || exit 0
USBDEV=$(basename "$(readlink -f "/sys/class/net/$IFACE/device/..")")
[ -e "/sys/bus/usb/devices/$USBDEV" ] || exit 0
echo "lte-watchdog: $IFACE wedged (rx_errors=$ERR), resetting USB $USBDEV" | logger -t teslcam-lte-watchdog
echo "$USBDEV" > /sys/bus/usb/drivers/usb/unbind
sleep 3
echo "$USBDEV" > /sys/bus/usb/drivers/usb/bind 2>/dev/null || true
WATCHDOG
chmod 755 /usr/local/sbin/teslcam-lte-watchdog
cat > /etc/systemd/system/teslcam-lte-watchdog.service <<'UNIT'
[Unit]
Description=teslcam LTE dongle RNDIS wedge watchdog

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/teslcam-lte-watchdog
UNIT
cat > /etc/systemd/system/teslcam-lte-watchdog.timer <<'UNIT'
[Unit]
Description=Periodic LTE dongle wedge check

[Timer]
OnBootSec=90
OnUnitActiveSec=2min

[Install]
WantedBy=timers.target
UNIT

systemctl enable ssh bluetooth teslcam-firstboot teslcam-agent \
  teslcam-lte-guard teslcam-lte-watchdog.timer teslcam-wg-provision \
  teslcam-boot-report.service teslcam-boot-report.timer

# Boot-time trims. NetworkManager-wait-online would stall boot on the (usual)
# no-connectivity cold start in the car; the agent spools offline anyway.
systemctl disable NetworkManager-wait-online.service 2>/dev/null || true
for u in apt-daily.timer apt-daily-upgrade.timer man-db.timer dphys-swapfile.service; do
  systemctl disable "$u" 2>/dev/null || true
done

apt-get clean
rm -rf /var/lib/apt/lists/*
CHROOT

say "tearing down chroot"
sudo rm -f "$MNT/usr/sbin/policy-rc.d"
sudo rm -f "$MNT/etc/resolv.conf"
if [[ -n "$RESOLV_WAS_LINK" ]]; then
  sudo ln -s "$RESOLV_WAS_LINK" "$MNT/etc/resolv.conf"
elif [[ -f "$MNT/etc/resolv.conf.build-orig" ]]; then
  sudo mv "$MNT/etc/resolv.conf.build-orig" "$MNT/etc/resolv.conf"
fi
cleanup
trap - EXIT

OUT="$OUT_DIR/teslcam-pi4-$VERSION.img.xz"
say "compressing to $OUT"
xz -T0 -6 -c "$IMG" > "$OUT"
sudo rm -rf "$WORK"

say "done: $OUT"
echo "flash (SD or USB SSD, identical):"
echo "  xz -dc '$OUT' | sudo dd of=/dev/rdiskN bs=4m   # or use Raspberry Pi Imager"
echo "first boot creates the ${BACKING_GB}G backing image; pair via BLE onboarding."
