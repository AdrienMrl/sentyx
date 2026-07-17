# Refactor findings — sentyx-app (Kotlin Multiplatform app)

Audit scope: `sentyx-app/composeApp/src` (~133 `.kt` files, commonMain + androidMain + iosMain + commonTest), SOLID/layering/state-management/robustness lens. Audited 2026-07-16.

> **Caveat — moving tree.** A concurrent session was landing push-notification work *during* this audit (`core/push/`, `data/push/`, `RealNotificationSettingsRepository`, `MainActivity` changes are uncommitted). Findings reflect the tree at audit time; a few "demo repo wired in real container" items were literally fixed mid-audit (notifications), which is why each finding carries file:line anchors — re-verify anchors before acting.

## Verdict in one paragraph

This is a well-architected small app: real hexagonal layering (domain interfaces in `domain/repository/`, data impls behind them, thin ViewModels, one manual composition root), disciplined `CancellationException` rethrow everywhere, pure unit-tested mapping functions, a single-flight token refresh, and a genuinely careful BLE session layer. The problems are not structural rot — they are (a) demo-era artifacts still live in the real flavor (fake success toasts, a crash-path route, a fake prefilled Wi-Fi password, committed backend config), (b) lifecycle blind spots (container/Navigator die on Android configuration change while ViewModels survive holding the old container; ViewModels are never cleared; poll loops run forever), and (c) a test suite that covers pure functions but none of the seams the architecture already paid for.

## Summary table

| # | Priority | Finding | Where |
|---|----------|---------|-------|
| 1 | **P1** | "Prototype states" row crashes the real flavor (`error()` on null `demoState`) | `SettingsHubScreen.kt:88`, `AppRoot.kt:416-418` |
| 2 | **P1** | Success toasts for silent no-op operations — UI lies (factory reset, restart, retry analysis, delete, bulk download) | `DeviceViewModel.kt:127-187`, `RealDeviceRepository.kt:130-138`, `RealEventRepository.kt:125-133`, `EventsFeedViewModel.kt:111-146`, `EventDetailViewModel.kt:118-145` |
| 3 | **P1** | Fake prefilled Wi-Fi password (`sentyx-demo-key`) is silently sent to the Pi; join failures are swallowed | `PairingViewModel.kt:180-186, 271`, `BlePairingService.kt:227-243` |
| 4 | **P1** | App container + Navigator live in composition; Android config change splits brain (surviving VMs hold a dead container) | `AppRoot.kt:111-134` |
| 5 | **P1** | Backend URL + Supabase URL + anon key committed in source; `BuildFlavor` doc contradicts its value | `BuildFlavor.kt:16-27` |
| 6 | **P2** | ViewModels are never cleared (single app-wide ViewModelStoreOwner); per-event `EventDetailViewModel`s accumulate; stale auth/pairing state on re-entry | `AppRoot.kt:171-425` (all `viewModel {}` call sites), `AppRoot.kt:268-271` |
| 7 | **P2** | Poll loops run unconditionally forever — no lifecycle gating, no error backoff | `RealDeviceRepository.kt:51-66`, `RealEventRepository.kt:76-93` |
| 8 | **P2** | Demo repos + hardcoded demo strings still wired into the real flavor (transfers, subscription, app settings, sessions list, "Garage Pi" labels) | `RealAppContainer.kt` (transfers/subscription/appSettings), `SupabaseAuthRepository.kt:36-37, 85-95`, `SettingsHubScreen.kt:70,103`, `DeviceSettingsScreen.kt:47-56` |
| 9 | **P2** | Pairing defaults send display strings as device config (`timezone = "Pacific · GMT-7"`, `vehicleModel = "Model 3"`) | `PairingViewModel.kt:293-296`, `BlePairingService.kt:170-178` |
| 10 | **P2** | Raw exception text (incl. HTTP response bodies) surfaces in user-facing toasts; no typed error model | `SentyxApi.kt:72-75` (pattern repeats), `DeviceViewModel.kt:200-208`, `AuthViewModel.kt:120-128` |
| 11 | **P2** | Test coverage stops at pure functions; zero ViewModel/repo/session tests despite ready-made seams | `commonTest/` (6 files) |
| 12 | **P3** | `SharingStarted.Eagerly` in a ViewModel keeps flows hot for the VM's (unbounded, see #6) lifetime | `DeviceViewModel.kt:72-80` |
| 13 | **P3** | `println` logging in the BLE stack; ships in release, logs SSIDs/status details | `PiBleSession.kt:263`, `BlePairingService.kt:335`, `BleDeviceWifiService.kt:182` |
| 14 | **P3** | 21 raw `Color(0x…)` literals in feature/ui code bypass the design-token system | `AppRoot.kt:445-498`, 8 more files |
| 15 | **P3** | `SentryEvent.originalSizeLabel = "248 MB"` fabricated default; real mapping passes `""` producing broken toast copy | `Event.kt:63`, `EventMapping.kt:183`, `EventDetailViewModel.kt:164-172` |
| 16 | **P3** | `EventSummaryDto`/`EventDetailDto` duplicate 16 fields + hand-written `toSummary()` | `SentyxApi.kt:339-415` |
| 17 | **P3** | Dead code: `VerifyEmail` route + `verifyEmail()` no-op retained | `AppRoot.kt:192-198`, `SupabaseAuthRepository.kt:57-60` |
| 18 | **P3** | Navigator state not saveable (process death → back to start route) | `Navigator.kt:15-19`, `AppRoot.kt:131-134` |

