package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.SessionInfo
import com.sentyx.app.domain.model.UserProfile
import kotlinx.coroutines.flow.StateFlow

/** Account/session lifecycle against the Sentyx backend. */
interface AuthRepository {
    /** Null when signed out. */
    val profile: StateFlow<UserProfile?>

    val sessions: StateFlow<List<SessionInfo>>

    suspend fun signIn(email: String, password: String)
    suspend fun createAccount(name: String, email: String, password: String)
    suspend fun verifyEmail(code: String)
    suspend fun sendPasswordReset(email: String)
    suspend fun revokeSession(id: String)
    suspend fun signOut()
}
