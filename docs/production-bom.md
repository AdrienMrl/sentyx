# Production Unit BOM (Pi 4 Model B)

**Status:** Proposed — extends the Pi 4 baseline in `hardware-plan.md` to field units
**Date:** 2026-07-26
**Volume assumption:** low-volume bulk, 20 units per order, mostly AliExpress

## Why not CM4

The first draft of this BOM specified a CM4 on a Waveshare carrier, on the
assumption that "production" implies a compute module. At 20 units that is
wrong, on cost and on risk:

| | Pi 4B 1GB | CM4101008 + CM4-IO-BASE-B |
|---|--:|--:|
| Compute | $35 | $71.25 |
| Carrier board | — | $26 |
| Storage attach | USB 3.0 bridge $7 | M.2 slot on carrier, $0 |
| **Subtotal** | **$42** | **$97** |

That is **$55/unit, ~$1,100 across 20 units**, for which the CM4 buys: slightly
lower idle power (no VL805 USB 3 hub or Ethernet PHY to keep alive), a U.FL
external Wi-Fi antenna option, neater mechanical integration, and a path to a
custom carrier PCB. None of those are worth $1,100 at this volume.

The risk argument is stronger than the cost argument. The Pi 4B is the board
that has **already** enumerated in the real car, served live dirty-exFAT reads
of in-flight Sentry events, and torn the gadget down cleanly. `build-image.sh`
already produces `teslcam-pi4-<version>.img.xz`, already supports USB-SSD boot
via PARTUUID + EEPROM boot order, and `hardware/case/` already models a Pi 4.
Moving to CM4 spends money to re-validate the single hardest-won result in the
project.

Two smaller technical points that also favour the Pi 4B:

- **USB bandwidth.** CM4 has exactly one USB 2.0 host controller — 480 Mbps
  shared by the modem and everything else, which is why the CM4 plan had to put
  storage on the single PCIe lane. The Pi 4B has USB 3.0 for the SSD and
  separate USB 2.0 ports for the modem, with headroom to spare.
- **PCIe is not needed for an SSD here.** USB 3.0 to NVMe gives ~350 MB/s,
  far beyond what writing a backing image and a spool requires.

Revisit CM4 only at a volume where a custom carrier PCB makes sense (~100+
units), where the carrier absorbs the modem socket, M.2, supercap UPS, and LCD
header into one board.

## Architecture

```text
Tesla glovebox USB-C ──┬── 5V power ──> supercap buffer ──> Pi 4B 5V
                       └── USB 2.0 data ──> Pi 4B USB-C (mass-storage gadget)

Pi 4 Model B 1GB
 ├── USB 3.0 ──> USB→NVMe bridge ──> 256GB SSD   backing image + upload spool + OS
 ├── USB 2.0 ──> mPCIe→USB carrier ──> Quectel EC25-AFX ──> Hologram SIM
 ├── SPI + GPIO ──> 1.3" IPS LCD + button        status, BLE pairing code
 └── WiFi/BT (PCB antenna)                       home upload path, BLE onboarding
```

Boot from the SSD (EEPROM boot order, already supported by the image build), so
there is no microSD wear path at all. The SSD carries OS, the MBR/exFAT backing
image, and the SQLite spool.

## Bill of materials — per unit at qty 20

| # | Part | Specific choice | Source | Unit @20 | Notes |
|--:|---|---|---|--:|---|
| 1 | Compute | **Raspberry Pi 4 Model B, 1GB** | Authorized distributor — see sourcing note | $35 | Industrial/commercial list price; 2GB retail is $55 |
| 2 | LTE modem | **Quectel EC25-AFX** mPCIe | AliExpress | $40 | US bands incl. **B71** (T-Mobile 600 MHz — Hologram roams T-Mobile). Ext. temp −40…+85 °C |
| 3 | Modem carrier | mPCIe → USB adapter with SIM slot | AliExpress | $9 | Generic "mPCIe with SIM card slot" board (~$12.72 single) |
| 4 | Storage | M.2 **NVMe 256 GB** (DRAM-less TLC) | AliExpress | $19 | Endurance is the point vs. microSD |
| 5 | Storage attach | USB 3.1 → M.2 NVMe bridge, bare board | AliExpress | $7 | Bare board, not a cased enclosure — packs better. Verify UAS behaviour |
| 6 | Buffer power | 2× **100F 2.7V EDLC** (85 °C rated) + 5V supercap UPS/boost board | AliExpress | $12 | ~500 J bank; see power section |
| 7 | Display | **1.3" IPS 240×240 ST7789** SPI | AliExpress | $3 | Status + 6-digit BLE pairing code |
| 8 | Input | 2× tactile momentary switch | AliExpress | $0.20 | Wake display / enter pairing mode |
| 9 | LTE antennas | 2× U.FL pigtail + adhesive film antenna (main + Rx diversity) | AliExpress | $5 | Diversity is worth it in a metal-heavy parked car |
| 10 | Thermal | Pi 4 aluminium heatsink set + thermal pads | AliExpress | $2 | **No fan** — dust and bearing failure in a car |
| 11 | Enclosure | 3D print, **ASA** (or PC-CF; PETG minimum) + M2.5 heat-set inserts/screws | in-house | $2.50 | Filament + fasteners; adapt existing `hardware/case/` model |
| 12 | Tesla cable | USB-C right-angle, 0.3 m, data-capable | AliExpress | $3 | Short and strain-relieved |
| 13 | Internal wiring | Dupont/FFC jumpers, silicone wire, kapton | AliExpress | $1 | |
| | **Total hardware** | | | **≈ $139** | ~$2,780 for 20 units |
| 14 | SIM/data | Hologram IoT SIM | Hologram | plan | ~$0.03/MB planning assumption |

