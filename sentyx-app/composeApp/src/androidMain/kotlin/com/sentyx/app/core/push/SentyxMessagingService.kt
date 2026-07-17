package com.sentyx.app.core.push

import android.app.Notification
import android.app.PendingIntent
import android.app.NotificationManager
import android.content.Intent
import android.os.Build
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import com.sentyx.app.core.storage.SentyxAppContext
import com.sentyx.app.di.RealServices
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout

/**
 * Firebase Cloud Messaging entry point.
 *
 * - [onNewToken] registers a refreshed token with the backend, but only if a
 *   session is persisted; a refresh that arrives while signed out is skipped
 *   silently. It uses the process-wide [RealServices] auth stack — Supabase
 *   refresh tokens are single-use, so a second session manager over the same
 *   persisted session would race the UI's and sign the user out.
 * - [onMessageReceived] fires for data messages and for notification messages
 *   while the app is foregrounded (backgrounded notification messages are drawn
 *   by the system automatically). It posts a local notification on the
 *   [NotificationChannels.SENTRY_ALERTS_ID] channel with the payload title/body,
 *   carrying `event_id` as an extra so a tap can be deep-linked later.
 *
 * Push is inert until a real `google-services.json` replaces the placeholder;
 * with the placeholder, FCM never delivers, so these callbacks simply never run.
 */
class SentyxMessagingService : FirebaseMessagingService() {

    override fun onNewToken(token: String) {
        SentyxAppContext.init(applicationContext)
        val api = try {
            // Touching RealServices resolves the real config; demo flavor /
            // missing config throws and there is nothing to register against.
            if (RealServices.sessionManager.current() == null) return // signed out
            RealServices.api
        } catch (e: Throwable) {
            return
        }
        runBlocking {
            try {
                withTimeout(REGISTER_TIMEOUT_MS) { api.registerPushToken(token, "android") }
            } catch (e: Throwable) {
                // Best-effort; the app re-registers on its next foreground.
            }
        }
    }

    override fun onMessageReceived(message: RemoteMessage) {
        NotificationChannels.ensureSentryAlerts(this)

        val data = message.data
        val notif = message.notification
        val title = notif?.title ?: "Sentry alert"
        val body = notif?.body ?: bodyFromData(data)
        val eventId = data["event_id"]

        val manager = getSystemService(NotificationManager::class.java) ?: return
        manager.notify(eventId?.hashCode() ?: message.messageId.hashCode(), buildNotification(title, body, eventId))
    }

    private fun buildNotification(title: String, body: String, eventId: String?): Notification {
        val launch = packageManager.getLaunchIntentForPackage(packageName)?.apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
            if (eventId != null) putExtra(EXTRA_EVENT_ID, eventId)
        }
        val pending = launch?.let {
            PendingIntent.getActivity(this, eventId?.hashCode() ?: 0, it, pendingIntentFlags())
        }

        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, NotificationChannels.SENTRY_ALERTS_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }
        return builder
            .setContentTitle(title)
            .setContentText(body)
            .setSmallIcon(applicationInfo.icon.takeIf { it != 0 } ?: android.R.drawable.ic_dialog_info)
            .setAutoCancel(true)
            .apply { if (pending != null) setContentIntent(pending) }
            .build()
    }

    private fun bodyFromData(data: Map<String, String>): String {
        val camera = data["camera"]
        val city = data["city"]
        return listOfNotNull(camera, city).joinToString(" · ").ifEmpty { "New Sentry event" }
    }

    private fun pendingIntentFlags(): Int =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        } else {
            PendingIntent.FLAG_UPDATE_CURRENT
        }

    companion object {
        /** Extra key on the launcher intent carrying the tapped event's id. */
        const val EXTRA_EVENT_ID: String = "event_id"
        private const val REGISTER_TIMEOUT_MS = 15_000L
    }
}
