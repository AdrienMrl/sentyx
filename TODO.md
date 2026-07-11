# TODO — Phase 1: exFAT live-reader + Tesla writer simulator

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
      file grows again). Later: treat event.json as event-finalized marker
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

- [x] Server (`internal/server`, `cmd/teslcam-server`): HTTP ingest
      (`PUT /files/TeslaCam/SentryClips/<event>/<file>`, atomic store on
      disk, sha256 + metadata in SQLite via modernc.org/sqlite), event
      metadata parsed from event.json, inspection API (`GET /events[/<id>]`)
- [x] Event completion: complete once event.json received and no file for
      QuietPeriod (covers the post-trigger minute); analysis triggered once,
      re-enqueued on restart if the server died mid-analysis
- [x] Clip selection: trigger camera from event.json camera code (pillar
      codes map to repeaters), latest clip at/before the event timestamp,
      graceful fallbacks; AppleDouble junk ignored
- [x] Analyzer trigger: AnalyzeCmd subprocess on the selected clip, JSON
      verdict stored (threat_level extracted), failures recorded with stderr
- [x] `analyze-video.ts --json`: Gemini structured output (responseSchema),
      verdict JSON on stdout, progress on stderr — parseable by the server
- [x] Uploader (`internal/upload`): pushes copy-out results to the server
      with retry; `teslcam-watch -post-to` (same HTTP path the Pi agent will
      use). Live harness now runs the full loop — sim → watcher → copy-out →
      upload → server → analyzer — and verifies every sentry file arrived
      byte-identical and the event was completed + analyzed
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
