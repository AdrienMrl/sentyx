#!/usr/bin/env bash
# teslcam — one command for the whole field-unit lifecycle.
#
#   scripts/teslcam.sh                  interactive menu
#   scripts/teslcam.sh doctor           environment preconditions only
#   scripts/teslcam.sh audit            static audit of the newest image
#   scripts/teslcam.sh validate         audit + boot + datapath (no hardware)
#   scripts/teslcam.sh build            build a golden image
#   scripts/teslcam.sh pipeline         build -> validate -> flash -> verify
#   scripts/teslcam.sh flash            flash the newest image to removable media
#   scripts/teslcam.sh healthcheck      first-boot check over BLE + USB (no network)
#   scripts/teslcam.sh unit [diskN]     flash -> wait for boot -> healthcheck, unattended
#   scripts/teslcam.sh verify [host]    health-check a flashed unit over SSH
#
# Options: --yes (never prompt; CI), --no-color, --plain (no cursor control),
#          --host <h> (default target for verify), --image <path>
#
# Everything hardware-free runs in the Lima VM (teslcam-dev). What still needs
# the real board — Pi firmware boot chain, EEPROM SD/USB order, dwc2 silicon,
# the BLE radio, and the car — is covered after flashing: `healthcheck` from
# this Mac over BLE and USB (no network needed, so it works on a first boot),
# then `verify` over SSH once the unit is on Wi-Fi.
set -uo pipefail

VM=teslcam-dev
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$PROJECT_DIR" || exit 2

# ═════════════════════════════════════════════════ terminal capability layer
# Everything visual goes through tput so the script degrades cleanly on a dumb
# terminal, in a pipe, or under CI. No hardcoded escape sequences.
TP() { command tput "$@" 2>/dev/null; }

USE_COLOR=1; USE_CURSOR=1; UNICODE=1
[ -t 1 ] || { USE_COLOR=0; USE_CURSOR=0; }
case "${TERM:-dumb}" in dumb|"") USE_COLOR=0; USE_CURSOR=0 ;; esac
[ -n "${NO_COLOR:-}" ] && USE_COLOR=0
case "$(locale charmap 2>/dev/null)" in *UTF-8*|*utf8*) ;; *) UNICODE=0 ;; esac

init_caps() {
  if [ "$USE_COLOR" = 1 ] && [ "$(TP colors || echo 0)" -ge 8 ]; then
    BOLD="$(TP bold)"; DIM="$(TP dim)"; RST="$(TP sgr0)"
    RED="$(TP setaf 1)"; GRN="$(TP setaf 2)"; YEL="$(TP setaf 3)"
    BLU="$(TP setaf 4)"; CYN="$(TP setaf 6)"; GRY="$(TP setaf 8)"
    [ -z "$GRY" ] && GRY="$DIM"
  else
    BOLD=""; DIM=""; RST=""; RED=""; GRN=""; YEL=""; BLU=""; CYN=""; GRY=""
  fi
  if [ "$USE_CURSOR" = 1 ]; then
    EL="$(TP el)"; CUU1="$(TP cuu1)"; CIVIS="$(TP civis)"; CNORM="$(TP cnorm)"
    SC="$(TP sc)"; RC="$(TP rc)"
  else
    EL=""; CUU1=""; CIVIS=""; CNORM=""; SC=""; RC=""
  fi
  COLS="$(TP cols || echo 80)"; [ -z "$COLS" ] && COLS=80
  [ "$COLS" -lt 40 ] && COLS=40
  if [ "$UNICODE" = 1 ]; then
    G_OK="✓"; G_NO="✗"; G_WARN="!"; G_ARROW="›"; G_HR="─"
    SPIN='⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏'
  else
    G_OK="ok"; G_NO="XX"; G_WARN="!!"; G_ARROW=">"; G_HR="-"
    SPIN='| / - \'
  fi
}
init_caps
trap 'printf "%s" "$CNORM"' EXIT

hr() { local w=$((COLS - 2)) i out=""; for i in $(seq 1 "$w"); do out="$out$G_HR"; done; printf '%s%s%s\n' "$GRY" "$out" "$RST"; }
# Truncate to the terminal width so long paths never wrap and corrupt a redraw.
# Width is clamped: a long label can drive the computed budget negative, and
# bash errors on a negative substring expression rather than truncating.
fit() {
  local s="$1" w="${2:-$((COLS - 8))}"
  [ "$w" -lt 8 ] && w=8
  [ "${#s}" -le "$w" ] && printf '%s' "$s" || printf '%s…' "${s:0:$((w - 1))}"
}

banner() {
  printf '\n%s%s teslcam %s%s%s\n' "$BOLD" "$CYN" "$RST$GRY" "field-unit lifecycle" "$RST"
  printf '%s%s%s\n' "$GRY" "$(fit "$PROJECT_DIR")" "$RST"
  hr
}
phase() { printf '\n%s%s %s%s\n' "$BOLD$BLU" "$G_ARROW" "$*" "$RST"; }
info()  { printf '     %s%s%s\n' "$GRY" "$(fit "$*")" "$RST"; }
die()   { printf '\n%s%s error:%s %s\n' "$BOLD$RED" "$G_NO" "$RST" "$*" >&2; exit 2; }

# ═══════════════════════════════════════════════════════════ result tallying
PASS=0; FAIL=0; WARN=0
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/teslcam-run.XXXXXX")" || exit 2
LOG_DIR="$STATE_DIR/logs"; mkdir -p "$LOG_DIR"
FAILED_LOG="$STATE_DIR/failed"; : > "$FAILED_LOG"
WARNED_LOG="$STATE_DIR/warned"; : > "$WARNED_LOG"
KEEP_LOGS=0
cleanup_state() {
  printf '%s' "$CNORM"
  if [ "$KEEP_LOGS" = 1 ]; then
    printf '\n%slogs kept: %s%s\n' "$GRY" "$LOG_DIR" "$RST"
  else
    rm -rf "$STATE_DIR"
  fi
}
trap cleanup_state EXIT

ok()   { PASS=$((PASS + 1)); printf '  %s%s%s  %s\n' "$GRN" "$G_OK" "$RST" "$(fit "$1")"; }
bad()  { FAIL=$((FAIL + 1)); KEEP_LOGS=1; printf '%s\n' "$1" >> "$FAILED_LOG"
         printf '  %s%s%s  %s\n' "$RED" "$G_NO" "$RST" "$(fit "$1")"; }
warn() { WARN=$((WARN + 1)); printf '%s\n' "$1" >> "$WARNED_LOG"
         printf '  %s%s%s  %s\n' "$YEL" "$G_WARN" "$RST" "$(fit "$1")"; }

# ═══════════════════════════════════════════════════════════════ interaction
ASSUME_YES=0
DEFAULT_HOST=""
IMAGE_OVERRIDE=""

confirm() {
  local q="$1" reply
  if [ "$ASSUME_YES" = 1 ]; then printf '  %s?%s %s %s(--yes)%s\n' "$CYN" "$RST" "$q" "$GRY" "$RST"; return 0; fi
  [ -t 0 ] || die "not a terminal and --yes not given; cannot ask: $q"
  local i
  for i in $(seq 1 5); do
    printf '  %s?%s %s %s[y/n]%s ' "$CYN" "$RST" "$q" "$GRY" "$RST"
    read -r reply || die "input closed"
    case "$reply" in
      y|Y|yes|YES) return 0 ;;
      n|N|no|NO)   return 1 ;;
      *) printf '     %sanswer y or n%s\n' "$YEL" "$RST" ;;
    esac
  done
  die "no valid answer"
}

