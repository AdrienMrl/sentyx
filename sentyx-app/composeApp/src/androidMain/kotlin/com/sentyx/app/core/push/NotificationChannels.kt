package com.sentyx.app.core.push

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.Build

/**
 * The high-importance channel Sentry alerts are posted on. The id must match the
 * FCM payload's `channel_id` ("sentry_alerts") so backend-sent notification
 * messages surface on the same channel as locally-posted ones.
 */
object NotificationChannels {
    const val SENTRY_ALERTS_ID: String = "sentry_alerts"
    private const val SENTRY_ALERTS_NAME: String = "Sentry alerts"

    /**
     * Create the Sentry-alerts channel if it does not exist. Called on app start
     * and defensively before posting from the messaging service. No-op below
     * Android 8 (channels don't exist there).
     */
    fun ensureSentryAlerts(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        if (manager.getNotificationChannel(SENTRY_ALERTS_ID) != null) return
        manager.createNotificationChannel(
            NotificationChannel(
                SENTRY_ALERTS_ID,
                SENTRY_ALERTS_NAME,
                NotificationManager.IMPORTANCE_HIGH,
            ),
        )
    }
}
