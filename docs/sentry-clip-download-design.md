# Pulling Sentry Clips Out of a Tesla — Design of the Proof of Concept

**Status:** working. `experiments/tesla-clip/dashcam` lists the car's Sentry
events, lets you pick one with the arrow keys, and writes the clip to the Mac as
an `.mp4`. First proven 2026-07-24 against VIN `<REDACTED_VEHICLE_IDENTIFIER>`.

This document explains *how* and *why* the pieces fit together, for whoever
picks this up later (including us). The terse operational reference —
flags, wire tables, quirks — lives in
[`sentry-clip-download-status.md`](./sentry-clip-download-status.md). The
archaeology of how the protocol was discovered lives in
[`tesla-app-traffic-capture.md`](./tesla-app-traffic-capture.md).

---

## 1. What problem this solves

The teslcam project's main line is a Raspberry Pi that pretends to be a USB
drive, so Sentry clips can be analysed the moment the car writes them. This POC
asks a different question:

> Can we get a clip out of the car *without* any hardware in the car?

The answer is yes, with a real caveat. There is **no clip API**. Clips never
exist on Tesla's servers. The phone app fetches them **peer-to-peer from the car
itself** over a WebRTC DataChannel, and that is the only path that exists. So
"download a clip" means: become a peer the car is willing to stream to.

That makes this a **pull** model — the car must be awake and online — and the
stream is a **724x470 preview**, not the 1280x960 file on the USB stick. It does
not replace the Pi. It is useful for ad-hoc retrieval, for testing the analyzer
against real footage, and as a fallback when no Pi is installed.

## 2. The shape of the system

Three processes, in two places. The split exists for exactly one reason (§5).

```
  ┌───────────────────────── Mac (laptop) ──────────────────────────┐
  │  secrets: ~/.teslcam/tesla-ble-key.pem · tesla-oauth.json       │
  │                             │                                   │
  │                             ▼                                   │
  │    ┌──────────────────────────────────────────┐                 │
  │    │ tesla-clip                               │──▶ ~/Downloads/ │
  │    │ signing · signaling · picker · ffmpeg    │        *.mp4    │
  │    └────┬────────────────────────────────┬────┘                 │
  └─────────┼────────────────────────────────┼──────────────────────┘
            │                                │
   (1) HTTPS: signed CarServer.Action   (2) ssh stdio (JSON lines):
       + read-only Hermes WS tap            offer/answer out,
       for STUN/TURN credentials            H.264 frames back
            │                                │
            ▼                                ▼
  ┌─────────────────────────────┐  ┌──────────────────────────────────┐
  │ Tesla cloud                 │  │ VPS 161.35.232.246 — public IP,  │
  │  owner-api /signed_command  │  │ holds no secrets                 │
  │  signaling.vn (Hermes WS)   │  │  tesla-peer                      │
  └──────────────┬──────────────┘  │  WebRTC endpoint + depacketizer  │
                 │                 └────────────────▲─────────────────┘
        (3) Hermes routing                          │
                 │              (4) WebRTC DataChannel, UDP 50007
                 ▼                                  │
  ┌─────────────────────────────┐                   │
  │ The car — behind carrier NAT│───────────────────┘
  └─────────────────────────────┘
```

The important property: **the car key and the account token never leave the
Mac**. `tesla-peer` speaks only WebRTC and knows nothing about Tesla. If the VPS
were compromised, the attacker would get video frames, not the ability to
command the car.

## 3. Two channels, and why both are needed

This is the part that confuses people (it confused us for three sessions).
There are two completely separate channels, and they carry different things:

| | Signaling channel | Media channel |
|---|---|---|
| **Carries** | "here is my WebRTC offer", "here is my answer" | the actual video |
| **Route** | Mac → Tesla cloud → car | car → VPS, *directly* |
| **Transport** | HTTPS `POST /signed_command` | WebRTC DataChannel over UDP |
| **Auth** | our P-256 key, enrolled in the car's VCSEC | DTLS, keyed by the SDP exchange |

Signaling is how two peers find each other. Once they have, the media flows
peer-to-peer and Tesla's cloud is out of the picture entirely. So we need
Tesla's cloud to make the introduction, and then we need to actually be
reachable — which is §5.

## 4. The signaling handshake

Everything rides Tesla's standard signed vehicle-command protocol, which the
open-source `teslamotors/vehicle-command` SDK already implements. We are not
inventing crypto; we are putting an unusual payload in a normal envelope.

