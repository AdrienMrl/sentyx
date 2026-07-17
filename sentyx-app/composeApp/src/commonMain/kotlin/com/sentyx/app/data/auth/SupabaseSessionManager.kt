package com.sentyx.app.data.auth

import com.sentyx.app.core.storage.KeyValueStore
import com.sentyx.app.core.storage.StorageKeys
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/**
 * Owns the current Supabase session: persists it across launches (via
 * [KeyValueStore]), hands out a valid access token to API callers, and refreshes
 * proactively (single-flight) when the token is expired or near expiry. A refresh
 * failure clears the session — the user is effectively signed out and callers
 * observe [session] going null.
 *
 * Persistence is synchronous string storage, so the session restored in the
 * constructor is available immediately (the app's startup route decision reads
 * it without waiting on a coroutine).
 */
class SupabaseSessionManager(
    private val client: SupabaseAuthClient,
    private val store: KeyValueStore,
) {
    private val refreshLock = Mutex()

    private val _session = MutableStateFlow(loadPersisted())
    val session: StateFlow<SupabaseSession?> = _session.asStateFlow()

    fun current(): SupabaseSession? = _session.value

    /** Persist and publish a freshly obtained session (sign-in / sign-up / refresh). */
    fun save(session: SupabaseSession) {
        persist(session)
        _session.value = session
    }

    /** Drop the session locally (sign-out, or an unrecoverable refresh failure). */
    fun clearLocal() {
        store.remove(StorageKeys.AUTH_ACCESS_TOKEN)
        store.remove(StorageKeys.AUTH_REFRESH_TOKEN)
        store.remove(StorageKeys.AUTH_EXPIRES_AT)
        store.remove(StorageKeys.AUTH_USER_ID)
        store.remove(StorageKeys.AUTH_USER_EMAIL)
        store.remove(StorageKeys.AUTH_USER_NAME)
        _session.value = null
    }

    /**
     * A currently-valid access token, refreshing first if the current one is
     * expired/near expiry. Null when signed out (or when a needed refresh failed,
     * which also clears the session).
     */
    suspend fun validAccessToken(): String? {
        val s = _session.value ?: return null
        if (!s.isNearExpiry()) return s.accessToken
        return refreshLocked(staleToken = s.accessToken)
    }

    /**
     * Force a refresh regardless of expiry — used to recover from a server 401.
     * Returns the new access token, or null if refresh failed (session cleared).
     */
    suspend fun forceRefresh(): String? {
        val s = _session.value ?: return null
        return refreshLocked(staleToken = s.accessToken)
    }

    /**
     * Single-flight refresh: whoever holds the lock refreshes; everyone else
     * re-checks on entry and reuses the just-refreshed token instead of hitting
     * GoTrue again. [staleToken] is the token the caller deemed unusable — if the
     * session already holds a different one, another caller refreshed first and
     * that token is returned as-is.
     */
    private suspend fun refreshLocked(staleToken: String): String? = refreshLock.withLock {
        val cur = _session.value ?: return null
        if (cur.accessToken != staleToken) return cur.accessToken
        try {
            val fresh = client.refresh(cur.refreshToken)
            // GoTrue may omit user_metadata on refresh; keep the known display name.
            val merged = if (fresh.name.isNullOrBlank()) fresh.copy(name = cur.name) else fresh
            save(merged)
            merged.accessToken
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            clearLocal()
            null
        }
    }

    // ---- Persistence --------------------------------------------------------

    private fun persist(session: SupabaseSession) {
        store.putString(StorageKeys.AUTH_ACCESS_TOKEN, session.accessToken)
        store.putString(StorageKeys.AUTH_REFRESH_TOKEN, session.refreshToken)
        store.putString(StorageKeys.AUTH_EXPIRES_AT, session.expiresAtEpochSeconds.toString())
        store.putString(StorageKeys.AUTH_USER_ID, session.userId)
        store.putString(StorageKeys.AUTH_USER_EMAIL, session.email)
        session.name?.let { store.putString(StorageKeys.AUTH_USER_NAME, it) }
            ?: store.remove(StorageKeys.AUTH_USER_NAME)
    }

    private fun loadPersisted(): SupabaseSession? {
        val access = store.getString(StorageKeys.AUTH_ACCESS_TOKEN) ?: return null
        val refresh = store.getString(StorageKeys.AUTH_REFRESH_TOKEN) ?: return null
        val expiresAt = store.getString(StorageKeys.AUTH_EXPIRES_AT)?.toLongOrNull() ?: return null
        val userId = store.getString(StorageKeys.AUTH_USER_ID) ?: return null
        val email = store.getString(StorageKeys.AUTH_USER_EMAIL) ?: return null
        return SupabaseSession(
            accessToken = access,
            refreshToken = refresh,
            expiresAtEpochSeconds = expiresAt,
            userId = userId,
            email = email,
            name = store.getString(StorageKeys.AUTH_USER_NAME),
        )
    }
}