# Required value. Never substitutes a silent default.
prompt_value() {
  local q="$1" reply i
  [ -t 0 ] || die "not a terminal; cannot prompt for: $q"
  for i in $(seq 1 5); do
    printf '  %s?%s %s: ' "$CYN" "$RST" "$q" >&2
    read -r reply || die "input closed"
    [ -n "$reply" ] && { printf '%s' "$reply"; return 0; }
    printf '     %sa value is required%s\n' "$YEL" "$RST" >&2
  done
  die "no value provided"
}

# Arrow-key menu with numeric fallback. Sets MENU_CHOICE to a 1-based index.
MENU_CHOICE=0
if [ "${BASH_VERSINFO[0]:-3}" -ge 4 ]; then ESC_TIMEOUT="0.05"; else ESC_TIMEOUT="1"; fi
menu() {
  local title="$1"; shift
  local items=("$@") n="$#" sel=1 key rest i
  if [ ! -t 0 ] || [ "$USE_CURSOR" = 0 ]; then
    printf '\n%s%s%s\n' "$BOLD" "$title" "$RST"
    for i in $(seq 1 "$n"); do printf '  %2d) %s\n' "$i" "${items[$((i - 1))]}"; done
    local reply; reply="$(prompt_value 'choice')"
    case "$reply" in ''|*[!0-9]*) die "invalid choice: $reply" ;; esac
    [ "$reply" -ge 1 ] && [ "$reply" -le "$n" ] || die "choice out of range: $reply"
    MENU_CHOICE="$reply"; return 0
  fi

  printf '\n%s%s%s %s(↑/↓ or 1-%d, enter to select, q to quit)%s\n' "$BOLD" "$title" "$RST" "$GRY" "$n" "$RST"
  printf '%s' "$CIVIS"
  local drawn=0
  while :; do
    [ "$drawn" = 1 ] && for i in $(seq 1 "$n"); do printf '%s' "$CUU1"; done
    for i in $(seq 1 "$n"); do
      if [ "$i" = "$sel" ]; then
        printf '\r%s  %s%s %s%s\n' "$EL" "$BOLD$CYN" "$G_ARROW" "${items[$((i - 1))]}" "$RST"
      else
        printf '\r%s    %s%s%s\n' "$EL" "$GRY" "${items[$((i - 1))]}" "$RST"
      fi
    done
    drawn=1
    IFS= read -rsn1 key || { printf '%s' "$CNORM"; die "input closed"; }
    case "$key" in
      $'\e')
        # bash 3.2 (stock on macOS) rejects a fractional read timeout, so the
        # escape-sequence tail is read with a 1s cap there instead. Arrow keys
        # deliver all three bytes at once and return immediately either way;
        # only a bare ESC keypress waits.
        IFS= read -rsn2 -t "$ESC_TIMEOUT" rest || rest=""
        case "$rest" in
          '[A') sel=$((sel > 1 ? sel - 1 : n)) ;;
          '[B') sel=$((sel < n ? sel + 1 : 1)) ;;
        esac ;;
      k) sel=$((sel > 1 ? sel - 1 : n)) ;;
      j) sel=$((sel < n ? sel + 1 : 1)) ;;
      q|Q) printf '%s' "$CNORM"; MENU_CHOICE=0; return 1 ;;
      ''|$'\n') printf '%s' "$CNORM"; MENU_CHOICE="$sel"; return 0 ;;
      [1-9]) [ "$key" -le "$n" ] && { sel="$key"; printf '%s' "$CNORM"; MENU_CHOICE="$sel"; return 0; } ;;
    esac
  done
}

# ══════════════════════════════════════════════════════════ step runner
# Runs a command with a live spinner and a hard timeout, logging to a file.
# The timeout is what keeps a wedged VM or a hung ssh from stalling the run.
STEP_N=0
run_step() {
  local label="$1" timeout_s="$2"; shift 2
  STEP_N=$((STEP_N + 1))
  local log="$LOG_DIR/$(printf '%02d' "$STEP_N").log"
  local start; start="$(date +%s)"

  [ "$USE_CURSOR" = 0 ] && printf '  ..  %s\n' "$label"
  # stdin from /dev/null: the step runs in the background, and a child that
  # tried to read the terminal would be stopped with SIGTTIN and then killed
  # by the timeout, which reads as a hang rather than a prompt.
  "$@" < /dev/null > "$log" 2>&1 &
  local pid=$! frames i=0 rc=0 timed_out=0 last=""
  # shellcheck disable=SC2206
  frames=($SPIN)
  local nframes="${#frames[@]}" max_ticks=$((timeout_s * 10))

  printf '%s' "$CIVIS"
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$i" -ge "$max_ticks" ]; then
      kill -TERM "$pid" 2>/dev/null
      local k
      for k in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
      kill -KILL "$pid" 2>/dev/null
      timed_out=1
      break
    fi
    if [ "$USE_CURSOR" = 1 ]; then
      # The spinner advances every tick, but the log tail is re-read only
      # every 5th (0.5s): a multi-minute step would otherwise fork `tail`
      # thousands of times for output nobody can read that fast anyway.
      if [ $((i % 5)) -eq 0 ]; then
        last="$(tail -1 "$log" 2>/dev/null | tr -d '\r' | tr -dc '[:print:]')"
      fi
      printf '\r%s  %s%s%s  %s %s%s%s' "$EL" "$CYN" "${frames[$((i % nframes))]}" "$RST" \
        "$label" "$GRY" "$(fit "$last" $((COLS - ${#label} - 12)))" "$RST"
    fi
    sleep 0.1
    i=$((i + 1))
  done
  wait "$pid" 2>/dev/null; rc=$?
  printf '%s' "$CNORM"
  [ "$USE_CURSOR" = 1 ] && printf '\r%s' "$EL"

  local elapsed=$(( $(date +%s) - start ))
  if [ "$timed_out" = 1 ]; then
    bad "$label — timed out after ${timeout_s}s"
    KEEP_LOGS=1; info "log: $log"
    return 1
  elif [ "$rc" -eq 0 ]; then
    printf '  %s%s%s  %s %s(%ds)%s\n' "$GRN" "$G_OK" "$RST" "$label" "$GRY" "$elapsed" "$RST"
    PASS=$((PASS + 1))
    return 0
  else
    bad "$label — exit $rc"
    KEEP_LOGS=1
    printf '%s' "$GRY"; tail -12 "$log" 2>/dev/null | sed 's/^/       /'; printf '%s' "$RST"
    info "full log: $log"
    return 1
  fi
}

# ═══════════════════════════════════════════════════════════════ VM plumbing
in_vm() { limactl shell --workdir "$PROJECT_DIR" "$VM" -- sudo bash -s -- "$@"; }
vm_running() { [ "$(limactl list --format '{{.Name}} {{.Status}}' 2>/dev/null | awk -v v="$VM" '$1 == v {print $2}')" = "Running" ]; }

newest_image() {
  [ -n "$IMAGE_OVERRIDE" ] && { printf '%s' "$IMAGE_OVERRIDE"; return 0; }
  ls -t "$PROJECT_DIR"/build/teslcam-pi4-*.img.xz 2>/dev/null | head -1
}

