# Tesla App Traffic Capture — Understanding the Dashcam Viewer Transport

> **Scope & authorization.** This is personal interoperability research on my own
> Tesla account and my own vehicle, observing how my own dashcam clips move over
> the network, using an Android emulator I own. Nothing here targets third-party
> accounts, data, or infrastructure; nothing is distributed. The aim is to learn
> whether my existing Tesla app can already do a job my in-car Raspberry Pi does
> today, so I can simplify my own setup.

## Goal

Determine whether the Tesla mobile app's **Dashcam Viewer** exposes a reusable way
to pull my Sentry/dashcam clips off my car over the network — ideally a fetchable
file/blob via the app's **Save/Download** button — so I could replace (or reduce)
the in-car Raspberry Pi with a purely account-side clip grab.

## What the research established (before observing any traffic)

- **No public clip-retrieval API exists.** The community-documented Owner API
  (timdorr/tesla-api) and official Fleet API are command/telemetry only. The only
  dashcam-adjacent endpoints are `dashcam_save_clip` (trigger a save) and
  `set_sentry_mode` (toggle). No list / download / metadata.
- **Clips are not stored in Tesla's cloud.** The app opens a live, on-demand
  session to a *specific awake vehicle* and streams from the car's USB drive to the
  phone (same class of mechanism as the Smart Summon live camera feed).
- **No public project has documented this transport.** Every Tesla dashcam
  repo is a *file* viewer for MP4s already pulled from USB/app-download. The closest
  adjacent work (Summon-Pro, the vehicle-session path) explicitly skipped video and
  had its Fleet API key revoked by Tesla.
- **The Save/Download button** implies a real file transfer (a downloaded MP4),
  which is the most tractable thing to look for — more so than screen-scraping a stream.
- **2026.20 encrypts clips by default** with an account-tied key; the web viewer
  (dashcam.tesla.com) fetches per-account decryption keys after login. Relevant to
  whatever we observe.

## Approach

Observe the Tesla app's own network traffic on an Android emulator, using a local
debugging proxy so the encrypted HTTPS calls the app makes on my behalf are
readable to me. Because the app validates its TLS connections strictly, I use
Frida to have the app trust my local proxy's certificate for the duration of the
session, then inspect what the Dashcam Viewer actually does when it lists and
downloads one of my clips. This is a standard app-debugging setup, pointed at my
own account.

## Gates / open questions (in the order we hit them)

1. **Play Integrity** — emulators often don't satisfy Google's device attestation,
   and the Tesla app may decline to log in there. First real go/no-go once the app
   is installed.
2. **Premium Connectivity** — the remote Dashcam stream reportedly requires it.
   Must be confirmed on the account, else there's nothing to observe.
3. **Car awake + online** during the test (wake from the real phone first).
4. **Clip encryption (2026.20+)** — observed clip bytes may be encrypted; the
   decryption-key path is a secondary question.

---

## Environment (what's built)

- Host: macOS arm64 (Apple Silicon)
- Emulator AVD: `teslacap`, Android 14 / **API 36**, `google_apis` arm64 image
  (developer image with adb root — NOT the Play Store image, which is locked)
- mitmproxy 12.x — proxy `:8080`, web UI `http://127.0.0.1:8081`
- frida-tools 17.16.4 (host) + matching `frida-server` (emulator, arm64)
- Working dir for all artifacts:
  `…/9d8b327b-…/scratchpad/` (referred to below as `$SCRATCH`)

### Key files in `$SCRATCH`

| File | Purpose |
|------|---------|
| `frida-server` | arm64 frida-server binary pushed to emulator |
| `c8750f0d.0` | mitmproxy CA in Android system-store hash form (legacy store; ignored on API 36) |
| `frida-set/config.js` | HTTPToolkit config with my real mitmproxy CA + proxy `10.0.2.2:8080` baked in |
| `frida-set/native-connect-hook.js`, `native-tls-hook.js` | native TLS + connect hooks |
| `frida-set/android/android-proxy-override.js` | route the app's traffic through my proxy |
| `frida-set/android/android-certificate-trust{,-fallback}.js` | make the app trust my proxy CA |
| `launch-tesla.sh` | launches Tesla under Frida with the full script set |
| `boot-and-provision.sh`, `setup-emulator.sh` | re-provision from scratch if needed |
| `flows.mitm` | saved captured flows (parse with `mitmdump -nr flows.mitm`) |

### Key finding that shaped the setup

API 36 moved the trusted CA store into the **Conscrypt APEX**
(`/apex/com.android.conscrypt/cacerts/`), so a CA placed in
`/system/etc/security/cacerts/` is ignored — only plaintext HTTP is readable.
Rather than remount the APEX (a per-process mount-namespace rabbit hole), I use
**Frida** to have the Tesla app trust my proxy CA for the session at the app
layer. This sidesteps the system store entirely for the one app I'm observing.

