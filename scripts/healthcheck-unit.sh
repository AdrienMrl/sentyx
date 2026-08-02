#!/usr/bin/env bash
# healthcheck-unit — is a freshly flashed unit actually green, from the outside?
#
#   scripts/healthcheck-unit.sh [options]
#
# Run this on the Mac right after scripts/flash-image.sh, with the Pi powered
# and its USB-C data port plugged into this Mac. It checks the unit the way its
# two real clients do, and nothing else:
#
#   BLE   as the app does — scan for the onboarding service, connect, read
#         DeviceInfo, establish the encrypted Just Works link (that IS pairing),
#         authenticate the session, then drive the Pi's Wi-Fi radio over BLE
#         (status + scan, optionally join a network). Same GATT sequence as
#         sentyx-app/.../data/ble/PiBleSession.kt, implemented in
#         tools/ble-probe/main.swift.
#
#   USB   as the car does — the gadget must enumerate as a mass-storage device,
#         be MBR-partitioned with an exFAT volume, mount, accept files, and
#         return them byte-identical after a remount.
#
# This deliberately needs no network and no SSH: on a first boot there is no
# Wi-Fi yet, which is exactly the state that has to be verified. Once the unit
# is on Wi-Fi, `scripts/teslcam.sh verify <host>` covers the inside view
# (systemd, gadget configfs, wireguard, sshd); --ssh <host> chains it here.
#
# Options
#   --wifi-ssid S [--wifi-psk P]  ask the Pi to actually join a network
#   --expect-ssid S               fail unless S shows up in the Pi's own scan
#   --usb-mb N                    size of the USB write test (default 16)
#   --usb-event                   write the test file as a real SentryClips
#                                 event, exercising the agent's live reader
#                                 (default writes to a hidden dir instead, so a
#                                 provisioned unit does not upload junk)
#   --usb-disk diskN              skip gadget-disk detection, use this disk
#   --ssh HOST                    also run `teslcam.sh verify HOST` at the end
#   --skip-ble | --skip-usb       run only one half
#   --ble-device ID               probe this exact unit (see --list-units); a
#                                 bench with a second unit powered is common,
#                                 and probing whichever answers first reports
#                                 on a device you did not mean
#   --list-units                  list every unit advertising in range, exit
#   --scan-timeout S              BLE scan budget (default 30s)
#   --yes                         never prompt
set -uo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$PROJECT_DIR" || exit 2

# ───────────────────────────────────────────────────────────────── output
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
  BOLD="$(tput bold)"; RST="$(tput sgr0)"; RED="$(tput setaf 1)"; GRN="$(tput setaf 2)"
  YEL="$(tput setaf 3)"; BLU="$(tput setaf 4)"; CYN="$(tput setaf 6)"; GRY="$(tput setaf 8)"
  [ -z "$GRY" ] && GRY="$(tput dim)"
else
  BOLD=""; RST=""; RED=""; GRN=""; YEL=""; BLU=""; CYN=""; GRY=""
fi

PASS=0; FAIL=0; WARN=0
FAILED=(); WARNED=()

phase() { printf '\n%s› %s%s\n' "$BOLD$BLU" "$*" "$RST"; }
ok()    { PASS=$((PASS + 1)); printf '  %s✓%s  %s\n' "$GRN" "$RST" "$*"; }
bad()   { FAIL=$((FAIL + 1)); FAILED+=("$*"); printf '  %s✗%s  %s\n' "$RED" "$RST" "$*"; }
warn()  { WARN=$((WARN + 1)); WARNED+=("$*"); printf '  %s!%s  %s\n' "$YEL" "$RST" "$*"; }
info()  { printf '     %s%s%s\n' "$GRY" "$*" "$RST"; }
die()   { printf '\n%s✗ error:%s %s\n' "$BOLD$RED" "$RST" "$*" >&2; exit 2; }

# ───────────────────────────────────────────────────────────────── options
WIFI_SSID=""; WIFI_PSK=""; EXPECT_SSID=""
USB_MB=16; USB_EVENT=0; USB_DISK=""
SSH_HOST=""; SKIP_BLE=0; SKIP_USB=0; SCAN_TIMEOUT=30; ASSUME_YES=0
BLE_DEVICE=""; LIST_UNITS=0

