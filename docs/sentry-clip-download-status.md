# Sentry Clip Download — DONE, and how it works

**Goal (met 2026-07-24):** download a Sentry clip from the car with our own
client, no emulator shortcut. Verified: a 10.7-minute Sentry event pulled as a
playable `.mp4` (`~/Downloads/tesla-sentryclips-2026-07-23_18-34-17-front.mp4`,
15294 frames, 23.74 fps, 62 MB), plus a 10-second front-camera clip before it.

Deep protocol history lives in `tesla-app-traffic-capture.md` (session logs 1–3);
this doc is the working reference for the finished pipeline.

---

## Use it

```sh
cd experiments/tesla-clip
./dashcam                      # arrow-key clip browser -> .mp4 in ~/Downloads
./dashcam -pick 0 -camera back # non-interactive (scripting / testing)
```

Env overrides: `TESLA_VIN`, `TESLCAM_PROXY` (default `adri@vps`),
`TESLCAM_PROXY_UDP_PORT` (default 50007), `TESLCAM_OUT_DIR`.

Preconditions: fresh `~/.teslcam/tesla-oauth.json` (tokens last ~8 h), the
enrolled key at `~/.teslcam/tesla-ble-key.pem`, `ffmpeg`, car awake + online,
and UDP 50007 open inbound on the proxy host.

## The working pipeline

```
[our P-256 key, enrolled in car VCSEC]  ✅
        │  signs
        ▼
[signed session over CLOUD]  ✅  POST owner-api…/api/1/vehicles/{vin}/signed_command
        │                          (plain `ownerapi` OAuth token is accepted;
        ▼                           fleet-api hosts reject it)
[webrtc_comms offer -> SDP answer]  ✅  CarServer.Action tag8 / Response tag19
        │
        ▼
[ICE via a PUBLIC-IP peer]  ✅  tesla-peer on the VPS, UDP 50007
        │                       (the car dials us; see "Why a public IP")
        ▼
[DataChannel "ordered"]  ✅  plain-text commands, 36-byte session-uuid prefix
        │
        ▼
[depacketize 00 00 00 01 | nal | len32 | payload]  ✅  in the peer
        │
        ▼
[frames streamed to the Mac -> ffmpeg -c copy -> .mp4]  ✅
```

Nothing sensitive goes to the proxy: the peer speaks only WebRTC, and the car
key and account token stay on the laptop. The video streams up the ssh pipe, so
no clip is left on the VPS.

## Why a public IP is required

The car advertises only a **private host candidate** (`192.168.x.x`, its
modem-side LAN), and Tesla's command proxy returns exactly one signaling message
per request, so its other candidates can never be collected. With no usable
remote candidate:

- a **TURN relay cannot help** — a relay only forwards packets from peers we have
  installed a permission for, and installing one needs the address we cannot learn;
- a **home-NAT srflx address cannot help** — the pinhole is address-restricted, so
  the car's packets are dropped.

