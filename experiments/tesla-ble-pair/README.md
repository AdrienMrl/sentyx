# tesla-ble-pair

Proof of concept: enroll a **self-held P-256 keypair** into the car's VCSEC
whitelist over BLE from a Mac.

## Why this exists

Dashcam clips never exist server-side. The Tesla app pulls them peer-to-peer
from the car over a WebRTC DataChannel, and the car only authorizes that
session for a key it has whitelisted. Holding our own whitelisted key is the
prerequisite for any non-app client. Background and evidence:
`docs/tesla-app-traffic-capture.md`.

This tool does **only** the key-enrollment step. It does not talk to the
dashcam — that is the next piece of work.

## Scope and consent

Operates on a vehicle you own. Enrollment requires physically tapping a key
card on the center console; that tap is the car's own consent gate and this
tool cannot bypass it. The key can be removed at any time in the Tesla app
(Controls → Locks).

## Requirements

- macOS with Bluetooth (uses the SDK's native `device_darwin.go` — no dongle)
- Go 1.25+
- Physical proximity to the car (BLE range, ~5–10 m)
- Your key card

Built on Tesla's open-source
[`vehicle-command`](https://github.com/teslamotors/vehicle-command) SDK (v0.4.1),
which implements the VCSEC protocol — we are not reimplementing crypto.

## Usage

```sh
cd experiments/tesla-ble-pair
export TESLA_VIN="<YOUR_VIN>"

# 1. Generate the keypair (once). Default: ~/.teslcam/tesla-ble-key.pem, mode 0600.
go run . genkey

# 2. Standing next to the car, confirm the Mac sees its BLE beacon.
go run . scan -vin "$TESLA_VIN"

# 3. Enroll the key. Tap the key card on the center console when prompted.
go run . pair -vin "$TESLA_VIN"
```

`-vin` can be replaced by the `TESLA_VIN` environment variable.

Run `scan` before `pair`: it verifies both radio range and that the VIN is
right, so a failed pairing is not ambiguous. RSSI weaker than about −85 dBm
means move closer.

## The key works for both enrollment routes

The generated key is plain P-256, so it is equally valid for Tesla's Fleet API
**virtual key** flow, which enrolls *without* physical proximity:

```sh
go run . pubkey > com.tesla.3p.public-key.pem
```

Serve that at `https://<domain>/.well-known/appspecific/com.tesla.3p.public-key.pem`
and have the owner approve at `https://tesla.com/_ak/<domain>`. That route needs
a registered Tesla developer application; the BLE route does not. Both put the
same key into the same VCSEC whitelist, so you never have to regenerate.

Note: P-256 is not a preference — it is the only curve VCSEC and the virtual-key
flow accept. An RSA key will be rejected.

## Status

- [x] Key generation, storage, public-key export — tested
- [x] Builds and vets clean on darwin/arm64
- [x] `scan` — **verified against the real car** (2026-07-24): found Model 3
      `<YOUR_VIN>` at −75 dBm. macOS CoreBluetooth works with no dongle.
      Beacon local name `S<first-16-hex-digits-of-SHA1(VIN)>C` matches `"S"+SHA1(VIN)[:16]+"C"`,
      which confirms vehicle identity rather than trusting any nearby Tesla.
- [x] `pair` — **verified against the real car** (2026-07-24): key enrolled into
      `<YOUR_VIN>` and confirmed by an authenticated VCSEC session on a
      fresh connection (`verify`).
- [ ] Using the enrolled key to authorize a dashcam signaling session — not built

## Two findings that cost us several failed attempts

**The car does not prompt on screen.** The protocol uses
`SIGNATURE_TYPE_PRESENT_KEY`: the car passively waits for a key-card tap to
authorize the pending request and displays nothing. Waiting for a prompt means
never tapping.

**`SendAddKeyRequestWithRole` is fire-and-forget.** It writes the BLE
characteristic and returns `nil` regardless of whether the car accepted, so a
successful return proves only that bytes were sent. Enrollment must be
confirmed with a session handshake — that is what `verify` does, and why `pair`
now polls instead of declaring victory. Our first two "successful" pairings had
not actually enrolled anything.

Because each request opens only a brief consent window, `pair` re-sends every
6 s and polls until the handshake succeeds. The successful run took 69
attempts — treat the attempt counter as normal, not as failure.

If `pair` times out, the usual cause is the key card not being tapped in time;
retry.

## macOS Bluetooth permission

Build a stable binary (`go build -o tesla-ble-pair .`) rather than using
`go run`. macOS attributes Bluetooth permission per binary, and `go run`
compiles to a fresh temporary path each time, so permission has to be
re-granted on every invocation.
