#!/usr/bin/env bash
# Interactively flash a built teslcam golden image onto an SD card or USB SSD
# on macOS.
#
#   scripts/flash-image.sh [image.img.xz]
#
# With no argument, offers the images found in build/ (newest first). Detects
# connected external physical disks via diskutil, asks which one to flash,
# and requires typing the disk identifier back before erasing anything.
# Internal disks are never listed or accepted.
#
# Flashing is identical for SD cards and USB SSDs; after first boot the unit
# expands its rootfs, creates the exFAT backing image, and waits for BLE
# onboarding (see scripts/build-image.sh).
#
# Writing the card needs root. Run scripts/install-flash-helper.sh once and
# flashing stops asking for a password (see that script for the trade-off);
# otherwise this prompts for sudo before the write.
#
# With TESLCAM_DEV_LINK=1, also arms the USB dev link on the card: the unit
# then exposes an ethernet + serial console alongside the drive, reachable at
# <hostname>.local over the same USB cable, so an agent fix is an scp instead
# of another 20-minute image rebuild. DEBUG only — never for a car-bound unit.
#
# After flashing, offers to provision remote access: generates a per-unit
# WireGuard keypair and writes it to the FAT boot partition, so the unit can
# be reached over LTE despite carrier CGNAT. Needs `brew install
# wireguard-tools`. See docs/remote-access-wireguard.md.
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname)" = "Darwin" ] || die "this script is macOS-only (uses diskutil)"

# xz is the one dependency macOS doesn't ship; the rest are stock but cheap
# to verify (protects against exotic PATHs).
command -v xz >/dev/null || die "xz not found — install with: brew install xz"
for c in diskutil plutil dd perl awk; do
  command -v "$c" >/dev/null || die "$c not found on PATH (expected stock macOS tool)"
done

# wg is only needed for the remote-access step, which runs *after* the write —
# so a missing wg used to surface fifteen minutes in, once the flash was
# already done. Warn here instead, while stopping is still cheap. Not fatal:
# flashing itself does not need WireGuard, and the tunnel can be provisioned
# later by dropping a config on the boot partition (no reflash).
if ! command -v wg >/dev/null; then
  printf '\033[1;33mnote:\033[0m wg not found — remote-access (WireGuard) provisioning will be unavailable.\n'
  printf '      install with: brew install wireguard-tools    (flashing itself works without it)\n\n'
fi

# ------------------------------------------------ non-interactive selection
# A flash that needs three typed answers cannot be part of an automated
# build->flash->healthcheck run. --disk/--yes make the whole thing unattended;
# without them the interactive prompts below are unchanged, including the
# type-the-identifier confirmation that guards a manual flash.
DISK_ARG=""
ASSUME_YES=0
POSITIONAL=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --disk) DISK_ARG="${2:-}"; shift 2 ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    --image) POSITIONAL+=("${2:-}"); shift 2 ;;
    -h|--help) sed -n '2,50p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) die "unknown option: $1" ;;
    *) POSITIONAL+=("$1"); shift ;;
  esac
done
set -- "${POSITIONAL[@]+"${POSITIONAL[@]}"}"
[[ "$ASSUME_YES" == 1 && -z "$DISK_ARG" ]] \
  && die "--yes requires --disk <diskN>: refusing to pick a disk to erase on its own"

# ------------------------------------------------------------- pick an image
IMAGE="${1:-}"
if [[ -z "$IMAGE" && "$ASSUME_YES" == 1 ]]; then
  # Newest first, same order the menu shows.
  IMAGE="$(ls -t "$PROJECT_DIR"/build/teslcam-pi4-*.img.xz 2>/dev/null | head -1)"
  [[ -n "$IMAGE" ]] || die "no images in build/ — run scripts/build-image.sh first"
  say "image (newest): $(basename "$IMAGE")"
