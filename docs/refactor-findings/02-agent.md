# Refactor findings — Pi agent side

Scope: `cmd/teslcam-agent`, `internal/{gadget,exfat,watch,eventupload,pipeline,clipselect,cameraselect,videocompress,copyout,health,blepair,wifi,dashcam}`.
Lens: SOLID (SRP / interface segregation / dependency inversion), field robustness (power cuts, offline windows, wedged subprocesses), and testability without a real Pi. Audited 2026-07-16 at commit `6af2671`.

## Summary table

| # | Priority | Title | Area |
|---|----------|-------|------|
| 1 | P1 | A wedged ffmpeg stalls the compression worker forever — and eventually the whole pipeline | videocompress / pipeline |
| 2 | P1 | Uploader retry is a fixed 5 s per-item timer: no backoff, no jitter, no offline gating | eventupload |
| 3 | P2 | `eventupload.Client` is four components in one struct (protocol client, queue, settle state machine, spool/eviction) | eventupload |
| 4 | P2 | `main()` is a 373-line monolith with ~45 flags and untestable interaction rules | cmd/teslcam-agent |
| 5 | P2 | `pipeline.Run` is a god-function wired to concrete types; the interesting orchestration is untestable | pipeline |
| 6 | P2 | Health collectors shell out with no timeout — a hung `vcgencmd` silently kills heartbeats and the local health log | health |
| 7 | P2 | `btmgmt` calls in bluez.go: no timeout, no injectable runner (untestable, hang risk in watchdog path) | blepair |
| 8 | P2 | `writeFileAtomic` doesn't fsync — a power cut after onboarding can leave a truncated/empty `server.token` | blepair/configstore |
| 9 | P2 | Unbounded in-memory event maps: finalized events and clip-selector state are never freed | eventupload, pipeline/select |
| 10 | P2 | Torn-directory snapshots emit spurious `Removed` → re-add churn and reset stability counters | watch |
| 11 | P3 | Fatal exits between gadget `Setup()` and `pipeline.Run` leave the gadget bound (self-heals next run) | cmd/teslcam-agent |
| 12 | P3 | `copyout` trusts image paths: a `..` component in an exFAT name escapes `DestDir` | copyout |
| 13 | P3 | Retry timers fire after shutdown and write to a closed spool DB | eventupload |
| 14 | P3 | Spool eviction accounting records size 0 when the enqueue-time stat fails | eventupload/durable |
| 15 | P3 | Hardcoded magic values scattered across packages (15-min score deadline, BLE chunk 100, `/usage` auth probe, retry 5 s, walk depth 8) | several |
| 16 | P3 | `watchDisconnects` goroutine leaks and treats *any* BlueZ device disconnect as "the central left" | blepair/bluez |
| 17 | P3 | `readEventMetadata` swallows read/parse errors silently | eventupload |
| 18 | P3 | `dashcam.FetchKeys` fails the whole batch on one bad clip; package has no tests | dashcam |
| 19 | P3 | Stale LED-era comments in blepair contradict the shipped design | blepair |

**What's already good** (worth preserving through any refactor): `gadget` is fully unit-testable against a fake configfs tree; `wifi.Manager` + injectable `commandRunner` is exactly the right seam (and the pattern findings 6/7 should copy); `blepair.session` is a clean state machine behind `configSink`/`connTester` interfaces; `cameraselect.Scorer` is a tidy interface with a policy split from signal extraction; copy-out's read → re-verify → fsync → rename dance is the right power-cut discipline; the durable spool's event-as-unit-of-durability design (durable.go's header comment) is sound and well-reasoned.

---

## P1 — hot

### 1. A wedged ffmpeg stalls the compression worker forever — and eventually the whole pipeline

**Where:**
- `/Users/adri/dev/teslcam-exp/internal/pipeline/pipeline.go:170` — `compressor.Compress(ctx, job.localPath)` gets the *pipeline lifetime* ctx, nothing shorter.
- `/Users/adri/dev/teslcam-exp/internal/videocompress/videocompress.go:223` and `:231` — `exec.CommandContext(ctx, ...)` for ffmpeg/ffprobe inherit that same unbounded ctx.
- `/Users/adri/dev/teslcam-exp/internal/pipeline/pipeline.go:201-210` — `enqueueFile` blocks when `compressionJobs` (buffer 128) is full.

