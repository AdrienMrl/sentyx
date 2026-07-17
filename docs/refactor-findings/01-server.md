# Server-side refactor & robustness findings

Scope: `internal/server/`, `cmd/teslcam-server/main.go`, `internal/fcm/`, `internal/telegram/`,
`internal/gemini/`, `internal/protocol/`, `internal/tokenfile/`. Audited 2026-07-16 against the
working tree (post user-accounts + FCM push work, uncommitted).

Lens: SOLID where it pays off for a single-maintainer product — testability and robustness first,
no enterprise ceremony. Severity: **P1** hot, **P2** worthwhile, **P3** nice-to-have.

## Summary table

| # | P | Finding | Where |
|---|---|---------|-------|
| 1 | P1 | `store.event(id)` loads *all* events and linear-scans — hot path everywhere | `store.go:334` |
| 2 | P1 | Auth-disabled guard (`c.cfg.Token != ""`) hand-repeated in ~10 handlers; one omission = data leak | `http.go`, `push.go`, `highlevel_http.go` |
| 3 | P1 | Analysis outcome round-trips through the DB; job state and event state are two sources of truth | `server.go:143`, `analyze.go`, `store.go:215` |
| 4 | P2 | `fmt.Errorf("loading event: %w", nil)` when the event is missing → `%!w(<nil>)` in stored error | `analyze.go:34-37` |
| 5 | P2 | Nil-pointer risk: `handlePutEventV1` uses `ev` without a nil check after `store.event` | `highlevel_http.go:57-64` |
| 6 | P2 | No `context.Context` anywhere in the store layer (non-ctx `Exec`/`Query` throughout) | `store.go`, `highlevel_store.go` |
| 7 | P2 | Inconsistent request-body handling: size limit + `DisallowUnknownFields` only on v1 ingest; account/device endpoints use bare decoders | `http.go:190`, `push.go:193,254` vs `highlevel_http.go:236` |
| 8 | P2 | Two response-writing styles; raw `err.Error()` (SQL, paths) leaks to clients on every 500 | `http.go` vs `highlevel_http.go:246` |
| 9 | P2 | `analyzeEvent` is a 100-line do-everything orchestration (select → analyze → persist → thumb → frame → notify → push) | `analyze.go:22-122` |
| 10 | P2 | `store.go` vs `highlevel_store.go` split is historical, not by responsibility; error conventions diverge | both files |
| 11 | P2 | `http.Server` has no timeouts; `Shutdown` error ignored | `server.go:104-123` |
| 12 | P2 | Check-then-act races: manifest pre-checks outside the tx; device-owner claim TOCTOU | `highlevel_store.go:76-99,157-186`, `http.go:205-227` |
| 13 | P2 | Notifier + push delivery run inline in the single analysis worker (up to ~25 s + 15 s × N tokens per event) | `analyze.go:120-121`, `push.go:125-127` |
| 14 | P3 | Dead code: `pendingAnalyses`, `prettyCamera`; `enqueueAnalysis` is test-only | `store.go:192`, `analyze.go:177`, `highlevel_store.go:255` |
| 15 | P3 | Magic numbers scattered (90 s online window, 250 ms poll, 10 min analysis timeout, 3×5 s retry, threat-level enum duplicated) | see detail |
| 16 | P3 | Migrations by matching the `"duplicate column"` error string | `store.go:113-133` |
| 17 | P3 | `upsertUser` write on **every** authenticated user request | `http.go:148` |
| 18 | P3 | JWT verifier: JWKS fetch has no request ctx, no single-flight, no clock-skew leeway | `jwt.go:133-183` |
| 19 | P3 | Blob upload has no size cap or quota — a leaked device token can fill the disk | `highlevel_http.go:100-154` |
| 20 | P3 | Two near-duplicate ffmpeg frame extractors | `analyze.go:127-152`, `thumbs.go:47-64` |
| 21 | P3 | Heartbeat for an unknown device returns 204 (UPDATE matched 0 rows) | `http.go:264`, `store.go:494` |
| 22 | P3 | Missing indexes for owner/user lookups (fine today, cheap to add) | `store.go` schema |
| 23 | P3 | Ownership rule duplicated in SQL (`eventsOwnedBy`) and Go (`eventReadable`) | `store.go:286`, `http.go:102` |
| 24 | P3 | Config surface split between flags and env (`TELEGRAM_DEBUG`, `GEMINI_API_KEY` vs everything else as flags) | `main.go:90` |

