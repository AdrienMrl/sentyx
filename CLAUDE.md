# teslcam-exp

## Objective

Software that emulates a USB mass-storage device for a Tesla and collects Sentry Mode events in real time, so clips can be analyzed (currently via Gemini, see `analyze-video.ts`) as soon as they're written rather than after manually pulling the drive.

Must work on macOS and Linux for development; the in-car component requires Linux gadget-mode hardware (e.g. Raspberry Pi Zero 2 W) since USB device-mode requires a UDC that Mac/PC ports don't have.

## Architecture

```
[Tesla] --USB--> [Pi: gadget agent] --WiFi/LTE--> [Collector: Mac/Linux] --> [Gemini analyzer (TS)]
```

- **Gadget agent (Go, runs on Pi)** — exposes a sparse exFAT image as a USB mass-storage LUN via Linux configfs/gadget. The car writes `TeslaCam/SentryClips/...` to it as a normal drive.
- **Live exFAT reader (Go, shared code)** — reads the backing image out-of-band while the car has it mounted; parses exFAT directly (read-only, tolerant of a dirty/in-flight filesystem) to detect new `SentryClips/<timestamp>/` folders as they're written. This is the core custom component — no existing library does live/dirty exFAT reads.
- **Collector (Go, Mac/Linux)** — receives event notifications + files from the agent, stores them (SQLite), and triggers downstream analysis (webhook into the existing TS/Gemini pipeline).
- **Simulator (Mac/Linux, dev-only)** — a fake "Tesla writer" that writes realistic SentryClips into a local exFAT image, exercising the same live-reader code without a Pi or car. Primary dev loop.
- **Analyzer (existing TS/Gemini code)** — `analyze-video.ts` and friends; the collector feeds this rather than replacing it.

## Key constraints

- Only SoCs with a dual-role/OTG USB controller (Pi Zero/3/4, etc.) can act as a USB device; Mac and most PC ports are host-only in silicon. This cannot be worked around in software.
- For Mac-side testing without a Pi: use a Linux VM (UTM/QEMU) with the `dummy_hcd` kernel module, which emulates a full gadget+host USB loop entirely in software. This validates the configfs/gadget setup and the exFAT reader end-to-end, but not real dwc2/dwc3 hardware timing or actual Tesla MCU compatibility — final validation needs a real Pi Zero 2 W plugged into the car.
- Reclaiming disk space on the backing image while the car may still be writing to it is the trickiest correctness problem (see teslausb's image-cycling approach for prior art).

## Dev VM (gadget testing without hardware)

A Lima VM defined in `teslcam-dev.yaml` (Debian 13 arm64, full kernel, Go, exfatprogs; ~3 GB on disk). Verified working: `dummy_hcd` + `g_mass_storage` emulate the full USB gadget loop in software — a backing image exposed as a gadget enumerates as `/dev/sda`, mounts, and its writes are readable out-of-band from the raw image.

- `limactl start teslcam-dev` / `limactl shell teslcam-dev` / `limactl stop teslcam-dev`
- The project dir is mounted read-write at the same path inside the VM.
- The genericcloud image boots a "cloud" kernel that lacks gadget modules; provisioning installs `linux-image-arm64` and the cloud kernel has been removed. If recreating the VM from the yaml, stop/start once after first boot to enter the full kernel.

## Phased plan

1. exFAT live-reader + Mac-side simulator (no hardware needed)
2. Pi gadget agent (configfs mass storage, dwc2 overlay, systemd unit)
3. Collector + Gemini integration (SQLite, webhook, push notification)
4. Hardening (power-cut resilience, read-only rootfs, space reclamation, LTE/hotspot connectivity)