```
RoutableMessage                      ← SDK signs this with our enrolled key
  └─ CarServer.Action
       └─ tag 8: webrtc_comms.Request
            └─ tag 1: WebRTCUniversalPayload (JSON)
                      {msg_type:"webrtc:signal", uid, type, session_description, v}

CarServer.Response
  └─ tag 19: webrtc_comms.Response
       └─ tag 1: WebRTCUniversalPayload (JSON)
```

`type` is `3` = offer, `4` = answer, `7` = ICE candidate.

```
  Mac (tesla-clip)   VPS (tesla-peer)   Tesla cloud        Car
        │                   │                │              │
        │ GET /api/1/vehicles/VIN            │              │
        │───────────────────────────────────▶│              │
        │ state: online                      │              │
        │◀───────────────────────────────────│              │
        │                   │                │              │
        │ open Hermes WS (jwt/hermes)        │              │
        │───────────────────────────────────▶│              │
        │ HermesServer hello (STUN/TURN creds)              │
        │◀───────────────────────────────────│              │
        │                   │                │              │
        │ spawn over ssh    │                │              │
        │──────────────────▶│                │              │
        │ SDP offer, host candidate 161.35.232.246:50007    │
        │◀──────────────────│                │              │
        │                   │                │              │
        │ StartSession (signed, DOMAIN_INFOTAINMENT)        │
        │───────────────────────────────────▶│              │
        │ SessionInfo                        │              │
        │◀───────────────────────────────────│              │
        │                   │                │              │
        │ ┌ loop: until the reply is the SDP answer ──────────────┐
        │ │ signed_command: webrtc_comms offer (type 3) ─────────▶│
        │ │                            Hermes routing ──────────▶ │
        │ │                          one queued message ◀──────── │
        │ │ ◀─ type 4 (answer)  OR  type 7 (candidate)            │
        │ └──────────────── give up after -offer-tries ───────────┘
        │                   │                │              │
        │ answer SDP        │                │              │
        │──────────────────▶│                │              │
        │                   │◀─ STUN connectivity check ────│
        │                   │   ICE learns the car's address as a
        │                   │   peer-reflexive candidate from this packet
        │ ice=connected, DataChannel open    │              │
        │◀──────────────────│                │              │
```

Two behaviours of the command proxy shape the code:

1. **One message per request.** Each `signed_command` returns exactly *one*
   queued signaling message, and only an offer elicits a reply at all. Probing
   payload types 1, 2, 5, 6 and 8 produced nothing but timeouts, and a bare
   `type:7` candidate returns HTTP 503 because the car never answers a one-way
   notification.
2. **The reply is a race.** Sometimes it is the SDP answer, sometimes an ICE
   candidate the car emitted first. Re-offering restarts the car's negotiation
   with a fresh ICE ufrag, so the fix is simply to re-offer with a new `uid`
   until the answer is what comes back (`-offer-tries`, default 4).

## 5. Why a machine with a public IP is mandatory

This was *the* blocker, and it is worth understanding because it is not obvious.

The car sits behind carrier NAT and only ever advertises a **private host
candidate** (`192.168.x.x` — its own modem-side LAN). Combined with the
one-message-per-request limit above, we can never collect its public address.
That single fact kills the two obvious approaches:

```
  ✗  Laptop behind home NAT
     car ──STUN check──▶ home router ─────▶ ✗ DROPPED
     The NAT mapping is address-restricted. The car's address was never
     seen outbound, so no pinhole exists for it.

  ✗  TURN relay
     car ──packet──▶ Cloudflare TURN relay ─▶ ✗ DROPPED
     A relay forwards only from peers we installed a permission for,
     and installing one needs the very address we cannot learn.

  ✓  Peer on a public IP
     car ──STUN check──▶ 161.35.232.246:50007 ─▶ ✓ ACCEPTED
     No NAT, no permission required. ICE creates a peer-reflexive
     candidate for the car from that very packet.
```

The insight: **we never need the car's candidate if the car can reach us
unprompted.** Standard ICE learns a remote peer's address from an inbound
connectivity check that passes the integrity check — and the car has our
credentials from the offer. One open UDP port is the entire requirement. With
`tesla-peer` on the droplet, ICE went `checking → connected` on the first
attempt after weeks of `failed`.

Practical note: DigitalOcean's cloud firewall blocks **all** inbound UDP by
default even when the droplet's own iptables is wide open. The rule that makes
this work is Custom/UDP/50007 from `0.0.0.0/0` + `::/0` on the droplet's
firewall.