---

## P1 — hot

### 1. `store.event(id)` is O(all events) and sits on every hot path

`store.go:334-345`:

```go
func (s *store) event(id string) (*EventSummary, error) {
	all, err := s.events()          // full table scan + files COUNT subquery per row
	...
	for i := range all { if all[i].ID == id { ... } }
}
```

Callers: `handleGetEventV1` (`highlevel_http.go:69`), `handlePutEventV1` (`highlevel_http.go:57`,
i.e. **every event upsert from the Pi**), `handleEvent` (`http.go:362`), `handleEventThumb`
(`http.go:400`), `analyzeEvent` (`analyze.go:34`), and the post-analysis reconciliation in
`analyzeLoop` (`server.go:144`). Each call materializes every event row **and** runs the
correlated `COUNT(*) FROM files` subquery for each. Today with dozens of events it's invisible;
after a season of sentry events it makes every upsert and thumb fetch scale with history size.

**Fix**: `eventsSelect` already exists precisely for this — add
`s.queryEvents(eventsSelect + " WHERE e.id = ?", id)` and return the single row. ~5 lines,
removes the accidental O(N).

### 2. Auth enforcement is opt-in per handler instead of default-deny in middleware

Every handler individually remembers to guard with `if c.cfg.Token != ""` before checking
authorization: `http.go:182` (register), `http.go:244` (heartbeat), `http.go:276` (device status),
`http.go:325` (events list), `http.go:371,399` (event/thumb), `highlevel_http.go:26`
(`denyUserIngest`), `push.go:170` (`meUserID`). The pattern means:

