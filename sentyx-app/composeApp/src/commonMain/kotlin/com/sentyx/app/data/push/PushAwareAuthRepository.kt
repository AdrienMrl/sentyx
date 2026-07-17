package com.sentyx.app.data.push

import com.sentyx.app.domain.repository.AuthRepository

/**
 * [AuthRepository] decorator that deletes the user's push token on sign-out
 * BEFORE the underlying session is cleared (the delete is an authenticated call).
 * Every other operation delegates unchanged; sign-in registration is handled
 * reactively by [PushController.start] observing the session flow.
 */
class PushAwareAuthRepository(
    private val delegate: AuthRepository,
    private val push: PushController,
) : AuthRepository by delegate {

    override suspend fun signOut() {
        push.unregisterBeforeSignOut()
        delegate.signOut()
    }
}
