# Push notifications (Android / FCM) — PLACEHOLDER SETUP

**`composeApp/google-services.json` in this directory is a PLACEHOLDER.**
It contains dummy Firebase project ids (`project_id: sentyx-placeholder`,
`project_number: 000000000000`, a fake API key) with the correct
`package_name: com.sentyx.app`. It exists only so the `google-services` Gradle
plugin applies and `assembleDebug` / `installDebug` stay green.

**Push is INERT until this file is replaced with a real one.** With the
placeholder, `FirebaseMessaging.getInstance().token` fails (resolving to `null`,
so no token is registered) and FCM delivers nothing, so
`SentyxMessagingService` never runs. No crashes — every push path degrades to a
silent no-op.

## To make push actually work

1. Create a Firebase project, add an Android app with package `com.sentyx.app`.
2. Download the real `google-services.json` and replace this file with it.
3. Configure the backend (the other agent's server) with the FCM service-account
   credentials so it can send to registered tokens.
4. Rebuild. On sign-in the app registers its FCM token via
   `PUT /v1/me/push-tokens`; verdicts at or above the user's threshold arrive on
   the `sentry_alerts` channel.

## How the plugin is wired

`composeApp/build.gradle.kts` applies `com.google.gms.google-services`
**only if `google-services.json` exists**, so deleting this placeholder (rather
than replacing it) also keeps the build green — the app just compiles without
Firebase resource processing. See the guarded `apply(plugin = ...)` block there.

## iOS

No APNs is wired. `PushTokenProvider` (iOS actual) returns `null`, so the whole
token lifecycle is a no-op on iOS. This is intentional for now.
