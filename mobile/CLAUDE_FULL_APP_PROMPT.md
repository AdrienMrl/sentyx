# Sentyx full mobile app prompt

Expand the approved Sentyx concept into a complete interactive React mockup. Use the design language we already agreed on and apply it consistently across the entire app. Do not propose additional design directions.

Sentyx is a hardware-and-cloud product that turns a Raspberry Pi connected to a Tesla into a real-time Sentry Mode monitor. The Pi appears to the car as USB storage, detects new Sentry events while they are being written, uploads selected clips, and sends them to Gemini for analysis. The mobile app connects locally to the Pi over Bluetooth or Wi-Fi and remotely to the Sentyx backend. The product will eventually support iOS and Android, but this work is an interactive mockup with realistic local data only.

Build all of the following screens and flows.

## Account and first launch

- Welcome screen with options to create an account or sign in.
- Account creation and sign-in screens.
- Password reset and email-verification states.
- Brief explanation of what Sentyx does and what hardware is required.
- Acceptance of terms and privacy policy.
- Returning-user state when an account exists but no device is paired.

## Device onboarding and pairing

- Introduction to pairing a Sentyx Pi with the app.
- Requests for Bluetooth, local-network, and notification permissions, including denied-permission states and instructions for enabling them later.
- Nearby-device discovery with scanning, device-found, multiple-devices-found, and no-device-found states.
- Secure pairing confirmation using a short code shown by both the Pi and the app.
- Device naming and vehicle naming.
- Vehicle information: model, optional nickname, and timezone.
- Wi-Fi setup: choose a network, enter credentials, connect, retry after failure, and skip for Bluetooth-only use.
- Link the paired Pi to the user's Sentyx backend account.
- Connection test covering Bluetooth, Wi-Fi, and backend connectivity.
- Firmware compatibility check and required-update state.
- Pairing-complete confirmation with a clear route into the event feed.

## Events

- Event feed showing events grouped by date.
- Each event should expose its time, location when known, thumbnail, camera source, duration, severity, Gemini summary, and analysis state.
- Analysis states: waiting for clip, uploading, analyzing, complete, failed, encrypted clip, and unsupported clip.
- Filters for severity, event type, date, vehicle, analysis state, favorites, and downloaded events.
- Search across event descriptions and locations.
- Empty feed, loading, offline, and server-error states.
- Pull-to-refresh behavior and pagination or older-event loading.
- Mark an event as reviewed or unreviewed.
- Favorite and unfavorite an event.
- Multi-select mode for downloading, deleting, or marking several events reviewed.

Use these representative events:

- Today, 2:14 PM, Downtown Garage — **Attention** — “Person lingered near driver door” — left repeater — 48 seconds — Gemini reports that a person looked through the driver window and then walked away; no contact detected; 92% confidence.
- Today, 11:38 AM, Whole Foods · Rampart — **Routine** — “Shopping cart passed close to vehicle” — front camera — 1:02 — no contact; 88% confidence.
- Today, 8:05 AM, Home — **Routine** — “Pedestrian walked past vehicle” — right repeater — 36 seconds — no stop or contact; 96% confidence.
- Yesterday, 10:42 PM, Street parking — **Urgent** — “Vehicle door made contact with passenger side” — right repeater — 1:14 — contact detected; 95% confidence.
- An event that is still being written by the car.
- An event waiting for upload because the Pi is offline.
- An encrypted event that cannot currently be analyzed.
- An event whose Gemini analysis failed and can be retried.

## Event detail and playback

- Video preview and playback controls.
- Full-screen playback.
- Switch between available camera angles when multiple clips exist.
- Event time, duration, location, vehicle, camera source, severity, and current storage location.
- Gemini description, confidence, detected subjects, detected contact, and the distinction between observed facts and Gemini's interpretation.
- Meaningful-moments timeline that can jump playback to notable timestamps.
- Analysis-in-progress and analysis-failed versions of the screen.
- User feedback on the analysis: accurate, inaccurate, or false alarm, with an optional correction.
- Change severity manually.
- Mark reviewed, favorite, share, export, delete, or retain longer.
- Show the remaining retention period for cloud and device copies.
- Show whether the original clip, an optimized preview, or both are available.
- Deep-link behavior when the screen is opened from a notification.

## Downloads and transfers

- Start a full-quality clip download from event detail.
- Choose an available transfer route: direct Wi-Fi, local Wi-Fi network, or Bluetooth.
- Explain when a route is unavailable and offer an available alternative.
- Show file size, estimated duration, destination, and available phone storage before starting.
- Transfer queue showing queued, connecting, transferring, paused, complete, failed, and canceled items.
- Progress, speed, remaining time, pause, resume, retry, cancel, and remove-from-history actions.
- Background-transfer state and a completed-transfer notification.
- Handle connection loss and resume a partial transfer.
- Handle insufficient phone storage.
- Handle a clip that is still being written by the car.
- Handle an original that has already been removed from the Pi.
- Downloaded-clips library with playback, sharing, export to the photo library or files, and deletion from the phone.
- A setting for preferred transfer route and whether cellular backend downloads are allowed.