---

## P1 — hot

### 1. "Prototype states" crashes the real flavor

`SettingsHubScreen.kt:88` renders the row unconditionally:

```kotlin
HubRow("🧪", "Prototype states", "Toggle demo conditions", onOpenPrototypeStates, showDivider = false)
```

and `AppRoot.kt:416-418` does:

```kotlin
Route.PrototypeStates -> {
    val demoState = c.demoState
        ?: error("PrototypeStatesScreen requires AppContainer.demoState, but it is null. ...")
```

With `BuildFlavor.useRealPairing = true` (the current shipped value), `RealAppContainer.demoState` is `null` — one tap on a visible settings row throws and kills the app.

**Why it matters:** it's a guaranteed user-reachable crash in the flavor you actually run against the car.

**Direction:** the container already exposes the capability signal (`demoState != null`). Pass it into `SettingsHubUiState` (or read `LocalAppContainer` in the screen) and render the row only when non-null. One conditional; no new abstraction.

### 2. Success toasts for operations that do nothing

The demo-era pattern "show optimistic toast, then call the repo" is still in place, but several real-repo methods are deliberate no-ops:

- `RealDeviceRepository.restart()` / `factoryReset()` — no-op stubs (`RealDeviceRepository.kt:130-138`, comments admit it), yet `DeviceViewModel.factoryReset()` (`DeviceViewModel.kt:148-166`) shows **"Device reset — \<name\> has been erased"** *before* even calling the repo. Restart shows "will be back in about a minute". Neither happens.
- `RealEventRepository.retryAnalysis()` / `delete()` — no-ops (`RealEventRepository.kt:125-133`), while `EventDetailViewModel.retry()` toasts "Retrying analysis… Queued with Gemini" (`EventDetailViewModel.kt:118-129`).
- `EventsFeedViewModel.bulkDownload()` (`EventsFeedViewModel.kt:111-123`) toasts "Added to transfers / Selected clips queued" without touching `TransferRepository` at all — the ids are read and discarded.
- `bulkDelete`, `share`, `requestDelete`, `fullscreen`, `extendRetention` at least say "Mock —" in the toast, which is honest but still shipped UI.

**Why it matters:** a real user with a real Pi is told their device was erased/restarted when nothing was sent anywhere. This is the single most trust-damaging class of bug an ops-flavored app can have. It also inverts the toast/action ordering (toast fires even if the repo call later throws).

**Direction (pick per action):**
1. For genuinely unimplemented server ops: remove or disable the UI affordance (a `capabilities` value on the repository interface, or simply hide the row like the firmware card already does via `firmwareUpdate == null`).
2. For implemented ops: toast **after** the suspend call succeeds, error toast on throw — `installFirmware()` (`DeviceViewModel.kt:101-124`) already does it right; copy that shape.
3. Never write a success toast in the same statement block that launches the coroutine.

### 3. Fake Wi-Fi password prefill + swallowed join failures

