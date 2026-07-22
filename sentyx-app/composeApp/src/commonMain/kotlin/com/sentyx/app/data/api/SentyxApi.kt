package com.sentyx.app.data.api

import io.ktor.client.HttpClient
import io.ktor.client.call.body
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.client.request.delete
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.put
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.HttpStatusCode
import io.ktor.http.contentType
import io.ktor.http.encodeURLPathPart
import io.ktor.http.isSuccess
import io.ktor.serialization.kotlinx.json.json
import com.sentyx.app.data.clip.ClipSource
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * Thin Ktor client for the Sentyx cloud backend. Uses the platform default
 * engine (OkHttp on Android, Darwin on iOS — both are on the classpath) and
 * authenticates every call with the signed-in user's Supabase access token.
 *
 * [accessToken] returns a currently-valid token (the session manager refreshes
 * proactively); if it returns null the user is signed out and the call throws.
 * On a 401 the request is retried once after [refreshToken] forces a refresh —
 * covering the window where a token expired between the proactive check and the
 * server validating it.
 */
class SentyxApi(
    baseUrl: String,
    private val accessToken: suspend () -> String?,
    private val refreshToken: suspend () -> String?,
    private val client: HttpClient = defaultClient(),
) {
    /** Base URL without a trailing slash so path concatenation is unambiguous. */
    private val base: String = baseUrl.trimEnd('/')

    /**
     * Run an authenticated request, retrying once on a 401 after forcing a token
     * refresh. [block] receives the bearer token to apply to the request.
     */
    private suspend fun authed(block: suspend (token: String) -> HttpResponse): HttpResponse {
        val token = accessToken() ?: throw SentyxApiException("Not signed in.")
        val response = block(token)
        if (response.status != HttpStatusCode.Unauthorized) return response
        val refreshed = refreshToken()
            ?: throw SentyxApiException("Session expired. Please sign in again.")
        return block(refreshed)
    }

    /**
     * Register a freshly paired Pi with the backend and return the per-device
     * ingest token it should use. `POST {base}/v1/devices` with the signed-in
     * user's bearer token; a non-2xx response throws with the status and body.
     */
    suspend fun registerDevice(deviceId: String, name: String): String {
        val response = authed { token ->
            client.post("$base/v1/devices") {
                header(HttpHeaders.Authorization, "Bearer $token")
                contentType(ContentType.Application.Json)
                setBody(RegisterDeviceRequest(deviceId = deviceId, name = name))
            }
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Device registration failed (${response.status}): ${response.bodyAsText()}",
            )
        }
        return response.body<RegisterDeviceResponse>().token
    }

    /**
     * Fetch the latest server-side status for [deviceId]. `GET {base}/v1/devices/{deviceId}`.
     * A 404 (unknown device id) throws [DeviceNotFoundException] so the caller can
     * treat it as "no data yet"; any other non-2xx throws [SentyxApiException].
     */
    suspend fun deviceStatus(deviceId: String): DeviceStatusDto {
        val response = authed { token ->
            client.get("$base/v1/devices/$deviceId") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
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

    /**
     * List all events for the signed-in user, newest-first ordering left to the
     * caller. `GET {base}/events`; a non-2xx response throws [SentyxApiException].
     */
    suspend fun events(): List<EventSummaryDto> {
        val response = authed { token ->
            client.get("$base/events") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Events list failed (${response.status}): ${response.bodyAsText()}",
            )
        }
        return response.body()
    }

    /**
     * Fetch one event with its received files. `GET {base}/events/{id}`. A 404
     * throws [SentyxApiException] like any other non-2xx (the feed is the source
     * of truth for which ids exist).
     */
    suspend fun event(id: String): EventDetailDto {
        val response = authed { token ->
            client.get("$base/events/$id") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Event fetch failed (${response.status}): ${response.bodyAsText()}",
            )
        }
        return response.body()
    }

    /**
     * Fetch the thumbnail image bytes for [id]. `GET {base}/events/{id}/thumb`;
     * returns `image/jpeg` or `image/png` bytes on success. Returns null for a
     * 404 (no thumbnail yet — event still uploading or analyzing) or any other
     * non-2xx. The id is URL-path-encoded because event ids contain `:`.
     */
    suspend fun eventThumb(id: String): ByteArray? {
        val response = authed { token ->
            client.get("$base/events/${id.encodeURLPathPart()}/thumb") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }
        if (!response.status.isSuccess()) return null
        return response.body()
    }

    /**
     * Build a playable [ClipSource] for [id]'s clip: the `GET {base}/events/{id}/clip`
     * URL plus a currently-valid bearer `Authorization` header for the platform
     * player to send on every (range) request. The id is URL-path-encoded because
     * event ids contain `:`. Unlike the JSON calls this does not fetch anything —
     * the player streams the URL directly — so there is no 401 retry here; a
     * signed-out session throws [SentyxApiException]. Whether a clip actually
     * exists is decided at playback time (the endpoint 404s until one is uploaded).
     */
    suspend fun clipSource(id: String): ClipSource {
        val token = accessToken() ?: throw SentyxApiException("Not signed in.")
        return ClipSource(
            url = "$base/events/${id.encodeURLPathPart()}/clip",
            headers = mapOf(HttpHeaders.Authorization to "Bearer $token"),
        )
    }

    /**
     * Register (upsert) a push token for the signed-in user.
     * `PUT {base}/v1/me/push-tokens` with `{"token","platform"}`; expects 204.
     * A non-success response throws [SentyxApiException].
     */
    suspend fun registerPushToken(token: String, platform: String) {
        val response = authed { t ->
            client.put("$base/v1/me/push-tokens") {
                header(HttpHeaders.Authorization, "Bearer $t")
                contentType(ContentType.Application.Json)
                setBody(PushTokenRequest(token = token, platform = platform))
            }
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Push token registration failed (${response.status}): ${response.bodyAsText()}",
            )
        }
    }

    /**
     * Remove a push token for the signed-in user (called on sign-out, while the
     * session is still valid). `DELETE {base}/v1/me/push-tokens/{token}`; the
     * token is URL-path-encoded. A 404 (already gone) is treated as success; any
     * other non-success throws [SentyxApiException].
     */
    suspend fun deletePushToken(token: String) {
        val response = authed { t ->
            client.delete("$base/v1/me/push-tokens/${token.encodeURLPathPart()}") {
                header(HttpHeaders.Authorization, "Bearer $t")
            }
        }
        if (!response.status.isSuccess() && response.status != HttpStatusCode.NotFound) {
            throw SentyxApiException(
                "Push token delete failed (${response.status}): ${response.bodyAsText()}",
            )
        }
    }

    /**
     * Fetch the signed-in user's push threshold.
     * `GET {base}/v1/me/notification-settings` → `{"min_threat_level": "..."}`.
     * A non-success response throws [SentyxApiException].
     */
    suspend fun getNotificationSettings(): NotificationSettingsDto {
        val response = authed { t ->
            client.get("$base/v1/me/notification-settings") {
                header(HttpHeaders.Authorization, "Bearer $t")
            }
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Notification settings fetch failed (${response.status}): ${response.bodyAsText()}",
            )
        }
        return response.body()
    }

    /**
     * Update the signed-in user's push threshold.
     * `PUT {base}/v1/me/notification-settings` with `{"min_threat_level"}`;
     * expects 204. A non-success response throws [SentyxApiException].
     */
    suspend fun putNotificationSettings(minThreatLevel: String) {
        val response = authed { t ->
            client.put("$base/v1/me/notification-settings") {
                header(HttpHeaders.Authorization, "Bearer $t")
                contentType(ContentType.Application.Json)
                setBody(NotificationSettingsDto(minThreatLevel = minThreatLevel))
            }
        }
        if (!response.status.isSuccess()) {
            throw SentyxApiException(
                "Notification settings update failed (${response.status}): ${response.bodyAsText()}",
            )
        }
    }

    /** True if `GET {base}/healthz` (open, no auth) returns a success status. */
    suspend fun healthz(): Boolean =
        client.get("$base/healthz").status.isSuccess()

    /**
     * True if the signed-in user's session is accepted by an authenticated
     * endpoint (`GET {base}/events` — user-accessible; `/usage` is
     * operator-only). Used by the diagnostics connection test.
     */
    suspend fun sessionValid(): Boolean =
        authed { token ->
            client.get("$base/events") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
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

/** App → server push-token upsert body (`PUT /v1/me/push-tokens`). */
@Serializable
private data class PushTokenRequest(val token: String, val platform: String)

/**
 * Push threshold document for `GET`/`PUT /v1/me/notification-settings`. The JSON
 * key is the server's snake_case `min_threat_level`; the value is one of
 * `off`/`none`/`low`/`medium`/`high` (see [com.sentyx.app.domain.model.MinThreatLevel]).
 */
@Serializable
data class NotificationSettingsDto(
    @SerialName("min_threat_level") val minThreatLevel: String,
)

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

/**
 * Server → app event summary (`GET /events`), mirroring the server's
 * `EventSummary`. Field names are the server's snake_case JSON keys. Unknown
 * keys are ignored by the shared codec, and every non-identifying field is
 * defaulted so a sparse row (e.g. still uploading, not yet analyzed) decodes.
 *
 * [analysisJson] is the raw Gemini verdict document as a string (parsed
 * separately by the mapping layer); [eventTs] is the car's local wall-clock
 * ("2006-01-02T15:04:05", no zone) and [firstSeen] is the server-side receipt
 * instant (RFC3339) used for ordering.
 */
@Serializable
data class EventSummaryDto(
    val id: String,
    @SerialName("first_seen") val firstSeen: String? = null,
    @SerialName("last_file_at") val lastFileAt: String? = null,
    @SerialName("completed_at") val completedAt: String? = null,
    @SerialName("event_ts") val eventTs: String = "",
    val city: String = "",
    val reason: String = "",
    val camera: String = "",
    @SerialName("analysis_state") val analysisState: String,
    @SerialName("analyzed_clip") val analyzedClip: String = "",
    @SerialName("threat_level") val threatLevel: String = "",
    @SerialName("analysis_json") val analysisJson: String = "",
    @SerialName("analysis_error") val analysisError: String = "",
    @SerialName("file_count") val fileCount: Int = 0,
    val state: String = "",
    val generation: Int = 0,
    @SerialName("device_id") val deviceId: String = "",
    @SerialName("source_event_id") val sourceEventId: String = "",
)

/** One received file for an event (`GET /events/{id}` → `files[]`). */
@Serializable
data class FileInfoDto(
    val name: String,
    val size: Long = 0,
    val sha256: String = "",
    @SerialName("stored_path") val storedPath: String = "",
    @SerialName("received_at") val receivedAt: String? = null,
)

/** Server → app single-event view: the summary plus its files. */
@Serializable
data class EventDetailDto(
    val id: String,
    @SerialName("first_seen") val firstSeen: String? = null,
    @SerialName("last_file_at") val lastFileAt: String? = null,
    @SerialName("completed_at") val completedAt: String? = null,
    @SerialName("event_ts") val eventTs: String = "",
    val city: String = "",
    val reason: String = "",
    val camera: String = "",
    @SerialName("analysis_state") val analysisState: String,
    @SerialName("analyzed_clip") val analyzedClip: String = "",
    @SerialName("threat_level") val threatLevel: String = "",
    @SerialName("analysis_json") val analysisJson: String = "",
    @SerialName("analysis_error") val analysisError: String = "",
    @SerialName("file_count") val fileCount: Int = 0,
    val state: String = "",
    val generation: Int = 0,
    @SerialName("device_id") val deviceId: String = "",
    @SerialName("source_event_id") val sourceEventId: String = "",
    val files: List<FileInfoDto> = emptyList(),
) {
    /** The summary portion, so mapping can treat detail and list rows alike. */
    fun toSummary(): EventSummaryDto = EventSummaryDto(
        id = id,
        firstSeen = firstSeen,
        lastFileAt = lastFileAt,
        completedAt = completedAt,
        eventTs = eventTs,
        city = city,
        reason = reason,
        camera = camera,
        analysisState = analysisState,
        analyzedClip = analyzedClip,
        threatLevel = threatLevel,
        analysisJson = analysisJson,
        analysisError = analysisError,
        fileCount = fileCount,
        state = state,
        generation = generation,
        deviceId = deviceId,
        sourceEventId = sourceEventId,
    )
}
