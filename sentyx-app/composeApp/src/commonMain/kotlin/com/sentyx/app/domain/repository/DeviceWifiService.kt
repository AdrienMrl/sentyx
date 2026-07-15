package com.sentyx.app.domain.repository

import com.sentyx.app.domain.model.WifiNetwork
import com.sentyx.app.domain.model.WifiStatus
import kotlinx.coroutines.flow.StateFlow

/** BLE link lifecycle for a Wi-Fi management session. */
enum class WifiLinkState { Disconnected, Connecting, Connected }

/**
 * Manages the paired Pi's Wi-Fi over BLE, from the settings screen (a separate
 * connection from onboarding). Acquire the link with [connect] (scan → connect
 * the bonded Pi → authenticate for management), run the ops, then [release] it.
 * [linkState] reflects the underlying BLE connection so the UI can show the
 * connecting/lost states. Ops throw [DeviceWifiException] with a message safe to
 * show on failure (including the device's own `detail`).
 */
interface DeviceWifiService {
    val linkState: StateFlow<WifiLinkState>

    /** Scan for the bonded Pi, connect, and authenticate for management. */
    suspend fun connect()

    /** Current + saved networks. */
    suspend fun status(): WifiStatus

    /** Scan for nearby networks (deduped, strongest first). */
    suspend fun scan(): List<WifiNetwork>

    /** Join [ssid] (pass [password] = null for open networks). Can take ~60s. */
    suspend fun connectWifi(ssid: String, password: String?)

    /** Forget the saved network [ssid]. */
    suspend fun forget(ssid: String)

    /** Disconnect the BLE link and free the session. Safe to call repeatedly. */
    fun release()
}

/** A Wi-Fi management failure carrying a message safe to show in the UI. */
class DeviceWifiException(message: String, cause: Throwable? = null) :
    Exception(message, cause)