**Why it matters:** ffmpeg on a thermally-stressed Pi with a hardware encoder (`h264_v4l2m2m`) is exactly the kind of process that hangs rather than fails — V4L2 M2M deadlocks are a known failure mode. There is a single compression goroutine; one hang stops all MP4 uploads *silently* (non-MP4s still flow, heartbeats stay green). Worse: once the 128-slot channel fills, `enqueueFile` blocks, which blocks the copier's `onCopy` callback (`pipeline.go:237-259`), which stops copy-out entirely — during an active Sentry event. Contrast with `cameraselect.CommandScorer`, which gets per-run timeout, cooldown, and thermal guard; the *much longer-running* ffmpeg gets none of that.

**Direction:** add a required `Timeout` to `videocompress.Config` (no implicit default, per project rule — wire a `-video-timeout` flag) and wrap each `Compress` call in `context.WithTimeout`. A clip is ≤ ~60 s of video; a 5–10 min ceiling is generous. On timeout, the existing fallback already does the right thing: log and upload the original. Cheap, ~15 lines, removes the only "pipeline stops and nothing tells you" failure mode found in this audit.

### 2. Uploader retry is a fixed 5 s per-item timer: no backoff, no jitter, no offline gating

**Where:**
- `/Users/adri/dev/teslcam-exp/internal/eventupload/eventupload.go:166` — `time.AfterFunc(c.cfg.RetryDelay, func() { c.Enqueue(it) })` on every failure.
- `/Users/adri/dev/teslcam-exp/internal/eventupload/eventupload.go:195` — finalize failures likewise retry on the flat `RetryDelay`.
- `/Users/adri/dev/teslcam-exp/cmd/teslcam-agent/main.go:349` — `RetryDelay: 5 * time.Second`, hardcoded, not a flag.

**Why it matters:** the product's core promise is "multi-day offline windows survive". During those windows, every pending item wakes every 5 s and drives a fresh upsert + hash + PUT attempt. With N spooled clips that's a continuous cycle of DNS lookups / TLS attempts / LTE radio wakeups — needless power and modem churn in a car whose glovebox USB budget is already the constraint (`hashFile` at `eventupload.go:336` also re-hashes the full file on *every* attempt, before the cheap connectivity check has a chance to fail). And when the server comes back after an outage, a fleet of agents all retrying on the same flat 5 s beat is a synchronized stampede.

**Direction:** two small changes rather than one big one:
1. Per-item (or better, global "server unreachable") exponential backoff with jitter: 5 s → cap at 5–10 min. State can live in the in-memory `Item` companion; it does not need to be durable — a reboot resetting backoff is fine.
2. Reorder `processItem` so the cheap network call (`upsertEvent`) happens before `hashFile`, so an offline attempt costs one failed dial, not a full SHA-256 of a video file.

A circuit-breaker ("first failure trips 30 s of quiet for the whole queue") would collapse N per-item timers into one and is arguably simpler than per-item backoff — either is fine; the flat 5 s is not.

---

## P2 — worthwhile

### 3. `eventupload.Client` is four components in one struct

**Where:** `/Users/adri/dev/teslcam-exp/internal/eventupload/eventupload.go` (577 lines) + `durable.go` (426 lines). One `Client` owns:
1. HTTP protocol transport (`do`, `doJSON`, `putBlob`, `upsertEvent`, `finalize` — lines 365–468),
2. the in-memory work queue (`Enqueue`/`pop`/`wake` — lines 109–141, 273–284),
3. the event settle/finalize state machine (`events`, `dirty`, `due`, tick loop — lines 144–201, 286–363),
4. durable-spool policy (reconcile, evict, over-cap warning throttling — lines 212–271).