`PairingViewModel.kt:271`: `PREFILLED_WIFI_PASSWORD = "sentyx-demo-key"`. `loadNetworks()`/`selectNetwork()` (`PairingViewModel.kt:173-203`) prefill the password field with it for any secured network. In the real flavor, a user who taps through (the field shows dots, looks legit) sends `"sentyx-demo-key"` to the Pi as their home Wi-Fi password. Then `BlePairingService.connectWifi()` (`BlePairingService.kt:227-243`) deliberately swallows the rejection (`log(...)` only), and `PairingViewModel.connectWifi()` (`PairingViewModel.kt:207-213`) is fire-and-forget with no state update — so onboarding proceeds showing no error. The safety net is the connection-test screen, but that only proves the Pi can reach the backend over *some* network (possibly LTE/current network), not that the join succeeded.

**Why it matters:** silent misconfiguration during the one flow a customer only ever runs once. Compare `SavedWifiNetworksViewModel.join()` (`SavedWifiNetworksViewModel.kt:196-227`), which does this correctly — awaits the result, surfaces `connectError`, re-opens the password sheet.

**Direction:** delete the prefill (empty field + "required" affordance). Make onboarding `connectWifi` await the `WifiResultDto` like the settings flow does and block "Continue" on failure — the plumbing (`wifiRequest`, `WifiResultDto.detail`) already exists; this is mostly deleting the tolerance.

### 4. Composition-scoped container + Navigator: config-change split brain

`AppRoot.kt:111-134`:

```kotlin
val scope = rememberCoroutineScope()
val container = remember { RealAppContainer(scope, ...) / DemoAppContainer(scope) }
...
val navigator = remember { Navigator(startRoute) }
```

On Android, an Activity recreation (rotation, dark-mode toggle, split-screen, process trim) destroys the composition: `rememberCoroutineScope` cancels (killing `RealDeviceRepository`/`RealEventRepository` poll loops, `stateIn` jobs, any live `PiBleSession` observe job), and a **new** container is constructed. But ViewModels survive in the Activity's `ViewModelStore` — and they hold references to the **old** container's repositories. Result after rotation: `DeviceViewModel.state` combines flows from a dead repo (frozen data, no more polls), while the TabBar (`AppRoot.kt:434-436`) observes the new container's fresh repos — two sources of truth on one screen. The `Navigator` back stack and current route are also reset to the start route.

**Why it matters:** every rotation silently degrades the app into a frozen-data state that only a process kill fixes; mid-pairing rotation drops the BLE session and restarts the flow. This is the root robustness issue — several other findings (6, 7, 12) get much easier once ownership is fixed.

**Direction:** hoist the container out of composition — a lazily-initialized holder created once per process (Android: `Application.onCreate` next to `SentyxAppContext.init`; iOS: top-level in `MainViewController`), owning its own `CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)`. Keep `LocalAppContainer` as the injection surface. Make `Navigator.current`/stack `rememberSaveable` (routes are trivially string-encodable — see finding 18).

### 5. Committed backend config/credentials + self-contradicting flag

`BuildFlavor.kt:16-27`: `useRealPairing = true` with KDoc *"Default false so the app runs on demo data out of the box"*, and a fully populated `AppConfig` — production server URL, Supabase project URL, and the publishable anon key — as source-code literals on `main`.

**Why it matters:** (a) the anon key is designed-public but the *pattern* invites the next secret to be pasted in the same place; (b) flavor is a code edit, not a build parameter, so you can't build demo and real from the same commit, and the doc/value drift shows it's already being hand-toggled; (c) it violates the repo's own "no implicit defaults" rule in spirit — config belongs to the build, not the source.

**Direction:** Gradle `buildConfigField`s (or a generated `AppConfig` from `local.properties`/CI env) with real product flavors `demo`/`real`; keep the fail-fast `AppConfig.init` exactly as is. `BuildFlavor` then reduces to reading generated constants. For iOS, an `expect val appConfig` fed from the framework consumer, or a compile-time generated file.

---

## P2 — worthwhile

### 6. ViewModels are never cleared; state leaks across flows

There is one `ViewModelStoreOwner` for the whole app (the Activity / `MainViewController`), and the hand-rolled `Navigator` knows nothing about ViewModel scoping. Consequences:

