package com.sentyx.app.data.demo

import com.sentyx.app.domain.model.SessionInfo
import com.sentyx.app.domain.model.UserProfile
import com.sentyx.app.domain.repository.AuthRepository
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Demo [AuthRepository]. Starts signed out; sign-in or create-account +
 * verify-email produce the canonical Alex Rivera profile after a short fake
 * network delay.
 */
class DemoAuthRepository : AuthRepository {

    private val _profile = MutableStateFlow<UserProfile?>(null)
    override val profile: StateFlow<UserProfile?> = _profile

    private val _sessions = MutableStateFlow(
        listOf(
            SessionInfo(
                id = "s1",
                deviceName = "iPhone 15 · this device",
                locationLine = "Los Angeles · active now",
                isCurrent = true,
                icon = "📱",
            ),
            SessionInfo(
                id = "s2",
                deviceName = "MacBook Pro",
                locationLine = "Los Angeles · 2 days ago",
                isCurrent = false,
                icon = "💻",
            ),
            SessionInfo(
                id = "s3",
                deviceName = "iPad",
                locationLine = "Home · 1 week ago",
                isCurrent = false,
                icon = "📱",
            ),
        ),
    )
    override val sessions: StateFlow<List<SessionInfo>> = _sessions

    /** Set by [createAccount], consumed by [verifyEmail]. */
    private var pending: UserProfile? = null

    override suspend fun signIn(email: String, password: String) {
        delay(700)
        _profile.value = PROFILE
    }

    override suspend fun createAccount(name: String, email: String, password: String) {
        delay(700)
        pending = UserProfile(
            name = name,
            email = email,
            initials = initialsOf(name),
            twoFactorEnabled = true,
        )
    }

    override suspend fun verifyEmail(code: String) {
        delay(700)
        _profile.value = pending ?: PROFILE
        pending = null
    }

    override suspend fun sendPasswordReset(email: String) {
        delay(500)
    }

    override suspend fun revokeSession(id: String) {
        _sessions.value = _sessions.value.filterNot { it.id == id }
    }

    override suspend fun signOut() {
        _profile.value = null
    }

    private fun initialsOf(name: String): String =
        name.trim().split(Regex("\\s+"))
            .take(2)
            .mapNotNull { it.firstOrNull()?.uppercaseChar() }
            .joinToString("")
            .ifEmpty { "AR" }

    private companion object {
        val PROFILE = UserProfile(
            name = "Alex Rivera",
            email = "alex@example.com",
            initials = "AR",
            twoFactorEnabled = true,
        )
    }
}