fi
if [[ -z "$IMAGE" ]]; then
  IMAGES=()
  while IFS= read -r f; do IMAGES+=("$f"); done \
    < <(ls -t "$PROJECT_DIR"/build/teslcam-pi4-*.img.xz 2>/dev/null)
  [[ ${#IMAGES[@]} -gt 0 ]] \
    || die "no images in build/ — run scripts/build-image.sh first, or pass an image path"
  echo "Images in build/ (newest first):"
  for i in "${!IMAGES[@]}"; do
    printf '  [%d] %s  (%s)\n' "$((i + 1))" "$(basename "${IMAGES[$i]}")" \
      "$(du -h "${IMAGES[$i]}" | cut -f1 | tr -d ' ')"
  done
  read -r -p "Image number [1]: " PICK
  PICK="${PICK:-1}"
  [[ "$PICK" =~ ^[0-9]+$ && "$PICK" -ge 1 && "$PICK" -le ${#IMAGES[@]} ]] \
    || die "invalid selection: $PICK"
  IMAGE="${IMAGES[$((PICK - 1))]}"
fi
[[ -f "$IMAGE" ]] || die "image not found: $IMAGE"
say "image: $IMAGE"

# ------------------------------------------------------------- pick the disk
# `diskutil list external physical` is the obvious enumeration and it is wrong
# here: macOS reports a *built-in* SD card reader as Internal=true, so a card
# in the slot is invisible to it while being plainly visible in Finder.
#
# Internal is therefore not the safety property we want. The discriminator is
# Ejectable: the SD reader is Internal=true/Ejectable=true, while the soldered
# APPLE SSD is Internal=true/Ejectable=false. A candidate must be a whole
# physical disk that is either external or ejectable — which admits SD cards
# and USB SSDs, and excludes the boot SSD, disk images, and APFS synthesized
# devices.
disk_attr() { diskutil info -plist "$1" 2>/dev/null | plutil -extract "$2" raw -o - - 2>/dev/null; }

is_flashable() {
  local d="$1"
  [[ "$(disk_attr "$d" VirtualOrPhysical)" == "Physical" ]] || return 1
  [[ "$(disk_attr "$d" WholeDisk)" == "true" ]] || return 1
  # Never the disk the running system booted from, whatever its flags say.
  local root_whole; root_whole="$(diskutil info -plist / 2>/dev/null | plutil -extract ParentWholeDisk raw -o - - 2>/dev/null)"
  [[ -n "$root_whole" && "$d" == "$root_whole" ]] && return 1
  df / 2>/dev/null | grep -q "^/dev/${d}s" && return 1
  local internal ejectable
  internal="$(disk_attr "$d" Internal)"
  ejectable="$(disk_attr "$d" Ejectable)"
  [[ "$internal" == "false" || "$ejectable" == "true" ]] || return 1
  return 0
}

list_external_disks() {
  local d
  for d in $(diskutil list 2>/dev/null | grep -oE '^/dev/disk[0-9]+' | sed 's|/dev/||'); do
    is_flashable "$d" && printf '%s\n' "$d"
  done
}

# --disk names the target outright. It still goes through is_flashable below,
# so an unattended run can no more erase the boot disk than an interactive one.
if [[ -n "$DISK_ARG" ]]; then
  DISK="${DISK_ARG#/dev/}"
  is_flashable "$DISK" \
    || die "--disk $DISK is not a removable whole disk (or is the boot disk) — refusing"
  say "target: /dev/$DISK — $(diskutil info "$DISK" | awk -F': +' '/Disk Size/ {print $2}' | awk -F' \\(' '{print $1}')"
fi

DISKS=()
while [[ -z "${DISK:-}" ]]; do
  DISKS=()
  while IFS= read -r d; do [[ -n "$d" ]] && DISKS+=("$d"); done < <(list_external_disks)
  [[ ${#DISKS[@]} -gt 0 ]] && break
  read -r -p "No external disks found — insert the SD card / SSD, then press Enter to rescan (q to quit): " R
  [[ "$R" == q* ]] && exit 1
done

if [[ -z "${DISK:-}" ]]; then
echo
echo "External disks:"
for i in "${!DISKS[@]}"; do
  d="${DISKS[$i]}"
  info="$(diskutil info "$d")"
  name="$(echo "$info" | awk -F': +' '/Device \/ Media Name/ {print $2}')"
  size="$(echo "$info" | awk -F': +' '/Disk Size/ {print $2}' | awk -F' \\(' '{print $1}')"
  proto="$(echo "$info" | awk -F': +' '/Protocol/ {print $2}')"
  printf '  [%d] /dev/%s  %s — %s (%s)\n' "$((i + 1))" "$d" "$size" "$name" "$proto"
  diskutil list "$d" | awk 'NR>2 {print "        " $0}'
done
echo
read -r -p "Disk number to FLASH: " PICK
[[ "$PICK" =~ ^[0-9]+$ && "$PICK" -ge 1 && "$PICK" -le ${#DISKS[@]} ]] \
  || die "invalid selection: $PICK"
DISK="${DISKS[$((PICK - 1))]}"
fi

# Re-verify right before erasing: still attached, still a removable target,
# still not the boot disk. Re-running the same predicate (rather than trusting
# the earlier scan) closes the window where a disk is swapped mid-prompt.
disk_attr "$DISK" DeviceIdentifier >/dev/null \
  || die "/dev/$DISK vanished — was it unplugged?"
is_flashable "$DISK" \
  || die "/dev/$DISK is not a removable whole disk (or is the boot disk) — refusing"

echo
printf '\033[1;31mThis will ERASE /dev/%s completely.\033[0m\n' "$DISK"
if [[ "$ASSUME_YES" == 1 ]]; then
  # The typed confirmation exists to catch a mis-picked disk. With --disk the
  # caller named the target explicitly and is_flashable re-vetted it twice, so
  # there is nothing left for the human to disambiguate.
  say "--yes: proceeding without the typed confirmation"
else
  read -r -p "Type the disk identifier ($DISK) to confirm: " CONFIRM
  [[ "$CONFIRM" == "$DISK" ]] || die "confirmation mismatch — nothing was written"
fi

# -------------------------------------------------------------------- flash
# Writing a raw disk needs root on macOS, full stop. Two ways to get there:
# the privileged helper (scripts/install-flash-helper.sh), which is reachable
# without a password and vets its own target, or plain sudo with a prompt.
# The helper is probed rather than assumed so an uninstalled Mac still works.
FLASH_HELPER=/usr/local/libexec/teslcam-flash-write
if [[ -x "$FLASH_HELPER" ]] && sudo -n "$FLASH_HELPER" --check 2>/dev/null; then
  WRITE_CMD=(sudo -n "$FLASH_HELPER" "$DISK")
  say "using the privileged flash helper (no password needed)"
else
  WRITE_CMD=(sudo dd "of=/dev/r$DISK" bs=4m)
  sudo -v   # prompt for the password up front, not mid-pipeline
fi

say "unmounting /dev/$DISK"
diskutil unmountDisk force "/dev/$DISK"

# `status=progress` is a GNU coreutils extension: BSD dd accepts the operand
# and silently emits nothing, so the write used to sit mute for ten-plus
# minutes with no way to tell a slow card from a hung one. Count the bytes in
# the pipe instead. The uncompressed total comes from the xz index (no need to
# decompress twice), and a 1 MiB-block copier is orders of magnitude faster
# than any SD card, so interposing it costs nothing. perl is stock on macOS.
TOTAL="$(xz -l --robot "$IMAGE" | awk '$1 == "totals" {print $5}')"
[[ "$TOTAL" =~ ^[0-9]+$ && "$TOTAL" -gt 0 ]] \
  || die "cannot read the uncompressed size of $IMAGE — is it a valid .xz?"

PROGRESS_PL="$(cat <<'PERL'
use strict; use warnings; use Time::HiRes qw(time);
my $total = shift or die "progress: total-bytes argument required\n";
binmode STDIN; binmode STDOUT;
# Redraw in place on a terminal; fall back to periodic lines when stderr is a
# log or a pipe, where carriage returns would produce one unreadable line.
my $tty = -t STDERR;
my ($done, $start, $last_draw, $last_pct) = (0, time, 0, -1);

sub human { my $b = shift;
  $b >= 2 ** 30 ? sprintf("%.2f GB", $b / 2 ** 30) : sprintf("%.0f MB", $b / 2 ** 20) }

sub draw {
  my $elapsed = time - $start; $elapsed = 0.001 if $elapsed <= 0;
  my $rate = $done / $elapsed;
  my $pct  = $done * 100 / $total; $pct = 100 if $pct > 100;
  my $eta  = ($rate > 0 && $done < $total) ? ($total - $done) / $rate : 0;
  if ($tty) {
    my $width = 32;
    my $fill  = int($width * $pct / 100);
    printf STDERR "\r  [%s%s] %3d%%  %s / %s  %.1f MB/s  ETA %d:%02d ",
      "#" x $fill, "." x ($width - $fill), $pct,
      human($done), human($total), $rate / 2 ** 20, int($eta / 60), int($eta) % 60;
  } else {
    printf STDERR "  %3d%%  %s / %s  %.1f MB/s\n",
      $pct, human($done), human($total), $rate / 2 ** 20;
  }
}

while (1) {
  my $n = sysread(STDIN, my $buf, 1 << 20);
  die "progress: read failed: $!\n" unless defined $n;
  last if $n == 0;
  # sysread/syswrite, not print: a short write to a pipe is normal and must be
  # resumed at the right offset, or the image silently loses bytes.
  my $off = 0;
  while ($off < $n) {
    my $w = syswrite(STDOUT, $buf, $n - $off, $off);
    die "progress: write failed: $!\n" unless defined $w;
    $off += $w;
  }
  $done += $n;
  my $pct = int($done * 100 / $total);
  if ($tty ? time - $last_draw >= 0.25 : $pct >= $last_pct + 5) {
    ($last_draw, $last_pct) = (time, $pct);
    draw();
  }
}
draw();
print STDERR "\n";
PERL
)"

say "flashing $(awk -v b="$TOTAL" 'BEGIN {printf "%.2f GB", b / 2 ^ 30}') to /dev/r$DISK (~5–15 min for an SD card)"
xz -dc "$IMAGE" | perl -e "$PROGRESS_PL" "$TOTAL" | "${WRITE_CMD[@]}"
sync

# ------------------------------------------------ remote access provisioning
# On LTE a unit sits behind the dongle's NAT *and* carrier CGNAT, so it has no
# reachable address and nothing can connect in to it. Reaching a unit in the
# field requires it to dial out to the VPS; we ride back down that tunnel.
# See docs/remote-access-wireguard.md.
#
# Each unit needs its OWN keypair — a key baked into the golden image would be
# shared by every unit, and they would all collide on one tunnel IP. So the
# key is generated here, per flash. macOS cannot write the ext4 rootfs, so the
# config is handed over on the FAT boot partition; teslcam-wg-provision moves
# it to /etc/wireguard on first boot and deletes it from the FAT partition.
WG_SUBNET_PREFIX="10.8.0"
WG_HUB_ENDPOINT="161.35.232.246:51821"
WG_HUB_PUBKEY="623glhgUggvKQPQmkrrzlMdo2c+58EF4GujbUtH1ciw="

# Provisioning is NOT optional and asks no questions: every field unit needs
# the tunnel (CGNAT makes an unprovisioned unit unreachable in the field), and
# the unit number comes from the hub itself — the VPS's peer table is the
# single source of truth for which tunnel IPs are taken. Manual numbering is
# exactly how two units end up fighting over one IP. Skip only with
# TESLCAM_NO_REMOTE_ACCESS=1 (offline bench flash); the unit can be
# provisioned later by dropping teslcam-wg1.conf onto the boot partition.
WANT_WG=y
if [[ "${TESLCAM_NO_REMOTE_ACCESS:-}" == 1 ]]; then
  WANT_WG=n
  say "remote access skipped (TESLCAM_NO_REMOTE_ACCESS=1)"
fi
if [[ "$WANT_WG" == y ]]; then
  command -v wg >/dev/null \
    || die "wg not found — install with: brew install wireguard-tools (or set TESLCAM_NO_REMOTE_ACCESS=1)"

  say "asking the hub for the next free unit number"
  TAKEN="$(ssh -o ConnectTimeout=15 "${TESLCAM_VPS:-adri@vps}" \
    "sudo wg show wg1 allowed-ips" 2>/dev/null | grep -oE "$WG_SUBNET_PREFIX\.[0-9]+" | cut -d. -f4)" \
    || die "cannot reach the hub to pick a unit number — fix connectivity or set TESLCAM_NO_REMOTE_ACCESS=1"
  UNIT_NUM=""
  for n in $(seq 1 253); do
    echo "$TAKEN" | grep -qx "$((n + 1))" || { UNIT_NUM="$n"; break; }
  done
  [[ -n "$UNIT_NUM" ]] || die "no free unit numbers on the hub (all 253 taken?)"
  UNIT_IP="$WG_SUBNET_PREFIX.$((UNIT_NUM + 1))"
  say "unit $UNIT_NUM -> $UNIT_IP (next free on the hub)"

  UNIT_KEY="$(wg genkey)"
  UNIT_PUB="$(printf '%s' "$UNIT_KEY" | wg pubkey)"

  # The boot partition is FAT and mounts on macOS; the rootfs (ext4) does not.
  say "mounting boot partition"
  diskutil mountDisk "/dev/$DISK" >/dev/null
  BOOTVOL=""
  for _ in $(seq 1 20); do
    for v in /Volumes/bootfs /Volumes/boot; do
      [[ -d "$v" ]] && { BOOTVOL="$v"; break 2; }
    done
    sleep 0.5
  done
  [[ -n "$BOOTVOL" ]] || die "boot partition did not mount — cannot provision"

  # umask so the key is not world-readable on the Mac while it is staged here.
  ( umask 077; cat > "$BOOTVOL/teslcam-wg1.conf" <<WGCONF
# teslcam unit $UNIT_NUM remote access. Installed to /etc/wireguard/wg1.conf
# on first boot by teslcam-wg-provision, which then deletes this file.
#
# AllowedIPs is deliberately ONLY the tunnel subnet, never 0.0.0.0/0: a default
# route here would drag all of the unit's traffic over the metered SIM.
[Interface]
Address = $UNIT_IP/24
PrivateKey = $UNIT_KEY

[Peer]
PublicKey = $WG_HUB_PUBKEY
Endpoint = $WG_HUB_ENDPOINT
AllowedIPs = $WG_SUBNET_PREFIX.0/24
PersistentKeepalive = 60
WGCONF
  )
  sync
  say "wrote $BOOTVOL/teslcam-wg1.conf"

  # The hub must know this peer or the handshake is refused. Register
  # immediately — the number was allocated from the hub moments ago, and any
  # delay is a window for a second flash to take the same slot.
  REGISTER_CMD="sudo wg set wg1 peer $UNIT_PUB allowed-ips $UNIT_IP/32 && sudo wg-quick save wg1"
  say "registering unit $UNIT_NUM on the hub"
  if ssh -o ConnectTimeout=15 "${TESLCAM_VPS:-adri@vps}" "$REGISTER_CMD"; then
    say "registered: $UNIT_PUB -> $UNIT_IP"
  else
    # The config is already on the card, so the unit will dial but be refused
    # until this runs. Loud, with the exact command.
    printf '\033[1;31merror:\033[0m hub registration failed — the unit cannot connect until you run:\n' >&2
    echo "  ssh ${TESLCAM_VPS:-adri@vps} '$REGISTER_CMD'" >&2
  fi
fi

# ------------------------------------------------- optional: USB dev link
# Both debug functions are armed by an empty marker file on the FAT boot
# partition, and the card is already in this Mac — so offering it here is the
# difference between a dev unit that is reachable from its very first boot and
# one that costs another card swap the first time something goes wrong.
# Opt-in via TESLCAM_DEV_LINK=1, never by default: the console is passwordless
# root over USB, and both make the gadget composite (see gadget.Config), which
# the car has not been validated against.
if [[ "${TESLCAM_DEV_LINK:-}" == 1 ]]; then
  # Read the baked hostname from the image builder rather than repeating it:
  # the mDNS name the dev link is reached by is exactly that hostname.
  DEV_LINK_HOST="$(awk -F= '/^HOSTNAME_BAKED=/{print $2; exit}' "$PROJECT_DIR/scripts/build-image.sh")"
  [[ -n "$DEV_LINK_HOST" ]] || DEV_LINK_HOST=sentyx
  diskutil mountDisk "/dev/$DISK" >/dev/null 2>&1 || true
  DEVBOOT=""
  for _ in $(seq 1 20); do
    for v in /Volumes/bootfs /Volumes/boot; do
      [[ -d "$v" ]] && { DEVBOOT="$v"; break 2; }
    done
    sleep 0.5
  done
  if [[ -n "$DEVBOOT" ]]; then
    : > "$DEVBOOT/teslcam-gadget-net"
    : > "$DEVBOOT/teslcam-gadget-console"
    sync
    say "armed the USB dev link (ethernet + serial console) on $DEVBOOT"
    printf '  reach it over the USB cable: \033[1mssh adri@%s.local\033[0m\n' "$DEV_LINK_HOST"
    printf '  deploy an agent build:       TESLCAM_PI=adri@%s.local scripts/install-agent.sh deploy\n' "$DEV_LINK_HOST"
    printf '  \033[1;33mremove both files before this unit goes in a car.\033[0m\n'
  else
    printf '\033[1;33mnote:\033[0m boot partition did not mount — dev link not armed.\n'
  fi
fi

say "ejecting"
diskutil eject "/dev/$DISK"

say "done — insert into the Pi and power up."
echo "First boot expands the filesystem and creates the backing image (allow ~2 min),"
echo "then the unit advertises for BLE onboarding from the app."
if [[ "${WANT_WG:-}" == y ]]; then
  echo
  echo "Remote access (only while the car is awake — Tesla cuts USB power on sleep):"
  echo "  ssh -J ${TESLCAM_VPS:-adri@vps} adri@${UNIT_IP}"
  echo "Requires inbound UDP 51821 allowed in the DigitalOcean cloud firewall."
fi
