# Cross-cutting audit — conventions, hygiene, scripts, tooling, tests, go.mod

Scope: repo-wide concerns only. `internal/server`, the agent pipeline packages, and
`sentyx-app` internals are covered by other reports. Audited at commit `6af2671` (main),
2026-07-16, with a dirty working tree (in-flight FCM push work: `internal/fcm/`,
`internal/server/push.go`, related diffs).

## Summary table

| # | Finding | Priority | Area |
|---|---------|----------|------|
| 1 | No CI at all — tests never run automatically; linux-only agent code isn't even compiled by `go build ./...` on the Mac | **P1** | Tests/CI |
| 2 | Finished slog logging migration is stranded on an abandoned worktree/branch, drifting further from main daily | **P2** | Logging |
| 3 | TODO.md is materially stale (claims prod isn't deployed; describes a removed analyzer-shipping flow) | **P2** | Hygiene |
| 4 | Two near-identical systemd units, both with a wrong "Pi Zero 2 W" header; the non-pi4 one is likely dead | **P2** | Scripts |
| 5 | Stale "Pi Zero 2 W" references across CLAUDE.md, agent doc comment, and unit headers (Pi 4 is the decided target) | **P2** | Docs |
| 6 | Error wrapping inconsistent: 117/277 `fmt.Errorf` use `%w`; several wrappable errors use `%v`, breaking `errors.Is` | **P3** | Conventions |
| 7 | `docs/index.html` doesn't link `api-reference.html`, `hardware-plan.md`, or `tesla-oauth.md` | **P3** | Docs |
| 8 | `internal/testcli` re-implements ~150 lines of the v1 ingest client already in `internal/eventupload` | **P3** | Tooling |
| 9 | `internal/teslaauth` + `internal/dashcam` are break-glass-only (used solely by `cmd/teslcam-decrypt`), zero tests, not marked as such | **P3** | Tooling |
| 10 | Branch/worktree clutter: merged `feat/ble-onboarding` branch, prunable detached worktree, `wifi-app-fixes` pending merge | **P3** | Hygiene |
| 11 | Untracked 17 MB binary at repo root + `build/` binaries — gitignored (not committed), but persistent local clutter | **P3** | Hygiene |
| 12 | `mobile/` React design mockup superseded by `sentyx-app` — archive candidate | **P3** | Hygiene |
| 13 | Server config split between flags and env (`GEMINI_API_KEY`, `TELEGRAM_DEBUG` via env, everything else flags) | **P3** | Conventions |
| 14 | A few `context.Background()` uses in library code worth a one-time audit | **P3** | Conventions |
| 15 | `vm-agent-test.sh` / `vm-debug.sh` use `#!/bin/bash` instead of `#!/usr/bin/env bash` | **P3** | Scripts |

**Explicit non-findings (things that are in good shape):** flag/config validation
discipline is exemplary and follows the "no implicit defaults" rule everywhere checked;
`go.mod` is clean (3 direct deps, `go mod tidy -diff` empty, `go build`/`go vet` clean);
`AGENTS.md` is a symlink to `CLAUDE.md` (no duplication); `node_modules` and compiled
binaries are all gitignored, none tracked; every shell script sets `set -euo pipefail`;
`docs/api-reference.html` was updated with the user-accounts commit (2026-07-16) and
covers the new `/v1/me/*`, heartbeat, and finalize endpoints.

---

## P1

### 1. No CI — nothing is built or tested automatically

**Evidence:** no `.github/` directory exists anywhere in the repo. There is no CI config
of any kind (no GitHub Actions, no other runner).

**Why it matters (three concrete gaps):**

1. The test suite (25 test files across 19 packages, including the substantial
   `internal/server` and `internal/exfat` suites) only runs when someone remembers to run
   it. With multiple concurrent Claude sessions touching main (see the `wifi-app-fixes`
   orphaning incident), an automated gate is the cheapest protection available.
2. **`cmd/teslcam-agent` is `//go:build linux`** (`cmd/teslcam-agent/main.go:1`), so
   `go build ./...` on the macOS dev machine silently skips the most important binary in
   the repo. A refactor can break the agent's `main.go` and nothing notices until the
   next manual `GOOS=linux` cross-compile or Pi deploy.
3. The integration tests are darwin-only (`integration/live_darwin_test.go:1`,
   `integration/powercut_darwin_test.go:1` — both `//go:build darwin`) and the Linux
   gadget e2e is a manual shell script (`scripts/vm-agent-test.sh`), so the exFAT
   live-reader's flagship end-to-end coverage runs only ad hoc on Adrien's Mac.

**Fix direction:** a minimal GitHub Actions workflow with three jobs:
`go vet ./... && go test ./...` on ubuntu-latest (runs everything except the darwin
integration tests), `GOOS=linux GOARCH=arm64 go build ./...` as a pure compile check for
the agent, and a macos-latest job running `go test ./integration/...` (hdiutil is
available on GitHub's macOS runners). The Lima/dummy_hcd loop can stay manual. Total
effort is one ~40-line YAML file; go.sum caching makes it fast.

---

## P2

### 2. The finished slog migration is rotting in an abandoned worktree

**Evidence:** `.claude/worktrees/logging-abstraction` is a live git worktree on branch
`logging-abstraction` (also pushed to `origin/logging-abstraction`), one commit ahead of
its fork point: `ce4001e "Add structured slog logging across server and agent"`. The
commit is complete work, not a sketch — 22 files, +593/−153, introducing
`internal/logging/logging.go` (97 lines, with its own tests): shared attribute keys,
text/JSON handlers, `-log-format`/`-log-level` flags on every binary, and replacement of
the threaded `logf` closures with `*slog.Logger`. It also adds logging to
previously-silent server paths (manifest finalize, Telegram send success, upsert
new-vs-existing).

Meanwhile main has moved substantially: `git diff main` from the worktree spans 244 files
(main gained `sentyx-app/`, user accounts, camera scoring changes). The branch predates
the camera-scorer rewrite and the Supabase auth work, so the conflict surface grows with
every commit to main.

**Current state of logging on main (what the branch would fix):**

- No levels, no structure anywhere. 75 raw `log.Printf`/`log.Fatal` call sites, all in
  `cmd/` (agent 37, server 17, sim 10, test 6, watch 5).
- `internal/` packages never touch the global logger (good discipline), but the
  convention is an anonymous closure type repeated in at least 14 signatures with no
  named type — e.g. `internal/pipeline/pipeline.go:71` (`Logf func(format string, v
  ...any)`), `internal/server/server.go:103`, `internal/blepair/blepair.go:32`,
  `internal/health/locallog.go:15`, `internal/server/analyze.go:22,158`,
  `internal/server/push.go:85` (the brand-new FCM code is still adopting the old
  pattern, which is the drift cost in action).
- No way to filter noise on the Pi (where journal volume matters for SD wear) or to grep
  structured fields on the VPS.

**Why it matters:** this is the single largest piece of completed-but-unlanded work in
the repo, and it conflicts with every new `logf` threaded through new code (FCM push is
doing exactly that right now). Each week of delay makes the rebase more expensive than a
re-do.

**Fix direction:** decide now — either (a) rebase/port `ce4001e` onto main (the
`internal/logging` package and the cmd-level flag wiring port cleanly; the
pipeline/server hunks need manual re-application), or (b) explicitly discard it: delete
the worktree (`git worktree remove`), delete the local + origin branch, and file the
design (which is sound) as a TODO item so the intent isn't lost. Leaving a live worktree
under `.claude/worktrees/` in a repo where agents run concurrently invites confusion.

### 3. TODO.md is stale enough to mislead

**Evidence** (`TODO.md`, last touched 2026-07-13 per git log):

- The Phase-3 deploy item says the VPS deployment is "**Not yet run against the real
  VPS**" and the bearer-token item says "**Not yet deployed to the VPS**" — but per
  CLAUDE.md and production reality, the server has been live on the droplet with auth
  (`/etc/teslcam/ingest.token`) for a while.
- The same section describes deploy as shipping the TS analyzer ("binary + analyzer
  rsync, **npm ci**, restart") and gating analysis on "GEMINI_API_KEY + **ANALYZE_CMD**"
  — both removed; `scripts/deploy-server.sh:18-22` now explicitly says "There is no
  external analyzer to ship" and the analyzer is native Go (`internal/gemini`).
- "Real **Pi Zero 2 W** bring-up" is still an open Phase-2 item, but the Pi Zero was
  dropped; Pi 4 is the decided prototype *and* field target, and bring-up against the
  real car already happened (CLAUDE.md "Prototype hardware" section).
- "[ ] Push notification on high threat_level" is listed as not-started while
  `internal/fcm/` + `internal/server/push.go` are being written in the current working
  tree.
- "[ ] User accounts (in progress — see plan)" — shipped in `6af2671`.

**Why it matters:** the file is the top-level plan of record and is loaded by agents as
context; several of its claims now actively contradict CLAUDE.md and the code. Wrong
"not yet deployed" claims are the dangerous kind — an agent could re-run one-time setup
against prod.

**Fix direction:** a single editing pass: check off shipped items (user accounts, VPS
deploy, auth, Pi bring-up), rewrite the deploy bullet to match the native-analyzer
reality, replace "Pi Zero 2 W" with Pi 4, and fold the current FCM work into section 4.
Consider trimming the fully-checked Phase-1 detail into a short "done" line to keep the
file scannable.

### 4. Duplicate systemd units, both with the wrong header; base unit likely dead

**Evidence:** `scripts/teslcam-agent.service` and `scripts/teslcam-agent-pi4.service`
share ~40 identical lines (unit metadata, env file, modprobe, image/udc/watch/spool/BLE/
heartbeat flags). The pi4 variant adds exactly six things: `-select-clips`,
`-select-metadata-timeout 5m`, `-camera-scorer /usr/local/bin/teslcam-camera-scorer`,
`-video-encoder h264_v4l2m2m`, `-video-fallback-encoder libx264`, and
`-health-log-interval 1m`. **Both** files open with the same comment: "systemd unit for
the in-car gadget agent (**Pi Zero 2 W**)" (`teslcam-agent.service:1`,
`teslcam-agent-pi4.service:1`) — wrong for the pi4 file by its own name, and wrong
overall since the Pi Zero was dropped entirely.

**Why it matters:** every flag change must be made twice (the video-encoder default
already diverged: base unit inherits `libx264` implicitly, pi4 sets `h264_v4l2m2m`
explicitly); and since Pi 4 is both the prototype and the field target, the base unit no
longer describes any real device — it's a trap for the next provisioning script.

**Fix direction:** keep one unit. Either delete `teslcam-agent.service` and rename the
pi4 one, or move the six pi4-specific flags into `/etc/teslcam/agent.env` (the
`EnvironmentFile` is already wired) so hardware variants are config, not unit files.
Fix the header comment to say Pi 4 either way.

### 5. Stale "Pi Zero 2 W" references beyond the units

**Evidence:**

- `CLAUDE.md:7` — "the in-car component requires Linux gadget-mode hardware (e.g.
  Raspberry Pi Zero 2 W)"; `CLAUDE.md:24` — "final validation needs a real Pi Zero 2 W
  plugged into the car" (validation already happened, on a Pi 4); the architecture
  section's "Gadget agent (Go, runs on Pi)" is fine, but the "Phased plan" item 2 still
  says "Pi gadget agent (configfs mass storage, dwc2 overlay, systemd unit)" — done.
- `cmd/teslcam-agent/main.go:3` — doc comment: "teslcam-agent is the in-car daemon (Pi
  Zero 2 W, or the dev VM via dummy_hcd)".