- `viewModel(key = "detail-" + route.eventId)` (`AppRoot.kt:268-271`) creates one `EventDetailViewModel` per event ever opened, none released until process death. Each holds a `stateIn(WhileSubscribed)` (collection does stop off-screen — good) but the VM objects and their `local` state accumulate.
- `AuthViewModel` (shared by key-less `viewModel {}` across SignIn/CreateAccount/Verify/Reset — `AppRoot.kt:178-203`) keeps the user's **password** in `AuthUiState` in memory for the entire session after sign-in.
- `PairingViewModel` shared across all 8 pairing routes is intentional and good — but it also survives *after* pairing completes, so "Add device" → pair a second Pi re-enters with stale `selectedDevice`, `pairingError`, `wifiNetworks`, `connectionTest` from the previous run.

**Why it matters:** memory growth is minor; the real issues are the retained password and stale-flow re-entry bugs that will surface exactly when someone pairs a replacement Pi.

**Direction (pragmatic, no nav library):** give `Navigator` a per-entry `ViewModelStore` map keyed by route (clear it on `back()`/`reset()`/`switchTab()`), and provide it via `LocalViewModelStoreOwner` around `RouteContent`. That's ~40 lines and makes VM lifetime match navigation semantics. Cheaper stopgap: `AuthViewModel.clearSensitive()` called on nav-away, and a `reset()` on `PairingViewModel` invoked from `PairIntroScreen`.

### 7. Unconditional infinite poll loops

`RealDeviceRepository.kt:51-66` (15 s) and `RealEventRepository.kt:80-93` (30 s) launch `while(true)` loops in `init` on the app scope. They run when the app is backgrounded (until Android kills the process), when signed out (each tick then throws "Not signed in." inside `authed()` and is swallowed), and they retry at full rate against a down server.

**Why it matters:** battery + LTE data on the phone, avoidable steady-state load on the VPS, and log noise; also both repos start polling at container construction even on the Welcome screen.

**Direction:** replace the init-launched loop with a cold `flow { while(true) { emit(fetch()); delay(...) } }` turned into the public `StateFlow` via `stateIn(scope, SharingStarted.WhileSubscribed(stopTimeout), initial)` — subscription count then naturally gates polling to "some screen is showing this" (the TabBar badge keeps device polling alive on tab roots, which is correct). Add exponential backoff with cap on consecutive failures, and skip ticks while `sessionManager.session.value == null`. This also removes the repos' need to *own* a scope, simplifying construction/testing.

### 8. Demo residue wired into the real flavor

`RealAppContainer` still binds demo implementations: `transfers = DemoTransferRepository(scope, device)`, `subscription = DemoSubscriptionRepository(scope, demoStateController)` (with a privately constructed `DemoStateController`), `appSettings = DemoAppSettingsRepository()`. Plus demo strings inside otherwise-real classes:

- `SupabaseAuthRepository.sessions` returns a hardcoded `DEMO_SESSIONS` row; `revokeSession` just filters the fake list (`SupabaseAuthRepository.kt:36-37, 66-69, 85-95`).
- `UserProfile.twoFactorEnabled = false` hardcoded with a demo Security toggle on top (`SupabaseAuthRepository.kt:98-107`, `AccountViewModel.kt:43`).
- `SettingsHubScreen.kt:70` hardcodes "Device — Garage Pi" (the real name is in `KeyValueStore` under `StorageKeys.DEVICE_NAME`); `:103` shows "Sentyx · v1.0.0 (mock)".
- `DeviceSettingsScreen.kt:47-56` shows hardcoded "Garage Pi", "Model 3 · GMT-7", "7 days", "4 GB" values as if they were device state.

(The old "home device-status card is mock" note is **stale**: `RealDeviceRepository` + `mapDeviceSnapshot` now serve real M6 heartbeats. Notifications also went real mid-audit.)

**Why it matters:** each is a small lie in a shipped surface; collectively they make it impossible to tell real from fake without reading source. Transfers is the worst: real events appear "downloadable" and progress bars animate, backed by nothing.

**Direction:** triage each demo binding into (a) implement (transfers is the roadmap item), (b) hide the surface in the real flavor (subscription screen, sessions list, 2FA toggle), or (c) drive from data you already have (`DEVICE_NAME` for the hub row; drop the version-mock footer for `versionName`). An `AppContainer.isDemo` (or reuse `demoState != null`) is enough of a gate — no new architecture needed.

### 9. Display strings sent as device configuration

