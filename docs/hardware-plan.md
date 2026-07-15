# In-Car Hardware Plan

**Status:** Proposed baseline for the next prototype and initial field units  
**Date:** 2026-07-13

## Decision summary

The in-car unit will use a Raspberry Pi 4 Model B, a high-endurance microSD
card, and a native-USB LTE modem with a Hologram SIM. The Pi will continue to
present the Tesla with a USB mass-storage gadget while it selects, trims, and
aggressively compresses the relevant Sentry clip for near-real-time upload.

This is the baseline architecture:

```text
Tesla glovebox USB
  | USB data + VBUS
  v
Pi 4 USB-C OTG port (mass-storage gadget)
  |
  +-- microSD: OS + MBR/exFAT backing image + durable upload spool
  |
  +-- USB-A host port --> USB LTE modem --> Hologram SIM --> VPS --> Gemini
```

“Near real time” means analysis begins shortly after the relevant clip and
event metadata become readable — not streaming an unfinished MP4.

## Baseline bill of materials

| Component | Baseline choice | Required properties | Prototype budget |
|---|---|---|---:|
| Computer | Raspberry Pi 4 Model B, 1 GB | Proven USB gadget support; Wi-Fi; separate USB host ports | $35–45 |
| Storage | 128 or 256 GB high-endurance microSD | Genuine endurance-rated card from a traceable supplier | $15–30 |
| Cellular | USB board or dongle based on SIM7670G or equivalent | Native USB ECM/RNDIS, physical SIM, US bands, LTE Cat 1 bis or better | $25–50 |
| SIM/data | Hologram IoT SIM | Planning assumption: $0.03/MB usage | Plan-dependent |
| LTE antenna | External adhesive or enclosure-mounted antenna | Correct LTE bands, 50-ohm coax, secure connector | $5–15 |
| Power hardware | To be selected after vehicle measurements | Must survive modem current peaks without Pi undervoltage | $5–25 |
| Enclosure/cables | Vented enclosure and short, restrained cables | Automotive temperature and vibration considered | $10–20 |

Expected prototype hardware cost is roughly **$90–$150**, excluding the data
plan. Production units must use controlled suppliers and fixed part numbers;
consumer marketplace availability is acceptable only for prototypes.

## Raspberry Pi 4 configuration

- Use the Pi's USB-C controller in peripheral mode for the Tesla-facing
  mass-storage gadget. This configuration has already enumerated successfully
  in the real car.
- Connect the LTE modem to one of the Pi's USB-A host ports. Do not place the
  modem on the Tesla-facing OTG connection.
- Use the 1 GB Pi 4 unless field testing demonstrates memory pressure.
- Keep CPU work serialized and use the Pi hardware H.264 path where available.
  LTE upload, exFAT inspection, and video conversion must not compete through
  multiple unbounded workers.

## Storage plan

Use a genuine high-endurance card such as a SanDisk High/Max Endurance or
Samsung PRO Endurance, sourced from an authorized or otherwise traceable
seller. Start with 128 GB for development; use 256 GB if the offline retention
test shows that 128 GB cannot hold the Tesla backing image plus the required
upload backlog.

The card holds:

1. Raspberry Pi OS and the application.
2. The MBR-partitioned exFAT backing image exposed to the Tesla.
3. The SQLite upload spool and extracted clips on ext4.

The upload spool remains authoritative until the server has verified each
content-addressed blob and finalized the manifest. Set the spool cap from a
retention requirement rather than filling all free space. A read-only root
filesystem is a hardening goal, not a prerequisite for the next prototype.

## LTE modem requirements

The first modem should be a native-USB SIM7670G development board or dongle.
A different module is acceptable only if it meets the same gates:

- Unlocked and compatible with the Hologram SIM and APN.
- LTE bands appropriate to the target US networks, especially B2, B4, B5,
  B12/B13, and B66; B71 is desirable.
- Native USB networking through ECM, NCM, or RNDIS. A USB connector wired only
  to a CH340/CP210x serial adapter is not acceptable for video transfer.
- At least LTE Cat 1-class upstream throughput. The design does not require a
  Cat 4 modem if Cat 1 provides stable coverage and sustained upload speed.
- Onboard regulator designed for the module's transmit-current peaks, a SIM
  holder, accessible PWRKEY or reliable auto-start, and antenna connectors.
- Automatic reconnection after coverage loss, Pi reboot, and modem reset.
- Stable Linux operation for a 24-hour upload soak with no manual AT commands.

Use USB networking for payload data and retain a serial AT port for diagnostics
and recovery. Record modem model, firmware, IMEI, SIM ICCID, signal metrics,
carrier, and reconnect count in device telemetry.

## Power design and acceptance gate

The Pi 4 plus an LTE modem must not be assumed safe on the glovebox port merely
because the Pi alone works. LTE transmit bursts can cause a short voltage dip,
which can reset the modem, trigger Pi undervoltage, corrupt storage, or break
the Tesla USB gadget connection.

