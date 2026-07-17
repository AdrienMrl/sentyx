package com.sentyx.app.core.permissions

import androidx.compose.runtime.staticCompositionLocalOf

/**
 * Platform bridge for the runtime notification permission (Android 13+
 * `POST_NOTIFICATIONS`). Below Android 13, and on iOS (no APNs wired yet), the
 * request is a no-op that reports granted. Provided to common UI through
 * [LocalNotificationPermissionController].
 */
interface NotificationPermissionController {
    /**
     * Ensure the notification permission is granted, prompting once if it has not
     * been decided. Returns true if notifications may be posted. Safe to call from
     * a composition side effect; returns immediately where no runtime grant exists.
     */
    suspend fun ensurePermission(): Boolean
}

/**
 * Inert controller used as the [LocalNotificationPermissionController] default so
 * previews, tests, and iOS compose without a platform host. Always reports
 * granted; real prompting comes from the Android platform controller.
 */
object NoopNotificationPermissionController : NotificationPermissionController {
    override suspend fun ensurePermission(): Boolean = true
}

/** Provided by the Android `MainActivity`; defaults to a no-op elsewhere. */
val LocalNotificationPermissionController =
    staticCompositionLocalOf<NotificationPermissionController> { NoopNotificationPermissionController }
