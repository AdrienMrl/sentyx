# Unmount incident (2026-08-08) — hardening & investigation plan

Root cause of the 2026-08-08 evening incident, established from blackbox events,
backfilled heartbeats, and the power-evidence log:

- **Two mid-outing unmounts (17:58 and 18:50 PDT):** car cut glovebox USB power
  at short stops (`PM_RSTS=0x1000` power-on reset, no watchdog, no pstore, no
  under-voltage — the Pi never crashed). The "unmounted" icon seen after driving
  off was the ~90 s reboot/remount gap.
- **Fatal unmount at 21:58:34 PDT, mid-drive:** gadget detached, then the car
  reset the bus ~50× over 26 s without enumeration ever completing, and gave up.
  The Pi was throttled to 600 MHz at load ~5.7 (84–86 °C, ffmpeg backlog from
  hours of failed uploads). CPU starvation of enumeration is the leading
  hypothesis but unproven — a load-independent dwc2 wedge would look identical.

## Investigate

1. **Bench-test enumeration under load** — desk Pi as gadget into a host: cap
   CPU (`cpufreq` 600 MHz) + saturate 4 cores, have the host re-enumerate
   repeatedly; compare with idle. Settles CPU-starvation vs. dwc2-wedge and
   picks the right fix (this decides the shape of item 9).
2. **Make journald persistent** (the Trixie volatile-override gotcha, the
   `40-rpi` drop-in) — the 21:58 dmesg evidence would have been lost by one
   reboot; kernel logs must survive.
3. **Blackbox: record enumeration storms** — watch kernel USB events and log
   `reset-storm` (N resets without `configured` in a window) as a first-class
   blackbox event with counts, so future occurrences are visible server-side
   without SSH.
4. **Richer heartbeats** — add CPU freq, load, and UDC state so throttle/attach
   correlation is queryable from the server after the fact.
5. **Check the Hologram plan** (dashboard) — expired plan is the prime suspect
   for the LTE DNS timeouts; then verify WG handshakes over LTE (DO firewall
   udp/51821 is now open, verified 2026-08-09) and that the eth1 nft allowlist
   passes UDP 51821 to the VPS.
6. **Fix the 422 finalize loop** — reproduce `manifest contains no video
   artifact` locally with `-select-clips`; those retry loops kept the backlog
   (and heat) alive for hours.

## Harden

7. **Keep the Pi off the thermal ceiling** — ffmpeg/ffprobe into a systemd
   slice with `CPUQuota` (~100–150%) + `Nice`/`IOSchedulingClass=idle`; single
   compression at a time.
8. **Yield to the car** — pause compression/scoring while
   `udc=configured && writes=active`; process backlog when the car is idle or
   on wifi.
9. **Storm recovery in the agent** — on detected enumeration storm: pause all
   heavy jobs, cool down briefly, unbind/re-bind the UDC to offer the car a
   fresh attach (the car clearly retries; give it a clean target).
10. **Prioritize the USB path** — real-time or elevated priority for the
    gadget-critical threads so enumeration never competes with batch work.
11. **Server-side alert** — extend the watchdog to Telegram-alert on
    `reset-storm` or "not attached while recording expected", so you learn
    before you look at the dash.
12. **Shrink the post-power-cut gap** — measure and trim boot-to-gadget time;
    the mid-drive "unmounted" sightings were the ~90 s window after the car
    re-powered the port.
13. **Enable the hardware watchdog** (`RuntimeWatchdogSec=15s`) — no freeze
    observed yet, but it's free insurance for the failure mode we can't log.
14. **Make agent flags OTA-updatable** — DONE (branch `feat/agent-args`). — an application release ships only
    binaries into `/opt/teslcam/releases/<id>/` and flips the `current`
    symlink; it never touches `/etc/systemd/system/teslcam-agent.service`. So
    any change needing a new flag (e.g. adding `-blackbox-*` to a unit flashed
    without it) currently requires SSH, a reflash, or a system release whose
    `postInstall` rewrites the unit as root. Fix: move the arguments out of the
    unit into an `agent.args` file shipped **inside** the signed bundle, with a
    permanently stable unit:

        EnvironmentFile=/opt/teslcam/current/agent.args
        ExecStart=/opt/teslcam/current/teslcam-agent $TESLCAM_AGENT_ARGS

    The symlink flip then swaps binary and flags atomically, and the existing
    rollback covers both — no root shell in manifests, no new failure surface
    in the updater, and flags can never drift from the binary expecting them.
    Per-unit identity (`POST_TO`, `DEVICE_ID`, tokens) stays in
    `/etc/teslcam/agent.env`, which BLE onboarding provisions and updates must
    preserve. The unit should hard-fail if `agent.args` is missing rather than
    starting with an empty argument list.

    Remaining debt: units already flashed carry the OLD unit file, so this
    change needs one visit each (SSH over WireGuard, or a system release with
    a `postInstall`) before their flags become OTA-updatable. After that hop,
    a flag change is an ordinary application release with rollback.

**Suggested order:** 14 before the next field flash (it is the one change that
makes every later flag change shippable); 2, 7, 8 are quick wins; 1 decides the shape of
9; 5, 6 stop the backlog from ever recreating the incident's conditions;
3, 4, 11 make the next occurrence diagnosable in minutes.