## Device overview

- Overview for the selected vehicle and Sentyx Pi.
- Device online, offline, reconnecting, and needs-attention states.
- Current connection paths: Bluetooth, Wi-Fi, and backend.
- Last seen time.
- Whether the car is currently writing an event.
- Pi storage used and remaining.
- Pending event and upload backlog.
- Estimated offline capacity.
- Power health, temperature, firmware version, and update availability.
- Current network and signal quality.
- Recent device issues or warnings.
- Multiple-device switcher and add-another-device flow.

Use this normal device state: **Model 3**, device name **Garage Pi**, connected over Wi-Fi and to the backend, Bluetooth nearby, 73% storage free, no upload backlog, healthy power, normal temperature, and current firmware.

Also include device states for offline, low storage, overheating, weak power, upload backlog, firmware update required, and backend unavailable while local connectivity still works.

## Device settings and maintenance

- Rename the device and vehicle.
- Update vehicle information and timezone.
- Manage saved Wi-Fi networks and connect to a new network.
- Reconnect or forget Bluetooth pairing.
- Preferred transfer route.
- Automatic upload and automatic download preferences.
- Clip-selection behavior and whether only the most relevant camera clip is uploaded.
- Local retention and storage-reclamation settings.
- Offline spool/storage limit.
- Firmware update flow with checking, downloading, installing, restarting, success, and failure states.
- Restart device.
- Diagnostics summary and exportable diagnostic report.
- Connection test.
- Remove the device from the account.
- Factory-reset flow with explicit confirmation.

## Notifications

- Notification settings overview.
- Minimum event severity that should trigger an alert.
- Separate controls for urgent events, attention events, routine events, analysis completion, analysis failure, device offline, device reconnected, low storage, weak power, overheating, firmware updates, and transfer completion.
- Event-type controls for people, vehicles, animals, motion, and detected contact.
- Quiet hours with start time, end time, days of week, and an option allowing urgent alerts through.
- Per-vehicle notification settings.
- Notification-preview privacy setting.
- A notification history screen.
- Example notification previews and routes into the relevant event or device issue.

## Subscription and billing

- Subscription overview showing the current tier and usage.
- Comparison of Free, Premium, and an additional higher tier if appropriate.
- Use clearly labeled mock pricing and mock feature limits that can be changed later.
- Show relevant differences such as event-history duration, Gemini analysis allowance, number of vehicles, cloud storage, advanced notification rules, and remote full-quality access.
- Upgrade flow, optional trial, purchase confirmation, success, declined-payment, and canceled-purchase states.
- Restore purchases.
- Manage billing, change plan, cancel, renew, and expired-subscription states.
- Explain what remains available locally when a subscription expires or the backend is unavailable.
- Usage-limit reached state with the next reset date and available actions.

## Account, privacy, and app settings

- Account profile and email management.
- Change password and account-security settings.
- Signed-in devices or sessions with the ability to revoke a session.
- App permission status for Bluetooth, local network, notifications, photo library, and files.
- Data-retention preferences.
- Export account data.
- Delete cloud event data.
- Delete account with confirmation.
- Units, time display, and language settings.
- Help center, contact support, send feedback, privacy policy, terms, app version, and sign out.

## Cross-app and system states

- No internet while the Pi remains reachable locally.
- Internet available while the Pi is not locally reachable.
- Completely offline mode.
- Session expired and sign-in required again.
- Backend maintenance or server error.
- Bluetooth turned off.
- Local-network permission revoked.
- Notification permission revoked.
- A newly detected urgent event arriving while another screen is open.
- A transfer continuing while the user browses other screens.
- Destructive-action confirmations and success or failure feedback.

## Prototype behavior

Make the main flows interactive rather than presenting disconnected static screens. A reviewer should be able to:

1. Complete onboarding and pair a mock device.
2. Browse and filter events.
3. Open an event, review the Gemini result, change camera angle, and jump to a meaningful moment.
4. Start a full-quality download and inspect its transfer progress.
5. Inspect normal and unhealthy device states.
6. Change notification severity and quiet-hours preferences.
7. Compare subscription tiers and walk through a mock upgrade.
8. Access account, privacy, support, and maintenance actions.

Use realistic mock data throughout. Do not implement real Bluetooth, Wi-Fi, backend, payment, notification, firmware, file-transfer, authentication, or destructive operations.
