# TODO

## 0a. Re-encrypt the onboarding payloads (security debt, opened 2026-08-01)

BLE onboarding no longer requires an encrypted link. The server token and the
Wi-Fi PSK therefore travel **in the clear** to anyone listening in radio range
during the onboarding window.

Why the link encryption was dropped (see commit `1d68d76`): BlueZ answers every
pairing request with IO capability `NoInputNoOutput` *and* the MITM bit set — a
combination no association model can satisfy — and both macOS and Android
respond by going silent. No SMP failure, no agent callback, nothing in the
unit's log; the app only sees a write that failed. It made first-time
onboarding impossible for every client, and only peers that already held a bond
could get through. Removing the requirement was the way out, and the loss is
narrower than it looks: Just Works has no MITM protection to begin with, so the
gate that ever mattered is physical presence during the onboarding window.

What is genuinely missing is payload confidentiality, and the fix belongs at the
application layer, which is ours to control, rather than in an SMP negotiation
which is not:

- [ ] Encrypt the `config` payload (server URL + token) and the `wifi` connect
      payload (SSID + PSK) end to end between app and agent — e.g. X25519 key
      agreement over the plain characteristics, then AEAD on the payloads.
      Note the device's public key can be published in DeviceInfo, which is
      already a plain read.
- [ ] Keep it independent of BLE bonding, so a reflashed unit (whose bond store
      is empty) never strands a client again.
- [ ] Decide whether to restore `encrypt-*` flags afterwards as defence in
      depth — only if a first-time pairing can be shown to complete on this
      controller.

## 0. Admin dashboard (next up; user accounts are a prereq)

Original prompt:

> New dashboard should be a react app that relies on an admin api that you
> might need to build. This dashboard should allow me to browse each clip
> created by each user. And view everything about it. Details on which camera
> was picked by the client, gemini output, cost, etc.

- [ ] User accounts (in progress — see plan)
- [ ] Admin API on the server
- [ ] React dashboard: browse clips per user; per-clip detail (camera picked
      by the client, Gemini output, cost, …)

(The old `cmd/teslcam-debug` exFAT debug UI was scratched in favor of this.)

# Phase 1: exFAT live-reader + Tesla writer simulator

## 1. exFAT live-reader (Go) — core component

### Scaffolding
- [x] Go module + repo layout (`go.mod`, `internal/exfat/`, `cmd/`)
- [x] Reproducible test fixture: `testdata/scripts/make-fixture.sh` builds an
      exFAT image with a TeslaCam layout + sha256 manifest for golden tests
      (macOS `hdiutil`/`newfs_exfat` and Linux `mkfs.exfat` branches both
      verified working)
- [ ] Add a larger fixture (or size param) with Tesla-realistic 32KB clusters —
      current 64MB fixture gets 4KB clusters from `newfs_exfat`'s defaults;
      verified spec-conformant but not representative of a real 32GB+ dashcam
      drive's allocation size