- A new endpoint that forgets the guard silently becomes **public-with-auth-configured** — the
  worst failure mode (`requireToken` already 401s unauthenticated callers, but the *authorization*
  layer is what's duplicated; e.g. a forgotten `eventReadable` check exposes cross-user data).
- The "no token = trust everyone" rule is encoded 10 times and must stay consistent by hand.

**Fix**: make the middleware the single place that knows about dev mode. When `cfg.Token == ""`,
`requireToken` injects `authInfo{Operator: true}` into the context. Then every handler drops its
`c.cfg.Token != ""` prefix and *always* consults `authFrom(ctx)`/`eventReadable` — authorization
code becomes uniform and exercised in both modes (also fixes the current test blind spot where
dev-mode and authed-mode take different code paths). `deviceReadable`/`eventReadable` and the
`meUserID`/`denyUserIngest` helpers then read as pure policy with no config awareness.

### 3. Analysis outcome is communicated through the database, with two competing state machines

`analyzeEvent` writes its outcome into `events.analysis_state` (via `setAnalysis`, which *also*
maps to a third field `events.state` via a SQL `CASE` — `store.go:224-229`). Then `analyzeLoop`
(`server.go:143-160`) **re-reads the event** and reverse-maps `analysis_state` back into a job
state to call `finishAnalysisJob`:

```go
c.analyzeEvent(ctx, job.EventID, logf)
ev, loadErr := c.store.event(job.EventID)   // re-read what we just wrote
switch ev.AnalysisState {
case "done", "skipped": state = "done"
case "failed": jobErr = fmt.Errorf("%s", ev.AnalysisError)
...
```

Problems: (a) `analysis_jobs.state`, `events.analysis_state`, and `events.state` are three
representations of one fact, reconciled by string mapping in two places (Go switch + SQL CASE);
(b) the re-read costs another full `events()` scan (finding 1); (c) a concurrent finalize of a new
generation between write and re-read can mis-record the job outcome; (d) the mapping switch is
untestable without a full server.

**Fix**: have `analyzeEvent` return `(outcome analysisOutcome, err error)` and let `analyzeLoop`
pass that directly to `finishAnalysisJob`. Keep the DB writes for durability, but stop using the
DB as the return channel. This deletes the re-read, the reverse mapping, and the race.

---

## P2 — worthwhile

### 4. Broken error formatting when the event is missing in `analyzeEvent`

`analyze.go:34-37`:

```go
ev, err := c.store.event(eventID)
if err != nil || ev == nil {
	fail("", fmt.Errorf("loading event: %w", err))   // err may be nil here
```

When `ev == nil` and `err == nil` (event deleted / never existed), the stored analysis error is
`"loading event: %!w(<nil>)"`. Split the two conditions (`"event not found"` for the nil case).
One-line fix; it's the message a user would actually see in `analysis_error`.

### 5. Nil-pointer dereference window in `handlePutEventV1`

`highlevel_http.go:57-64`: after a successful upsert, `c.store.event(key)` is called and `ev.State`
is dereferenced with no `ev == nil` check. `store.event` returns `(nil, nil)` for a missing row.
Unreachable under normal flow, but any future delete/cleanup path (space reclamation is on the
roadmap) turns this into a panic that takes down the request. Add the nil check — it costs
nothing and the pattern is already used in `handleGetEventV1`.

### 6. The store layer takes no `context.Context`

Every store method uses `db.Exec`/`db.Query` rather than the `Context` variants. Consequences:

- An HTTP client that disconnects keeps its query running.
- `analyzeLoop`'s store calls can't be interrupted at shutdown; `Run` returns while a claim/finish
  may still be in flight.
- `busy_timeout(5000)` means a lock-contended write can pin a handler for 5 s with no cancel.

**Fix**: mechanical — thread `ctx` through the store methods (handlers pass `r.Context()`,
the analyze loop passes its loop ctx) and switch to `ExecContext`/`QueryRowContext`. Do it
opportunistically per method as they're touched; the signatures are internal.

### 7. Request-body hygiene is inconsistent between the two HTTP layers

`highlevel_http.go:236` has the right helper:

```go
func decodeJSON(w, r, dst) — http.MaxBytesReader(4 MiB) + DisallowUnknownFields
```

But the account/device endpoints decode bare bodies with no size cap and silently accept unknown
fields: `handleRegisterDevice` (`http.go:190`), `handlePutPushToken` (`push.go:193`),
`handlePutNotificationSettings` (`push.go:254`). `handleDeviceHeartbeat` rolls its own
`io.LimitReader` (`http.go:248`) — a third pattern, and note `LimitReader` silently *truncates* an
oversized heartbeat and stores the truncated (possibly invalid-JSON-but-validated-by-prefix-probe)
body rather than rejecting it; `MaxBytesReader` would 413.

**Fix**: use `decodeJSON` everywhere a JSON body is read (move it to a shared `httputil.go` inside
the package); replace the heartbeat's `LimitReader` with `MaxBytesReader`.

### 8. Two response-writing styles, and internal errors leak to clients

`http.go` handlers hand-roll `w.Header().Set(...); json.NewEncoder(w).Encode(...)` while
`highlevel_http.go` uses `writeJSON`. More important: nearly every 500 path does
`http.Error(w, err.Error(), 500)`, sending raw SQLite errors, file paths, and wrapped internal
messages to any authenticated caller (and, in dev mode, anyone). For a product that will face
the public internet (behind Caddy, but the app talks to it), 500 bodies should be generic and the
detail should go to the log.

**Fix**: one `internalError(w, logf, err)` helper that logs the real error and writes
`"internal error"`; adopt `writeJSON` in `http.go`. Small, mostly mechanical, and it also gives a
single place to add request logging later.

### 9. `analyzeEvent` mixes five responsibilities in one 100-line function

`analyze.go:22-122` does: load + clip selection → mark running → run analyzer → parse/validate
verdict → persist → thumbnail generation → notification-frame extraction → Telegram notify → FCM
push. The pure parts (clip selection, camera ranking, verdict parsing) are already well factored
and tested; the *orchestration* is only testable through a full server with a fake analyzer.

**Fix direction** (keep it light): extract the post-verdict fan-out into a
`deliverVerdict(ctx, ev, clip, parsed, res)` method, and the "load event + files + select clip"
prefix into `selectAnalysisClip(eventID)`. Combined with finding 3 (return the outcome), the loop
becomes: claim → select → analyze → persist → deliver → finish, each independently testable.

### 10. `store.go` vs `highlevel_store.go` is a historical split, not a design

Both files define methods on the same `*store`; the boundary is "phase 3 code" vs "v1-protocol
code", which no longer means anything (device/user/push-token methods landed in `store.go`, the
analysis-job queue in `highlevel_store.go`). Divergences that bite:

