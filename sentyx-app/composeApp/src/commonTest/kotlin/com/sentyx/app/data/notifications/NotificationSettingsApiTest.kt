package com.sentyx.app.data.notifications

import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.data.api.SentyxApiException
import com.sentyx.app.domain.model.MinThreatLevel
import io.ktor.client.HttpClient
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.client.request.HttpRequestData
import io.ktor.http.HttpHeaders
import io.ktor.http.HttpMethod
import io.ktor.http.HttpStatusCode
import io.ktor.http.content.TextContent
import io.ktor.http.headersOf
import io.ktor.serialization.kotlinx.json.json
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue

/**
 * MockEngine-driven tests for the push-token and notification-settings endpoints
 * on [SentyxApi], plus the [MinThreatLevel] wire mapping. No real backend is
 * contacted; requests are captured so paths, methods, and JSON bodies are asserted.
 */
class NotificationSettingsApiTest {

    private class Captured(val requests: MutableList<HttpRequestData> = mutableListOf())

    private fun api(
        status: HttpStatusCode,
        body: String,
        captured: Captured,
    ): SentyxApi {
        val engine = MockEngine { request ->
            captured.requests.add(request)
            respond(
                content = body,
                status = status,
                headers = headersOf(HttpHeaders.ContentType, "application/json"),
            )
        }
        val http = HttpClient(engine) {
            install(ContentNegotiation) { json(Json { ignoreUnknownKeys = true; encodeDefaults = true }) }
        }
        return SentyxApi(
            baseUrl = "https://srv.example/",
            accessToken = { "tok" },
            refreshToken = { "tok" },
            client = http,
        )
    }

    private fun bodyText(request: HttpRequestData): String = (request.body as TextContent).text

    @Test
    fun getNotificationSettingsParsesThreshold() = runTest {
        val cap = Captured()
        val dto = api(HttpStatusCode.OK, """{"min_threat_level":"medium"}""", cap)
            .getNotificationSettings()

        assertEquals("medium", dto.minThreatLevel)
        val req = cap.requests.single()
        assertEquals(HttpMethod.Get, req.method)
        assertEquals("/v1/me/notification-settings", req.url.encodedPath)
    }

    @Test
    fun putNotificationSettingsSendsThreshold() = runTest {
        val cap = Captured()
        api(HttpStatusCode.NoContent, "", cap).putNotificationSettings("high")

        val req = cap.requests.single()
        assertEquals(HttpMethod.Put, req.method)
        assertEquals("/v1/me/notification-settings", req.url.encodedPath)
        assertTrue(bodyText(req).contains("\"min_threat_level\":\"high\""), "was: ${bodyText(req)}")
    }

    @Test
    fun putNotificationSettingsThrowsOnError() = runTest {
        val cap = Captured()
        assertFailsWith<SentyxApiException> {
            api(HttpStatusCode.InternalServerError, "boom", cap).putNotificationSettings("low")
        }
    }

    @Test
    fun registerPushTokenSendsTokenAndPlatform() = runTest {
        val cap = Captured()
        api(HttpStatusCode.NoContent, "", cap).registerPushToken("fcm-abc", "android")

        val req = cap.requests.single()
        assertEquals(HttpMethod.Put, req.method)
        assertEquals("/v1/me/push-tokens", req.url.encodedPath)
        val text = bodyText(req)
        assertTrue(text.contains("\"token\":\"fcm-abc\""), "was: $text")
        assertTrue(text.contains("\"platform\":\"android\""), "was: $text")
    }

    @Test
    fun deletePushTokenEncodesTokenInPath() = runTest {
        val cap = Captured()
        api(HttpStatusCode.NoContent, "", cap).deletePushToken("a/b c")

        val req = cap.requests.single()
        assertEquals(HttpMethod.Delete, req.method)
        // Slash and space in the token are percent-encoded into a single path segment.
        assertEquals("/v1/me/push-tokens/a%2Fb%20c", req.url.encodedPath)
    }

    @Test
    fun deletePushTokenTreats404AsSuccess() = runTest {
        val cap = Captured()
        // Should not throw despite the 404.
        api(HttpStatusCode.NotFound, "", cap).deletePushToken("gone")
        assertEquals(1, cap.requests.size)
    }

    @Test
    fun minThreatLevelWireRoundTrips() {
        // UI ↔ wire mapping matches the server's none<low<medium<high semantics.
        assertEquals("off", MinThreatLevel.Off.wire)
        assertEquals("high", MinThreatLevel.HighOnly.wire)
        assertEquals("medium", MinThreatLevel.MediumAndUp.wire)
        assertEquals("low", MinThreatLevel.LowAndUp.wire)
        assertEquals("none", MinThreatLevel.Everything.wire)

        MinThreatLevel.entries.forEach { level ->
            assertEquals(level, MinThreatLevel.fromWire(level.wire))
        }
    }

    @Test
    fun minThreatLevelFromWireRejectsUnknown() {
        assertFailsWith<IllegalArgumentException> { MinThreatLevel.fromWire("critical") }
    }
}