**Why it matters:** the settle/finalize state machine is the most correctness-critical logic on the agent (it decides when the server gets told "this event is complete") and it can currently only be tested through a live `httptest` server plus real SQLite plus real timers — `eventupload_test.go` is 647 lines largely because of that. Concretely: subtle rules like "re-seen artifact of a finalized event is skipped" (`eventupload.go:309-312`) or "reconciled events get a fresh settle window" (`:232`) deserve table-driven tests against a fake transport.

**Direction:** don't shatter it — one incision is enough. Extract the five protocol calls behind a small interface (`type transport interface { UpsertEvent(...) (int, error); PutBlob(...); PutManifest(...); Finalize(...) }`) with the HTTP implementation in its own file. The state machine then becomes testable with a recording fake, and finding 2's backoff has a natural home (wrap the transport). The spool store is already well-separated; leave it.

### 4. `main()` is a 373-line monolith with ~45 flags and untestable interaction rules

**Where:** `/Users/adri/dev/teslcam-exp/cmd/teslcam-agent/main.go:46-373`.

**Why it matters:** the flag *interaction* rules are genuinely subtle and completely untested: `selectClipsActive` gating (`main.go:107-117`), the "stray `-ble-*` flag without `-ble-onboard` fails loudly" scan (`:127-155`), the "token file may be missing only when BLE onboarding is on" carve-out (`:156-168`), heartbeat inertness pre-provisioning (`:283-326`). These encode the provisioning lifecycle — the thing most likely to regress when the next milestone touches onboarding. They can only be verified today by running the binary on Linux with a UDC present, because validation is interleaved with `gadget.Setup()` and `log.Fatal`.

**Direction:** split into `type agentConfig struct {...}` + `parseFlags([]string) (agentConfig, error)` + `validate(agentConfig) error` (pure, unit-testable on macOS) and a `run(ctx, agentConfig) error` that does the wiring. `main` shrinks to parse → validate → run → teardown. This also fixes finding 11 for free (validate everything before touching configfs) and gives the BLE/heartbeat/scorer gating rules a test home. The video/camera flag groups could also collapse into their packages' own `FlagSet` registration helpers, but that's optional polish.

### 5. `pipeline.Run` is a god-function wired to concrete types

**Where:** `/Users/adri/dev/teslcam-exp/internal/pipeline/pipeline.go:76-295`. Inside one function: config validation (40 lines), uploader construction + goroutine, compression worker construction + goroutine, selector construction, copier construction + goroutine, watcher event routing with priority logic.

**Why it matters (DIP/testability):** `pipeline` depends on concrete `*eventupload.Client`, `*copyout.Copier`, `*videocompress.Compressor`. The routing decisions — "stable MP4 → selector → promote → compress → upload", "event.json gets `copyPriorityMetadata`", "selected clip jumps to `copyPrioritySelected`" — are the agent's actual business logic, and `pipeline_test.go` is 77 lines because none of it can run without the real uploader (which demands a BaseURL) and real image files. `select_test.go` (401 lines) shows the codebase already knows how to test against the `enqueue func(localPath, imagePath string)` seam; the rest of the pipeline never got the same treatment.

**Direction:** minimal-ceremony version: define two one-method interfaces where pipeline consumes them (`type enqueuer interface { Enqueue(eventupload.Item) bool }` and `type promoter interface { EnqueuePriority(string, int) bool; Promote(string, int) bool }`), accept them via unexported constructor `newRouter(...)`, and make `Run` a thin composition of `newRouter` + real components. The watcher-event → priority → selector flow then gets table-driven tests. Go's implicit interfaces mean `eventupload`/`copyout` don't change at all.

### 6. Health collectors shell out with no timeout — a hung `vcgencmd` silently kills heartbeats

**Where:**
- `/Users/adri/dev/teslcam-exp/internal/health/collect_linux.go:87` — `exec.Command(path, "get_throttled").Output()` (vcgencmd talks to the GPU firmware mailbox; it has documented wedge modes when the firmware is unhappy — precisely the under-voltage/overheat situations this collector exists to report).
- `collect_linux.go:153` and `:160` — `iwgetid` / `nmcli` likewise uncontexted.