- `docs/hardware-plan.md` (2026-07-14) is the corrected source of truth (Pi 4 decision)
  — the older references just never caught up.

**Why it matters:** CLAUDE.md is injected into every agent session; contradictory
hardware claims cost every future session a reconciliation step (the memory file
`pi4-is-final-target.md` exists precisely because of this confusion).

**Fix direction:** one sweep replacing Pi Zero 2 W with Pi 4 Model B in CLAUDE.md and the
agent doc comment, and pointing at `docs/hardware-plan.md` for the rationale.

---

## P3

### 6. Error wrapping: `%w` vs `%v` inconsistency

**Evidence:** 277 non-test `fmt.Errorf` calls; 117 use `%w`. Some `%v` uses genuinely
flatten intentionally, but several discard wrappable errors where callers could plausibly
want `errors.Is`:

- `internal/wifi/nmcli.go:184` — `fmt.Errorf("forget failed: %v", err)`
- `internal/wifi/nmcli.go:237` — `fmt.Errorf("connect failed: %v", err)`
- `internal/wifi/nmcli.go:247` — `fmt.Errorf("%s: %v", action, err)`
- `internal/exfat/locate.go:58` — `fmt.Errorf("exfat: no exFAT partition in MBR: %v", probeErrs)`
- `internal/blepair/bluez.go:357` — `fmt.Errorf("btmgmt add-adv: %v: %s", err, out)`