`PairingUiState` defaults (`PairingViewModel.kt:293-296`): `deviceName = "Garage Pi"`, `vehicleModel = "Model 3"`, `timezone = "Pacific · GMT-7"`. `configure()` forwards these verbatim; `BlePairingService.configure()` (`BlePairingService.kt:170-178`) writes `timezone` into the Pi's `ConfigDto`. A UI label ("Pacific · GMT-7") is not an IANA zone id — whatever the agent does with it (event timestamps, quiet hours) will be wrong or ignored, and there is no screen to edit it.

**Direction:** send `TimeZone.currentSystemDefault().id` (kotlinx-datetime is already a dependency) and keep the pretty label UI-only; make vehicle model free-text or omit it from `ConfigDto` until it has a consumer.

### 10. Error handling: raw internals in user-facing copy, toast-only modeling

`SentyxApi` builds exception messages like `"Device registration failed (${response.status}): ${response.bodyAsText()}"` (`SentyxApi.kt:72-75`, repeated for every endpoint), and the shared `showError()` pattern (`DeviceViewModel.kt:200-208`, `AuthViewModel.kt:120-128`, `SavedWifiNetworksViewModel.kt:236-244`) puts `e.message` straight into a toast. A 500 with an HTML error page becomes toast copy. Contrast the BLE layer, which deliberately crafts UI-safe messages (`BleGattException`, `DeviceWifiException`, `PairingException`) — the HTTP layer never got the same treatment. Secondarily: transient errors are *only* toasts; screens have no error state to re-render from (except pairing/wifi, which model inline errors properly).

**Direction:** give `SentyxApiException` a `status: HttpStatusCode` and a `userMessage` (generic per category: network unreachable / signed out / server error), keep the body in `message` for logs. One mapping function `Throwable.toUserMessage()` used by all `showError`s. Adopt the inline-error pattern from `SavedWifiUiState` for screens where a toast can be missed (auth is fine as-is since it has `loading` + toast).

### 11. Test coverage stops at pure functions

`commonTest/` has exactly: `LruCacheTest`, `SupabaseAuthClientTest` (good — uses ktor `MockEngine`), `ChunkConfigTest`, `FrameReassemblerTest`, `DeviceStatusMappingTest`, `EventMappingTest`. That is the right *kind* of test but stops precisely where the bugs in this report live. Untested and cheap to test with existing seams:

- **`SupabaseSessionManager`** — constructor-injected client + store; the single-flight refresh, near-expiry logic, and clear-on-failure (`SupabaseSessionManager.kt:78-93`) are subtle and pure. Fake the client, use a `MutableMap`-backed fake `KeyValueStore` (the `expect class` makes this awkward — see below).
- **`SentyxApi.authed()` 401-retry** — MockEngine, no new seams needed.
- **ViewModels** — every one takes only interfaces + `ToastController`; the `data/demo/*` implementations are ready-made fakes. Missing piece is `Dispatchers.setMain` (`kotlinx-coroutines-test` is already in `commonTest` deps) — a tiny `MainDispatcherRule`-style helper unlocks all of them. Highest-value targets: `PairingViewModel` (flow-critical), `EventsFeedViewModel.filterGroups`, `RealEventRepository` feed-state transitions (inject a fake `SentyxApi`… which is a **class**, see below).
- **Seam gaps to fix while you're there:** `KeyValueStore` is an `expect class` (not an interface) so commonTest can't fake it without an actual — split into `interface KeyValueStore` + `expect fun platformKeyValueStore()`. `SentyxApi` is a concrete class injected everywhere — either keep it and test repos via ktor MockEngine through it, or extract the 4-5 methods each repo uses into small interfaces (interface segregation win too: `RealDeviceRepository` needs only `deviceStatus`; `PushController` only the two token calls).

---

## P3 — nice-to-have

### 12. `SharingStarted.Eagerly` in `DeviceViewModel`

`DeviceViewModel.kt:72-80` uses `Eagerly` where every other VM uses `WhileSubscribed(5_000)`. Combined with finding 6 (VMs never cleared) each visited device screen keeps its combine active forever. Switch to `WhileSubscribed` for consistency; the eager initial value is already provided.

### 13. `println` logging in the BLE stack

`PiBleSession.log` (`PiBleSession.kt:263`), `BlePairingService.kt:335`, `BleDeviceWifiService.kt:182`. No levels, always on in release, and lines include SSIDs and device status details. A 10-line `expect fun logDebug(tag, msg)` (Android `Log.d`, iOS `NSLog`, no-op in release) preserves the excellent diagnostic coverage without shipping it.