**Why it matters:** `collect()` is called synchronously from `Reporter.sendOnce` (`reporter.go:125`) and `LocalLog` (`locallog.go:25`). One hung child blocks *both* telemetry loops forever, with no log line — the exact "car parked in the heat" window the local log was built to audit becomes the window with no data. Also, wifi/nmcli here duplicates what `internal/wifi` already does properly with timeouts.

**Direction:** thread a `context.WithTimeout(ctx, 5*time.Second)` from the reporter/locallog into `collect` and use `exec.CommandContext` for all three binaries. Secondary (testability): the sysfs/proc paths (`/proc/loadavg`, `/sys/class/thermal/...`, `/proc/net/wireless`) are hardcoded, so `collect_linux.go` is only exercised on a Pi; a tiny `type paths struct` default that tests can point at a tempdir would make the parsers (already pure: `parseThrottled`, `rssiToPct`) fully covered along with their file plumbing. Reuse the `commandRunner` pattern from `wifi/nmcli.go:34`.

### 7. `btmgmt` calls in bluez.go: no timeout, no injectable runner

**Where:**
- `/Users/adri/dev/teslcam-exp/internal/blepair/bluez.go:342` — `exec.Command("btmgmt", ... "rm-adv" ...).Run()` (error deliberately ignored, fine).
- `bluez.go:354` — `add-adv` with `CombinedOutput()`, no context.
- `bluez.go:395` — `rm-adv` during unregister.

**Why it matters:** btmgmt talks to the same kernel mgmt socket that this code already documents as getting into kernel/controller desync states (`bluez.go:333-335`). If btmgmt blocks on a wedged controller, `registerLegacyAdv` blocks holding `advMu`; the disconnect handler (`bluez.go:433-436`) then blocks the godbus dispatch goroutine → all GATT reads/writes stop responding — a BLE hang caused by the BLE watchdog. Also, `btmgmt` is resolved from `$PATH` with no configurability, and none of the fallback-advertising logic is testable off-Pi (`gadget_test.go` proves the team values that; blepair's D-Bus half is inherently hardware-bound, but the exec half needn't be).

**Direction:** copy `wifi`'s `commandRunner` seam into `gattServer` (default `execRunner` with a 10 s `context.WithTimeout`), and run the disconnect-path re-assert (`bluez.go:435`) on its own goroutine so godbus dispatch never blocks on it. That makes the rm-adv/add-adv/watchdog sequencing unit-testable with a fake runner.

### 8. `writeFileAtomic` doesn't fsync — power cut can leave a truncated `server.token`

**Where:** `/Users/adri/dev/teslcam-exp/internal/blepair/configstore.go:47-57` — `os.WriteFile(tmp) → os.Rename` with no `File.Sync()` and no directory fsync.

**Why it matters:** onboarding ends with `complete()` scheduling a systemd restart 2 s later (`session.go:237-241`); the user's very next action is often plugging the Pi into the car — where power is cut whenever the car sleeps (see project memory: the car kills glovebox USB). On ext4, rename-to-a-*new* path (first provisioning: `server.token` doesn't exist yet, so `auto_da_alloc`'s replace-via-rename heuristic does not apply) can leave a zero-length file after an ill-timed cut. `TokenPath()` existence is the provisioning marker (`configstore.go:20`, `blepair.go:64`), so the failure mode is: device believes it's provisioned, keeps its bonds, refuses `begin_pair` re-onboarding semantics, and runs with an empty token that fails auth on every request. Recovery requires SSH.

**Direction:** in `writeFileAtomic`, switch to `os.OpenFile` + `Write` + `f.Sync()` + `Close` + `Rename` + fsync of the parent dir (~10 lines). Belt-and-braces: treat an empty/whitespace token file as unprovisioned at startup — `tokenfile.Read` already errors on missing; make blepair's `os.Stat` provisioning probe (`blepair.go:64`) also check non-zero size. The same fsync treatment applies to `camera-selection.json` (`pipeline/select.go:351`), though that one self-heals via `restoreSelectionLocked`'s validation.