- Sentinel errors (`errEventNotFound`, `errFinalizedManifest`, …) exist only on the highlevel
  side; `store.go` methods signal absence with `(nil, nil)` / `("" , nil)` — two conventions for
  "not found" in one type, and the `(nil, nil)` convention caused findings 4 and 5.
- Time handling differs: `store.go` methods take `now time.Time` params (testable),
  `highlevel_store.go` calls `time.Now()` inline (`highlevel_store.go:22,59,102,187,259,273` —
  not testable, and `claimAnalysisJob` reads the clock twice in one transaction).

**Fix direction**: regroup by domain — `store_events.go`, `store_devices.go`, `store_users.go`
(users + push tokens), `store_jobs.go`, keeping `store.go` for open/schema/migrations. Pick one
absence convention (sentinel errors) and one clock convention (inject `now`, or give `store` a
`now func() time.Time` field defaulting to `time.Now` — a one-field test seam).

### 11. `http.Server` without timeouts; `Shutdown` error dropped

`server.go:108`: `srv := &http.Server{Handler: c.Handler()}` — no `ReadHeaderTimeout`, so a
slow-header client holds a connection forever (slowloris). Blob uploads legitimately need long
read times, so don't set `ReadTimeout`; `ReadHeaderTimeout: 10 * time.Second` and an
`IdleTimeout` are safe and enough. Caddy fronts the prod deploy, but the server also runs bare in
dev and the Pi agent talks straight to whatever's configured. Also `server.go:119`:
`srv.Shutdown(shutCtx)`'s error is ignored — log it, since a shutdown that times out means
in-flight uploads were cut.

### 12. Check-then-act races in `putManifest`, `finalizeManifest`, and device claiming

- `putManifest` (`highlevel_store.go:76-99`): the event-exists and manifest-not-finalized checks
  run *before* `s.db.Begin()`. A finalize racing between check and tx can be overwritten
  (finalized manifests are supposed to be immutable).
- `finalizeManifest` (`highlevel_store.go:157-186`): `current_generation`, manifest existence,
  video count, and `missingBlobs` are all read outside the tx that flips the event to `ready`.
- `handleRegisterDevice` (`http.go:205-227`): owner is read, policy applied in Go, then
  `upsertDevice` unconditionally overwrites `owner_user_id` — two users claiming the same device
  concurrently both pass the check and last-write-wins.

In practice there is one Pi and one worker, so none of these fire today — hence P2 not P1. But
they're cheap to close: move the reads inside the transaction (SQLite serializes writers anyway),
and make the device-claim a conditional SQL update
(`... SET owner_user_id = ? WHERE owner_user_id IS NULL OR owner_user_id = ?`, check RowsAffected).

### 13. Delivery blocks the single analysis worker

