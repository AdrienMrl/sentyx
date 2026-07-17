package com.sentyx.app.data.notifications

import com.sentyx.app.data.api.SentyxApi
import com.sentyx.app.data.api.SentyxApiException
import com.sentyx.app.domain.model.MinThreatLevel
import io.ktor.client.HttpClient
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.http.HttpHeaders
import io.ktor.http.HttpMethod
import io.ktor.http.HttpStatusCode
import io.ktor.http.headersOf
import io.ktor.serialization.kotlinx.json.json
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

/**
 * Behavioural tests for [RealNotificationSettingsRepository]: it loads the
 * threshold on construction and applies changes optimistically, rolling back on a
 * failed `PUT`.
 */
class RealNotificationSettingsRepositoryTest {

    private fun apiWith(getLevel: String, putStatus: HttpStatusCode): SentyxApi {
        val engine = MockEngine { request ->
            when (request.method) {
                HttpMethod.Get -> respond(
                    content = """{"min_threat_level":"$getLevel"}""",
                    status = HttpStatusCode.OK,
                    headers = headersOf(HttpHeaders.ContentType, "application/json"),
                )
                else -> respond(
                    content = "",
                    status = putStatus,
                    headers = headersOf(HttpHeaders.ContentType, "application/json"),
                )
            }
        }
        val http = HttpClient(engine) {
            install(ContentNegotiation) { json(Json { ignoreUnknownKeys = true; encodeDefaults = true }) }
        }
        return SentyxApi(
            baseUrl = "https://srv.example",
            accessToken = { "tok" },
            refreshToken = { "tok" },
            client = http,
        )
    }

    @Test
    fun loadsThresholdFromServerOnInit() = runTest {
        val repo = RealNotificationSettingsRepository(backgroundScope, apiWith("low", HttpStatusCode.NoContent))
        // The init GET completes on the client's real dispatcher; await the emission.
        assertEquals(MinThreatLevel.LowAndUp, repo.minThreatLevel.first { it == MinThreatLevel.LowAndUp })
    }

    @Test
    fun setMinThreatLevelPersistsOnSuccess() = runTest {
        val repo = RealNotificationSettingsRepository(backgroundScope, apiWith("low", HttpStatusCode.NoContent))
        repo.minThreatLevel.first { it == MinThreatLevel.LowAndUp } // wait for load

        repo.setMinThreatLevel(MinThreatLevel.HighOnly)
        assertEquals(MinThreatLevel.HighOnly, repo.minThreatLevel.value)
    }

    @Test
    fun setMinThreatLevelRollsBackOnFailure() = runTest {
        val repo = RealNotificationSettingsRepository(backgroundScope, apiWith("low", HttpStatusCode.InternalServerError))
        repo.minThreatLevel.first { it == MinThreatLevel.LowAndUp } // wait for load

        assertFailsWith<SentyxApiException> {
            repo.setMinThreatLevel(MinThreatLevel.Everything)
        }
        // Optimistic value reverted to the loaded one.
        assertEquals(MinThreatLevel.LowAndUp, repo.minThreatLevel.value)
    }
}
