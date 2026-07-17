package com.sentyx.app.data.auth

import io.ktor.client.HttpClient
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.http.HttpStatusCode
import io.ktor.http.headersOf
import io.ktor.http.HttpHeaders
import io.ktor.serialization.kotlinx.json.json
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue

/**
 * Parsing and error-mapping tests for [SupabaseAuthClient], driven by a Ktor
 * MockEngine so no real GoTrue endpoint is contacted. Covers the happy-path
 * session parse (tokens + user_metadata name), the "expires_in derives expiry"
 * path, and the two error bodies the UI must translate (bad credentials, user
 * already exists).
 */
class SupabaseAuthClientTest {

    private fun clientReturning(
        status: HttpStatusCode,
        body: String,
        capture: MutableList<String>? = null,
    ): SupabaseAuthClient {
        val engine = MockEngine { request ->
            capture?.add(request.url.encodedPath + (request.url.encodedQuery.let { if (it.isEmpty()) "" else "?$it" }))
            respond(
                content = body,
                status = status,
                headers = headersOf(HttpHeaders.ContentType, "application/json"),
            )
        }
        val http = HttpClient(engine) {
            install(ContentNegotiation) {
                json(Json { ignoreUnknownKeys = true; encodeDefaults = true })
            }
        }
        return SupabaseAuthClient(
            supabaseUrl = "https://proj.supabase.co",
            anonKey = "anon-key",
            client = http,
        )
    }

    @Test
    fun signInParsesSessionAndName() = runTest {
        val body = """
            {
              "access_token": "acc-123",
              "refresh_token": "ref-456",
              "expires_at": 2000000000,
              "user": { "id": "u-1", "email": "a@b.com",
                        "user_metadata": { "full_name": "Alex Rivera" } }
            }
        """.trimIndent()
        val session = clientReturning(HttpStatusCode.OK, body)
            .signInWithPassword("a@b.com", "pw")

        assertEquals("acc-123", session.accessToken)
        assertEquals("ref-456", session.refreshToken)
        assertEquals(2000000000L, session.expiresAtEpochSeconds)
        assertEquals("u-1", session.userId)
        assertEquals("a@b.com", session.email)
        assertEquals("Alex Rivera", session.name)
    }

    @Test
    fun signUpDerivesExpiryFromExpiresIn() = runTest {
        val body = """
            {
              "access_token": "acc",
              "refresh_token": "ref",
              "expires_in": 3600,
              "user": { "id": "u", "email": "a@b.com" }
            }
        """.trimIndent()
        val session = clientReturning(HttpStatusCode.OK, body)
            .signUp("a@b.com", "pw", "Alex")

        // Expiry was derived (no absolute expires_at), so it is comfortably in
        // the future rather than zero, and the token is not immediately "near".
        assertTrue(session.expiresAtEpochSeconds > 0)
        assertTrue(!session.isNearExpiry(nowSeconds = session.expiresAtEpochSeconds - 3000))
        assertEquals(null, session.name)
    }

    @Test
    fun invalidCredentialsMapsToFriendlyMessage() = runTest {
        val body = """{"code":400,"error_code":"invalid_credentials","msg":"Invalid login credentials"}"""
        val ex = assertFailsWith<AuthException> {
            clientReturning(HttpStatusCode.BadRequest, body).signInWithPassword("a@b.com", "pw")
        }
        assertEquals("Incorrect email or password.", ex.message)
    }

    @Test
    fun userAlreadyExistsMapsToFriendlyMessage() = runTest {
        val body = """{"code":422,"msg":"User already registered"}"""
        val ex = assertFailsWith<AuthException> {
            clientReturning(HttpStatusCode.UnprocessableEntity, body).signUp("a@b.com", "pw", "Alex")
        }
        assertTrue(ex.message!!.contains("already exists"), "was: ${ex.message}")
    }

    @Test
    fun refreshHitsRefreshTokenGrant() = runTest {
        val paths = mutableListOf<String>()
        val body = """
            {"access_token":"a2","refresh_token":"r2","expires_at":2000000000,
             "user":{"id":"u","email":"a@b.com"}}
        """.trimIndent()
        val session = clientReturning(HttpStatusCode.OK, body, paths).refresh("old-refresh")

        assertEquals("a2", session.accessToken)
        assertTrue(paths.single().contains("grant_type=refresh_token"), "was: $paths")
    }

    @Test
    fun isNearExpiryRespectsSkew() {
        val s = SupabaseSession(
            accessToken = "a", refreshToken = "r",
            expiresAtEpochSeconds = 1000, userId = "u", email = "e", name = null,
        )
        // 30s before expiry with a 60s skew → considered near-expiry.
        assertTrue(s.isNearExpiry(nowSeconds = 970, skewSeconds = 60))
        // 120s before expiry → not near.
        assertTrue(!s.isNearExpiry(nowSeconds = 880, skewSeconds = 60))
    }
}
