package com.sentyx.app.core.push

/**
 * iOS [PushTokenProvider] stub. APNs is not wired yet, so there is no token and
 * the token lifecycle (registration / deletion) is a no-op on iOS.
 */
actual class PushTokenProvider actual constructor() {
    actual val platform: String = "ios"

    actual suspend fun currentToken(): String? = null
}