---

## Checklist

### Done ✅

- [x] Confirm no existing clip API / public write-up covers this (web research)
- [x] Confirm Dashcam Viewer does **not** require phone-key/BLE pairing (remote feature)
- [x] Install mitmproxy (host)
- [x] Install frida-tools (host) + download matching `frida-server` (emulator arm64)
- [x] Download developer `google_apis;android-36;arm64-v8a` system image
- [x] Create AVD `teslacap` and boot it (`-writable-system`)
- [x] `adb root` + remount confirmed
- [x] Generate mitmproxy CA; compute Android hash filename (`c8750f0d.0`)
- [x] Push `frida-server`; confirm host can enumerate emulator processes
- [x] Start mitmweb; **prove proxy path works** (40 flows captured from emulator)
- [x] Diagnose HTTPS-not-decrypting → root cause: API 36 Conscrypt APEX trust store
- [x] Assemble full HTTPToolkit Frida script set with real CA baked into `config.js`
- [x] Write `launch-tesla.sh`
- [x] Set emulator global proxy `10.0.2.2:8080` (later dropped — see 2026-07-24 session log)
- [x] **Obtain the Tesla APK** — classic app `com.teslamotors.tesla` 4.58.6-4430, arm64-v8a
      universal-DPI APKM bundle from APKMirror (matches audited launcher package)
- [x] Install Tesla APK — `install-tesla-apk.sh` (extract split bundle → `adb install-multiple`)
- [x] Repair `config.js` — prior hand-written config had dropped the canonical
      below-the-line section; restored `IGNORED_NON_HTTP_PORTS`, `CERT_DER`, `waitForModule`,
      `BLOCK_HTTP3`, `PROXY_SUPPORTS_SOCKS5` and fixed `CERT_PEM` boundaries (see session log)
- [x] Launch Tesla via `launch-tesla.sh` — clean load, `libssl.so` hooked, unpinning done
- [x] **Verify HTTPS is readable** — app-stack HTTPS decrypts (proved: `ownership.tesla.com`,
      `digitalcontent.tesla.com` over HTTP/2 in mitmproxy)

### Blocked / next input needed 🚧

- [ ] Confirm **Premium Connectivity** on the account
- [ ] Car **awake + online** available for the test
- [ ] **Enter Tesla credentials** to sign in (must be done by the account owner)

### To do ⏭️

- [ ] **Go/no-go: does it log in?** (Play Integrity check — login page now reachable via
      Custom Tab after dropping the device proxy; success/rejection unknown until sign-in)
- [ ] Log in; capture baseline account/vehicle API calls
- [ ] Open **Dashcam Viewer** (car awake) — capture the session-setup handshake
- [ ] Classify the video transport: WebRTC (STUN/TURN, SDP, DTLS) vs HTTPS segments
- [ ] **Hit Save/Download on a clip** — see whether it's a fetchable MP4 blob
- [ ] If a blob: document host, auth, URL shape, and whether it's replayable out-of-app
- [ ] If encrypted (2026.20+): understand the account key-fetch path
- [ ] Write up findings; decide feasibility vs keeping the Pi

## Fallbacks if a gate fails

- **Play Integrity blocks login** → try an older Tesla APK version, or Genymotion,
  or a spare physical Android device with developer access.
- **No Premium Connectivity** → the remote stream likely won't start; observation is
  moot until it's enabled.
- **Transport is pure WebRTC with no fetchable blob** → replacing the Pi this way is
  impractical; document and stop.

## How to resume

```sh
# emulator (if not running)
export PATH="$HOME/Library/Android/sdk/emulator:$HOME/Library/Android/sdk/platform-tools:$PATH"
emulator -avd teslacap -writable-system -no-snapshot &

# proxy + UI
mitmweb --listen-port 8080 --web-port 8081 --set web_open_browser=false \
  --save-stream-file "$SCRATCH/flows.mitm" &

# proxy + frida-server
adb shell settings put global http_proxy 10.0.2.2:8080
adb shell "su 0 sh -c '/data/local/tmp/frida-server &'"

# once Tesla APK installed:
$SCRATCH/install-tesla-apk.sh   # extract APKM + adb install-multiple
$SCRATCH/launch-tesla.sh        # run backgrounded with stdin held open (see session log)

# inspect captures (Homebrew mitmproxy: `from mitmproxy import io` does NOT import;
# use an addon script instead)
mitmdump -n -q -r "$SCRATCH/flows.mitm" -s addon.py
```

## Session log — 2026-07-24

Progress this session (emulator was already live: API 36, adb root, frida-server up):

