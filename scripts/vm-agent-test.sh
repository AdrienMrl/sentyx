#!/bin/bash
# End-to-end test of the Phase-2 gadget agent inside the Lima dev VM, over the
# full software USB loop:
#
#   backing image -> teslcam-agent (configfs gadget, dummy_hcd UDC)
#     -> host /dev/sda -> mount ("the car") -> teslcam-sim writes sentry event
#     -> agent watcher/copy-out/upload -> teslcam-server -> verified vs journal
#
# Run from the Mac (re-execs itself in the VM via limactl) or inside the VM.
# Every wait is bounded; the whole test fails rather than hangs.
set -euo pipefail

VM=teslcam-dev
MNT=/mnt/tesla-agent
GADGET_NAME=teslcam-test
SERVER_ADDR=127.0.0.1:8091
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"

if [ "$(uname)" = "Darwin" ]; then
  exec limactl shell "$VM" -- bash "$PROJECT_DIR/scripts/vm-agent-test.sh" "$@"
fi

WORK="$(mktemp -d /tmp/teslcam-agent-test.XXXXXX)"
IMG="$WORK/backing.img"
AGENT_PID="" SERVER_PID=""

cleanup() {
  [ -n "$AGENT_PID" ] && sudo kill -TERM "$AGENT_PID" 2>/dev/null || true
  # Bounded wait so the agent can tear the gadget down itself.
  for _ in $(seq 1 20); do
    [ -n "$AGENT_PID" ] && sudo kill -0 "$AGENT_PID" 2>/dev/null || break
    sleep 0.25
  done
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  mountpoint -q "$MNT" && sudo umount "$MNT" || true
  # If the agent died without cleaning up, remove the gadget by hand.
  if [ -d "/sys/kernel/config/usb_gadget/$GADGET_NAME" ]; then
    g="/sys/kernel/config/usb_gadget/$GADGET_NAME"
    echo "" | sudo tee "$g/UDC" >/dev/null 2>&1 || true
    sudo rm -f "$g/configs/c.1/mass_storage.0"
    sudo rmdir "$g/configs/c.1/strings/0x409" "$g/configs/c.1" \
      "$g/functions/mass_storage.0" "$g/strings/0x409" "$g" 2>/dev/null || true
  fi
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo ">> building binaries"
cd "$PROJECT_DIR"
go build -o "$WORK/bin/" ./cmd/teslcam-agent ./cmd/teslcam-sim ./cmd/teslcam-server

echo ">> preparing fresh 256MB exFAT image at $IMG"
truncate -s 256M "$IMG"
mkfs.exfat -L TESLACAM "$IMG" >/dev/null

echo ">> loading gadget modules (dummy_hcd + libcomposite)"
mountpoint -q "$MNT" && sudo umount "$MNT" || true
lsmod | grep -q '^g_mass_storage' && sudo rmmod g_mass_storage || true
sudo modprobe dummy_hcd
sudo modprobe libcomposite

echo ">> starting server on $SERVER_ADDR (bearer-token auth on)"
mkdir -p "$WORK/data"
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$WORK/ingest.token"
"$WORK/bin/teslcam-server" -data "$WORK/data" -listen "$SERVER_ADDR" -quiet 8s \
  -token-file "$WORK/ingest.token" \
  >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
TOKEN="$(cat "$WORK/ingest.token")"

echo ">> checking auth: healthz open, everything else needs the token"
up=""
for _ in $(seq 1 20); do
  if curl -sf "http://$SERVER_ADDR/healthz" >/dev/null; then up=1; break; fi
  sleep 0.25
done
[ -n "$up" ] || fail "server did not come up within 5s"
code="$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary x \
  "http://$SERVER_ADDR/files/TeslaCam/SentryClips/x/y.mp4")"
[ "$code" = "401" ] || fail "unauthenticated PUT returned $code, want 401"
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer wrong" \
  "http://$SERVER_ADDR/events")"
[ "$code" = "401" ] || fail "wrong-token GET /events returned $code, want 401"
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" \
  "http://$SERVER_ADDR/events")"
[ "$code" = "200" ] || fail "authed GET /events returned $code, want 200"

echo ">> starting agent (gadget: $GADGET_NAME, udc: auto)"
mkdir -p "$WORK/spool"
sudo "$WORK/bin/teslcam-agent" -image "$IMG" -udc auto -gadget-name "$GADGET_NAME" \
  -interval 500ms -stable-polls 2 \
  -copy-to "$WORK/spool" -copy-prefix /TeslaCam/SentryClips \
  -device-id vm-test-pi -event-settle 5s \
  -post-to "http://$SERVER_ADDR" -token-file "$WORK/ingest.token" \
  >"$WORK/agent.log" 2>&1 &
AGENT_PID=$!

echo ">> waiting for the host side to enumerate the gadget as /dev/sda"
dev=""
for _ in $(seq 1 40); do
  if [ -b /dev/sda ]; then dev=/dev/sda; break; fi
  sudo kill -0 "$AGENT_PID" 2>/dev/null || { cat "$WORK/agent.log" >&2; fail "agent exited early"; }
  sleep 0.25
done
[ -n "$dev" ] || fail "gadget did not enumerate as /dev/sda within 10s"

sudo mkdir -p "$MNT"
sudo mount -o "uid=$(id -u),gid=$(id -g)" "$dev" "$MNT"
echo ">> car-side mount up: $IMG -> $dev -> $MNT"

echo ">> running simulator (3 scaled minutes, sentry after minute 1)"
"$WORK/bin/teslcam-sim" -mount "$MNT" -minutes 3 -mb-per-cam-min 4 -timescale 30 \
  -recent-cap 5 -sentry-after 1 -journal "$WORK/journal.json" >"$WORK/sim.log" 2>&1
sync  # flush the host page cache so the raw image reflects every write

echo ">> waiting for the server to complete the event"
done=""
for _ in $(seq 1 120); do
  if curl -sf -H "Authorization: Bearer $TOKEN" "http://$SERVER_ADDR/events" | grep -q '"completed_at"'; then done=1; break; fi
  sudo kill -0 "$AGENT_PID" 2>/dev/null || { cat "$WORK/agent.log" >&2; fail "agent exited early"; }
  sleep 1
done
[ -n "$done" ] || { tail -20 "$WORK/agent.log" "$WORK/server.log" >&2; fail "no completed event within 120s"; }

echo ">> verifying every journaled sentry file arrived byte-identical"
python3 - "$WORK/journal.json" "$WORK/data" <<'EOF'
import hashlib, json, sys, pathlib
journal, data = json.load(open(sys.argv[1])), pathlib.Path(sys.argv[2])
sentry = [r for r in journal
          if r["Path"].split("/")[:2] == ["TeslaCam", "SentryClips"] and not r["Deleted"]]
assert sentry, "journal contains no sentry files"
bad = []
for r in sentry:
    p = data / "blobs" / r["SHA256"][:2] / r["SHA256"]
    if not p.exists():
        bad.append(f"missing: {r['Path']}")
    elif hashlib.sha256(p.read_bytes()).hexdigest() != r["SHA256"]:
        bad.append(f"checksum mismatch: {r['Path']}")
if bad:
    sys.exit("FAIL:\n  " + "\n  ".join(bad))
print(f"   {len(sentry)} sentry files verified byte-for-byte")
EOF

echo ">> stopping agent; gadget must tear itself down"
sudo kill -TERM "$AGENT_PID"
gone=""
for _ in $(seq 1 40); do
  sudo kill -0 "$AGENT_PID" 2>/dev/null || { gone=1; break; }
  sleep 0.25
done
[ -n "$gone" ] || fail "agent did not exit within 10s of SIGTERM"
AGENT_PID=""
[ -d "/sys/kernel/config/usb_gadget/$GADGET_NAME" ] && fail "gadget dir still present after agent exit"
echo "   gadget removed cleanly"

echo "PASS: full gadget-agent loop verified (logs in $WORK)"