### 14. Raw color literals bypass the token system

21 `Color(0x…)` occurrences across 9 feature/ui files (`ui/AppRoot.kt:445,447,478,498`, `EventsFeedScreen.kt`, `EventDetailScreen.kt`, `EventsStyles.kt`, `AuthComponents.kt`, `PairingCommon.kt`, `DeviceScreen.kt`, `DeviceSettingsScreen.kt`, `SubscriptionScreen.kt`) while `core/designsystem/Tokens.kt` + `SxColors` exist for exactly this. Mechanical consolidation; prevents drift when theming.

### 15. Fabricated `originalSizeLabel` default

`Event.kt:63`: `val originalSizeLabel: String = "248 MB"` — a made-up default that violates the repo's explicit no-implicit-defaults rule; the real mapping passes `""` (`EventMapping.kt:183`), so the download toast renders "` over Wi-Fi`" (`EventDetailViewModel.kt:164-172`). Make it nullable with no default; render "size unknown" when null; hide the size in the toast.

### 16. DTO duplication in `SentyxApi`

`EventSummaryDto` and `EventDetailDto` duplicate 16 fields, kept in sync by hand plus a 20-line `toSummary()` (`SentyxApi.kt:339-415`). Compose instead: `EventDetailDto(…summary fields via @Serializable flattening isn't available…)` — simplest is `EventDetailDto` = `summary: EventSummaryDto`-shaped decode by decoding the same body twice, or keep one DTO with `files: List<FileInfoDto> = emptyList()`. The single-DTO option is least code and the server's detail payload is a strict superset.

### 17. Dead verification flow

`Route.VerifyEmail` is documented "retained but unreachable" (`AppRoot.kt:192-198`); `verifyEmail()` is a no-op (`SupabaseAuthRepository.kt:57-60`); `AuthViewModel.resendCode()` shows a fake "Code resent" toast. Delete the route, screen, and methods — git remembers.

### 18. Navigator not restoration-safe

`Navigator` (`Navigator.kt:15-19`) holds `current` in plain `mutableStateOf` and a plain `ArrayDeque`; `remember { Navigator(startRoute) }` (`AppRoot.kt:134`) means both config change (finding 4) and process death land the user on the start route. Routes are a closed sealed set — a `Saver` that encodes `Route` to a string (`"eventDetail:<id>"` for the one data class) makes the whole thing `rememberSaveable` in ~20 lines. Do it together with finding 4.

---

## What is already good (keep it this way)

- **Dependency inversion is real.** Every feature depends on `domain/repository` interfaces; concretions (Kable BLE, Ktor, Supabase) are constructor-injected via one composition root (`AppContainer` / `RealAppContainer` / `DemoAppContainer`). No service locators, no statics (except `SentyxAppContext`, which is a necessary Android bridge and fails fast).
- **Interface segregation is mostly fine.** `SettingsRepositories.kt` bundles three *small* interfaces in one file — that's file organization, not an ISP violation; each interface is cohesive and separately implemented. No action needed beyond optionally splitting the file for discoverability.
- **Coroutine hygiene is unusually disciplined**: `CancellationException` rethrow before every broad catch, single-flight refresh with stale-token check (`SupabaseSessionManager.refreshLocked`), subscribe-before-write races handled with `CoroutineStart.UNDISPATCHED` in BLE, `NonCancellable` cleanup in the thumbnail loader, dedup + LRU there too.
- **Pure mapping layers** (`EventMapping`, `mapDeviceSnapshot`/`buildDiagnostics`) with honest fallbacks and unit tests — extend this pattern, don't dilute it.
- **PairingViewModel / SavedWifiNetworksViewModel error modeling** (inline `pairingError`/`configureError`/`connectError` + in-flight flags) is the house style the rest of the app should converge on.

## Suggested order of attack

1. #1 (one conditional), #3 (delete prefill + await join result), #2 (toast honesty) — user-facing correctness, each < 1 day.
2. #4 + #18 together (container to app scope, saveable Navigator) — unlocks 6/7/12.
3. #5 (build-time config/flavors) before any external testers get builds.
4. #7 (WhileSubscribed polling + backoff) and #6 (per-route ViewModelStore).
5. #11 test seams (`KeyValueStore` interface, MainDispatcher helper) + tests for `SupabaseSessionManager` and `PairingViewModel`.
6. P3 batch opportunistically.
