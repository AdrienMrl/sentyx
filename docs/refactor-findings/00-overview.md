# Refactoring & robustness audit — overview

Audit date: 2026-07-16. Four detailed reports, one per area, each with a
prioritized findings table (P1 hot / P2 worthwhile / P3 nice-to-have),
file:line anchors, and sketched refactor directions. No code was changed.

| Report | Scope | Findings |
|---|---|---|
| [01-server.md](01-server.md) | `internal/server`, `cmd/teslcam-server`, fcm/telegram/gemini/protocol | 24 (3×P1) |
| [02-agent.md](02-agent.md) | `cmd/teslcam-agent`, gadget/exfat/pipeline/eventupload/blepair/wifi/health | 19 (2×P1) |
| [03-app.md](03-app.md) | `sentyx-app` (Kotlin Multiplatform) | ~20 (5×P1) |
| [04-cross-cutting.md](04-cross-cutting.md) | logging, scripts, systemd units, docs, tests/CI, go.mod, repo hygiene | (1×P1) |

Note: the working tree had concurrent uncommitted changes (push-notification
work) during the audit — re-verify line anchors before acting on a finding.

## Top 10 across all reports

1. **No CI** (04). Nothing runs tests, and `go build ./...` on the Mac never
   compiles the linux-only agent binary. One small GitHub Actions workflow
   (vet + test on ubuntu, `GOOS=linux` compile check, darwin integration job)
   is the highest-leverage single change here — it also guards every refactor
   the other reports propose.
2. **App: one tap crashes the real flavor** (03). The unconditional
   "Prototype states" settings row routes into `error()` when `demoState` is
   null. Trivial fix, real crash.
3. **Agent: hung ffmpeg silently stalls all copy-out** (02). Compression jobs
   have no per-job timeout; the full queue backpressures the copier callback.
   In-car this means events quietly stop extracting.
4. **Agent: uploader retry churn** (02). Flat 5 s retry with a full re-hash of
   the clip per attempt — multi-day offline windows (the normal parked case)
   burn CPU/battery/LTE. Needs backoff + hash-once.
5. **Server: `store.event()` is O(all events)** (01) and runs on every upsert,
   GET, thumb, and analysis pass. A `WHERE e.id = ?` fixes it.
6. **Server: per-handler opt-in auth** (01). ~10 repetitions of
   `if c.cfg.Token != ""`; one forgotten guard = silent cross-user exposure.
   Move to middleware that always injects an auth context.
7. **App: UI reports success for documented no-ops** (03). Factory reset,
   restart, retry-analysis, delete, bulk download all toast success while the
   real repositories do nothing. Either wire them or disable the affordances.
8. **Onboarding correctness pair** (02+03). Pi side: `writeFileAtomic` never
   fsyncs, so a power cut right after pairing can leave an empty
   `server.token` — which *is* the provisioning marker (device stuck
   half-provisioned). App side: the Wi-Fi password field prefills a fake demo
   key and join failures are swallowed, so pairing can "succeed" with broken
   Wi-Fi. Same flow, both edges.
9. **Stranded slog migration** (04). A complete logging refactor (22 files,
   tested `internal/logging`, `-log-level`/`-log-format` everywhere) sits in
   `.claude/worktrees/logging-abstraction`, 244 commits behind main, while new
   code keeps adopting the `logf` closure pattern it replaces. Decide:
   rebase it or delete it — every month it rots further.
10. **App: committed secrets/config + lifecycle split-brain** (03). Server URL
    and Supabase URL/anon key are hardcoded in `BuildFlavor.kt`; the DI
    container and Navigator live in `remember` scope, so a rotation rebuilds
    the container while ViewModels keep the dead one.

## Cross-cutting themes

- **The SOLID story is mostly "extract seams, not layers."** All four reports
  converge on the same shape: the domain design is sound (consumer-side
  interfaces on the server, gadget's fake-configfs, wifi's injectable command
  runner, the app's AppContainer DI), but a handful of god units hide the
  logic from tests — `eventupload.Client` (transport + queue + settle state
  machine + spool policy in one), `pipeline.Run` (220 lines), agent `main()`
  (373 lines / ~45 flags), and the server's analyze loop state round-trip.
  None of the reports recommend repository-style interface blankets; both Go
  reports explicitly say the concrete-store/real-SQLite testing is right.
- **Exec-without-timeout is a repeated pattern** — ffmpeg (pipeline), vcgencmd
  / iwgetid (health), btmgmt (blepair). `wifi/nmcli.go` already has the right
  pattern (injectable runner + per-op timeout); copy it.
- **Slow leaks in long-lived maps** — agent (`eventupload.events`,
  `clipSelector.events`) and app (per-event ViewModels never cleared). The
  agent runs for weeks; these matter.
- **Docs drift faster than code** — TODO.md describes a removed deployment
  flow; CLAUDE.md and the agent doc comment still say Pi Zero 2 W; both
  systemd unit headers are wrong; blepair still documents the dropped LED
  code. Small, but each one misleads the next session that reads it.

## Suggested attack order

1. CI workflow (04 §1) — protects everything below.
2. The four small correctness fixes: settings-row crash (03), `%w(nil)` in
   `analyze.go` (01), `store.event` WHERE clause (01), fsync in
   `configstore.writeFileAtomic` (02).
3. ffmpeg per-job timeout + uploader backoff/hash-once (02) — field
   robustness for the parked-car case.
4. Auth middleware on the server (01).
5. slog worktree decision, then unify systemd units + refresh TODO/CLAUDE
   hardware references (04).
6. The bigger seam extractions (eventupload split, pipeline.Run split, app
   lifecycle/DI ownership) as standalone PRs when touching those areas.

## Related: fresh-Pi install script

`scripts/install-agent.sh` (new, written alongside this audit) automates
fresh-Raspberry-Pi bring-up: `setup` → reboot → `image <size-gb>` → `scorer`
→ `deploy` → `check`. It installs runtime deps (ffmpeg, exfatprogs, bluez,
network-manager), configures the dwc2 overlay + modules, creates the
MBR-partitioned exFAT backing image, builds the camera scorer on-Pi, and
installs the pi4 systemd unit. Provisioning remains BLE onboarding's job.
Note finding 04 §systemd: once the two units are consolidated, point the
script's unit install at the merged file.