`analyzeEvent` ends with `c.notify(...)` (15–25 s timeout, `analyze.go:158-172`) then
`dispatchPush` which sends to each token **sequentially** with a fresh 15 s timeout per token
(`push.go:125-127`). Worst case an event with a slow notifier and 3 registered phones stalls the
analysis queue ~70 s. The verdict is already durable at that point.

**Fix**: fire delivery in a goroutine (the pattern already exists — `notifyUploadReceived`,
`notify.go:38-51`, is async), or at minimum send pushes concurrently per token with a shared
context. Keep the "log and never fail analysis" contract.

---

## P3 — nice-to-have

### 14. Dead code

- `pendingAnalyses` (`store.go:190-200`) — no callers anywhere; leftover from the pre-jobs
  recovery path (its job is now done by the SQL backfill in `openStore`).
- `prettyCamera` (`analyze.go:177-182`) — no callers (`prettyClipCamera` is the live one).
- `enqueueAnalysis` (`highlevel_store.go:255-261`) — called only from a test
  (`highlevel_test.go:177`); production enqueue happens inside `finalizeManifest`. Either delete
  and have the test use `finalizeManifest`, or comment it as a test hook.

### 15. Hardcoded values worth naming

- `90_000` ms online window inline in `handleDeviceStatus` (`http.go:296`) — the one constant a
  product decision will most likely revisit (heartbeat cadence coupling).
- `250 * time.Millisecond` analysis poll (`server.go:127`); consider a small named const and,
  eventually, a nudge channel from `finalizeManifest` instead of pure polling.
- `10 * time.Minute` per-analysis timeout (`analyze.go:59`), `30 s` thumb/frame timeouts,
  `15/25 s` notify timeouts, `3` attempts × `attempts*5s` backoff (`highlevel_store.go:305-306`).
- The threat-level ladder (`none/low/medium/high`) is spelled out independently in
  `push.go:41-62`, `gemini.go:54` (schema enum), and `gemini.go:419` (validation). A single
  `[]string` in `internal/protocol` (it *is* part of the contract) would keep them in lockstep.

Group these as consts at the top of their files; only promote to `Config` fields the ones tests
need to shrink (the poll interval already hurts test latency).

### 16. Migration mechanism

`store.go:113-133` detects "already migrated" by substring-matching `duplicate column` in the
error. Works with modernc/sqlite today, breaks silently if the driver rewords errors, and can
mask a genuinely different migration failure. `PRAGMA user_version`-gated migration steps are
~15 lines and make ordering explicit. Fine to defer until the next schema change.

### 17. A DB write per authenticated user request

