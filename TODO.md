# TODO — Phase 1: exFAT live-reader + Tesla writer simulator

## 1. exFAT live-reader (Go) — core component

### Scaffolding
- [ ] Go module + repo layout (`go.mod`, `internal/exfat/`, `cmd/`)
- [ ] Reproducible test fixture: script that creates a small exFAT image
      (macOS `hdiutil`/`newfs_exfat`, Linux `mkfs.exfat`) with known contents

### Static parsing (read a clean, unmounted image first)
- [ ] Boot sector (VBR): validate `EXFAT` signature, extract bytes/sector,
      sectors/cluster, FAT offset, cluster-heap offset, root-dir first cluster
- [ ] FAT reader + cluster-chain walker (incl. `NoFatChain` contiguous files,
      which the FAT doesn't describe)
- [ ] Directory-entry-set parsing: File (0x85) + Stream Extension (0xC0) +
      File Name (0xC1) entries, entry-set checksum, timestamps, file size
- [ ] Path walking: resolve `TeslaCam/SentryClips/...`, list dirs, read file
      contents out to a local copy
- [ ] Golden tests against fixture images written by real OS drivers
      (macOS and Linux produce different-but-valid layouts — test both)

### Dirty/live tolerance (the hard part)
- [ ] Tolerate `VolumeDirty` flag set (never refuse to read)
- [ ] Tolerate torn/in-flight directory entry sets: bad checksums, secondary
      count mismatch, allocated-but-incomplete entries — skip, don't crash
- [ ] Never trust the allocation bitmap or FAT for liveness; re-read
      directory entries as source of truth
- [ ] Handle file size growing between polls (stream-extension `ValidDataLength`
      vs `DataLength`)
- [ ] Torn-write tests: snapshot image mid-write (or fuzz truncated entry sets)

### Live watcher
- [ ] Poll loop over the raw image: diff directory state, emit events for
      new `SentryClips/<timestamp>/` dirs and new/updated files
- [ ] "File complete" heuristic: size stable across N polls (later: event.json
      presence marks the event finalized)
- [ ] CLI: `teslcam-watch <image>` — prints events, optionally copies out clips

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
