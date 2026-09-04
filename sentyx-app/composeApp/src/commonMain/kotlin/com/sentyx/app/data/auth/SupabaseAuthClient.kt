package com.sentyx.app.data.auth

import io.ktor.client.HttpClient
import io.ktor.client.call.body
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import io.ktor.serialization.kotlinx.json.json
import kotlin.time.Clock
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive

/**
 * Plain Ktor client for Supabase Auth (GoTrue) REST. Email/password only, with
 * email verification assumed DISABLED on the project — so `signup` returns a
 * usable session immediately (no confirm step). Every call sends the project
 * `apikey`; token/logout calls also carry the bearer where GoTrue requires it.
 *
 * The client is stateless: it returns a parsed [SupabaseSession] (or throws an
 * [AuthException] carrying a user-facing message) and never persists anything —
 * session storage/refresh scheduling lives in [SupabaseSessionManager].
 */
class SupabaseAuthClient(
    supabaseUrl: String,
    private val anonKey: String,
    private val client: HttpClient = defaultClient(),
) {
    /** Base URL without a trailing slash so path concatenation is unambiguous. */
    private val base: String = supabaseUrl.trimEnd('/')

    /**
     * Create a new account. `POST /auth/v1/signup` with the display name in
     * `data` (GoTrue user_metadata). With email confirmation disabled the
     * response contains a full session; if it doesn't (confirmation still on),
     * this throws so the caller doesn't silently believe it signed in.
     */
    suspend fun signUp(email: String, password: String, name: String): SupabaseSession {
        val response = client.post("$base/auth/v1/signup") {
            header("apikey", anonKey)
            contentType(ContentType.Application.Json)
            setBody(SignUpRequest(email = email, password = password, data = UserData(fullName = name)))
        }
        val body = response.bodyAsText()
        if (!response.status.isSuccess()) throw authError(response, body)
        val token = decode(body)
        return token.toSession()
            ?: throw AuthException(
                "Account created but no session was returned — email confirmation may still " +
                    "be enabled on the Supabase project. Disable it to sign in without a code.",
            )
    }

    /** Sign in with email + password. `POST /auth/v1/token?grant_type=password`. */
    suspend fun signInWithPassword(email: String, password: String): SupabaseSession {
        val response = client.post("$base/auth/v1/token?grant_type=password") {
            header("apikey", anonKey)
            contentType(ContentType.Application.Json)
            setBody(PasswordGrantRequest(email = email, password = password))
        }
        val body = response.bodyAsText()
        if (!response.status.isSuccess()) throw authError(response, body)
        return decode(body).toSession()
            ?: throw AuthException("Sign-in returned no session. Please try again.")
    }

    /** Exchange a refresh token for a fresh session. `grant_type=refresh_token`. */
    suspend fun refresh(refreshToken: String): SupabaseSession {
        val response = client.post("$base/auth/v1/token?grant_type=refresh_token") {
            header("apikey", anonKey)
            contentType(ContentType.Application.Json)
            setBody(RefreshGrantRequest(refreshToken = refreshToken))
        }
        val body = response.bodyAsText()
        if (!response.status.isSuccess()) throw authError(response, body)
        return decode(body).toSession()
            ?: throw AuthException("Token refresh returned no session.")
    }

    /**
     * Revoke the given access token server-side. `POST /auth/v1/logout` with the
     * bearer. Best-effort: the caller clears local state regardless, so any
     * network failure here is swallowed by the caller.
     */
    suspend fun logout(accessToken: String) {
        client.post("$base/auth/v1/logout") {
            header("apikey", anonKey)
            header(HttpHeaders.Authorization, "Bearer $accessToken")
        }
    }

    /** Send a password-reset email. `POST /auth/v1/recover`. */
    suspend fun recover(email: String) {
        val response = client.post("$base/auth/v1/recover") {
            header("apikey", anonKey)
            contentType(ContentType.Application.Json)
            setBody(RecoverRequest(email = email))
        }
        if (!response.status.isSuccess()) throw authError(response, response.bodyAsText())
    }

    private fun decode(body: String): TokenResponse = jsonCodec.decodeFromString(body)

    /**
     * Map a GoTrue error response to a friendly [AuthException]. GoTrue has used
     * a few shapes over versions: `{error, error_description}`,
     * `{code, error_code, msg}`, and `{message}` — try each, then fall back to a
     * status-based message keyed off the well-known cases.
     */
    private fun authError(response: HttpResponse, body: String): AuthException {
        val err = runCatching { jsonCodec.decodeFromString<GoTrueError>(body) }.getOrNull()
        val code = err?.errorCode ?: err?.error
        val raw = err?.msg ?: err?.errorDescription ?: err?.message
        val friendly = when {
            code == "invalid_credentials" || raw?.contains("Invalid login", ignoreCase = true) == true ->
                "Incorrect email or password."
            code == "user_already_exists" || raw?.contains("already registered", ignoreCase = true) == true ->
                "An account with that email already exists. Try signing in instead."
            code == "weak_password" || raw?.contains("password", ignoreCase = true) == true &&
                raw.contains("least", ignoreCase = true) ->
                "Password is too weak. Use at least 6 characters."
            code == "email_address_invalid" || raw?.contains("valid email", ignoreCase = true) == true ->
                "Please enter a valid email address."
            !raw.isNullOrBlank() -> raw
            response.status.value == 401 || response.status.value == 403 -> "Not authorized."
            else -> "Authentication failed (${response.status})."
        }
        return AuthException(friendly, httpStatus = response.status.value, errorCode = code)
    }

    companion object {
        private val jsonCodec = Json {
            ignoreUnknownKeys = true
            encodeDefaults = true
        }

        private fun defaultClient(): HttpClient = HttpClient {
            install(ContentNegotiation) { json(jsonCodec) }
        }
    }
}