With the 2 GB Pi 4B at retail instead of the 1 GB industrial part: **≈$159/unit,
~$3,180 for 20**.

Realistic range once shipping, dud units, and one respin of the printed case
are folded in: **$150–175 per unit** on the 1 GB part.

### Sourcing note: the $35 is channel-restricted

Raspberry Pi raised prices in **April 2026** — the 2 GB Pi 4B is now **$55**
(launch $45), 3 GB is $84, and 4 GB is $100. The 1 GB part is no longer widely
stocked at retail because it lost its price advantage over the 2 GB, but it
**remains available to industrial and commercial customers at a $35 list
price**. A 20-unit order is plausibly enough to open that conversation with a
distributor, but it is not a web-checkout purchase.

**Action:** confirm 1 GB availability and terms with a distributor before
budgeting $35. If it requires an MOQ or a business account you do not have,
the fallback is the 2 GB at $55, which is still $16 cheaper than a CM4 *and*
saves the $26 carrier.

Buy the Pi from an authorized distributor, not AliExpress — listings there are
gray-market or misrepresented. Every other line above is a commodity part where
AliExpress bulk is correct. Given two price increases in the last year, treat
every figure here as needing a re-quote at order time.

### RAM: 1 GB is the baseline

1 GB matches `hardware-plan.md` ("use the 1 GB Pi 4 unless field testing
demonstrates memory pressure") and matches the prototype that has already run
end-to-end in the car. The workload that would have justified more RAM no
longer exists: NanoDet was deleted in favour of a pixel-change scorer over
keyframe-only decodes, so nothing in the pipeline holds a model or a decoded
video in memory.

The guardrail that makes 1 GB safe is bounded concurrency, not RAM: with a
read-only rootfs there is no swap, so an OOM kill is unrecoverable until
reboot. Keep clip extraction, scoring, and upload serialized — which
`hardware-plan.md` already requires for power reasons anyway — and log peak RSS
during the field soak.

### Wi-Fi and BLE: on the board, but no external antenna

The Pi 4B includes 2.4/5 GHz Wi-Fi and Bluetooth 5.0/BLE, so there is no radio
line item. Unlike the CM4, it has **no U.FL connector** — the PCB trace antenna
is all you get, and a U.FL mod is not a production-appropriate answer.

This is the one real capability the CM4 would have bought. The evidence that it
does not matter is the existing prototype: it already associates to Wi-Fi and
serves BLE onboarding from inside the car. Validate BLE pairing range from the
driver's seat with the glovebox shut before finalizing the enclosure, and keep
the radio side of the case free of metal (no heatsink plate over the Pi's
antenna corner).

## Power design

Two thirds of the risk in this BOM is power, not compute.

**Load budget (peak, 5V rail):** Pi 4B ~3 W idle / ~6 W under ffmpeg, USB SSD
~2 W active, EC25 ~2 W average with ~2 A@3.8 V transmit bursts. Peak ≈ 9–10 W ≈
**2 A at 5 V** — at or slightly over what the glovebox port supplies, and
higher than the CM4 variant of this design by roughly 1 W of always-on
overhead (VL805 USB 3 hub, Ethernet PHY). Disabling the Ethernet PHY and
trimming USB power where possible is worth doing.

**Recommended: supercapacitor buffer (item 6).** Two 100F 2.7V cells in series
(50F @ 5.4V) with balancing, feeding a 5V boost, stores ~500 J. That is ~30–60 s
at full load, which buys:

1. Absorption of modem transmit spikes so the Pi never browns out.
2. Ride-through of momentary USB dropouts without a reboot.
3. Graceful shutdown on power cut — fsync the exFAT backing image, checkpoint
   the SQLite spool, unbind the gadget cleanly.

Supercaps are the right chemistry here specifically **because of heat**: EDLCs
are commonly rated to 85 °C and store energy electrostatically, so a 70 °C
North Las Vegas glovebox does not swell or degrade them. Li-ion swelling risk
accelerates above 70 °C, which rules out both ordinary power banks and the
10440 Li-ion cell used on Waveshare's UPS-equipped CM4 boards.

**Optional, only if >5 min holdover is actually needed:** 2× LiFePO4 IFR18650
(~1500 mAh) with a TP5000-class charger and an **NTC cutoff that disables
charging above ~50 °C** (~$10/unit). LiFePO4 is far more thermally stable than
Li-ion, but its charge window is still roughly 0–50 °C, so expect capacity fade
over Vegas summers. Do not ship this unless "finish the in-flight upload after
the car cuts power" turns out to be a requirement rather than a nicety — the
durable spool already makes that survivable across reboots.

**Fallback if the glovebox port fails the power gate:** automotive 12 V→5 V buck
rated 5 A with reverse/overvoltage protection, fed from a fuse tap, plus
power-path isolation so the unit can never backfeed the Tesla USB port
(~$12/unit + install labor). Materially worse install story, so treat it as the
fallback. The acceptance gate in `hardware-plan.md` still applies and must be
re-run **with the modem transmitting**, not with the Pi alone. The Pi 4B's
higher baseline draw makes this gate more likely to bite than it would on CM4 —
this is the one place the cheaper board costs something real.

## Display and pairing

A 1.3" IPS TFT solves a problem the project previously punted on: with no
display, BLE onboarding had no way to show a pairing code, and LED-blink codes
were rejected as bad UX. A screen lets the Pi display a **6-digit numeric
pairing code** that the app confirms — better UX, and a real defence against
pairing with the wrong unit.

Display choice reasoning:

- **IPS TFT (chosen)** — typically rated −20…+70 °C. Above ~70 °C the liquid
  crystal can go washed-out or dark, but it recovers on cooling. Acceptable for
  a status display only read when someone opens the glovebox.
- **OLED (rejected)** — a mostly-static status screen is the worst case for
  burn-in, and heat accelerates it.
- **E-paper (rejected)** — ghosting and slow refresh at high temperature.

Drive it over SPI from the 40-pin header. Keep it asleep by default and wake on
button press; a permanently-lit panel is wasted power and wasted panel life.

## Enclosure

`hardware/case/` already models the Pi 4 prototype, so this is an adaptation
rather than a new design — it needs to grow bays for the mPCIe modem carrier,
the USB-NVMe bridge, the supercap bank, and the LCD.

**Material: ASA**, or PC-CF if the printer can do it. PETG is the minimum
acceptable. **PLA is disqualified** — its glass transition is around 60 °C and a
parked car interior exceeds that regularly; the case will sag and pull the board
out of alignment.

Design requirements:

- Vent slots top and bottom for convection, but no fan.
- Heatsink thermally coupled outward, so the SoC is not cooking in still air
  inside a plastic box — but keep metal away from the Pi's antenna corner.
- Recessed window for the LCD, ideally a separately printed clear-PETG lens.
- M2.5 brass heat-set inserts, not self-tappers into plastic — this box will be
  opened repeatedly during field debugging.
- Strain relief on the Tesla USB cable; vibration is the failure mode nobody
  tests for.
- The Pi 4B puts connectors on three sides, which is worse for cable dressing
  than a compact carrier would be. Budget for one respin.

## Cost reduction levers

| Lever | Saving | Cost |
|---|--:|---|
| Reuse the existing ASR USB dongle instead of mPCIe modem + adapter | −$24 | Inherits the documented RNDIS cold-boot wedge and its watchdog; loses B71 and the −40…+85 °C rating |
| 128 GB NVMe instead of 256 GB | −$6 | Shorter offline retention window |
| High-endurance microSD instead of NVMe + bridge | −$20 | Rejected: endurance under constant backing-image writes is the whole reason for an SSD |
| Drop the LCD | −$3 | Loses the pairing-code UX; not worth it |

## Open questions — resolve on the bench before ordering 20

1. **Pi 4B 1 GB industrial availability at $35** — the single biggest line item
   and the one that is channel-dependent. Get a distributor quote first.
2. **Does the modem on USB 2.0 coexist with the SSD on USB 3.0** while the
   mass-storage gadget is active? Expected to be fine given separate buses, but
   verify — and check the USB→NVMe bridge's UAS/quirks behaviour under
   sustained write, since a bad bridge chip is a classic Pi 4 failure mode.
3. **Re-run the power acceptance gate** with the modem transmitting at worst
   expected signal. This is the gate most likely to fail on Pi 4B.
4. **SSD thermals** in a closed printed box at 70 °C ambient. Consumer
   DRAM-less drives are typically rated 0–70 °C; log SMART temperature during a
   summer soak, and budget ~$45/unit for an industrial part if it fails.
5. **BLE pairing range** from the driver's seat with the glovebox shut, on the
   PCB antenna only.