# ═══════════════════════════════════════════════════════════ phase: doctor
GIT_HASH=""
do_doctor() {
  phase "environment"
  [ "$(uname)" = "Darwin" ] || die "run from the Mac; VM phases re-exec into Lima themselves"

  local c missing=0
  for c in limactl git xz; do
    command -v "$c" >/dev/null || { bad "$c not found on PATH"; missing=1; }
  done
  [ "$missing" = 0 ] && ok "host tools (limactl, git, xz)"

  local status
  status="$(limactl list --format '{{.Name}} {{.Status}}' 2>/dev/null | awk -v v="$VM" '$1 == v {print $2}')"
  case "$status" in
    Running) ok "Lima VM $VM running" ;;
    "") bad "Lima VM $VM does not exist"; info "create it: limactl start ./teslcam-dev.yaml"; return 1 ;;
    *)
      warn "Lima VM $VM is $status"
      if confirm "start it now?"; then
        run_step "starting $VM" 300 limactl start "$VM" || return 1
      else
        bad "the VM is required for every hardware-free phase"; return 1
      fi ;;
  esac

  # Each image phase unpacks ~4.5 GB into /var/tmp on a 15 GiB disk. Leftovers
  # from an interrupted run wedge the VM with an I/O error that reads nothing
  # like "out of space".
  local stale free_mb
  stale="$(limactl shell "$VM" -- sudo sh -c 'ls -d /var/tmp/teslcam-* 2>/dev/null' 2>/dev/null)"
  if [ -n "$stale" ]; then
    warn "stale scratch dirs in the VM from an earlier run"
    printf '%s\n' "$stale" | sed "s/^/       /"
    if confirm "remove them?"; then
      limactl shell "$VM" -- sudo sh -c 'rm -rf /var/tmp/teslcam-*' 2>/dev/null && ok "VM scratch cleaned"
    fi
  fi
  free_mb="$(limactl shell "$VM" -- df -Pm / 2>/dev/null | awk 'NR==2 {print $4}')"
  if [ -n "$free_mb" ] && [ "$free_mb" -lt 6000 ]; then
    bad "VM has ${free_mb} MB free; each image phase needs ~4.5 GB"
    info "recreate it: limactl delete $VM && limactl start ./teslcam-dev.yaml"
  elif [ -n "$free_mb" ]; then
    ok "VM disk ${free_mb} MB free"
  else
    warn "could not read VM free space"
  fi

  if limactl shell "$VM" -- modinfo dummy_hcd >/dev/null 2>&1; then
    ok "dummy_hcd present (software USB loop)"
  else
    warn "VM kernel lacks dummy_hcd — stop/start $VM to enter the full kernel"
  fi

  GIT_HASH="$(git rev-parse --short HEAD 2>/dev/null)"
  if git diff --quiet 2>/dev/null && git diff --cached --quiet 2>/dev/null; then
    ok "working tree clean at $GIT_HASH"
  else
    warn "working tree dirty — images will be stamped -dirty and are not reproducible"
  fi
  return 0
}

# ═════════════════════════════════════════════ phase: source regressions
do_source_checks() {
  phase "source regressions"
  local main=cmd/teslcam-agent/main.go
  [ -f "$main" ] || { bad "$main not found"; return 1; }

  # BLE onboarding must not sit downstream of gadget setup: a unit with a
  # missing backing.img would fatal before advertising, leaving no way in.
  local ble_line gadget_line
  ble_line="$(grep -n 'blepair\.Run(' "$main" | head -1 | cut -d: -f1)"
  gadget_line="$(grep -n 'g\.Setup()' "$main" | head -1 | cut -d: -f1)"
  if [ -z "$ble_line" ] || [ -z "$gadget_line" ]; then
    warn "could not locate blepair.Run / g.Setup in $main — check by hand"
  elif [ "$ble_line" -lt "$gadget_line" ]; then
    ok "BLE onboarding starts before gadget setup ($ble_line < $gadget_line)"
  else
    bad "BLE starts after gadget setup (line $ble_line vs $gadget_line)"
    info "missing backing.img => fatal before BLE => unreachable over BLE and SSH"
  fi

  run_step "go build ./..." 600 go build ./...
}