(Counter-example done right: `internal/gadget/gadget.go:84` wraps the primary error with
`%w` and flattens the secondary cleanup error with `%v`.)

**Why it matters:** mostly future-proofing — e.g. distinguishing `exec.ErrNotFound`
(nmcli missing) from an nmcli failure requires the chain. Not urgent because current
callers mostly log-and-continue.

**Fix direction:** adopt "always `%w` unless flattening is deliberate" and fix the sites
above opportunistically; a `go vet`-adjacent linter (`errorlint`) in the future CI job
(finding 1) enforces it for free.

### 7. `docs/index.html` doesn't link half the docs

**Evidence:** `docs/index.html` links only `ingestion-api.html` and
`teslacam-filesystem.html`. Missing: `api-reference.html` (the full REST reference,
actively maintained — last touched with the user-accounts commit), `hardware-plan.md`
(the Pi 4 decision record), and `tesla-oauth.md` (the encryption decision record).

**Fix direction:** add three links. If the md files are meant to stay markdown, link them
as-is (GitHub renders them) rather than converting.

### 8. `internal/testcli` duplicates the v1 ingest client

**Evidence:** `internal/testcli/client.go` (307 lines) independently implements
`upsertEvent`, `putBlob`, `finalize`, `doJSON`, `do`, and `hashFile` — the same
protocol steps as `internal/eventupload/eventupload.go:365-470,557` (compare
`testcli/client.go:133-227,282`). Both build on `internal/protocol` for the wire types,
so the duplication is the HTTP/hashing plumbing (~150 lines), not the types.