## 6. The DataChannel protocol

Once open, the protocol is startlingly simple: **plain newline-terminated text
commands**, each prefixed with the 36-byte lowercase session UUID — the same
value the offer carried in `uid`.

```
┌────────────────────────────────────┬──────────────────────────┐
│ 36 bytes: session UUID (ASCII)     │ "list_events\n"          │
└────────────────────────────────────┴──────────────────────────┘
```

Commands: `list_events`, `list_photobooth_metadata`, `metadata:<path>`,
`play_event:<path>:<startMs>:<durationMs>:<camera>`, `play_keyframes:<same>`,
`delete:<path>`. Paths look like `SentryClips/2026-07-23_18-35-27`; cameras are
`front`, `left_pillar`, `left_repeater`, `right_pillar`, `right_repeater`,
`back`.

Replies come back as a packet stream. Packets **straddle DataChannel message
boundaries**, so the parser must be a running state machine, not a per-message
decoder:

```
┌───────────────┬─────────┬──────────────────┬─────────────────────┐
│ 00 00 00 01   │ nal     │ length (u32 BE)  │ payload             │
│ (start code)  │ 1 byte  │ 4 bytes          │ `length` bytes      │
└───────────────┴─────────┴──────────────────┴─────────────────────┘
```

| nal | meaning |
|-----|---------|
| `0x1e` | JSON control reply (event list, event metadata). Preceded by a 4-byte inner length; `metadata:` also appends a PNG thumbnail after the JSON. |
| `0x1f` | 21-byte timing record: base epoch ms (8), frame index (4), presentation offset ms (8), flag (1). Used to derive the true frame rate. |
| `0x1b` | video frame — payload is **already Annex-B H.264**, opening with its own `00 00 00 01` SPS. |
| `0x1d` | 17-byte marker roughly every 40 s of video; unused. |

Because `0x1b` payloads are already Annex-B, assembling a playable file is just
concatenation. No transcoding, no bitstream surgery:

```
  DataChannel bytes
        │
        ▼
  strip the 36-byte session UUID          (present on every message)
        │
        ▼
  packet state machine                    (packets straddle messages,
        │                                  so this cannot be per-message)
        ├── 0x1b video ──▶ payload is already Annex-B ──▶ base64 ──▶ ssh
        │                                                        │
        ├── 0x1f PTS   ──▶ remember last presentation offset ─┐   │
        ├── 0x1e JSON  ──▶ control reply ──▶ picker / UI      │   │
        └── 0x1d mark  ──▶ ignored                            │   │
                                                              ▼   ▼
                            fps = (frames-1) × 1000 / lastPTS   clip.h264
                                             │                    │
                                             └────────┬───────────┘
                                                      ▼
                                        ffmpeg -r <fps> -c copy
                                                      │
                                                      ▼
                                                  clip.mp4
```

The frame rate is derived from the PTS records rather than assumed — measured
23.74 fps on one clip and 23.92 on another, so a hardcoded 24 would drift.

## 7. End-to-end walk-through

```
   You            tesla-clip (Mac)                  Car (via cloud + peer)
    │                   │                                    │
    │ ./dashcam         │                                    │
    │──────────────────▶│                                    │
    │                   │ negotiate (see §4 and §5)          │
    │                   │═══════════════════════════════════▶│
    │                   │ list_events                        │
    │                   │───────────────────────────────────▶│
    │                   │ SentryClips: [ ... ]               │
    │                   │◀───────────────────────────────────│
    │ arrow-key picker  │                                    │
    │◀──────────────────│                                    │
    │ choose an event   │                                    │
    │──────────────────▶│                                    │
    │                   │ metadata:SentryClips/…             │
    │                   │───────────────────────────────────▶│
    │                   │ duration · city · reason · cameras │
    │                   │◀───────────────────────────────────│
    │ camera picker     │                                    │
    │◀──────────────────│                                    │
    │ choose a camera   │                                    │
    │──────────────────▶│                                    │
    │                   │ play_event:…:0:durationMs:front    │
    │                   │───────────────────────────────────▶│
    │                   │                                    │
    │                   │ ┌ loop: until complete / quiet / Ctrl-C ─┐
    │                   │ │            0x1f PTS + 0x1b frame ◀──── │
    │ "1152 frames,     │ │                                        │
    │  48.1s / 59.7s" ◀─┤ └────────────────────────────────────────┘
    │                   │                                    │
    │                   │ ffmpeg mux (own context, survives Ctrl-C)
    │ open the .mp4     │                                    │
    │◀──────────────────│                                    │
```