# ═══════════════════════════════════════════════════════ phase: static audit
do_audit() {
  local image="$1"
  phase "static audit  $(basename "$image")"

  local out="$STATE_DIR/audit.out"
  in_vm "$image" > "$out" 2>&1 <<'VMAUDIT'
set -uo pipefail
IMG_XZ="$1"
W=/var/tmp/teslcam-audit; M="$W/mnt"; LOOP=""
cleanup() {
  set +e
  for m in "$M/boot/firmware" "$M"; do mountpoint -q "$m" && umount "$m"; done
  [ -n "$LOOP" ] && losetup -d "$LOOP"
  rm -rf "$W"
}
trap cleanup EXIT
rm -rf "$W"; mkdir -p "$M"
xz -dc "$IMG_XZ" > "$W/s.img" || { echo "AUDIT-FAIL cannot unpack image (disk full?)"; exit 1; }
LOOP="$(losetup --find --show --partscan "$W/s.img")"
for _ in $(seq 1 20); do [ -e "${LOOP}p2" ] && break; sleep 0.5; done
[ -e "${LOOP}p2" ] || { echo "AUDIT-FAIL partition nodes never appeared"; exit 1; }
mount "${LOOP}p2" "$M" || { echo "AUDIT-FAIL rootfs will not mount"; exit 1; }
mount "${LOOP}p1" "$M/boot/firmware" || { echo "AUDIT-FAIL boot partition will not mount"; exit 1; }

p() { echo "AUDIT-OK $*"; }
f() { echo "AUDIT-FAIL $*"; }
w() { echo "AUDIT-WARN $*"; }

while read -r path label; do
  [ -z "$path" ] && continue
  [ -e "$M$path" ] && p "$label" || f "$label (missing $path)"
done <<'MANIFEST'
/opt/teslcam/current/teslcam-agent            agent binary
/opt/teslcam/current/teslcam-camera-scorer    camera scorer
/usr/local/bin/teslcam-updater                OTA updater
/etc/systemd/system/teslcam-updater.service   OTA updater unit
/etc/teslcam/ota-release.pub.pem              OTA release trust key
/usr/local/sbin/teslcam-firstboot       firstboot script
/usr/local/sbin/teslcam-wg-provision    wireguard provisioning script
/usr/local/sbin/teslcam-boot-report     boot-report script
/etc/systemd/system/teslcam-boot-report.service  boot-report unit
/etc/systemd/system/teslcam-boot-report.timer    boot-report timer
/etc/systemd/system/teslcam-agent.service        agent unit
/etc/systemd/system/teslcam-firstboot.service    firstboot unit
/etc/systemd/system/teslcam-wg-provision.service wireguard provisioning unit
/etc/teslcam/agent.env                  unprovisioned agent.env
/etc/teslcam/backing-size-gb            backing size marker
MANIFEST

# Resolve through the image's own PATH: exfatprogs installs to /usr/sbin,
# ffmpeg to /usr/bin — hardcoding either produces false failures.
for cmd in mkfs.exfat fsck.exfat rfkill ffmpeg ffprobe wg wg-quick bluetoothd; do
  r="$(chroot "$M" sh -c "command -v $cmd" 2>/dev/null)"
  [ -n "$r" ] && p "$cmd ($r)" || f "$cmd not installed in image"
done

# Pi OS ships both radios rfkill-soft-blocked pending the wireless-country
# wizard, which this image skips — without the unblock rule a unit boots with
# hci0 present but dead: no BLE onboarding, no way in. Field-found.
if grep -qs 'SUBSYSTEM=="rfkill"' "$M/etc/udev/rules.d/90-teslcam-rfkill-unblock.rules"; then
  p "rfkill unblock udev rule present"
else
  f "rfkill unblock udev rule missing — radios boot soft-blocked, BLE dead"
fi

# pi-bluetooth attaches the UART BT module (hci0). The trixie Lite base does
# not ship it; without it BLE onboarding fails silently — no advertising, no
# way into a fresh unit. Learned from the first field flash.
if grep -q "^Package: pi-bluetooth$" "$M/var/lib/dpkg/status" 2>/dev/null; then
  p "pi-bluetooth installed (hci0 bring-up)"
else
  f "pi-bluetooth NOT installed — no hci0, BLE onboarding dead on real hardware"
fi

# Volatile journald means power cuts erase all runtime evidence — an in-car
# unit that misbehaves leaves nothing to read. The property that matters is
# which Storage= value WINS the merge: journald sorts fragments by filename
# across /usr/lib and /etc, and Pi OS ships 40-rpi-volatile-storage.conf
# (Storage=volatile) that silently beat our old 10- prefixed file.
WINNER="$(for d in "$M/usr/lib/systemd/journald.conf.d" "$M/etc/systemd/journald.conf.d"; do
    [ -d "$d" ] && for fpath in "$d"/*.conf; do [ -f "$fpath" ] && echo "$(basename "$fpath")|$fpath"; done
  done | sort -t'|' -k1,1 | cut -d'|' -f2 | xargs grep -h '^Storage=' 2>/dev/null | tail -1)"
if [ "$WINNER" = "Storage=persistent" ]; then
  grep -qs '^SystemMaxUse=' "$M"/etc/systemd/journald.conf.d/*.conf \
    && p "journald Storage=persistent wins the dropin merge (size-capped)" \
    || f "journald persistent but no SystemMaxUse cap (can eat the card)"
else
  f "journald effective Storage is '${WINNER:-unset}' — logs vanish on every power cut"
fi

for u in ssh bluetooth teslcam-agent teslcam-firstboot teslcam-wg-provision teslcam-boot-report; do
  ls "$M"/etc/systemd/system/*.target.wants/"$u".service >/dev/null 2>&1 \
    && p "$u enabled" || f "$u NOT enabled (no target.wants symlink)"
done
ls "$M"/etc/systemd/system/timers.target.wants/teslcam-boot-report.timer >/dev/null 2>&1 \
  && p "boot-report timer enabled" || f "boot-report timer NOT enabled"
ls "$M"/etc/systemd/system/*.target.wants/NetworkManager-wait-online.service >/dev/null 2>&1 \
  && f "NetworkManager-wait-online enabled (stalls boot)" || p "NetworkManager-wait-online disabled"

# Effective sshd config. An unset PasswordAuthentication defaults to YES, which
# a grep for "no" cannot notice.
SSHD_T="$(chroot "$M" /usr/sbin/sshd -T -C user=root,host=localhost,addr=127.0.0.1 2>/dev/null)"
if [ -n "$SSHD_T" ]; then
  PW="$(echo "$SSHD_T" | awk '/^passwordauthentication /{print $2}')"
  KB="$(echo "$SSHD_T" | awk '/^kbdinteractiveauthentication /{print $2}')"
  RL="$(echo "$SSHD_T" | awk '/^permitrootlogin /{print $2}')"
  [ "$PW" = "no" ] && p "sshd passwordauthentication no" || f "sshd passwordauthentication is '$PW' (must be no)"
  [ "$KB" = "no" ] && p "sshd kbdinteractiveauthentication no" || f "sshd kbdinteractiveauthentication is '$KB'"
  case "$RL" in no|prohibit-password) p "sshd permitrootlogin $RL" ;; *) w "sshd permitrootlogin is '$RL'" ;; esac
else
  EFF="$(grep -rhiE '^[[:space:]]*PasswordAuthentication' "$M/etc/ssh/sshd_config" "$M/etc/ssh/sshd_config.d/" 2>/dev/null | tail -1)"
  if [ -z "$EFF" ]; then f "sshd PasswordAuthentication unset — defaults to YES"
  elif echo "$EFF" | grep -qi 'no'; then p "sshd PasswordAuthentication no (from config)"
  else f "sshd PasswordAuthentication enabled: $EFF"; fi
fi

ADMIN_HASH="$(awk -F: '$1 == "adri" {print $2}' "$M/etc/shadow" 2>/dev/null)"
case "$ADMIN_HASH" in
  '!'*|'*'|'!') p "admin password locked" ;;
  "")           f "admin user 'adri' missing from /etc/shadow" ;;
  *)            f "admin account has a usable password hash" ;;
esac
[ -s "$M/home/adri/.ssh/authorized_keys" ] && p "authorized_keys baked in" || f "authorized_keys missing/empty"

grep -q 'dtoverlay=dwc2,dr_mode=peripheral' "$M/boot/firmware/config.txt" \
  && p "dwc2 peripheral overlay in config.txt" || f "dwc2 overlay missing from config.txt"
grep -qx 'dwc2' "$M/etc/modules" && grep -qx 'libcomposite' "$M/etc/modules" \
  && p "dwc2 + libcomposite in /etc/modules" || f "gadget modules missing from /etc/modules"

# Ships no secrets: onboarding and flash-time provisioning supply them.
grep -qE '^POST_TO=.+' "$M/etc/teslcam/agent.env" 2>/dev/null \
  && f "agent.env has POST_TO set — image is not clean" || p "agent.env unprovisioned"
[ -e "$M/etc/teslcam/server.token" ] && f "server.token baked into image" || p "no server.token in image"
[ -e "$M/etc/wireguard/wg1.conf" ] && f "wireguard key baked into image" || p "no wireguard key in image"
[ -e "$M/boot/firmware/teslcam-wg1.conf" ] && f "wireguard config left on boot partition" || p "boot partition carries no wg config"
if ls "$M"/etc/NetworkManager/system-connections/*.nmconnection >/dev/null 2>&1; then
  grep -rlqi 'psk=' "$M"/etc/NetworkManager/system-connections/ 2>/dev/null \
    && f "a Wi-Fi PSK is baked into the image" || p "no Wi-Fi credentials in NM profiles"
fi

[ -L "$M/etc/systemd/system/userconfig.service" ] \
  && [ "$(readlink "$M/etc/systemd/system/userconfig.service")" = "/dev/null" ] \
  && p "userconfig.service masked" || f "userconfig.service not masked — blocks SSH login"
[ -e "$M/etc/ssh/sshd_config.d/rename_user.conf" ] && f "rename_user.conf present (blocks ssh)" || p "rename_user.conf removed"

for flag in -ble-onboard -ble-adapter -ble-name -ble-config-dir; do
  grep -q -- "$flag" "$M/etc/systemd/system/teslcam-agent.service" \
    && p "agent unit passes $flag" || f "agent unit missing $flag"
done

# The baked agent must carry the BLE advertising fixes (bounded btmgmt with the
# Set-Advertising-off teardown, and the debugfs advertising-interval write).
# Checked by grepping the binary for their log/path strings — crude but exact:
# an agent built before the fix lacks both, and that unit is undiscoverable in
# practice (kernel-default 1280 ms interval + wedged adv state machine).
if grep -aq "legacy advertising: asserting instance" "$M/opt/teslcam/current/teslcam-agent" \
   && grep -aq "adv_min_interval" "$M/opt/teslcam/current/teslcam-agent"; then
  p "agent binary contains the BLE advertising fixes (interval + teardown)"
else
  f "agent binary predates the BLE advertising fixes — onboarding will be undiscoverable"
fi

# The connection test's auth probe must be the device's own status endpoint,
# never /usage. A per-device token is authorized for the device it was minted
# for; /usage is operator-only, so an agent built before the fix fails the last
# step of onboarding with 403 on a token that is perfectly valid. Detected by
# the absence of the "/usage" literal, which only that probe ever put in this
# binary.
if grep -aq "/usage" "$M/opt/teslcam/current/teslcam-agent"; then
  f "agent binary still probes /usage — onboarding will fail 403 at the connection test"
else
  p "agent binary probes the device's own endpoint for the auth check"
fi

# The offline escape hatches: a unit with no network is only debuggable through
# the USB cable, and both paths need image-side wiring that the agent alone
# cannot supply. Shipping the CDC-ACM console without a getty (as the first
# version did) yields a port that opens and then sits mute, which is worse than
# not having it — the fault looks like the unit, not the missing service.
grep -rq "ttyGS0" "$M/etc/udev/rules.d/" 2>/dev/null \
  && p "udev starts a getty when the gadget console appears" \
  || f "no getty wiring for ttyGS0 — the USB serial console would be a dead port"
grep -q -- "--autologin" "$M/etc/systemd/system/serial-getty@ttyGS0.service.d/10-teslcam-autologin.conf" 2>/dev/null \
  && p "gadget console autologin drop-in present" \
  || f "no autologin on the gadget console — the admin password is locked, so the prompt is unanswerable"
# The Wi-Fi trap this image was shipped with once: rfkill unblocked, radio
# healthy, and NetworkManager still refusing to scan because it persisted
# WirelessEnabled=false at first boot. Silent — the scan returns an empty list
# with no error — so only an explicit check catches a regression here.
[ -L "$M/etc/systemd/system/multi-user.target.wants/teslcam-wifi-enable.service" ] \
  && p "wifi radio switch is re-enabled every boot" \
  || f "no teslcam-wifi-enable unit — a unit can end up unable to scan Wi-Fi at all"
grep -q "NM_UNMANAGED" "$M/etc/udev/rules.d/86-teslcam-usb0-managed.rules" 2>/dev/null \
  && p "usb0 udev override present (NM ignores gadget devices by default)" \
  || f "no usb0 udev override — NetworkManager would leave the dev link unmanaged"
grep -q "ip link set usb0 up" "$M/etc/udev/rules.d/86-teslcam-usb0-managed.rules" 2>/dev/null \
  && p "usb0 rule brings the link up (no carrier, no NM activation)" \
  || f "usb0 rule does not raise the link — NM never activates a carrier-less device"

# Without this, onboarding works exactly once per boot: BlueZ rejects the
# second Just Works pairing from a phone it still holds a bond for, inside
# bluetoothd, and the app can only report "write failed".
grep -q "^JustWorksRepairing = always" "$M/etc/bluetooth/main.conf" 2>/dev/null \
  && p "BlueZ allows Just Works re-pairing (retried onboarding works)" \
  || f "JustWorksRepairing not set — a retried pairing is rejected before our agent sees it"

USBNM="$M/etc/NetworkManager/system-connections/usb-gadget.nmconnection"
if grep -q "interface-name=usb0" "$USBNM" 2>/dev/null; then
  p "usb0 NM profile present (USB dev link)"
  # Only meaningful once the profile exists; otherwise it double-reports the
  # same missing file as two failures.
  grep -q "never-default=true" "$USBNM" \
    && p "usb0 profile is never-default (no route hijack from the dev host)" \
    || f "usb0 profile may take a default route from whatever host the cable is in"
else
  f "no usb0 NM profile — the USB ethernet link would come up unconfigured"
fi
[ -e "$M/boot/firmware/teslcam-gadget-net" ] || [ -e "$M/boot/firmware/teslcam-gadget-console" ] \
  && f "a gadget debug marker is baked into the image — every unit would ship composite" \
  || p "no gadget debug marker baked in (armed per-unit at flash time)"
VMAUDIT
  local rc=$? seen=0 line
  while IFS= read -r line; do
    case "$line" in
      AUDIT-OK\ *)   seen=1; ok   "${line#AUDIT-OK }" ;;
      AUDIT-WARN\ *) seen=1; warn "${line#AUDIT-WARN }" ;;
      AUDIT-FAIL\ *) seen=1; bad  "${line#AUDIT-FAIL }" ;;
      *) [ -n "$line" ] && info "$line" ;;
    esac
  done < "$out"
  [ "$seen" = 0 ] && { bad "static audit produced no results (exit $rc)"; return 1; }
  [ "$rc" -ne 0 ] && [ "$FAIL" -eq 0 ] && bad "audit exited $rc without a specific failure"
  return 0
}

# ═══════════════════════════════════════════════════════════ phase: freshness
do_freshness() {
  local image="$1"
  phase "freshness"
  local stamp; stamp="$(basename "$image" .img.xz)"; stamp="${stamp#teslcam-pi4-}"
  case "$stamp" in *-dirty) warn "built from a dirty tree ($stamp) — not reproducible" ;; esac
  if [ "${stamp%-dirty}" = "$GIT_HASH" ]; then
    ok "image matches HEAD ($GIT_HASH)"
  else
    bad "image is stale: built from ${stamp%-dirty}, HEAD is $GIT_HASH"
    info "anything added since is not in it"
  fi
}

# ══════════════════════════════════════════════════════════════ phase: build
do_build() {
  phase "build"
  local gb keys ota_key
  if [ "$ASSUME_YES" = 1 ]; then
    gb="${BACKING_GB:-}"; keys="${AUTHORIZED_KEYS:-}"; ota_key="${OTA_PUBLIC_KEY:-}"
    [ -n "$gb" ] || die "--yes with a build requires BACKING_GB in the environment"
    [ -n "$keys" ] || die "--yes with a build requires AUTHORIZED_KEYS in the environment"
    [ -n "$ota_key" ] || die "--yes with a build requires OTA_PUBLIC_KEY in the environment"
  else
    gb="$(prompt_value 'backing image size in GB (e.g. 64)')"
    keys="$(prompt_value 'SSH public key to bake in (e.g. ~/.ssh/id_rsa.pub)')"
    ota_key="$(prompt_value 'OTA Ed25519 public key (PEM)')"
  fi
  keys="${keys/#\~/$HOME}"
  ota_key="${ota_key/#\~/$HOME}"
  case "$gb" in ''|*[!0-9]*) die "backing size must be an integer, got: $gb" ;; esac
  [ -f "$keys" ] || die "no such key file: $keys"
  [ -f "$ota_key" ] || die "no such OTA public key: $ota_key"
  run_step "build-image.sh $gb" 3600 scripts/build-image.sh "$gb" "$keys" "$ota_key"
}

# ═══════════════════════════════════════════════════ phases: boot + datapath
do_boot_test() {
  local image="$1"
  phase "boot (firstboot chroot + nspawn userspace)"
  run_step "test-image.sh" 1800 scripts/test-image.sh "$image"
  info "nspawn has no UDC, so an agent crash loop there proves nothing"
  info "real agent health comes from the datapath phase"
}

do_datapath_test() {
  phase "datapath (dummy_hcd gadget -> sim write -> upload)"
  if ! limactl shell "$VM" -- modinfo dummy_hcd >/dev/null 2>&1; then
    warn "dummy_hcd unavailable — skipping; stop/start $VM to enter the full kernel"
    return 0
  fi
  run_step "vm-agent-test.sh" 1800 scripts/vm-agent-test.sh
}

# ══════════════════════════════════════════════════════════════ phase: flash
do_flash() {
  local image="$1"
  phase "flash"
  info "flash-image.sh is interactive: it lists external disks and requires"
  info "typing the disk identifier back before erasing anything."
  if [ "$ASSUME_YES" = 1 ]; then
    warn "refusing to flash under --yes; flashing always requires confirmation"
    return 1
  fi
  confirm "hand off to scripts/flash-image.sh now?" || { info "skipped"; return 1; }
  scripts/flash-image.sh "$image"
}

# ════════════════════════════════════════════════════════════ phase: verify
# The hardware-only paths, checked over SSH on a freshly flashed unit. One
# flash yields a full pass/fail list instead of one bug per cycle.
do_verify() {
  local host="$1"
  phase "verify unit  $host"

  local out="$STATE_DIR/verify.out"
  if ! ssh -o ConnectTimeout=10 -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
       "$host" 'bash -s' > "$out" 2>&1 <<'REMOTE'
set -uo pipefail
p() { echo "V-OK $*"; }
f() { echo "V-FAIL $*"; }
w() { echo "V-WARN $*"; }

# --- boot media and firmware chain (only observable on the real board)
ROOTDEV="$(findmnt -no SOURCE / 2>/dev/null)"
case "$ROOTDEV" in
  /dev/mmcblk*) p "booted from SD card ($ROOTDEV)" ;;
  /dev/sd*)     p "booted from USB media ($ROOTDEV)" ;;
  *)            w "unexpected root device: $ROOTDEV" ;;
esac
if command -v vcgencmd >/dev/null 2>&1; then
  TH="$(vcgencmd get_throttled 2>/dev/null | cut -d= -f2)"
  case "$TH" in
    0x0) p "no undervoltage/throttling since boot" ;;
    "")  w "could not read throttling state" ;;
    *)   w "throttling flags $TH (glovebox USB is ~1-2A; check power)" ;;
  esac
fi

# --- USB device mode: the dwc2 UDC must exist and be bound to our gadget
UDC="$(ls /sys/class/udc 2>/dev/null | head -1)"
[ -n "$UDC" ] && p "UDC present ($UDC)" || f "no UDC in /sys/class/udc — dwc2 peripheral mode not active"
GADGET="$(ls -d /sys/kernel/config/usb_gadget/* 2>/dev/null | head -1)"
if [ -n "$GADGET" ]; then
  BOUND="$(cat "$GADGET/UDC" 2>/dev/null)"
  [ -n "$BOUND" ] && p "gadget bound to $BOUND" || f "gadget exists but is not bound to a UDC"
  LUN="$(cat "$GADGET"/functions/mass_storage.*/lun.0/file 2>/dev/null | head -1)"
  [ -n "$LUN" ] && p "mass-storage LUN backed by $LUN" || f "mass-storage LUN has no backing file"