**Why it matters:** protocol changes (e.g. a new manifest field or auth header) must be
made twice; the test client can silently drift from what the real agent sends, weakening
`cmd/teslcam-test` as an end-to-end probe. Counter-argument: an independent
implementation catches server bugs a shared client would mask. That's legitimate — but
then the independence should be a stated design decision in the package doc.

**Fix direction:** either extract a small shared `protocol.Client` (upsert/putBlob/
finalize/doJSON) used by both, or add one line to `testcli`'s package comment declaring
the duplication intentional.

### 9. Break-glass packages not marked as such

**Evidence:** `internal/teslaauth` (Tesla SSO OAuth+PKCE) and `internal/dashcam`
(dashcam.tesla.com client) are imported only by `cmd/teslcam-decrypt`. Per the
2026-07-13 decision (`docs/tesla-oauth.md:3-14`), decryption is explicitly **not** a
product dependency — users are told to disable clip encryption; this code is
break-glass only. Both packages have zero tests and depend on undocumented Tesla
endpoints that "can break on any update" (the doc's own words).

**Why it matters:** low — the code is inert. The risk is future effort miscalibration:
someone (or some agent) treating it as production surface, adding tests/CI/refactors to
code that's deliberately allowed to bit-rot.

**Fix direction:** one-line package-comment addition to both packages ("break-glass
tooling for cmd/teslcam-decrypt; not a product dependency — see docs/tesla-oauth.md")
and exclude them from any future coverage targets. Moving them under
`cmd/teslcam-decrypt/internal/` would encode the scoping structurally.

### 10. Branch and worktree clutter

**Evidence** (`git branch -a`, `git worktree list`):

- `feat/ble-onboarding` — merged into main at `2734d95`; deletable.
- `wifi-app-fixes` — 3 app commits orphaned by a concurrent-session rewrite (per memory);
  pending merge, keep until merged.
- `logging-abstraction` — see finding 2 (local + origin).
- A **prunable** detached worktree at
  `/private/tmp/claude-501/.../scratchpad/deploy-wt` (`beab1bc`, from a past agent
  session) — `git worktree prune` removes the record.

**Fix direction:** `git branch -d feat/ble-onboarding`, `git worktree prune`, and
resolve findings 2 and the wifi-app-fixes merge, after which the branch list is clean.

### 11. Untracked binaries at repo root and `build/`

**Evidence:** `./teslcam-agent` (17 MB, Jul 15) and `build/teslcam-agent-arm64` +
`build/teslcam-agent-pi4` (Jul 14). **None are committed** — `.gitignore` covers
`/teslcam-agent`, `/build/`, and `node_modules/` explicitly, and `git ls-files` confirms
nothing binary is tracked (largest tracked file is a 296 KB PNG in sentyx-app). So this
is local clutter, not repo bloat.

**Why it matters:** only that a stale root binary invites running an old agent by
accident (`./teslcam-agent` vs a fresh build), and the stale `build/` artifacts predate
the current camera-scorer flags.

**Fix direction:** delete the local files; consider standardizing all build output into
`build/` (already ignored) so the root-level stray stops reappearing — the `.gitignore`
comment ("stray compiled agent binary at repo root") suggests it recurs.

### 12. `mobile/` design mockup superseded by `sentyx-app`

**Evidence:** `mobile/` is a Vite/React "design lab" for three visual directions
(`mobile/README.md:1-8`) with its own `package-lock.json` (32 KB tracked), local
`node_modules` and `dist/` (untracked). The real app is the Compose Multiplatform
`sentyx-app/`, which has its own committed design reference
(`sentyx-app/design/Sentyx.dc.html`, 140 KB).

**Fix direction:** if the three-direction exploration is settled, archive or delete
`mobile/` (git history preserves it). At minimum, add a line to its README saying the
production app is `sentyx-app/` so nobody iterates on the wrong codebase.

### 13. Server config: env vs flags split

**Evidence:** `cmd/teslcam-server/main.go` takes everything as flags except
`GEMINI_API_KEY` (main.go:48) and `TELEGRAM_DEBUG` (main.go:90), which come from env.
The API-key-via-env choice is sound (keeps secrets out of argv, consistent with
`-token-file`/`-telegram-token-file` file-based secrets); `TELEGRAM_DEBUG` as env is the
odd one out — it's a non-secret toggle that would normally be a flag, and it's the only
setting invisible to `teslcam-server -h`.

**Why it matters:** cosmetic; the launcher script on the VPS assembles flags from
`server.env` anyway, so operators see one config file. Validation of both env vars is
explicit and fail-fast (main.go:49-51, 91-98) — the no-implicit-defaults rule is
respected.

**Fix direction:** if touched anyway, promote `TELEGRAM_DEBUG` to a `-telegram-debug`
flag and let the launcher map the env var, keeping env strictly for secrets.

### 14. `context.Background()` in library code — brief audit list

**Evidence** (13 non-test uses; most are legitimate detached lifecycles like shutdown
timeouts `internal/server/server.go:117` and fire-and-forget notify
`internal/server/notify.go:43`). Three worth a look when nearby code changes:

- `internal/pipeline/select.go:71` — `newClipSelector` hardcodes `context.Background()`
  into the scored-selector path instead of accepting the caller's ctx (its sibling
  `newScoredClipSelector` at line 74 takes one).
- `internal/blepair/session.go:305` — a Background ctx inside the session, decoupled from
  the agent's signal-cancelled root ctx.
- `internal/gemini/gemini.go:440` — fixed 30 s Background timeout (probably a cleanup
  call; fine if so).

**Fix direction:** thread the caller's ctx where a parent exists; keep Background only
for deliberately detached work, with a comment saying so (the codebase already does this
well in most places).

### 15. Shebang inconsistency in scripts

**Evidence:** `scripts/vm-agent-test.sh:1` and `scripts/vm-debug.sh:1` use
`#!/bin/bash`; the other three scripts use `#!/usr/bin/env bash`. All five set
`set -euo pipefail`, use bounded waits (vm-agent-test.sh explicitly: "Every wait is
bounded; the whole test fails rather than hangs"), and `deploy-server.sh` is notably
well-structured (`say`/`die` helpers, `ssh -o ConnectTimeout=10` everywhere,
base64-encoded sudo heredocs to sidestep quoting, `remote_arch` validation at
deploy-server.sh:39-77). This is a consistency nit only.

**Fix direction:** switch the two `#!/bin/bash` lines to `#!/usr/bin/env bash`. Adding
`shellcheck` to the future CI job costs nothing.

---

## Test coverage map (for reference)

Packages with **zero** test files: `internal/teslaauth`, `internal/dashcam`,
`internal/protocol` (pure wire types), `internal/sim` (exercised heavily by the darwin
integration tests instead), and all `cmd/*` mains (thin, validated by hand/e2e).
Everything else has at least one `_test.go`; the heavyweights are `internal/server`
(9 test files), `internal/exfat` (6), then `pipeline`/`health`/`cameraselect`/`blepair`
(2 each). The gap is not coverage breadth — it's that nothing runs any of it
automatically (finding 1).

## go.mod health (for reference)

`go 1.25.1`; three direct dependencies (`godbus/dbus` — BLE, `golang.org/x/oauth2` —
FCM service-account auth via `google.CredentialsFromJSON`, `modernc.org/sqlite` — pure-Go
store), nine indirect. `go mod tidy -diff` produces no changes; `go build ./...` and
`go vet ./...` are clean on darwin. No action needed.
