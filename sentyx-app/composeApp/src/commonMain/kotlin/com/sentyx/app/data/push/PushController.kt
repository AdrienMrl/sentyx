package com.sentyx.app.data.push

import com.sentyx.app.core.push.PushTokenProvider
import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.data.auth.SupabaseSession
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch

/**
 * Coordinates the push-token lifecycle against the backend. Registration is
 * driven off the auth [session] flow: whenever it becomes non-null (sign-in, or
 * app start with a restored session) the current token is fetched and upserted
 * via `PUT /v1/me/push-tokens`.
 *
 * Deletion is not reactive — the token must be removed while the session is still
 * valid, so [unregisterBeforeSignOut] is called explicitly from the sign-out path
 * (see `PushAwareAuthRepository`) before the session is cleared.
 *
 * On iOS the [tokenProvider] yields null, so every operation is a silent no-op.
 */
class PushController(
    private val scope: CoroutineScope,
    private val api: SentyxApi,
    private val session: StateFlow<SupabaseSession?>,
    private val tokenProvider: PushTokenProvider,
) {
    /** The token last successfully registered, so sign-out can delete the right one. */
    private var registeredToken: String? = null

    /** Begin observing the session; registers a token each time the user is signed in. */
    fun start() {
        scope.launch {
            session
                .map { it != null }
                .distinctUntilChanged()
                .collect { signedIn ->
                    if (signedIn) registerToken() else registeredToken = null
                }
        }
    }

    private suspend fun registerToken() {
        val token = tokenProvider.currentToken() ?: return
        try {
            api.registerPushToken(token, tokenProvider.platform)
            registeredToken = token
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            // Best-effort: registration is retried on the next sign-in / app start.
        }
    }

    /**
     * Delete the current push token while the session is still authenticated.
     * Call BEFORE clearing the session on sign-out. Silent no-op when there is no
     * token (iOS, or never registered).
     */
    suspend fun unregisterBeforeSignOut() {
        val token = registeredToken ?: tokenProvider.currentToken() ?: return
        try {
            api.deletePushToken(token)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            // Best-effort; the local session is cleared regardless.
        }
        registeredToken = null
    }
}