`http.go:148`: `upsertUser` runs on every JWT-authenticated request ("cheap upsert keeps email
fresh"). Every user GET is a WAL write. Harmless at current scale; if the app ever polls
`/events`, cache the last-upserted (sub, email) pair in memory and skip identical writes.

### 18. JWT verifier ergonomics

`jwt.go`: `verify` → `key` → `refresh` performs a network fetch with no caller context (only the
client's 10 s timeout), so a request can't cancel it; concurrent unknown-kid requests all fetch
JWKS at once (no single-flight — `fetchedAt` is only set on success); `exp` is checked with no
leeway (`jwt.go:119`) and `nbf`/`iat` aren't checked at all. All minor for Supabase-issued
tokens; thread `r.Context()` through `verify(ctx, token)` when touching this file, and wrap the
refresh in `sync/singleflight` if the server ever sees real user traffic.

### 19. Blob endpoint has no size or quota limit

`handlePutBlobV1` (`highlevel_http.go:100-154`) streams `r.Body` to disk unbounded. Any valid
device token (stored on the Pi in the glovebox) can fill the VPS disk. The manifest already
declares artifact sizes — enforcing `Content-Length`/copy cap against a sane max (e.g. largest
plausible sentry clip, ~100 MB) is a few lines. Related: nothing ever garbage-collects blobs
orphaned by superseded generations — worth a note in the space-reclamation phase.

### 20. Duplicate ffmpeg single-frame extraction

`extractEventFrame` (`analyze.go:127-152`, accurate seek, temp file, full-res) and
`runThumbFFmpeg` (`thumbs.go:47-64`, keyframe seek, scaled). Deliberately different seek modes,
but the arg building / run / stat-nonempty scaffolding is copy-pasted. One
`extractFrame(ctx, ffmpeg, in, out string, seconds int, opts frameOpts) error` would carry both.

### 21. Heartbeat for an unknown device silently succeeds

`updateDeviceHeartbeat` (`store.go:494-499`) is an `UPDATE` whose RowsAffected is never checked;
`handleDeviceHeartbeat` returns 204 for a device id that was never registered (reachable by the
operator token, or by anyone in dev mode). Return 404 on 0 rows so a misconfigured agent notices.

### 22. Missing indexes

All lookups are correct but unindexed: `devices.owner_user_id` (used by `eventsOwnedBy`'s
subquery and `deviceOwner`-driven checks), `push_tokens.user_id`, `events.device_id`,
`analysis_jobs(state, available_at)`. Trivial `CREATE INDEX IF NOT EXISTS` additions to the
schema block; do it together with finding 1.

### 23. Ownership rule expressed twice

"A user sees events whose device they own" lives as SQL in `eventsOwnedBy` (`store.go:286-290`)
and as Go in `eventReadable` (`http.go:102-118`). They agree today (including the "NULL device_id
is operator-only" rule) but nothing keeps them agreeing. Cheapest guard: a test that lists via
`eventsOwnedBy` and asserts every returned event passes `eventReadable`, and vice versa for a
non-owned event.

### 24. Config surface split between flags and env

`main.go` takes everything as flags except `GEMINI_API_KEY` (env, justified — secrets) and
`TELEGRAM_DEBUG` (`main.go:90`, env for no clear reason, with bespoke parse-and-fatal logic).
Make debug notifications a `-debug-notifications` flag for consistency; keep secrets in files/env
as today. Also note `DebugNotifications` is Telegram-flavored in name at the main level
(`telegramDebug`) but generic in `server.Config` — the FCM path ignores it; fine, just keep the
naming honest.

---

## What's already good (don't churn)

- **Dependency inversion is done right where it matters**: `server` defines `Analyzer`,
  `Notifier`, `Pusher` (consumer-side interfaces); `gemini`/`telegram`/`fcm` implement them and
  are wired only in `main`. Tests inject fakes freely. Leave this alone — extracting the shared
  value types (`Notification`, `PushMessage`, `TokenUsage`) into a separate package would only
  matter if a non-server consumer appears.
- The **concrete `*store` with a real temp SQLite DB in tests** is the right call at this scale;
  do not introduce a store interface. The fixes above (per-method ctx, injected clock, domain
  file grouping) give the needed seams without one.
- `tokenfile` (empty-file-is-error), constant-time operator-token comparison via hash
  (`http.go:127-132`), token-hash-only device storage, HTML escaping discipline in `telegram`,
  JSON-only payload construction in `fcm`, and `gemini`'s pointer-field verdict validation are
  all careful and worth preserving as-is.
- The durable `analysis_jobs` queue with crash recovery + bounded retry is sound; findings 3/13
  are about how its worker communicates, not the queue design.

## Suggested order of attack

1. Finding 1 + 22 (one small store PR: `event(id)` by WHERE clause, add indexes).
2. Finding 2 (auth middleware injects operator in dev mode; delete the 10 guards) with
   findings 4, 5, 21 folded in — this is the robustness core.
3. Finding 3 + 9 (analyze worker returns its outcome; extract delivery fan-out) with 13
   (async delivery) as the follow-up.
4. Findings 7, 8, 11 as one "HTTP hygiene" pass.
5. Everything P3 opportunistically, when the file is already open.
