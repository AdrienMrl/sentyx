# teslcam-exp

## Purpose and architecture

Collect Tesla Sentry events while the car writes them: Tesla USB → Linux Pi
mass-storage gadget → WiFi/LTE → Go server → analyzer. Development supports
macOS and Linux; USB device mode requires an OTG/dual-role controller on the Pi.
Mac/PC host-only ports cannot emulate this in software.

- Agent: sparse exFAT LUN via configfs; read-only live/dirty exFAT parsing;
  durable SQLite upload spool (`-spool-db`, `-spool-max-mb`). `-select-clips`
  uploads the trigger clip plus metadata; all clips still extract locally.
- Ingestion: upsert → content-addressed blobs → manifest → explicit finalize.
  See `docs/ingestion-api.html` and `docs/api-reference.html`.
- Server: SQLite storage, selected-clip analysis, optional Telegram/FCM alerts.
  Gemini remains in production (`internal/gemini`); preserve it until a
  replacement wins. Research is in `bench/analyzers/vjepa/`; follow `goal.md`
  for the current objective and allowed approaches, not Gemini prompt tuning.
- The simulator exercises the live reader locally. Space reclamation while
  the car writes is correctness-sensitive; see teslausb's image cycling.

## Development

- Start the server with `scripts/run-server-local.sh`, not bare `go run`.
  `ANALYZE=1` enables Gemini; `DATA` and `LISTEN` override defaults.
- End-to-end ingest client: `cmd/teslcam-test/README.md`.
- Gadget VM: `teslcam-dev.yaml`; use `limactl start|shell|stop teslcam-dev`.
  The project is mounted read-write at the same path. `dummy_hcd` plus
  `g_mass_storage` tests the gadget/host loop, not hardware timing or Tesla
  compatibility. Validate those with a real Pi in the car.
- Recreated VMs need a stop/start after provisioning to enter the full
  `linux-image-arm64` kernel; the cloud kernel lacks gadget modules.

## Research

Read `goal.md` and the existing `bench/research/` record when resuming.
Keep the dashboard updated throughout work, including delegated experiments:
log focus before starting, launches, results/failures, meaningful checkpoints,
dataset changes, decisions, and blockers. The lead owns these updates.
Write to this checkout even when computation is remote; keep `goal.json`
aligned with the objective and actual status. A failed experiment does not
make the overall goal blocked or complete.

Schema and commands: `docs/research-progress.md`.
Dashboard: `go run ./cmd/teslcam-research` → http://127.0.0.1:8770
(read-only; refreshes every 30 seconds).

## Machines and deployment

- VPS: `adri@vps` / `161.35.232.246` (x86_64), Caddy TLS;
  public URL `https://teslcam.161-35-232-246.sslip.io`.
  Use `scripts/deploy-server.sh setup|deploy|status|logs` or `caddy <host>`;
  override target with `TESLCAM_VPS=user@host`.
  Config: `/etc/teslcam/server.env`; data: `/var/lib/teslcam`;
  bearer token: `/etc/teslcam/ingest.token`. Binary/launcher:
  `/usr/local/bin/teslcam-server` and `teslcam-server-start`.
  Only `/healthz` is unauthenticated. Empty `TELEGRAM_CHAT_ID` disables alerts;
  empty `FCM_CREDENTIALS_FILE` disables Android push.
- Prototype: Pi 4 B at `adri@sentyx.local`, repo `~/code/sentyx`, Go in
  `/usr/local/go`. Keep CPU load low: glovebox USB supplies roughly 1–2A.
  `dwc2,dr_mode=peripheral`, `libcomposite`, UDC `fe980000.usb`.
  `/var/lib/teslcam/backing.img` **must have an MBR and one exFAT partition**;
  Tesla ignores superfloppy images (`exfat.LocateVolume`). Real-car operation
  verified on firmware 2026.14. Account for encrypted clips on newer Ryzen MCU
  firmware before upload; do not assume every MP4 is plaintext.
- LTE: metered Hologram dongle on `eth1`; agent fallback uses `internal/lte` /
  `-lte-iface`. **No general default route**: restricted policy table 101
  provides fallback routing. Preserve the nftables allowlist (server, dongle
  LAN/DNS, fixed NTP peers) and disabled background updates.
  See `hardware/lte-dongle.md`.
- Field SSH: Pi-initiated WireGuard (`wg1`, `10.8.0.0/24`, UDP 51821) to VPS;
  per-unit keys provisioned at flash time, never baked in. Cloud firewall must
  allow UDP 51821. See `docs/remote-access-wireguard.md`.
- GPU PC: `adri@Adri-PC` (verified SSH via `100.97.2.123`; old LAN address `192.168.1.58` is stale), RTX 4070 SUPER **12 GB VRAM**,
  Ryzen 5 5500, 32 GB RAM. Check availability and disk capacity before use;
  C: has previously been nearly full.

## Field images and OTA

- Build: `scripts/build-image.sh <backing-gb> <authorized-keys-file>` in the
  Lima VM; output `build/teslcam-pi4-<version>.img.xz`. Pi OS Lite arm64,
  SD/USB SSD boot, first-boot sparse backing image, BLE secret provisioning.
  **Never bake secrets into images.** Smoke-test with `scripts/test-image.sh`;
  firmware/EEPROM/dwc2/BLE still require hardware validation.
- Flash: `scripts/flash-image.sh`. USB Ethernet and serial development access
  are enabled by default. For car-bound units use `TESLCAM_DEV_LINK=0`:
  the composite gadget is not car-validated and serial gives passwordless root.
- OTA workflow: `docs/ota-updates.md`; operator CLI `cmd/teslcam-ota`, Pi
  service `cmd/teslcam-updater`, verification `internal/ota`, API `/v1/ota`.
  Units pull signed releases; order by monotonic manifest `sequence`, never
  CalVer text. Rollback or three failed devices pauses a campaign.
- **Signing private keys never reach the repo, Pi, or VPS.** Images carry
  only `/etc/teslcam/ota-release.pub.pem`.
- App releases swap `/opt/teslcam/current`, including signed `agent.args`;
  current Pi 4 image units read it, so flags can change through OTA. Releases
  do not replace systemd units; older units need migration to this mechanism.
  Keep per-unit values in `/etc/teslcam/agent.env`; see `docs/ota-updates.md`.
- Installs wait for `/run/teslcam/update-ready.sock`; failed agent healthchecks
  roll back the symlink. APT releases pin exact versions and have **no rollback**;
  recover with WireGuard/SSH or reflash.
- The Android app cannot display or trigger real OTA updates; firmware screens
  use `DemoDeviceRepository`. Real OTA is operator CLI only.

## Communication

Be clear, concise, and technically precise. Preserve evidence, uncertainty,
and material constraints; usually aim for 3–5 sentences.
