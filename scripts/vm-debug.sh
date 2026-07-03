#!/bin/bash
# Runs the teslcam-debug server inside the Lima dev VM with the full software
# USB gadget loop: backing image -> g_mass_storage gadget -> dummy_hcd host
# -> /dev/sda -> mounted at /mnt/tesla (the "car" side).
#
#   scripts/vm-debug.sh up      # set up gadget + mount + start server
#   scripts/vm-debug.sh down    # unmount + remove gadget
#
# Run from the Mac (it re-execs itself inside the VM via limactl) or directly
# inside the VM. The server listens on 0.0.0.0:8080; Lima forwards it, so the
# UI is at http://localhost:8080 on the Mac.
set -euo pipefail

VM=teslcam-dev
IMG="$HOME/teslcam-backing.img"
MNT=/mnt/tesla
# pwd -P: resolve symlinks (e.g. ~/code -> ~/dev) — the VM mounts only the
# physical path.
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"

if [ "$(uname)" = "Darwin" ]; then
  exec limactl shell "$VM" -- bash "$PROJECT_DIR/scripts/vm-debug.sh" "$@"
fi

cmd="${1:?usage: vm-debug.sh up|down}"

case "$cmd" in
up)
  if [ ! -f "$IMG" ]; then
    echo ">> creating backing image via make-fixture.sh"
    "$PROJECT_DIR/testdata/scripts/make-fixture.sh" "$IMG" 256
  fi
  # Tear down any existing gadget/mount first: modprobe on an already-loaded
  # g_mass_storage is a silent no-op, which would leave the gadget pointing
  # at whatever file a previous session used while we read a different one.
  mountpoint -q "$MNT" && sudo umount "$MNT"
  lsmod | grep -q '^g_mass_storage' && sudo rmmod g_mass_storage

  sudo modprobe dummy_hcd
  sudo modprobe g_mass_storage "file=$IMG" removable=1 stall=0

  # Bounded wait for the host side to enumerate the gadget as /dev/sda.
  dev=""
  for _ in $(seq 1 40); do
    if [ -b /dev/sda ]; then dev=/dev/sda; break; fi
    sleep 0.25
  done
  if [ -z "$dev" ]; then
    echo "gadget did not enumerate as /dev/sda within 10s" >&2
    exit 1
  fi

  sudo mkdir -p "$MNT"
  mountpoint -q "$MNT" || sudo mount -o "uid=$(id -u),gid=$(id -g)" "$dev" "$MNT"
  echo ">> gadget up: $IMG -> $dev -> $MNT"
  echo ">> starting server; UI at http://localhost:8080 on the Mac (ctrl-c to stop)"
  cd "$PROJECT_DIR"
  exec go run ./cmd/teslcam-debug -image "$IMG" -mount "$MNT" -listen 0.0.0.0:8080
  ;;
down)
  mountpoint -q "$MNT" && sudo umount "$MNT"
  sudo rmmod g_mass_storage 2>/dev/null || true
  echo ">> gadget down"
  ;;
*)
  echo "usage: vm-debug.sh up|down" >&2
  exit 1
  ;;
esac
