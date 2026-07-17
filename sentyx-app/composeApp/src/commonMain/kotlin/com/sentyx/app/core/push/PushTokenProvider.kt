package com.sentyx.app.core.push

/**
 * Platform bridge to the messaging token registered with the Sentyx backend.
 * Android is backed by Firebase Cloud Messaging; iOS returns null until APNs is
 * wired, so the token lifecycle becomes a no-op there.
 *
 * Construction must not perform blocking work — token retrieval is deferred to
 * [currentToken], which may suspend on the platform SDK.
 */
expect class PushTokenProvider() {
    /** Identifier sent to the server as `platform` ("android" / "ios"). */
    val platform: String

    /**
     * The current push token, or null if unavailable (iOS, or a messaging SDK
     * that has not initialised — e.g. a placeholder `google-services.json`). Never
     * throws; failures resolve to null so the caller simply skips registration.
     */
    suspend fun currentToken(): String?
}