The next prototype should initially use the glovebox USB supply and log voltage
and undervoltage events while measuring current at idle, during compression,
and during a worst-signal upload. It passes only if all of the following remain
true across repeated events:

- No Pi undervoltage flags, USB resets, modem disconnects, or storage errors.
- The Tesla gadget remains enumerated throughout compression and LTE transmit.
- Five-minute sustained upload and repeated reconnect tests complete normally.
- Operation remains stable at the weakest cellular signal expected in service.

If this test fails, the preferred fallback is a qualified automotive
12 V-to-5 V converter sized for the complete load, with power-path isolation
that prevents backfeeding the Tesla USB port. A battery or capacitor covers a
peak but cannot fix an inadequate sustained power budget.

## Video and data plan

The cellular budget is **bytes per analyzed event**, not simply encoder ratio.
The device should upload only the selected trigger camera, trim around the
event when metadata permits, remove audio, reduce resolution and frame rate,
and then encode with hardware H.264. The original clip remains in the local
spool until upload and finalization succeed.

Initial encoding profile to validate:

| Setting | Initial target |
|---|---|
| Camera count | One selected camera |
| Time window | 20–30 seconds around the trigger; full minute only as fallback |
| Resolution | Approximately 480 px wide, preserving aspect ratio |
| Frame rate | 5–10 fps |
| Codec | H.264 via the Pi's hardware encoder |
| Video bitrate | Start at 150 kbps; test 100–250 kbps |
| Audio | Removed |
| Upload behavior | Durable queued HTTP upload with resume/retry semantics |

At 150 kbps, a 20–30 second window is about 0.38–0.56 MB of video, or
**$0.011–$0.017 per event** at $0.03/MB before protocol overhead. The release
target is **no more than 1 MB per normal analyzed event** ($0.03), with
**0.25–0.75 MB preferred**. Failed uploads must resume or retry the same
compressed artifact rather than re-encode into a different blob.

The existing compression implementation targets 1.2–2.5 Mbps and preserves
the source resolution and frame rate, so it does not yet meet this cellular
plan. It must gain explicit trimming, scaling, frame-rate reduction, and an LTE
profile. Software HEVC results from desktop experiments are useful quality
references, but they must not be used for capacity planning until they run in
real time on the Pi; the Pi 4 baseline should assume hardware H.264.

The agent must detect encrypted Tesla clips before attempting compression or
upload. Encrypted clips cannot be made useful to Gemini by transcoding them;
support for affected vehicle firmware requires a separate key acquisition and
decryption design. Until that exists, the unit should retain the clip locally,
report the unsupported state, and avoid consuming cellular data on ciphertext.

## Validation sequence

### 1. Bench integration

- Bring up the chosen modem over USB and activate the Hologram SIM.
- Confirm unattended boot, APN configuration, DNS, TLS, modem reset, and
  recovery after removal and reinsertion.
- Upload fixed 0.25, 1, 10, and 100 MB payloads and record throughput, signal,
  current, temperature, byte accounting, and reconnect behavior.

### 2. Compression qualification

- Build a representative corpus: people near the car, door contact, carts,
  shadows, rain, nighttime footage, and distant motion.
- Compare 100, 150, and 250 kbps H.264 at 5, 8, and 10 fps.
- Measure wall-clock encoding speed, peak temperature, total payload bytes,
  upload latency, and Gemini verdict quality.
- Select the lowest profile that preserves acceptable analysis recall. Visual
  attractiveness is secondary to evidence retention.

### 3. Combined load and power test

- Run exFAT monitoring, compression, and LTE upload concurrently.
- Repeat with weak signal, because the modem draws more power and transfers
  more slowly under poor radio conditions.
- Confirm that USB gadget enumeration and Tesla writes are unaffected.
- Perform repeated ignition/power removal tests during every pipeline stage.

### 4. In-car pilot

- Install one instrumented unit in the glovebox and run it for at least two
  weeks before shipping devices to users.
- Track event count, bytes/event, monthly projected data cost, upload latency,
  modem resets, Pi undervoltage, temperatures, spool high-water mark, and
  Gemini failures.
- Expand to a small pilot batch only after the power and data budgets hold in
  real-world weak-coverage conditions.

## Ship/no-ship criteria

A hardware revision is ready for a controlled user pilot only when:

- The exact Pi, SD card, modem, antenna, cables, and power parts have fixed SKUs.
- All four validation stages above pass, including the power gate.
- Normal events stay below 1 MB and the measured monthly data cost fits the
  product model at the observed event rate.
- Component temperatures remain within rated limits in a parked-car thermal
  test; indoor bench testing is insufficient.
- Each device can be provisioned, identified, updated, diagnosed, and revoked
  without sharing one fleet-wide credential.

## Decisions still open

1. Exact USB LTE board/dongle and its production supplier.
2. Whether glovebox USB power passes the combined-load test.
3. Final SD capacity after measuring offline retention.
4. Final H.264 resolution, frame rate, bitrate, and trigger-window duration.
5. Hologram base-plan/SIM fees and real network behavior ($0.03/MB is a
   planning assumption, not the complete service cost).