1. **APK obtained & installed.** Two current Tesla, Inc. apps exist on APKMirror:
   *Tesla One* 26.14.x (new rebrand, different package) and the classic *Tesla* 4.58.6-4430
   (`com.teslamotors.tesla`). Installed the classic one — it matches the launcher's target
   package. `install-tesla-apk.sh` extracts the 34-part split bundle and runs
   `adb install-multiple`.

2. **`config.js` was broken and is now fixed.** The prior session's hand-written `config.js`
   kept only `CERT_PEM`/`PROXY_HOST`/`PROXY_PORT`/`DEBUG_MODE` and dropped the canonical
   HTTPToolkit "below-the-line" section. At runtime the hooks threw
   `IGNORED_NON_HTTP_PORTS is not defined`, `waitForModule is not defined`, `BLOCK_HTTP3 is
   not defined`, and a cert `ASN.1 DECODE_ERROR` (native TLS hook needs a `CERT_DER`
   Uint8Array that was never computed). Restored the utility section verbatim from
   `httptoolkit/frida-interception-and-unpinning`, added the missing user constants, and
   reformatted `CERT_PEM` (its template literal had a leading/trailing newline that would
   fail `pemToDer`'s boundary check). Verified the restored `pemToDer` yields DER bytes
   byte-identical to `openssl` (825 bytes, sha256 `e1fbe2…af41`). Old file kept at
   `config.js.bak.truncated`.

3. **HTTPS interception proven against the app.** Clean launch:
   `Hooked native TLS lib libssl.so`, `Certificate unpinning completed`,
   `Unpinning fallback auto-patcher installed`, no errors. mitmproxy shows decrypted
   HTTP/2 to `ownership.tesla.com/mobile-app/splash-assets` and
   `digitalcontent.tesla.com/oxp/...`. This settles the open question from setup — the
   API-36 Conscrypt-APEX problem is fully handled by the app-layer Frida CA-trust hooks.

4. **Login uses a Chrome Custom Tab — key obstacle.** The app opens `auth.tesla.com` in a
   Chrome Custom Tab (a *separate* process), which our Frida hooks do **not** instrument.
   Under the device global proxy, Chrome used the system CA store, didn't trust the
   mitmproxy CA, and showed `NET::ERR_CERT_AUTHORITY_INVALID` — blocking login.
   - **Fix applied (Option A, low-risk):** dropped the device global proxy
     (`settings put global http_proxy :0`). Chrome now reaches the real `auth.tesla.com`
     cert (login can proceed), while the Frida hooks keep intercepting + decrypting the
     app's own stack — **verified**: a cold start with the proxy off still captured fresh
     decrypted `ownership.tesla.com` / `digitalcontent.tesla.com` flows. Trade-off: the
     in-Chrome OAuth *page* isn't captured, but the app-side token exchange and dashcam
     traffic still are.
   - **Alternative (Option B, more complete):** install the mitmproxy CA into the Conscrypt
     APEX so Chrome trusts it too and the full login flow is captured. This is the
     APEX-remount "rabbit hole" the setup notes avoided; only needed if the in-browser auth
     request itself must be observed.

**Current state:** Frida session running with hooks loaded; device proxy = `:0`; app
installed and cold-started; app-stack HTTPS decrypting. **Next human steps:** enter Tesla
credentials to test login (Play Integrity go/no-go), confirm Premium Connectivity, and have
the car awake — then open the Dashcam Viewer and hit Save/Download.

**Operational note:** `launch-tesla.sh` ends in an interactive `frida` REPL. Run it
backgrounded with stdin held open so it doesn't EOF-exit, e.g.
`sleep 100000000 | ./launch-tesla.sh > frida.log 2>&1 &`, and kill that process to tear down.

## Session log — 2026-07-24 (part 2): the clip-retrieval path, fully mapped

Revising the earlier "no account-side grab, impossible" conclusion. It is **not
impossible** — it is a bounded (if non-trivial) engineering build, and every
piece of the wire format is now known. No black boxes remain.

### The complete signaling stack (from decompiled Wire protobuf classes)

The Tesla app pulls clips over a WebRTC DataChannel it negotiates via
`wss://signaling.vn.teslamotors.com/v1/mobile`. Each WS frame is **Tesla's
standard signed vehicle-command protocol**, not a bespoke format:

```
RoutableMessage (universal_message.proto — SIGNED, HMAC session)   [open-source SDK]
  └─ carserver Action  (fc0/a.java)
       ├─ tag 3  signatures.GetSessionInfoRequest   ← session handshake  [open-source SDK]
       └─ tag 8  webrtc_comms.Request               ← { request_data: bytes }
                    └─ WebRTCUniversalPayload (JSON) ← offer/answer/ICE (schema below)

carserver Response (fc0/m3.java)
  ├─ tag 3  signatures.SessionInfo                  ← handshake reply     [open-source SDK]
  └─ tag 19 webrtc_comms.Response { response_data: bytes → JSON }
```

`WebRTCUniversalPayload` (kotlinx JSON, from the `@Serializable` classes):

```
WebRTCUniversalPayload { msg_type:str, uid:str, type:int,
  session_description: { type:str("offer"/"answer"), sdp:str,
                         candidate:str, sdpMLineIndex:int, sdpMid:str },
  v:int, data_limit_reached:bool, rejected:bool, reason:str }
```

### Why this is buildable

- The **signing + session layer** (`GetSessionInfoRequest`/`SessionInfo`, HMAC,
  RoutableMessage) is already implemented in the open-source
  `teslamotors/vehicle-command` SDK. It is what authorizes the session, and it
  is why a whitelisted key is required.
- We now **hold a whitelisted key**: `experiments/tesla-ble-pair` enrolled a
  self-generated P-256 key into the car's VCSEC over Mac BLE and verified it via
  an authenticated session (2026-07-24, VIN …0395). Same key works for the Fleet
  API virtual-key route.
- The only genuinely new code is: (1) a **WebSocket transport/connector** for
  `signaling.vn` to plug into the SDK's signer (the SDK ships BLE + Fleet
  connectors, not WS); (2) the two thin `webrtc_comms` protos; (3) a **WebRTC
  DataChannel client** that sends the offer and reassembles the H.264 clip
  (`initiateDownload(eventPath, camera)` semantics → `downloadCache`).