**Ctrl-C** cancels collection but deliberately still muxes and opens what
arrived; the mux runs on its own context so a cancelled one cannot kill ffmpeg
mid-write. A second Ctrl-C aborts. Inside the picker, raw mode means Ctrl-C
arrives as byte `0x03` rather than a signal, and is handled there.

## 8. Clip length is set by the car, not by us

A recurring surprise: two events recorded a minute apart differ in length by
10×. That is genuine. We download whatever `TotalDurationMs` the car reports in
the `metadata:` reply:

| event | reported duration |
|---|---|
| `2026-07-23_18-35-27` | 59.7 s |
| `2026-07-23_18-34-17` | 644.3 s |

Sentry folders hold a variable number of the car's 1-minute segments, and events
triggered close together end up sharing them.

There is a second, real truncation risk: the car pauses between stored segments,
so collection stops after `-quiet-gap` (8 s) of silence. The UI now prints
`got / expected` throughout and names the stop reason, so a short clip is never
silently mistaken for a complete one.

## 9. Roads not taken, and why

Recording these so nobody re-walks them.

| Approach | Outcome |
|---|---|
| **Signaling over BLE** | The key, the session and arbitrary `CarServer.Action` sends all work over BLE — but the carserver never returns a `webrtc_comms` response. WebRTC is cloud-mediated. Three iterations ruled out msg_type and the 1024-byte BLE message cap as causes. |
| **Fleet API** | Tested properly on 2026-07-25 and **ruled out** — see §9.1. The official API relays ordinary signed commands but rejects the `webrtc_comms` action. |
| **Sending the offer over the Hermes WS** | Would remove the one-message-per-request limit, because the car's answer *and* all its candidates would route back to our socket. Blocked on the `ProtoCommandMessage` envelope (vehicle topic/routing), which was not resolvable from static decompilation. Now off the critical path — but see §10. |
| **TURN relay / home-NAT srflx** | Impossible without the car's address; see §5. |
| **Polling the proxy for more messages** | Only offers get replies, and each offer resets negotiation with a new ufrag — so queued candidates can never be drained this way. |

### 9.1 Fleet API cannot carry this — measured, not assumed

The account token above is captured out of Tesla's own Android app with mitmproxy.
No shipped product can mint that, so the obvious question is whether Tesla's
official **Fleet API** can replace it. On 2026-07-25 we ran the experiment rather
than guessing, using a registered third-party app (all Fleet scopes, including
`vehicle_cmds`) and the same P-256 key already enrolled in the car's VCSEC.

Everything up to the payload works:

| Step, all on `fleet-api.prd.na.vn.cloud.tesla.com` | Result |
|---|---|
| Third-party OAuth (authorization code → access + refresh) | ✅ |
| `GET /api/1/vehicles` | ✅ 200 (the same call 412s on `owner-api`) |
| Wake the vehicle | ✅ `state: online` |
| `StartSession`, `DOMAIN_INFOTAINMENT`, signed with our enrolled key | ✅ session established |
| `signed_command` carrying a CarServer **Ping** (authenticated no-op) | ✅ accepted |
| `signed_command` carrying the **`webrtc_comms` offer** (tag 8) | ❌ `{"error":"Unauthorized"}` |

The last two rows are the whole finding. Identical host, identical token,
identical key, identical session — only the action inside the envelope differs.
So Tesla's Fleet proxy is **not** a blind relay of opaque signed bytes: it
inspects the `CarServer.Action` and refuses `webrtc_comms` for third parties.
This is not a missing scope, not an unpaired virtual key, and not an
unrecognised key — any of those would have failed the Ping too.

The obvious follow-up — carry the *legitimate* Fleet token to the *first-party*
host, which we know does relay `webrtc_comms` — fails at the auth layer. The two
surfaces are partitioned by token audience, in both directions:

| Host | Token | `webrtc_comms` offer |
|---|---|---|
| `owner-api` | `ownerapi` | ✅ the car answers (2026-07-24) |
| `owner-api` | Fleet | ❌ HTTP 401 `invalid bearer token` |
| `fleet-api` | `ownerapi` | ❌ HTTP 401 `invalid bearer token` |
| `fleet-api` | Fleet | auth ✅, Ping ✅, offer ❌ `Unauthorized` |