else
  f "no configfs gadget — the agent never set one up"
fi

# --- backing image
IMG=/var/lib/teslcam/backing.img
if [ -f "$IMG" ]; then
  SZ="$(du -h "$IMG" 2>/dev/null | cut -f1)"
  AP="$(du -h --apparent-size "$IMG" 2>/dev/null | cut -f1)"
  p "backing.img present (apparent $AP, on disk $SZ)"
  sudo sfdisk -d "$IMG" 2>/dev/null | grep -q 'type=7' \
    && p "backing.img has MBR type 7 (Tesla ignores partitionless images)" \
    || f "backing.img is not MBR type 7"
else
  f "backing.img missing — firstboot did not complete"
fi
[ -f /var/lib/teslcam/.firstboot-done ] && p "firstboot completed" || f "firstboot marker absent"

# --- agent health. A crash loop is the failure mode that matters: NRestarts
# keeps climbing while is-active still reports "activating".
AG="$(systemctl is-active teslcam-agent 2>/dev/null)"
NR="$(systemctl show teslcam-agent -p NRestarts --value 2>/dev/null)"
case "$AG" in
  active) p "teslcam-agent active (NRestarts=${NR:-?})" ;;
  *)      f "teslcam-agent is '$AG' (NRestarts=${NR:-?})" ;;
