# LTE USB dongle (ASR Microelectronics "Mobile Router")

USB stick that provides metered LTE as an upload fallback when the car is away
from known Wi-Fi. **Data on this link is very expensive** — the whole design
below exists to guarantee that only sentry-clip uploads (and tiny heartbeats)
can ever use it. SIM: Hologram (roams on T-Mobile in the US).

## Device basics

- USB ID `2ecc:3012`, `Asrmicro` / "Mobile Router". Enumerates as an RNDIS
  ethernet device (`rndis_host` driver) → interface **`eth1`** on the Pi.
- Runs its own router at **`192.168.8.1`** (subnet `192.168.8.0/24`, DHCP,
  NAT to LTE; the WAN side is CGNAT — `10.x` address, inbound impossible).
- Web UI: lighttpd behind the vanity vhost **`mobile.router`** — requests to
  the bare IP get a 301 to `http://mobile.router/`, so API calls must send
  `Host: mobile.router` (curl: `-H "Host: mobile.router"`).
- Admin password: `admin` (web UI login; username fixed to `admin`).
- DHCP hands out DNS `8.8.8.8` / `8.8.4.4`; the dongle also runs a DNS relay
  on `192.168.8.1:53`.

### RNDIS wedge quirk (important)

On cold boot the RNDIS link can come up **wedged**: interface present with
carrier, but 0 packets received and `rx_errors` climbing continuously (500k+),
DHCP timing out, gateway ARP unanswered. No error appears in dmesg. The fix is
a USB re-bind:

```sh
echo 1-1.2 | sudo tee /sys/bus/usb/drivers/usb/unbind   # port of the dongle
sleep 3
echo 1-1.2 | sudo tee /sys/bus/usb/drivers/usb/bind     # may say "busy" — fine
```

`teslcam-lte-watchdog.timer` (every 2 min) automates exactly this: it resets
the USB device only when gateway ping fails **and** `rx_errors > 100`, so an
absent/powered-off dongle is never touched.

## API (reverse-engineered from the web UI JS)

The UI is a ubus-over-HTTP bridge: `GET/POST
http://192.168.8.1/api.cgi?path=<obj>&method=<m>&timeout=<s>` with a JSON
body, JSON responses. Unauthenticated calls return
`{"system_err": "session no exist"}`.

### Login (challenge-response)

1. `POST /api.cgi?path=account&method=get_rand` body
   `{"type":"admin","user_id":"<8 random [a-z0-9] chars>"}` →
   `{"result":0,"rand":"<nonce>"}`
2. `POST /api.cgi?path=account&method=login` body
   `{"type":"admin","username":"admin",
     "password": md5hex(nonce + lowercase(password)), "user_id": <same>}`
   → `{"result":3}` on success (`LOGIN_RESULT`: 0 user+pwd err, 1 pwd err,
   2 user err, **3 OK**, 4/5 rand err, **6 locked out after too many tries**).
3. Session = `CGISID` cookie from the response; send it on every later call.
   Note the cookie's domain is `mobile.router`, so standard cookie jars keyed
   on the IP URL refuse to store it — handle it manually.

The password is lowercased before hashing (firmware quirk). Beware the
lockout (result 6): don't brute-force retries.

### Useful calls (session required)

| path.method | returns |
|---|---|
| `cm.get_link_context` | signal + connection: `celluar_basic_info` (sic) `{rssi, network_name, roaming_network_name, roaming}`, `signal_info {rat: "4g", level: 0-5}`, `contextlist[] {connection_status, ipv4_ip, apn, ipv4_dns1/2, ipv4_gateway}` |
| `sim.get_sim_status` | PIN/PUK state (`pin_puk.sim_status` 1 = ready) |
| `statistics.stat_get_traffic_transport_status` | live tx/rx activity |
| `router.get_bat_info` | battery info (empty on this stick) |

Other ubus objects seen in the UI: `wireless`, `sms`, `ota`, `aoc`, `router`.
`GET /file.cgi` exists for file ops. RSSI observed ~42-43 with level 4/5.

`teslcam-lte -gateway http://192.168.8.1 -password admin -iface eth1` prints
link context + byte counters as JSON (client: `internal/lte/dongle.go`).

## Metered-link protection (how LTE is kept upload-only)

Three independent layers, baked into `scripts/build-image.sh` and live on the
prototype Pi:

1. **No default route, policy table instead.** NM profile `lte-dongle`
   (`/etc/NetworkManager/system-connections/lte-dongle.nmconnection`):
   `ipv4.never-default yes`, `ipv6.method disabled` (a carrier RA could
   otherwise install a v6 default route past `never-default`), default route
   `via 192.168.8.1` in **table 101**, rule `from 192.168.8.0/24 lookup 101`
   (prio 30100). Only a socket **bound to eth1's address** matches the rule —
   which only the agent's fallback dialer does (`internal/lte/dialer.go`,
   agent flags `-lte-iface eth1 -lte-dns 8.8.8.8:53 -lte-dial-timeout 10s`).
   `ping -I eth1` (device bind) does NOT route — bind to the *address*
   (`ping -I 192.168.8.x`, `curl --interface <addr>`) to test.
   **Table 100 is taken by the WireGuard setup on the prototype — don't reuse.**
   `/etc/NetworkManager/conf.d/teslcam-lte.conf` sets `no-auto-default=eth1`
   so NM never regenerates a "Wired connection 1" that would DHCP a default
   route onto the metered link.
2. **nftables egress allowlist** (`/etc/teslcam/lte-guard.nft`, loaded by
   `teslcam-lte-guard.service`): out of `eth1` only the dongle LAN, the
   teslcam server `161.35.232.246:443`, and DNS `8.8.8.8/8.8.4.4:53` are
   allowed; everything else is counter-logged and dropped, and the forward
   chain blocks routing anything (Docker, WireGuard) out the dongle.
3. **No background chatter:** `apt-daily{,-upgrade}.timer`, `man-db.timer`
   disabled (already the case in the field image; now also on the prototype).

Failover is an *application* decision: the dialer tries the default route
(Wi-Fi) first with a 10s timeout and only then redials bound to eth1, so LTE
is used exactly when Wi-Fi is down and only by upload/heartbeat traffic.
Verified end-to-end 2026-07-23 (server blocked on wlan0 via a temp nft rule):
`internal/lte/real_test.go` → DNS over LTE, TLS to `/healthz`, 200 in ~1.5s
after the 8s primary timeout.

## Open items

- LTE byte counters + signal (`LinkContext`) in the heartbeat, so the app can
  show metered usage per day and catch leaks early.
- Optional daily LTE byte budget in the agent (halt uploads over LTE once
  exceeded; spool keeps everything).