while [ $# -gt 0 ]; do
  case "$1" in
    --wifi-ssid)    WIFI_SSID="${2:-}"; shift 2 ;;
    --wifi-psk)     WIFI_PSK="${2:-}"; shift 2 ;;
    --expect-ssid)  EXPECT_SSID="${2:-}"; shift 2 ;;
    --usb-mb)       USB_MB="${2:-}"; shift 2 ;;
    --usb-event)    USB_EVENT=1; shift ;;
    --usb-disk)     USB_DISK="${2:-}"; shift 2 ;;
    --ssh)          SSH_HOST="${2:-}"; shift 2 ;;
    --skip-ble)     SKIP_BLE=1; shift ;;
    --skip-usb)     SKIP_USB=1; shift ;;
    --ble-device)   BLE_DEVICE="${2:-}"; shift 2 ;;
    --list-units)   LIST_UNITS=1; shift ;;
    --scan-timeout) SCAN_TIMEOUT="${2:-}"; shift 2 ;;
    --yes|-y)       ASSUME_YES=1; shift ;;
    -h|--help)      sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)              die "unknown option: $1 (try --help)" ;;
  esac
done
[ -n "$WIFI_PSK" ] && [ -z "$WIFI_SSID" ] && die "--wifi-psk without --wifi-ssid"
[[ "$USB_MB" =~ ^[0-9]+$ ]] || die "--usb-mb must be a number of megabytes"

confirm() {
  local q="$1" reply
  [ "$ASSUME_YES" = 1 ] && return 0
  [ -t 0 ] || die "not a terminal and --yes not given; cannot ask: $q"
  printf '  %s?%s %s %s[y/N]%s ' "$CYN" "$RST" "$q" "$GRY" "$RST"
  read -r reply || return 1
  case "$reply" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

printf '\n%s%s healthcheck %s%s%s\n' "$BOLD" "$CYN" "$RST$GRY" "freshly flashed field unit" "$RST"
printf '%s%s%s\n' "$GRY" "$PROJECT_DIR" "$RST"

# ─────────────────────────────────────────────────────────────── preflight
phase "preflight"
[ "$(uname)" = "Darwin" ] || die "macOS only: this checks the unit from a client's point of view (CoreBluetooth + diskutil)"
for c in diskutil plutil shasum dd; do
  command -v "$c" >/dev/null || die "$c not found on PATH (expected stock macOS tool)"
done
ok "macOS host tooling present"

BLE_PROBE=""
if [ "$SKIP_BLE" = 0 ]; then
  command -v swiftc >/dev/null || die "swiftc not found — install Xcode command line tools: xcode-select --install"
  BLE_PROBE="$PROJECT_DIR/build/ble-probe"
  SRC="$PROJECT_DIR/tools/ble-probe/main.swift"
  [ -f "$SRC" ] || die "missing $SRC"
  if [ ! -x "$BLE_PROBE" ] || [ "$SRC" -nt "$BLE_PROBE" ]; then
    mkdir -p "$PROJECT_DIR/build"
    info "building the CoreBluetooth probe…"
    swiftc -O -o "$BLE_PROBE" "$SRC" || die "could not build tools/ble-probe/main.swift"
  fi
  ok "BLE probe built ($(basename "$BLE_PROBE"))"
fi

# Listing is a lookup, not a check: it prints what is on the air and exits, so
# the operator can name a target for the real run.
if [ "$LIST_UNITS" = 1 ]; then
  phase "units advertising in range"
  "$BLE_PROBE" --list --scan-timeout "$SCAN_TIMEOUT" 2>&1 | sed 's/^HC-[A-Z]* //'
  exit 0
fi

# ══════════════════════════════════════════════════════════════ phase: BLE
# Everything here is the app's exact GATT sequence; the probe reports one
# HC-OK/HC-FAIL/HC-WARN line per step and we simply adopt its verdict.
do_ble() {
  phase "BLE onboarding  (as the app does it)"
  info "the Pi advertises only while teslcam-agent is running; give it ~30s after power-up"

  local args=(--scan-timeout "$SCAN_TIMEOUT")
  [ -n "$BLE_DEVICE" ] && args+=(--device "$BLE_DEVICE")
  [ -n "$EXPECT_SSID" ] && args+=(--expect-ssid "$EXPECT_SSID")
  if [ -n "$WIFI_SSID" ]; then
    args+=(--wifi-connect "$WIFI_SSID")
    [ -n "$WIFI_PSK" ] && args+=(--wifi-psk "$WIFI_PSK")
  fi

  local line denied=0
  while IFS= read -r line; do
    case "$line" in
      HC-OK\ *)   ok   "${line#HC-OK }" ;;
      HC-FAIL\ *) bad  "${line#HC-FAIL }"
                  case "$line" in *"Bluetooth permission denied"*) denied=1 ;; esac ;;
      HC-WARN\ *) warn "${line#HC-WARN }" ;;
      HC-INFO\ *) info "${line#HC-INFO }" ;;
      HC-DATA\ *) printf '%s' "${line#HC-DATA }" > "$SUMMARY_JSON" ;;
      *)          [ -n "$line" ] && info "$line" ;;
    esac
  done < <("$BLE_PROBE" "${args[@]}" 2>&1)

  # A denied Bluetooth permission is a host problem, not a unit problem, and it
  # is the single most common reason this phase reports nothing useful.
  [ "$denied" = 1 ] && info "this is a Mac permission, not a unit fault — grant it and re-run"
}