The 401s are returned by `/api/1/vehicles/{vin}/signed_command` itself (and by
`/api/1/users/jwt/hermes`) before any payload parsing, so this is not something a
better-formed request can get around. Whether Tesla enforces the block by action
or by audience is therefore moot: **no credential a user can legitimately grant a
third party reaches the relay that carries clip signaling.**

Consequence: **the clip path is first-party-only.** It works with an `ownerapi`
token and cannot be carried by anything a third-party app is allowed to hold.
The Pi therefore stays the product's clip pipeline; Fleet API remains useful for
account linking, vehicle state, and ordinary commands, but not for this.

And the `ownerapi` token itself can no longer be obtained. The community PKCE
flow (`client_id=ownerapi`, `redirect_uri=https://auth.tesla.com/void/callback`,
implemented in `internal/teslaauth`) was retested on 2026-07-25 and Tesla now
rejects it at the authorize step:

> The 'redirect_uri' supplied is not registered for this 'client_id'.

The client still exists — it is what Tesla's own app uses — but its registered
redirect is a URI scheme owned by that app. Claiming it would mean shipping a
client that impersonates Tesla's, which is not a path we will take. So the three
routes to a clip-capable credential are: capture one from Tesla's app with
mitmproxy (works, personal use, unshippable), the retired `void/callback` flow
(closed), or impersonation (rejected). This is why the no-hardware clip path
cannot become a product.

Reproduce with:

```sh
tesla-clip -api-host fleet-api.prd.na.vn.cloud.tesla.com -listen-ws=false \
           -tokens ~/.teslcam/tesla-fleet.json -vin <VIN> -ping   # succeeds
tesla-clip -api-host fleet-api.prd.na.vn.cloud.tesla.com -listen-ws=false \
           -tokens ~/.teslcam/tesla-fleet.json -vin <VIN>         # Unauthorized
```

Note that `owner-api` is half-retired: `GET /api/1/vehicles` now returns HTTP 412
("only available on fleetapi"), while `GET /api/1/vehicles/{vin}`,
`POST /api/1/users/jwt/hermes` and `POST /api/1/vehicles/{vin}/signed_command`
all still work with an owner token. Do not read the 412 as "the owner API is
gone".

## 10. Known limits and where to go next

- **Pull, not push.** Needs the car awake and online. It cannot do the Pi's
  real-time on-write capture, which remains the main line.
- **Preview resolution.** 724x470, not the 1280x960 on the USB drive. This is
  what the car offers remotely — the app's own Save button gets the same thing.
- **One viewer at a time.** The car answers `rejected: other_phone_connected`
  when a recent session still holds the slot.
- **Manual token refresh.** `~/.teslcam/tesla-oauth.json` lasts ~8 h and holds a
  refresh token that nothing uses yet. Cheapest real improvement.
- **The Hermes WS envelope** is the one piece of unfinished reverse engineering.
  It would delete the offer-retry loop, and — more interestingly — it is the
  same envelope needed for any push-style vehicle messaging.
- **Encryption.** This car streams plaintext. Firmware 2026.20+ on Ryzen MCUs
  encrypts clips by default; the effect on this stream is untested.

## 11. Where the code lives

```
experiments/tesla-clip/
├── dashcam          one-command wrapper (env: TESLA_VIN, TESLCAM_PROXY, …)
├── main.go          flags, transport setup, signal handling, one-shot -request mode
├── session.go       signed webrtc_comms signaling over the command proxy
├── peer.go          rtcPeer interface: in-process pion, or tesla-peer over stdio
├── ui.go            arrow-key browser, frame collection, ffmpeg mux, open
├── signaling.go     Hermes WS tap (ICE servers, candidate scanning)
└── peer/main.go     tesla-peer: public-IP WebRTC endpoint + depacketizer
```

Redeploy the peer after changing it:

```sh
GOOS=linux GOARCH=amd64 go build -o /tmp/tesla-peer ./peer/
scp /tmp/tesla-peer adri@vps:~/tesla-peer
```

Prerequisites for a run: a fresh `~/.teslcam/tesla-oauth.json`, the enrolled key
at `~/.teslcam/tesla-ble-key.pem` (created by `experiments/tesla-ble-pair`),
`ffmpeg`, the car awake and online, and UDP 50007 open inbound on the proxy host.

---

*Scope note: this is interoperability work on the author's own Tesla account and
own vehicle, retrieving the author's own dashcam footage.*
