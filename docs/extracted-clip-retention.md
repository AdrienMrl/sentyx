# Extracted-clip retention

The Tesla recording image is separate from extracted footage. This policy
never modifies `backing.img` or its exFAT volume.

The production agent arguments now require:

- `-spool-max-mb 2048`: total extracted event-file budget, including unselected
  cameras, pending uploads and temporary transcodes—not only SQLite records.
- `-storage-reserve-mb 2048`: available filesystem space to preserve for the OS.

Oldest events are discarded as whole directories when either limit requires
space, even if they never uploaded. This intentionally trades old evidence for
continued operation during extended backend outages. There is no promise to
retain every offline event. Events larger than the budget can themselves be
discarded. Other processes can still exhaust the disk; the agent refuses new
extraction if cleanup cannot restore its reserve.

Cleanup runs before the upload database opens, before extraction/transcoding,
and every 30 seconds. Writers and cleanup share a lock; extraction reserves the
incoming file size and transcoding reserves twice the source size. FFmpeg also
has an output-size ceiling; ineffective transcodes use the original instead.

`<copy-to>/.retention-cutoff` is an fsynced event-name high-water mark, persisted
before deleting event directories. It prevents a reboot's image rescan from
re-extracting discarded events; an interrupted deletion is replayed. Old paths
are rejected *before* making room for new writes, so stale work cannot evict
newer footage. Canonical event timestamps are required; symlink trees and
unexpected event-root entries fail closed rather than deleting unknown data.

Upload enqueue/retries and finalization consult the same cutoff. Discarded
events' SQLite item/artifact/event rows are retired, including on restart.
An in-flight remote request can finish before noticing a discard, but the
agent will not subsequently finalize that discarded event.

## 2026-09-10 field repair

The Pi had 0 available bytes, a 64 GiB recording image, and approximately
50 GiB of extracted files. The production backend was stopped. A validated
old extracted event (`2026-08-13_17-37-46`, approximately 1.8 GiB) was removed
to create space for the signed update; no recording-image files were changed.

The release was served from Mac loopback through an SSH reverse tunnel using
`scripts/ota-local-server.py`, with only the intended device's update endpoints
and signed artifact exposed. The existing Pi updater performed signature/hash
verification, readiness gating, installation and rollback-capable activation.
The ordinary updater service was temporarily stopped to prevent concurrent
updates and restored afterwards. The production backend was not restarted.

Initial release `2026.9.11-retention1` (sequence 4) reclaimed approximately
48 GiB. Follow-up `2026.9.11-retention2` (sequence 5) adds the explicit
stale-work-before-eviction guard and path traversal rejection, with regression
tests. These are application firmware updates, not OS/package upgrades.

Tests: race-enabled clipretention, copyout, pipeline, eventupload,
videocompress, OTA and updater suites; Linux ARM64 cross-build.

Final on-device verification: updater state reports version
`2026.9.11-retention2`, application sequence 5; the installed agent SHA-256
matches the local cross-build
`afef92699b2fd0e113ca700581c64ad55f6073957364874364ac38a3369a3dc5`.
Agent active, zero systemd auto-restarts, USB gadget bound to `fe980000.usb`,
recording-image size unchanged at 68,719,476,736 bytes. Linux available space
is approximately 48 GiB, and extracted footage remains around the 2 GiB cap
while the baseline scan continues. The ordinary updater was restored and the
temporary local servers/tunnels stopped. The production backend remains off.
