#!/usr/bin/env bash
# Assign an existing device to a user account by setting owner_user_id in the
# production SQLite DB over ssh. This is the one-time migration to hand the
# existing (operator-registered) device to the first real account.
#
#   scripts/claim-device.sh <device_id> <user_id>
#
# <user_id> is the Supabase user id (the JWT "sub" claim) — the same value the
# server stores in users.id after that account first authenticates. Have the
# account log in once before claiming so the users row exists (the device row
# does not require it, but it keeps things consistent).
#
# Target host is adri@vps; override with TESLCAM_VPS=user@host. DB lives at
# /var/lib/teslcam/server.db (owned by the teslcam service user).
set -euo pipefail

HOST="${TESLCAM_VPS:-adri@vps}"
DB="/var/lib/teslcam/server.db"

device_id="${1:-}"
user_id="${2:-}"
if [[ -z "$device_id" || -z "$user_id" ]]; then
  echo "usage: $0 <device_id> <user_id>   (target host: $HOST)" >&2
  exit 1
fi

# Single-quote the values for safe embedding in SQL; reject embedded quotes
# rather than try to escape them (device/user ids never contain them).
case "$device_id$user_id" in
  *"'"*) echo "error: device_id/user_id must not contain single quotes" >&2; exit 1 ;;
esac

# sqlite3 runs as the teslcam user so it can write the service-owned DB. UPDATE
# reports the change count; we verify a row actually matched.
ssh -t -o ConnectTimeout=10 "$HOST" "sudo -u teslcam sqlite3 '$DB' \
  \"UPDATE devices SET owner_user_id = '$user_id' WHERE device_id = '$device_id'; \
    SELECT 'rows changed: ' || changes();\""

echo "done — verify with:"
echo "  ssh $HOST sudo -u teslcam sqlite3 $DB \"SELECT device_id, owner_user_id FROM devices;\""
