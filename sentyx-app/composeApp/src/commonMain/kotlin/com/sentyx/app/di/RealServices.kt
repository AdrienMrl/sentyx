package com.sentyx.app.di

import com.sentyx.app.core.storage.KeyValueStore
import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.data.auth.SupabaseAuthClient
import com.sentyx.app.data.auth.SupabaseSessionManager

/**
 * Process-wide singletons for the real (non-demo) auth stack.
 *
 * Exactly one [SupabaseSessionManager] may exist per process: Supabase refresh
 * tokens are single-use, so two managers over the same persisted session would
 * race — whichever refreshes second holds a dead refresh token and gets signed
 * out. [RealAppContainer] (UI) and the Android FCM service (which can run
 * before any UI exists) must both go through these instances.
 *
 * Everything is lazy and reads [BuildFlavor.requireRealConfig], so merely
 * loading this object is safe in the demo flavor; touching a property without
 * real config fails fast (no implicit defaults).
 */
object RealServices {
    val authClient: SupabaseAuthClient by lazy {
        val config = BuildFlavor.requireRealConfig()
        SupabaseAuthClient(supabaseUrl = config.supabaseUrl, anonKey = config.supabaseAnonKey)
    }

    val sessionManager: SupabaseSessionManager by lazy {
        SupabaseSessionManager(authClient, KeyValueStore())
    }

    val api: SentyxApi by lazy {
        SentyxApi(
            baseUrl = BuildFlavor.requireRealConfig().serverBaseUrl,
            accessToken = { sessionManager.validAccessToken() },
            refreshToken = { sessionManager.forceRefresh() },
        )
    }
}