# ══════════════════════════════════════════════════════════════ phase: USB
disk_attr() { diskutil info -plist "$1" 2>/dev/null | plutil -extract "$2" raw -o - - 2>/dev/null; }

# What macOS shows as "MediaName" for a USB mass-storage device is the SCSI
# INQUIRY product string of the LUN, not the USB iProduct descriptor — so the
# agent's "TeslaCam Drive" (idProduct strings) never appears here. internal/gadget
# leaves lun.0/inquiry_string unset, so the kernel default is what both this Mac
# and the car see. Either is a legitimate match; anything else is suspicious.
GADGET_MEDIA_NAMES="File-Stor Gadget|TeslaCam Drive"
GADGET_VOLUME="TESLACAM"

# Whole physical disks that are not this Mac's boot disk. Same safety predicate
# as flash-image.sh: Internal is not the discriminator (a built-in SD reader is
# Internal=true), so a candidate must be external or ejectable.
candidate_disks() {
  local d root_whole
  root_whole="$(disk_attr / ParentWholeDisk)"
  for d in $(diskutil list 2>/dev/null | grep -oE '^/dev/disk[0-9]+' | sed 's|/dev/||'); do
    [ "$(disk_attr "$d" VirtualOrPhysical)" = "Physical" ] || continue
    [ "$(disk_attr "$d" WholeDisk)" = "true" ] || continue
    [ -n "$root_whole" ] && [ "$d" = "$root_whole" ] && continue
    local internal ejectable
    internal="$(disk_attr "$d" Internal)"; ejectable="$(disk_attr "$d" Ejectable)"
    [ "$internal" = "false" ] || [ "$ejectable" = "true" ] || continue
    printf '%s\n' "$d"
  done
}

# The gadget disk: matched on the agent's USB product string, falling back to
# "an external disk carrying an exFAT TESLACAM volume" so an older agent (or a
# renamed gadget) still gets checked, loudly.
find_gadget_disk() {
  local d name
  for d in $(candidate_disks); do
    name="$(disk_attr "$d" MediaName)"
    if printf '%s' "$name" | grep -qE "$GADGET_MEDIA_NAMES"; then printf '%s\n' "$d"; return 0; fi
  done
  for d in $(candidate_disks); do
    if diskutil list "$d" 2>/dev/null | grep -q "$GADGET_VOLUME"; then
      printf '%s\n' "$d"; return 0
    fi
  done
  return 1
}

# The exFAT slice of a whole disk, via diskutil's per-slice info.
find_exfat_slice() {
  local whole="$1" s fs
  for s in $(diskutil list -plist "$whole" 2>/dev/null \
             | plutil -convert json -o - - 2>/dev/null \
             | grep -oE '"DeviceIdentifier":"'"$whole"'s[0-9]+"' \
             | grep -oE "${whole}s[0-9]+"); do
    fs="$(disk_attr "$s" FilesystemName)"
    case "$fs" in *[eE][xX][fF][aA][tT]*) printf '%s\n' "$s"; return 0 ;; esac
  done
  return 1
}