esac
[ -n "$NR" ] && [ "$NR" -gt 5 ] 2>/dev/null && f "teslcam-agent has restarted $NR times — crash loop"

# --- BLE radio: present, powered, unblocked, and actually advertising
command -v rfkill >/dev/null 2>&1 && {
  rfkill list bluetooth 2>/dev/null | grep -qi 'Soft blocked: yes' \
    && f "bluetooth is rfkill soft-blocked — no onboarding possible" \
    || p "bluetooth not rfkill-blocked"
}
if hciconfig hci0 2>/dev/null | grep -q 'UP RUNNING'; then
  p "hci0 UP RUNNING"
else
  f "hci0 not UP RUNNING — BLE onboarding cannot advertise"
fi
SET="$(sudo btmgmt info 2>/dev/null | awk '/current settings/{print}')"
echo "$SET" | grep -q 'le' && p "adapter has LE enabled" || w "LE not in current settings"
# The `advertising` flag in current settings is unreliable on this stack (it
# stayed off while an HCI trace showed advertising enabled and a scanner saw
# the unit). Count mgmt advertising instances instead.
ADVN="$(sudo btmgmt advinfo 2>/dev/null | grep -oE 'Instances list with [0-9]+' | grep -oE '[0-9]+$')"
if [ -n "$ADVN" ] && [ "$ADVN" -gt 0 ] 2>/dev/null; then
  p "advertising instance active ($ADVN)"
else
  w "no advertising instance — expected only if the unit is already provisioned"
fi

# --- provisioning state
if [ -s /etc/teslcam/agent.env ] && grep -qE '^POST_TO=.+' /etc/teslcam/agent.env; then
  p "provisioned (POST_TO set)"
  [ -s /etc/teslcam/server.token ] && p "server token present" || f "provisioned but no server.token"
else
  p "unprovisioned — awaiting BLE onboarding (expected on a fresh unit)"
fi

# --- remote access tunnel (/etc/wireguard is 0700, so every probe needs sudo)
if sudo test -f /etc/wireguard/wg1.conf; then
  PERM="$(sudo stat -c '%a' /etc/wireguard/wg1.conf 2>/dev/null)"
  [ "$PERM" = "600" ] && p "wg1.conf mode 600" || f "wg1.conf mode is $PERM (must be 600)"
  systemctl is-active --quiet wg-quick@wg1 && p "wg-quick@wg1 active" || f "wg-quick@wg1 not active"
  HS="$(sudo wg show wg1 latest-handshakes 2>/dev/null | awk '{print $2}' | head -1)"
  if [ -n "$HS" ] && [ "$HS" -gt 0 ] 2>/dev/null; then
    AGE=$(( $(date +%s) - HS ))
    [ "$AGE" -lt 300 ] && p "wg1 handshake ${AGE}s ago" || w "wg1 last handshake ${AGE}s ago"
  else
    w "wg1 has no handshake yet (can take up to PersistentKeepalive)"
  fi
  ip route show 2>/dev/null | grep -q '^default.*wg1' \
    && f "default route is over wg1 — would pull all traffic across the metered SIM" \
    || p "default route not over wg1"
