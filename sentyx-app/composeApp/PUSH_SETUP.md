# Push notifications (Android / FCM)

`composeApp/google-services.json` contains a real Firebase Android client
configuration, not a placeholder. Forks should replace it with the configuration
for their own Firebase project. Firebase client API keys identify a project;
they do not grant administrative access. Restrict their allowed APIs and Android
application/signing certificate in Google Cloud as appropriate:
[Firebase API key documentation](https://firebase.google.com/docs/projects/api-keys).

FCM sending requires separate backend service-account credentials. Keep those
credentials outside the repository and set `FCM_CREDENTIALS_FILE` on the server.

## To make push actually work

1. Create a Firebase project, add an Android app with package `com.sentyx.app`.
2. Download the real `google-services.json` and replace this file with it.
3. Configure the backend server with the FCM service-account
   credentials so it can send to registered tokens.
4. Rebuild. On sign-in the app registers its FCM token via
   `PUT /v1/me/push-tokens`; verdicts at or above the user's threshold arrive on
   the `sentry_alerts` channel.

## How the plugin is wired

`composeApp/build.gradle.kts` applies `com.google.gms.google-services`
**only if `google-services.json` exists**. Removing this client configuration
skips Firebase resource processing. See the guarded `apply(plugin = ...)` block there.

## iOS

No APNs is wired. `PushTokenProvider` (iOS actual) returns `null`, so the whole
token lifecycle is a no-op on iOS. This is intentional for now.