### Debug tooling
- [x] `teslcam-debug` server + web UI (`cmd/teslcam-debug`): fake-Tesla write
      actions via the OS mount, live tree/status views via our out-of-band
      parser; poll-diff event log. Runs on Mac (hdiutil) or in the VM through
      the full dummy_hcd/g_mass_storage gadget loop (`scripts/vm-debug.sh up`,
      UI at http://localhost:8080)

### Static parsing (read a clean, unmounted image first)
- [x] Boot sector (VBR): validate `EXFAT` signature, extract bytes/sector,
      sectors/cluster, FAT offset, cluster-heap offset, root-dir first cluster
- [x] FAT reader + cluster-chain walker (incl. `NoFatChain` contiguous files,
      which the FAT doesn't describe)
- [x] Directory-entry-set parsing: File (0x85) + Stream Extension (0xC0) +
      File Name (0xC1) entries, entry-set checksum, timestamps, file size
- [x] Path walking: `Lookup`/`ReadDirPath` (case-insensitive) + `Open` reader
      over cluster chains capped at ValidDataLength
- [x] Golden tests: every fixture manifest file resolved by path and
      sha256-verified byte-for-byte (macOS fixture; Linux fixture exercised
      live in the VM via the debug server + watcher)

### Dirty/live tolerance (the hard part)
- [x] Tolerate `VolumeDirty` flag set (never refuse to read; exposed as info)
- [x] Tolerate torn/in-flight directory entry sets: structurally broken sets
      skipped, checksum mismatches reported per-entry (`ChecksumOK`), torn
      FAT chains return partial results, cycles bounded
- [x] Directory entries as source of truth (allocation bitmap never consulted)
- [x] `ValidDataLength` vs `DataLength` (parsed, surfaced in events, `Open`
      never reads past VDL)
- [ ] Torn-write tests: snapshot image mid-write (or fuzz truncated entry
      sets) — unit-level corruption tests exist; no mid-write image snapshots
      or fuzzing yet

### Live watcher
- [x] `internal/watch`: poll loop over the raw image, diff directory state,
      events: DIR_ADDED / FILE_ADDED / FILE_CHANGED / FILE_STABLE / REMOVED;
      checksum-failed entries deferred to next poll
- [x] "File complete" heuristic: size stable across N polls (re-arms if the
      file grows again). `event.json` is metadata, never a finalization marker.
- [x] CLI daemon: `teslcam-watch -image ... -interval ... -stable-polls ...` —
      logs events, flags `>>> NEW SENTRY EVENT` on new SentryClips dirs.
      Verified in the VM gadget loop: 1s detection latency, streaming clip
      seen as ADDED → CHANGED → STABLE
- [x] Copy-out mode (`internal/copyout`): stable files under a path prefix
      extracted to a local dir — queued off FILE_STABLE events, atomic
      temp+rename, entry re-checked after copy (changed-mid-copy files are
      dropped and retried on the next stabilize). Wired into `teslcam-watch`
      via `-copy-to`/`-copy-prefix`; integration harness verifies every
      extracted sentry file byte-for-byte against the simulator journal
- [ ] Watcher efficiency for Pi-scale drives (cost grows with directory count;
      metadata-only today but ~32KB/event-dir/poll): scope walk to TeslaCam,
      skip descending into settled dirs whose parent entry is unchanged,
      adaptive poll interval; endgame is LBA-write sniffing in the Phase-2
      gadget agent (agent sees every SCSI write → reparse only touched dirs)

## 2. Fake Tesla writer (simulator)

- [x] Research pass: 1-min segments × 4 cameras (~28MB/cam/min), rolling
      ~60-min RecentClips buffer (oldest deleted), sentry alert copies last
      ~10 min into `SentryClips/<ts>/` + event.json + thumb.png, in-progress
      segments not closed cleanly on interruption
- [x] Write mechanism: through the OS exFAT mount (like the car), no syncs,
      no clean unmount
- [x] `internal/sim` + `cmd/teslcam-sim`: chunked per-second streaming,
      minute rotation, rolling cap deletion, sentry trigger (last ≤9 min +
      one post-trigger minute), sha256 journal of every write as ground truth,
      TimeScale compression for tests
- [x] Integration harness (`integration/`, macOS): simulator through a real
      hdiutil mount + watcher on the raw image at 150ms concurrently — sentry
      event detected live, deletions observed, every journaled file verified
      byte-for-byte on the still-mounted dirty volume
- [x] Power-cut harness (`integration/powercut_darwin_test.go`): sim runs as
      a subprocess streaming its journal (JSONL via new `-journal-stream` /
      `sim.Config.OnRecord`), SIGKILLed mid-minute after the sentry event.
      Verified: reader salvages the torn unflushed image (every checksummed
      entry readable to ValidDataLength), and post-flush every completed
      write matches the journal byte-for-byte
- [ ] Harness variants remaining: real-time pacing soak (timescale 1,
      ~28MB/cam/min), run in the VM through the gadget loop

## 3. Pi gadget agent (Phase 2)

- [x] `internal/gadget`: configfs mass-storage gadget (setup/bind/teardown,
      UDC auto-detect via /sys/class/udc); pure filesystem ops, unit-tested
      against a fake configfs tree
- [x] `internal/pipeline`: shared watcher → copy-out → upload wiring,
      extracted from teslcam-watch (both daemons now use it)
- [x] `cmd/teslcam-agent`: gadget up → pipeline → gadget teardown on exit;
      replaces a leftover gadget from a crashed run on startup
- [x] Automatic upload compression: serialized ffmpeg worker uses Pi hardware
      H.264, targets 55% of source bitrate (1.2–2.5 Mbps), skips small/HEVC
      clips, and falls back to originals unless at least 10% is saved; all
      thresholds and tool/encoder paths are CLI-configurable
- [x] High-level event uploader: stable files become verified content-addressed
      blobs and typed manifest artifacts; the agent finalizes a generation only
      after a configurable local settle period (`-event-settle`, default 90s).
- [x] systemd unit template (`scripts/teslcam-agent.service`; dwc2 overlay +
      backing image are manual prereqs on the Pi)
- [x] VM end-to-end test (`scripts/vm-agent-test.sh`): agent's configfs
      gadget on dummy_hcd → /dev/sda mount → sim writes a sentry event →
      agent detects/copies/uploads → server completes the event; every
      journaled sentry file verified byte-for-byte, gadget teardown verified
- [ ] Real Pi Zero 2 W bring-up (dwc2, real car): image provisioning script,
      LTE/hotspot connectivity, then Phase-4 hardening (read-only rootfs,
      space reclamation, LBA-write sniffing for watcher efficiency)

## 4. Server + analyzer wiring (Phase 3)

- [x] Server (`internal/server`, `cmd/teslcam-server`): v1 content-addressed
      HTTP ingest (blobs + typed manifests), atomic store on disk, sha256 +
      metadata in SQLite via modernc.org/sqlite, inspection API
      (`GET /events[/<id>]`)
- [x] Event completion v1: agent explicitly finalizes a versioned manifest;
      server verifies every declared blob before atomically marking it ready.
      (The legacy event.json + QuietPeriod `/files` upload path has been removed.)
- [x] Clip selection: trigger camera from event.json camera code (pillar
      codes map to repeaters), latest clip at/before the event timestamp,
      graceful fallbacks; AppleDouble junk ignored
- [x] Analyzer trigger: AnalyzeCmd subprocess on the selected clip, JSON
      verdict stored (threat_level extracted), failures recorded with stderr
- [x] `analyze-video.ts --json`: Gemini structured output (responseSchema),
      verdict JSON on stdout, progress on stderr — parseable by the server
- [x] Event uploader (`internal/eventupload`): idempotent event upsert, SHA-256
      blob upload, typed manifest, generation finalization, and retry.
- [x] Durable SQLite analysis jobs: finalized generations are queued in the
      same transaction, running jobs are reclaimed after restart, and analyzer
      failures receive three bounded attempts.
- [x] VPS deployment (`scripts/deploy-server.sh`): `setup` (service user,
      dirs, /etc/teslcam/server.env, systemd unit — analysis off until
      GEMINI_API_KEY + ANALYZE_CMD are set), `deploy` (arch-detected static
      cross-compile, binary + analyzer rsync, npm ci, restart, bounded
      health check), `status`/`logs`. Target adri@vps, override TESLCAM_VPS.
      Server listens on 127.0.0.1 behind a TLS reverse proxy on a public
      api.<domain> subdomain (TLS in transit + bearer token). Not yet run
      against the real VPS
- [ ] Real end-to-end run with Gemini (needs GEMINI_API_KEY; harness uses a
      fake analyzer — sim clips aren't real video, so use a real TeslaCam
      clip via `teslcam-server -analyze`)
- [x] Bearer-token auth: server requires `Authorization: Bearer` on all
      endpoints but /healthz (constant-time compare); uploader/pipeline send
      it; all daemons take `-token-file` (no tokens in argv); deploy setup
      generates /etc/teslcam/ingest.token; VM e2e test runs with auth on and
      asserts 401s. Not yet deployed to the VPS
- [ ] Push notification on high threat_level
- [ ] Retention/cleanup of stored clips + analysis cost controls

## 5. Later phases (unchanged, see CLAUDE.md)
- Hardening