else
  w "no wg1.conf — unit has no remote access (provision at flash time)"
fi
[ -f /boot/firmware/teslcam-wg1.conf ] && f "wg config still on FAT boot partition (private key readable)" \
  || p "no wg private key left on the boot partition"

# --- clock synchronization. The fixed peers have destination-specific policy
# rules through LTE, so this must work after a cold boot without Wi-Fi while
# preserving the no-default-route invariant on the metered interface.
if [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null)" = yes ]; then
  TS_SERVER="$(timedatectl show-timesync -p ServerAddress --value 2>/dev/null)"
  case "$TS_SERVER" in
    162.159.200.1|162.159.200.123) p "clock synchronized through fixed LTE NTP peer $TS_SERVER" ;;
    *) w "clock synchronized, but current NTP peer is ${TS_SERVER:-unknown} (not the LTE peers)" ;;
  esac
else
  f "system clock is not synchronized"
fi
for ntp_ip in 162.159.200.1 162.159.200.123; do
  ip rule show | grep -q "to $ntp_ip lookup 101" \
    && p "LTE policy route present for NTP $ntp_ip" \
    || f "LTE policy route missing for NTP $ntp_ip"
done
sudo nft list chain inet lte_guard output 2>/dev/null \
  | grep -q '162.159.200.1.*162.159.200.123.*udp dport 123.*accept' \
  && p "LTE firewall permits only the fixed NTP peers" \
  || f "LTE firewall NTP exception missing"

# --- sshd hardening, as actually resolved
PW="$(sudo sshd -T 2>/dev/null | awk '/^passwordauthentication /{print $2}')"
[ "$PW" = "no" ] && p "sshd passwordauthentication no" || f "sshd passwordauthentication is '${PW:-unknown}'"

# --- capacity
USE="$(df -P / | awk 'NR==2 {print $5}' | tr -d '%')"
[ -n "$USE" ] && { [ "$USE" -lt 85 ] && p "rootfs ${USE}% used" || w "rootfs ${USE}% used"; }
REMOTE
  then
    bad "cannot reach $host over SSH"
    printf '%s' "$GRY"; sed 's/^/       /' "$out" 2>/dev/null | head -5; printf '%s' "$RST"
    info "on LTE a unit is reachable only via the tunnel: ssh -J adri@vps adri@10.8.0.<N>"
    info "and only while the car is awake — Tesla cuts glovebox USB on sleep"
    return 1
  fi

  local line seen=0
  while IFS= read -r line; do
    case "$line" in
      V-OK\ *)   seen=1; ok   "${line#V-OK }" ;;
      V-WARN\ *) seen=1; warn "${line#V-WARN }" ;;
      V-FAIL\ *) seen=1; bad  "${line#V-FAIL }" ;;
      *) [ -n "$line" ] && info "$line" ;;
    esac
  done < "$out"
  [ "$seen" = 0 ] && bad "verify produced no results from $host"
  return 0
}

# ═════════════════════════════════════════════════════════════════ verdict
verdict() {
  local context="$1"
  phase "verdict"
  printf '  %s%d passed%s   %s%d warnings%s   %s%d failed%s\n' \
    "$GRN" "$PASS" "$RST" "$YEL" "$WARN" "$RST" "$RED" "$FAIL" "$RST"

  if [ -s "$WARNED_LOG" ]; then
    printf '\n  %swarnings%s\n' "$BOLD$YEL" "$RST"
    sed "s/^/    $G_WARN /" "$WARNED_LOG"
  fi
  if [ -s "$FAILED_LOG" ]; then
    printf '\n  %sfailures%s\n' "$BOLD$RED" "$RST"
    sed "s/^/    $G_NO /" "$FAILED_LOG"
  fi

  if [ "$context" = "hardware-free" ]; then
    printf '\n  %sstill untested — needs the real board%s\n' "$GRY" "$RST"
    printf '  %s  Pi firmware boot chain · EEPROM SD/USB order · dwc2 silicon%s\n' "$GRY" "$RST"
    printf '  %s  BLE radio · the car itself      (run: %s verify <host>)%s\n' "$GRY" "$(basename "$0")" "$RST"
  fi

  hr
  if [ "$FAIL" -gt 0 ]; then
    printf '%s%s NOT ready%s — %d check(s) failed\n\n' "$BOLD$RED" "$G_NO" "$RST" "$FAIL"
    return 1
  fi
  printf '%s%s all clear%s — %d checks passed\n\n' "$BOLD$GRN" "$G_OK" "$RST" "$PASS"
  return 0
}

# Sets IMAGE_PATH. Does not print the path: `die` inside a command
# substitution only kills the subshell, so a missing image would otherwise
# sail past the check with an empty path.
IMAGE_PATH=""
require_image() {
  IMAGE_PATH="$(newest_image)"
  if [ -z "$IMAGE_PATH" ]; then
    bad "no image in build/ — run: $(basename "$0") build"
    return 1
  fi
  if [ ! -f "$IMAGE_PATH" ]; then
    bad "image not found: $IMAGE_PATH"
    return 1
  fi
  return 0
}

# ══════════════════════════════════════════════════════════════ command flow
cmd_doctor()  { banner; do_doctor; verdict none; }
cmd_audit()   { banner; do_doctor || { verdict none; return 1; }
                require_image || { verdict none; return 1; }
                do_freshness "$IMAGE_PATH"; do_audit "$IMAGE_PATH"; verdict hardware-free; }
cmd_validate(){ banner; do_doctor || { verdict none; return 1; }
                do_source_checks
                require_image || { verdict none; return 1; }
                do_freshness "$IMAGE_PATH"; do_audit "$IMAGE_PATH"
                do_boot_test "$IMAGE_PATH"; do_datapath_test; verdict hardware-free; }
cmd_build()   { banner; do_doctor || { verdict none; return 1; }
                do_source_checks; do_build; verdict none; }
cmd_flash()   { banner; require_image || { verdict none; return 1; }
                do_flash "$IMAGE_PATH"; }
# The first-boot check runs on this Mac, not in the VM and not over SSH: a
# freshly flashed unit has no network yet, so the only ways in are the two its
# clients use — BLE (as the app) and USB mass storage (as the car).
cmd_healthcheck(){
  banner
  if [ "$(uname)" != "Darwin" ]; then
    bad "healthcheck needs macOS (CoreBluetooth + diskutil)"; verdict none; return 1
  fi
  phase "first-boot healthcheck"
  info "power the unit and plug its USB-C *data* port into this Mac"
  local args=()
  [ "$ASSUME_YES" = 1 ] && args+=(--yes)
  "$PROJECT_DIR/scripts/healthcheck-unit.sh" "${args[@]+"${args[@]}"}"
}