do_usb() {
  phase "USB mass storage  (as the car sees it)"

  local disk
  if [ -n "$USB_DISK" ]; then
    disk="${USB_DISK#/dev/}"
    [ "$(disk_attr "$disk" WholeDisk)" = "true" ] || { bad "--usb-disk $disk is not a whole disk"; return; }
  elif ! disk="$(find_gadget_disk)"; then
    bad "no USB mass-storage gadget on this Mac — the Pi is not presenting a drive"
    info "check: USB-C *data* cable into the Pi's USB-C port, agent running, dwc2 in peripheral mode"
    info "the inside view of the same failure is: scripts/teslcam.sh verify <host>"
    return
  fi

  local media size proto scheme
  media="$(disk_attr "$disk" MediaName)"
  size="$(disk_attr "$disk" TotalSize)"
  proto="$(disk_attr "$disk" BusProtocol)"
  scheme="$(disk_attr "$disk" Content)"

  if printf '%s' "$media" | grep -qE "$GADGET_MEDIA_NAMES"; then
    ok "gadget enumerated as \"$media\" on /dev/$disk ($proto, $((size / 1000 / 1000 / 1000)) GB)"
  else
    warn "using /dev/$disk (\"$media\") — not a known teslcam LUN inquiry string"
  fi

  # The Tesla MCU ignores partitionless "superfloppy" images; MBR is required
  # (see exfat.LocateVolume and the firstboot image builder).
  case "$scheme" in
    FDisk_partition_scheme) ok "MBR partition scheme (the Tesla MCU ignores partitionless images)" ;;
    *)                      bad "partition scheme is $scheme, not MBR — the car would not read this drive" ;;
  esac

  local slice
  if ! slice="$(find_exfat_slice "$disk")"; then
    bad "no exFAT partition on /dev/$disk — firstboot never created the backing image"
    return
  fi
  local vol; vol="$(disk_attr "$slice" VolumeName)"
  if [ "$vol" = "$GADGET_VOLUME" ]; then
    ok "exFAT volume \"$vol\" on /dev/$slice"
  else
    warn "exFAT volume is named \"$vol\", expected \"$GADGET_VOLUME\""
  fi

  local mnt; mnt="$(disk_attr "$slice" MountPoint)"
  if [ -z "$mnt" ]; then
    diskutil mount "$slice" >/dev/null 2>&1 || { bad "macOS could not mount /dev/$slice (dirty or corrupt filesystem?)"; return; }
    mnt="$(disk_attr "$slice" MountPoint)"
  fi
  [ -n "$mnt" ] && [ -d "$mnt" ] || { bad "/dev/$slice mounted but has no mount point"; return; }
  ok "mounted read-write at $mnt"

  # Hard guard before anything writes or deletes: only ever touch a volume that
  # is under /Volumes and lives on the slice we just identified.
  case "$mnt" in
    /Volumes/*) ;;
    *) bad "refusing to write to $mnt — not under /Volumes"; return ;;
  esac

  local free_kb; free_kb="$(df -Pk "$mnt" | awk 'NR==2 {print $4}')"
  if [ -z "$free_kb" ]; then
    warn "could not read free space on $mnt"
  elif [ "$free_kb" -gt $((USB_MB * 1024 * 2)) ]; then
    if [ "$free_kb" -ge $((1024 * 1024)) ]; then
      ok "$((free_kb / 1024 / 1024)) GB free on the drive"
    else
      ok "$((free_kb / 1024)) MB free on the drive"
    fi
  else
    warn "only $((free_kb / 1024)) MB free — not enough headroom for the write test or for Sentry clips"
  fi

  # Where to write. By default a hidden directory: SentryClips is watched live
  # by the agent, and on an already-provisioned unit a fake event there would be
  # uploaded and analysed for real. --usb-event opts into that on purpose.
  local stamp reldir
  stamp="$(date +%Y-%m-%d_%H-%M-%S)"
  if [ "$USB_EVENT" = 1 ]; then
    reldir="TeslaCam/SentryClips/$stamp"
    info "writing a real SentryClips event — the agent's live reader should pick it up"
  else
    reldir=".teslcam-healthcheck/$stamp"
  fi
  local dir="$mnt/$reldir"

  if ! mkdir -p "$dir" 2>/dev/null; then
    bad "cannot create directories on the drive — the LUN is read-only to the host"
    info "a read-only LUN usually means the agent bound mass_storage with ro=1, or the image is missing"
    return
  fi
  ok "created $reldir/ on the drive"

  local file="$dir/front.mp4" t0 t1 secs
  t0="$(date +%s)"
  if ! dd if=/dev/urandom of="$file" bs=1m count="$USB_MB" 2>/dev/null; then
    bad "write of ${USB_MB} MB failed — the drive rejected data"
    rm -rf "$dir" 2>/dev/null
    return
  fi
  sync
  t1="$(date +%s)"; secs=$((t1 - t0)); [ "$secs" -lt 1 ] && secs=1
  ok "wrote ${USB_MB} MB in ${secs}s (~$((USB_MB / secs)) MB/s)"
  [ $((USB_MB / secs)) -lt 2 ] && warn "throughput under 2 MB/s — the car writes ~4 clips at once; check the cable and CPU load"

  # A small sidecar too: real events are one big clip plus tiny JSON/PNG files,
  # and a filesystem that only handles one of those shapes is still broken.
  printf '{"healthcheck":true,"stamp":"%s"}\n' "$stamp" > "$dir/event.json" 2>/dev/null \
    && ok "small sidecar file (event.json) written" \
    || bad "could not write a small file next to the clip"

  local want; want="$(shasum -a 256 "$file" | awk '{print $1}')"

  # Remount before reading back: otherwise the host page cache answers and the
  # test proves nothing about what actually landed on the Pi's backing image.
  if diskutil unmount "$slice" >/dev/null 2>&1 && diskutil mount "$slice" >/dev/null 2>&1; then
    mnt="$(disk_attr "$slice" MountPoint)"
    ok "unmounted and remounted cleanly"
  else
    warn "could not remount /dev/$slice — the readback below may be served from cache"
  fi
  dir="$mnt/$reldir"; file="$dir/front.mp4"

  if [ ! -f "$file" ]; then
    bad "the file is gone after a remount — writes are not reaching the backing image"
    return
  fi
  local got; got="$(shasum -a 256 "$file" | awk '{print $1}')"
  if [ "$got" = "$want" ]; then
    ok "readback is byte-identical after remount (sha256 ${want:0:12}…)"
  else
    bad "readback differs from what was written — exFAT corruption on the LUN"
  fi

  if rm -rf "$dir" 2>/dev/null && sync && [ ! -d "$dir" ]; then
    ok "test data deleted (drive left clean)"
  else
    warn "could not delete $reldir — remove it by hand before the car uses this unit"
  fi
  [ "$USB_EVENT" = 1 ] && [ -z "$SSH_HOST" ] \
    && info "whether the agent *detected* the event is only visible from inside: --ssh <host>"

  if diskutil unmount "$slice" >/dev/null 2>&1; then
    ok "volume unmounted — safe to unplug from this Mac and plug into the car"
  else
    warn "volume still mounted on this Mac; eject it before moving the unit to the car"
  fi
}

# ══════════════════════════════════════════════════════ phase: inside view
do_ssh() {
  phase "inside view  (teslcam.sh verify $SSH_HOST)"
  "$PROJECT_DIR/scripts/teslcam.sh" verify "$SSH_HOST"
  local rc=$?
  # teslcam.sh prints and tallies its own results; only its overall verdict
  # folds into ours, so a failure there cannot be missed here.
  if [ "$rc" = 0 ]; then ok "inside-view checks completed (see their own verdict above)"
  else bad "teslcam.sh verify $SSH_HOST reported failures"; fi
}

# ═══════════════════════════════════════════════════════════════════ run
SUMMARY_JSON="$(mktemp "${TMPDIR:-/tmp}/teslcam-hc.XXXXXX")" || exit 2
trap 'rm -f "$SUMMARY_JSON"' EXIT

[ "$SKIP_BLE" = 0 ] && do_ble
if [ "$SKIP_USB" = 0 ]; then
  if [ "$SKIP_BLE" = 0 ] && [ "$FAIL" -gt 0 ]; then
    info "BLE checks failed; USB is independent, continuing"
  fi
  do_usb
fi
[ -n "$SSH_HOST" ] && do_ssh

# ═══════════════════════════════════════════════════════════════ verdict
phase "verdict"
printf '  %s%d passed%s   %s%d warnings%s   %s%d failed%s\n' \
  "$GRN" "$PASS" "$RST" "$YEL" "$WARN" "$RST" "$RED" "$FAIL" "$RST"
if [ "${#WARNED[@]}" -gt 0 ]; then
  printf '\n  %swarnings%s\n' "$BOLD$YEL" "$RST"
  printf '    · %s\n' "${WARNED[@]}"
fi
if [ "${#FAILED[@]}" -gt 0 ]; then
  printf '\n  %sfailures%s\n' "$BOLD$RED" "$RST"
  printf '    · %s\n' "${FAILED[@]}"
  printf '\n  %sunit is NOT ready%s\n\n' "$BOLD$RED" "$RST"
  exit 1
fi
RAN=""
[ "$SKIP_BLE" = 0 ] && RAN="BLE onboarding"
[ "$SKIP_USB" = 0 ] && RAN="${RAN:+$RAN and }the USB drive"
printf '\n  %sunit is green%s — %s behaved as its clients expect\n' "$BOLD$GRN" "$RST" "$RAN"
{ [ "$SKIP_BLE" = 1 ] || [ "$SKIP_USB" = 1 ]; } && \
  printf '  %snote: this run skipped a phase — a full check needs both%s\n' "$YEL" "$RST"
[ -z "$SSH_HOST" ] && printf '  %snext: onboard it from the app, then scripts/teslcam.sh verify <host>%s\n' "$GRY" "$RST"
printf '\n'
exit 0