### 9. Unbounded in-memory event maps: finalized events and selector state never freed

**Where:**
- `/Users/adri/dev/teslcam-exp/internal/eventupload/eventupload.go:324` — `c.events[sourceID] = ev` grows forever; `finalize` (`:416-419`) clears `dirty` but never deletes the entry. The tick loop (`:189-199`) iterates the whole map every ≤ 1 s.
- `/Users/adri/dev/teslcam-exp/internal/pipeline/select.go:181` — `s.events[sourceID]` heldEvents (with `known`/`held`/`enqueued` maps and stopped-but-referenced timers) are never removed.

**Why it matters:** each entry is small, so this is not a fast leak — but the design target is a device that runs unattended for months (desk-powered dev units already do). Beyond memory, correctness edges accumulate: an in-memory `eventState` for a long-finalized event shadows the durable store's `savedEvent` re-open logic (`eventupload.go:292-323` only consults the spool when `c.events[sourceID] == nil`), so the carefully-written "already finalized, skip re-upload" path stops being exercised after the first process lifetime of any given event; state divergence between the two paths is a latent bug factory.

**Direction:** delete from `c.events` on confirmed finalize (the durable store remains the source of truth for late re-opens — that path already exists and is tested); in the selector, drop the event entry when it's fully resolved (scored/enqueued or timed out) plus a lazy sweep of entries older than some hours. Both are ~10-line changes.

### 10. Torn-directory snapshots emit spurious Removed → re-add churn

**Where:** `/Users/adri/dev/teslcam-exp/internal/watch/watch.go:168-184` — `walk` swallows `ReadDirectory` errors (`_ = err`) and keeps whatever entries were salvaged; `diff` (`:141-147`) then reports everything missing from the partial listing as `Removed`, deleting its `stable`/`settled` state.

**Why it matters:** a directory that parses partially on poll N (mid-flush) and fully on poll N+1 makes every file in it go `Removed` → `FileAdded` → re-stabilize. Consequences: (a) `FileStable` for in-flight files is delayed by another `StablePolls` cycle — during an active Sentry event, when latency matters most; (b) settled files re-fire `FileStable`, re-enter copyout (skipped, cheap) and the selector (deduped, fine) — so it's masked downstream, but only by accident of those layers' idempotency; (c) the swallowed error means zero observability when the exfat parser is struggling on real car-written filesystems — the one signal you'd want from the field.

**Direction:** smallest fix — when `ReadDirectory` errors for a directory, carry forward the previous snapshot's entries for that subtree instead of the partial result (the snapshot already has the paths; a prefix copy from `w.prev` suffices), and count/expose torn-read occurrences via an optional `onError`-style hook so it lands in the journal. Note `takeSnapshot`/`walk` are pure enough that this is easily unit-tested with the existing fixture machinery (`fixture_test.go`).

---

## P3 — nice to have

### 11. Fatal exits between gadget Setup and pipeline.Run leave the gadget bound

**Where:** `/Users/adri/dev/teslcam-exp/cmd/teslcam-agent/main.go:209` (Setup) vs `:240` (`log.Fatal` if `NewCommandScorer` rejects config) and `:156-168` (token fatal is before Setup — fine).
**Why:** `log.Fatal` skips the teardown at `:361`; the car sees a drive appear and vanish uncleanly. Self-heals next start via the leftover-teardown path (`:203-208`), so purely cosmetic — but trivially avoided.
**Direction:** falls out of finding 4: run *all* validation/construction before `g.Setup()`; only the pipeline itself should run after the gadget goes live.

### 12. `copyout` trusts image paths — `..` escapes DestDir