# flash -> wait for the unit to come up -> healthcheck, with no typing in
# between. The card must already be in this Mac and the unit's USB-C data port
# plugged in; everything after that is unattended.
#
# The wait is not a fixed sleep: first boot expands the rootfs and creates a
# multi-gigabyte backing image, which takes anywhere from one to several
# minutes depending on the card, and the gadget only appears once the agent has
# it. Polling for the drive is both faster on a quick card and correct on a
# slow one.
cmd_unit(){
  banner
  [ "$(uname)" = "Darwin" ] || { bad "unit needs macOS"; verdict none; return 1; }

  local disk="${1:-}"
  if [ -z "$disk" ]; then
    local candidates=()
    while IFS= read -r d; do [ -n "$d" ] && candidates+=("$d"); done < <(removable_disks)
    case "${#candidates[@]}" in
      0) bad "no removable disk found — insert the card first"; verdict none; return 1 ;;
      1) disk="${candidates[0]}"; info "target card: /dev/$disk" ;;
      *) bad "several removable disks (${candidates[*]}) — name one: $(basename "$0") unit diskN"
         verdict none; return 1 ;;
    esac
  fi

  phase "flash  /dev/$disk"
  if ! TESLCAM_DEV_LINK=1 "$PROJECT_DIR/scripts/flash-image.sh" --disk "$disk" --yes; then
    bad "flashing failed"; verdict none; return 1
  fi
  ok "card written"

  phase "first boot"
  info "insert the card into the Pi, power it up, keep the USB-C data cable to this Mac"
  # The card was just ejected, so the operator has to move it. Wait for the
  # unit to announce itself rather than guessing how long that takes.
  local waited=0
  while [ "$waited" -lt 600 ]; do
    if diskutil list 2>/dev/null | grep -q TESLACAM; then
      ok "unit enumerated its drive after ${waited}s"
      break
    fi
    sleep 10
    waited=$((waited + 10))
  done
  if [ "$waited" -ge 600 ]; then
    bad "no drive from the unit after 10 minutes — is it powered and cabled?"
    verdict none; return 1
  fi
  # The agent needs a moment past the drive appearing before BLE is up.
  sleep 20

  phase "healthcheck"
  "$PROJECT_DIR/scripts/healthcheck-unit.sh" --yes
}

# Whole removable disks, by the same predicate flash-image.sh uses (the
# built-in SD reader reports Internal=true, so Ejectable is the discriminator).
removable_disks() {
  local d root_whole
  root_whole="$(diskutil info -plist / 2>/dev/null | plutil -extract ParentWholeDisk raw -o - - 2>/dev/null)"
  for d in $(diskutil list 2>/dev/null | grep -oE '^/dev/disk[0-9]+' | sed 's|/dev/||'); do
    [ "$(diskutil info -plist "$d" 2>/dev/null | plutil -extract VirtualOrPhysical raw -o - - 2>/dev/null)" = "Physical" ] || continue
    [ "$(diskutil info -plist "$d" 2>/dev/null | plutil -extract WholeDisk raw -o - - 2>/dev/null)" = "true" ] || continue
    [ "$d" = "$root_whole" ] && continue
    local internal ejectable
    internal="$(diskutil info -plist "$d" 2>/dev/null | plutil -extract Internal raw -o - - 2>/dev/null)"
    ejectable="$(diskutil info -plist "$d" 2>/dev/null | plutil -extract Ejectable raw -o - - 2>/dev/null)"
    [ "$internal" = "false" ] || [ "$ejectable" = "true" ] || continue
    printf '%s\n' "$d"
  done
}

cmd_verify()  { banner
                local h="${1:-$DEFAULT_HOST}"
                [ -n "$h" ] || h="$(prompt_value 'unit to verify (ssh host, e.g. pi or 10.8.0.2)')"
                do_verify "$h"; verdict none; }
cmd_pipeline(){
  banner
  do_doctor || { verdict none; return 1; }
  do_source_checks
  if confirm "build a fresh image?"; then do_build; fi
  require_image || { verdict none; return 1; }
  do_freshness "$IMAGE_PATH"; do_audit "$IMAGE_PATH"
  do_boot_test "$IMAGE_PATH"; do_datapath_test
  if [ "$FAIL" -gt 0 ]; then
    verdict hardware-free
    warn "not flashing: hardware-free checks failed"
    return 1
  fi
  verdict hardware-free
  do_flash "$IMAGE_PATH" || return 0
  printf '\n'
  # The first-boot check comes before the SSH one: it is the only one that works
  # on a unit that has never been onboarded, and it is what puts it on Wi-Fi.
  if [ "$(uname)" = "Darwin" ] && confirm "run the first-boot healthcheck (BLE + USB) now?"; then
    info "power the unit from the card you just flashed and plug its USB-C data port into this Mac"
    confirm "unit powered and plugged in?" && cmd_healthcheck
  fi
  printf '\n'
  if confirm "verify the flashed unit over SSH now?"; then
    info "power the unit and wait for it to join Wi-Fi (BLE onboarding first, if fresh)"
    confirm "unit booted and reachable?" || return 0
    local h="${DEFAULT_HOST:-}"
    [ -n "$h" ] || h="$(prompt_value 'unit ssh host')"
    PASS=0; FAIL=0; WARN=0; : > "$FAILED_LOG"; : > "$WARNED_LOG"
    do_verify "$h"; verdict none
  fi
}

interactive() {
  # The menu needs a keyboard. Piped/CI use goes through the subcommands,
  # which is a clearer failure than prompting into a closed stdin.
  [ -t 0 ] || die "no terminal for the menu — use a subcommand (see --help), e.g. $(basename "$0") validate --yes"
  while :; do
    banner
    menu "what would you like to do?" \
      "full pipeline    build → validate → flash → verify" \
      "validate         audit + boot + datapath (no hardware)" \
      "audit            static audit of the newest image (fast)" \
      "build            build a golden image" \
      "flash            write the newest image to removable media" \
      "healthcheck      first-boot check over BLE + USB (no network needed)" \
      "unit             flash a card, wait for boot, healthcheck (unattended)" \
      "verify unit      health-check a flashed Pi over SSH" \
      "doctor           check this machine and the Lima VM" \
      "quit" || return 0
    PASS=0; FAIL=0; WARN=0; : > "$FAILED_LOG"; : > "$WARNED_LOG"; STEP_N=0
    case "$MENU_CHOICE" in
      1) cmd_pipeline ;;
      2) cmd_validate ;;
      3) cmd_audit ;;
      4) cmd_build ;;
      5) cmd_flash ;;
      6) cmd_healthcheck ;;
      7) cmd_unit ;;
      8) cmd_verify ;;
      9) cmd_doctor ;;
      10|0) return 0 ;;
    esac
    [ -t 0 ] || return 0
    confirm "back to the menu?" || return 0
  done
}

# ═════════════════════════════════════════════════════════════════════ main
CMD=""
ARGS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --yes|-y)   ASSUME_YES=1 ;;
    --no-color) USE_COLOR=0; init_caps ;;
    --plain)    USE_COLOR=0; USE_CURSOR=0; init_caps ;;
    --host)     shift; DEFAULT_HOST="${1:-}"; [ -n "$DEFAULT_HOST" ] || die "--host needs a value" ;;
    --image)    shift; IMAGE_OVERRIDE="${1:-}"; [ -f "$IMAGE_OVERRIDE" ] || die "no such image: ${1:-}" ;;
    -h|--help)  sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)         die "unknown option: $1" ;;
    *)          if [ -z "$CMD" ]; then CMD="$1"; else ARGS="$1"; fi ;;
  esac
  shift
done

case "${CMD:-}" in
  "")         interactive ;;
  doctor)     cmd_doctor ;;
  audit)      cmd_audit ;;
  validate)   cmd_validate ;;
  build)      cmd_build ;;
  flash)      cmd_flash ;;
  healthcheck) cmd_healthcheck ;;
  unit)       cmd_unit "$ARGS" ;;
  verify)     cmd_verify "$ARGS" ;;
  pipeline)   cmd_pipeline ;;
  *)          die "unknown command: $CMD (try --help)" ;;
esac
