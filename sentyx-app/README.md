# Sentyx App (Kotlin Multiplatform)

Companion mobile app for **Sentyx** — the Raspberry-Pi-in-a-Tesla Sentry Mode
monitor. Shows the live event feed, per-event Gemini analysis, and lets you
pull the full-quality clip from the car. Shared UI in Compose Multiplatform,
targeting **Android** and **iOS**.

## Stack

| Component | Version |
|-----------|---------|
| Kotlin | 2.4.0 |
| Compose Multiplatform | 1.11.0 |
| Android Gradle Plugin | 8.11.0 |
| Gradle | 8.13 |
| compileSdk / minSdk | 36 / 24 |

Compose Multiplatform 1.11.0 ships its iOS/native klibs built with Kotlin
2.3.20, so the Kotlin/Native target requires Kotlin ≥ 2.3.20 — 2.2.20 fails to
resolve the klibs even though the Android target builds fine with it. Kotlin
2.4.0 (latest stable) consumes them and is used here.

## Layout

```
composeApp/src/commonMain/kotlin/com/sentyx/app/
  domain/model + domain/repository   contracts (entities + repo interfaces)
  data/demo                          demo implementations + DemoStateController
  core/designsystem                  tokens + Sx* component library (see DESIGNSYSTEM.md)
  core/navigation                    stack Navigator + sealed Routes
  di                                 AppContainer (constructor injection)
  feature/{onboarding,pairing,events,transfers,device,notifications,settings}
  ui/AppRoot.kt                      shell: route dispatch, tab bar, toast host
composeApp/src/androidMain/          MainActivity
composeApp/src/iosMain/              MainViewController
iosApp/                              Xcode project consuming the ComposeApp framework
design/Sentyx.dc.html                imported Claude Design prototype (source of truth)
```

The full 37-screen design (auth → pairing → events/device/transfers/settings
tabs, event detail with Gemini analysis, subscription/upgrade flow) is
implemented against `domain/repository` interfaces backed by `data/demo` —
swap in real Pi/backend implementations without touching feature code. The
Settings → "Prototype states" screen switches demo scenarios (device offline,
low storage, feed error, plan tier, urgent-event push) at runtime.

## Run

**Android** — open the project root in Android Studio (or IntelliJ with the KMP
plugin) and run the `composeApp` Android configuration, or:

```
./gradlew :composeApp:assembleDebug
```

**iOS** — open `iosApp/iosApp.xcodeproj` in Xcode and run the `iosApp` scheme on
a simulator. The `Compile Kotlin Framework` build phase invokes Gradle to build
the shared framework automatically. Set your Apple `TEAM_ID` in
`iosApp/Configuration/Config.xcconfig` for device builds.

## Current state

Full design implemented on demo data. Not yet wired: real backend/Pi
transport (Ktor client deps are in place), real BLE pairing, video playback,
push notifications, Android system-back (needs `org.jetbrains.compose.ui:ui-backhandler`).
