#!/usr/bin/env bash
# Install the privileged helper that lets scripts/flash-image.sh write a card
# without asking for a password.
#
#   sudo scripts/install-flash-helper.sh          install
#   sudo scripts/install-flash-helper.sh remove   uninstall
#
# Why a helper instead of a sudoers rule on dd: writing a raw disk needs root
# on macOS and cannot be delegated any other way — but a NOPASSWD rule on `dd`
# would be a passwordless "write anything anywhere", including /dev/rdisk0, the
# Mac's own boot disk. That is full root by another name.
#
# So the privilege is granted to one root-owned program that re-derives the
# safety predicate itself, in root's own context, from arguments it does not
# trust: the target must be a whole, physical, removable disk that is not the
# running system's. The worst a caller can do is overwrite a card that was
# already about to be overwritten.
#
# It is still a real grant: after this, anything running as an admin user on
# this Mac can erase removable media without a password. Remove it with
# `remove` when the flashing sprint is over.
set -euo pipefail

HELPER=/usr/local/libexec/teslcam-flash-write
SUDOERS=/etc/sudoers.d/teslcam-flash

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname)" = "Darwin" ] || die "macOS only"
[ "$(id -u)" = 0 ] || die "run with sudo: sudo $0 ${1:-}"

if [ "${1:-install}" = "remove" ]; then
  rm -f "$HELPER" "$SUDOERS"
  say "removed $HELPER and $SUDOERS — flashing needs a password again"
  exit 0
fi

install -d -m 755 -o root -g wheel /usr/local/libexec

# The helper runs as root. It must not be writable by the user it privileges,
# or the grant becomes "edit this file, become root" — hence 0755 root:wheel,
# written here rather than referenced from the repo (a repo path is user-
# writable by definition).
cat > "$HELPER" <<'HELPEREOF'
#!/bin/sh
# teslcam-flash-write — write stdin to a removable whole disk, as root.
#
# Invoked as: teslcam-flash-write <diskN>   (image bytes on stdin)
#             teslcam-flash-write --check   (probe: exits 0, does nothing)
#
# Every check below is deliberately re-done here instead of trusting the
# caller: this program is reachable without a password, so its arguments are
# untrusted input, not a continuation of flash-image.sh's own vetting.
set -eu

[ "${1:-}" = "--check" ] && exit 0

DISK="${1:-}"
# Anchored: no slashes, no relative paths, no globs reaching another device.
case "$DISK" in
  disk[0-9]|disk[0-9][0-9]|disk[0-9][0-9][0-9]) ;;
  *) echo "teslcam-flash-write: refusing target '$DISK'" >&2; exit 2 ;;
esac

attr() { diskutil info -plist "$1" 2>/dev/null | plutil -extract "$2" raw -o - - 2>/dev/null; }

[ "$(attr "$DISK" VirtualOrPhysical)" = "Physical" ] \
  || { echo "teslcam-flash-write: $DISK is not a physical disk" >&2; exit 3; }
[ "$(attr "$DISK" WholeDisk)" = "true" ] \
  || { echo "teslcam-flash-write: $DISK is not a whole disk" >&2; exit 3; }

# Removable, by the only predicate that holds on a Mac: the built-in SD reader
# reports Internal=true, so "not internal" alone would reject legitimate cards
# and "internal" alone would accept the soldered SSD. Ejectable is what
# separates them.
INTERNAL="$(attr "$DISK" Internal)"
EJECTABLE="$(attr "$DISK" Ejectable)"
[ "$INTERNAL" = "false" ] || [ "$EJECTABLE" = "true" ] \
  || { echo "teslcam-flash-write: $DISK is not removable" >&2; exit 3; }

# Never the running system's disk, whatever the flags claim.
ROOT_WHOLE="$(diskutil info -plist / 2>/dev/null | plutil -extract ParentWholeDisk raw -o - - 2>/dev/null)"
[ -n "$ROOT_WHOLE" ] && [ "$DISK" = "$ROOT_WHOLE" ] \
  && { echo "teslcam-flash-write: $DISK carries the running system" >&2; exit 4; }
df / 2>/dev/null | grep -q "^/dev/${DISK}s" \
  && { echo "teslcam-flash-write: $DISK carries /" >&2; exit 4; }

exec /bin/dd of="/dev/r$DISK" bs=4m
HELPEREOF
chown root:wheel "$HELPER"
chmod 755 "$HELPER"
say "installed $HELPER"

# Written to a temp file and syntax-checked before being put in place: a
# malformed file in sudoers.d breaks sudo for everything, including the sudo
# needed to repair it.
TMP="$(mktemp)"
cat > "$TMP" <<SUDOEOF
# teslcam: flash a card without a password. The helper validates its own
# target (removable, whole, physical, not the boot disk) as root.
# Remove with: sudo $PWD/scripts/install-flash-helper.sh remove
%admin ALL=(root) NOPASSWD: $HELPER
SUDOEOF
chmod 440 "$TMP"
visudo -c -f "$TMP" >/dev/null || { rm -f "$TMP"; die "generated sudoers file is invalid — nothing installed"; }
install -m 440 -o root -g wheel "$TMP" "$SUDOERS"
rm -f "$TMP"
say "installed $SUDOERS (group admin, no password, this helper only)"

printf '\n\033[1;33mgranted:\033[0m any admin user on this Mac can now overwrite removable\n'
printf 'media without a password. Undo with: sudo %s remove\n' "$0"
