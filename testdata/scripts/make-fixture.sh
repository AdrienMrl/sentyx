#!/bin/bash
# Creates a reproducible exFAT fixture image with a known TeslaCam layout,
# plus a .manifest file (sha256 + size per file) for golden tests.
#
# Usage: make-fixture.sh <output.img> [size-mb]
#
# Works on macOS (hdiutil + newfs_exfat) and Linux (mkfs.exfat + loop mount,
# needs passwordless sudo — e.g. inside the Lima dev VM).
set -euo pipefail

IMG="${1:?usage: make-fixture.sh <output.img> [size-mb]}"
SIZE_MB="${2:-64}"

# Deterministic file content: repeating tagged pattern, sized to spill across
# multiple clusters so cluster-chain walking is actually exercised.
gen() { # gen <tag> <bytes>
  local tag="$1" bytes="$2"
  # (yes gets SIGPIPE when head exits; don't let pipefail kill the script)
  (yes "TESLCAM-FIXTURE:${tag}" 2>/dev/null || true) | head -c "$bytes"
}

populate() { # populate <mountpoint>
  local mnt="$1"
  local ev="$mnt/TeslaCam/SentryClips/2026-07-03_12-00-00"
  mkdir -p "$ev" "$mnt/TeslaCam/RecentClips"
  for cam in front back left_repeater right_repeater; do
    # ~1.5 MB each: > one cluster at any sane cluster size for a small image
    gen "$cam" 1572864 > "$ev/2026-07-03_11-59-30-$cam.mp4"
  done
  printf '{"timestamp":"2026-07-03T12:00:00","city":"North Las Vegas","reason":"sentry_aware_object_detection","camera":"5"}' \
    > "$ev/event.json"
  gen thumb 4096 > "$ev/thumb.png"
  # A small file that fits in one cluster, and an empty dir edge case
  gen recent 512 > "$mnt/TeslaCam/RecentClips/2026-07-03_11-58-00-front.mp4"
  mkdir -p "$mnt/TeslaCam/SavedClips"
}

manifest() { # manifest <mountpoint> <outfile>
  local mnt="$1" out="$2"
  (cd "$mnt" && find TeslaCam -type f | sort | while read -r f; do
    printf '%s %s %s\n' "$(shasum -a 256 "$f" | cut -d' ' -f1)" "$(stat -f %z "$f" 2>/dev/null || stat -c %s "$f")" "$f"
  done) > "$out"
}

rm -f "$IMG"

case "$(uname)" in
Darwin)
  dd if=/dev/zero of="$IMG" bs=1048576 count="$SIZE_MB" status=none
  DEV=$(hdiutil attach -imagekey diskimage-class=CRawDiskImage -nomount "$IMG" | awk 'NR==1{print $1}')
  trap 'hdiutil detach "$DEV" >/dev/null 2>&1 || true' EXIT
  newfs_exfat -v TESLACAM "$DEV" >/dev/null
  hdiutil detach "$DEV" >/dev/null
  # Re-attach so macOS auto-mounts the freshly formatted volume
  ATTACH_OUT=$(hdiutil attach -imagekey diskimage-class=CRawDiskImage "$IMG")
  DEV=$(echo "$ATTACH_OUT" | awk 'NR==1{print $1}')
  MNT=$(echo "$ATTACH_OUT" | grep -o '/Volumes/.*$')
  populate "$MNT"
  # Strip AppleDouble ._* files — a Tesla never writes those, and they make
  # the manifest nondeterministic across macOS versions
  dot_clean -m "$MNT"
  find "$MNT/TeslaCam" -name '._*' -delete
  manifest "$MNT" "${IMG%.img}.manifest"
  hdiutil detach "$DEV" >/dev/null
  trap - EXIT
  ;;
Linux)
  dd if=/dev/zero of="$IMG" bs=1M count="$SIZE_MB" status=none
  mkfs.exfat -L TESLACAM "$IMG" >/dev/null
  MNT=$(mktemp -d)
  sudo mount -o loop,uid="$(id -u)",gid="$(id -g)" "$IMG" "$MNT"
  trap 'sudo umount "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true' EXIT
  populate "$MNT"
  manifest "$MNT" "${IMG%.img}.manifest"
  sudo umount "$MNT"
  rmdir "$MNT"
  trap - EXIT
  ;;
*)
  echo "unsupported OS: $(uname)" >&2; exit 1
  ;;
esac

echo "fixture: $IMG ($(du -h "$IMG" | cut -f1)), manifest: ${IMG%.img}.manifest"