/** A parsed, ready-to-persist GoTrue session. */
data class SupabaseSession(
    val accessToken: String,
    val refreshToken: String,
    /** Access-token expiry as unix epoch seconds. */
    val expiresAtEpochSeconds: Long,
    val userId: String,
    val email: String,
    /** Display name from user_metadata, null if unset. */
    val name: String?,
) {
    /**
     * True when the access token has expired or is within [skewSeconds] of it,
     * so callers refresh proactively rather than firing a request that 401s.
     */
    fun isNearExpiry(
        nowSeconds: Long = Clock.System.now().epochSeconds,
        skewSeconds: Long = 60,
    ): Boolean = nowSeconds >= expiresAtEpochSeconds - skewSeconds
}

/**
 * Raised by [SupabaseAuthClient] with a message safe to show the user.
 * [httpStatus]/[errorCode] carry the raw GoTrue response so callers can tell a
 * definitive rejection apart from a transient server failure; both are null for
 * errors that never got a GoTrue response (e.g. a 2xx with no session in it).
 */
class AuthException(
    message: String,
    val httpStatus: Int? = null,
    val errorCode: String? = null,
) : Exception(message) {
    /**
     * True when GoTrue definitively rejected the request (4xx), meaning a retry
     * with the same credentials cannot succeed. 408 (timeout) and 429
     * (rate-limit) are transient despite being 4xx.
     */
    val isDefinitiveRejection: Boolean
        get() = httpStatus != null && httpStatus in 400..499 &&
            httpStatus != 408 && httpStatus != 429
}

// ---- Wire DTOs ----------------------------------------------------------------

@Serializable
private data class SignUpRequest(
    val email: String,
    val password: String,
    val data: UserData,
)

@Serializable
private data class UserData(@SerialName("full_name") val fullName: String)

@Serializable
private data class PasswordGrantRequest(val email: String, val password: String)

@Serializable
private data class RefreshGrantRequest(@SerialName("refresh_token") val refreshToken: String)

@Serializable
private data class RecoverRequest(val email: String)

@Serializable
private data class GoTrueError(
    val error: String? = null,
    @SerialName("error_description") val errorDescription: String? = null,
    @SerialName("error_code") val errorCode: String? = null,
    val msg: String? = null,
    val message: String? = null,
)

@Serializable
private data class TokenResponse(
    @SerialName("access_token") val accessToken: String? = null,
    @SerialName("refresh_token") val refreshToken: String? = null,
    @SerialName("expires_in") val expiresIn: Long? = null,
    @SerialName("expires_at") val expiresAt: Long? = null,
    val user: GoTrueUser? = null,
) {
    /**
     * Build a [SupabaseSession], or null when this response carries no tokens
     * (e.g. a signup awaiting email confirmation). Expiry prefers the absolute
     * `expires_at`; otherwise it is derived from `expires_in` against the clock.
     */
    fun toSession(): SupabaseSession? {
        val access = accessToken ?: return null
        val refresh = refreshToken ?: return null
        val expiry = expiresAt
            ?: (Clock.System.now().epochSeconds + (expiresIn ?: 3600))
        return SupabaseSession(
            accessToken = access,
            refreshToken = refresh,
            expiresAtEpochSeconds = expiry,
            userId = user?.id.orEmpty(),
            email = user?.email.orEmpty(),
            name = user?.metadata?.displayName(),
        )
    }
}

@Serializable
private data class GoTrueUser(
    val id: String? = null,
    val email: String? = null,
    @SerialName("user_metadata") val metadata: JsonObject? = null,
)

/** Pull a display name out of freeform user_metadata (full_name / name). */
private fun JsonObject.displayName(): String? {
    fun str(key: String): String? =
        this[key]?.let { runCatching { it.jsonPrimitive.content }.getOrNull() }
            ?.takeIf { it.isNotBlank() }
    return str("full_name") ?: str("name")
}
