package com.sentyx.app.data.api

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
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * Thin Ktor client for the Sentyx cloud backend. Uses the platform default
 * engine (OkHttp on Android, Darwin on iOS — both are on the classpath) and
 * authenticates with the operator bearer token.
 */
class SentyxApi(
    baseUrl: String,
    private val operatorToken: String,
    private val client: HttpClient = defaultClient(),
) {
    /** Base URL without a trailing slash so path concatenation is unambiguous. */
    private val base: String = baseUrl.trimEnd('/')

    /**
     * Register a freshly paired Pi with the backend and return the per-device
     * ingest token it should use. `POST {base}/v1/devices` with the operator
     * bearer token; a non-2xx response throws with the status and body.
     */
    suspend fun registerDevice(deviceId: String, name: String): String {
        val response: HttpResponse = client.post("$base/v1/devices") {
            header(HttpHeaders.Authorization, "Bearer $operatorToken")
            contentType(ContentType.Application.Json)
            setBody(RegisterDeviceRequest(deviceId = deviceId, name = name))
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Device registration failed (${response.status}): ${response.bodyAsText()}",
            )
        }
        return response.body<RegisterDeviceResponse>().token
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

/** Raised when the backend returns a non-success response. */
class SentyxApiException(message: String) : Exception(message)

@Serializable
private data class RegisterDeviceRequest(val deviceId: String, val name: String)

@Serializable
private data class RegisterDeviceResponse(val deviceId: String, val token: String)