**Where:** `/Users/adri/dev/teslcam-exp/internal/copyout/copyout.go:185` — `filepath.Join(c.cfg.DestDir, filepath.FromSlash(strings.TrimPrefix(path, "/")))`. `underPrefix` (`:237`) only checks the prefix, so `/TeslaCam/SentryClips/../../x` passes and Join cleans it out of DestDir.
**Why:** paths come from exFAT names parsed out of an image written by the car — a trusted writer today, but the parser is explicitly designed to tolerate dirty/hostile data, and a USB-writable filesystem is an attack surface (any host the Pi is ever plugged into can write names). Defense costs three lines.
**Direction:** reject any path whose split contains `.` or `..` in `Enqueue` (or verify `strings.HasPrefix(dst, destDir+sep)` after Join). Same check is cheap insurance in `eventupload.parseImagePath` (it currently accepts `..` as an event directory name, which then flows into a local re-join via `Item.LocalPath` — though that path originates from copyout, so fixing copyout covers it).

### 13. Retry timers fire after shutdown and write to a closed spool DB

**Where:** `/Users/adri/dev/teslcam-exp/internal/eventupload/eventupload.go:166` (AfterFunc → `Enqueue`) vs `:146` (`defer c.store.close()` when `Run` returns).
**Why:** on SIGTERM with failed items pending, up-to-RetryDelay-later `Enqueue` calls hit the closed DB and log `persisting ... failed: sql: database is closed` — noise that looks like data loss during the exact window (shutdown) where people read logs carefully. Harmless (item is already durably pending), but scary.
**Direction:** track ctx in the client (or a closed flag set before `store.close()`) and make `Enqueue` a no-op after shutdown; or hold the AfterFunc timers and stop them on exit. Folds naturally into finding 2's backoff rework.

### 14. Spool eviction accounting records size 0 when the enqueue-time stat fails

**Where:** `/Users/adri/dev/teslcam-exp/internal/eventupload/durable.go:102-105` — `os.Stat` best-effort; failure ⇒ `size = 0` forever for that row.
**Why:** `totalBytes()` (`:420`) undercounts, so the cap enforcement lets real disk usage exceed `-spool-max-mb`. On a Pi whose spool shares the root filesystem, silently blowing the cap is a disk-full → SQLite-write-failure → cascading spiral.
**Direction:** stat again in `markUploaded` (the file provably exists at upload time) and update `size_bytes`; or fail `add` loudly when the stat fails (the file was just written by copyout — a stat failure is genuinely abnormal).

### 15. Hardcoded magic values

**Where / what:**
- `/Users/adri/dev/teslcam-exp/internal/pipeline/select.go:320` — 15-minute scoring deadline, buried in `scoreEvent`.
- `/Users/adri/dev/teslcam-exp/internal/blepair/session.go:354` — BLE response chunk size 100.
- `/Users/adri/dev/teslcam-exp/internal/blepair/conntest.go:31` — `/usage` as the authenticated-probe endpoint (couples onboarding to a server route chosen for unrelated reasons; a dedicated `/v1/ping` or reusing the heartbeat route would decouple).
- `/Users/adri/dev/teslcam-exp/cmd/teslcam-agent/main.go:349` — `RetryDelay: 5 * time.Second` not exposed as a flag while far less important knobs are.
- `/Users/adri/dev/teslcam-exp/internal/watch/watch.go:164` — walk depth 8.
- `/Users/adri/dev/teslcam-exp/internal/eventupload/eventupload.go:89` — 5-minute HTTP timeout (reasonable, but it bounds the largest uploadable blob on slow LTE; worth a named const with that comment).

**Why:** none is wrong today; each is a value someone will need to find under time pressure. Named consts at package top with a one-line rationale is enough — flags only for the retry delay.

### 16. `watchDisconnects` goroutine leaks and over-matches

