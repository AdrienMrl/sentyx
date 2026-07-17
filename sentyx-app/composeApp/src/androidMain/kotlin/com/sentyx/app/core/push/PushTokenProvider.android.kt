package com.sentyx.app.core.push

import com.google.firebase.messaging.FirebaseMessaging
import kotlin.coroutines.resume
import kotlinx.coroutines.suspendCancellableCoroutine

/**
 * Android [PushTokenProvider] backed by Firebase Cloud Messaging. Resolves the
 * current registration token via the FCM Task API. Any failure (including an
 * uninitialised FirebaseApp when only the placeholder `google-services.json` is
 * present) resolves to null so push registration is skipped rather than crashing.
 */
actual class PushTokenProvider actual constructor() {
    actual val platform: String = "android"

    actual suspend fun currentToken(): String? =
        suspendCancellableCoroutine { cont ->
            try {
                FirebaseMessaging.getInstance().token
                    .addOnSuccessListener { token -> cont.resume(token) }
                    .addOnFailureListener { cont.resume(null) }
            } catch (e: Throwable) {
                // FirebaseApp not initialised (e.g. placeholder google-services.json).
                cont.resume(null)
            }
        }
}
