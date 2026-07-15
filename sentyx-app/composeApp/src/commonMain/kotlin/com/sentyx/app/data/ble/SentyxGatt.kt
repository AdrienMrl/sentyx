package com.sentyx.app.data.ble

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * On-wire GATT contract for the Sentyx Pi pairing peripheral: service /
 * characteristic UUIDs, the JSON DTOs exchanged over them, and the framing
 * used to chunk the (potentially large) Config payload across BLE writes.
 *
 * The Pi advertises [SERVICE_UUID] with a LocalName like "Sentyx-Pi4" and
 * exposes four characteristics:
 *  - [DEVICE_INFO_UUID]  read, plaintext   → [DeviceInfoDto]
 *  - [CONTROL_UUID]      write, encrypted  ← [ControlOp] (triggers Just-Works bonding)
 *  - [STATUS_UUID]       read+notify, enc. → [StatusDto]
 *  - [CONFIG_UUID]       write, encrypted, FRAMED ← [ConfigDto] (see [chunkConfig])
 */
object SentyxGatt {
    const val SERVICE_UUID = "7a65f000-53e1-4b2e-9f5a-1c29b3e60001"
    const val DEVICE_INFO_UUID = "7a65f001-53e1-4b2e-9f5a-1c29b3e60001"
    const val CONTROL_UUID = "7a65f002-53e1-4b2e-9f5a-1c29b3e60001"
    const val STATUS_UUID = "7a65f003-53e1-4b2e-9f5a-1c29b3e60001"
    const val CONFIG_UUID = "7a65f004-53e1-4b2e-9f5a-1c29b3e60001"

    /**
     * Shared JSON codec: tolerant of unknown/extra fields the device may add,
     * writes the protocol-version default (`v = 1`) explicitly, and omits null
     * optionals so "empty optionals" never appear on the wire.
     */
    val json: Json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = true
        explicitNulls = false
    }

    // ---- Config framing -----------------------------------------------------

    /** Header byte marking the only frame of a single-frame payload. */
    const val FRAME_SINGLE: Byte = 0x03

    /** Header byte marking the first frame of a multi-frame payload. */
    const val FRAME_FIRST: Byte = 0x01

    /** Header byte marking a middle frame of a multi-frame payload. */
    const val FRAME_CONTINUATION: Byte = 0x00

    /** Header byte marking the last frame of a multi-frame payload. */
    const val FRAME_LAST: Byte = 0x02

    /**
     * When the negotiated ATT MTU is unknown, assume the BLE default 23-byte
     * MTU: 23 − 3 (ATT write header) = 20 bytes per write.
     */
    const val DEFAULT_MAX_FRAME: Int = 20

    /**
     * Largest total frame size (header + payload) the protocol permits, even
     * when a large MTU is negotiated.
     */
    const val MAX_FRAME_CAP: Int = 180

    /**
     * Total frame size (header byte + payload bytes) to use for a given
     * negotiated ATT [mtu], or the [DEFAULT_MAX_FRAME] fallback when the MTU is
     * unknown. Matches the device rule `min(mtu - 3, 180)`.
     */
    fun maxFrameFor(mtu: Int?): Int =
        if (mtu == null) DEFAULT_MAX_FRAME else minOf(mtu - 3, MAX_FRAME_CAP)

    /**
     * Split [payload] into framed BLE writes. Each returned frame is
     * `[header byte] + up to (maxFrame - 1) payload bytes`:
     *  - a payload that fits in one frame → a single [FRAME_SINGLE] frame
     *    (this includes the empty payload → exactly `[0x03]`),
     *  - otherwise → [FRAME_FIRST], zero or more [FRAME_CONTINUATION], and a
     *    final [FRAME_LAST] frame.
     */
    fun chunkConfig(payload: ByteArray, maxFrame: Int): List<ByteArray> {
        require(maxFrame >= 2) { "maxFrame must leave room for a header + a payload byte, was $maxFrame" }
        val perFrame = maxFrame - 1
        if (payload.size <= perFrame) {
            return listOf(byteArrayOf(FRAME_SINGLE) + payload)
        }
        val frames = ArrayList<ByteArray>()
        var offset = 0
        while (offset < payload.size) {
            val end = minOf(offset + perFrame, payload.size)
            val isFirst = offset == 0
            val isLast = end == payload.size
            val header = when {
                isFirst -> FRAME_FIRST
                isLast -> FRAME_LAST
                else -> FRAME_CONTINUATION
            }
            frames += byteArrayOf(header) + payload.copyOfRange(offset, end)
            offset = end
        }
        return frames
    }
}

// ---- DTOs -------------------------------------------------------------------

/** DeviceInfo characteristic (read, plaintext). */
@Serializable
data class DeviceInfoDto(
    val v: Int = 1,
    val deviceId: String,
    val hw: String? = null,
    val agent: String? = null,
    val provisioned: Boolean? = null,
    val state: String? = null,
)

/**
 * Status characteristic (read + notify). `state` is one of
 * idle|authenticated|config_saved|testing|test_step|done|locked. Writing
 * `begin_pair` transitions the device straight to `authenticated` (Just Works
 * bonding; no separate code-confirmation step).
 * For state `test_step`, [step] is "healthz"|"auth" and [ok]/[detail] describe
 * that individual check. Any rejected write also produces a notify with
 * [ok] = false and a human-readable [detail].
 */
@Serializable
data class StatusDto(
    val v: Int = 1,
    val state: String,
    val step: String? = null,
    val ok: Boolean? = null,
    val detail: String? = null,
)

/**
 * Config characteristic payload (written framed via [SentyxGatt.chunkConfig]).
 * Optional fields ([timezone], [vehicleModel], [nickname]) are dropped from the
 * JSON when null (see [SentyxGatt.json]).
 */
@Serializable
data class ConfigDto(
    val v: Int = 1,
    val serverUrl: String,
    val token: String,
    val deviceName: String,
    val timezone: String? = null,
    val vehicleModel: String? = null,
    val nickname: String? = null,
)

/** Control characteristic command (write). */
@Serializable
data class ControlOp(
    val op: String,
) {
    companion object {
        fun beginPair() = ControlOp("begin_pair")
        fun test() = ControlOp("test")
        fun complete() = ControlOp("complete")
    }
}