**Where:** `/Users/adri/dev/teslcam-exp/internal/blepair/bluez.go:407-444` — signal channel never unsubscribed, goroutine runs for process life (acceptable — blepair.Run is process-scoped) — but the match is *every* `org.bluez` `Device1` PropertiesChanged. Any unrelated Bluetooth device connecting/disconnecting (the Pi's radio is claimed "dedicated", but nothing enforces that) resets the onboarding session via `onDisconnect` even while the real central is still connected, and flips `g.connected` tracking used by the adv watchdog.
**Direction:** low priority given the dedicated-radio assumption; if touched, track the connecting device path (`sig.Path`) and only reset when *that* device disconnects. Pass ctx and remove the match on exit for symmetry.

### 17. `readEventMetadata` swallows errors silently

**Where:** `/Users/adri/dev/teslcam-exp/internal/eventupload/eventupload.go:470-490` — unreadable/corrupt `event.json` ⇒ trigger/location stay nil, no log.
**Why:** the pipeline's clip selector logs the equivalent failure loudly (`select.go:127-129`); the uploader path losing trigger metadata is equally consequential for the server's verdict quality but invisible. Also `coordinate()` maps a malformed string to `0` — a fabricated (0,0) location rather than absent, contradicting the "never fabricate a default" rule the health package documents.
**Direction:** take a `Logf` line on failure (Logf is already required with spool config); make `coordinate` return `(float64, bool)` and omit `Location` when parsing fails.

### 18. `dashcam.FetchKeys` fails the whole batch on one bad clip; no tests

**Where:** `/Users/adri/dev/teslcam-exp/internal/dashcam/dashcam.go:167-171` — first per-item API `Error` aborts the loop; the header parsing (`ReadHeader`, `pageIV`, `DecryptClip`) is pure/deterministic but has zero tests (`internal/dashcam/` has no `_test.go`).
**Why:** this is break-glass tooling only (used solely by `cmd/teslcam-decrypt`; the shipped decision is "ask users to disable encryption"), so priority stays P3. But when it's needed it'll be needed urgently, on a batch where one clip's key is legitimately unavailable — all-or-nothing is the wrong shape for that moment.
**Direction:** return per-item results (`map[string][]byte` + `map[string]error`), and add a fixture test for `ReadHeader`/`pageIV`/`DecryptClip` with a synthesized container (the format constants are all local; a 3-page fixture is ~30 lines to build).

### 19. Stale LED-era comments in blepair

**Where:** `/Users/adri/dev/teslcam-exp/internal/blepair/protocol.go:2-5` ("prove physical presence via a code blinked on the Pi's activity LED") and `/Users/adri/dev/teslcam-exp/internal/blepair/blepair.go:164` ("stop any LED activity, restore trigger").
**Why:** LED pairing codes were explicitly rejected and removed; the package doc now describes a security mechanism that does not exist, which is exactly the kind of doc a future security review will trip over. `session.go:18-19` also has a duplicated phrase ("Just Works Just Works SMP encryption").
**Direction:** doc-only fix.

---

## Cross-cutting observations

**Interface segregation is mostly right-sized already.** `wifi.Manager` (4 methods, one consumer), `cameraselect.Scorer` (1 method), `configSink`/`connTester` (1 method each) are exemplary for a single-maintainer codebase. The two places concrete types leak where a seam would pay for itself are pipeline→uploader/copier (finding 5) and eventupload→its own HTTP layer (finding 3). Nothing here needs a DI framework — Go's implicit interfaces defined at the consumer are enough.

**Platform seams are in good shape.** `//go:build linux` on the agent main and `health/collect_linux.go`+`collect_other.go` keep macOS builds green; `gadget` and `exfat` are pure file/byte code testable anywhere; the Lima VM covers the rest. The remaining hardware-only blind spots are the exec halves of blepair (finding 7) and health (finding 6) — both fixable with the runner pattern the repo already owns.

**Power-cut resilience is genuinely strong** in the data path (spool WAL, event-as-durable-unit, copyout's verify+fsync+rename, watcher re-baseline rebuilding selector state) — the audit found the gaps at the *edges*: config persistence (finding 8) and eviction accounting (finding 14), not the core.

**Goroutine lifecycle:** pipeline.Run's workers (uploader, compressor, copier) are fire-and-forget on the shared signal ctx and the process exits without draining them. That is a defensible *choice* given the durable spool (an interrupted PUT is retried after restart; blobs are content-addressed so double-uploads are idempotent) — but it's currently an implicit choice. Worth a comment at `pipeline.go:143`; an errgroup with bounded drain is optional polish, not a correctness need.
