package com.sentyx.app.data.auth

import com.sentyx.app.domain.model.SessionInfo
import com.sentyx.app.domain.model.UserProfile
import com.sentyx.app.domain.repository.AuthRepository
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

/**
 * Server-backed [AuthRepository] over Supabase Auth (GoTrue). Email/password
 * only; email verification is disabled on the project, so [createAccount]
 * establishes a session immediately and [verifyEmail] is a no-op.
 *
 * The session (tokens, expiry, user identity) is owned by [sessionManager],
 * which persists it and refreshes it; this repository maps that session into the
 * [UserProfile] the UI observes. [profile] is seeded synchronously from any
 * restored session so the app's startup route reflects the signed-in state
 * without a frame of "signed out".
 */
class SupabaseAuthRepository(
    scope: CoroutineScope,
    private val client: SupabaseAuthClient,
    private val sessionManager: SupabaseSessionManager,
) : AuthRepository {

    private val _profile = MutableStateFlow(sessionManager.current()?.toProfile())
    override val profile: StateFlow<UserProfile?> = _profile.asStateFlow()

    // Multi-device session listing isn't exposed to the GoTrue client SDK, so the
    // "Signed-in devices" screen stays on demo data (see task note). Sign-out and
    // the real profile above are live.
    private val _sessions = MutableStateFlow(DEMO_SESSIONS)
    override val sessions: StateFlow<List<SessionInfo>> = _sessions.asStateFlow()

    init {
        // Keep the profile in lockstep with the session, including the implicit
        // sign-out that a failed refresh performs (session → null → profile null).
        scope.launch {
            sessionManager.session.collect { s -> _profile.value = s?.toProfile() }
        }
    }

    override suspend fun signIn(email: String, password: String) {
        val session = client.signInWithPassword(email.trim(), password)
        sessionManager.save(session)
    }

    override suspend fun createAccount(name: String, email: String, password: String) {
        val session = client.signUp(email.trim(), password, name.trim())
        sessionManager.save(session)
    }

    /** No-op: email verification is disabled, so a created account is already active. */
    override suspend fun verifyEmail(code: String) {
        // Intentionally empty.
    }

    override suspend fun sendPasswordReset(email: String) {
        client.recover(email.trim())
    }

    override suspend fun revokeSession(id: String) {
        // Only the demo session rows are revocable client-side; drop the row.
        _sessions.value = _sessions.value.filterNot { it.id == id }
    }

    override suspend fun signOut() {
        val token = sessionManager.current()?.accessToken
        if (token != null) {
            try {
                client.logout(token)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Throwable) {
                // Best-effort server revoke; local state is cleared regardless.
            }
        }
        sessionManager.clearLocal()
    }

    private companion object {
        val DEMO_SESSIONS = listOf(
            SessionInfo(
                id = "s1",
                deviceName = "This device",
                locationLine = "active now",
                isCurrent = true,
                icon = "📱",
            ),
        )
    }
}

private fun SupabaseSession.toProfile(): UserProfile {
    val display = name?.takeIf { it.isNotBlank() } ?: email.substringBefore('@')
    return UserProfile(
        name = display,
        email = email,
        initials = initialsOf(display),
        // No real 2FA yet; the Security screen's toggle is local/demo state.
        twoFactorEnabled = false,
    )
}

private fun initialsOf(name: String): String =
    name.trim().split(Regex("\\s+"))
        .take(2)
        .mapNotNull { it.firstOrNull()?.uppercaseChar() }
        .joinToString("")
        .ifEmpty { name.take(2).uppercase() }
        .ifEmpty { "?" }