### Decision field to watch

`WebRTCUniversalPayload.rejected` + `reason`: the single field that will say
whether our whitelisted key is sufficient to open a dashcam session. That is the
first thing a signaling client should read.

### External dependency

Connecting the WS needs a `jwt/hermes` token, which is minted from the account
OAuth token (`POST /api/1/users/jwt/hermes`, 200 observed). A build/test cycle
needs a fresh account token (Tesla OAuth or Fleet API).

### Net verdict

Retrieving a Sentry clip without the Pi is feasible: pair a key (done) →
WS + signed session handshake (SDK-reusable) → WebRTC offer → DataChannel
download. It is a real build, and it is a *pull* model (car awake + online),
so it still does not do the Pi's real-time on-write capture — but "impossible"
was wrong.

## Session log — 2026-07-24 (part 3): live attempts against the car

Built `experiments/tesla-clip` (Go + pion/webrtc) and made real end-to-end
attempts against VIN …0395. Results, all experimentally verified:

**Works over BLE with our enrolled key:**
- Authenticated session to **DOMAIN_INFOTAINMENT** (carserver), not just VCSEC.
- Sending arbitrary signed `CarServer.Action` bytes via the SDK's low-level
  `Vehicle.Send(domain, payload, AuthMethodHMAC)`.
- `webrtc_comms.Request` is genuinely tag 8 of `CarServer.Action`
  (`fc0/a.java` type URL = `type.googleapis.com/CarServer.Action`); the offer
  payload is `WebRTCUniversalPayload{msg_type:"webrtc:signal", uid, type:3,
  session_description:{type:"offer",sdp}, v:3}` (from `f1.java:196`).

**Does NOT work over BLE (three iterations, each ruling out a cause):**
1. Wrong `msg_type` ("offer" → corrected to "webrtc:signal") — still no answer.
2. Message >1024 B — the SDK/car BLE layer drops anything over
   `maxBLEMessageSize=1024` (`ble.go:70`). Fixed by sending the offer *before*
   ICE gathering (compact ~700 B SDP, trickle candidates as type-7) — the car
   then emitted new response frames, so it *received* it.
3. Even so, the **carserver never returns a `webrtc_comms.Response`** — the only
   infotainment-domain frame is the session-info reply; all other frames are
   routine VCSEC (`from_destination.domain=2`) chatter.

**Conclusion:** WebRTC dashcam signaling is **not serviced over BLE/carserver** —
the car establishes its WebRTC side via the Hermes cloud relay, so signaling
must go over `wss://signaling.vn.teslamotors.com`. The BLE experiment
nonetheless proved the key + session + `CarServer.Action` send path all work.

**The remaining wall (Hermes path):** the vehicle-addressing/envelope on the
Hermes WS (`ProtoCommandMessage.topic` for the car, command-center routing) is
not resolvable from static decompilation with confidence, and there is **no
captured example of a successful signed Hermes command** to validate against —
the un-paired emulator never produced one. Getting that reference realistically
needs instrumenting a *paired* device (the whitelisted-emulator-key route, or a
rooted phone). Without it, the Hermes envelope is guess-and-check with no
oracle. This is the honest blocker; a clip has **not** been downloaded e2e.
