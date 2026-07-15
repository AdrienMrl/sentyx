package com.sentyx.app.data.api

import io.ktor.client.HttpClient
import io.ktor.client.call.body
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.HttpStatusCode
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

    /**
     * Fetch the latest server-side status for [deviceId]. `GET {base}/v1/devices/{deviceId}`
     * with the operator bearer token. A 404 (unknown device id) throws
     * [DeviceNotFoundException] so the caller can treat it as "no data yet";
     * any other non-2xx throws [SentyxApiException].
     */
    suspend fun deviceStatus(deviceId: String): DeviceStatusDto {
        val response: HttpResponse = client.get("$base/v1/devices/$deviceId") {
            header(HttpHeaders.Authorization, "Bearer $operatorToken")
        }
        if (response.status == HttpStatusCode.NotFound) {
            throw DeviceNotFoundException(deviceId)
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Device status failed (${response.status}): ${response.bodyAsText()}",
            )
        }
        return response.body()
    }

    /** True if `GET {base}/healthz` (open, no auth) returns a success status. */
    suspend fun healthz(): Boolean =
        client.get("$base/healthz").status.isSuccess()

    /**
     * True if the operator token is accepted by an authenticated endpoint
     * (`GET {base}/usage`). Used by the diagnostics connection test.
     */
    suspend fun operatorTokenValid(): Boolean =
        client.get("$base/usage") {
            header(HttpHeaders.Authorization, "Bearer $operatorToken")
        }.status.isSuccess()

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

/** Raised for a 404 from [SentyxApi.deviceStatus]: the device id is unknown to the backend. */
class DeviceNotFoundException(deviceId: String) :
    Exception("Device not found: $deviceId")

@Serializable
private data class RegisterDeviceRequest(val deviceId: String, val name: String)

@Serializable
private data class RegisterDeviceResponse(val deviceId: String, val token: String)

/**
 * Server → app device status (`GET /v1/devices/{deviceId}`), mirroring the M6
 * heartbeat contract. [status] is the latest heartbeat exactly as received, or
 * null if the device has never reported. [online] is server-computed
 * (now - lastSeenMs < 90s).
 */
@Serializable
data class DeviceStatusDto(
    val deviceId: String,
    val name: String,
    val registeredAtMs: Long? = null,
    val online: Boolean,
    val lastSeenMs: Long? = null,
    val status: HeartbeatDto? = null,
)

/**
 * Latest heartbeat metrics from the Pi agent. Every field except the protocol
 * version is best-effort: a metric that can't be read is omitted, so all are
 * nullable — never fill a fabricated default.
 */
@Serializable
data class HeartbeatDto(
    val v: Int? = null,
    val agentVersion: String? = null,
    val uptimeSec: Long? = null,
    val storageFreeBytes: Long? = null,
    val storageTotalBytes: Long? = null,
    val cpuTempC: Double? = null,
    val throttledHex: String? = null,
    val underVoltageNow: Boolean? = null,
    val underVoltageEver: Boolean? = null,
    val wifiSsid: String? = null,
    val wifiRssiDbm: Int? = null,
    val wifiSignalPct: Int? = null,
    val uploadBacklog: Int? = null,
    val recordingNow: Boolean? = null,
    val sentryActive: Boolean? = null,
)
