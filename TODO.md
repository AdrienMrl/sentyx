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
- [ ] Copy-out mode: extract stable clips to a local dir (feeds the collector)
- [ ] Watcher efficiency for Pi-scale drives (cost grows with directory count;
      metadata-only today but ~32KB/event-dir/poll): scope walk to TeslaCam,
      skip descending into settled dirs whose parent entry is unchanged,
      adaptive poll interval; endgame is LBA-write sniffing in the Phase-2
      gadget agent (agent sees every SCSI write → reparse only touched dirs)

## 2. Fake Tesla writer (simulator) — needs a planning pass first

- [ ] **Research real TeslaCam write behavior** (docs, teslausb issues, our own
      SentryClips samples): folder naming `YYYY-MM-DD_HH-MM-SS/`, per-camera
      files (`-front.mp4`, `-back.mp4`, `-left_repeater.mp4`,
      `-right_repeater.mp4`), 1-minute segments, `event.json`, `thumb.png`,
      RecentClips rolling buffer, write pacing/order
- [ ] Decide write mechanism: mount image via OS exFAT driver
      (`hdiutil attach` on Mac / kernel exfat in VM) and write through it —
      most realistic; the car also writes via a normal driver
- [ ] Writer that streams realistic clips at realistic pace (append in chunks,
      ~36 MB/min/camera), sets dirty bit while mounted, never cleanly unmounts
- [ ] Scenario scripting: sentry event → burst of 4-camera segments + event.json
- [ ] Integration harness: writer (mounted) + live-reader (raw image) running
      concurrently; assert reader sees every event with correct content

## 3. Later phases (unchanged, see CLAUDE.md)
- Pi gadget agent · Collector + Gemini integration · Hardening
