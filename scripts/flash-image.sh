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
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname)" = "Darwin" ] || die "this script is macOS-only (uses diskutil)"

# xz is the one dependency macOS doesn't ship; the rest are stock but cheap
# to verify (protects against exotic PATHs).
command -v xz >/dev/null || die "xz not found — install with: brew install xz"
for c in diskutil plutil dd; do
  command -v "$c" >/dev/null || die "$c not found on PATH (expected stock macOS tool)"
done

# ------------------------------------------------------------- pick an image
IMAGE="${1:-}"
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
list_external_disks() {
  diskutil list external physical 2>/dev/null \
    | awk -F'[/ ]' '/^\/dev\/disk/ {print $3}'
}

DISKS=()
while true; do
  DISKS=()
  while IFS= read -r d; do [[ -n "$d" ]] && DISKS+=("$d"); done < <(list_external_disks)
  [[ ${#DISKS[@]} -gt 0 ]] && break
  read -r -p "No external disks found — insert the SD card / SSD, then press Enter to rescan (q to quit): " R
  [[ "$R" == q* ]] && exit 1
done

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

# Re-verify right before erasing: still attached, still external.
INTERNAL="$(diskutil info -plist "$DISK" | plutil -extract Internal raw -o - - 2>/dev/null)" \
  || die "/dev/$DISK vanished — was it unplugged?"
[[ "$INTERNAL" == "false" ]] || die "/dev/$DISK looks internal — refusing"

echo
printf '\033[1;31mThis will ERASE /dev/%s completely.\033[0m\n' "$DISK"
read -r -p "Type the disk identifier ($DISK) to confirm: " CONFIRM
[[ "$CONFIRM" == "$DISK" ]] || die "confirmation mismatch — nothing was written"

# -------------------------------------------------------------------- flash
sudo -v   # prompt for the password up front, not mid-pipeline

say "unmounting /dev/$DISK"
diskutil unmountDisk force "/dev/$DISK"

say "flashing (raw device, ~5–15 min for an SD card; progress below)"
xz -dc "$IMAGE" | sudo dd of="/dev/r$DISK" bs=4m status=progress
sync

say "ejecting"
diskutil eject "/dev/$DISK"

say "done — insert into the Pi and power up."
echo "First boot expands the filesystem and creates the backing image (allow ~2 min),"
echo "then the unit advertises for BLE onboarding from the app."
