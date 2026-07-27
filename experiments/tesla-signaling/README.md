# tesla-signaling

Transport probe for Tesla's dashcam signaling channel — the WebSocket the app
uses to negotiate the WebRTC session that streams Sentry/dashcam clips from the
car. Part of the effort to retrieve a clip without the in-car Pi. Background and
full protocol map: `docs/tesla-app-traffic-capture.md`.

## What it does (this increment only)

1. Reads the account OAuth token from `~/.teslcam/tesla-oauth.json`.
2. Mints a per-connection Hermes JWT (`POST /api/1/users/jwt/hermes`).
3. Opens `wss://signaling.vn.teslamotors.com/v1/mobile` with the app's headers
   (`X-Jwt`, `X-Connection-Id`, `X-Tesla-App-Key`, `X-Tesla-User-Agent`).
4. Reads and pretty-prints the server's `HermesServer` hello frames.

It stops before the signed vehicle-command exchange — that is the next build.

## Usage

```sh
# needs ~/.teslcam/tesla-oauth.json (captured from the app; see docs)
go run . -timeout 20s
```

## Result (verified 2026-07-24, production)

Connected (HTTP 101) and held a **stable** channel — notably the app itself got
an abnormal close at this stage on the un-paired emulator; our client does not.
The `HermesServer` hello is a protobuf carrying a JSON config, including the
WebRTC bootstrap we need:

- connection uuid, `expiration`, `connection_timeout: 180000`, `ping_frequency: 60000`
- **ICE servers**: `stun/turn.cloudflare.com` with **live TURN username + credential**
  (rotate on each hello frame)

So the transport + auth are done. The remaining work to actually pull a clip:

1. **Signed session handshake** — send a `RoutableMessage` carrying
   `signatures.GetSessionInfoRequest`, signed with the BLE-enrolled key
   (`experiments/tesla-ble-pair`), routed to the car over this WS; read back
   `signatures.SessionInfo`. Reuses the open-source `vehicle-command` signer.
2. **WebRTC offer** — wrap a `WebRTCUniversalPayload` offer in
   `webrtc_comms.Request`, send inside a signed command; watch the
   `rejected`/`reason` field on the response.
3. **DataChannel download** — establish the peer connection using the ICE/TURN
   config above and reassemble the H.264 clip.

## Notes

- `X-Tesla-App-Key` is a static per-app value observed in the capture, not an
  account secret.
- The car must be online (connected to Hermes) for step 1's messages to route
  to it. The relay connection here does not require the car.