A public host candidate needs neither: the car's connectivity check arrives
directly and ICE learns its address as a peer-reflexive candidate from that very
packet. This was the whole blocker — with `tesla-peer` on the droplet, ICE went
`checking -> connected` on the first try. One open UDP port is the entire
requirement. (DigitalOcean's cloud firewall blocks all inbound UDP by default;
the droplet's own iptables was already open.)

## The DataChannel protocol

Channel label **`ordered`**, ordered delivery (`f1.java:986-988`). Every frame in
both directions is prefixed with the **36-byte lowercase session UUID**, which is
the same value the offer carries in `uid` (`f1.java:196` and `:1457` both use
`f1.sessionID`).

Requests are plain newline-terminated text (`o1.java:744`,
`RNH264StreamEvents.java:135`, `f1.java:1629`):

```
list_events\n
list_photobooth_metadata\n
metadata:<eventPath>[,<eventPath>...]\n
play_event:<eventPath>:<startMsRel>:<durationMs>:<camera>[:<seq>]\n
play_keyframes:<same args>\n
delete:<eventPath>\n
```

`eventPath` is `<folder>/<EventName>`, e.g. `SentryClips/2026-07-23_18-35-27`.
Cameras (`h.java`): `front`, `left_pillar`, `left_repeater`, `right_pillar`,
`right_repeater`, `back`. On open the app sends
`list_events\nlist_photobooth_metadata\n`.

Replies are a packet stream (`f1.java:410-437`), confirmed byte-for-byte against
a live capture:

```
00 00 00 01 | <nal byte> | <4-byte big-endian payload length> | <payload>
```

Packets straddle DataChannel messages, so the parser must be a running state
machine. NAL byte (the app masks it with `& 31`, `o.java:28`):

| byte | meaning |
|------|---------|
| `0x1e` | JSON control reply, preceded by a 4-byte inner length. `metadata:` also appends a PNG thumbnail after the JSON. |
| `0x1f` | 21-byte timing record: 8-byte base epoch ms, 4-byte frame index, 8-byte presentation offset ms, 1 flag byte. Used to derive the true frame rate. |
| `0x1b` | video frame; payload is **already Annex-B H.264** (opens with its own `00 00 00 01` SPS), so frames concatenate directly into a playable stream. |
| `0x1d` | 17-byte marker every ~40 s of video; unused. |

Also special-cased inbound: the four bytes `12 34 56 78` mean "close", and a
`pong:` prefix is a keepalive reply. The app never sends pings itself, so there
is no DataChannel keepalive to imitate.

`list_events` reply:

```json
{"SentryClips":[{"EventName":"2026-07-23_18-35-27"}, ...],
 "SavedClips":[],"EmergencyClips":[],"InternalClips":[],"Error":""}
```

`metadata:<path>` reply:

```json
{"Name":"SentryClips/2026-07-23_18-35-27","EventEpochTimeMs":1784831672000,
 "TotalDurationMs":59749,"EarliestClipTimeMs":1784831645000,
 "EventMetadata":{"City":"Las Vegas","Est_lat":36.2023,"Est_lon":-115.243,
   "Reason":"sentry_aware_object_detection","Camera":6},
 "TotalAvailableCameras":["front","left_pillar","left_repeater",
   "right_pillar","right_repeater","back"]}
```

Note the stream is a **724x470 preview**, not the 1280x960 file on the USB drive
— this is the same stream the app's Save/Download button captures.

## Signaling quirks that the tool works around

- **One message per request.** `signed_command` returns exactly one queued
  signaling payload, and only an offer elicits a reply at all (types 1, 2, 5, 6, 8
  were probed: all time out, and a bare `type:7` candidate 503s because the car
  never answers a one-way notification).
- **Answer/candidate race.** The single reply to an offer is sometimes the SDP
  answer and sometimes an ICE candidate. Re-offering restarts negotiation with a
  fresh ufrag, so `-offer-tries` (default 4) simply re-offers with a new uid until
  the answer is what comes back.
- **`other_phone_connected`.** The car serves one dashcam viewer at a time; a
  recent session of ours holds the slot for a while. Surfaced via
  `WebRTCUniversalPayload.rejected`/`reason`.
- **TURN credentials come free.** The `HermesServer` hello repeats on
  `wss://signaling.vn.teslamotors.com/v1/mobile` every ~5 s with Cloudflare
  STUN/TURN creds. The tool parses them (useful for a non-public-IP peer), though
  the public-IP path does not need them.
- **owner-api is half-retired.** `GET /api/1/vehicles` → 412 "only available on
  fleetapi", but `GET /api/1/vehicles/{vin}`, `POST /api/1/users/jwt/hermes` and
  `POST /api/1/vehicles/{vin}/signed_command` all still work with the owner token.
  No Fleet partner registration was ever needed.
- **Fleet API cannot replace the ownerapi token.** Measured 2026-07-25 with a
  registered third-party app holding every Fleet scope: on `fleet-api`, a signed
  CarServer **Ping** is accepted but the **`webrtc_comms` offer** returns
  `{"error":"Unauthorized"}` — same host, token, key and session, only the action
  inside the envelope differs. The proxy inspects the action and blocks this one
  for third parties. Evidence table: `sentry-clip-download-design.md` §9.1.
  This makes the clip path **first-party-only**, so the Pi stays the clip pipeline.

## Layout

- `experiments/tesla-clip/dashcam` — one-command wrapper.
- `main.go` — flags, transport setup, one-shot `-request` mode.
- `session.go` — signed `webrtc_comms` signaling over the command proxy.
- `peer.go` — the `rtcPeer` interface: in-process pion, or `tesla-peer` over stdio.
- `ui.go` — arrow-key browser, frame collection, ffmpeg mux.
- `signaling.go` — Hermes WS tap (ICE servers, candidate scanning).
- `peer/main.go` — `tesla-peer`: the public-IP WebRTC endpoint + depacketizer.

Deploy the peer after changes:

```sh
GOOS=linux GOARCH=amd64 go build -o /tmp/tesla-peer ./peer/
scp /tmp/tesla-peer adri@vps:~/tesla-peer
```

## Still open

- **A pull model, not a replacement for the Pi.** This needs the car awake and
  online and streams a downscaled preview; it cannot do the Pi's real-time
  on-write capture of full-resolution clips.
- **The Hermes WS envelope** is still unreverse-engineered. It is no longer on the
  critical path, but sending the offer over the WS would remove the
  one-message-per-request limitation (and with it the offer-retry loop).
- **Token refresh** is manual; `~/.teslcam/tesla-oauth.json` has a refresh token
  that nothing uses yet.
- **Encrypted clips.** This car is on firmware that streams plaintext; 2026.20+
  on Ryzen MCUs encrypts by default (see the encryption note in memory).

---

## Historical: session log — 2026-07-24 (part 4)


The part-3 wall ("no captured Hermes command to validate against") is **gone**.
The Tesla command proxy does the Hermes routing for us.

**Endpoint reality check (owner-api is half-retired):**
- `GET /api/1/vehicles` → HTTP 412 "only available on fleetapi" ❌
- `GET /api/1/vehicles/{vin}` → 200, `state: online` ✅
- `POST /api/1/users/jwt/hermes` → 200, mints the WS token ✅
- `POST /api/1/vehicles/{vin}/signed_command` → **works with our `ownerapi`
  token** ✅ (fleet-api hosts reject that token: `invalid bearer token`)

So no Fleet partner registration is needed — the fallback the old doc worried
about never came up.

**What the car returns over the cloud** (`webrtc_comms.Response`, tag 19):

```json
{"msg_type":"webrtc:signal_response","type":4,"v":2,
 "uid":"<echoes our uid>",
 "session_description":{"type":"answer","sdp":"v=0\r\no=- ... a=setup:active
   a=ice-options:trickle a=fingerprint:sha-256 ... a=sctp-port:5000
   a=max-message-size:262144\r\n"}}
```

A real answer for a `webrtc-datachannel` m-line. Our enrolled key is sufficient —
no phone-key/app privilege is missing.

**Rejection semantics observed.** One attempt returned
`rejected: reason=other_phone_connected` — the car serves **one dashcam viewer
at a time**, and a prior session of ours still held the slot. This is the
`rejected`/`reason` decision field part 2 flagged, seen live. It clears on its own
after a short while.

**TURN credentials are obtainable without any capture.** The `HermesServer` hello
repeats on the WS every ~5 s and carries the full ICE config, including
credentialed Cloudflare TURN:

```json
{"expiration":…, "connection_timeout":180000, "ping_frequency":60000,
 "ice_servers":[{"urls":["stun:stun.cloudflare.com:3478","…:53"]},
   {"urls":["turn:turn.cloudflare.com:3478?transport=udp", …,
            "turns:turn.cloudflare.com:443?transport=tcp"],
    "username":"g0…","credential":"a7…"}]}
```

`tesla-clip` now opens the WS first, parses these, and builds the offer with them
— yielding 6 relay candidates on public Cloudflare addresses.

## The new blocker, precisely characterized

**`signed_command` is a mailbox with one slot: exactly one message out per
message in, and only an offer elicits a reply.** Measured behaviour:

| We send | Car replies | Proxy result |
|---|---|---|
| `type:3` offer | yes (answer *or* a candidate) | 200, **1** webrtc payload |
| `type:7` ICE candidate | no | holds ~10–30 s → HTTP 503 "vehicle is not available" |
| `type:1,2,5,6,8` (probes) | no | same 503/timeout |

Consequences, all verified:

1. Each response carries **exactly one** payload (checked by reading tag 19 as a
   repeated field — never more than one). The car's answer *and* its candidates
   cannot both arrive.
2. Which one arrives is a race — some runs return the answer, some a candidate.
3. **Re-sending the offer to pull the next queued message restarts negotiation**:
   each reply carries a different ICE ufrag (`eVvg`, `rhQy`, …), and pion
   correctly drops them — "dropping candidate with ufrag X because it doesn't
   match the current ufrags". So the queue can't be drained this way.
4. No message type among 1,2,5,6,8 acts as a non-destructive puller.
5. The car only ever offers a **private host candidate** (`192.168.20.2`, its
   modem-side LAN). Reaching it directly is impossible; the link has to be the
   car connecting out to our TURN relay.
6. We trickled our 6 relay candidates as `type:7` (accepting the 503s) — the car
   still never sent a connectivity check, so ICE ends `checking → failed`.
   Either those sends are not delivered, or the car ignores candidates that
   arrive outside its own signaling channel.

**The honest conclusion: a trickle-ICE negotiation needs a bidirectional
channel, and the command proxy is not one.** The offer has to go over the
Hermes WebSocket, so the car's answer *and* its candidates route back to our
live connection — exactly how the app does it.

## ⏳ NEXT TASK (resume here)

**Send the `webrtc_comms` offer over the signaling WebSocket instead of the
command proxy.** The remaining unknown is only the Hermes envelope
(`ProtoCommandMessage` topic/routing) that wraps a `RoutableMessage`.

This is materially easier than it was in part 3, because **we now have an
oracle**. Before, the envelope *and* the inner signed message were both guesses.
Now the inner `RoutableMessage` is known-good — the SDK builds it and the car
accepts it over the proxy. Concretely:

1. Capture the exact `RoutableMessage` bytes the SDK sends for the offer (tee
   `inet.Connection.Send`) — a known-accepted payload.
2. Wrap those bytes in the Hermes envelope and send on the WS. Iterate on the
   envelope alone; anything the server rejects is an envelope bug, not a
   signing bug.
3. Envelope fields to work out: the vehicle topic (VIN-derived?), the
   `connection_id` we already set on the upgrade, and the message-type tag. The
   `HermesServer` hello frames (805–806 B, dump with `-ws-dump`) show the
   server-side shape of the same envelope and are a useful template.
4. Once the WS carries the offer, the answer and all candidates arrive on the
   same socket — `tesla-clip` already parses and applies both.

**Fallback if the envelope resists:** instrument a *paired* device to capture one
real signaling session (part-3's suggestion). Still out of scope by the
no-emulator-shortcut constraint, but it is now the only untried alternative.

## Fallback rungs (only after the DataChannel opens)

1. Send the download request over the DataChannel (`initiateDownload(eventPath,
   camera)` semantics — inspect `RNH264Stream`/`f1` for the exact request frame).
2. Reassemble the 9-byte-header, 4-byte-big-endian-length packets
   (`f1.java:411-437`) → raw `.h264`.
3. `ffmpeg -i clip.h264 -c copy clip.mp4`.

## Key facts nailed down (so we don't re-derive them)

- Clips never exist server-side; the app pulls them peer-to-peer over a WebRTC
  **DataChannel**. No download URL exists anywhere in the app.
- Offer payload (`f1.java:196`): `WebRTCUniversalPayload{msg_type:"webrtc:signal",
  uid:<uuid>, type:3, session_description:{type:"offer",sdp}, v:3}`. Types:
  **3 = offer, 4 = answer, 7 = ICE candidate**; the car answers with
  `msg_type:"webrtc:signal_response"` and `v:2`.
- Envelope: `CarServer.Action{ tag8 = webrtc_comms.Request{ tag1 = <JSON bytes> } }`.
  Response: `CarServer.Response{ tag19 = webrtc_comms.Response{ tag1 = <JSON> } }`.
- BLE hard limit: `maxBLEMessageSize = 1024` — offers must be pre-ICE-gathering
  (~700 B) to fit. The cloud path has no such limit (4.3 KB offers go through).
- Depacketization (`f1.java:411-437`): 9-byte header, 4-byte big-endian length at
  offset 5, then payload; payloads are H.264 chunks (`.h264chunk`).

## Preconditions to run any attempt

- Car awake + online: `GET api/1/vehicles/{vin}` → `state: online`.
- A fresh OAuth token (they expire in ~8 h).
- Cloud attempts need **no** BLE proximity and no phone-Bluetooth juggling.
- Build: `cd experiments/tesla-clip && go build -o tesla-clip .`

## Running `tesla-clip`

```sh
./tesla-clip -transport cloud -vin <VIN>            # offer → answer → ICE
./tesla-clip -transport cloud -vin <VIN> -ws-dump   # dump Hermes WS frames
./tesla-clip -transport cloud -vin <VIN> -drain "type:5,6,8" -polls 3  # probe types
```

Useful flags: `-wait` (ICE window), `-send-timeout` (per signed_command; candidate
sends always time out by design), `-polls`/`-drain` (queue-drain strategy),
`-listen-ws=false`, `-transport ble` (the old, dead-end path).

Keep runs short — a full attempt is ~60–90 s including the car round-trips.
